package gate

import (
	"regexp"
	"strings"
)

// HardLock 描述一条危险命令硬锁规则。
type HardLock struct {
	ID     string
	Reason string
	// pattern 只匹配命令文本，覆盖整条命令以及 &&、;、| 串联后的子命令。
	pattern *regexp.Regexp
}

// defaultHardLocks 是默认硬锁清单，与 noop 的 gates.ts 对齐。
// 每一项都允许出现在命令串的任意子命令位置，普通 feature 分支推送不受影响。
var defaultHardLocks = []HardLock{
	{
		ID:      "merge",
		Reason:  "合并 PR",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*gh\s+pr\s+merge\b`),
	},
	{
		ID:      "push-force",
		Reason:  "强制推送",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*git\s+push\b[^\n;&|]*(?:--force-with-lease\b|--force\b|(?:^|\s)-[a-zA-Z]*f[a-zA-Z]*(?:\s|$))`),
	},
	{
		ID:      "push-main",
		Reason:  "推送到 main 或 master",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*git\s+push\b[^\n;&|]*\s(?:main|master)(?:\s|$)`),
	},
	{
		ID:      "tag-push",
		Reason:  "打标签后推送",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*git\s+tag\b[^\n;]*(?:&&|;|\|)[^\n;]*\bgit\s+push\b`),
	},
	{
		ID:      "publish",
		Reason:  "发布软件包",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*(?:npm|bun|cargo)\s+publish\b`),
	},
	{
		ID:      "release",
		Reason:  "创建发布",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*gh\s+release\s+create\b`),
	},
	{
		ID:      "secret",
		Reason:  "修改仓库密钥",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*gh\s+secret\b`),
	},
	{
		ID:      "repo-delete",
		Reason:  "删除仓库",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*gh\s+repo\s+delete\b`),
	},
	{
		ID:      "repo-edit",
		Reason:  "修改仓库设置",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*gh\s+repo\s+edit\b`),
	},
	{
		ID:      "destructive",
		Reason:  "删除根目录或用户目录",
		pattern: regexp.MustCompile(`(?m)(?:^|[\n;&|])\s*rm\s+(?:-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*)\s+(?:/(?:\*|(?:\s|$))|~(?:\s|/|$))`),
	},
}

// CheckHardLock 判断命令是否命中默认硬锁。override 非空表示本次运行已获用户解锁。
func CheckHardLock(command, override string) (HardLock, bool) {
	if strings.TrimSpace(override) != "" {
		return HardLock{}, false
	}
	if strings.TrimSpace(command) == "" {
		return HardLock{}, false
	}
	for _, lock := range defaultHardLocks {
		if lock.pattern.MatchString(command) {
			return lock, true
		}
	}
	return HardLock{}, false
}

// ClaudeDeniedPatterns 返回传给 Claude CLI --disallowed-tools 的 Bash 模式。
// 这些模式与 defaultHardLocks 对应，供无法逐命令审批的外部 CLI 在启动时先挡住危险命令。
func ClaudeDeniedPatterns() []string {
	return []string{
		"Bash(gh pr merge:*)",
		"Bash(git push --force:*)",
		"Bash(git push --force-with-lease:*)",
		"Bash(git push -f:*)",
		"Bash(git push main:*)",
		"Bash(git push master:*)",
		"Bash(git push origin main:*)",
		"Bash(git push origin master:*)",
		"Bash(git tag:*)",
		"Bash(npm publish:*)",
		"Bash(bun publish:*)",
		"Bash(cargo publish:*)",
		"Bash(gh release create:*)",
		"Bash(gh secret:*)",
		"Bash(gh repo delete:*)",
		"Bash(gh repo edit:*)",
		"Bash(rm -rf /:*)",
		"Bash(rm -rf ~:*)",
	}
}
