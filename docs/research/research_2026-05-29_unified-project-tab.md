# 研究:统一 Projects 与 Issues 标签页 (Unify Projects and Issues tabs)

> Research session · 2026-05-29 · DRI: ruibromt50142
> 本文件只「画地图」,不做架构决策、不选方案、不写代码 —— 那是 Plan / Implement session 的事。

## 问题

我们想搞清楚:把顶层的 **Projects** 和 **Issues** 两个标签合并成一个标签,需要动什么。
目标形态(来自 plan):合并后的标签默认展示**项目仪表盘卡片网格**(每张卡片显示项目关键信息);点击某个项目后,进入一个 **`导航侧栏 | 项目列表 | 该项目的 issues`** 三栏布局,**参照运行时(Runtime / 运行时)标签**。

四个维度去摸清:(1) 现在这两个标签和运行时标签到底怎么实现的;(2) 同类产品怎么做;(3) 合并/移除一个顶层标签会踩哪些坑;(4) 团队 SOP / 跨端架构有哪些硬约束。

---

## 发现

### 现有代码

> ⭐ **最关键的结论:目标 UI 的两块硬骨头其实已经存在。** 项目卡片网格已实现、"项目侧栏 + 该项目 issues" 的可调整分栏布局也已实现。这件事更像"**重组导航 + 复用已有组件**",而不是"从零搭一个新标签"。

