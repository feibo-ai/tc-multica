package cli

// skillsync.go 是 daemon 无感 skill 分发(mini-ADR v4 ⑩c)的写路径安全核心。
// 这是 RCE 面:一个已验签通过的 bundle 仍然不能被无条件落盘,因为 tar entry 路径、
// 目标位置的软链、父目录软链都可能把字节写到 claudeDir 白名单之外(覆盖 ~/.ssh、
// ~/.bashrc 等)。本文件逐条硬实现 invariant #5(写路径安全)与 invariant #6
// (dev/消费机互斥)。所有违规都是**确定性拒写 + error**:绝不 follow 软链、绝不
// 静默跳过。任一 entry 违规 → 整体 fail-closed error。
//
// 调用约束:ExtractSkillBundleSafely 只能在 VerifySkillBundleAttestation 返回 nil
// 之后调用;本文件不做验签,只做落盘的路径安全。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// skillsSubdir 是 bundle 内 skills/ 顶层路径,映射到 <claudeDir>/skills/。
const skillsSubdir = "skills"

// claudeMDBundleName 是 bundle 内团队全局 CLAUDE.md 的固定文件名(顶层)。
const claudeMDBundleEntry = "claude_md_team_global.md"

// claudeMDTarget 是它在消费机上的落地文件名。
const claudeMDTargetName = "CLAUDE.md"

// ExtractSkillBundleSafely 把一份**已验签通过**的 skill bundle(tar.gz)byte-for-byte
// 落盘到 claudeDir(调用方传 ~/.claude)。返回成功写入的目标路径清单(便于调用方
// 诊断 / 决定是否回滚)。任一 entry 违反写路径安全不变量 → 整体返回非 nil error
// (fail-closed),已写清单一并返回。
//
// 写路径安全不变量(逐条 · invariant #5):
//   - 路径白名单:仅接受顶层 `skills/<rest>`(→ <claudeDir>/skills/<rest>)与
//     `claude_md_team_global.md`(→ <claudeDir>/CLAUDE.md);其它顶层路径一律拒。
//   - 目录穿越拒:filepath.Clean 后目标必须仍在 <claudeDir>/skills/ 内或恰为
//     <claudeDir>/CLAUDE.md;含 .. 逃逸 → 拒 + error。
//   - 父目录软链拒:写前对目标每一级父目录 EvalSymlinks,断言解析后真实路径仍在
//     <claudeDir> 白名单根内;父链含逃出白名单的软链 → 拒 + error。
//   - 写原语二分:新建用 O_CREATE|O_EXCL|O_WRONLY|O_NOFOLLOW;更新先 Lstat 断言
//     目标是普通文件(非 symlink/目录/特殊),再写同目录 temp + Rename 原子覆盖。
//   - byte-for-byte:写入内容 == tar entry 内容。
func ExtractSkillBundleSafely(bundleGzBytes []byte, claudeDir string) (written []string, err error) {
	// 白名单根:所有写入必须落在 claudeRoot 内。先 Abs+Clean,再 EvalSymlinks 把
	// 根自身的软链祖先(如 macOS 上 /var -> /private/var)规约成真实路径 —— 之后所有
	// 父链 EvalSymlinks 的容器比较都基于这个**已解析的真实根**,否则正常的软链祖先
	// 会被误判为逃逸。根可以通过软链祖先抵达(安全);要堵的是根**之下**的逃逸软链。
	abs, absErr := filepath.Abs(filepath.Clean(claudeDir))
	if absErr != nil {
		return nil, fmt.Errorf("resolve claudeDir %q: %w", claudeDir, absErr)
	}
	claudeRoot := abs
	if resolved, evErr := filepath.EvalSymlinks(abs); evErr == nil {
		claudeRoot = filepath.Clean(resolved)
	} else if !os.IsNotExist(evErr) {
		return nil, fmt.Errorf("eval claudeDir %q: %w", abs, evErr)
	}
	skillsRoot := filepath.Join(claudeRoot, skillsSubdir)
	claudeMDPath := filepath.Join(claudeRoot, claudeMDTargetName)

	gz, gzErr := gzip.NewReader(bytes.NewReader(bundleGzBytes))
	if gzErr != nil {
		return nil, fmt.Errorf("open gzip: %w", gzErr)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, readErr := tr.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, fmt.Errorf("read tar header: %w", readErr)
		}

		// 1) 路径白名单 + 穿越拒:把 tar entry 名映射成绝对目标路径。
		target, isDir, mapErr := mapBundleEntry(hdr, claudeRoot, skillsRoot, claudeMDPath)
		if mapErr != nil {
			return written, mapErr
		}
		if target == "" {
			// 映射为空 = 这是被白名单拒绝的顶层路径之外的 entry。mapBundleEntry
			// 已经把所有需要拒的情况转成 error;能到这里说明是可安全忽略的 entry
			// (例如纯 "skills/" 根目录本身,会在父链校验时被 MkdirAll 处理)。
			continue
		}

		// 2) 父目录软链拒:对目标的每一级父目录 EvalSymlinks,确保真实路径仍在
		//    claudeRoot 白名单内。父链含逃出白名单的软链 → 拒。
		if symErr := assertParentChainInsideRoot(target, claudeRoot); symErr != nil {
			return written, symErr
		}

		switch {
		case isDir:
			// tar 目录 entry:MkdirAll 目标(每级仍受上面的父链软链校验约束 ——
			// 注意 MkdirAll 只创建不存在的层级,已存在的软链层级已在 assert 里拒)。
			if mkErr := os.MkdirAll(target, 0o755); mkErr != nil {
				return written, fmt.Errorf("mkdir %q: %w", target, mkErr)
			}
		default:
			// 普通文件:先确保父目录存在(同样受白名单约束),再走写原语二分。
			parent := filepath.Dir(target)
			if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
				return written, fmt.Errorf("mkdir parent %q: %w", parent, mkErr)
			}
			// 父目录可能刚被 MkdirAll 新建,重新校验一次父链(纵深防御)。
			if symErr := assertParentChainInsideRoot(target, claudeRoot); symErr != nil {
				return written, symErr
			}

			content, contentErr := io.ReadAll(tr)
			if contentErr != nil {
				return written, fmt.Errorf("read tar entry %q: %w", hdr.Name, contentErr)
			}
			if writeErr := writeFileAtomicNoFollow(target, content); writeErr != nil {
				return written, writeErr
			}
			written = append(written, target)
		}
	}

	return written, nil
}

