package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sharedEntries 定义主目录与各账号目录之间需要共享的文件及子目录。
// auth.json 等私有凭据文件严禁在此共享。
var sharedEntries = []string{
	"config.toml",
	"AGENTS.md",
	"prompts",
	"skills",
	"rules",
	"sessions",
	"archived_sessions",
}

// EnsureSharedLayout 确保 Codex 账号目录共享主机主配置目录 ~/.codex 下的公共条目。
// 若 home 为空，或 home 与 ~/.codex 为同一目录，直接返回 nil。
func EnsureSharedLayout(home string) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("codex: 获取用户主目录失败: %w", err)
	}
	hostDir := filepath.Join(userHome, ".codex")
	return ensureSharedLayout(hostDir, home)
}

// ensureSharedLayout 是共享布局的具体实现，便于单元测试注入测试目录。
func ensureSharedLayout(hostDir, home string) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}

	absHome, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("codex: 解析 home 绝对路径失败: %w", err)
	}
	absHost, err := filepath.Abs(hostDir)
	if err != nil {
		return fmt.Errorf("codex: 解析 hostDir 绝对路径失败: %w", err)
	}

	// 若绝对路径相同，则为同一目录，直接返回
	if absHome == absHost {
		return nil
	}

	// 进一步通过 EvalSymlinks 解析软链接后比较，若均成功且指向同一真实路径则直接返回
	evalHome, errHome := filepath.EvalSymlinks(absHome)
	evalHost, errHost := filepath.EvalSymlinks(absHost)
	if errHome == nil && errHost == nil && evalHome == evalHost {
		return nil
	}

	var errs []error
	for _, entry := range sharedEntries {
		srcPath := filepath.Join(hostDir, entry)
		// 主目录中不存在该条目则跳过
		if _, err := os.Lstat(srcPath); err != nil {
			continue
		}

		dstPath := filepath.Join(home, entry)
		// 账号目录下已有同名项（通过 os.Lstat 判断，断链也视为已存在）则跳过，绝不覆盖
		if _, err := os.Lstat(dstPath); err == nil {
			continue
		}

		// 确保账号目录本身存在
		if err := os.MkdirAll(home, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("codex: 创建账号目录 %s 失败: %w", home, err))
			continue
		}

		// 创建指向主目录项的符号链接
		if err := os.Symlink(srcPath, dstPath); err != nil {
			errs = append(errs, fmt.Errorf("codex: 创建软链接 %s -> %s 失败: %w", dstPath, srcPath, err))
		}
	}

	return errors.Join(errs...)
}
