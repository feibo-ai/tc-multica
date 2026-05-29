"use client";

import { FolderKanban } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectListOptions } from "@multica/core/projects/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { ProjectIcon } from "./project-icon";

// Resident project list for the unified-tab drill-down's left rail. Runs its
// own `projectListOptions` query — the SAME query (and cache) the card grid
// uses — so a cold deep link to /{slug}/projects/:id hydrates the rail and
// highlights the target without assuming the user arrived via the grid.
export function ProjectListRail({ currentProjectId }: { currentProjectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { data: projects = [], isLoading } = useQuery(projectListOptions(wsId));

  return (
    <div className="flex h-full flex-col border-r bg-muted/10">
      <div className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="truncate text-xs font-medium text-muted-foreground">
          {t(($) => $.page.title)}
        </span>
        {!isLoading && projects.length > 0 && (
          <span className="ml-auto text-[10px] tabular-nums text-muted-foreground/60">
            {projects.length}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto py-1">
        {isLoading ? (
          <div className="space-y-1 px-2 py-1">
            {Array.from({ length: 8 }).map((_, i) => (
              <div key={i} className="flex items-center gap-2 px-1 py-1.5">
                <Skeleton className="h-4 w-4 rounded" />
                <Skeleton className="h-3.5 w-32" />
              </div>
            ))}
          </div>
        ) : (
          projects.map((project) => {
            const selected = project.id === currentProjectId;
            return (
              <AppLink
                key={project.id}
                href={wsPaths.projectDetail(project.id)}
                aria-current={selected ? "page" : undefined}
                className={cn(
                  "flex items-center gap-2 px-3 py-1.5 text-sm transition-colors",
                  selected
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-muted-foreground hover:bg-accent/50 hover:text-foreground",
                )}
              >
                <ProjectIcon project={project} size="sm" />
                <span className="min-w-0 flex-1 truncate">{project.title}</span>
                {project.issue_count > 0 && (
                  <span className="shrink-0 text-[10px] tabular-nums text-muted-foreground/60">
                    {project.issue_count}
                  </span>
                )}
              </AppLink>
            );
          })
        )}
      </div>
    </div>
  );
}
