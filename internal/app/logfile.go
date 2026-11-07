package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	// logFileName 是 app 层运行日志的文件名。
	logFileName = "mcode.log"
	// rotatedLogFileName 是轮转后的旧日志文件名，下一次轮转会覆盖它。
	rotatedLogFileName = "mcode.log.1"
	// maxLogFileSize 是单个日志文件的大小上限，超过后在启动阶段轮转（5MB）。
	maxLogFileSize int64 = 5 << 20
)

var (
	logFileMu sync.Mutex
	// logFilePath 记录本次启动实际写入的日志文件路径，供诊断导出读取。
	logFilePath string
)

// logDir 返回 mcode 日志目录：优先放在数据目录下的 logs（与 mcode.db、config.json 同处，
// MCODE_DATA 隔离的开发实例也就不会与正式安装共用一份日志）；数据目录为空时退回用户配置目录下的 mcode/logs。
func logDir(dataDir string) (string, error) {
	if dataDir != "" {
		return filepath.Join(dataDir, "logs"), nil
	}
	base, err := os.UserConfigDir()
	if err == nil && base != "" {
		return filepath.Join(base, "mcode", "logs"), nil
	}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil || home == "" {
		if err != nil {
			return "", fmt.Errorf("resolve user config directory: %w", err)
		}
		return "", fmt.Errorf("resolve user home directory: %w", homeErr)
	}
	return filepath.Join(home, ".config", "mcode", "logs"), nil
}

// logFilePaths 按「先旧后新」返回诊断导出用的日志路径；日志目录不可用时返回 nil。
func logFilePaths() []string {
	logFileMu.Lock()
	defer logFileMu.Unlock()
	return logFilePathsLocked()
}

// logFilePathsLocked 在已持锁的前提下计算日志路径，缺失的文件由 ExportDiagnostics 容忍。
func logFilePathsLocked() []string {
	path := logFilePath
	if path == "" {
		dir, err := logDir("")
		if err != nil {
			return nil
		}
		path = filepath.Join(dir, logFileName)
	}
	return []string{filepath.Join(filepath.Dir(path), rotatedLogFileName), path}
}

// rotateLogIfNeeded 在日志超过 maxLogFileSize 时改名为 mcode.log.1（覆盖旧文件）；
// 轮转失败只影响诊断内容完整性，不阻塞启动。
func rotateLogIfNeeded(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxLogFileSize {
		return
	}
	rotated := filepath.Join(filepath.Dir(path), rotatedLogFileName)
	// 先删后改名，避免 Windows 上目标已存在导致改名失败。
	_ = os.Remove(rotated)
	_ = os.Rename(path, rotated)
}

// openLogFile 打开（必要时先轮转）日志文件，返回写入句柄与文件路径。
// 目录创建与打开都在锁内完成，并发调用安全。
func openLogFile(dataDir string) (io.WriteCloser, string, error) {
	logFileMu.Lock()
	defer logFileMu.Unlock()

	dir, err := logDir(dataDir)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create log directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, logFileName)
	rotateLogIfNeeded(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open log file %s: %w", path, err)
	}
	logFilePath = path
	return f, path, nil
}
