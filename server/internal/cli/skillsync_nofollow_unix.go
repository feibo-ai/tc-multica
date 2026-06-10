//go:build !windows

package cli

import "syscall"

// syscallNoFollow 返回 O_NOFOLLOW 标志位。在 unix(daemon 写循环的真实部署面:
// linux 消费机 + darwin dev 机)上,O_NOFOLLOW 让 open 在末跳是符号链接时直接
// 失败 —— 这是写原语软链防护的关键一跳(invariant #5)。
func syscallNoFollow() int {
	return syscall.O_NOFOLLOW
}
