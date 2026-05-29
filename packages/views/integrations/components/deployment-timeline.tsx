"use client";

import { useQuery } from "@tanstack/react-query";
import { CircleDot } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import type { DeploymentStatus } from "@multica/core/types";
import { useT } from "../../i18n";

// Vertical timeline of the most recent deployments for one integration.
// Active deployment (the topmost row whose status is not stopped) is
// visually emphasized; stopped / degraded rows are dimmer.

interface Props {
  integrationId: string;
  limit?: number;
}

export function DeploymentTimeline({ integrationId, limit = 20 }: Props) {
  const workspaceId = useWorkspaceId();
  const { t } = useT("integrations");
  const { data, isLoading, isError } = useQuery({
    queryKey: [workspaceId, "integrations", integrationId, "deployments", limit],
    queryFn: () => api.listIntegrationDeployments(integrationId, limit),
    enabled: !!workspaceId && !!integrationId,
    refetchInterval: 15_000,
  });

  if (isLoading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
      </div>
    );
  }
  if (isError) {
    return <p className="text-sm text-destructive">{t(($) => $.timeline.load_failed)}</p>;
  }
  if (!data || data.length === 0) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="deployment-timeline-empty">
        {t(($) => $.timeline.empty)}
      </p>
    );
  }

  return (
    <ol className="relative space-y-3 border-l pl-5" data-testid="deployment-timeline">
      {data.map((d, idx) => (
        <li key={d.id} className="relative" data-testid={`deployment-${d.id}`}>
          <StatusMarker status={d.status} active={idx === 0 && d.status !== "stopped"} />
          <div className="flex flex-wrap items-baseline gap-x-3 text-sm">
            <code className="font-mono text-xs">
              {shorten(d.image_or_commit)}
            </code>
            <span className="text-xs text-muted-foreground">
              {t(($) => $.version_tag, { version: d.version })}
            </span>
            <StatusPill status={d.status} />
            {idx === 0 && d.status !== "stopped" && (
              <span className="rounded bg-emerald-500/10 px-1.5 py-0.5 text-xs text-emerald-700 dark:text-emerald-400">
                {t(($) => $.timeline.active)}
              </span>
            )}
          </div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            {t(($) => $.timeline.started)} {d.started_at}
            {d.last_heartbeat
              ? ` · ${t(($) => $.timeline.last_heartbeat)} ${d.last_heartbeat}`
              : ""}
            {d.stopped_at ? ` · ${t(($) => $.timeline.stopped)} ${d.stopped_at}` : ""}
          </div>
        </li>
      ))}
    </ol>
  );
}

function shorten(s: string) {
  // Common case: short SHA hex string. Trim to 12 chars, otherwise keep
  // the full thing (an image tag is usually short and readable already).
  if (/^[0-9a-fA-F]{20,}$/.test(s)) return s.slice(0, 12);
  return s;
}

function StatusMarker({ status, active }: { status: DeploymentStatus; active: boolean }) {
  const color =
    status === "running"
      ? "text-emerald-500"
      : status === "starting"
        ? "text-sky-500"
        : status === "degraded"
          ? "text-amber-500"
          : "text-zinc-400";
  return (
    <CircleDot
      className={`absolute -left-[26px] top-0.5 size-3 ${color}`}
      strokeWidth={active ? 3 : 2}
    />
  );
}

function StatusPill({ status }: { status: DeploymentStatus }) {
  const styles: Record<DeploymentStatus, string> = {
    starting: "bg-sky-500/10 text-sky-700 dark:text-sky-400",
    running: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
    degraded: "bg-amber-500/10 text-amber-700 dark:text-amber-400",
    stopped: "bg-zinc-500/10 text-zinc-700 dark:text-zinc-400",
  };
  return (
    <span className={`rounded px-1.5 py-0.5 text-xs ${styles[status] ?? styles.stopped}`}>
      {status}
    </span>
  );
}
