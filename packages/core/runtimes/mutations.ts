import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runtimeKeys } from "./queries";
import { workspaceKeys } from "../workspace/queries";
import { agentTaskSnapshotKeys } from "../agents/queries";

export function useDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string) => api.deleteRuntime(runtimeId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// Cascade-mode counterpart to useDeleteRuntime. The dialog routes here when
// the strict DELETE refused with `runtime_has_active_agents` (or when the
// caller already knows the runtime has active agents and wants to skip the
// pre-flight refusal). Mutation fn returns the server-reported counts so
// the caller can render a richer success toast.
//
// Invalidates runtimes (the list / detail), workspace agents (the cascade
// archives them) and the agent presence snapshot (cascade also cancels
// queued/running tasks). Without the agent-side invalidation the Agents
// page would keep showing the just-archived rows as live until a refetch.
export function useArchiveAgentsAndDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      expectedActiveAgentIds,
    }: {
      runtimeId: string;
      expectedActiveAgentIds: string[];
    }) => api.archiveAgentsAndDeleteRuntime(runtimeId, expectedActiveAgentIds),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(wsId) });
    },
  });
}

// useUpdateRuntime patches editable fields on a runtime (visibility).
// Invalidates the runtime list so the picker disabled-state recomputes.
export function useUpdateRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      patch,
    }: {
      runtimeId: string;
      patch: { visibility?: "private" | "public" };
    }) => api.updateRuntime(runtimeId, patch),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// TEA-113 fleet one-click update. Nudges every lagging local runtime in the
// workspace to self-check and pull the authoritative latest release. DRI-only
// (server enforces the owner/admin gate, INV-3).
//
// INV-1: the only input is `{ force }` — no version. The mutationFn forwards
// exactly that to api.nudgeFleetSelfCheck, which sends `{ force }` and nothing
// else. The server fills the target version.
//
// On settle we invalidate (a) the runtime list — the just-nudged machines will
// report new cli_version on their next heartbeat, so the "needs update" set and
// the sidebar dot recompute — and (b) every fleet audit query for this
// workspace (regardless of `since`), since the audit table is the authoritative
// progress source (INV-6) and now has fresh (A) trigger rows. We do NOT key
// progress off the ephemeral self-check response; that is a one-shot trigger
// receipt, while the audit table survives the 5min ephemeral TTL.
//
// 429 (INV-12) surfaces as a typed FleetRateLimitError from the client — it
// propagates to the caller's onError so the UI can disable the button and show
// "try again shortly". The error is not swallowed here.
export function useNudgeFleet(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ force }: { force?: boolean } = {}) =>
      api.nudgeFleetSelfCheck(wsId, { force }),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      // Invalidate all fleet-audit variants for this workspace (any `since`):
      // the audit query key is ["runtimes", wsId, "fleet", "audit", ...], so a
      // prefix match catches every lookback window.
      qc.invalidateQueries({
        queryKey: ["runtimes", wsId, "fleet", "audit"],
      });
    },
  });
}