// mapBundleEntry 把一个 tar header 映射成绝对目标路径,并执行白名单 + 穿越校验。
// 返回 (target, isDir, error):
//   - target=="" 且 error==nil:可安全忽略的 entry(如 skills/ 根目录本身)。
//   - error!=nil:违反白名单或含 .. 穿越 → 确定性拒。
func mapBundleEntry(hdr *tar.Header, claudeRoot, skillsRoot, claudeMDPath string) (target string, isDir bool, err error) {
	// 只处理普通文件与目录;符号链接 / 硬链接 / 设备等特殊 entry 一律拒
	// (bundle 里不该有,出现即视为攻击)。PAX / GNU 扩展 header 是无 payload 的
	// 元数据 entry(git archive 会带一个 pax_global_header),它们不落盘 —— 安全
	// 忽略(target=="",isDir 任意),绝不当文件写。
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		isDir = false
	case tar.TypeDir:
		isDir = true
	case tar.TypeXGlobalHeader, tar.TypeXHeader:
		// 扩展头元数据:无内容、不映射到任何路径,直接忽略。
		return "", false, nil
	default:
		return "", false, fmt.Errorf("entry %q has disallowed tar type %q (only regular files and dirs allowed)", hdr.Name, string(hdr.Typeflag))
	}

	// 规整 entry 名:用 forward slash 视角先 Clean,拒绝绝对路径与 .. 穿越。
	name := strings.TrimPrefix(hdr.Name, "./")
	cleaned := filepath.Clean("/" + name) // 强制相对根,吃掉任何 ../ 逃逸
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", false, nil // 空 / 当前目录,忽略
	}
	// 显式拒绝任何残留的 .. 段(Clean 后理论上不会有逃出根的,但绝对路径 + 防御)。
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
		return "", false, fmt.Errorf("entry %q escapes via .. (path traversal rejected)", hdr.Name)
	}
	if filepath.IsAbs(name) {
		return "", false, fmt.Errorf("entry %q is an absolute path (rejected)", hdr.Name)
	}

	// 白名单顶层路径映射。
	switch {
	case cleaned == claudeMDBundleEntry:
		target = claudeMDPath
	case cleaned == skillsSubdir:
		// skills/ 根目录本身:作为目录创建即可,但用 skillsRoot 作 target。
		if !isDir {
			return "", false, fmt.Errorf("entry %q is the skills root but not a directory", hdr.Name)
		}
		target = skillsRoot
	case strings.HasPrefix(cleaned, skillsSubdir+"/"):
		rest := strings.TrimPrefix(cleaned, skillsSubdir+"/")
		if rest == "" {
			return "", false, fmt.Errorf("entry %q maps to empty skills path", hdr.Name)
		}
		target = filepath.Join(skillsRoot, filepath.FromSlash(rest))
	default:
		// 其它顶层路径(如 README 顶层、任意文件)一律拒 —— 白名单语义。
		return "", false, fmt.Errorf("entry %q is outside the skills/ + CLAUDE.md whitelist (rejected)", hdr.Name)
	}

	// 穿越再校验:Clean 后的绝对目标必须仍在 claudeRoot 内,且对 skills 条目必须
	// 在 skillsRoot 内(CLAUDE.md 恰为 claudeMDPath)。
	cleanTarget := filepath.Clean(target)
	if target == claudeMDPath {
		if cleanTarget != claudeMDPath {
			return "", false, fmt.Errorf("entry %q resolved outside CLAUDE.md target (rejected)", hdr.Name)
		}
	} else {
		if cleanTarget != skillsRoot && !strings.HasPrefix(cleanTarget, skillsRoot+string(os.PathSeparator)) {
			return "", false, fmt.Errorf("entry %q resolved outside skills root %q (path traversal rejected)", hdr.Name, skillsRoot)
		}
	}
	if !pathInsideRoot(cleanTarget, claudeRoot) {
		return "", false, fmt.Errorf("entry %q resolved outside claudeDir %q (rejected)", hdr.Name, claudeRoot)
	}
	return cleanTarget, isDir, nil
}

