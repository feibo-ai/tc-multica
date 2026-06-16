import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { FleetAuditResult } from "../types";

// TEA-113 INV-6 auto-refresh. While any audit row is still non-terminal
// (report_status null / pending / running) the daemon's (B) terminal report has
// not landed yet, so the progress panel must poll the AUTHORITATIVE audit table
// to pick it up — the daemon pushes to the server, not the browser. Once every
// row is terminal (completed / failed / timeout) polling stops. 4s balances
// responsiveness against load; the server caps the read.
const FLEET_AUDIT_POLL_MS = 4_000;
const FLEET_NON_TERMINAL = new Set<string>(["pending", "running"]);

function fleetAuditHasInflight(result: FleetAuditResult | undefined): boolean {
  if (!result) return false;
  return result.rows.some(
    (row) =>
      row.report_status === null || FLEET_NON_TERMINAL.has(row.report_status),
  );
}

export const runtimeKeys = {
  all: (wsId: string) => ["runtimes", wsId] as const,
  list: (wsId: string) => [...runtimeKeys.all(wsId), "list"] as const,
  listMine: (wsId: string) => [...runtimeKeys.all(wsId), "list", "mine"] as const,
  usage: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", rid, days, tz] as const,
  usageByAgent: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-agent", rid, days, tz] as const,
  // by-hour now follows the viewer's tz, like the other reports.
  usageByHour: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-hour", rid, days, tz] as const,
  latestVersion: () => ["runtimes", "latestVersion"] as const,
  // TEA-113 fleet one-click update — the persistent audit table is the
  // AUTHORITATIVE per-runtime progress source (INV-6), never the ephemeral
  // getUpdateResult. `since` is part of the key so a widened lookback refetches.
  fleetAudit: (wsId: string, since?: number) =>
    ["runtimes", wsId, "fleet", "audit", since ?? "default"] as const,
};

// `tz` is the viewer's IANA name — all reports follow the viewer's tz.
export function runtimeUsageOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usage(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsage(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

export function runtimeUsageByAgentOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usageByAgent(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByAgent(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

export function runtimeUsageByHourOptions(runtimeId: string, days: number, tz: string) {
  return queryOptions({
    queryKey: runtimeKeys.usageByHour(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByHour(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

export function runtimeListOptions(wsId: string, owner?: "me") {
  return queryOptions({
    queryKey: owner === "me" ? runtimeKeys.listMine(wsId) : runtimeKeys.list(wsId),
    queryFn: () => api.listRuntimes({ workspace_id: wsId, owner }),
  });
}

// INV-11: the authoritative latest CLI release comes from the backend
// (feibo-ai/tc-multica via GET /api/cli/latest-release), NOT a hard-coded
// upstream GitHub URL. The old direct fetch pointed at the wrong repo
// (multica-ai/multica) and was unreachable from self-hosted internal networks,
// which made the app-sidebar red dot and the per-runtime update buttons show a
// stale/false "up to date". Routing through the backend fixes both: the server
// resolves the correct repo with a short TTL cache and is always reachable.
export function latestCliVersionOptions() {
  return queryOptions({
    queryKey: runtimeKeys.latestVersion(),
    queryFn: async (): Promise<string | null> => {
      const release = await api.getLatestCliRelease();
      // parseWithFallback yields EMPTY_FLEET_LATEST_RELEASE ("") on a backend
      // hiccup / contract drift; normalise the empty tag to null so the
      // "needs update" comparison treats "unknown latest" as not-lagging.
      return release.tag_name || null;
    },
    staleTime: 10 * 60 * 1000, // 10 minutes
  });
}

// TEA-113 fleet update progress/audit. Reads the persistent audit table (the
// authoritative per-runtime progress source, INV-6) — not the ephemeral
// getUpdateResult. `since`/`limit` bound the read; the server caps both.
export function fleetAuditOptions(
  wsId: string,
  params?: { since?: number; limit?: number },
) {
  return queryOptions({
    queryKey: runtimeKeys.fleetAudit(wsId, params?.since),
    queryFn: () => api.getFleetAudit(wsId, params),
    // Poll only while a triggered update is still in flight; settle to no
    // polling once every row reaches a terminal state. The callback reads the
    // latest cached result so the interval self-stops without extra state.
    refetchInterval: (query) =>
      fleetAuditHasInflight(query.state.data) ? FLEET_AUDIT_POLL_MS : false,
  });
}
