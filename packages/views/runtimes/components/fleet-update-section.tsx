"use client";

// TEA-113 — DRI one-click fleet update (pure CLI dimension · nudge + force).
//
// This is the PageHeaderBar right-side action for the Runtimes page. It lets a
// DRI nudge every lagging local CLI runtime in the workspace to self-check and
// pull the authoritative latest release in one click, and force-override the
// machines that have not opted into auto-update.
//
// Authoritative-source discipline (mini-ADR REV-5):
//   - The lagging list comes from useUpdatableRuntimeIds(wsId, "fleet") — the
//     same owner-agnostic, desktop-excluded candidate set the server acts on.
//   - The per-runtime PROGRESS uses getFleetAudit (the persistent audit table,
//     INV-6) as the AUTHORITATIVE terminal-state source — NEVER the ephemeral
//     getUpdateResult. The audit table survives the 5min ephemeral TTL.
//   - The trigger receipt (the self-check response) supplies the skipped /
//     unreachable partitions for the run we just fired; the audit table supplies
//     the terminal state of each triggered update_id. A triggered update_id with
//     NO audit row (dropped / past retention) is reported as `unknown`.
//   - "completed" is labelled "daemon self-reported updated" (report_source=
//     daemon-reported), NOT "safely completed": the daemon report is the
//     not-fully-trusted (B)-column (INV-4). "timeout" comes from the
//     server-timeout sweep (report_source=server-timeout).
//
// a11y (INV-15 · self-contained rule, NOT copied from any coloured-text sample):
//   - status labels are ALWAYS neutral text-foreground / text-muted-foreground;
//     they NEVER carry semantic text-success/destructive/warning/info.
//   - the colour cue lives ONLY in the dot (bg-xxx + aria-hidden) + the tint
//     (bg-xxx/10 container background).
//   - every terminal state has its own text label and is distinguishable
//     without colour. No emoji — lucide icons / text only.
//
// Pure CLI: no copy ever mentions skills being out of date (mini-ADR §8).

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  Loader2,
  RefreshCw,
  Rocket,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type {
  AgentRuntime,
  FleetAuditRow,
  FleetSelfCheckResult,
  MemberWithUser,
} from "@multica/core/types";
import { useUpdatableRuntimeIds } from "@multica/core/runtimes/hooks";
import {
  runtimeListOptions,
  latestCliVersionOptions,
  fleetAuditOptions,
} from "@multica/core/runtimes/queries";
import { useNudgeFleet } from "@multica/core/runtimes/mutations";
import { FleetRateLimitError } from "@multica/core/api";
import { useCurrentMember } from "@multica/core/permissions";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// Fallback rate-limit window (INV-12). The server's fixed window is 30s and the
// client normally parses Retry-After into FleetRateLimitError.retryAfterSeconds;
// this is only the floor used to seed the re-enable timer when the server omits
// the header. UI-only — never a correctness gate.
const FLEET_RATE_LIMIT_FALLBACK_SECONDS = 30;

// ---------------------------------------------------------------------------
// Progress bucket model (INV-6).
//
// Buckets that count toward the x/N denominator (N = trigger-success + skipped
// + unknown). `desktop` / unreachable machines are reported separately as
// "not triggered" and DO NOT enter the denominator.
//
// Trust split (INV-4): `completed` is the daemon's self-report; `timeout` is
// the server-timeout sweep. They are distinct buckets so the copy can
// distinguish "server triggered" from "daemon self-reported".
// ---------------------------------------------------------------------------
type FleetBucket =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "timeout"
  | "skipped"
  // Trigger-time infrastructure failure (Create faulted) — server's `failed`
  // bucket on the trigger receipt. Distinct from `failed` (a daemon-reported
  // terminal update failure from the audit table) and from `skipped` (already
  // updating). Its own bucket so a transient trigger fault is honest, not
  // disguised as "已在更新中" (INV-6).
  | "trigger_failed"
  | "unknown";