// pathInsideRoot 判断 p(已 Clean 的绝对路径)是否等于 root 或落在 root 之下。
func pathInsideRoot(p, root string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// assertParentChainInsideRoot 对 target 的每一级父目录(从 root 向 target 逐级)做
// EvalSymlinks,断言每一级解析后的真实路径仍在 root 白名单内。任一级父目录是逃出
// root 的软链 → 返回 error(确定性拒)。不存在的层级跳过(它们将由 MkdirAll 在
// root 内新建,新建路径天然在 root 内)。
//
// 这一步堵的是「目录本身是软链」的攻击:例如 <claudeDir>/skills/tc-render 是指向
// /tmp/evil 的软链,O_NOFOLLOW 只防最末一跳(目标文件),管不住父目录跳转 —— 这里
// 显式逐级 EvalSymlinks 把父链锁死在 root 内。
// 前置约定:root 必须已是 EvalSymlinks 解析后的真实路径(ExtractSkillBundleSafely
// 在入口处解析好),故此处不再校验 root 自身 —— root 通过软链祖先抵达是安全的。
func assertParentChainInsideRoot(target, root string) error {
	// 收集 root 与 target 之间的每一级父目录(含 target 的直接父目录,不含 target)。
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("rel %q under %q: %w", target, root, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("target %q is not under root %q (rejected)", target, root)
	}

	segments := strings.Split(rel, string(os.PathSeparator))
	cur := root
	// 逐级下钻;最后一段是 target 自身(文件或目录),其父目录是倒数第二段。
	for i := 0; i < len(segments)-1; i++ {
		cur = filepath.Join(cur, segments[i])
		real, evalErr := evalIfExists(cur)
		if evalErr != nil {
			return fmt.Errorf("eval parent %q: %w", cur, evalErr)
		}
		if real == "" {
			// 该层级尚不存在 —— 后续 MkdirAll 会在 root 内创建,安全。一旦某级不
			// 存在,更深层级也不可能已存在为逃逸软链,停止下钻。
			return nil
		}
		if !pathInsideRoot(real, root) {
			return fmt.Errorf("parent directory %q resolves via symlink to %q outside claude root %q (rejected)", cur, real, root)
		}
	}
	return nil
}

// evalIfExists 对 p 做 EvalSymlinks;若 p 不存在返回 ("", nil)(让调用方知道这层
// 尚未创建);其它错误透传。返回的真实路径已 Clean + 绝对。
func evalIfExists(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	abs, absErr := filepath.Abs(real)
	if absErr != nil {
		return "", absErr
	}
	return filepath.Clean(abs), nil
}

// writeFileAtomicNoFollow 把 content 写到 target,执行写原语二分(invariant #5):
//   - 目标不存在:O_CREATE|O_EXCL|O_WRONLY|O_NOFOLLOW 直接新建写入。O_EXCL 保证
//     竞态下不会覆盖被攻击者抢先植入的软链;O_NOFOLLOW 保证不跟随末跳软链。
//   - 目标已存在:Lstat 断言必须是**普通文件**(IsRegular 且非 symlink)。是
//     symlink/目录/特殊文件 → 确定性拒写 + error(绝不 follow、绝不静默跳过)。
//     是普通文件 → 写**同目录** temp(O_CREATE|O_EXCL|O_NOFOLLOW)→ Rename 原子覆盖。
func writeFileAtomicNoFollow(target string, content []byte) error {
	info, statErr := os.Lstat(target)
	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("lstat %q: %w", target, statErr)
		}
		// 新建路径:O_EXCL|O_NOFOLLOW。
		f, openErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscallNoFollow(), 0o644)
		if openErr != nil {
			return fmt.Errorf("create %q (O_EXCL|O_NOFOLLOW): %w", target, openErr)
		}
		if _, wErr := f.Write(content); wErr != nil {
			f.Close()
			return fmt.Errorf("write %q: %w", target, wErr)
		}
		if cErr := f.Close(); cErr != nil {
			return fmt.Errorf("close %q: %w", target, cErr)
		}
		return nil
	}

	// 目标已存在:必须是普通文件,否则确定性拒。
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write %q: existing target is a symlink (rejected, not followed)", target)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to write %q: existing target is not a regular file (mode=%v, rejected)", target, info.Mode())
	}

	// 普通文件 → 同目录 temp(O_EXCL|O_NOFOLLOW)+ Rename 原子覆盖。
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	tmp, tmpErr := createExclTemp(dir, base)
	if tmpErr != nil {
		return tmpErr
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, wErr := tmp.Write(content); wErr != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp for %q: %w", target, wErr)
	}
	if cErr := tmp.Close(); cErr != nil {
		cleanup()
		return fmt.Errorf("close temp for %q: %w", target, cErr)
	}
	// Rename 原子覆盖普通文件(同目录,落在同 mount 上)。
	if rErr := os.Rename(tmpName, target); rErr != nil {
		cleanup()
		return fmt.Errorf("rename temp -> %q: %w", target, rErr)
	}
	return nil
}

