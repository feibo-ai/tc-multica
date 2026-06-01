"use client";

import { Check } from "lucide-react";
import {
  PROJECT_STATUS_CONFIG,
  PROJECT_STATUS_ORDER,
  PROJECT_PRIORITY_CONFIG,
  PROJECT_PRIORITY_ORDER,
  PROJECT_HEALTH_CONFIG,
  deriveProjectHealth,
} from "@multica/core/projects/config";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import type { Project, ProjectStatus, ProjectPriority, UpdateProjectRequest } from "@multica/core/types";
import { PriorityIcon } from "../../issues/components/priority-icon";
import { useProjectStatusLabels, useProjectPriorityLabels } from "./labels";

export function ProjectStatusBadge({ project, handleUpdate, triggerClassName, align = "end" }: { project: Project; handleUpdate: (data: UpdateProjectRequest) => void; triggerClassName?: string; align?: "start" | "end" | "center" }) {
  const statusLabels = useProjectStatusLabels();
  const statusCfg = PROJECT_STATUS_CONFIG[project.status];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button type="button" className={cn(
            "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium cursor-pointer hover:opacity-80 transition-opacity",
            statusCfg.badgeBg, statusCfg.badgeText,
            triggerClassName
          )}>
            {statusLabels[project.status]}
          </button>
        }
      />
      <DropdownMenuContent align={align} className="w-44">
        {PROJECT_STATUS_ORDER.map((s) => (
          <DropdownMenuItem key={s} onClick={() => handleUpdate({ status: s as ProjectStatus })}>
            <span className={cn("size-2 rounded-full", PROJECT_STATUS_CONFIG[s].dotColor)} />
            <span>{statusLabels[s]}</span>
            {s === project.status && <Check className="ml-auto h-3.5 w-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// Read-only DERIVED health indicator (Phase D2) — no picker, no mutation. A
// terminal-state project derives to null and renders nothing (graceful
// absence, per completion criterion #2).
export function ProjectHealthBadge({ project, className }: { project: Project; className?: string }) {
  const { t } = useT("projects");
  const health = deriveProjectHealth(project);
  if (!health) return null;
  const cfg = PROJECT_HEALTH_CONFIG[health];
  const label = t(($) => $.health[health]);
  return (
    <span className={cn("inline-flex items-center gap-1 text-[10px]", className)} title={label}>
      <span className={cn("size-1.5 rounded-full", cfg.dotColor)} aria-hidden />
      <span className={cfg.textColor}>{label}</span>
    </span>
  );
}

export function ProjectPriorityBadge({ project, handleUpdate, triggerClassName, align = "end" }: { project: Project; handleUpdate: (data: UpdateProjectRequest) => void; triggerClassName?: string; align?: "start" | "end" | "center" }) {
  const priorityLabels = useProjectPriorityLabels();
  const priorityCfg = PROJECT_PRIORITY_CONFIG[project.priority];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button type="button" className={cn(
            "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium hover:bg-accent/60 transition-colors cursor-pointer",
            triggerClassName
          )}>
            <PriorityIcon priority={project.priority} />
            <span className={cn("text-xs", priorityCfg.color)}>{priorityLabels[project.priority]}</span>
          </button>
        }
      />
      <DropdownMenuContent align={align} className="w-44">
        {PROJECT_PRIORITY_ORDER.map((p) => (
          <DropdownMenuItem key={p} onClick={() => handleUpdate({ priority: p as ProjectPriority })}>
            <PriorityIcon priority={p} />
            <span>{priorityLabels[p]}</span>
            {p === project.priority && <Check className="ml-auto h-3.5 w-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
