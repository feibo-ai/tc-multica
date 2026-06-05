import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const dashboardKeys = {
  all: (wsId: string) => ["dashboard", wsId] as const,
  daily: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "daily", days, projectId, tz] as const,
  byAgent: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "by-agent", days, projectId, tz] as const,
  // No projectId in the key: the per-person view is workspace-wide (ambient
  // usage has no project).
  byPerson: (
    wsId: string,
    days: number,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "by-person", days, tz] as const,
  // Usage v2 — ambient-only per-person leaderboard for the user tab. Follows
  // the page `days` window like the other leaderboards.
  ambientByPerson: (
    wsId: string,
    days: number,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "ambient-by-person", days, tz] as const,
  // Usage v2 — one owner's ambient usage by day (user-tab heatmap). ownerId is
  // null when nothing is selected, "" for the unattributed bucket, a UUID for a
  // person. `days` (= HEATMAP_DAYS, fixed 26 weeks) is in the key so the heatmap
  // window never shares a cache entry with a leaderboard that happens to request
  // the same day count.
  ambientDaily: (
    wsId: string,
    ownerId: string | null,
    days: number,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "ambient-daily", ownerId, days, tz] as const,
  // Usage v2 — one agent's task usage by day (agent-tab heatmap). Same fixed
  // 26-week window in the key.
  agentDaily: (
    wsId: string,
    agentId: string | null,
    days: number,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "agent-daily", agentId, days, tz] as const,
  agentRuntime: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "agent-runtime", days, projectId, tz] as const,
  runTimeDaily: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "runtime-daily", days, projectId, tz] as const,
};

// 5-min rollup cadence on the server, 60s background refetch on the client.
const STALE_TIME = 60 * 1000;

// The usage-page heatmaps render a fixed GitHub-profile-style window of 26
// weeks (= 182 days), DECOUPLED from the page's days selector (which tops out
// at 180): a `days=1` heatmap would be a single lit cell. The component itself
// draws HEATMAP_WEEKS = 26 columns; this is the matching data window. Kept as a
// named constant and threaded into the query key so the 26-week window is
// explicit and never collides with a leaderboard cache entry.
export const HEATMAP_DAYS = 182;

// `tz` participates in every dashboard key so a Preferences change
// repoints the cache. All four series — token rollups and the
// atq.completed_at-based run-time series — slice their day boundary in
// the viewer's tz, so the four dashboard tabs always agree.
export function dashboardUsageDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.daily(wsId, days, projectId, tz),
    queryFn: () =>
      api.getDashboardUsageDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

export function dashboardUsageByAgentOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.byAgent(wsId, days, projectId, tz),
    queryFn: () =>
      api.getDashboardUsageByAgent({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

export function dashboardUsageByPersonOptions(
  wsId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.byPerson(wsId, days, tz),
    queryFn: () => api.getDashboardUsageByPerson({ days, tz }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

// Usage v2 — ambient-only per-(owner, model) leaderboard feed for the user tab.
export function dashboardAmbientUsageByPersonOptions(
  wsId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.ambientByPerson(wsId, days, tz),
    queryFn: () => api.getDashboardAmbientUsageByPerson({ days, tz }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

// Usage v2 — one owner's ambient usage per day for the user-tab heatmap.
//
// The `enabled` guard is `ownerId !== null`, NOT `!!ownerId`: the unattributed
// bucket's ownerId is the empty string "", and `!!"" === false` would leave its
// heatmap permanently un-fetched. The contract is null = nothing selected, "" =
// unattributed row selected, UUID = a person selected. Window is fixed at
// HEATMAP_DAYS (26 weeks), decoupled from the page days selector.
export function dashboardAmbientUsageDailyOptions(
  wsId: string,
  ownerId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.ambientDaily(wsId, ownerId, HEATMAP_DAYS, tz),
    queryFn: () =>
      api.getDashboardAmbientUsageDaily({
        owner_id: ownerId ?? "",
        days: HEATMAP_DAYS,
        tz,
      }),
    enabled: !!wsId && ownerId !== null,
    staleTime: STALE_TIME,
  });
}

// Usage v2 — one agent's task usage per day for the agent-tab heatmap. agentId
// is null when nothing is selected (agent IDs are never ""), so a plain
// `agentId !== null` guard is correct here too. Same fixed 26-week window.
export function dashboardAgentUsageDailyOptions(
  wsId: string,
  agentId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.agentDaily(wsId, agentId, HEATMAP_DAYS, tz),
    queryFn: () =>
      api.getDashboardAgentUsageDaily({
        agent_id: agentId ?? "",
        days: HEATMAP_DAYS,
        tz,
      }),
    enabled: !!wsId && agentId !== null,
    staleTime: STALE_TIME,
  });
}

export function dashboardAgentRunTimeOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.agentRuntime(wsId, days, projectId, tz),
    queryFn: () =>
      api.getDashboardAgentRunTime({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

export function dashboardRunTimeDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return queryOptions({
    queryKey: dashboardKeys.runTimeDaily(wsId, days, projectId, tz),
    queryFn: () =>
      api.getDashboardRunTimeDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}
