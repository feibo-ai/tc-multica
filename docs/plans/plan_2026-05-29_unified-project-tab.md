---
version: 1.0
layer: project
dri: ruibromt50142
---
# 计划:unified-project-tab

**创建:** 2026-05-29
**DRI:** ruibromt50142
**层级:** project

## 目标
Merge the Projects and Issues top-level tabs into one: default to a project-dashboard card grid (each card shows key project info), and clicking a project opens a sidebar | project-list | issues 3-column layout modeled on the Runtime (运行时) tab.

## 完成标准
- [ ] TBD (DRI 在 Research 后填写)

## 分工
- DRI: ruibromt50142
- EXEC: _(invoke role-assignment-protocol skill)_
- COLLAB: _(invoke role-assignment-protocol skill)_
- REVIEW: _(assign before Implement phase)_

## 投入预算
1 week

## 研究输入
docs/research/research_2026-05-29_unified-project-tab.md

## 方案
_(Research 后填写)_

## 评审
- Reviewer: _(pending)_
- Reviewed: _(pending)_
- Verdict: pending

## 当前状态(交接槽 · 见 pre-clear skill)

**交接时间**: 2026-05-29 18:08
**基线 commit**: 40bc6d0c i18n(integrations): translate config-form labels/hints/titles (T3-4)
**Worktree**: `chore/t3-backlog` —— 本次 Research 的 `docs/research/` + `docs/plans/` 作为一个 commit 落在基线之上(新 session `git log` 可见)。

**已完成 (What's done)**:
- Research session 完成,findings 写入 [research_2026-05-29_unified-project-tab.md](../research/research_2026-05-29_unified-project-tab.md)(4 维度:现有代码/先例/陷阱/约束 + 待解问题 + 推荐方案)。
- 关键结论:**目标 UI 大部分已存在** —— 项目卡片网格 `projects-page.tsx:381`、项目详情 issues 分栏 `project-detail.tsx:296`,均可复用。
- 前提校准:Runtime 实为 **2 栏(非 plan 所述 3 栏)**;选中用本地 React state、**不可深链**。
- 风险面已摸清:保留 slug(`issues`/`projects` 皆保留字)、desktop 默认落地硬编码 `/issues`、tab 持久化需 v3→v4 迁移、web 无重定向中间件(老链 404)。

**下一步 (Next action — 可冷启动)**:
1. 开 **Plan session**,以 `docs/research/research_2026-05-29_unified-project-tab.md` 为主输入。
2. ❗ Plan 前 DRI 先答**开放问题 #1**:合并后全局"所有 issues 扁平视图"去留 —— 此答案决定走推荐方案 **1(彻底合并)/ 2(hybrid 保留 All-Issues)/ 3(渐进先打地基)** 中哪条。
3. 回填本 plan 的「完成标准」与「方案」两节(现为 TBD),并走 role-assignment 定 EXEC / REVIEW。

**死胡同 — 勿重试 (Dead ends)**:
- 本 session 为纯 Research,无失败尝试。
- ⚠️ 唯一需纠偏的前提:**别假设 "Runtime 已提供三栏模板"** —— 它是 2 栏;三栏中间的"项目列表"列需新做,或由卡片网格收窄而来。

**清理原因 (Context pollution signal)**:
- 无污染。按 RPI 纪律正常交接:Research 完成即 `/clear`,避免研究 context 挤占独立的 Plan session。
