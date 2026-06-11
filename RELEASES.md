# Releases

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
