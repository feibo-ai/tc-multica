import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { Project } from "../types";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
};

// The `withoutDri` flag is part of the queryKey so the regular and triage
// caches stay isolated — switching the filter swaps the cached row set
// instead of blowing the existing list away. Detail-page / mutation
// invalidations use the `projectKeys.list(wsId)` prefix (no `exact: true`)
// so both buckets refetch after a save. Both queryFns return a `Project[]`
// directly — the default list unwraps the `{ projects, total }` envelope
// here so callers don't have to remember which variant returns what.
export function projectListOptions(
  wsId: string,
  opts?: { withoutDri?: boolean },
) {
  const scope = opts?.withoutDri ? "without_dri" : "all";
  return queryOptions({
    queryKey: [...projectKeys.list(wsId), scope] as const,
    queryFn: async (): Promise<Project[]> => {
      if (opts?.withoutDri) {
        return api.listProjectsWithoutDRI();
      }
      const res = await api.listProjects();
      return res.projects;
    },
  });
}

export function projectDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detail(wsId, id),
    queryFn: () => api.getProject(id),
  });
}