// Every bucket carries an explicit dot + tint pairing. INV-15: the label text
// is rendered NEUTRAL by the badge component below; these classes only ever
// feed the dot (foreground colour cue) and the tint (container background).
// We deliberately do NOT store a `text-xxx` semantic label colour here.
const BUCKET_VISUAL: Record<FleetBucket, { dot: string; tint: string }> = {
  pending: { dot: "bg-muted-foreground/50", tint: "bg-muted" },
  running: { dot: "bg-info", tint: "bg-info/10" },
  completed: { dot: "bg-success", tint: "bg-success/10" },
  failed: { dot: "bg-destructive", tint: "bg-destructive/10" },
  timeout: { dot: "bg-warning", tint: "bg-warning/10" },
  skipped: { dot: "bg-muted-foreground/50", tint: "bg-muted" },
  trigger_failed: { dot: "bg-destructive", tint: "bg-destructive/10" },
  unknown: { dot: "bg-muted-foreground/40", tint: "bg-muted" },
};

// Order the buckets render in — settled-positive first, then in-flight, then
// the attention states. Keeps the aggregate readout scannable.
const BUCKET_ORDER: FleetBucket[] = [
  "completed",
  "running",
  "pending",
  "skipped",
  "failed",
  "timeout",
  "trigger_failed",
  "unknown",
];

// Map an audit row's (B)-column terminal status to a bucket. A null
// report_status means the trigger fact is recorded but no terminal result has
// landed yet — still in flight (pending). The audit table never silently drops
// a row the way the 5min-TTL ephemeral store does, so a present row with a null
// report is genuinely pending, NOT unknown.
function bucketForAuditRow(row: FleetAuditRow): FleetBucket {
  switch (row.report_status) {
    case "completed":
      return "completed";
    case "failed":
      return "failed";
    case "timeout":
      return "timeout";
    case "running":
      return "running";
    case "pending":
      return "pending";
    default:
      return "pending";
  }
}

// ---------------------------------------------------------------------------
// a11y badge (INV-15): dot (bg-xxx + aria-hidden) + tint container + NEUTRAL
// text label. Structurally modelled on shared.tsx HealthBadge but deliberately
// NOT copying its v.tone (which colours the whole badge text) and adding the
// aria-hidden it lacks.
// ---------------------------------------------------------------------------
function FleetStatusBadge({
  bucket,
  label,
  count,
}: {
  bucket: FleetBucket;
  label: string;
  count: number;
}) {
  const v = BUCKET_VISUAL[bucket];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-foreground",
        v.tint,
      )}
    >
      <span
        aria-hidden="true"
        className={cn("h-1.5 w-1.5 shrink-0 rounded-full", v.dot)}
      />
      <span className="text-foreground">{label}</span>
      <span className="font-mono tabular-nums text-muted-foreground">
        {count}
      </span>
    </span>
  );
}

interface LaggingEntry {
  runtimeId: string;
  cliVersion: string | null;
}

interface OwnerGroup {
  ownerId: string | null;
  ownerName: string;
  entries: LaggingEntry[];
}

function readCliVersion(runtime: AgentRuntime): string | null {
  const version = runtime.metadata?.cli_version;
  return typeof version === "string" && version.trim() ? version.trim() : null;
}

