package projctx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/git"
)

const (
	workspaceSnapshotMaxBytes    = 1500
	workspaceSnapshotMaxTopLevel = 40
	workspaceSnapshotMaxGitFiles = 10
)

// snapshotGitRunner 用于工作区快照采集的只读 Git 执行器，不注入影子仓库。
var snapshotGitRunner = git.NewRunner("", 2)

// WorkspaceSnapshot 采集工作区根目录的技术栈、Git 状态与顶层条目概况，
// 包装为 <workspace_snapshot> 块（硬上限 1500 字节），供首轮注入模型上下文。
// dir 为空、不存在或不可读时返回空串；ctx 超时或取消时返回已采集部分。
func WorkspaceSnapshot(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lines := []string{"root: " + dir}
	if stack := detectWorkspaceStack(entries); stack != "" {
		lines = append(lines, "stack: "+stack)
	}
	if ctx.Err() == nil {
		if gitLines := collectWorkspaceGit(ctx, dir); len(gitLines) > 0 {
			lines = append(lines, gitLines...)
		}
	}
	if top := formatWorkspaceTopLevel(entries); top != "" {
		lines = append(lines, "top-level: "+top)
	}
	return formatWorkspaceSnapshot(lines, workspaceSnapshotMaxBytes)
}

func detectWorkspaceStack(entries []os.DirEntry) string {
	files := make(map[string]bool, len(entries))
	hasDotNet := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		files[name] = true
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".csproj" || ext == ".sln" {
			hasDotNet = true
		}
	}

	var stacks []string
	if files["go.mod"] {
		stacks = append(stacks, "Go")
	}
	if files["package.json"] {
		switch {
		case files["pnpm-lock.yaml"]:
			stacks = append(stacks, "Node (pnpm)")
		case files["yarn.lock"]:
			stacks = append(stacks, "Node (yarn)")
		case files["bun.lockb"]:
			stacks = append(stacks, "Node (bun)")
		default:
			stacks = append(stacks, "Node")
		}
	}
	if files["Cargo.toml"] {
		stacks = append(stacks, "Rust")
	}
	if files["pyproject.toml"] || files["requirements.txt"] {
		stacks = append(stacks, "Python")
	}
	if files["pom.xml"] || files["build.gradle"] || files["build.gradle.kts"] {
		stacks = append(stacks, "JVM")
	}
	if files["Gemfile"] {
		stacks = append(stacks, "Ruby")
	}
	if files["Package.swift"] {
		stacks = append(stacks, "Swift")
	}
	if hasDotNet {
		stacks = append(stacks, ".NET")
	}
	return strings.Join(stacks, ", ")
}

func collectWorkspaceGit(ctx context.Context, dir string) []string {
	root, err := snapshotGitRunner.RepoRoot(ctx, dir)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	branch, err := snapshotGitRunner.CurrentBranch(ctx, root)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	if branch == "" {
		branch = "(detached)"
	}
	status, err := snapshotGitRunner.Status(ctx, root)
	if err != nil || ctx.Err() != nil {
		return nil
	}

	limit := len(status)
	if limit > workspaceSnapshotMaxGitFiles {
		limit = workspaceSnapshotMaxGitFiles
	}
	lines := make([]string, 0, 1+limit)
	lines = append(lines, fmt.Sprintf("git: branch %s, %d changed", branch, len(status)))
	for _, entry := range status[:limit] {
		lines = append(lines, fmt.Sprintf("  %c%c %s", entry.Index, entry.Worktree, entry.Path))
	}
	return lines
}

func formatWorkspaceTopLevel(entries []os.DirEntry) string {
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		if entry.IsDir() {
			name += "/"
		}
		items = append(items, name)
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) <= workspaceSnapshotMaxTopLevel {
		return strings.Join(items, ", ")
	}
	extra := len(items) - workspaceSnapshotMaxTopLevel
	return fmt.Sprintf("%s, … (+%d)", strings.Join(items[:workspaceSnapshotMaxTopLevel], ", "), extra)
}

func formatWorkspaceSnapshot(lines []string, maxBytes int) string {
	const (
		openTag  = "<workspace_snapshot>\n"
		closeTag = "\n</workspace_snapshot>"
	)
	body := strings.Join(lines, "\n")
	if len(openTag)+len(body)+len(closeTag) <= maxBytes {
		return openTag + body + closeTag
	}
	for len(lines) > 1 {
		lines = lines[:len(lines)-1]
		body = strings.Join(lines, "\n")
		if len(openTag)+len(body)+len(closeTag) <= maxBytes {
			return openTag + body + closeTag
		}
	}
	budget := maxBytes - len(openTag) - len(closeTag)
	return openTag + truncateUTF8Bytes(lines[0], budget) + closeTag
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		cut = i
	}
	return s[:cut]
}
