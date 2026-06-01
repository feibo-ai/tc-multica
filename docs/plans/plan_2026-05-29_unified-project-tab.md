---
version: 1.3
layer: project
dri: ruibromt50142
---
# 计划:unified-project-tab

**创建:** 2026-05-29
**DRI:** ruibromt50142
**层级:** project

## 目标 (Goal)

Replace the two top-level tabs **Issues** and **Projects** with a single tab at `/{slug}/projects` whose default view is an enriched project-dashboard card grid. Clicking a project opens a **deep-linkable** 3-column drill-down (`app sidebar | resident project list | that project's issues`), and the merged tab keeps a global **"All issues"** flat view as an in-tab sub-view toggle (hybrid model, à la Linear).

This is a **reorganize-and-reuse** effort, not a from-scratch build: the card grid ([projects-page.tsx:34-125](packages/views/projects/components/projects-page.tsx#L34)), the compact project row ([projects-page.tsx:127-186](packages/views/projects/components/projects-page.tsx#L127)), the project-detail issues split ([project-detail.tsx:696-811](packages/views/projects/components/project-detail.tsx#L696)), and the issue board/list/swimlane views all already exist and are reused.

### DRI decisions (locked 2026-05-29, drove this plan)
1. **Strategy** → **Hybrid (Approach 2)**: merge to one tab, keep an in-tab "All issues" view.
2. **Middle column** → **Reuse the compact list** (`ProjectCardCompact`) as the resident left rail.
3. **Selection** → **URL route, deep-linkable** (`/{slug}/projects/:id`), not Runtime-style local state.
4. **Card fields** → **Add health + target date + issue count** to the grid cards.
5. **Route name** → **Reuse `projects`**; retire `issues` route (redirect old links); desktop default landing moves `/issues → /projects`.
6. **"All issues" placement** → **Sub-view toggle inside the merged tab** (no separate route).

## 完成标准 (Completion criteria)
Observable signals — each must be demonstrable, not "looks done":

- [ ] **One nav entry.** The sidebar shows a single merged entry where "Issues" + "Projects" were ([app-sidebar.tsx:140-147](packages/views/layout/app-sidebar.tsx#L140)); it routes to `/{slug}/projects`. No second top-level entry for issues remains.
- [ ] **Default = card grid.** Entering the merged tab shows the project card grid by default. Cards render ring progress, status, lead, **issue count**, **health badge**, and **target date**, each degrading gracefully (no crash / no blank card) when its field is absent — verified by a malformed-response test.
- [ ] **Deep-linkable 3-column drill-down.** Visiting `/{slug}/projects/:id` *directly* (cold deep link / refresh) renders `app-sidebar | resident project list | that project's issues`, with the list populated and the target project selected — not a blank or "no list" state. The existing properties sidebar is present but **collapsed by default**, so the default reading is three columns (see Approach C2).
- [ ] **In-tab "All issues".** A view toggle inside the merged tab shows the cross-project flat issue list (board/list/swimlane) with today's filter/sort behavior preserved.
- [ ] **No dead links — in-app AND external.** (a) Every in-app navigation that meant "workspace home" now targets the merged tab — `paths.root()` and all `paths.*.issues()` push/href sites are repointed (Approach B0). (b) Old external deep links `/{slug}/issues` and `/{slug}/projects` resolve (redirect, no 404) on **web**; the post-login cookie landing renders. (c) On **desktop**, persisted tabs survive the tab-store version bump with **zero** dropped or stale-titled tabs (assert in a migration test).
- [ ] **E2E lands clean.** The shared `loginAsDefault` helper ([e2e/helpers.ts:28-29](e2e/helpers.ts#L28)) and any spec asserting a `/issues` URL (e.g. `navigation.spec.ts`) are updated to the merged route — *before* the rest of the suite runs, or every spec flakes.
- [ ] **Endpoint is schema-guarded.** `listProjects` / `getProject` parse through a `ProjectSchema` via `parseWithFallback`, with a test feeding a malformed body (missing field, wrong type, null array) that fails closed.
- [ ] **Green pipeline.** `make check` passes: `pnpm typecheck`, `pnpm test`, `make test`, E2E, **and** the reserved-slug generator drift check.

## 分工 (How to split)
> Proposed defaults — DRI confirms / adjusts via the `tc-roles` skill. No team roster was available at plan time, so only the DRI is named.

- **DRI:** ruibromt50142 — owns scope, the appetite call on Phase D, and the route-name cutover.
- **EXEC:** ruibromt50142 + Claude Code (Implement session). Frontend reorganization + the route migration are one person's critical path.
- **COLLAB:** _TBD_ — **if `server/` has a separate owner**, invite them for the Phase D backend migration (target_date column, activity listener) and the `ProjectSchema` contract. Pure-frontend phases need no collaborator.
- **REVIEW:**
  - *Plan review (this gate):* a second Claude session as **staff engineer** — verdict recorded in **## 评审** below before any code.
  - *Code review (pre-merge):* human or second session on the route-migration + tab v3→v4 PR specifically (highest blast radius).

## 投入预算 (Appetite)
**1 week** (project layer, Shape Up appetite — a limit, not an estimate).

⚠️ **Appetite tension to resolve at the review gate / Monday kickoff:** Phases A–C (the merge itself, reuse-driven) fit comfortably. **Phase D is the risk.** `issue_count` is free, but **`health` and `target_date` are net-new full-stack additions** (migration → sqlc → API → schema → create/edit UI → activity listener). If the week gets tight, the proposed cut line is: **ship A–C + issue_count + a derived health badge in v1; land `target_date` (and an explicit health enum, if product wants one) as a fast-follow.** This keeps the merge — the actual goal — inside the box and treats the heaviest enrichment as the shock absorber. DRI rules on whether to stretch the week or take the cut.

## 研究输入 (Research input)
[docs/research/research_2026-05-29_unified-project-tab.md](../research/research_2026-05-29_unified-project-tab.md)

## 方案 (Approach)

Sequenced so the **risky, irreversible** work (route/slug/tab migration) lands on a **de-risked base**, and the **appetite shock absorber** (Phase D backend) is last and severable.

### Phase A — Close the API-compatibility gap *(do first; low risk, unblocks the dashboard)*
Today `listProjects`/`getProject` return raw bodies — there is **no `ProjectSchema`** ([api/schemas.ts] has User/Issue/Squad/Integration schemas but none for Project; [client.ts:31](packages/core/api/queries.ts) returns `res.projects` unparsed). The merged tab makes the projects endpoint a far more prominent surface, so per CLAUDE.md *API Response Compatibility* and research pitfall #2143/#2147/#2192:

- **A1.** Add `ProjectSchema` (+ `ListProjectsResponseSchema`) to `packages/core/api/schemas.ts`, mirroring the `Project` type ([project.ts:5-26](packages/core/types/project.ts#L5)). Treat every field as possibly-missing; `status`/`priority` as `z.string()` with a `default`/`catch` so enum drift downgrades, never crashes.
- **A2.** Route `listProjects` / `listProjectsWithoutDRI` / `getProject` through `parseWithFallback` with an explicit empty fallback.
- **A3.** Add a malformed-response test (missing field, wrong type, `null` array) that fails closed. *Completion criterion #6.*

### Phase B — Route migration & navigation cutover *(the load-bearing, highest-blast-radius change — one atomic PR)*
All of this lands **together** so no intermediate state 404s or drops tabs. **Do B0 — the full cutover inventory — first**; the v1.1 draft under-scoped this (review finding) and it is the real schedule risk, not Phase D.

- **B0. Cutover inventory (do first).** Grep the repo for `/issues`, `.issues()`, and `paths.root` and fix every in-app caller that means "workspace home", not only the nav array:
  - `paths.root()` ([paths.ts:20](packages/core/paths/paths.ts#L20)) returns `${ws}/issues` — the post-login landing helper. Repoint to `${ws}/projects` and confirm its consumers (no-access page, invite / onboarding / new-workspace flows, desktop `window-overlay`) still land correctly.
  - Post-join / post-create pushes: [app-sidebar.tsx:427](packages/views/layout/app-sidebar.tsx#L427) (push after join) and the workspace-switcher `AppLink` (~:521); `create-workspace.tsx:69`, `invite-page.tsx:88`, `invitations-page.tsx:115` — all currently `push(...issues())`.
  - Command palette: `search-command.tsx` has **both** an "issues" and a "projects" page-jump entry (~:169-170, dispatched ~:468); after merge they collide — drop the `issues` entry or relabel to the merged tab.
- **B1. Nav:** collapse the two `workspaceNav` entries ([app-sidebar.tsx:140-147](packages/views/layout/app-sidebar.tsx#L140)) into one pointing at `paths.projects()`. Pick the merged-tab icon (keep `FolderKanban`, or choose a neutral one).
- **B2. Reserved slugs:** keep `projects`; **keep `issues` reserved too** (redirect placeholder, not a free slug) in [reserved_slugs.json](server/internal/handler/reserved_slugs.json) → run `pnpm generate:reserved-slugs` → commit both JSON + generated [reserved-slugs.ts](packages/core/paths/reserved-slugs.ts). *CI drift check is a completion criterion.*
- **B3. Desktop defaults:** change the workspace-entry redirect [routes.tsx:118](apps/desktop/src/renderer/src/routes.tsx#L118) `/issues → /projects`, and `defaultPathFor`/`defaultTabFor` ([tab-store.ts:224-231](apps/desktop/src/renderer/src/stores/tab-store.ts#L224)). Update `ROUTE_ICONS` ([tab-store.ts:122-132](apps/desktop/src/renderer/src/stores/tab-store.ts#L122)).
- **B4. Desktop tab persistence bump.** Bump the persisted schema version ([tab-store.ts:574](apps/desktop/src/renderer/src/stores/tab-store.ts#L574)). The migration's job is **path renaming** — rewrite persisted `/{slug}/issues` (and `/{slug}/projects`) tab paths to the merged route and refresh title/icon. (Correction to v1.1: `sanitizeTabPath` ([tab-store.ts:177-193](apps/desktop/src/renderer/src/stores/tab-store.ts#L177)) would *not* drop these — the workspace slug is the first segment and isn't reserved — so the real risk is a **stale title/icon**, not a dropped tab.) Decide explicitly whether to keep the desktop `issues` route ([routes.tsx:120](apps/desktop/src/renderer/src/routes.tsx#L120)) as a redirect for un-migrated tabs or remove it and let `WorkspaceRouteLayout` auto-heal. Migration test asserts no dropped/stale tabs; do **not** disturb the per-workspace tab-grouping invariant.
- **B5. Web redirects:** there is no `apps/web/middleware.ts`. Keep a thin `apps/web/app/[workspaceSlug]/(dashboard)/issues/page.tsx` that redirects to the merged tab. (Note: the bare `/` → workspace-home redirect is performed by an **external proxy**, not Next.js code — see [no-access-page.tsx:23](packages/views/layout/no-access-page.tsx#L23); the only in-repo guarantee is that `/{slug}/issues` resolves, which the stub provides. Don't promise proxy changes inside the appetite.) The merged tab stays a **session route**, never a WindowOverlay.
- **B6. E2E + i18n + conventions:** update the shared `loginAsDefault` helper ([e2e/helpers.ts:28-29](e2e/helpers.ts#L28)) and any `/issues`-asserting spec (`navigation.spec.ts`) to the merged route **first**. Merge the `nav.*` label and reconcile `issues.json`/`projects.json` strings in `packages/views/locales/{en,zh-Hans}/`; update the workspace-scoped-routes section of [conventions.mdx](apps/docs/content/docs/developers/conventions.mdx) (+ `.zh.mdx`).

### Phase C — Merged-tab UI *(pure reuse in `packages/views/`)*
- **C1. Default card grid:** the merged tab default = the existing `comfortable` grid ([projects-page.tsx:381](packages/views/projects/components/projects-page.tsx#L381)). Render `issue_count` now (free). Wire health/target-date display so they no-op gracefully until Phase D supplies data.
- **C2. Deep-linkable 3-column drill-down:** clicking a card → `useNavigation().push(paths.project(id))` → `/{slug}/projects/:id`.
  - **Panel-count resolution (review finding — was a hole in v1.1).** Today's `ResizablePanelGroup` in [project-detail.tsx:696-811](packages/views/projects/components/project-detail.tsx#L696) is **2 panels**: `id="content"` (the issues surface, minSize 50%) then `id="sidebar"` (project properties — DRI / status / description / resources — which is **already collapsible**, `defaultSize 0` when closed). Naively inserting a list rail would make 4 visual columns. **Decision:** add the resident list as a new left panel and **default the properties sidebar to collapsed** in the drill-down, so the default reading is the intended three columns `app-sidebar | project list | project issues`; properties stays one click away via its existing toggle. Drive the collapse via the **panel lever** `desktopSidebarOpen` / `sidebarRef.collapse()` ([project-detail.tsx:419/760](packages/views/projects/components/project-detail.tsx#L419)) — *not* the in-sidebar `propertiesOpen` accordion ([:406](packages/views/projects/components/project-detail.tsx#L406)), which collapses sections *within* the sidebar, not the column.
  - ⚠️ **Persistence (confirmation-review finding — fold into the same work).** Split sizes persist under one shared key `useDefaultLayout({ id: "multica_project_detail_layout" })` ([project-detail.tsx:411](packages/views/projects/components/project-detail.tsx#L411)), and the **same** `ProjectDetail` component (signature `{ projectId }` only) renders on the standalone project-detail page too. So a `useState`-default-collapse alone is **not** robust: the rehydrated persisted layout overwrites the default, and `onLayoutChanged` writes the collapsed state back to that shared key — leaking it to the standalone page. **Fix (in-appetite):** give the drill-down a **distinct `useDefaultLayout` id** (or add a `layoutId` / `defaultSidebarCollapsed` prop to `ProjectDetail`). Pick one in C2, don't leave it to "just collapse it."
  - Build the rail from `ProjectCardCompact` ([projects-page.tsx:127](packages/views/projects/components/projects-page.tsx#L127)); it runs its own `projectListOptions` query — the current `ProjectDetail` has **no** list, so this list-load + selected-state is the net-new work, not the route.
  - **Deep-link wiring (research Android two-pane pitfall):** a cold load of `/projects/:id` must hydrate the resident list *and* select the target — not assume the user arrived via the grid. Both `apps/web/.../projects/[id]/page.tsx` and the desktop project-detail page already render `<ProjectDetail projectId={id} />` on cold load, so the route itself is fine; the new work is specifically the rail.
- **C3. "All issues" in-tab toggle:** a view switch inside the merged tab that swaps the card grid for the cross-project flat issue list, reusing the issues board/list/swimlane and the existing `view-store` filter/sort. Keep the project-scoped issue view store ([project-detail.tsx:111](packages/views/projects/components/project-detail.tsx#L111)) isolated from this global one.

### Phase D — Card enrichment backend *(severable shock absorber; see Appetite)*
- **D1. issue count:** already on `Project` — frontend-only, done in C1.
- **D2. health badge:** **no `health` field exists.** Cheapest path is a **derived** badge (e.g. red when `dri_user_id == null` — already the documented P-5-risk signal — or overdue / stalled), needing no migration. If product wants an explicit `on-track/at-risk/off-track` enum, that becomes a backend column (migration + compute + API + schema). **Recommend derived for v1.**
- **D3. target date:** **net-new** — projects have no date columns. Add `target_date` (migration + sqlc + `Create/UpdateProjectRequest` + `ProjectSchema` field from A1 + a date picker in project create/edit + a `due_date_changed`-style activity listener, mirroring the issue one at [activity_listeners.go:174](server/cmd/server/activity_listeners.go#L174)). Heaviest item; first to defer if the week is tight.

### Out of scope
- **Mobile** (`apps/mobile/`) — independent; shares only `import type`. If `Project` gains `target_date`, that's a type-only change, zero runtime coupling. No mobile screens in this effort.
- A generic "master-detail template" component — research found none exists and none is needed; reuse `ResizablePanelGroup` directly.
- `My Issues` / Inbox behavior — `personalNav` "My Issues" is a *filtered personal* view, distinct from the new "All issues"; left as-is unless review finds a routing dependency.

## 评审 (Review)
- **Reviewer:** second Claude session, staff-engineer role (read-only, independent codebase verification).
- **Reviewed:** 2026-05-29 — round 1 against v1.1, confirmation pass against v1.2.
- **Verdict:** ✅ **APPROVED** (confirmation pass on v1.2). v1.3 folds in the one non-blocking refinement from that pass. Cleared for hand-off to Implement.

**Blocking findings (v1.1) → resolution in v1.2:**
1. *Route cutover incomplete* — `paths.root()` and ~6 scattered `paths.*.issues()` push/href sites + the command-palette duplicate entry were not inventoried. → Added **Approach B0** (cutover inventory, done first) + the "No dead links — in-app AND external" completion criterion.
2. *E2E landing chokepoint* — shared `loginAsDefault` ([e2e/helpers.ts:28-29](e2e/helpers.ts#L28)) and `navigation.spec.ts` hard-code `/issues`, so "E2E green" was unachievable. → Folded into **B6** (fix first) + a dedicated "E2E lands clean" criterion.
3. *3-column panel-count conflict* — existing project-detail split is `issues | properties-sidebar` (2 panels); adding a list rail = 4 columns, and the draft didn't say what happens to properties. → **C2** now resolves it: properties sidebar defaults to collapsed (reusing its existing toggle) so the default reading is three columns.

**Non-blocking, also applied:** corrected stale line refs (project-detail panel group is 696-811, not 296; panel order is issues-then-properties); reframed the desktop tab migration as path-*renaming* (sanitize wouldn't drop these — the risk is a stale title/icon); noted the bare-`/` redirect is proxy-owned, out of repo reach.

**Verified sound (no change needed):** A→B→C→D sequencing with D severable; reuse targets (`ProjectCardCompact`, `comfortable` grid, `ResizablePanelGroup`) all exist; no `ProjectSchema` today (Phase A justified); `issue_count`/`done_count` present while `health`/`target_date` are genuinely net-new; reserved-slug + desktop touchpoints accurate; no CLAUDE.md hard-rule violation. **Appetite call:** 1 week fits **A + B + C + D1 + D2 (derived health)** *iff* the B0/B6/C2 surface is treated as in-scope from the start; **D3 (`target_date`) stays cut from v1** by default.

**Confirmation pass (v1.2 → approved):** verified all three blockers closed against the code (B0 covers `paths.root()` + the scattered `.issues()` sites including `issue-detail.tsx`'s "back to issues" targets via criterion-#5's "all"; B6 names `loginAsDefault`; C2's default-collapse is achievable via `sidebarRef.collapse()`). One non-blocking refinement raised — the shared `multica_project_detail_layout` persistence key would leak the collapsed state to the standalone page — now folded into C2 (distinct layout id / prop) as **v1.3**. No open blockers remain.

## 当前状态 (handoff @ 2026-05-29 18:39 · 见 tc-handoff skill)

**Last commit / 基线**: `c1ff0c7f docs(research): unify-project-tab research findings + plan handoff` —— 本次「approved plan v1.3」作为一个 commit 落在其上(新 session `git log` 即见)。
**Worktree**: `chore/t3-backlog`,本次交接 1 file changed(本计划文件)。状态 ✅ **Plan approved,cleared for Implement** —— 尚无代码(RPI:Plan ≠ Implement)。

**What's done (本 session 完成)**:
- Plan session 完成:以 research 为输入,经 DRI 6 项决策(hybrid 合并 / 复用 compact list / URL 深链 / 卡片增强 / 路由名 `projects` / All-issues 标签内切换)写出完整计划。
- 4 项强制字段齐全(目标 / 完成标准 / 分工 / 预算);Approach 拆为 A(ProjectSchema)→ B(路由迁移,含 B0 清点)→ C(合并标签 UI)→ D(卡片增强,D3 默认砍掉)。
- 代码核查校准范围:`issue_count` 免费、`health`/`target_date` 全新、Projects 无 `ProjectSchema`。
- 评审关卡:staff-engineer session 给 changes-requested(3 阻断项)→ 已修 → 确认复审 **APPROVED**(v1.3 又补入 1 条非阻断的持久化改进)。

**Next action (可冷启动 — 给 Implement session)**:
1. 主输入 = 本计划 + 它引用的 research。动代码前先读两份。
2. 从 **Approach B0** 起手(路由切换清点):先 grep `/issues` / `.issues()` / `paths.root`,确认完整调用方清单再改。这是真正的进度风险,不是 Phase D。
3. A → B → C 作为各自连贯的 commit 落地;**D3(`target_date`)默认从 v1 砍掉** —— 扩范围前先问 DRI。
4. 留给 Implement/DRI 的实现期决定:合并标签图标(B1)、desktop `issues` 路由保留与否(B4)、derived-health 语义(D2)、C2 持久化用独立 `useDefaultLayout` id 还是给 `ProjectDetail` 加 prop。

**Dead ends — do NOT retry (死胡同)**:
- 尚无实现尝试,故无失败路径。
- ⚠️ 勿假设 Runtime 是三栏模板(它是 2 栏);勿用 `propertiesOpen` 驱动 C2 的整栏折叠(用 panel lever `desktopSidebarOpen`/`sidebarRef`)。

**Context pollution signal (为何交接)**:
- 无污染。按 RPI 纪律正常交接:Plan 已获批,`/clear` 后另开 Implement session,避免 Plan/评审 的 context 挤占执行。
