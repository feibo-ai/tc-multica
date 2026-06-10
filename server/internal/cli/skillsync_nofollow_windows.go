//go:build windows

package cli

// syscallNoFollow 在 Windows 上返回 0(无 O_NOFOLLOW 等价标志)。daemon 无感
// skill 写循环的真实部署面是 unix(linux 消费机 + darwin dev 机);Windows 仅为
// 保持跨平台编译通过而提供桩。Windows 上的末跳软链防护改由 ExtractSkillBundleSafely
// 的 Lstat 普通文件断言 + 父链 EvalSymlinks 兜底(O_EXCL 仍生效,防新建竞态)。
func syscallNoFollow() int {
	return 0
}
