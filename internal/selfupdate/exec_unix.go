//go:build unix

package selfupdate

import "syscall"

// execFn 原地换成另一个程序（测试替换）。
var execFn = syscall.Exec
