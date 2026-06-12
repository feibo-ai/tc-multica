import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import { useRecentContextStore } from "../chat/recent-context-store";
import type { Project, CreateProjectRequest, UpdateProjectRequest } from "../types";

// The default project list cache stores an unwrapped `Project[]` — see
// `projectListOptions` in ./queries, which unwraps the `{ projects, total }`
// envelope so every consumer reads a bare array. The helpers below patch that
// array shape directly; the hooks apply them via `setQueryData<Project[]>`.
//
// The "without_dri" triage cache lives under the same prefix with a different
// suffix. Optimistic writes here only touch the default "all" bucket — the
// triage cache is refetched by the prefix invalidate on settle, because
// membership in "without DRI" depends on the new `dri_user_id` value and we
// don't want to predict the new membership here.
const projectListAllKey = (wsId: string) =>
  [...projectKeys.list(wsId), "all"] as const;

// Pure cache transforms, kept separate so the optimistic-update logic is unit
// testable in a Node environment (the hooks themselves require a React render).
// All three take and return the unwrapped `Project[]` shape and no-op on an
// absent cache (`undefined`) — never the `{ projects, total }` envelope.
export function addProjectToList(
  list: Project[] | undefined,
  created: Project,
): Project[] | undefined {
  return list && !list.some((p) => p.id === created.id)
    ? [...list, created]
    : list;
}

export function updateProjectInList(
  list: Project[] | undefined,
  id: string,
  data: Partial<Project>,
): Project[] | undefined {
  return list ? list.map((p) => (p.id === id ? { ...p, ...data } : p)) : list;
}

export function removeProjectFromList(
  list: Project[] | undefined,
  id: string,
): Project[] | undefined {
  return list ? list.filter((p) => p.id !== id) : list;
}

export function useCreateProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateProjectRequest) => api.createProject(data),
    onSuccess: (newProject) => {
      qc.setQueryData<Project[]>(projectListAllKey(wsId), (old) =>
        addProjectToList(old, newProject),
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}

export function useUpdateProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateProjectRequest) =>
      api.updateProject(id, data),
    onMutate: ({ id, ...data }) => {
      qc.cancelQueries({ queryKey: projectKeys.list(wsId) });
      const prevList = qc.getQueryData<Project[]>(projectListAllKey(wsId));
      const prevDetail = qc.getQueryData<Project>(projectKeys.detail(wsId, id));
      qc.setQueryData<Project[]>(projectListAllKey(wsId), (old) =>
        updateProjectInList(old, id, data),
      );
      qc.setQueryData<Project>(projectKeys.detail(wsId, id), (old) =>
        old ? { ...old, ...data } : old,
      );
      return { prevList, prevDetail, id };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList) qc.setQueryData(projectListAllKey(wsId), ctx.prevList);
      if (ctx?.prevDetail) qc.setQueryData(projectKeys.detail(wsId, ctx.id), ctx.prevDetail);
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: projectKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteProject(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: projectKeys.list(wsId) });
      const prevList = qc.getQueryData<Project[]>(projectListAllKey(wsId));
      qc.setQueryData<Project[]>(projectListAllKey(wsId), (old) =>
        removeProjectFromList(old, id),
      );
      qc.removeQueries({ queryKey: projectKeys.detail(wsId, id) });
      return { prevList };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prevList) qc.setQueryData(projectListAllKey(wsId), ctx.prevList);
    },
    onSuccess: (_data, id) => {
      useRecentContextStore.getState().forgetContext(wsId, { type: "project", id });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}
