package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Indirections over the real cli skill-sync primitives so the loop can be
// exercised deterministically in tests without reaching out to GitHub or
// touching the real ~/.claude tree. Mirrors the pattern at the top of
// auto_update.go (fetchLatestRelease / isReleaseVersion / isNewerVersion).
var (
	fetchLatestSkillBundle       = cli.FetchLatestSkillBundle
	skillWriteGuard              = cli.SkillWriteGuard
	verifySkillBundleAttestation = cli.VerifySkillBundleAttestation
	extractSkillBundleSafely     = cli.ExtractSkillBundleSafely
	skillBundleIsNewerVersion    = cli.IsNewerVersion
)

// skillSyncTimeout bounds each outbound call the loop makes (releases fetch +
// bundle download in FetchLatestSkillBundle, and the attestation fetch in
// VerifySkillBundleAttestation). Same order of magnitude as the auto-update
// download timeout; a bundle is small, so a generous-but-finite ceiling keeps a
// hung connection from wedging the loop until the next tick.
var skillSyncTimeout = 120 * time.Second

// skillSyncLoop periodically pulls the team skill bundle from team-context,
// verifies its provenance attestation, and writes it into ~/.claude — but only
// on machines that have explicitly opted in (SkillWriteEnabled) and are not a
// developer's git-symlink box (SkillWriteGuard). It is the orchestration layer
// over the already-tested cli primitives (mini-ADR v4 ⑩c); it adds no new
// security logic of its own.
//
// Disabled / skipped when:
//   - SkillWriteEnabled is false (default everywhere — pure opt-in);
//   - SkillWriteGuard rejects the machine (skills/ is a git symlink layout, i.e.
//     a dev box where the daemon writing files would clobber the symlinks).
//
// Structure deliberately mirrors autoUpdateLoop: log gate, guard, initial delay,
// one immediate attempt, then a ticker until ctx is cancelled.
func (d *Daemon) skillSyncLoop(ctx context.Context) {
	if !d.cfg.SkillWriteEnabled {
		d.logger.Info("skill-sync: disabled")
		return
	}

	claudeDir, err := claudeDirForSkillSync()
	if err != nil {
		d.logger.Info("skill-sync: skipped", "reason", err)
		return
	}

	// Mutual-exclusion guard: dev boxes (skills are git symlinks) are refused so
	// the write loop never clobbers the symlink layout (invariant #6). A guard
	// error means "this machine syncs skills another way" — skip the loop, don't
	// treat it as a failure.
	if err := skillWriteGuard(claudeDir); err != nil {
		d.logger.Info("skill-sync: skipped", "reason", err)
		return
	}

	interval := d.cfg.SkillSyncCheckInterval
	if interval <= 0 {
		interval = DefaultAutoUpdateCheckInterval
	}
	d.logger.Info("skill-sync: started", "interval", interval, "claude_dir", claudeDir)

	if err := sleepWithContext(ctx, autoUpdateInitialDelay); err != nil {
		return
	}
	d.trySkillSync(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.trySkillSync(ctx)
		}
	}
}

// trySkillSync runs one fetch → anti-rollback → verify → write cycle. It never
// returns an error and never panics or exits the process: every failure mode is
// a fail-closed refusal plus a local log line, retried at the next tick. This
// matches the tryAutoUpdate philosophy — a transient blip must not escalate to a
// process-level event, and a security failure (bad attestation, unsafe write)
// must refuse with no fallback.
func (d *Daemon) trySkillSync(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	claudeDir, err := claudeDirForSkillSync()
	if err != nil {
		d.logger.Warn("skill-sync: resolve claude dir failed — will retry", "error", err)
		return
	}

	tag, bundleBytes, err := fetchLatestSkillBundle(skillSyncTimeout)
	if err != nil {
		d.logger.Warn("skill-sync: fetch failed — will retry", "error", err)
		return
	}

	// Anti-rollback (invariant 3.1): never apply a bundle whose version is <=
	// the version already on disk. Read the persisted tag and compare semantic
	// versions (after stripping the skills-v prefix). On a tie or downgrade,
	// skip silently — this is the steady state once the machine is current.
	current := d.readSkillSyncVersion()
	if !skillBundleVersionIsNewer(tag, current) {
		d.logger.Debug("skill-sync: already current — skipping", "fetched", tag, "current", current)
		return
	}

	// Provenance verification is fail-closed with no fallback (invariant #3):
	// if the attestation does not verify, refuse and do NOT write.
	if err := verifySkillBundleAttestation(bundleBytes, skillSyncTimeout); err != nil {
		d.logger.Error("skill-sync: attestation verification failed — refusing (fail-closed)", "error", err)
		return
	}

	written, err := extractSkillBundleSafely(bundleBytes, claudeDir)
	if err != nil {
		// Write failures land as a LOCAL alert only (invariant #9: alerts stay
		// local, never reported back to the server).
		d.logger.Error("skill-sync: write failed", "error", err)
		return
	}

	if err := d.writeSkillSyncVersion(tag); err != nil {
		// The bundle is already applied; failing to persist the version only
		// means we re-verify (and idempotently re-write) the same bundle next
		// tick. Warn, don't fail the cycle.
		d.logger.Warn("skill-sync: applied but failed to persist version", "tag", tag, "error", err)
	}
	d.logger.Info("skill-sync: applied", "tag", tag, "files", len(written))
}

// claudeDirForSkillSync resolves the consumer-machine ~/.claude directory the
// bundle is written into.
func claudeDirForSkillSync() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// skillSyncVersionPath is the persisted "last applied skill bundle tag" file:
// ~/.multica/skill-sync-version. It stores only the tag string (e.g.
// "skills-v0.3.1"). Used for the anti-rollback comparison.
func skillSyncVersionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".multica", "skill-sync-version"), nil
}

// readSkillSyncVersion returns the persisted last-applied tag, or "" if none has
// been recorded yet (or the file can't be read — treated as "nothing applied",
// which still leaves the verify step as the gate before any write).
func (d *Daemon) readSkillSyncVersion() string {
	path, err := skillSyncVersionPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeSkillSyncVersion atomically persists the applied tag.
func (d *Daemon) writeSkillSyncVersion(tag string) error {
	path, err := skillSyncVersionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(tag)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// skillBundleVersionIsNewer reports whether the fetched skill bundle tag is a
// strictly newer release than the currently-applied tag. An empty current means
// nothing has been applied yet, so any valid skills-v* tag is newer. A fetched
// tag that doesn't parse as skills-v<semver> is treated as not-newer (fail-safe:
// don't apply something we can't version-compare).
func skillBundleVersionIsNewer(fetched, current string) bool {
	fv, ok := skillBundleSemver(fetched)
	if !ok {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	cv, ok := skillBundleSemver(current)
	if !ok {
		// We have an unparseable on-disk tag; rather than risk a downgrade or a
		// spurious re-apply, only apply when the fetched tag differs. Compare by
		// raw string to avoid clobbering with the same tag.
		return strings.TrimSpace(fetched) != strings.TrimSpace(current)
	}
	return skillBundleIsNewerVersion(fv, cv)
}

// skillBundleSemver strips the skills-v prefix off a tag and returns the
// semantic-version remainder, or ("", false) if the tag lacks the prefix.
func skillBundleSemver(tag string) (string, bool) {
	t := strings.TrimSpace(tag)
	const prefix = "skills-v"
	if !strings.HasPrefix(t, prefix) {
		return "", false
	}
	return strings.TrimPrefix(t, prefix), true
}
