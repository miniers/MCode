//go:build windows

package fsutil

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// junction 用 mklink /J 创建目录联接：与目录软链接不同，普通用户即可创建。
// cmd.exe 对引号的解析与 Go 的参数转义不一致，所以直接写整条命令行；Windows 路径不会含双引号。
func junction(target, link string) error {
	if strings.ContainsRune(target, '"') || strings.ContainsRune(link, '"') {
		return fmt.Errorf("路径含双引号: %s -> %s", link, target)
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       fmt.Sprintf(`cmd.exe /d /c mklink /J "%s" "%s"`, link, target),
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mklink /J: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
