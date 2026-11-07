//go:build !windows

package main

// showFatal 在非 Windows 平台为空操作：macOS 从终端或 Finder 启动时错误会写进
// stderr 与日志文件，不需要（也不该在 App.Run 之前）弹原生对话框。
func showFatal(msg string) {}
