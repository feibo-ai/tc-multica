# Releases

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
