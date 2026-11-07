package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// Forge 表示远端代码托管平台，决定使用哪个 CLI 自动化 PR/MR。
type Forge string

const (
	// ForgeUnknown 表示未识别（或暂不支持）的托管平台。
	ForgeUnknown Forge = ""
	// ForgeGitHub 表示 GitHub，使用 gh CLI。
	ForgeGitHub Forge = "github"
	// ForgeGitLab 表示 GitLab，使用 glab CLI。
	ForgeGitLab Forge = "gitlab"
)

// DetectForge 根据远端 URL 探测托管平台。
// 支持 https:// 与 scp 风格（user@host:owner/repo.git）两类地址：
// 主机名为 github.com 时返回 ForgeGitHub，主机名包含 "gitlab" 时返回 ForgeGitLab，
// 其余（含空字符串与本地路径）返回 ForgeUnknown。
func DetectForge(remoteURL string) Forge {
	host := remoteHost(remoteURL)
	switch {
	case host == "github.com":
		return ForgeGitHub
	case strings.Contains(host, "gitlab"):
		return ForgeGitLab
	default:
		return ForgeUnknown
	}
}

// remoteHost 提取远端 URL 的小写主机名，忽略用户信息与端口；无法识别时返回空字符串。
func remoteHost(remoteURL string) string {
	s := strings.TrimSpace(remoteURL)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if k := strings.IndexByte(rest, ':'); k >= 0 {
			rest = rest[:k]
		}
		return strings.ToLower(rest)
	}
	// scp 风格：user@host:path 或 host:path
	rest := s
	if at := strings.Index(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	k := strings.IndexByte(rest, ':')
	if k <= 0 {
		return ""
	}
	return strings.ToLower(rest[:k])
}

// CommandExecutor 执行外部命令并返回合并后的 stdout+stderr。
type CommandExecutor func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// ForgeClient 通过 gh/glab CLI 创建与合入 PR/MR。
type ForgeClient struct {
	Exec CommandExecutor
}

// NewForgeClient 创建 ForgeClient；exec 为 nil 时使用 os/exec 默认实现
// （exec.CommandContext + CombinedOutput，工作目录为 dir）。
func NewForgeClient(exec CommandExecutor) *ForgeClient {
	if exec == nil {
		exec = defaultCommandExecutor
	}
	return &ForgeClient{Exec: exec}
}

func defaultCommandExecutor(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	// 按登录 Shell 的 PATH 解析二进制，避免 GUI 进程缺少用户 PATH 目录时找不到 gh/glab
	if p, err := loginenv.LookPath(name); err == nil {
		name = p
	}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, name, args...))
	cmd.Dir = dir
	// 继承登录 Shell 环境变量（含代理等），与命令行使用的环境保持一致
	cmd.Env = loginenv.Environ()
	return cmd.CombinedOutput()
}

// PullRequestSpec 描述创建 PR/MR 所需的标题、正文与源/目标分支。
type PullRequestSpec struct {
	Title string
	Body  string
	Head  string
	Base  string
}

// ErrForgeUnsupported 表示当前托管平台不支持通过 CLI 自动创建/合入 PR/MR。
var ErrForgeUnsupported = errors.New("当前代码托管平台不支持自动创建 PR/MR，请手动处理")

