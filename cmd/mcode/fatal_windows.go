//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// MB_OK | MB_ICONERROR | MB_SETFOREGROUND：弹窗置前，双击启动失败时用户才看得到。
const messageBoxFlags = 0x00000000 | 0x00000010 | 0x00010000

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// showFatal 用 Windows 原生消息框显示启动失败原因：GUI 程序没有控制台，
// 日志、os.Exit 与 panic 都看不见，弹窗是唯一能让用户知道发生了什么的方式。
// 调用线程会阻塞到用户点「确定」，所以只在准备退出时调用。
func showFatal(msg string) {
	text := msg
	if hint := fatalLogHint(); hint != "" {
		text += "\n\n日志：" + hint
	}
	title, err := windows.UTF16PtrFromString("MCode")
	if err != nil {
		return
	}
	body, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	// 忽略返回值：弹窗失败也只能继续退出。
	_, _, _ = procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(body)),
		uintptr(unsafe.Pointer(title)),
		uintptr(messageBoxFlags),
	)
}
