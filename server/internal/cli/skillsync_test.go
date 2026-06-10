package cli

// skillsync_test.go —— ⑩c 写路径安全核心的客观安全断言。
//
// 验签侧(S1/S2):证明 skill 锚三元组绑死 —— 真 fixture 用 skill 锚验过、用二进制
//   锚验不过(反之亦然)。走纯离线 verifyProvenanceBundlesWithSAN,不联网。
// 写路径侧(W1–W5):byte-for-byte 落盘、软链目标拒、父目录软链拒、互斥守卫、
//   穿越拒。全部用 temp dir,确定性断言「必 error」而非「跳过」。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// loadSkillFixtureBundles 读 testdata/fixture-skill-attestations.json(真 GitHub
// /attestations API 响应)取每个 .attestations[].bundle 的原始 JSON。
func loadSkillFixtureBundles(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture-skill-attestations.json"))
	if err != nil {
		t.Fatalf("read fixture-skill-attestations.json: %v", err)
	}
	var parsed githubAttestationsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal fixture skill attestations: %v", err)
	}
	bundles := make([][]byte, 0, len(parsed.Attestations))
	for _, a := range parsed.Attestations {
		if len(a.Bundle) == 0 {
			continue
		}
		bundles = append(bundles, []byte(a.Bundle))
	}
	if len(bundles) == 0 {
		t.Fatal("no bundles extracted from fixture-skill-attestations.json")
	}
	return bundles
}

func loadSkillFixtureBundleGz(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixture-skill-bundle.tar.gz"))
	if err != nil {
		t.Fatalf("read fixture-skill-bundle.tar.gz: %v", err)
	}
	return data
}

// tarEntryBytes 解开 fixture bundle 取指定 entry 的原始字节(供 byte-for-byte 断言)。
func tarEntryBytes(t *testing.T, gzBytes []byte, entryName string) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Name == entryName {
			b, rErr := io.ReadAll(tr)
			if rErr != nil {
				t.Fatalf("read entry %q: %v", entryName, rErr)
			}
			return b
		}
	}
	t.Fatalf("entry %q not found in fixture bundle", entryName)
	return nil
}

// S1 —— skill 锚验签真过(离线核心)。VerifySkillBundleAttestation 会联网 fetch,
// 故直接测离线密码学核心:fixture skill bundle 字节 + fixture skill attestation
// bundles + skillBundleSANRegex → verifyProvenanceBundlesWithSAN 返回 nil。
// 这是 keystone:证明 skill 三元组锚(team-context + skill-bundle-release.yml +
// skills-v* tag)的身份策略 + digest 绑定对真 fixture 成立。**必过**。
func TestVerifySkillBundle_RealFixturePasses_S1(t *testing.T) {
	bundleBytes := loadSkillFixtureBundleGz(t)
	attBundles := loadSkillFixtureBundles(t)

	if err := verifyProvenanceBundlesWithSAN(bundleBytes, attBundles, skillBundleSANRegex); err != nil {
		t.Fatalf("S1: expected real skill fixture to verify with skill anchor, got: %v", err)
	}
}

// S2 —— 错锚拒。同一 fixture skill bundle + 同一 attestation bundles,但用 ⑨ 的
// 二进制 SAN regex(attestationSANRegex,锚 tc-multica/release.yml)验 → 必非 nil。
// 证明二进制锚不能验 skill bundle —— skill 锚三元组绑死,两锚互不相交。
func TestVerifySkillBundle_WrongAnchorRejected_S2(t *testing.T) {
	bundleBytes := loadSkillFixtureBundleGz(t)
	attBundles := loadSkillFixtureBundles(t)

	// 用二进制锚 regex 验 skill bundle → 身份不匹配,必拒。
	if err := verifyProvenanceBundlesWithSAN(bundleBytes, attBundles, attestationSANRegex); err == nil {
		t.Fatal("S2: expected binary anchor (attestationSANRegex) to REJECT skill bundle, got nil")
	}

	// 反向 sanity:控制组 —— skill 锚 regex 对 skill bundle 仍过,证明上面的拒是
	// 因身份不符,而非配置坏了。
	if err := verifyProvenanceBundlesWithSAN(bundleBytes, attBundles, skillBundleSANRegex); err != nil {
		t.Fatalf("S2 control: skill anchor should still pass, got: %v", err)
	}
}

