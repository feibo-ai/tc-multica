---
layer: project
dri: ruibromt50142
plan: ../docs/plans/plan_2026-05-29_unified-project-tab.md
debriefed: 2026-06-01
---
# Case:unified-project-tab

**复盘 (Debrief):** 2026-06-01 · **DRI:** ruibromt50142 · **层级:** project
**计划 (Plan):** [plan_2026-05-29_unified-project-tab.md](../docs/plans/plan_2026-05-29_unified-project-tab.md) (v1.3, approved)
**研究 (Research):** [research_2026-05-29_unified-project-tab.md](../docs/research/research_2026-05-29_unified-project-tab.md)

## 1. 目标 (Goal) — *paste from plan*

> Replace the two top-level tabs **Issues** and **Projects** with a single tab at `/{slug}/projects` whose default view is an enriched project-dashboard card grid. Clicking a project opens a **deep-linkable** 3-column drill-down (`app sidebar | resident project list | that project's issues`), and the merged tab keeps a global **"All issues"** flat view as an in-tab sub-view toggle (hybrid model, à la Linear).
>
> This is a **reorganize-and-reuse** effort, not a from-scratch build.

## 2. 实际经过 (What actually happened) — *≤200 words*

Ran as RPI over ~3 days (plan 05-29 → implement 06-01). Research and a v1.1 plan were written, then a **second Claude session acting as staff engineer reviewed the plan** and returned *changes-requested* with **3 blocking findings**: an incomplete route-cutover inventory, an E2E landing chokepoint (`loginAsDefault` hard-codes `/issues`), and an unresolved 3-column panel conflict. The plan was revised to v1.3 (added Approach **B0**, folded the E2E fix into **B6**, resolved the panel count in **C2**) and approved **before any code**.

Implementation landed in plan order as atomic commits: **A** (`ProjectSchema` parse-guard, on a de-risked base) → **B** (one atomic route cutover + desktop tab `v3→v4` migration) → **C** (default card grid, 3-column drill-down rail, in-tab "All Issues" toggle) → **D2** (derived health badge). **D3 (`target_date`) was cut per the appetite, as planned**, with `ProjectSchema.loose()` left forward-compatible for the fast-follow.

Criteria 1–6 shipped with tests. The E2E specs were updated but **not executed** (needs the running stack), so criteria 7–8 are code-complete, not verified.

## 3. 完成标准 (Completion criteria) — met or not?

- [x] **#1 One nav entry.** Two `workspaceNav` entries collapsed into one at `paths.projects()` (kept `FolderKanban`); duplicate command-palette "issues" jump dropped. *(3077c24c)*
- [~] **#2 Default = card grid.** Grid is the default (view-store `compact→comfortable`); `issue_count` ring + **derived** health badge render and degrade gracefully (malformed-response test). **`target_date` field deferred** — its display rides on D3, which was cut. *(5b603861, bacf269d)*
- [x] **#3 Deep-linkable 3-column drill-down.** New `ProjectListRail` runs the same `projectListOptions` query so a cold deep link hydrates the list + selects the target; properties sidebar defaults collapsed via the panel lever; drill-down moved to its own `useDefaultLayout` id. *(5b603861)*
- [x] **#4 In-tab "All Issues".** `IssuesSurface` extracted from `IssuesPage`; `Projects | All Issues` segmented toggle; project-scoped issue view store kept isolated from the global one. *(ece043f4)*
- [x] **#5 No dead links + tab migration.** Every "home"-meaning `.issues()` caller repointed to `.projects()`; web `/{slug}/issues` redirect stub; desktop tab-store `v3→v4` path-renaming migration with a no-dropped/stale-tab migration test. *(3077c24c)*
- [x] **#6 Endpoint schema-guarded.** `listProjects` / `listProjectsWithoutDRI` / `getProject` route through `parseWithFallback`; malformed-response test (missing id, wrong type, null array) fails closed. *(8f9219ad)*
- [ ] **#7 E2E lands clean.** Spec code updated (`openAllIssues` helper; `issues.spec`/`comments.spec` open the view in `beforeEach` and after each reload), but **not run this session** — requires the running stack. *(10185923)* **Unverified.**
- [ ] **#8 Green pipeline (`make check`).** Not executed end-to-end this session (E2E above; `typecheck`/`pnpm test`/`make test`/slug-drift not run here). **Unverified — DRI must run `make check` before merge.**

