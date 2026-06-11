"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { teamOverviewOptions } from "@multica/core/team";
import { Skeleton } from "@multica/ui/components/ui/skeleton";

import { useT } from "../i18n";
import { AppLink } from "../navigation";
import { TeamMemberCard } from "./member-card";

// Team overview — one card per workspace member. Visible to everyone (the same
// visibility as the existing per-person usage read-outs). Cards arrive already
// sorted by the backend (self → most-blocked → most-running) so the boss's
// attention lands on stuck / busy people first.
export function TeamOverviewPage() {
  const { t } = useT("team");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data, isLoading } = useQuery(teamOverviewOptions(wsId));
  const members = data?.members ?? [];

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-5 border-b px-4">
        <span className="-mb-px border-b-2 border-foreground py-2.5 text-sm font-medium">
          {t(($) => $.tab)}
        </span>
        <AppLink
          href={paths.projects()}
          className="py-2.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          {t(($) => $.projects_tab)}
        </AppLink>
      </div>

      <div className="flex-1 overflow-auto p-4">
        {isLoading ? (
          <CardGrid>
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-44 rounded-xl" />
            ))}
          </CardGrid>
        ) : members.length === 0 ? (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {t(($) => $.empty)}
          </p>
        ) : (
          <CardGrid>
            {members.map((m) => (
              <TeamMemberCard key={m.member_id} member={m} />
            ))}
          </CardGrid>
        )}
      </div>
    </div>
  );
}

function CardGrid({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      {children}
    </div>
  );
}