// urlPattern 匹配命令输出中的 Web 地址。
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// CreatePullRequest 创建 PR/MR 并返回其 Web URL。
// GitHub 使用 gh pr create，若提示已存在则回退 gh pr view 复用已有 URL；
// GitLab 使用 glab mr create --yes，已存在时复用输出中打印的 URL。
// 未知平台返回 ErrForgeUnsupported。
func (f *ForgeClient) CreatePullRequest(ctx context.Context, dir string, forge Forge, spec PullRequestSpec) (string, error) {
	switch forge {
	case ForgeGitHub:
		out, err := f.exec(ctx, dir, "gh", "pr", "create",
			"--title", spec.Title, "--body", spec.Body, "--head", spec.Head, "--base", spec.Base)
		if err != nil {
			if strings.Contains(strings.ToLower(string(out)), "already exists") {
				// PR 已存在：优先用 gh pr view 取回 URL，失败再退回创建输出中的链接
				viewOut, viewErr := f.exec(ctx, dir, "gh", "pr", "view", spec.Head, "--json", "url", "-q", ".url")
				if viewErr == nil {
					if url := lastURL(string(viewOut)); url != "" {
						return url, nil
					}
				}
				if url := lastURL(string(out)); url != "" {
					return url, nil
				}
			}
			return "", commandError("gh", "pr create", out, err)
		}
		if url := lastURL(string(out)); url != "" {
			return url, nil
		}
		return "", fmt.Errorf("gh pr create: 未在输出中找到 PR 地址：%s", outputSummary(out))
	case ForgeGitLab:
		out, err := f.exec(ctx, dir, "glab", "mr", "create",
			"--title", spec.Title, "--description", spec.Body,
			"--source-branch", spec.Head, "--target-branch", spec.Base, "--yes")
		if err != nil {
			// MR 已存在时 glab 同样以非零码退出并打印已有 MR 的 URL，直接复用；
			// 鉴权/网络等其它失败不能因为输出里恰好带地址就当成功。
			if strings.Contains(strings.ToLower(string(out)), "already exists") {
				if url := lastURL(string(out)); url != "" {
					return url, nil
				}
			}
			return "", commandError("glab", "mr create", out, err)
		}
		if url := lastURL(string(out)); url != "" {
			return url, nil
		}
		return "", fmt.Errorf("glab mr create: 未在输出中找到 MR 地址：%s", outputSummary(out))
	default:
		return "", ErrForgeUnsupported
	}
}

// MergePullRequest 对已创建的 PR/MR 执行 squash 合入。
// GitHub 使用 gh pr merge --squash，GitLab 使用 glab mr merge --yes --squash --remove-source-branch。
// 未知平台返回 ErrForgeUnsupported。
func (f *ForgeClient) MergePullRequest(ctx context.Context, dir string, forge Forge, ref string) error {
	switch forge {
	case ForgeGitHub:
		out, err := f.exec(ctx, dir, "gh", "pr", "merge", ref, "--squash")
		if err != nil {
			return commandError("gh", "pr merge", out, err)
		}
		return nil
	case ForgeGitLab:
		out, err := f.exec(ctx, dir, "glab", "mr", "merge", ref, "--yes", "--squash", "--remove-source-branch")
		if err != nil {
			return commandError("glab", "mr merge", out, err)
		}
		return nil
	default:
		return ErrForgeUnsupported
	}
}

// IsRetryableForgeError 判断远端阶段失败是否值得延时重试：
// 覆盖 Forge 仍在检查（405 / checking）、暂不可合入（not mergeable）、
// 网络瞬态故障（timeout / connection reset / DNS 解析失败）与 ctx 超时。
func IsRetryableForgeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, keyword := range []string{
		"405", "checking", "not mergeable", "timeout", "timed out",
		"connection reset", "could not resolve host",
	} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// exec 调用注入的命令执行器（Exec 为 nil 时按默认实现处理）。
func (f *ForgeClient) exec(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if f == nil {
		return nil, errors.New("forge client is nil")
	}
	exec := f.Exec
	if exec == nil {
		exec = defaultCommandExecutor
	}
	return exec(ctx, dir, name, args...)
}

// commandError 包装 CLI 失败信息，并保留输出中的关键提示供 IsRetryableForgeError 分类。
func commandError(name, sub string, out []byte, err error) error {
	detail := outputSummary(out)
	if detail == "" {
		return fmt.Errorf("%s %s: %w", name, sub, err)
	}
	return fmt.Errorf("%s %s: %w: %s", name, sub, err, detail)
}

// outputSummary 截取输出末尾最多 500 个字符，避免错误信息过长。
func outputSummary(out []byte) string {
	text := strings.TrimSpace(string(out))
	if len(text) > 500 {
		text = text[len(text)-500:]
	}
	return text
}

// lastURL 返回输出中最后出现的 URL；没有则返回空字符串。
func lastURL(out string) string {
	matches := urlPattern.FindAllString(out, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		url := strings.TrimRight(matches[i], `.,;:)]}"'`)
		if url != "" {
			return url
		}
	}
	return ""
}
