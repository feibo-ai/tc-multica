package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// codexSource is the collector / provider id for Codex CLI rollout transcripts.
const codexSource = "codex"

// codexCollector scans ~/.codex/{sessions,archived_sessions}/**/rollout-*.jsonl
// for per-session cumulative token usage. It is the second concrete Collector;
// it mirrors claudeCollector — adding it changed nothing in the framework, only
// appended one entry to ambientCollectors.
//
// Counting model differs from Claude. Codex emits a `token_count` event whose
// `info.total_token_usage` is a per-session RUNNING TOTAL (cumulative), and the
// same total is typically emitted multiple times per turn. So we never sum
// events — per session we take the FINAL/max cumulative seen this scan and emit
// the DELTA against the watermarked cumulative. This is the only counting path.
//
// Privacy doctrine (decisions/2026-06-03-local-log-privacy.md): a rollout line
// is decoded ONLY into codexRolloutLine, whose fields are numbers and ids. The
// prompt / cwd / instructions keys on the line are never named, so encoding/json
// never populates them — message content is structurally unable to leave here.
type codexCollector struct {
	logger *slog.Logger
	root   string // ~/.codex; overridable in tests
}

func newCodexCollector(logger *slog.Logger) *codexCollector {
	root := ""
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, ".codex")
	}
	return &codexCollector{logger: logger, root: root}
}

func (c *codexCollector) Source() string { return codexSource }

// codexFileState is the per-file watermark: how far we have read and the stat we
// last saw, so an unchanged file is skipped without opening it. Mirrors
// claudeFileState.
type codexFileState struct {
	Offset    int64 `json:"offset"`
	MTimeNano int64 `json:"mtime_nano"`
	Size      int64 `json:"size"`
}

// codexSessionWatermark is the last-reported cumulative tuple for one session.
// The next scan's emitted delta is (current final cumulative − this).
type codexSessionWatermark struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	CacheRead int64 `json:"cache_read"`
}

// codexState is the collector's opaque, serialized watermark. Seeded gates the
// forward-only contract: the very first scan records every existing session's
// current cumulative WITHOUT emitting (we do not backfill history — plan
// decision "只向前"); only growth after that, and sessions created after that,
// are reported.
type codexState struct {
	Seeded   bool                             `json:"seeded"`
	Files    map[string]codexFileState        `json:"files"`
	Sessions map[string]codexSessionWatermark `json:"sessions"`
}

// codexRolloutLine is the ONLY shape a rollout line is decoded into — numbers
// and ids, never content. Mirrors the codexSessionTokenCount struct in
// pkg/agent/codex.go (copied here to avoid exporting pkg/agent internals), with
// the session_meta id and a top-level timestamp added.
type codexRolloutLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   *struct {
		Type string `json:"type"`
		ID   string `json:"id"` // session_meta: the session UUID
		Info *struct {
			TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
			LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
			Model           string           `json:"model"`
		} `json:"info"`
		Model string `json:"model"` // turn_context: the model
	} `json:"payload"`
}

