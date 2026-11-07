//go:build !windows

// 本文件移植自 magpie 的 internal/edit/hardlink_other.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"os"
	"syscall"
)

// hardLinked 报告 path 处的文件是否还有其它名字。系统不给链接数时按没有
// 其它名字处理。
func hardLinked(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

// keepGroup 让替换文件 st 的临时文件带上原文件的组：新文件取所在目录的
// 组，所以与组共享的配置（组可写的 models.json）会在 rename 后丢掉它。用户
// 给不了的组保持创建时的样子。
func keepGroup(tmp *os.File, st os.FileInfo) {
	old, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	now, err := tmp.Stat()
	if err != nil {
		return
	}
	if cur, ok := now.Sys().(*syscall.Stat_t); ok && cur.Gid != old.Gid {
		_ = tmp.Chown(-1, int(old.Gid))
	}
}
