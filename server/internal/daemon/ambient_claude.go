package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// claudeSource is the collector / provider id for Claude Code transcripts.
const claudeSource = "claude"

// claudeCollector scans ~/.claude/projects/**/*.jsonl for assistant-message
// token usage. It is the first concrete Collector; adding another CLI means
// writing a sibling that implements Collector, nothing here changes.
//
// Privacy doctrine (decisions/2026-06-03-local-log-privacy.md): a transcript
// line is decoded ONLY into claudeLine, whose fields are numbers and ids. The
// `content` / `cwd` / `gitBranch` / … keys on the line are never named, so
// encoding/json never populates them — message content is structurally unable
// to leave this function.
type claudeCollector struct {
	logger *slog.Logger
	root   string // ~/.claude/projects; overridable in tests
}

func newClaudeCollector(logger *slog.Logger) *claudeCollector {
	root := ""
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, ".claude", "projects")
	}
	return &claudeCollector{logger: logger, root: root}
}

func (c *claudeCollector) Source() string { return claudeSource }

// claudeFileState is the per-file watermark: how far we have read and the
// stat we last saw, so an unchanged file is skipped without opening it.
type claudeFileState struct {
	Offset    int64 `json:"offset"`
	MTimeNano int64 `json:"mtime_nano"`
	Size      int64 `json:"size"`
}

// claudeState is the collector's opaque, serialized watermark. Seeded gates the
// forward-only contract: the very first scan records every existing file's
// current end WITHOUT emitting (we do not backfill years of history — plan
// decision "只向前"); only content appended after that, and files created after
// that, are reported.
type claudeState struct {
	Seeded bool                       `json:"seeded"`
	Files  map[string]claudeFileState `json:"files"`
}

// claudeLine is the ONLY shape a transcript line is decoded into — numbers and
// ids, never content. See the privacy note on claudeCollector.
type claudeLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (c *claudeCollector) Scan(ctx context.Context, prevState json.RawMessage) ([]AmbientUsageEntry, json.RawMessage, error) {
	state := claudeState{Files: map[string]claudeFileState{}}
	if len(prevState) > 0 {
		_ = json.Unmarshal(prevState, &state) // corrupt state → treat as first run
		if state.Files == nil {
			state.Files = map[string]claudeFileState{}
		}
	}
	firstRun := !state.Seeded

	var entries []AmbientUsageEntry
	// Dedup within a scan by (message.id, requestId): a transcript repeats the
	// same assistant line many times (empirically up to ~33x), so a naive sum
	// over-counts. The server's unique key is authoritative across scans; this
	// trims the upload volume (坑#1).
	seen := map[string]struct{}{}
	present := map[string]struct{}{}

	if c.root != "" {
		// WalkDir safety contract, mirrored from diskusage.go: never descend
		// into .git, never follow symlinks, only regular *.jsonl files.
		_ = filepath.WalkDir(c.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry → skip, never fatal (坑#3)
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			present[path] = struct{}{}

			size := info.Size()
			mtime := info.ModTime().UnixNano()
			prev, known := state.Files[path]

			if firstRun && !known {
				// Forward-only seed: remember the current end, emit nothing.
				state.Files[path] = claudeFileState{Offset: size, MTimeNano: mtime, Size: size}
				return nil
			}
			if known && prev.MTimeNano == mtime && prev.Size == size {
				return nil // unchanged since last scan
			}

			start := int64(0)
			if known {
				start = prev.Offset
				if start > size {
					start = 0 // truncated / rotated → re-read (server dedups)
				}
			}

			newOffset, fileEntries := c.tailFile(path, start, seen)
			entries = append(entries, fileEntries...)
			state.Files[path] = claudeFileState{Offset: newOffset, MTimeNano: mtime, Size: size}
			return nil
		})
	}

	// Drop watermarks for files that no longer exist so the map stays bounded
	// to the live transcript set.
	for path := range state.Files {
		if _, ok := present[path]; !ok {
			delete(state.Files, path)
		}
	}

	state.Seeded = true
	next, err := json.Marshal(state)
	if err != nil {
		return nil, prevState, err
	}
	return entries, next, nil
}

// tailFile reads [start, EOF) of a transcript, parses complete lines into usage
// entries, and returns the offset of the last COMPLETE line so a half-written
// trailing line (an actively-appending session) is re-read next scan rather
// than parsed truncated and lost. Per-line failures are skipped, never fatal.
func (c *claudeCollector) tailFile(path string, start int64, seen map[string]struct{}) (int64, []AmbientUsageEntry) {
	f, err := os.Open(path)
	if err != nil {
		return start, nil
	}
	defer f.Close()

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return start, nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return start, nil
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return start, nil // no complete line yet
	}
	complete := data[:lastNL+1]
	newOffset := start + int64(lastNL) + 1

	var entries []AmbientUsageEntry
	for _, raw := range bytes.Split(complete, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if entry, ok := parseClaudeUsageLine(raw); ok {
			key := entry.MessageID + "\x00" + entry.RequestID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			entries = append(entries, entry)
		}
	}
	return newOffset, entries
}

// parseClaudeUsageLine decodes one line into the numbers-only claudeLine and,
// if it is a real assistant usage row, returns the wire entry. ok=false drops
// the line (non-assistant, synthetic warm-up, missing dedup key / timestamp,
// or all-zero usage), which the caller skips fail-soft.
func parseClaudeUsageLine(raw []byte) (AmbientUsageEntry, bool) {
	var line claudeLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return AmbientUsageEntry{}, false
	}
	if line.Type != "assistant" {
		return AmbientUsageEntry{}, false
	}
	// N3: skip Claude's "<synthetic>" warm-up messages (always zero usage).
	if line.Message.Model == "" || line.Message.Model == "<synthetic>" {
		return AmbientUsageEntry{}, false
	}
	// Need the full dedup key and a bucket timestamp.
	if line.SessionID == "" || line.Message.ID == "" || line.RequestID == "" || line.Timestamp == "" {
		return AmbientUsageEntry{}, false
	}
	u := line.Message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return AmbientUsageEntry{}, false // no signal
	}
	return AmbientUsageEntry{
		SessionID:        line.SessionID,
		MessageID:        line.Message.ID,
		RequestID:        line.RequestID,
		Provider:         claudeSource,
		Model:            line.Message.Model,
		EventAt:          line.Timestamp,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		Source:           claudeSource,
	}, true
}