// createExclTemp 在 dir 内用 O_CREATE|O_EXCL|O_WRONLY|O_NOFOLLOW 创建一个唯一的
// temp 文件并返回打开的句柄。用 PID + 递增计数器构造唯一名;O_EXCL 保证不与既存
// 文件/软链冲突,失败即换名重试。
func createExclTemp(dir, base string) (*os.File, error) {
	for i := 0; i < 1000; i++ {
		name := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", base, os.Getpid(), i))
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscallNoFollow(), 0o644)
		if err == nil {
			return f, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, fmt.Errorf("create temp in %q: %w", dir, err)
	}
	return nil, fmt.Errorf("exhausted temp name attempts in %q", dir)
}

// SkillWriteGuard 是写循环启动前的互斥配置守卫(invariant #6:dev/消费机互斥)。
// 若 <claudeDir>/skills 本身是 symlink,或其下任一 tc-* 条目是 symlink(= dev 机
// sync-team-config.sh 的 git 软链工作流),返回 error 拒启 daemon 写循环 —— 此机
// 用 git 软链 sync,daemon 写循环会破坏软链布局。消费机(无软链)→ 返回 nil。
func SkillWriteGuard(claudeDir string) error {
	skillsDir := filepath.Join(claudeDir, skillsSubdir)

	// skills 目录本身是软链 → dev 机布局,拒。
	if info, err := os.Lstat(skillsDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("此机用 git 软链 sync(%s 是 symlink),拒启 daemon 写循环(invariant #6:dev/消费机互斥)", skillsDir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat %q: %w", skillsDir, err)
	}

	// skills/ 下任一 tc-* 条目是软链 → dev 机布局,拒。
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // skills 目录还不存在 → 消费机首次,放行。
		}
		return fmt.Errorf("read %q: %w", skillsDir, err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "tc-") {
			continue
		}
		full := filepath.Join(skillsDir, e.Name())
		info, lErr := os.Lstat(full)
		if lErr != nil {
			return fmt.Errorf("lstat %q: %w", full, lErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("此机用 git 软链 sync(%s 是 symlink),拒启 daemon 写循环(invariant #6:dev/消费机互斥)", full)
		}
	}
	return nil
}
