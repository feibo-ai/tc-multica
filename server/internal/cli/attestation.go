package cli

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// embeddedTrustedRoot is the sigstore trusted-root bundle (Fulcio roots, Rekor
// public keys, TSA / CT log keys) compiled into the binary so verification is
// fully offline — no network fetch of trust material, no TUF round-trip. This
// is the cryptographic trust anchor for daemon self-update (mini-ADR v4).
//
//go:embed embedded_trusted_root.json
var embeddedTrustedRoot []byte

// 安全不变量 · 逐字 (mini-ADR v4 invariant #2) —— 这些常量是信任根的全部边界。
const (
	// TrustAnchorRepo 是唯一信任锚 (invariant #2)。仅作文档/引用用途;真正的
	// 绑定由 attestationSANRegex 强制(它把 repo 写死在正则里)。
	TrustAnchorRepo = "feibo-ai/tc-multica"

	// githubOIDCIssuer 是 GitHub Actions keyless 签名的 OIDC issuer。精确匹配,
	// 不接受任何其它 issuer。
	githubOIDCIssuer = "https://token.actions.githubusercontent.com"

	// attestationSANRegex 三元组全绑 (invariant #2):它同时绑死
	//   repo     == feibo-ai/tc-multica
	//   workflow == .github/workflows/release.yml
	//   ref      =~ refs/tags/v<语义版本>
	// 任何 upstream multica-ai/multica、其它 repo、其它 workflow、非 tag ref 的
	// 同名 attestation 都被拒。
	attestationSANRegex = `^https://github\.com/feibo-ai/tc-multica/\.github/workflows/release\.yml@refs/tags/v[0-9][0-9A-Za-z.\-]*$`

	// skillBundleSANRegex 是 skill 分发的第二信任锚 (mini-ADR v4 ⑩c)。它把
	// 三元组同样绑死,但锚定 team-context 仓 + skill-bundle-release.yml workflow
	// + skills-v* tag:
	//   repo     == feibo-ai/team-context
	//   workflow == .github/workflows/skill-bundle-release.yml
	//   ref      =~ refs/tags/skills-v<版本>
	// 它与 attestationSANRegex 互不相交 —— 二进制锚不能验 skill bundle、skill
	// 锚也不能验二进制(T/S2 客观断言这点)。fail-closed,无 fallback。
	skillBundleSANRegex = `^https://github\.com/feibo-ai/team-context/\.github/workflows/skill-bundle-release\.yml@refs/tags/skills-v[0-9][0-9A-Za-z.\-]*$`
)

// attestationsAPIBase 是二进制(⑨)的 GitHub artifact attestations 查询端点前缀。
// 完整 URL: <base>/sha256:<digestHex>
const attestationsAPIBase = "https://api.github.com/repos/feibo-ai/tc-multica/attestations/"

// skillAttestationsAPIBase 是 skill bundle(⑩c)的第二信任锚查询端点前缀,
// 锚定 feibo-ai/team-context 仓。完整 URL: <base>/sha256:<digestHex>
const skillAttestationsAPIBase = "https://api.github.com/repos/feibo-ai/team-context/attestations/"

// loadEmbeddedTrustedMaterial 把内嵌的 trusted root 解析成可供 verifier 使用的
// TrustedMaterial。embedded_trusted_root.json 是 JSONL —— 每行一份独立的
// `trustedroot+json` 文档(本仓为 2 份:一份含 tlogs/ctlogs/CA/TSA,一份补充
// CA/TSA)。root.NewTrustedRootFromJSON 只能解析单个 JSON 对象,故这里逐行解析
// 后用 root.TrustedMaterialCollection 合并(它把各成员的 Fulcio CA / Rekor 日志
// / CT 日志 / TSA 全部并集)。这样无需改动 embedded_trusted_root.json。
func loadEmbeddedTrustedMaterial() (root.TrustedMaterial, error) {
	var collection root.TrustedMaterialCollection
	for i, line := range bytes.Split(embeddedTrustedRoot, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		tr, err := root.NewTrustedRootFromJSON(trimmed)
		if err != nil {
			return nil, fmt.Errorf("parse trusted root document %d: %w", i, err)
		}
		collection = append(collection, tr)
	}
	if len(collection) == 0 {
		return nil, fmt.Errorf("embedded trusted root is empty")
	}
	return collection, nil
}

