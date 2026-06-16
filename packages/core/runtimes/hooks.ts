import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import type { AgentRuntime } from "../types";
import { runtimeListOptions, latestCliVersionOptions } from "./queries";

function stripV(v: string): string {
  return v.replace(/^v/, "");
}

function isNewer(latest: string, current: string): boolean {
  const l = stripV(latest).split(".").map(Number);
  const c = stripV(current).split(".").map(Number);
  for (let i = 0; i < Math.max(l.length, c.length); i++) {
    const lv = l[i] ?? 0;
    const cv = c[i] ?? 0;
    if (lv > cv) return true;
    if (lv < cv) return false;
  }
  return false;
}

// Scope of the "needs update" computation.
//   - "owner" (default): only the current user's own local runtimes. Drives the
//     per-user app-sidebar dot and the per-machine "update" buttons. Preserves
//     the historical behaviour exactly.
//   - "fleet": every lagging local runtime in the workspace, regardless of
//     owner. Drives the DRI one-click fleet update (TEA-113). The owner filter
//     is dropped; the desktop-launched exclusion is KEPT (those are managed by
//     the Desktop app's own auto-updater and are "unreachable" to the fleet).
export type RuntimeUpdateScope = "owner" | "fleet";

function runtimeNeedsUpdate(
  rt: AgentRuntime,
  latestVersion: string,
  userId: string,
  scope: RuntimeUpdateScope,
): boolean {
  if (rt.runtime_mode !== "local") return false;
  // "owner" scope only counts the current user's own runtimes; "fleet" scope
  // (DRI) counts every lagging local runtime in the workspace.
  if (scope === "owner" && rt.owner_id !== userId) return false;
  // Desktop-managed runtimes are updated by the Desktop app's own auto-updater;
  // the platform should not surface CLI update prompts for them, and the fleet
  // treats them as "unreachable" (excluded, mirrors the server's INV-5 echo).
  // KEPT for both scopes.
  if (rt.metadata && rt.metadata.launched_by === "desktop") {
    return false;
  }
  const cliVersion =
    rt.metadata && typeof rt.metadata.cli_version === "string"
      ? rt.metadata.cli_version
      : null;
  if (!cliVersion) return false;
  return isNewer(latestVersion, cliVersion);
}

/**
 * Returns true if the current user has any local runtime with an outdated CLI version.
 * Accepts wsId as parameter so callers outside WorkspaceIdProvider can use it safely.
 *
 * `scope` defaults to "owner" (the current user's own machines). Pass "fleet"
 * for the DRI "any lagging machine in the workspace" signal (TEA-113).
 */
export function useMyRuntimesNeedUpdate(
  wsId: string | undefined,
  scope: RuntimeUpdateScope = "owner",
): boolean {
  const userId = useAuthStore((s) => s.user?.id);
  const { data: runtimes } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: latestVersion } = useQuery(latestCliVersionOptions());

  // "fleet" scope is owner-agnostic, so it does not require a resolved userId.
  if (!runtimes || !latestVersion) return false;
  if (scope === "owner" && !userId) return false;

  return runtimes.some((rt) =>
    runtimeNeedsUpdate(rt, latestVersion, userId ?? "", scope),
  );
}

/**
 * Returns a Set of runtime IDs that have updates available.
 * Accepts wsId as parameter so callers outside WorkspaceIdProvider can use it safely.
 *
 * `scope` defaults to "owner" (the current user's own machines, for the
 * per-machine update buttons). Pass "fleet" for every lagging local runtime in
 * the workspace, which is the candidate set the DRI one-click update acts on
 * (TEA-113). The desktop-launched exclusion holds in both scopes.
 */
export function useUpdatableRuntimeIds(
  wsId: string | undefined,
  scope: RuntimeUpdateScope = "owner",
): Set<string> {
  const userId = useAuthStore((s) => s.user?.id);
  const { data: runtimes } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: latestVersion } = useQuery(latestCliVersionOptions());

  return useMemo(() => {
    if (!runtimes || !latestVersion) return new Set<string>();
    // "owner" scope needs a resolved userId; "fleet" scope is owner-agnostic.
    if (scope === "owner" && !userId) return new Set<string>();
    const ids = new Set<string>();
    for (const rt of runtimes) {
      if (runtimeNeedsUpdate(rt, latestVersion, userId ?? "", scope)) {
        ids.add(rt.id);
      }
    }
    return ids;
  }, [runtimes, latestVersion, userId, scope]);
}
