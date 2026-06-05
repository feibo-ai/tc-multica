# Design — Usage 页:用户/智能体 tabs + 热力图(runtime-token-usage v2)

**Date:** 2026-06-05
**Status:** Design approved (brainstorming) — feeds `tc-3-plan` for the Phase 1 plan
**Project:** Multica 迭代 (`d47d2c07-86ec-4bb3-bdb9-da4e04a7e2ea`)
**Builds on:** v1 `runtime-token-usage`(已发 v0.4.9、生产可见)— ambient(本地 CLI)用量已按 person × runtime 落账

## 目标(用户原话拆解)

1. 用量页新增 tab:**用户 | 智能体**,把"人"和"挂载的 Multica agent"分开。
2. 用户、智能体的 token 用量 / 费用以**热力图**展示(GitHub 贡献图式日历)。
3. 用户维度下可按**工具**细分(Claude Code / Codex / Codex CLI / Claude Desktop …)。
4. 上报数据更全面(session 名称、日期等)。

## 关键决策(brainstorming 已确认)

| # | 决策 | 选定 |
|---|---|---|
| D1 | 「智能体」指什么 | **用户手动挂载到 Multica 的 agent**(`agent` 表 / `agent_task_queue.agent_id`),不是 AI 工具 |
| D2 | 用户 tab 算什么 | **干净二分**:用户 tab = ambient(本地 CLI)用量,仅此;智能体 tab = task 用量,仅此。两边不重叠、合起来=全部 |
| D3 | 两个 tab 的布局 | **方案 B**:紧凑榜单 + 点开看大热力图(最贴 GitHub 个人 profile 大图,扩展性好) |
| D4 | 热力图着色指标 | **费用 / Token 切换**,默认费用(复用现成 `ActivityHeatmap` 的着色) |
| D5 | 智能体 tab | 沿用 B:现有 by-agent 榜 → 点开该 agent 的 per-day 热力图 |
| D6 | 分期 | 三期分解;本 spec 只交付 Phase 1 |

## 可行性分析(基于实测,影响范围)

各工具本地数据源(决定 D3 的工具细分):

- **Claude Code** `~/.claude/projects/**/*.jsonl` — v1 已采 ✓
- **Codex** `~/.codex/sessions/rollout-*.jsonl` — 数据存在,**可行**,但格式 + token 记账方式与 Claude 不同(逐消息求和 vs 末值累计),需独立 adapter + 隐私审查 → **Phase 2**
- **Claude Desktop** `~/Library/Application Support/Claude/local-agent-mode-sessions/**/audit.jsonl` — 审计日志**只有 model、无 token 数**;其 local-agent-mode 内含 `claude_code_version`(底层即 Claude Code,真有 usage 会写进 `~/.claude/projects`、已被当 "claude code" 计)→ **本地无法单独、准确拆出,不做**
- **Codex CLI vs Codex**:大概率同一来源(`~/.codex`),按一个 `source` 处理

**架构契合**:v1 的可插拔 Collector 框架 + ambient 表的 `source` 列,正是为"加工具=新 adapter、按 source 分组"设计的 → 工具细分天然落在这套机制上。

## Phase 1 设计(本期交付)— 纯现有数据,无 daemon 改动、无新迁移

### 页面结构(Usage / 用量页)
- 顶部 **KPI 卡 + 趋势图**:保持不动。
- 下方原 "Cost by agent" 排行榜区域 → 改成**带 tab 的区块**:`👤 用户 | 🤖 智能体`(原 by-agent 榜成为「智能体」tab 的内容)。

### 👤 用户 tab(布局 B)
- 左:按 **ambient 花费**排序的**人榜**(头像 + 名 + 总额/总 token)。
- 右:点选某人 → ta 的 **26 周热力图**(`ActivityHeatmap`,费用/Token 切换)+ **工具拆分**区(Phase 1 只有 Claude Code = 100%,UI 预留位给 Phase 2 的 Codex)。

### 🤖 智能体 tab(布局 B)
- 左:现有 by-agent 榜(task 用量)。
- 右:点选某 agent → 该 agent 的 **26 周热力图**(费用/Token 切换)。

### 复用
- `packages/views/runtimes/components/charts/activity-heatmap.tsx`(GitHub 式日历,按费用着色)→ 加**费用/Token toggle**(把着色指标参数化)。
- Leaderboard 列表模式、客户端按 model 定价算费用(沿用现有约定:model 上线、cost 客户端算)。

### 数据 / 读端点(都基于现有 hourly rollup)
| 读 | 来源 | 状态 |
|---|---|---|
| 人榜:每人 ambient 总额 | `ambient_usage_hourly` GROUP BY runtime→`owner_id`(NULL→未归属) | **新增**(轻;v1 by-person UNION 的 ambient 半边) |
| 人热力图:某人 ambient/天 | `ambient_usage_hourly` WHERE owner=…,按 `bucket_hour AT TIME ZONE tz` 切天 | **新增** |
| agent 榜:每 agent task | `ListDashboardUsageByAgent` | **已有** |
| agent 热力图:某 agent task/天 | `task_usage_hourly` WHERE agent_id=…,按天 | **新增**(或扩现有) |

- owner_id NULL 仍走 v1 已有的「未归属」桶语义。
- 多租户:所有读按 `workspace_id` 过滤(沿用现有)。
- 前端走 `parseWithFallback` + zod(沿用 API-Response-Compatibility 约定),每个新端点配 malformed-response 测试。

### 不在 Phase 1
- 任何 daemon / collector / migration 改动。
- 真·工具细分(只占位)。
- session 名称等更全上报。

## Phase 2(后续,独立 spec/plan)— Codex adapter + 真工具细分
- 新 Codex Collector adapter(`~/.codex`),`source="codex"`,处理其 token 记账差异 + 隐私审查(沿用 numbers/ids-only doctrine)。
- 用户 tab 工具拆分变成真数据(Claude Code vs Codex)。
- 读端按 `source` 分组。

## Phase 3(后续,独立 spec/plan)— 更全上报
- session "名称"/日期等。**隐私边界冲突**:Claude session 只有 UUID,"名称"若取自对话内容 → **违反 v1 隐私 doctrine(只读数字+id、绝不读 content)**。
- 候选(待 Plan 定):用 **cwd/项目路径**作 session 标签(元数据、非 content)、或 session UUID + 首末时间;日期已由 `event_at` 捕获。
- 需要 Plan 阶段专门处理这个 doctrine 取舍。

## 给 Plan 阶段的待决项
1. 「未挂载本地会话」在用户 tab 的呈现:owner NULL 的"未归属"桶是否单列一行。
2. 热力图窗口:固定 26 周,还是跟随页面已有的 days 选择器。
3. Phase 3 的 session 标签隐私取舍(cwd 路径 vs UUID)。
4. agent 热力图的 task/天读端是新增独立 query 还是扩 `ListDashboardUsageByAgent`。