// W1 —— byte-for-byte 写。ExtractSkillBundleSafely(fixture bundle, tmpDir)成功,
// 且落盘文件内容 == bundle 内对应 entry 字节。
func TestExtractSkillBundleSafely_ByteForByte_W1(t *testing.T) {
	bundleBytes := loadSkillFixtureBundleGz(t)
	tmp := t.TempDir()

	written, err := ExtractSkillBundleSafely(bundleBytes, tmp)
	if err != nil {
		t.Fatalf("W1: extract failed: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("W1: nothing written")
	}

	// 抽查 skills/tc-render/SKILL.md byte-for-byte。
	checkEntries := []struct {
		entry  string
		onDisk string
	}{
		{"skills/tc-render/SKILL.md", filepath.Join(tmp, "skills", "tc-render", "SKILL.md")},
		{"skills/tc-1-start/SKILL.md", filepath.Join(tmp, "skills", "tc-1-start", "SKILL.md")},
		{"claude_md_team_global.md", filepath.Join(tmp, "CLAUDE.md")},
	}
	for _, c := range checkEntries {
		want := tarEntryBytes(t, bundleBytes, c.entry)
		got, rErr := os.ReadFile(c.onDisk)
		if rErr != nil {
			t.Fatalf("W1: read written %q: %v", c.onDisk, rErr)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("W1: byte mismatch for %q: bundle=%d bytes on-disk=%d bytes", c.entry, len(want), len(got))
		}
	}

	// CLAUDE.md 必须落在 tmp 顶层(路径映射正确),且不存在顶层 claude_md_team_global.md。
	if _, err := os.Stat(filepath.Join(tmp, "CLAUDE.md")); err != nil {
		t.Fatalf("W1: expected CLAUDE.md at root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, claudeMDBundleEntry)); !os.IsNotExist(err) {
		t.Fatalf("W1: bundle entry name should NOT appear at root, stat err=%v", err)
	}
}

// W2 —— 软链目标拒。tmpDir 预置 skills/tc-render/SKILL.md 为指向 tmpDir 外的
// symlink → ExtractSkillBundleSafely 必 error(确定性拒写,不 follow、不覆盖软链外)。
func TestExtractSkillBundleSafely_SymlinkTargetRejected_W2(t *testing.T) {
	bundleBytes := loadSkillFixtureBundleGz(t)
	tmp := t.TempDir()
	outside := t.TempDir() // tmpDir 之外
	outsideFile := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(outsideFile, []byte("ORIGINAL-SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 预置 skills/tc-render/ 目录(真实目录,非软链),其下 SKILL.md 为指向外部的软链。
	skillDir := filepath.Join(tmp, "skills", "tc-render")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillDir, "SKILL.md")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractSkillBundleSafely(bundleBytes, tmp)
	if err == nil {
		t.Fatal("W2: expected symlink target to be rejected, got nil")
	}
	// 确定性拒写:外部文件内容绝不能被改写(没 follow 软链覆盖)。
	got, rErr := os.ReadFile(outsideFile)
	if rErr != nil {
		t.Fatalf("W2: read victim: %v", rErr)
	}
	if string(got) != "ORIGINAL-SECRET\n" {
		t.Fatalf("W2: symlink was FOLLOWED — outside file got overwritten: %q", string(got))
	}
}

// W3 —— 父目录软链拒。tmpDir 预置 skills/tc-render 为指向 tmpDir 外目录的 symlink
// → 必 error。堵 O_NOFOLLOW 管不住的「父目录跳转」攻击。
func TestExtractSkillBundleSafely_ParentSymlinkRejected_W3(t *testing.T) {
	bundleBytes := loadSkillFixtureBundleGz(t)
	tmp := t.TempDir()
	outsideDir := t.TempDir() // tmpDir 之外的真实目录

	// 预置 skills/ 真实目录,其下 tc-render 为指向外部目录的软链。
	if err := os.MkdirAll(filepath.Join(tmp, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "skills", "tc-render")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractSkillBundleSafely(bundleBytes, tmp)
	if err == nil {
		t.Fatal("W3: expected parent symlink to be rejected, got nil")
	}
	// 确定性拒:外部目录里不能被写出 SKILL.md。
	if _, statErr := os.Stat(filepath.Join(outsideDir, "SKILL.md")); !os.IsNotExist(statErr) {
		t.Fatalf("W3: parent symlink was traversed — wrote into outside dir, stat err=%v", statErr)
	}
}

// W4 —— 互斥守卫。SkillWriteGuard:无软链 tmpDir → nil;skills/tc-render 为
// symlink 的 tmpDir → error。
func TestSkillWriteGuard_W4(t *testing.T) {
	// 消费机:无软链。
	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, "skills", "tc-render"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, "skills", "tc-render", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SkillWriteGuard(clean); err != nil {
		t.Fatalf("W4: clean consumer machine should pass guard, got: %v", err)
	}

	// dev 机:skills/tc-render 为软链。
	dev := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dev, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dev, "skills", "tc-render")); err != nil {
		t.Fatal(err)
	}
	if err := SkillWriteGuard(dev); err == nil {
		t.Fatal("W4: dev machine with symlinked tc-render should be rejected, got nil")
	}

	// 另一形态:skills 目录本身是软链 → 也必拒。
	dev2 := t.TempDir()
	skillsTarget := t.TempDir()
	if err := os.Symlink(skillsTarget, filepath.Join(dev2, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := SkillWriteGuard(dev2); err == nil {
		t.Fatal("W4: dev machine with symlinked skills dir should be rejected, got nil")
	}
}

// W5 —— 穿越拒。构造一个含 ../../etc/x 路径的恶意 tar(测试内造)→
// ExtractSkillBundleSafely 必 error。
func TestExtractSkillBundleSafely_TraversalRejected_W5(t *testing.T) {
	tmp := t.TempDir()

	// 测试内造恶意 tar.gz:一个 skills/../../etc/x 的 entry。
	maliciousNames := []string{
		"skills/../../etc/x",
		"../../../etc/passwd",
		"skills/../../../tmp/escape",
	}
	for _, name := range maliciousNames {
		gzBytes := buildMaliciousTarGz(t, name, []byte("PWNED\n"))
		_, err := ExtractSkillBundleSafely(gzBytes, tmp)
		if err == nil {
			t.Fatalf("W5: expected traversal entry %q to be rejected, got nil", name)
		}
	}

	// 也覆盖非白名单顶层路径(白名单语义)。
	gzBytes := buildMaliciousTarGz(t, "evil_top_level.sh", []byte("rm -rf /\n"))
	if _, err := ExtractSkillBundleSafely(gzBytes, tmp); err == nil {
		t.Fatal("W5: expected non-whitelisted top-level entry to be rejected, got nil")
	}

	// 也覆盖 bundle 内嵌 symlink entry(tar 软链类型)→ 必拒。
	symGz := buildMaliciousSymlinkTarGz(t, "skills/tc-evil", "/etc")
	if _, err := ExtractSkillBundleSafely(symGz, tmp); err == nil {
		t.Fatal("W5: expected in-bundle symlink entry to be rejected, got nil")
	}
}

func buildMaliciousTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildMaliciousSymlinkTarGz(t *testing.T, name, linkTarget string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o777,
		Typeflag: tar.TypeSymlink,
		Linkname: linkTarget,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
