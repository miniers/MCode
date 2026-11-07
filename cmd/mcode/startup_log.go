package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
)

// 早期日志文件名。GUI 程序（Windows 的 MCode.exe）没有控制台，双击启动时 stderr 全部丢失，
// 启动早期的日志与崩溃堆栈必须落到文件里才有排查线索。
const (
	startupLogFileName = "startup.log"
	crashLogFileName   = "crash.log"
)

// startupLogPaths 记录本次启动的早期日志位置，供致命错误弹窗提示用户。
// 只在 main 启动阶段写入一次，之后只读，不需要加锁。
var startupLogPaths struct {
	dir        string
	startupLog string
	crashLog   string
}

// startupLogDir 返回 mcode 日志目录：<数据目录>/logs，与 internal/app 的 mcode.log 同目录。
// 数据目录为空时退回 <用户配置目录>/mcode/logs，再退回 ~/.config，最后用临时目录，保证日志总有落点。
func startupLogDir(dataDir string) string {
	if dataDir != "" {
		return filepath.Join(dataDir, "logs")
	}
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, "mcode", "logs")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "mcode", "logs")
	}
	return os.TempDir()
}

// initStartupLog 在 config.Load 之前接管日志：追加写 startup.log，同时保留 stderr；
// 并把未捕获 panic 的崩溃输出额外写到 crash.log。返回 startup.log 路径（打不开时为空串）。
// app.Open 之后会把 log 输出切到 mcode.log，这里的文件句柄不必关闭，进程退出时一并释放。
func initStartupLog(dataDir string) string {
	dir := startupLogDir(dataDir)
	startupLogPaths.dir = dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("failed to create log directory %s: %v", dir, err)
		return ""
	}

	startupPath := filepath.Join(dir, startupLogFileName)
	f, err := os.OpenFile(startupPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("failed to open startup log %s: %v", startupPath, err)
	} else {
		startupLogPaths.startupLog = startupPath
		// 文件写在前面：Windows GUI 进程没有 stderr，写 stderr 会失败，而 MultiWriter
		// 遇到第一个错误就停止，放在后面才能保证文件里一定有内容。
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}

	crashPath := filepath.Join(dir, crashLogFileName)
	cf, err := os.OpenFile(crashPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("failed to open crash log %s: %v", crashPath, err)
	} else {
		// SetCrashOutput 内部会 dup 文件描述符，返回后本地句柄可以立刻关闭。
		if crashErr := debug.SetCrashOutput(cf, debug.CrashOptions{}); crashErr != nil {
			log.Printf("failed to redirect crash output to %s: %v", crashPath, crashErr)
		} else {
			startupLogPaths.crashLog = crashPath
		}
		_ = cf.Close()
	}

	log.Printf("startup: pid=%d os=%s args=%q", os.Getpid(), runtime.GOOS, os.Args)
	return startupLogPaths.startupLog
}

// fatalLogHint 返回致命错误时要提示用户的日志位置：优先 startup.log，其次 crash.log 与日志目录。
func fatalLogHint() string {
	switch {
	case startupLogPaths.startupLog != "":
		return startupLogPaths.startupLog
	case startupLogPaths.crashLog != "":
		return startupLogPaths.crashLog
	default:
		return startupLogPaths.dir
	}
}

// fatalExit 记录致命错误、在 Windows 上弹窗告知用户，然后以退出码 1 结束。
// 日志文本与原来的 log.Fatalf 保持一致，便于沿用既有排查习惯。
func fatalExit(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	showFatal(msg)
	os.Exit(1)
}
