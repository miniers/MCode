//go:build darwin

package git

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// CloneIgnoredSupported 报告当前平台是否支持把被忽略条目克隆进新 worktree。
func CloneIgnoredSupported() bool { return true }

// cloneVolumeOK 检查 src 位于 APFS 卷上且 src 与 dst 在同一卷，只有这样才能使用 clonefile。
func cloneVolumeOK(src, dst string) (bool, string) {
	var st unix.Statfs_t
	if err := unix.Statfs(src, &st); err != nil {
		return false, fmt.Sprintf("无法读取 %s 的文件系统信息：%v", src, err)
	}
	fsType := string(bytes.TrimRight(st.Fstypename[:], "\x00"))
	if fsType != "apfs" {
		return false, fmt.Sprintf("源目录文件系统为 %s，仅支持 apfs", fsType)
	}

	var srcStat, dstStat unix.Stat_t
	if err := unix.Stat(src, &srcStat); err != nil {
		return false, fmt.Sprintf("无法读取 %s 的状态：%v", src, err)
	}
	if err := unix.Stat(dst, &dstStat); err != nil {
		return false, fmt.Sprintf("无法读取 %s 的状态：%v", dst, err)
	}
	if srcStat.Dev != dstStat.Dev {
		return false, "不在同一卷"
	}
	return true, ""
}

// cloneTree 用 clonefile 写时复制整棵树（对目录会递归克隆）。
func cloneTree(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