export function FleetUpdateSection({ wsId }: { wsId: string }) {
  const { t } = useT("runtimes");
  // DRI gate (INV-3). The button is hidden for non-DRI members. NOTE: this hide
  // is UX ONLY — authorization is enforced by the server's owner/admin role
  // check, which returns 403 for anyone who is not a DRI. Never treat the hide
  // as the security boundary.
  const { role } = useCurrentMember(wsId);
  const isDri = role === "owner" || role === "admin";

  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: latestVersion } = useQuery(latestCliVersionOptions());
  // INV-6: the persistent audit table is the AUTHORITATIVE per-runtime progress
  // source — not the ephemeral getUpdateResult. We read the recent window so a
  // freshly-triggered run's (A) trigger rows and any (B) terminal rows show up.
  const { data: audit } = useQuery({
    ...fleetAuditOptions(wsId),
    // Only DRIs can read the audit endpoint (same gate as the trigger), so we
    // skip the request entirely for non-DRIs to avoid a guaranteed 403.
    enabled: isDri,
  });

  // The owner-agnostic, desktop-excluded fleet candidate set (the INV-5 desktop
  // exclusion is kept inside the hook). These are the machines the one-click
  // nudge acts on.
  const laggingIds = useUpdatableRuntimeIds(wsId, "fleet");

  const memberById = useMemo(() => {
    const map = new Map<string, MemberWithUser>();
    for (const m of members) map.set(m.user_id, m);
    return map;
  }, [members]);

  // Lagging machines grouped by owner (whose machine is behind + current
  // cli_version → target). Pure CLI: never references skill state (§8).
  const ownerGroups = useMemo<OwnerGroup[]>(() => {
    const byOwner = new Map<string, OwnerGroup>();
    for (const rt of runtimes) {
      if (!laggingIds.has(rt.id)) continue;
      const key = rt.owner_id ?? "__unassigned__";
      let group = byOwner.get(key);
      if (!group) {
        const ownerName = rt.owner_id
          ? memberById.get(rt.owner_id)?.name ??
            t(($) => $.fleet.owner_unknown)
          : t(($) => $.fleet.owner_unassigned);
        group = { ownerId: rt.owner_id, ownerName, entries: [] };
        byOwner.set(key, group);
      }
      group.entries.push({
        runtimeId: rt.id,
        cliVersion: readCliVersion(rt),
      });
    }
    return Array.from(byOwner.values()).sort((a, b) =>
      a.ownerName.localeCompare(b.ownerName),
    );
  }, [runtimes, laggingIds, memberById, t]);

  const laggingCount = laggingIds.size;

  if (!isDri || laggingCount === 0) return null;

  return (
    <FleetUpdatePopover
      wsId={wsId}
      laggingCount={laggingCount}
      ownerGroups={ownerGroups}
      targetVersion={latestVersion ?? null}
      auditRows={audit?.rows ?? []}
    />
  );
}

