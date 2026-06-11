import { queryOptions } from "@tanstack/react-query";

import { api } from "../api";

export const teamKeys = {
  all: (wsId: string) => ["team", wsId] as const,
  overview: (wsId: string) => [...teamKeys.all(wsId), "overview"] as const,
};

// Team overview for the /{slug}/team page. Keyed on wsId so switching workspace
// swaps the cache entry automatically (workspace-scoped query rule). Workspace
// scope itself rides on the X-Workspace-ID header set by the API client.
export function teamOverviewOptions(wsId: string) {
  return queryOptions({
    queryKey: teamKeys.overview(wsId),
    queryFn: () => api.getTeamOverview(),
  });
}
