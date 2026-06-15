# Releases

## v0.4.20

本地 session 用量新增 Codex 采集器,并标注 Users/Agents 用量的口径差异(TEA-111 / TEA-112)。

### 新增 Codex 本地 session 用量采集(TEA-112)
daemon 的 ambient 采集器此前只认 Claude（`~/.claude` transcript），Codex CLI 的本地用量完全没进库，导致 dashboard「用量 · Users」只看得到 Claude。新增 `codexCollector`，扫 `~/.codex/{sessions,archived_sessions}/**/rollout-*.jsonl` 的 `token_count` 事件入库。

口径要点（Codex 的 `total_token_usage` 是**按会话累计**值，非逐消息增量、且同一会话会重复发）：
- **per-session 累计增量**：`delta = 本次末值 − 上次水位`，绝不把多行累计值相加；
- **forward-only watermark**：首跑记录每会话当前累计值（不回填历史，避免把历史一次性灌进当前小时）；
- **合成 dedup 键**（session_id=rollout UUID、message_id=session_id、request_id=`cum:`+累计）去重，`sessions/` 与 `archived_sessions/` 不重复计，归档移动后水位保留；
- 模型从 `turn_context` 解析。

### dashboard 用量口径标注（TEA-111）
Users（本地 session）用量按本地 transcript 估算，属**近似**；Agents（云端任务）用量来自服务端逐任务计量，**精确**。两个 tab 各加一行小字说明，避免把近似值误读为精确账单。

### 升级
- 自更：daemon 设 `MULTICA_DAEMON_AUTO_UPDATE=true` 后自动升级；手动 `multica update` 或重跑 `scripts/install.sh`。
- 注：Codex 用量采集在 **daemon** 侧——需 daemon 升级到本版并运行才会开始上报。

## v0.4.18

修复团队总览卡的人均统计全为 0 的根因（TEA-104 跟进）。

### 根因：member 维度统计用错了 ID 域
团队卡的每个人均聚合（负责项目、任务分布、自动化、小队）都按 `member.id` 分桶，但 Multica 的 polymorphic actor 约定里，member 类型的 `assignee_id` / `lead_id` / `created_by_id` / `squad_member.member_id` 存的其实是 **`user.id`**（见 `issue.sql`：`assignee_type='member' AND assignee_id=user_id`）。于是这些计数永远匹配不上 member.id、恒为 0；而 DRI / 智能体 / AI 用量本就按 user.id 键，所以一直正常。生产数据印证：曾振华名下 93 个 issue、负责 11 个项目，卡片却显示 0。

修复：handler 把全部人均聚合改为按 **user.id** 分桶（经 `member.UserID` 装配），与 DRI / 智能体 / tokens 一致；`member.id` 仅用于卡片身份 / IsSelf / viewer 匹配。先前 v0.4.17 的「标签修正 + 智能体派发任务合并 + 面板加宽」保留不变——本版让 member 直接指派的任务也正确显示。

### 升级
- 自更：daemon 设 `MULTICA_DAEMON_AUTO_UPDATE=true` 后自动升级；手动 `multica update` 或重跑 `scripts/install.sh`。

## v0.4.17

团队总览页上线后修复：卡片数据口径 + 主从面板宽度（TEA-104 跟进）。

### 团队卡片
- **修复标签错位**：智能体 tile 原标「（运行中）」实为总数 → 改「智能体」；项目 tile 原标「负责 / 参与」但无参与数据 → 改「项目 · 负责 / DRI」。
- **任务分布纳入智能体派发的活**：新增 `CountAgentIssuesByOwnerStatus` 聚合（每个人名下智能体被指派的 issue，按状态），卡片任务条改为「本人 + 智能体」合并——AI-native 团队把 issue 派给智能体，原本只数成员直接指派会全 0，现在显示真实活动。
- AI 用量周≈月属真实（近期才活跃，用量都在 7 天内），非 bug。

### 项目主从布局
- 点 issue 后右侧详情面板加宽到约 **1/3 页宽**（原复用属性栏的 320px 太窄）；关闭后回到列表聚焦视图。