// verifyProvenanceBundles 是纯离线密码学验证(可单测,无网络)。
//
// 给定一份 artifact 字节和一组候选 attestation bundle 的原始 JSON:
//  1. 计算 sha256(artifactBytes);
//  2. 从内嵌 trusted root 构造 verifier;
//  3. 用 issuer 精确匹配 + SAN 正则(三元组绑死)构造证书身份策略;
//  4. 策略绑定 artifact digest;
//  5. 逐 bundle 反序列化并验证 —— 任一 bundle 验过即 return nil。
//
// fail-closed:空 bundle 列表直接 error;全部 bundle 验失败返回非 nil error
// (mini-ADR v4 invariant #3 —— 绝无 fallback 到仅 SHA-256)。
func verifyProvenanceBundles(artifactBytes []byte, bundleJSONs [][]byte) error {
	return verifyProvenanceBundlesWithSAN(artifactBytes, bundleJSONs, attestationSANRegex)
}

// verifyProvenanceBundlesWithSAN 是 verifyProvenanceBundles 的内部实现,允许
// 注入 SAN 正则以便测试可以断言「换一个 repo 的正则就验不过」(T4),从而证明
// 三元组断言真生效、不是橡皮图章。导出的 attestationSANRegex 常量绝不被改写。
func verifyProvenanceBundlesWithSAN(artifactBytes []byte, bundleJSONs [][]byte, sanRegex string) error {
	// fail-closed:没有任何 attestation 就是验证失败,绝不放行。
	if len(bundleJSONs) == 0 {
		return fmt.Errorf("no attestation bundles to verify (fail-closed)")
	}

	sum := sha256.Sum256(artifactBytes)

	trustedMaterial, err := loadEmbeddedTrustedMaterial()
	if err != nil {
		return fmt.Errorf("load embedded trusted root: %w", err)
	}

	// GitHub attestation 携带 Fulcio 证书(含 SCT)+ Rekor tlog 条目(含
	// inclusion proof + integrated timestamp)。下面三个阈值要求各类时间戳/透明
	// 日志证据齐全;这是让真 fixture 验过的组合(见报告)。
	verifier, err := verify.NewVerifier(trustedMaterial,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithIntegratedTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("build verifier: %w", err)
	}

	// issuer 精确匹配(第二个参数为空 regex)+ SAN 正则(三元组绑死)。
	certID, err := verify.NewShortCertificateIdentity(githubOIDCIssuer, "", "", sanRegex)
	if err != nil {
		return fmt.Errorf("build certificate identity: %w", err)
	}

	policy := verify.NewPolicy(
		verify.WithArtifactDigest("sha256", sum[:]),
		verify.WithCertificateIdentity(certID),
	)

	// 逐 bundle 验证;任一过即放行。聚合所有失败原因便于诊断。
	var lastErr error
	for i, raw := range bundleJSONs {
		var b bundle.Bundle
		if err := b.UnmarshalJSON(raw); err != nil {
			lastErr = fmt.Errorf("bundle[%d] unmarshal: %w", i, err)
			continue
		}
		if _, err := verifier.Verify(&b, policy); err != nil {
			lastErr = fmt.Errorf("bundle[%d] verify: %w", i, err)
			continue
		}
		// 验过一个就够了:身份+digest+透明日志全部满足。
		return nil
	}

	// fail-closed:没有任何 bundle 通过 → 拒绝。
	if lastErr == nil {
		lastErr = fmt.Errorf("no attestation bundle satisfied the policy")
	}
	return fmt.Errorf("attestation verification failed (fail-closed): %w", lastErr)
}

// githubAttestationsResponse 是 GET /repos/{owner}/{repo}/attestations/{digest}
// 的响应骨架。每个 attestation 内嵌一个 sigstore bundle,我们只需要它的原始
// JSON,故用 json.RawMessage 保留。
type githubAttestationsResponse struct {
	Attestations []struct {
		Bundle json.RawMessage `json:"bundle"`
	} `json:"attestations"`
}

