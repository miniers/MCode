package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/remote/wire"
)

// 远程克隆项目的宿主实现：与桌面端 internal/ui/github_clone.go 的 runGitClone 一致，
// 用 os/exec 调 git（不引入 cgo），克隆成功后走 AddProjectPath 登记项目。
//
// 网络操作不持 b.mu：克隆可能跑满 10 分钟，持锁会卡住所有其它请求。

const (
	// projectCloneTimeout 是单次 git clone 的硬超时，与桌面端一致。
	projectCloneTimeout = 10 * time.Minute
)

var (
	// errProjectCloneTargetExists 表示克隆目标目录已被占用。
	errProjectCloneTargetExists = errors.New("目标目录已存在")
	// errProjectCloneGitMissing 表示宿主上找不到 git 命令。
	errProjectCloneGitMissing = errors.New("未找到 git 命令")
)

// projectCloneRunner 执行一次 git clone；测试注入假实现，避免真的走网络。
type projectCloneRunner func(ctx context.Context, url, dir string) error

// projectExtCloner 是 projects.clone 需要的后端能力，由 *MemoryBackend 实现。
type projectExtCloner interface {
	CloneProject(ctx context.Context, url, parentDir, name string) (wire.ProjectSummary, error)
}

// SetProjectCloner 替换克隆执行器，传 nil 恢复默认的 git 子进程实现。
// 只给测试与宿主定制用。
func (b *MemoryBackend) SetProjectCloner(fn projectCloneRunner) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projectCloner = fn
}

// cloneRunner 返回当前生效的克隆执行器。
func (b *MemoryBackend) cloneRunner() projectCloneRunner {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.projectCloner == nil {
		return runProjectGitClone
	}
	return b.projectCloner
}

// CloneProject 在宿主上同步执行 git clone，并把结果目录登记为项目。
// parentDir 必须是已存在的绝对路径；目标目录已存在时返回 errProjectCloneTargetExists。
// 克隆失败会删掉半成品目录；克隆成功但登记项目失败时保留目录（用户数据不丢）。
func (b *MemoryBackend) CloneProject(ctx context.Context, url, parentDir, name string) (wire.ProjectSummary, error) {
	parentDir = filepath.Clean(parentDir)
	target := filepath.Join(parentDir, name)
	if _, err := os.Stat(target); err == nil {
		return wire.ProjectSummary{}, errProjectCloneTargetExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return wire.ProjectSummary{}, fmt.Errorf("无法访问目标目录: %w", err)
	}

	cloner := b.cloneRunner()
	// 网络操作放在锁外执行。
	if err := cloner(ctx, url, target); err != nil {
		// git clone 被中断时可能留下半个目录，清理掉避免下次「目录已存在」。
		_ = os.RemoveAll(target)
		return wire.ProjectSummary{}, err
	}

	proj, err := b.AddProjectPath(ctx, target)
	if err != nil {
		return wire.ProjectSummary{}, fmt.Errorf("克隆成功但添加项目失败: %w", err)
	}
	return proj, nil
}

// runProjectGitClone 执行 git clone <url> <dir>（os/exec，不引入 cgo）。
func runProjectGitClone(ctx context.Context, url, dir string) error {
	// 服务进程的 PATH 不一定带登录 Shell 的目录，用登录环境查找 git。
	gitPath, err := loginenv.LookPath("git")
	if err != nil {
		return errProjectCloneGitMissing
	}
	ctx, cancel := context.WithTimeout(ctx, projectCloneTimeout)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, gitPath, "clone", "--", url, dir))
	// git 依赖登录环境里的 GIT_* 变量与 credentials 配置；
	// GIT_TERMINAL_PROMPT=0 让无凭据的私有仓库直接报错，而不是卡在交互式输入上。
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
				msg = msg[:idx]
			}
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// projectExtNormalizeCloneURL 校验远程传入的克隆地址。
// 只接受 https:// 与 git@ 两种形式；成功时返回 (原地址, "")，失败返回中文原因。
// 不做 host 白名单：与桌面端一致，允许企业自建 Git 服务。
func projectExtNormalizeCloneURL(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "克隆地址不能为空"
	}
	if len(trimmed) > projectExtMaxPathLength {
		return "", "克隆地址过长"
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", "克隆地址包含非法字符"
	}
	for _, r := range trimmed {
		if r <= ' ' || r == 0x7f {
			return "", "克隆地址不能包含空白或控制字符"
		}
	}
	switch {
	case strings.HasPrefix(trimmed, "https://"):
		if len(trimmed) == len("https://") {
			return "", "克隆地址缺少主机与仓库路径"
		}
	case strings.HasPrefix(trimmed, "git@"):
		// git@host:owner/repo.git 必须用冒号分隔主机与路径。
		if !strings.Contains(strings.TrimPrefix(trimmed, "git@"), ":") {
			return "", "git@ 地址必须写成 git@host:owner/repo.git"
		}
	default:
		return "", "只支持 https:// 或 git@ 形式的 Git 地址"
	}
	return trimmed, ""
}

// projectExtCloneDirName 从 Git 地址解析仓库目录名（去掉末尾 .git 与斜杠）。
func projectExtCloneDirName(url string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(url), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return trimmed
}

// projectExtValidateCloneName 校验用作目录名的仓库名；返回空串表示合法。
func projectExtValidateCloneName(name string) string {
	if name == "" {
		return "无法从地址解析仓库名，请用 name 指定"
	}
	if len(name) > 255 {
		return "仓库名过长"
	}
	if name == "." || name == ".." {
		return "仓库名不合法"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '+':
		default:
			return "仓库名只能包含字母、数字、点、下划线、连字符与加号"
		}
	}
	return ""
}
