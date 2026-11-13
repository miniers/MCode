package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote/wire"
)

func TestWorktreeMoveLocations(t *testing.T) {
	orig := generateWorktreeMoveBranch
	generateWorktreeMoveBranch = func(context.Context, string, string) string { return "" }
	t.Cleanup(func() { generateWorktreeMoveBranch = orig })
	for _, location := range []string{config.WorktreeLocationCentral, config.WorktreeLocationProject} {
		for _, transfer := range []bool{false, true} {
			name := location + "/keep"
			if transfer {
				name = location + "/transfer"
			}
			t.Run(name, func(t *testing.T) {
				b, threadID, repo, runGit := setupGitOpsBackend(t)
				data := t.TempDir()
				b.SetDataDir(data)
				cfg := config.Defaults()
				cfg.SourceControl.WorktreeLocation = location
				if err := config.Save(data, cfg); err != nil {
					t.Fatal(err)
				}
				root := runGit("rev-parse", "--show-toplevel")
				base := filepath.Join(data, "worktrees")
				if location == config.WorktreeLocationProject {
					base = filepath.Join(root, ".mcode", "worktrees")
				}
				// 项目路径指向子目录时，仍在仓库根下创建。
				subdir := filepath.Join(repo, "src")
				if err := os.MkdirAll(subdir, 0o755); err != nil {
					t.Fatal(err)
				}
				want := filepath.Join(base, "git-ops-project-"+worktreeMoveShortID(threadID))
				if got, err := b.worktreeMovePath(context.Background(), "git-ops-project", subdir, threadID); err != nil || got != want {
					t.Fatalf("迁移路径 %q，期望 %q，错误 %v", got, want, err)
				}
				if err := os.WriteFile(filepath.Join(repo, "change.txt"), []byte("change"), 0o644); err != nil {
					t.Fatal(err)
				}
				preview, err := b.WorktreeMovePreview(context.Background(), threadID)
				if err != nil || !preview.Eligible || preview.WorktreePath != want {
					t.Fatalf("预览异常：%+v，错误 %v", preview, err)
				}
				result, err := b.WorktreeMoveExecute(context.Background(), wire.ThreadsWorktreeExecuteParams{
					ThreadID: threadID, Branch: "move-location", TransferFiles: transfer,
				})
				if err != nil || result.WorktreePath != want {
					t.Fatalf("迁移异常：%+v，错误 %v", result, err)
				}
				if wd, ok := b.ThreadWorkDir(threadID); !ok || wd != want {
					t.Fatalf("会话未切换目录：%q", wd)
				}
				if transfer {
					if _, err := os.Stat(filepath.Join(want, "change.txt")); err != nil {
						t.Fatal(err)
					}
					if status := runGit("status", "--porcelain"); status != "" {
						t.Fatalf("转移后主仓库应保持干净：%s", status)
					}
				}
				if location == config.WorktreeLocationProject {
					exclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
					if err != nil || !strings.Contains(string(exclude), "/.mcode/worktrees/\n") {
						t.Fatalf("项目内 worktree 未被排除：%s，错误 %v", exclude, err)
					}
				}
				// 切换偏好不会改动已持久化的会话绝对路径。
				cfg.SourceControl.WorktreeLocation = config.WorktreeLocationCentral
				if err := config.Save(data, cfg); err != nil {
					t.Fatal(err)
				}
				if wd, _ := b.ThreadWorkDir(threadID); wd != want {
					t.Fatalf("已有会话路径不应变化：%q", wd)
				}
			})
		}
	}
}

// TestWorktreeMoveSuggestedBranch_ModelGeneratedSlug 验证模型风格输出（原文多词文本）
// 会被归一化为本地 slug。
func TestWorktreeMoveSuggestedBranch_ModelGeneratedSlug(t *testing.T) {
	orig := generateWorktreeMoveBranch
	defer func() { generateWorktreeMoveBranch = orig }()

	cases := []struct {
		name string
		out  string
		want string
	}{
		{"原始模型输出", "Fix Login Bug", "fix-login-bug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generateWorktreeMoveBranch = func(ctx context.Context, provider, title string) string { return tc.out }
			got := worktreeMoveSuggestedBranch(context.Background(), "memory", "修复登录问题", "thread-abcdef12")
			if got != tc.want {
				t.Fatalf("worktreeMoveSuggestedBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorktreeMoveSuggestedBranch_FallbackToLocal 验证生成器返回空时回退本地 worktreeMoveBranchName。
func TestWorktreeMoveSuggestedBranch_FallbackToLocal(t *testing.T) {
	orig := generateWorktreeMoveBranch
	generateWorktreeMoveBranch = func(ctx context.Context, provider, title string) string { return "" }
	defer func() { generateWorktreeMoveBranch = orig }()

	const threadID = "thread-abcdef12"
	title := "Fix Login Bug"
	got := worktreeMoveSuggestedBranch(context.Background(), "memory", title, threadID)
	want := worktreeMoveBranchName(title, threadID)
	if got != want {
		t.Fatalf("worktreeMoveSuggestedBranch = %q, want %q", got, want)
	}
}

// TestWorktreeMoveModelSlug 验证模型输出归一化：去前缀、转 slug，textgen 兜底值 "task"
// 返回空串以便回退本地分支名。
func TestWorktreeMoveModelSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"原始多词文本", "Fix Login Bug", "fix-login-bug"},
		{"裸 task 兜底", "task", ""},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := worktreeMoveModelSlug(tc.in); got != tc.want {
				t.Fatalf("worktreeMoveModelSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestWorktreeMoveSuggestedBranch_TaskFallback 验证生成器返回 textgen 兜底分支名
// task 时回退本地 worktreeMoveBranchName（中文标题 → mcode/<短ID>）。
func TestWorktreeMoveSuggestedBranch_TaskFallback(t *testing.T) {
	orig := generateWorktreeMoveBranch
	generateWorktreeMoveBranch = func(ctx context.Context, provider, title string) string { return "task" }
	defer func() { generateWorktreeMoveBranch = orig }()

	title := "修复登录"
	const threadID = "abcdef123456"
	got := worktreeMoveSuggestedBranch(context.Background(), "claude", title, threadID)
	want := worktreeMoveBranchName(title, threadID)
	if got != want {
		t.Fatalf("worktreeMoveSuggestedBranch = %q, want %q", got, want)
	}
}