// fetchAttestationBundles 从二进制(⑨)信任锚拉取某 artifact digest 的全部
// attestation bundle。保持 ⑨ 二进制路径行为不变 —— 它只是用 attestationsBaseURL()
// 调用通用的 fetchAttestationBundlesFor。
//
// TEA-115:base 由 attestationsBaseURL() 提供,当 daemon 配了 verified-mirror 时
// 它返回 <mirror>/attestations/,否则回退 GitHub-pinned attestationsAPIBase 字面量
// (bootstrap / 老机路径)。这只改字节传输 host;digest content-addressed 路径
// (sha256:<digest>)、Accept header、非-200 错误处理(fetchAttestationBundlesFor
// :204-206)、离线验签全部不变(INV-16)。skill 第二信任锚 skillAttestationsAPIBase
// 绝不经此变量重写 —— 它仍由 VerifySkillBundleAttestation 直接传 const(INV-18 隔离)。
func fetchAttestationBundles(digestHex string, timeout time.Duration) ([][]byte, error) {
	return fetchAttestationBundlesFor(attestationsBaseURL(), digestHex, timeout)
}

// fetchAttestationBundlesFor 从给定信任锚 API base 拉取某 artifact digest 的全部
// attestation bundle(原始 JSON 列表)。apiBase 决定信任锚仓(⑨ 二进制用
// attestationsAPIBase;⑩c skill 用 skillAttestationsAPIBase)。
// fail-closed:404 / 空列表 → error。
func fetchAttestationBundlesFor(apiBase, digestHex string, timeout time.Duration) ([][]byte, error) {
	url := apiBase + "sha256:" + digestHex

	// 复用 update.go 的 fetchURLBytes 不够 —— attestations API 需要
	// Accept header(与 fetchReleaseByTag 同款),且我们要区分 404。这里用同款
	// client + header 直接发请求。
	client := &http.Client{Timeout: updateDownloadTimeoutOrDefault(timeout)}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build attestations request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch attestations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 404 = 该 artifact 没有 attestation;fail-closed 视为验证不可能通过。
		return nil, fmt.Errorf("GitHub attestations API returned %d for %s (fail-closed)", resp.StatusCode, url)
	}

	var parsed githubAttestationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode attestations response: %w", err)
	}

	bundles := make([][]byte, 0, len(parsed.Attestations))
	for _, a := range parsed.Attestations {
		if len(a.Bundle) == 0 {
			continue
		}
		bundles = append(bundles, []byte(a.Bundle))
	}
	if len(bundles) == 0 {
		return nil, fmt.Errorf("no attestation bundles returned for sha256:%s (fail-closed)", digestHex)
	}
	return bundles, nil
}

// VerifyArtifactAttestation 把 fetch + 离线验证串起来:计算 artifact 的 sha256,
// 拉取其 GitHub attestation bundle,再做纯离线密码学验证。任一环节失败均返回
// 非 nil error(fail-closed · mini-ADR v4 invariant #3)。
func VerifyArtifactAttestation(artifactBytes []byte, timeout time.Duration) error {
	sum := sha256.Sum256(artifactBytes)
	digestHex := hex.EncodeToString(sum[:])

	bundles, err := fetchAttestationBundles(digestHex, timeout)
	if err != nil {
		return fmt.Errorf("fetch attestation bundles: %w", err)
	}
	return verifyProvenanceBundles(artifactBytes, bundles)
}

// VerifySkillBundleAttestation 是 skill 分发(⑩c)的第二信任锚验证入口:计算
// skill bundle(tar.gz)的 sha256,从 feibo-ai/team-context 的 attestations API
// 拉取其 bundle,再用 skillBundleSANRegex(三元组绑死 team-context +
// skill-bundle-release.yml + skills-v* tag)做纯离线密码学验证。
//
// fail-closed,无 fallback(mini-ADR v4 invariant #3):fetch 失败、无 attestation、
// 身份/digest/透明日志任一不满足 → 非 nil error。调用方(daemon 写循环)只有在本
// 函数返回 nil 时才得把 bundle 交给 ExtractSkillBundleSafely 落盘。
func VerifySkillBundleAttestation(bundleBytes []byte, timeout time.Duration) error {
	sum := sha256.Sum256(bundleBytes)
	digestHex := hex.EncodeToString(sum[:])

	bundles, err := fetchAttestationBundlesFor(skillAttestationsAPIBase, digestHex, timeout)
	if err != nil {
		return fmt.Errorf("fetch skill attestation bundles: %w", err)
	}
	return verifyProvenanceBundlesWithSAN(bundleBytes, bundles, skillBundleSANRegex)
}
