package remote

import (
	"context"
	"testing"
)

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
