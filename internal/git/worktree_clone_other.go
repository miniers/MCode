//go:build !darwin

package git

import "errors"

// CloneIgnoredSupported 报告当前平台是否支持把被忽略条目克隆进新 worktree。
func CloneIgnoredSupported() bool { return false }

// cloneVolumeOK 在非 darwin 平台始终不支持 clonefile。
func cloneVolumeOK(src, dst string) (bool, string) {
	return false, "当前平台不支持 clonefile"
}

// cloneTree 在非 darwin 平台始终返回不支持。
func cloneTree(src, dst string) error {
	return errors.New("clonefile unsupported")
}