// codexTokenUsage mirrors the token tuple in pkg/agent/codex.go.
type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheReadInputTokens  int64 `json:"cache_read_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexSessionScan accumulates the per-session facts gathered from one or more
// rollout files during a single scan, before deltas are computed.
type codexSessionScan struct {
	id      string
	model   string // turn_context.payload.model (precedence) or token_count info.model
	hasCum  bool
	input   int64 // final/max cumulative this scan
	output  int64
	cache   int64
	eventAt string // timestamp of the LAST token_count event observed
	// active marks a session seen under sessions/ (vs archived_sessions/). When a
	// session UUID appears in both dirs, the active copy wins.
	active bool
}

func (c *codexCollector) Scan(ctx context.Context, prevState json.RawMessage) ([]AmbientUsageEntry, json.RawMessage, error) {
	state := codexState{
		Files:    map[string]codexFileState{},
		Sessions: map[string]codexSessionWatermark{},
	}
	if len(prevState) > 0 {
		_ = json.Unmarshal(prevState, &state) // corrupt state → treat as first run
		if state.Files == nil {
			state.Files = map[string]codexFileState{}
		}
		if state.Sessions == nil {
			state.Sessions = map[string]codexSessionWatermark{}
		}
	}
	firstRun := !state.Seeded

	// Per-session accumulation across both subtrees. A session resume can write a
	// new file but Codex keys cumulative usage per session UUID, so we fold all
	// files of a session into one final cumulative.
	sessions := map[string]*codexSessionScan{}
	present := map[string]struct{}{}

	for _, sub := range []string{"sessions", "archived_sessions"} {
		if c.root == "" {
			break
		}
		active := sub == "sessions"
		dir := filepath.Join(c.root, sub)
		// WalkDir safety contract, mirrored from ambient_claude.go / diskusage.go:
		// never descend into .git, never follow symlinks, only regular
		// rollout-*.jsonl files.
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry → skip, never fatal
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
			name := entry.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
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
				// Forward-only seed: parse to capture the session's current
				// cumulative as the watermark, then remember the file end and
				// emit nothing (the seed is committed via the sessions map
				// after the walk).
				c.scanFile(path, active, sessions)
				state.Files[path] = codexFileState{Offset: size, MTimeNano: mtime, Size: size}
				return nil
			}
			if known && prev.MTimeNano == mtime && prev.Size == size {
				// Unchanged file: still register the session's known cumulative so
				// the post-walk delta uses the true current total (a session can
				// span multiple files; an unchanged file still contributes its
				// final cumulative).
				c.scanFile(path, active, sessions)
				state.Files[path] = prev
				return nil
			}

			c.scanFile(path, active, sessions)
			state.Files[path] = codexFileState{Offset: size, MTimeNano: mtime, Size: size}
			return nil
		})
	}

	// Drop watermarks for files that no longer exist so the map stays bounded.
	for path := range state.Files {
		if _, ok := present[path]; !ok {
			delete(state.Files, path)
		}
	}

	var entries []AmbientUsageEntry
	liveSessions := map[string]struct{}{}
	for id, s := range sessions {
		liveSessions[id] = struct{}{}
		if !s.hasCum {
			continue // no token_count seen → nothing to watermark or emit
		}
		// Skip a session the server would reject anyway (empty model or id).
		if s.id == "" || s.model == "" {
			continue
		}

		if firstRun {
			// HIGHEST-RISK PATH: seed at the CURRENT final cumulative, never zero.
			// Seeding at zero would make the first post-seed scan emit the whole
			// session history as one delta.
			state.Sessions[id] = codexSessionWatermark{Input: s.input, Output: s.output, CacheRead: s.cache}
			continue
		}

		prev := state.Sessions[id] // zero value for a never-seen (new) session
		dInput := s.input - prev.Input
		dOutput := s.output - prev.Output
		dCache := s.cache - prev.CacheRead
		// Negative deltas shouldn't happen (cumulative is monotonic) but clamp
		// defensively so a reset/rotation can't emit a negative token count.
		if dInput < 0 {
			dInput = 0
		}
		if dOutput < 0 {
			dOutput = 0
		}
		if dCache < 0 {
			dCache = 0
		}
		if dInput > 0 || dOutput > 0 || dCache > 0 {
			total := s.input + s.output + s.cache
			entries = append(entries, AmbientUsageEntry{
				SessionID: s.id,
				MessageID: s.id,
				// Monotonic cumulative total → re-scan same total = same key =
				// server ON CONFLICT DO NOTHING; a new turn = larger total = new
				// key = new row.
				RequestID:        "cum:" + strconv.FormatInt(total, 10),
				Provider:         codexSource,
				Model:            s.model,
				EventAt:          s.eventAt,
				InputTokens:      dInput,
				OutputTokens:     dOutput,
				CacheReadTokens:  dCache,
				CacheWriteTokens: 0, // Codex has no separate cache-write signal
				Source:           codexSource,
			})
		}
		// Advance the watermark to the current cumulative regardless of whether a
		// positive delta was emitted (idempotent re-scans then see delta 0).
		state.Sessions[id] = codexSessionWatermark{Input: s.input, Output: s.output, CacheRead: s.cache}
	}

	// Drop watermarks for sessions whose files all disappeared, to keep state
	// bounded. SAFE for the normal flows: archiving moves a file
	// sessions/→archived_sessions/ but BOTH dirs are walked every scan, so the
	// session UUID never leaves liveSessions and its watermark survives the move
	// (TestCodexCollector_ArchiveMovePreservesWatermark); a permanently-deleted
	// session never reappears. KNOWN v1 EDGE: if a UUID vanishes from both dirs
	// for a full scan and later reappears (external move-out-and-back, or a
	// non-atomic archive caught mid-scan), it re-seeds at prev=0 and re-emits its
	// cumulative once — a bounded over-count. Accepted over unbounded state
	// growth; revisit with a grace-TTL if it ever bites in practice.
	for id := range state.Sessions {
		if _, ok := liveSessions[id]; !ok {
			delete(state.Sessions, id)
		}
	}

	state.Seeded = true
	next, err := json.Marshal(state)
	if err != nil {
		return nil, prevState, err
	}
	return entries, next, nil
}

// scanFile reads a whole rollout file and folds its facts into the per-session
// accumulator. It always reads from the start: the cumulative model means we
// need the session's CURRENT final total, not a tail delta — re-reading is cheap
// and the per-file mtime/size guard already skips truly unchanged files. Per-line
// failures are skipped, never fatal.
func (c *codexCollector) scanFile(path string, active bool, sessions map[string]*codexSessionScan) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	var (
		sessionID  string
		model      string // from turn_context (precedence)
		infoModel  string // from token_count info.model (fallback)
		hasCum     bool
		curInput   int64
		curOutput  int64
		curCache   int64
		lastEvtAt  string
		sawSession bool
	)

	for scanner.Scan() {
		raw := scanner.Bytes()
		// Fast pre-filter: only three event kinds carry anything we read.
		if !bytesHasCodexKeyword(raw) {
			continue
		}
		var line codexRolloutLine
		if err := json.Unmarshal(raw, &line); err != nil || line.Payload == nil {
			continue
		}
		switch {
		case line.Type == "session_meta" && line.Payload.ID != "":
			sessionID = line.Payload.ID
			sawSession = true
		case line.Type == "turn_context" && line.Payload.Model != "":
			model = line.Payload.Model
		case line.Payload.Type == "token_count" && line.Payload.Info != nil:
			u := line.Payload.Info.TotalTokenUsage
			if u == nil {
				u = line.Payload.Info.LastTokenUsage
			}
			if u == nil {
				continue // the empty "info":null warm-up event carries no usage
			}
			cache := u.CachedInputTokens
			if cache == 0 {
				cache = u.CacheReadInputTokens
			}
			// Cumulative is monotonic; keep the max so out-of-order or duplicate
			// lines can't lower the running total.
			if u.InputTokens >= curInput {
				curInput = u.InputTokens
			}
			if out := u.OutputTokens + u.ReasoningOutputTokens; out >= curOutput {
				curOutput = out
			}
			if cache >= curCache {
				curCache = cache
			}
			if line.Payload.Info.Model != "" {
				infoModel = line.Payload.Info.Model
			}
			if line.Timestamp != "" {
				lastEvtAt = line.Timestamp
			}
			hasCum = true
		}
	}

	if !sawSession || sessionID == "" {
		return // can't attribute usage without a session UUID
	}

	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = infoModel
	}

	s := sessions[sessionID]
	if s == nil {
		s = &codexSessionScan{id: sessionID}
		sessions[sessionID] = s
	}
	// Active (sessions/) wins over archived for the model/usage source when the
	// same session id appears in both dirs.
	if active && !s.active {
		s.active = true
	}
	preferActive := active || !s.active

	if resolvedModel != "" && (preferActive || s.model == "") {
		s.model = resolvedModel
	}
	if hasCum {
		// Fold this file's cumulative into the session max (a resumed session may
		// span files; the true session total is the largest cumulative seen).
		s.input = max64(s.input, curInput)
		s.output = max64(s.output, curOutput)
		s.cache = max64(s.cache, curCache)
		// EventAt tracks the latest token_count timestamp across the session's
		// files; prefer a non-empty one, later wins lexically (RFC3339 sorts).
		if lastEvtAt != "" && lastEvtAt >= s.eventAt {
			s.eventAt = lastEvtAt
		}
		s.hasCum = true
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// bytesHasCodexKeyword is the cheap pre-filter mirroring parseCodexSessionFile:
// only session_meta, turn_context, and token_count lines carry anything we read.
func bytesHasCodexKeyword(line []byte) bool {
	return bytes.Contains(line, []byte("token_count")) ||
		bytes.Contains(line, []byte("turn_context")) ||
		bytes.Contains(line, []byte("session_meta"))
}
