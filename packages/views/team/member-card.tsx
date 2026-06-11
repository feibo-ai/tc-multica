"use client";

import type { TeamOverviewMember } from "@multica/core/team";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";

import { useT } from "../i18n";

// Distribution-bar segments in workflow order. "blocked" uses the destructive
// token so it breaks out of the chart-1..5 single-hue blue ramp (which is
// colourblind-ambiguous on its own); the legend below always carries a text
// label + count as well, so meaning never rides on colour alone (TEA-104 N-D3).
// All seven issue statuses, in workflow order, so the bar + legend sum to
// `issues_total` shown alongside (no header-number vs legend-sum mismatch).
// Backlog / cancelled get muted tones; the legend carries text labels too, so
// colour is never the sole signal (a11y).
const STATUS_SEGMENTS = [
  { key: "backlog", color: "var(--chart-5)" },
  { key: "todo", color: "var(--chart-1)" },
  { key: "in_progress", color: "var(--chart-2)" },
  { key: "in_review", color: "var(--chart-3)" },
  { key: "done", color: "var(--chart-4)" },
  { key: "blocked", color: "var(--destructive)" },
  { key: "cancelled", color: "var(--muted-foreground)" },
] as const;

type StatusKey = (typeof STATUS_SEGMENTS)[number]["key"];

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function initialsOf(name: string): string {
  return name
    .split(" ")
    .map((w) => w[0] ?? "")
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

export function TeamMemberCard({ member }: { member: TeamOverviewMember }) {
  const { t } = useT("team");
  const { t: tm } = useT("members");

  const roleLabel =
    member.role === "owner"
      ? tm(($) => $.role.owner)
      : member.role === "admin"
        ? tm(($) => $.role.admin)
        : tm(($) => $.role.member);

  const statusLabel = (key: StatusKey): string => {
    switch (key) {
      case "backlog":
        return t(($) => $.status.backlog);
      case "todo":
        return t(($) => $.status.todo);
      case "in_progress":
        return t(($) => $.status.in_progress);
      case "in_review":
        return t(($) => $.status.in_review);
      case "done":
        return t(($) => $.status.done);
      case "blocked":
        return t(($) => $.status.blocked);
      case "cancelled":
        return t(($) => $.status.cancelled);
    }
  };

  const segments = STATUS_SEGMENTS.map((s) => ({
    ...s,
    count: member.issues_by_status[s.key] ?? 0,
  }));
  const barTotal = segments.reduce((sum, s) => sum + s.count, 0);

  return (
    <div
      className={`flex flex-col gap-3 rounded-xl border bg-card p-4 text-left ${member.is_self ? "ring-1 ring-primary/40" : ""}`}
    >
      <div className="flex items-start gap-3">
        <ActorAvatar
          name={member.name}
          initials={initialsOf(member.name)}
          avatarUrl={member.avatar_url}
          size={40}
          className="rounded-full"
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <p className="truncate text-sm font-semibold">
              {member.name || member.email}
            </p>
            <span className="rounded-md bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
              {roleLabel}
            </span>
            {member.is_self && (
              <span className="rounded-md bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                {t(($) => $.you)}
              </span>
            )}
            {member.squad_name && (
              <span className="truncate text-[11px] text-muted-foreground">
                {member.squad_name}
              </span>
            )}
          </div>
          <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
            <span
              className="inline-block h-1.5 w-1.5 shrink-0 rounded-full"
              style={{
                backgroundColor:
                  member.agents_running > 0
                    ? "var(--chart-2)"
                    : "var(--muted-foreground)",
              }}
              aria-hidden
            />
            <span>
              {member.agents_running > 0
                ? t(($) => $.agents_running, { n: member.agents_running })
                : t(($) => $.agents_idle)}
            </span>
          </div>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2">
        <MetricTile
          label={t(($) => $.metrics.projects)}
          help={t(($) => $.metrics_help.projects)}
          value={`${member.projects_led}${member.projects_dri ? ` · DRI ${member.projects_dri}` : ""}`}
        />
        <MetricTile
          label={t(($) => $.metrics.agents)}
          help={t(($) => $.metrics_help.agents)}
          value={String(member.agents_total)}
        />
        <MetricTile
          label={t(($) => $.metrics.autopilots)}
          help={t(($) => $.metrics_help.autopilots)}
          value={String(member.autopilots)}
        />
        <MetricTile
          label={t(($) => $.metrics.ai_usage)}
          help={t(($) => $.metrics_help.ai_usage)}
          value={`${formatTokens(member.tokens_week)} / ${formatTokens(member.tokens_month)}`}
        />
      </div>

      <div>
        <div className="mb-1.5 flex items-center justify-between text-xs">
          <span className="text-muted-foreground">{t(($) => $.distribution)}</span>
          <span className="font-medium tabular-nums">{member.issues_total}</span>
        </div>
        <div
          className="flex h-2 overflow-hidden rounded-full bg-muted"
          role="presentation"
        >
          {barTotal > 0 &&
            segments.map((s) =>
              s.count > 0 ? (
                <span
                  key={s.key}
                  style={{ flexGrow: s.count, backgroundColor: s.color }}
                />
              ) : null,
            )}
        </div>
        <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          {segments.map((s) => (
            <span key={s.key} className="inline-flex items-center gap-1">
              <span
                className="inline-block h-1.5 w-1.5 shrink-0 rounded-sm"
                style={{ backgroundColor: s.color }}
                aria-hidden
              />
              {statusLabel(s.key)} {s.count}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

function MetricTile({
  label,
  value,
  help,
}: {
  label: string;
  value: string;
  help: string;
}) {
  return (
    <div className="rounded-md bg-muted/60 px-2.5 py-1.5" title={help}>
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="text-base font-medium tabular-nums">{value}</div>
    </div>
  );
}
