import { redirect } from "next/navigation";
import { paths } from "@multica/core/paths";

// The Issues and Projects top-level tabs merged into one unified tab at
// /{slug}/projects, whose in-tab "All issues" sub-view replaces the old
// standalone issues list. This server-side redirect keeps old external deep
// links / bookmarks to /{slug}/issues resolving instead of 404ing — the
// merge retired the list route but kept the `issues` slug reserved.
// Individual issue pages at /{slug}/issues/:id are unaffected; only the
// list route merged.
export default async function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(paths.workspace(workspaceSlug).projects());
}