function FleetUpdatePopover({
  wsId,
  laggingCount,
  ownerGroups,
  targetVersion,
  auditRows,
}: {
  wsId: string;
  laggingCount: number;
  ownerGroups: OwnerGroup[];
  targetVersion: string | null;
  auditRows: FleetAuditRow[];
}) {
  const { t } = useT("runtimes");
  const [open, setOpen] = useState(false);
  const [rateLimited, setRateLimited] = useState(false);
  const nudge = useNudgeFleet(wsId);

  // INV-12 rate-limit recovery timer. A 429 disables every trigger button; we
  // MUST re-enable them automatically once the server's fixed window elapses,
  // otherwise the whole session is wedged until a manual page refresh (the user
  // cannot click anything to reset it). We hold the timer id in a ref so the
  // cleanup effect and any re-trigger can clear it — preventing a leaked timer
  // or a stale callback re-disabling a fresh interaction.
  const rateLimitTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearRateLimitTimer = useCallback(() => {
    if (rateLimitTimer.current !== null) {
      clearTimeout(rateLimitTimer.current);
      rateLimitTimer.current = null;
    }
  }, []);

  // Clear any pending timer on unmount so a fired callback never touches an
  // unmounted component's state.
  useEffect(() => clearRateLimitTimer, [clearRateLimitTimer]);

  // Aggregate progress for the run we just fired (INV-6). The trigger receipt
  // partitions triggered / skipped / unreachable; the audit table supplies the
  // terminal state for each triggered update_id (the authoritative source).
  const progress = useMemo(
    () => aggregateProgress(nudge.data ?? null, auditRows),
    [nudge.data, auditRows],
  );

  const triggerFleet = (force: boolean) => {
    // A fresh trigger supersedes any in-flight recovery timer; clear it so the
    // old window's callback cannot re-enable mid-request or fire twice.
    clearRateLimitTimer();
    setRateLimited(false);
    nudge.mutate(
      { force },
      {
        onError: (err) => {
          // INV-12: a 429 means the server produced ZERO side effects (all-or-
          // nothing). Disable the buttons and surface "try again shortly".
          if (err instanceof FleetRateLimitError) {
            setRateLimited(true);
            // Consume retryAfterSeconds: re-enable the buttons once the
            // server's window elapses (default to the known 30s window when the
            // server omits Retry-After). Without this the session stays wedged
            // until a manual refresh. Guard against a duplicate/overlapping
            // timer by clearing first.
            clearRateLimitTimer();
            const seconds =
              err.retryAfterSeconds && err.retryAfterSeconds > 0
                ? err.retryAfterSeconds
                : FLEET_RATE_LIMIT_FALLBACK_SECONDS;
            rateLimitTimer.current = setTimeout(() => {
              rateLimitTimer.current = null;
              setRateLimited(false);
            }, seconds * 1000);
          }
        },
      },
    );
  };

  const busy = nudge.isPending;
  const disabled = busy || rateLimited;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button type="button" size="sm" variant="outline" disabled={disabled}>
            {busy ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <Rocket className="h-3 w-3" />
            )}
            {t(($) => $.fleet.action)}
            <span className="font-mono tabular-nums text-muted-foreground">
              {laggingCount}
            </span>
          </Button>
        }
      />
      <PopoverContent align="end" className="w-96 p-0">
        <div className="border-b px-4 py-3">
          <p className="text-sm font-medium">{t(($) => $.fleet.title)}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t(($) => $.fleet.subtitle, { count: laggingCount })}
          </p>
          {targetVersion && (
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.fleet.target_prefix)}{" "}
              <span className="font-mono text-foreground">{targetVersion}</span>
            </p>
          )}
        </div>

        {/* Lagging list, grouped by owner. Pure CLI: shows current cli_version
            → target version only — never skill state (§8). */}
        <div className="max-h-64 overflow-auto px-4 py-3">
          <ul className="space-y-3">
            {ownerGroups.map((group) => (
              <li key={group.ownerId ?? "__unassigned__"}>
                <p className="text-xs font-medium text-foreground">
                  {group.ownerName}
                </p>
                <ul className="mt-1 space-y-1">
                  {group.entries.map((entry) => (
                    <li
                      key={entry.runtimeId}
                      className="flex items-center gap-1.5 text-xs text-muted-foreground"
                    >
                      <span className="font-mono">
                        {entry.cliVersion ??
                          t(($) => $.fleet.version_unknown)}
                      </span>
                      <span aria-hidden="true">→</span>
                      <span className="font-mono text-foreground">
                        {targetVersion ?? t(($) => $.fleet.version_unknown)}
                      </span>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        </div>

        {/* Aggregate progress from the persistent audit table (INV-6). */}
        {progress.total > 0 && (
          <div className="border-t px-4 py-3">
            <p className="text-xs font-medium text-foreground">
              {t(($) => $.fleet.progress_title, {
                done: progress.done,
                total: progress.total,
              })}
            </p>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {BUCKET_ORDER.filter((b) => progress.counts[b] > 0).map(
                (bucket) => (
                  <FleetStatusBadge
                    key={bucket}
                    bucket={bucket}
                    label={t(($) => $.fleet.bucket[bucket])}
                    count={progress.counts[bucket]}
                  />
                ),
              )}
            </div>
            {/* desktop / unreachable machines are reported separately and never
                enter the x/N denominator (INV-6). */}
            {progress.notTriggered > 0 && (
              <p className="mt-2 text-[11px] text-muted-foreground">
                {t(($) => $.fleet.not_triggered, {
                  count: progress.notTriggered,
                })}
              </p>
            )}
            {/* (B)-column completed is the daemon's self-report, NOT a "safely
                completed" assertion (INV-4). */}
            {progress.counts.completed > 0 && (
              <p className="mt-2 text-[11px] text-muted-foreground">
                {t(($) => $.fleet.completed_caption)}
              </p>
            )}
            {/* unknown rows get a re-trigger entry point: the server-side trigger
                is the cheapest recovery (INV-6). */}
            {progress.counts.unknown > 0 && (
              <Button
                type="button"
                size="xs"
                variant="ghost"
                className="mt-2"
                disabled={disabled}
                onClick={() => triggerFleet(false)}
              >
                <RefreshCw className="h-3 w-3" />
                {t(($) => $.fleet.retrigger_unknown)}
              </Button>
            )}
          </div>
        )}

        <div className="border-t px-4 py-3">
          {rateLimited && (
            <p className="mb-2 flex items-center gap-1.5 text-xs text-muted-foreground">
              <AlertTriangle className="h-3 w-3" aria-hidden="true" />
              {t(($) => $.fleet.rate_limited)}
            </p>
          )}
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="sm"
              className="flex-1"
              disabled={disabled}
              onClick={() => triggerFleet(false)}
            >
              {busy ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : (
                <Rocket className="h-3 w-3" />
              )}
              {t(($) => $.fleet.nudge_action)}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={disabled}
              onClick={() => triggerFleet(true)}
            >
              {t(($) => $.fleet.force_action)}
            </Button>
          </div>
          <p className="mt-2 text-[11px] text-muted-foreground">
            {t(($) => $.fleet.force_hint)}
          </p>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// Progress aggregation (INV-6).
//
// Source of truth for terminal state = the persistent audit table; the trigger
// receipt partitions the run.
//
//   N (denominator) = trigger-success + skipped + trigger_failed + unknown
//   x (numerator / "done") = every triggered runtime that has reached a
//     terminal bucket (completed / failed / timeout) plus skipped +
//     trigger_failed + unknown (those will not progress on their own).
//
// Bucketing of triggered machines:
//   - the audit row for the triggered update_id provides the terminal state
//     (completed / failed / timeout) or pending/running when no terminal result
//     has landed yet;
//   - a triggered update_id with NO audit row at all (dropped / past the audit
//     retention window) is `unknown` — never silently lost.
//
// skipped comes from the trigger receipt (Create refused — "still updating to a
// prior trigger"). trigger_failed also comes from the trigger receipt (Create
// faulted with an infrastructure error) and is a SEPARATE bucket from skipped.
// unreachable (desktop) is reported as `notTriggered` and is EXCLUDED from x/N
// entirely.
// ---------------------------------------------------------------------------
export function aggregateProgress(
  trigger: FleetSelfCheckResult | null,
  auditRows: FleetAuditRow[],
): {
  counts: Record<FleetBucket, number>;
  total: number;
  done: number;
  notTriggered: number;
} {
  const counts: Record<FleetBucket, number> = {
    pending: 0,
    running: 0,
    completed: 0,
    failed: 0,
    timeout: 0,
    skipped: 0,
    trigger_failed: 0,
    unknown: 0,
  };

  if (!trigger) {
    return { counts, total: 0, done: 0, notTriggered: 0 };
  }

  // Index audit rows by update_id (latest trigger wins — rows are newest-first).
  const auditByUpdateId = new Map<string, FleetAuditRow>();
  for (const row of auditRows) {
    if (!auditByUpdateId.has(row.update_id)) {
      auditByUpdateId.set(row.update_id, row);
    }
  }

  // Triggered machines: terminal state from the audit table; `unknown` when the
  // triggered update_id has no audit row (dropped / past retention).
  for (const triggered of trigger.triggered) {
    const row = auditByUpdateId.get(triggered.update_id);
    if (!row) {
      counts.unknown += 1;
      continue;
    }
    counts[bucketForAuditRow(row)] += 1;
  }

  // skipped (Create refused — already updating) — from the trigger receipt.
  counts.skipped += trigger.skipped.length;

  // trigger_failed (Create faulted — infrastructure error) — from the trigger
  // receipt, its OWN bucket. Distinct from skipped: a transient store/Redis
  // fault is surfaced honestly and is re-triggerable, never disguised as
  // "已在更新中" (INV-6: zero silent drops).
  counts.trigger_failed += trigger.failed.length;

  // N = trigger-success + skipped + trigger_failed + unknown. (`unknown` is
  // already counted in the triggered loop above; triggered.length already
  // includes those rows.)
  const total =
    trigger.triggered.length +
    trigger.skipped.length +
    trigger.failed.length;

  // done = settled buckets that will not move on their own. trigger_failed is
  // terminal for this run (the Create never landed) so it counts as settled.
  const done =
    counts.completed +
    counts.failed +
    counts.timeout +
    counts.skipped +
    counts.trigger_failed +
    counts.unknown;

  // desktop / unreachable — reported separately, NOT in x/N.
  const notTriggered = trigger.unreachable.length;

  return { counts, total, done, notTriggered };
}
