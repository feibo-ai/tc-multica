package cli

// revocation.go 是 stage-4 吊销机制(mini-ADR v4 invariant #8 · 安全关键)的核心。
//
// 吊销 = CI 签的吊销表工件。daemon 在应用【任何】二进制(⑨ update)或 skill bundle
// (⑩c skill-sync)之前查它:被吊销的 digest 或 version 一律拒装(fail-closed)。
//
// 信任链:revocations.json 由 tc-multica 的 release.yml 作 release 资产发布并 attest
// (与 dist/* 同一个 Attest build provenance,二进制锚 = feibo-ai/tc-multica +
// release.yml + refs/tags/v*)。daemon 用 VerifyArtifactAttestation 复用同一条离线
// 验签路径验证吊销表本身 —— 绝不信一张未经验签的吊销表。
//
// 防回放:吊销表带单调递增 counter。撤销条目走 2-review-merged PR 时 bump counter。
// daemon 持久化已验过的最高 counter + 整张表(~/.multica/revocation-list.json)。
// 抓到的表 counter < 持久化的 counter → 判定为回放旧表,拒用抓到的,改用持久化表。
//
// fail 模式(逐条):
//   - fetch 失败 → fail-open:用 persisted 最后已知表(离线机不砖,下次在线收敛);
//   - 验签失败 → 不信此表,用 persisted(不信未签表);
//   - 工件命中 revoked → fail-closed:拒(返回 error)。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// revocationsAssetName 是 tc-multica release 里吊销表 asset 的固定文件名。
const revocationsAssetName = "revocations.json"

// RevocationEntry 是吊销表里的一条记录。Kind 决定 Value 的语义:
//   - "digest":Value 形如 "sha256:<hex>",匹配被应用工件的内容哈希;
//   - "version":Value 形如 "v0.4.13" 或 "skills-v0.0.1",匹配工件的发布版本。
type RevocationEntry struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// RevocationList 是整张吊销表。Counter 单调递增,用于防回放(旧表 counter 更小)。
type RevocationList struct {
	Counter int               `json:"counter"`
	Revoked []RevocationEntry `json:"revoked"`
}

// revocationListPath 是持久化的「已验过的最高 counter 吊销表」文件:
// ~/.multica/revocation-list.json。存整张已验最高表(含 counter)。
func revocationListPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".multica", revocationsAssetName), nil
}

