package fsutil

import (
	"fmt"
	"os"
	"runtime"
)

var (
	// linkGOOS 与 symlink 是 LinkOrCopy 的平台标识与建软链接函数，测试中可替换。
	linkGOOS = runtime.GOOS
	symlink  = os.Symlink
)

// LinkOrCopy 在 link 处创建指向 target 的软链接。
// Windows 未开启开发者模式时建软链接需要管理员权限，失败后按无需特权的方式退化：
// 目录建目录联接（junction）；普通文件先建硬链接（写入仍落到原文件），跨卷等原因失败时复制一份。
// 其他平台软链接失败时直接返回错误。
func LinkOrCopy(target, link string) error {
	err := symlink(target, link)
	if err == nil || linkGOOS != "windows" {
		return err
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		return err
	}
	if info.IsDir() {
		if jErr := junction(target, link); jErr != nil {
			return fmt.Errorf("%w；目录联接也失败: %v", err, jErr)
		}
		return nil
	}
	if os.Link(target, link) == nil {
		return nil
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		return fmt.Errorf("%w；复制也失败: %v", err, readErr)
	}
	return os.WriteFile(link, data, 0o600)
}
