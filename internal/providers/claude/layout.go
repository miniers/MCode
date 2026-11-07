package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sharedEntries 定义主目录与各账号配置目录之间需要共享的文件及子目录。
// .claude.json 与 .credentials.json 等私有登录态文件严禁在此共享。
var sharedEntries = []string{
	"CLAUDE.md",
	"settings.json",
	"agents",
	"commands",
	"skills",
	"hooks",
	"plugins",
	"output-styles",
	"projects",
}

// EnsureSharedLayout 确保 Claude 账号配置目录共享主机主配置目录 ~/.claude 下的公共条目。
// 若 configDir 为空，或 configDir 与 ~/.claude 为同一目录，直接返回 nil。
func EnsureSharedLayout(configDir string) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("claude: 获取用户主目录失败: %w", err)
	}
	hostDir := filepath.Join(userHome, ".claude")
	return ensureSharedLayout(hostDir, configDir)
}

// EnsureSharedLayoutFromEnv 从环境变量切片中提取 CLAUDE_CONFIG_DIR，并尽力调用 EnsureSharedLayout。
func EnsureSharedLayoutFromEnv(env []string) error {
	var configDir string
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			configDir = strings.TrimPrefix(e, "CLAUDE_CONFIG_DIR=")
		}
	}
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	return EnsureSharedLayout(configDir)
}

// ensureSharedLayout 是共享布局的具体实现，便于单元测试注入测试目录。
func ensureSharedLayout(hostDir, configDir string) error {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}

	absConfig, err := filepath.Abs(configDir)
	if err != nil {
		return fmt.Errorf("claude: 解析 configDir 绝对路径失败: %w", err)
	}
	absHost, err := filepath.Abs(hostDir)
	if err != nil {
		return fmt.Errorf("claude: 解析 hostDir 绝对路径失败: %w", err)
	}

	// 若绝对路径相同，则为同一目录，直接返回
	if absConfig == absHost {
		return nil
	}

	// 进一步通过 EvalSymlinks 解析软链接后比较，若均成功且指向同一真实路径则直接返回
	evalConfig, errConfig := filepath.EvalSymlinks(absConfig)
	evalHost, errHost := filepath.EvalSymlinks(absHost)
	if errConfig == nil && errHost == nil && evalConfig == evalHost {
		return nil
	}

	var errs []error
	for _, entry := range sharedEntries {
		srcPath := filepath.Join(hostDir, entry)
		// 主目录中不存在该条目则跳过
		if _, err := os.Lstat(srcPath); err != nil {
			continue
		}

		dstPath := filepath.Join(configDir, entry)
		// 目标配置目录下已有同名项（通过 os.Lstat 判断，断链也视为已存在）则跳过，绝不覆盖
		if _, err := os.Lstat(dstPath); err == nil {
			continue
		}

		// 确保账号配置目录本身存在
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("claude: 创建配置目录 %s 失败: %w", configDir, err))
			continue
		}

		// 创建指向主目录项的符号链接
		if err := os.Symlink(srcPath, dstPath); err != nil {
			errs = append(errs, fmt.Errorf("claude: 创建软链接 %s -> %s 失败: %w", dstPath, srcPath, err))
		}
	}

	return errors.Join(errs...)
}
