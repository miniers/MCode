package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestGitPRStartReview(t *testing.T) {
	ctx := context.Background()

	t.Run("正常创建审查会话并发送指令", func(t *testing.T) {
		c, mainRoot, threadID := branchClient(t)
		c.dataDir = t.TempDir()

		bare := filepath.Join(t.TempDir(), "origin.git")
		branchGit(t, mainRoot, "init", "--bare", bare)
		branchGit(t, mainRoot, "remote", "add", "origin", bare)
		base := branchGit(t, mainRoot, "branch", "--show-current")
		branchGit(t, mainRoot, "push", "origin", "HEAD:refs/heads/"+base, "HEAD:refs/heads/feat-review")

		origDetail := gitPRDetailFunc
		origCheckout := gitPRCheckoutWorktreeFunc
		origSend := gitPRSendFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRCheckoutWorktreeFunc = origCheckout
			gitPRSendFunc = origSend
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, gotThreadID string, number int) (git.PRDetail, error) {
			if gotThreadID != threadID || number != 12 {
				t.Fatalf("detail 参数不符: thread=%q number=%d", gotThreadID, number)
			}
			return git.PRDetail{
				PRSummary: git.PRSummary{
					Number:     number,
					Title:      "修复审查流程",
					HeadBranch: "feat-review",
					BaseBranch: base,
				},
			}, nil
		}

		var gotPrompt, gotSentID string
		gitPRSendFunc = func(_ *Client, _ context.Context, id, text string) (bool, error) {
			gotSentID = id
			gotPrompt = text
			return false, nil
		}

		newID, err := c.GitPRStartReview(ctx, threadID, 12)
		if err != nil {
			t.Fatalf("GitPRStartReview 失败: %v", err)
		}
		if newID == "" || newID == threadID {
			t.Fatalf("新会话 ID 异常: %q", newID)
		}
		if gotSentID != newID {
			t.Fatalf("Send 目标会话 = %q，期望 %q", gotSentID, newID)
		}
		wantPrompt := fmt.Sprintf(mcodeui.GitPRAIReviewPrompt, 12, "修复审查流程", base, base, base)
		if gotPrompt != wantPrompt {
			t.Fatalf("发送内容不符:\ngot:  %s\nwant: %s", gotPrompt, wantPrompt)
		}

		summary, err := c.backend.GetThread(ctx, newID)
		if err != nil {
			t.Fatalf("GetThread 失败: %v", err)
		}
		if summary.Title != "审查 PR #12" || summary.ProjectID != "p1" || summary.IsProvisional {
			t.Fatalf("新会话属性不符: %+v", summary)
		}
		wantWT := prWorktreePath(c.dataDir, mainRoot, 12)
		if workDir, ok := c.ThreadWorkDir(newID); !ok || workDir != wantWT {
			t.Fatalf("新会话工作目录 = (%q, %v)，期望 %q", workDir, ok, wantWT)
		}
	})

	t.Run("detail 失败时不创建会话", func(t *testing.T) {
		c, _, threadID := branchClient(t)

		origDetail := gitPRDetailFunc
		origCheckout := gitPRCheckoutWorktreeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRCheckoutWorktreeFunc = origCheckout
		})

		detailErr := errors.New("gh view failed")
		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, _ int) (git.PRDetail, error) {
			return git.PRDetail{}, detailErr
		}
		checkoutCalled := false
		gitPRCheckoutWorktreeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string, _ bool) (string, error) {
			checkoutCalled = true
			return "", nil
		}

		newID, err := c.GitPRStartReview(ctx, threadID, 7)
		if !errors.Is(err, detailErr) || newID != "" {
			t.Fatalf("结果不符: id=%q err=%v", newID, err)
		}
		if checkoutCalled {
			t.Fatal("detail 失败后不应调用 checkout")
		}
		list, err := c.backend.ListThreads(ctx, wire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Threads) != 1 || list.Threads[0].ID != threadID {
			t.Fatalf("不应创建新会话，当前会话列表: %+v", list.Threads)
		}
	})

	t.Run("checkout 失败时不创建会话", func(t *testing.T) {
		c, _, threadID := branchClient(t)

		origDetail := gitPRDetailFunc
		origCheckout := gitPRCheckoutWorktreeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRCheckoutWorktreeFunc = origCheckout
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, number int) (git.PRDetail, error) {
			return git.PRDetail{PRSummary: git.PRSummary{Number: number, HeadBranch: "feat", BaseBranch: "main"}}, nil
		}
		checkoutErr := errors.New("checkout failed")
		gitPRCheckoutWorktreeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string, _ bool) (string, error) {
			return "", checkoutErr
		}

		newID, err := c.GitPRStartReview(ctx, threadID, 8)
		if !errors.Is(err, checkoutErr) || newID != "" {
			t.Fatalf("结果不符: id=%q err=%v", newID, err)
		}
		list, err := c.backend.ListThreads(ctx, wire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Threads) != 1 || list.Threads[0].ID != threadID {
			t.Fatalf("不应创建新会话，当前会话列表: %+v", list.Threads)
		}
	})

	t.Run("切 worktree 失败时删除新建会话", func(t *testing.T) {
		c, _, threadID := branchClient(t)

		origDetail := gitPRDetailFunc
		origCheckout := gitPRCheckoutWorktreeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRCheckoutWorktreeFunc = origCheckout
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, number int) (git.PRDetail, error) {
			return git.PRDetail{PRSummary: git.PRSummary{Number: number, HeadBranch: "feat", BaseBranch: "main"}}, nil
		}
		gitPRCheckoutWorktreeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string, _ bool) (string, error) {
			return filepath.Join(t.TempDir(), "missing-worktree"), nil
		}

		newID, err := c.GitPRStartReview(ctx, threadID, 9)
		if err == nil || newID != "" {
			t.Fatalf("预期切 worktree 失败并清理会话，got id=%q err=%v", newID, err)
		}
		list, err := c.backend.ListThreads(ctx, wire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Threads) != 1 || list.Threads[0].ID != threadID {
			t.Fatalf("失败后未删除新建会话，当前列表: %+v", list.Threads)
		}
	})

	t.Run("Send 失败时保留会话并返回 ID 与错误", func(t *testing.T) {
		c, mainRoot, threadID := branchClient(t)

		origDetail := gitPRDetailFunc
		origCheckout := gitPRCheckoutWorktreeFunc
		origSend := gitPRSendFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRCheckoutWorktreeFunc = origCheckout
			gitPRSendFunc = origSend
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, number int) (git.PRDetail, error) {
			return git.PRDetail{PRSummary: git.PRSummary{Number: number, Title: "PR 标题", HeadBranch: "feat", BaseBranch: "main"}}, nil
		}
		gitPRCheckoutWorktreeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string, _ bool) (string, error) {
			return mainRoot, nil
		}
		sendErr := errors.New("provider offline")
		gitPRSendFunc = func(_ *Client, _ context.Context, _ string, _ string) (bool, error) {
			return false, sendErr
		}

		newID, err := c.GitPRStartReview(ctx, threadID, 10)
		if newID == "" || !errors.Is(err, sendErr) || !strings.Contains(err.Error(), "首条消息发送失败") {
			t.Fatalf("预期保留会话 ID 并返回发送错误，got id=%q err=%v", newID, err)
		}
		summary, err := c.backend.GetThread(ctx, newID)
		if err != nil {
			t.Fatalf("Send 失败后会话应保留: %v", err)
		}
		if summary.Title != "审查 PR #10" {
			t.Fatalf("会话标题 = %q，期望 审查 PR #10", summary.Title)
		}
	})
}