### 升级
- 自更：daemon 设 `MULTICA_DAEMON_AUTO_UPDATE=true` 后自动升级；手动 `multica update` 或重跑 `scripts/install.sh`。

## v0.4.16

团队总览页 + 侧边栏底部化/折叠 + 项目主从布局（TEA-104 · 前端三波次）。

### 团队总览页（新「团队」tab · 默认落地）
- 新 `GET /api/team/overview` 单聚合端点（7 个 GROUP-BY，query 数与成员数无关 · N2），多租户 `workspace_id` 全隔离、UUID 走 `parseUUIDOrBadRequest`，zod + `parseWithFallback` 抗漂移。
- 前端 `/{ws}/team` 全员大卡（web + desktop 双端）：负责/参与项目 · 智能体运行中 · 自动化任务 · AI 用量 · 任务状态分布——全通俗中文指标名 + hover 解释、a11y dot+文字、零 emoji。
- 工作区默认落地从项目页翻到 `/team`，侧边栏新增「团队」入口。

### 侧边栏
- 「搜索 / 新建issue」从顶部移到底部 footer，新增可持久化的 icon 折叠（复用 shadcn SidebarProvider）。

### 项目主从布局
- 点 issue 在右栏内嵌 issue 详情（`?issue=` URL 驱动 · web + desktop），保留「侧边栏 + 项目栏 + issue详情」三栏不再整页跳走；My Issues / issues 页 / actor 面板导航零回归。

### 修复
- `CountRunningAgentsByOwner` 队列状态过滤修正为 `('queued','dispatched','running')`（原误用 `'claimed'`/`'in_progress'` 非队列枚举、漏 `queued`，致「智能体运行中」少报）。

### 升级
- 自更：daemon 设 `MULTICA_DAEMON_AUTO_UPDATE=true` 后自动升级；手动 `multica update` 或重跑 `scripts/install.sh`。

## v0.4.15

真无感自更安全子系统(⑨⑩)+ stage-4 吊销 + Phase-1 收敛。

### ⑨ 二进制自更 · keyless OIDC attestation 验签
- `update.go`:daemon 自更下载二进制后、SHA-256 前,用内置 sigstore trusted root **离线验 attestation**(OIDC subject 三元组 repo+workflow+ref 绑死,拒 upstream 同名工件),验不过 **fail-closed 无 fallback**。
- release workflow 用 `actions/attest-build-provenance` 给 release 工件签 provenance(无长期私钥)。

### ⑩ skill/md 无感分发
- 信任锚 = team-context(skill 源仓);daemon `skillSyncLoop` 拉签名 skill bundle → 离线验签 → **写路径安全 byte-for-byte 落盘**(typeflag 白名单 / 父链 EvalSymlinks / O_EXCL\|O_NOFOLLOW / 确定性拒软链)。
- **gated `MULTICA_DAEMON_SKILL_WRITE`(默认 off · opt-in)**;dev 软链机互斥守卫拒启。

### stage-4 吊销
- `CheckArtifactRevocation`:应用任何二进制/skill 前查 CI 签的 `revocations.json`(验签不信未签表)+ 单调防回放 + 命中 fail-closed;fetch 失败 fail-open 用 persisted(离线不砖)。

### Phase-1 收敛
- `multica user list`(workspace 成员)· `multica doctor`(发布前 preflight)· daemon 结构化遥测(skill 健康 + 命门成功率)· 一命令 install onboarding(token/workspace 自动发现)。

### 信任根 / 治理
- release provenance 合并门(release tag 须经 PR 合并入 main · invariant #7,DRI 决定实施为「经 PR 合并入 main」,不强制独立评审)+ main/tag rulesets + `scripts/verify-trust-root-config.sh` 配置漂移探针。

### 升级
- 自更:daemon 设 `MULTICA_DAEMON_AUTO_UPDATE=true` 后 6h 轮询自动升级(首次到 v0.4.15 走旧 SHA-256 路径,之后用本版 attestation 验签)。
- 手动:重跑 `scripts/install.sh` 或 `multica update`。
