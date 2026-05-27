"use client";

import { useState } from "react";
import { UserMinus } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { projectKeys } from "@multica/core/projects/queries";
import type { MemberWithUser, Project } from "@multica/core/types";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

/**
 * Renders the DRI (Directly Responsible Individual) for a project as a
 * properties-row entry: a red "Unassigned" warning when null (SOP v0.4 P-5
 * risk), the member's avatar + name when set, and a Popover-driven member
 * picker that opens on click so the DRI can be assigned or changed.
 *
 * Authorization mirrors the rest of the project-detail page — no explicit
 * permission gate; if you can reach the detail page you can edit. The
 * mutation calls `PUT /api/projects/{id}/dri` directly and invalidates the
 * project list + detail caches on success so the "Without DRI" filter and
 * the sidebar pill both refresh.
 */
export function ProjectDriSection({
  project,
  members,
  wsId,
}: {
  project: Project;
  members: MemberWithUser[];
  wsId: string;
}) {
  const { t } = useT("projects");
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const driMember = project.dri_user_id
    ? members.find((m) => m.user_id === project.dri_user_id) ?? null
    : null;

  const query = filter.toLowerCase();
  const filteredMembers = members.filter(
    (m) =>
      m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query),
  );

  const assignDri = async (userId: string | null) => {
    try {
      await api.assignProjectDRI(project.id, userId);
      // Invalidate by prefix so both the "all" cache and the "without_dri"
      // triage cache refetch — the project's membership in the triage list
      // depends on the new value, so we don't try to patch it locally.
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
      qc.invalidateQueries({
        queryKey: projectKeys.detail(wsId, project.id),
      });
      toast.success(t(($) => $.detail.toast_dri_updated));
    } catch {
      toast.error(t(($) => $.detail.toast_dri_update_failed));
    } finally {
      setOpen(false);
      setFilter("");
    }
  };

  return (
    <Popover
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
    >
      <PopoverTrigger
        render={
          <button
            type="button"
            className="inline-flex items-center gap-1.5 text-xs hover:text-foreground transition-colors"
            aria-label={
              project.dri_user_id
                ? t(($) => $.detail.dri_change)
                : t(($) => $.detail.dri_assign)
            }
          >
            {project.dri_user_id ? (
              driMember ? (
                <>
                  <ActorAvatar
                    name={driMember.name}
                    initials={driMember.name.slice(0, 2).toUpperCase()}
                    avatarUrl={driMember.avatar_url}
                    size={16}
                  />
                  <span className="cursor-pointer">{driMember.name}</span>
                </>
              ) : (
                // The DRI exists on the server but isn't in the local member
                // list — likely an old user_id that's been removed from the
                // workspace. Surface a truncated id so we don't silently look
                // unassigned, and let the popover assign a new DRI.
                <span className="font-mono text-muted-foreground/70">
                  {project.dri_user_id.slice(0, 8)}…
                </span>
              )
            ) : (
              <span className="text-red-500">
                {t(($) => $.detail.dri_unassigned)}
              </span>
            )}
          </button>
        }
      />
      <PopoverContent align="start" className="w-52 p-0">
        <div className="px-2 py-1.5 border-b">
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t(($) => $.detail.dri_assign_placeholder)}
            className="w-full bg-transparent text-sm placeholder:text-muted-foreground outline-none"
          />
        </div>
        <div className="p-1 max-h-60 overflow-y-auto">
          <button
            type="button"
            onClick={() => assignDri(null)}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent transition-colors"
          >
            <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-muted-foreground">
              {t(($) => $.detail.dri_unassigned)}
            </span>
          </button>
          {filteredMembers.map((m) => (
            <button
              type="button"
              key={m.user_id}
              onClick={() => assignDri(m.user_id)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent transition-colors"
            >
              <ActorAvatar
                name={m.name}
                initials={m.name.slice(0, 2).toUpperCase()}
                avatarUrl={m.avatar_url}
                size={16}
              />
              <span>{m.name}</span>
            </button>
          ))}
          {filteredMembers.length === 0 && filter && (
            <div className="px-2 py-3 text-center text-sm text-muted-foreground">
              {t(($) => $.lead.no_results)}
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
