package claude

// 本文件读取 Claude Code 的登录身份与配置：身份、MCP 与项目信任都存放在 .claude.json 的
// oauthAccount 中；默认登录的文件与 ~/.claude 并列（即 ~/.claude.json），配置目录被搬迁时
// （CLAUDE_CONFIG_DIR）位于该目录下。邮箱取 oauthAccount.emailAddress，套餐取
// oauthAccount.organizationType。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/providers/loginenv"
)

// configDirectory 返回 CLI 子进程实际使用的 Claude 配置目录，空串表示按默认的 ~/.claude。
// 额度、账号与网关设置必须和子进程看同一份环境：子进程环境来自登录 Shell（loginenv.Environ），
// 只在 .zshrc 里 export CLAUDE_CONFIG_DIR 时本进程环境里并没有该变量，只读进程环境就会读到
// ~/.claude 下另一个账号的凭据，面板的邮箱/额度与实际跑 CLI 的账号对不上。
// 进程环境显式给出该变量时以它为准（哪怕为空表示用默认值），便于调用方（含测试）覆盖；
// 只有进程环境里根本没有这个变量时才去读登录 Shell 环境。
func configDirectory() string {
	if dir, ok := os.LookupEnv("CLAUDE_CONFIG_DIR"); ok {
		return strings.TrimSpace(dir)
	}
	for _, entry := range loginenv.Environ() {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// accountConfigFile 返回 Claude Code 的 .claude.json 路径：home 为用户主目录，
// 为空时用当前用户主目录；CLAUDE_CONFIG_DIR 非空时改用它（该配置目录有自己的登录）。
func accountConfigFile(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		resolved, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("claude: 解析用户主目录: %w", err)
		}
		home = resolved
	}
	if dir := configDirectory(); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	return filepath.Join(home, ".claude.json"), nil
}

// ReadAccount 读取 Claude Code 当前登录账户的邮箱与套餐。
// 配置文件缺失、内容非法或没有已登录的 oauthAccount 时返回错误。
func ReadAccount(home string) (email, plan string, err error) {
	file, err := accountConfigFile(home)
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", "", fmt.Errorf("claude: 读取 %s: %w", file, err)
	}
	var root struct {
		OAuthAccount struct {
			EmailAddress     string `json:"emailAddress"`
			OrganizationType string `json:"organizationType"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", "", fmt.Errorf("claude: 解析 %s: %w", file, err)
	}
	email = strings.TrimSpace(root.OAuthAccount.EmailAddress)
	if email == "" {
		return "", "", fmt.Errorf("claude: %s 中没有已登录的 oauthAccount", file)
	}
	return email, organizationPlanName(root.OAuthAccount.OrganizationType), nil
}

// organizationPlanName 把 oauthAccount.organizationType（如 "claude_max"）转成展示名（"Max"），
// 与方案名的首字母大写风格一致。
func organizationPlanName(organizationType string) string {
	name := strings.TrimPrefix(strings.TrimSpace(organizationType), "claude_")
	name = strings.ReplaceAll(name, "_", " ")
	words := strings.Fields(name)
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
