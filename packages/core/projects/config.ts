import type { Project, ProjectStatus, ProjectPriority } from "../types";

export const PROJECT_STATUS_ORDER: ProjectStatus[] = [
  "planned",
  "in_progress",
  "paused",
  "completed",
  "cancelled",
];

export const PROJECT_STATUS_CONFIG: Record<
  ProjectStatus,
  { label: string; color: string; dotColor: string; badgeBg: string; badgeText: string }
> = {
  planned: { label: "Planned", color: "text-muted-foreground", dotColor: "bg-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
  in_progress: { label: "In Progress", color: "text-warning", dotColor: "bg-warning", badgeBg: "bg-warning", badgeText: "text-white" },
  paused: { label: "Paused", color: "text-muted-foreground", dotColor: "bg-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
  completed: { label: "Completed", color: "text-info", dotColor: "bg-info", badgeBg: "bg-info", badgeText: "text-white" },
  cancelled: { label: "Cancelled", color: "text-destructive", dotColor: "bg-destructive", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
};

export const PROJECT_PRIORITY_ORDER: ProjectPriority[] = [
  "urgent",
  "high",
  "medium",
  "low",
  "none",
];

export const PROJECT_PRIORITY_CONFIG: Record<
  ProjectPriority,
  { label: string; bars: number; color: string; badgeBg: string; badgeText: string }
> = {
  urgent: { label: "Urgent", bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
  high: { label: "High", bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  medium: { label: "Medium", bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  low: { label: "Low", bars: 1, color: "text-info", badgeBg: "bg-info/10", badgeText: "text-info" },
  none: { label: "No priority", bars: 0, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
};

// Project health for the unified-tab card grid (Phase D2). There is NO health
// column — it's DERIVED from signals already on the Project, so it needs no
// migration. v1 surfaces the documented SOP v0.4 P-5 risk: an active project
// with no DRI is "at risk" (the same signal the backend without-dri triage
// list is built around). When a `target_date` column lands (D3 fast-follow),
// overdue/stalled can promote to "off_track" here without touching callers.
export type ProjectHealth = "on_track" | "at_risk" | "off_track";

export const PROJECT_HEALTH_CONFIG: Record<
  ProjectHealth,
  { dotColor: string; textColor: string }
> = {
  on_track: { dotColor: "bg-success", textColor: "text-success" },
  at_risk: { dotColor: "bg-warning", textColor: "text-warning" },
  off_track: { dotColor: "bg-destructive", textColor: "text-destructive" },
};

export function deriveProjectHealth(project: Project): ProjectHealth | null {
  // Terminal states have no ongoing "health" — the work is shipped or
  // abandoned. Returning null renders no badge (graceful absence).
  if (project.status === "completed" || project.status === "cancelled") return null;
  if (!project.dri_user_id) return "at_risk";
  return "on_track";
}