**顶层导航(要在哪里加/删/改标签)**
- 侧栏导航定义:[app-sidebar.tsx:140-147](packages/views/layout/app-sidebar.tsx#L140) —— `workspaceNav` 静态数组,`Issues` (`ListTodo` icon, key `"issues"`) 与 `Projects` (`FolderKanban` icon, key `"projects"`) 都在这里。渲染循环 [app-sidebar.tsx:680-695](packages/views/layout/app-sidebar.tsx#L680),label 经 `t(($) => $.nav[item.labelKey])` 走 i18n。
- 路径构造器:[paths.ts:17-44](packages/core/paths/paths.ts#L17) —— `issues()` → `/{slug}/issues`,`projects()` → `/{slug}/projects`。所有共享代码通过这个 `paths` helper + `useNavigation().push()` 导航。
- i18n label:[locales/en/layout.json:2-14](packages/views/locales/en/layout.json#L2)(`nav.issues` / `nav.projects`),中文对应 `locales/zh-Hans/layout.json`。

**Issues 标签(被合并方之一)**
- Web 路由:`apps/web/app/[workspaceSlug]/(dashboard)/issues/page.tsx`(+ `issues/[id]/page.tsx` 详情)。
- Desktop 路由:[routes.tsx:120-132](apps/desktop/src/renderer/src/routes.tsx#L120)。
- 视图组件:[issues-page.tsx:32-251](packages/views/issues/components/issues-page.tsx#L32) —— board / list / swimlane 三种视图模式。
- 状态:`view-store.ts`(viewMode/过滤/排序,按 workspace 持久化)、`selection-store.ts`、`issues-scope-store.ts`(members/agents/all)、`draft-store.ts`,均在 [packages/core/issues/stores/](packages/core/issues/stores/)。
- 数据:[queries.ts:227-233](packages/core/issues/queries.ts#L227) `issueListOptions(wsId, sort?)`,按 status 分桶并行拉取后拍平成 `Issue[]`。

**Projects 标签(被合并方之二)—— 卡片网格已存在**
- Web 路由:`apps/web/app/[workspaceSlug]/(dashboard)/projects/page.tsx`(+ `projects/[id]/page.tsx` 详情);Desktop [routes.tsx:134-142](apps/desktop/src/renderer/src/routes.tsx#L134)。
- 视图组件:[projects-page.tsx:188-391](packages/views/projects/components/projects-page.tsx#L188)。
  - ⭐ **卡片网格已实现**:`comfortable` 模式 [projects-page.tsx:381-385](packages/views/projects/components/projects-page.tsx#L381) 用 `grid-cols-1 sm:grid-cols-2 lg:grid-cols-4` 渲染 `ProjectCard`([projects-page.tsx:34-125](packages/views/projects/components/projects-page.tsx#L34)),卡片含**环形进度、状态、lead 选择器**。
  - 另有 `compact` 表格模式(`ProjectCardCompact` [127-186](packages/views/projects/components/projects-page.tsx#L127):icon/名称/优先级/状态/进度/lead/创建时间)。两种模式由 [view-store.ts:15-31](packages/core/projects/stores/view-store.ts#L15) 切换。
- 数据:[queries.ts:19-34](packages/core/projects/queries.ts#L19) `projectListOptions(wsId, { withoutDri? })`。

**Project 详情 —— "项目侧栏 + 该项目 issues" 的分栏布局已存在**
- ⭐ [project-detail.tsx:296-330](packages/views/projects/components/project-detail.tsx#L296) 已是一个 `ResizablePanelGroup`:Panel 1 = 项目侧栏(属性/描述/resources/DRI),Panel 2 = **该项目的 issues**,且**复用了全部标准 issue 视图组件**(board/list/swimlane/gantt)。
- 它用独立的 view store:[project-detail.tsx:111](packages/views/projects/components/project-detail.tsx#L111) `createIssueViewStore("project_issues_view")`,与全局 issues 视图偏好隔离。
- 项目维度 issues 通过 `issueListOptions` 带 `project_id` 过滤,gantt 走 [queries.ts:327](packages/core/issues/queries.ts#L327) `projectGanttIssuesOptions`。
- ❗ 目前**没有** `/projects/{id}/issues` 这种独立路由;该项目的 issues 只内嵌在 project 详情页里。

**Runtime / 运行时标签(plan 指定的"模板")—— 实际是 2 栏,不是 3 栏**
- 位置:Web `apps/web/app/[workspaceSlug]/(dashboard)/runtimes/page.tsx` → 视图 [runtimes-page.tsx:84-278](packages/views/runtimes/components/runtimes-page.tsx#L84);Desktop [routes.tsx:159-166](apps/desktop/src/renderer/src/routes.tsx#L159)(经 `desktop-runtimes-page.tsx` 注入 daemon 状态)。
- ❗ **当前是 2 栏:`MachineSidebar | MachineDetail`**(若把全局 app 侧栏算进去,视觉上才是 3 栏 —— 这正好对应 plan 说的 `导航侧栏 | 项目列表 | issues`)。
- 布局原语:[runtimes-page.tsx:228-266](packages/views/runtimes/components/runtimes-page.tsx#L228) 用 `ResizablePanelGroup`(横向)。列 1 `defaultSize 300 / min 240 / max 420`,列 2 `min 45%`;布局经 `useDefaultLayout("multica_runtimes_layout")` 持久化。
- ⭐ **选中机制是本地 React state,不是 store、不是 URL**:[runtimes-page.tsx:98-108](packages/views/runtimes/components/runtimes-page.tsx#L98) `selectedMachineId` + 自动选中逻辑 [161-176](packages/views/runtimes/components/runtimes-page.tsx#L161)。点列表项 → 右栏就地更新。**而进一步点到 runtime 行才走 URL 导航**到独立详情页 `/runtimes/:id`([runtime-list.tsx:163-170](packages/views/runtimes/components/runtime-list.tsx#L163))。
- 移动端:[runtimes-page.tsx:203-225](packages/views/runtimes/components/runtimes-page.tsx#L203) `useIsMobile()` 时退化成单列堆叠(不用 resizable panel)。空状态/溢出截断都有处理。

**复用资产 vs 各页自造**
- `ResizablePanelGroup/Panel/Handle` 是 [packages/ui/.../resizable.tsx](packages/ui/components/ui/resizable.tsx) 的通用原语(react-resizable-panels 薄封装),Runtime / Inbox / Project 详情都在用。
- ❗ **但没有"master-detail"通用模板组件**:Runtime 的 `MachineSidebar`/`MachineDetail`、Inbox 的 2 栏([inbox-page.tsx:450-479](packages/views/inbox/components/inbox-page.tsx#L450))、Project 详情的分栏,各自手写。三处共享的只是 `ResizablePanelGroup` + `useDefaultLayout` 这套底座。

### 先例(Prior art)

> 一句话:**主流工具几乎都把"项目总览"和"跨项目 issues 列表"分开**;要看某个项目的 issues,是"进到项目里"看,而不是过滤全局列表。真正合并成单标签的代价,业界文档反复指向同一个 —— **丢失快速的跨项目"所有 issues"扁平视图**。

- **导航结构**(均为分离派或单一侧栏树派,几乎无人把 projects+issues 并成一个顶层标签):
  - Linear:Projects 是跨 team 的正交分组,有 workspace 级 Projects 页;单个项目页内部才有 **Overview / Issues 子标签**,全局 issues 独立。([conceptual-model](https://linear.app/docs/conceptual-model)、[projects](https://linear.app/docs/projects))
  - Asana:**My Tasks**(跨项目任务)与 **Portfolios**(项目集合的健康/工作量)分层;用户长期求一个"组合内全部任务扁平视图"而默认没有。([portfolios](https://help.asana.com/s/article/portfolios-overview)、[论坛诉求](https://forum.asana.com/t/one-consolidated-view-to-see-all-tasks-from-all-projects-within-the-portfolio/1045395))
  - Jira:Projects 目录 vs Issue Navigator/All work 分离;Cloud 正把独立 "All work" 并进 List(只是 issue 侧内部合并,不是 projects+issues 合并)。([issue-navigator](https://support.atlassian.com/jira-software-cloud/docs/enable-the-project-issue-navigator/))
  - GitHub:Issues 与 Projects **就是两个顶层标签**,社区反映两者间来回跳转有摩擦(返回/直链 bug)。([issues](https://github.com/features/issues)、[discussion#4678](https://github.com/orgs/community/discussions/4678))
  - 反例(单侧栏树/把"项目"降级):ClickUp 的 **"Everything"** 视图统一全部任务居于层级之上;Notion 纯 teamspace 树;Height 把 **"Projects" 也建模成一个 list**;Shortcut 把 Projects 降级出主导航、改以 epic/story 为中心。([ClickUp 层级](https://help.clickup.com/hc/en-us/articles/13856392825367-Intro-to-the-Hierarchy)、[Height](https://help.height.app/en/articles/3606831-height-overview)、[Shortcut 新导航](https://www.shortcut.com/blog/a-quick-look-at-our-new-layout-and-navigation))
- **项目卡片显示什么**:最被强调的两项是 **健康度色块(On track/At risk/Off track)** 与 **进度**;此外常见 lead/owner、目标日期、issue/task 计数、工作量。(Linear [initiatives](https://linear.app/docs/initiatives);Asana [portfolios](https://asana.com/features/goals-reporting/portfolios);卡片设计 [bricxlabs](https://bricxlabs.com/blogs/card-ui-design-examples))
- **master-detail / 多栏下钻的坑**(微软/Oracle/OutSystems/Android 文档):
  - 并排只适合宽视口,**窄屏会塌成单栏**(微软断点 320–640epx 堆叠 / 641+ 并排;OutSystems/Oracle ~720px),窄屏下"选中即替换列表",**用户感觉像两个独立页面**,必须**显式做返回**。([MS list-details](https://learn.microsoft.com/en-us/windows/apps/design/controls/list-details)、[Android twopane](https://developer.android.com/develop/ui/views/layout/twopane))
  - **深链到某个选中项需要刻意接线**(深链要先落到分栏宿主再导航到详情),否则窄屏塌陷态深链会断。
  - 多一栏就多一层导航深度;跨项对比只在宽屏并排态成立。
- **合并导航标签的权衡**(NN/g 等):好处是标签更少、认知负担更低("tabs 越少越好",移动端建议 ≤5 顶层);**代价**集中在 —— (a) 原本一键可达的跨项视图改为下钻后**交互成本上升**,尤其伤"跨项对比"工作流;(b) 被收纳项**可发现性下降**。常见折中是 hybrid(主项常驻、次项收进菜单)。([NN/g tabs](https://www.nngroup.com/articles/tabs-used-right/))

### 陷阱(Pitfalls)

- **Desktop 默认落地页硬编码到 `/issues`**:进入 workspace 的重定向 [routes.tsx:118](apps/desktop/src/renderer/src/routes.tsx#L118) `<Navigate to="issues" replace />`,以及 [tab-store.ts:224-231](apps/desktop/src/renderer/src/stores/tab-store.ts#L224) `defaultPathFor`/`defaultTabFor` 也返回 `/{slug}/issues`。**移除/改名 `issues` 路由 = 每次进 workspace 无处落地**,必须同步改默认落地路由。
- **Desktop tab 图标映射**:[tab-store.ts:122-132](apps/desktop/src/renderer/src/stores/tab-store.ts#L122) `ROUTE_ICONS` 里 `issues→ListTodo`、`projects→FolderKanban`。合并后需要为新路由选一个图标(或按视图模式条件选)。
- **Desktop tab 持久化迁移**:当前 schema 版本 [tab-store.ts:574](apps/desktop/src/renderer/src/stores/tab-store.ts#L574) 是 **v3**。路由名一变,**需要 v3→v4 迁移**把老的 `/{slug}/projects`、`/{slug}/issues` 持久化 tab 重写到新路由,否则用户已存的 tab 会被 `sanitizeTabPath()`([177-193](apps/desktop/src/renderer/src/stores/tab-store.ts#L177))静默丢弃。
- **Web 没有重定向中间件**:`apps/web/` 下无 `middleware.ts`。老深链/书签 `…/acme/projects`、`…/acme/issues` 在路由移除后会直接 **404**,除非保留路由或新增重定向。`last_workspace_slug` cookie([layout.tsx:62-69](apps/web/app/[workspaceSlug]/layout.tsx#L62))只存 slug 不存路由,登录后会落到 `/acme/` —— 该路径必须能渲染。
- **运行时模板与现有项目流的选中机制不一致**:Runtime 用**本地 state 就地切换**(不可深链);现有 project 详情用 **URL 路由 `/projects/:id`**(可深链/可分享)。照搬 Runtime 模型 = 把"切项目"从"导航到详情路由"改成"在常驻列表里选中、右栏就地更新",**会牺牲项目详情的可深链性**(除非额外接线,见上 Android 深链坑)。
- **历史 bug 提醒**(来自 CLAUDE.md / git):#2143/#2147/#2192 是 **API 响应漂移**导致旧 desktop 白屏 —— 合并视图若在一个仪表盘里同时吃 `/issues` 与 `/projects` 两个端点,**两个响应都得过 `parseWithFallback` schema**([schema.ts:38-55](packages/core/api/schema.ts#L38)),任一漂移不能拖垮整页。Desktop 历史上还修过**跨 workspace tab 串台**(已改为按 workspace 分组),任何 tab-store 改动别破坏其不变量。

### 约束(Constraints)

- **保留 slug(单一真源)**:[reserved_slugs.json:68-86](server/internal/handler/reserved_slugs.json#L68) 里 `"issues"`、`"projects"` 都是保留字(防止 `/{slug}/{view}` 与某个叫 issues 的 workspace 歧义)。生成物 [reserved-slugs.ts:80-81](packages/core/paths/reserved-slugs.ts#L80) 由 `pnpm generate:reserved-slugs` 生成,**CI 校验漂移**。合并后**至少要保留一个名字**(或一个新的单词路由名)继续占位。
- **路由命名规则(硬约束)**:CLAUDE.md 明令**新顶层路由必须是单个词**或 `/{noun}/{verb}` 对,**禁止连字符词组**(`/project-issues`、`/workspace-board` 都违规)。所以合并后的标签名要么复用 `projects`/`issues` 之一,要么起一个**全新单词**(如 `work` / `board` / `space` 等,需查保留字与 workspace 名冲突)。
- **Desktop 路由分类不可混**:合并视图仍是 **session route**(`/{slug}/{route}`,跑在 per-tab memory router),**不能**做成 WindowOverlay(那是 pre-workspace transition flow 专用)。混淆会重现已修 bug。
- **跨端共享**:页面/组件放 `packages/views/`,禁止 import `next/*` 或 `react-router-dom`,导航一律走 `NavigationAdapter` / `useNavigation()`。web 与 desktop 两端都要各自接路由。
- **i18n / 命名词表**:命名/翻译的单一真源是 [conventions.mdx](apps/docs/content/docs/developers/conventions.mdx)(英)/`conventions.zh.mdx`(中)。新标签名要进 conventions 的"workspace-scoped routes"小节;tab/breadcrumb label 翻译在 `packages/views/locales/{en,zh-Hans}/`(现 `issues.json` / `projects.json` 各一份)。
- **投入预算**:plan 写的是 **1 week**(project 层)。

---

## 待解问题(必须在 Plan 前由 DRI / 团队定夺)

1. **❗ 全局"所有 issues 扁平视图"何去何从?** 这是业界文档反复点名的最大代价。合并后,跨项目的"我现在要看全工作区所有 issues"还存不存在?是降级成项目里的下钻(失去一键扁平视图),还是在卡片网格旁保留一个"All issues"入口?——**产品决策,研究无法替答。**
2. **三栏的中间"项目列表"列怎么来?** 现状是"卡片网格(全宽)→ 点击 → 项目详情(全宽)"。plan 要的是一个**常驻项目列表列**(Runtime 风格)。是新做这一列,还是把卡片网格本身收窄成左列?二者交互差别大。
3. **选中项目用 URL 路由还是本地 state?** Runtime 用本地 state(不可深链);现有 project 详情用 `/projects/:id`(可深链可分享)。要可分享的项目链接吗?这决定要不要为深链接线(见 Android 坑)。
4. **合并后的顶层路由名是什么?** 复用 `projects` / 复用 `issues` / 还是新单词?直接牵动:保留 slug、默认落地路由、desktop tab 迁移版本、i18n 词表、图标。
5. **默认落地页改成什么?** Desktop 当前进 workspace 落 `/issues`。合并后默认落到卡片网格?还是上次所在项目?
6. **卡片显示哪些字段?** 现有 `ProjectCard` 已有环形进度/状态/lead;业界还常加健康度色块、目标日期、issue 计数。要不要补?(影响是否要新数据/端点。)
7. **My Issues 与 Inbox 受影响吗?** `personalNav` 里的 "My Issues" 也依赖 issues 路由;合并是否波及它?

---

## 推荐方案(选项,非决策 —— 留给 Plan session)

> 以下是**可选路线**,研究阶段不拍板。三条路线的主要差异在于"合并的彻底程度"与"是否保留全局 issues"。

1. **「Projects 为主、Issues 内嵌」彻底合并**
   单顶层标签默认卡片网格 → 选项目进 `app侧栏 | 项目列表 | 该项目issues` 三栏(复用已有 `ProjectCard` 网格 + `project-detail` 的 issues 分栏 + Runtime 的常驻列表+就地选中模式)。全局 issues 改为"进项目看"。
   *轻在复用度高;重在牺牲全局扁平 issues 视图(开放问题 #1 的最激进答案)。*

2. **「合并但保留 All-Issues 入口」hybrid**
   同上做卡片网格 + 三栏下钻,但在合并标签内**保留一个 "All issues" 子入口/视图**(对应 NN/g 推荐的 hybrid 折中,也贴近 Linear 的"项目内 Issues 子标签 + 全局 issues 独立")。
   *缓解最大代价;代价是合并没那么"干净",要多设计一个切换。*

3. **「先打地基、不动顶层标签」渐进**
   先把三处手写的 master-detail(Runtime/Inbox/Project 详情)沉淀成一个**通用分栏模板组件 + 常驻列表列**,先给 Projects 详情加上"常驻项目列表列"(不删 Issues 标签),验证交互后再决定是否合并顶层标签 / 迁移路由。
   *把高风险的路由迁移、tab 迁移、保留 slug 变更推后;先拿到大部分体验收益,适合 1 周预算内降风险。*

---

### 给 Plan session 的交接提示
- 复用优先级:`ProjectCard` 网格([projects-page.tsx:34-125](packages/views/projects/components/projects-page.tsx#L34))、`project-detail` issues 分栏([project-detail.tsx:296-330](packages/views/projects/components/project-detail.tsx#L296))、`ResizablePanelGroup`+`useDefaultLayout` 底座、Runtime 的常驻列表+本地 state 选中模式([runtimes-page.tsx:98-176](packages/views/runtimes/components/runtimes-page.tsx#L98))。
- 凡涉及路由名变更,**一个 PR 内**同时改:`reserved_slugs.json`→跑 `generate:reserved-slugs`、desktop 默认落地([routes.tsx:118](apps/desktop/src/renderer/src/routes.tsx#L118) / [tab-store.ts:224](apps/desktop/src/renderer/src/stores/tab-store.ts#L224))、tab-store 迁移版本 v3→v4、i18n 词表、web 旧链重定向。
- 先回答开放问题 #1(全局 issues 去留)再设计,它决定走方案 1/2/3 中的哪条。