// readPersistedRevocationList 读取持久化的最后已知吊销表。文件不存在 / 不可读 /
// 无法解析 → 返回空表(counter=0,无条目)+ false(表示「没有可信 persisted 表」)。
// 空表语义安全:fail-open 时它不会误吊销任何工件,而抓到的有效表 counter>=0 总能取代它。
func readPersistedRevocationList() (RevocationList, bool) {
	path, err := revocationListPath()
	if err != nil {
		return RevocationList{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RevocationList{}, false
	}
	var list RevocationList
	if err := json.Unmarshal(data, &list); err != nil {
		return RevocationList{}, false
	}
	return list, true
}

// writePersistedRevocationList 原子写持久化吊销表(temp + rename,落在同目录同 mount)。
// 只在抓到的表【已验签且 counter >= persisted】时调用 —— 持久化的永远是已验过的最高表。
func writePersistedRevocationList(list RevocationList) error {
	path, err := revocationListPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// pickEffectiveList 实现单调防回放(纯函数,可单测):
//   - fetchedOK==false(没拿到 / 没验过的有效抓取表)→ 用 persisted(fail-open);
//   - fetched.Counter >= persisted.Counter → 用 fetched(更新或等同,接受);
//   - fetched.Counter <  persisted.Counter → 回放旧表,拒,改用 persisted。
//
// 返回 (生效表, 是否应持久化 fetched)。只有当 fetched 被接受为生效表时才返回 true,
// 让调用方把它持久化成新的最高已知表。
func pickEffectiveList(fetched RevocationList, fetchedOK bool, persisted RevocationList, persistedOK bool) (effective RevocationList, persistFetched bool) {
	if !fetchedOK {
		// 没拿到有效(已验签的)抓取表 → fail-open 用 persisted。persistedOK==false
		// 时 persisted 是空表(无吊销),离线首跑机不砖。
		return persisted, false
	}
	if persistedOK && fetched.Counter < persisted.Counter {
		// 回放检测:抓到的表比已知最高表旧 → 拒抓到的,用 persisted。
		return persisted, false
	}
	// fetched 是已验签且不旧的表 → 采纳并持久化。
	return fetched, true
}

// checkRevocationAgainstList 是纯逻辑命中判定(可单测,无 I/O):
// 若 `sha256:<artifactDigestHex>` 命中某 digest 条目,或 version 命中某 version 条目,
// 返回 error(refuse,fail-closed);否则 nil。
//
// version 比较做大小写不敏感的精确匹配,并对常见前缀宽容:吊销表里 version 条目可写成
// "v0.4.13" 或 "0.4.13"、"skills-v0.0.1" 等,与调用方传入的 version 字符串两侧都规整后比较。
func checkRevocationAgainstList(artifactDigestHex, version string, list RevocationList) error {
	wantDigest := "sha256:" + strings.ToLower(strings.TrimSpace(artifactDigestHex))
	normVersion := normalizeRevocationVersion(version)

	for _, e := range list.Revoked {
		switch strings.ToLower(strings.TrimSpace(e.Kind)) {
		case "digest":
			if strings.EqualFold(strings.TrimSpace(e.Value), wantDigest) {
				return fmt.Errorf("artifact %s is revoked (reason: %s) — refusing (fail-closed)", wantDigest, e.Reason)
			}
		case "version":
			if normVersion != "" && normalizeRevocationVersion(e.Value) == normVersion {
				return fmt.Errorf("version %q is revoked (reason: %s) — refusing (fail-closed)", strings.TrimSpace(version), e.Reason)
			}
		default:
			// 未知 kind:忽略该条(向前兼容未来的 kind),不影响其它条目命中判定。
			continue
		}
	}
	return nil
}

// normalizeRevocationVersion 规整 version 字符串以便精确比较:trim、小写。保留前缀
// (v / skills-v)原样 —— "v0.4.13" 与 "0.4.13" 不视为相等是有意的:吊销表条目应写
// 调用方实际传入的形式(二进制传 targetVersion = "v0.4.13" 形式;skill 传 tag =
// "skills-v0.0.1")。这里只吸收大小写 / 空白差异,不做语义版本解析(避免「0.4.13」误
// 命中「v0.4.130」之类)。
func normalizeRevocationVersion(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// CheckArtifactRevocation 是 daemon 应用任何工件前调用的吊销门(invariant #8)。
//
// 步骤:
//  1. FetchLatestRelease(tc-multica)→ 找 revocations.json asset → fetchURLBytes 下载。
//     fetch 失败 → 走 persisted(fail-open),不 error。
//  2. VerifyArtifactAttestation(revocationsJSONBytes)——二进制锚验签该表。验不过 →
//     不信此表,走 persisted(不信未签表)。
//  3. 解析 JSON;读 persisted。单调防回放:fetched.counter >= persisted.counter → 采纳
//     fetched 并持久化;否则用 persisted(拒回放旧表)。
//  4. 对生效表做命中判定:digest 或 version 命中 revoked → return error(fail-closed);
//     否则 nil。
//
// artifactDigestHex 是被应用工件内容的 sha256 十六进制(不含 "sha256:" 前缀);version
// 是其发布版本(二进制 "v0.4.13" 形式 / skill bundle "skills-v0.0.1" tag)。
func CheckArtifactRevocation(artifactDigestHex, version string, timeout time.Duration) error {
	persisted, persistedOK := readPersistedRevocationList()

	fetched, fetchedOK := fetchAndVerifyRevocationList(timeout)

	effective, persistFetched := pickEffectiveList(fetched, fetchedOK, persisted, persistedOK)

	if persistFetched {
		// 抓到的已验签表被采纳为新的最高已知表 → 持久化它。持久化失败不致命:下次
		// 在线会重新抓取 + 重新验,只是少了一次离线兜底的更新;不应因写盘失败而误放行
		// 或误拒。记不了就用内存里的 effective 继续判定。
		_ = writePersistedRevocationList(effective)
	}

	return checkRevocationAgainstList(artifactDigestHex, version, effective)
}

// fetchAndVerifyRevocationList 抓取并验签最新 tc-multica release 的 revocations.json。
// 返回 (表, true) 仅当 fetch + 验签 + 解析全部成功;任一失败 → (零值, false),由
// pickEffectiveList 走 persisted(fail-open / 不信未签表)。
//
// 安全红线:绝不返回一张未经 VerifyArtifactAttestation 的表为 ok。
func fetchAndVerifyRevocationList(timeout time.Duration) (RevocationList, bool) {
	release, err := FetchLatestRelease()
	if err != nil || release == nil {
		return RevocationList{}, false
	}

	var assetURL string
	for i := range release.Assets {
		if release.Assets[i].Name == revocationsAssetName {
			assetURL = release.Assets[i].BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		// release 没带吊销表 asset → 当作没抓到(fail-open 用 persisted)。
		return RevocationList{}, false
	}

	raw, err := fetchURLBytes(assetURL, timeout)
	if err != nil {
		return RevocationList{}, false
	}

	// 二进制锚验签(复用 ⑨ 路径):吊销表由 tc-multica release.yml attest,与
	// dist/* 同锚。验不过 → 不信此表。
	if err := VerifyArtifactAttestation(raw, timeout); err != nil {
		return RevocationList{}, false
	}

	var list RevocationList
	if err := json.Unmarshal(raw, &list); err != nil {
		// 已验签但解析不了(畸形)→ 不信此表内容,走 persisted。
		return RevocationList{}, false
	}
	return list, true
}

// sha256HexOf 是给调用方(update.go / skill_sync.go)算工件内容 digest 的小工具,
// 与 attestation.go 里 VerifyArtifactAttestation 内部用的同一算法(sha256 → 小写
// hex),保证吊销表的 digest 条目与 attestation 锚定的是同一份字节。
func sha256HexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
