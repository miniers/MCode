//go:build windows

// 本文件移植自 magpie 的 internal/edit/hardlink_windows.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"os"

	"golang.org/x/sys/windows"
)

// hardLinked 报告 path 处的文件是否还有其它名字。
func hardLinked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var fi windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &fi) == nil && fi.NumberOfLinks > 1
}

// keepGroup：Windows 文件没有要保留的 Unix 组。
func keepGroup(*os.File, os.FileInfo) {}