**Net:** the merge itself (the actual goal, criteria 1–6) is code-complete with tests. The two pipeline-execution criteria (7, 8) are the open gate — code is in place, verification is not. `target_date` display (part of #2) is a deliberate appetite cut, not a miss.

## 4. 关键判断 (Key judgments)

#### Judgment 1: Cut D3 (`target_date`) entirely; ship health as a *derived* badge.
- **Context:** 1-week appetite. `health` and `target_date` were both net-new full-stack (migration → sqlc → API → schema → UI → activity listener). The plan named Phase D the shock absorber.
- **Options:** (a) stretch the week to build the `target_date` column; (b) cut both enrichments; (c) cut D3, but ship health *without* a backend column by deriving it.
- **Chose (c).** Derived `deriveProjectHealth()` from a signal already on `Project` — an active project with **no DRI** is "at risk" (reusing the documented SOP P-5 risk signal), with a DRI "on track", terminal states → no badge. No migration. `ProjectSchema.loose()` was left so a future `target_date` passes through with **zero client change**.
- **In hindsight:** right. The merge — the real goal — shipped inside the box; the heaviest, most-deferrable item absorbed the schedule pressure instead of the goal. Deriving health from DRI-presence turned a "need a backend column" into a free, meaningful signal.
- **"Ancient impossible" check:** No — this is Shape-Up appetite discipline, possible without AI. What *was* AI-native: the cut line and the forward-compatible schema were decided **at plan time**, so implementation never had to discover the cut under pressure.

#### Judgment 2: Build the 3-column drill-down by collapsing the existing properties panel — no new template, distinct persistence key.
- **Context:** The existing project-detail is a **2-panel** split (`issues | properties`). Naively adding a list rail = **4 visual columns**, and the v1.1 draft didn't say what happens to properties.
- **Options:** (a) build a generic master-detail template (research found none exists); (b) add the rail and live with 4 columns; (c) add the rail + default the properties sidebar collapsed.
- **Chose (c).** Added `ProjectListRail` as a new left `ResizablePanel`, defaulted properties **collapsed via the panel lever** (`sidebarRef.collapse()`, not the in-sidebar accordion), and — critically — moved the drill-down to its **own `useDefaultLayout` id** (`multica_project_drilldown_layout`) so the persisted collapsed state can't leak into the standalone project-detail page that reuses the same component.
- **In hindsight:** right. Pure reuse of `ResizablePanelGroup`, no parallel abstraction (honors the no-duplication / reuse rules). The shared-persistence-key leak would have been a subtle cross-page bug.
- **"Ancient impossible" check:** No — but the leak was caught by the **independent plan-review session before any code**, when it's nearly free to fix, rather than as a post-merge bug report.

#### Judgment 3: Treat the route cutover — not the backend enrichment — as the real schedule risk; land it as one atomic, grep-first PR.
- **Context:** The flashy work looked like Phase D (health/dates). The review found the actual risk was the **breadth of the route rename**: `paths.root()` plus ~6 scattered `.issues()` "home" callers plus a duplicate command-palette entry — all under-scoped in v1.1.
- **Options:** (a) incremental route changes phase-by-phase; (b) inventory every caller first (B0), then cut over atomically.
- **Chose (b).** Grep `/issues` / `.issues()` / `paths.root` → fix every home-meaning caller + web redirect stub + desktop tab `v3→v4` migration in **one commit**, so no intermediate state 404s or drops a tab.
- **In hindsight:** right, and the *reframing* was the valuable part — "the real schedule risk is the cutover breadth, not Phase D." The atomic landing is what kept any half-migrated state from existing.
- **"Ancient impossible" check:** No — atomic migrations are standard practice. AI-native lever: a second session re-scoped *what the risk actually was* cheaply, catching the under-scope at plan time.

## 5. 通用规则候选 (General rule candidates)

> Promotion test (SOP P-4): "does this apply to **ALL** future similar projects?" YES → `case_promote_rule`. ~10% promote.

- [ ] **A route/identifier rename must start with a grep-first inventory of every caller that means the renamed concept (not just the visible nav entry), and land as one atomic PR.** `needs DRI promotion decision` — *my read: strongest candidate.* Generalizes B0; route/identifier cutovers recur, and "atomic + inventory-first" prevents the half-migrated 404/dropped-tab class. This is plausibly the ~10%.

- [ ] **When deferring a net-new boundary field behind an appetite cut, make the response schema forward-accept it (`.loose()` / optional) in the *shipping* PR, so the fast-follow needs no client change.** `needs DRI promotion decision` — *my read: maybe; adjacent to the existing API Response Compatibility rule.* Useful, but narrower than a standalone rule.

- [ ] **When a shared component persists UI/layout state under a fixed key and is reused in a second context, give the new context a distinct persistence key — a shared key silently leaks state across pages.** `needs DRI promotion decision` — *my read: likely stays local.* Real footgun, but niche (only components with persisted layout). Keep in this case file as a reusable note.

> **Examined, NOT a new candidate:** "promote an endpoint to a primary surface ⇒ it must have a `parseWithFallback` schema" — **already covered** by CLAUDE.md *API Response Compatibility*. Phase A applied the existing rule; it doesn't need a new one.

## 交接 (Hand-off)
- **Open gate before merge:** run `make check` (criteria #7/#8) on the running stack — code is in place, execution is not. This is the one thing standing between "code-complete" and "done."
- **Fast-follow:** D3 `target_date` (migration → sqlc → `Create/UpdateProjectRequest` → date picker → `due_date_changed`-style activity listener). `ProjectSchema.loose()` already accepts the field, so the client side is a display-only add.
- **Branch note:** this work shares the `chore/t3-backlog` branch with the T3 task-layer items (T3-1…T3-7, Zeabur, pg18) — those are weekly-bucket task layer, out of scope for this project case.
- Present at Friday Demo with the real merged tab (not slides).
