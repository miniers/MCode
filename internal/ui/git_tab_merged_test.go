package mcodeui

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitTabMergedFakeClient struct {
	*gitHistoryFakeClient
}

func (f *gitTabMergedFakeClient) GitStageFiles(context.Context, string, []string) error { return nil }
func (f *gitTabMergedFakeClient) GitUnstageFiles(context.Context, string, []string) error {
	return nil
}
func (f *gitTabMergedFakeClient) GitDiscardFiles(context.Context, string, []string) (*git.Snapshot, error) {
	return nil, nil
}
func (f *gitTabMergedFakeClient) GitCommitIndex(context.Context, string, string, bool) (string, error) {
	return "abc1234", nil
}
func (f *gitTabMergedFakeClient) GitLastCommitMessage(context.Context, string) (string, error) {
	return "last commit", nil
}
func (f *gitTabMergedFakeClient) GitHeadPushed(context.Context, string) (bool, error) {
	return false, nil
}

func TestGitTabMerged_RegistryHasThreeTabs(t *testing.T) {
	wantIDs := []string{sidePaneFileID, sidePaneGitID, sidePanePreviewID}
	wantTitles := []string{"文件", "Git", "浏览器"}
	if len(sidePanes) != len(wantIDs) {
		t.Fatalf("右栏应只有 %d 个 Tab，实际 %d 个", len(wantIDs), len(sidePanes))
	}
	for i := range sidePanes {
		if sidePanes[i].id != wantIDs[i] || sidePanes[i].title != wantTitles[i] {
			t.Fatalf("第 %d 个 Tab = (%q, %q)，期望 (%q, %q)", i, sidePanes[i].id, sidePanes[i].title, wantIDs[i], wantTitles[i])
		}
	}
}

func TestGitTabMerged_ShortcutsToggleAndScope(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })

	s, _, base := gitPaneTestState(t)
	base.SetReviewSnapshot("git-test", "turn", "", "", ReviewSnapshot{Scope: "turn", HeadRevision: "h1", Files: []ReviewFile{{Path: "turn.go", Status: "M"}}})
	base.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "h2", Files: []ReviewFile{{Path: "all.go", Status: "M"}}})

	// ⌘D：打开 Git Tab 并切到 turn
	id, _, ok := matchShortcut(ui.Cmd, ui.KeyD)
	if !ok || id != "toggle-diff" {
		t.Fatalf("⌘D 命中 = %q (%v)，期望 toggle-diff", id, ok)
	}
	s.toggleDiffPanel()
	dp := getDiffPanelState(s)
	if !inspectorPaneOpen(s, sidePaneGitID) || gitPaneCurrentScope(dp) != "turn" {
		t.Fatalf("⌘D 后状态错误：open=%v scope=%q", inspectorPaneOpen(s, sidePaneGitID), gitPaneCurrentScope(dp))
	}

	// 已在 Git Tab 且范围为 turn 时再按 ⌘D 关闭右栏
	s.toggleDiffPanel()
	if inspectorPaneOpen(s, sidePaneGitID) {
		t.Fatal("已在 Git Tab 且范围为 turn 时再次 ⌘D 应关闭右栏")
	}

	// ⇧⌘G：打开 Git Tab 并切到 all
	id, _, ok = matchShortcut(ui.Cmd|ui.Shift, ui.KeyG)
	if !ok || id != "toggle-git-pane" {
		t.Fatalf("⇧⌘G 命中 = %q (%v)，期望 toggle-git-pane", id, ok)
	}
	toggleGitPane(s)
	if !inspectorPaneOpen(s, sidePaneGitID) || gitPaneCurrentScope(dp) != "all" {
		t.Fatalf("⇧⌘G 后状态错误：open=%v scope=%q", inspectorPaneOpen(s, sidePaneGitID), gitPaneCurrentScope(dp))
	}

	// 已在 Git Tab 且范围为 all 时按 ⌘D：不关右栏，切换到 turn
	s.toggleDiffPanel()
	if !inspectorPaneOpen(s, sidePaneGitID) || gitPaneCurrentScope(dp) != "turn" {
		t.Fatalf("all 状态下按 ⌘D 应保持打开并切到 turn：open=%v scope=%q", inspectorPaneOpen(s, sidePaneGitID), gitPaneCurrentScope(dp))
	}

	// 已在 Git Tab 且范围为 turn 时按 ⇧⌘G：不关右栏，切换到 all
	toggleGitPane(s)
	if !inspectorPaneOpen(s, sidePaneGitID) || gitPaneCurrentScope(dp) != "all" {
		t.Fatalf("turn 状态下按 ⇧⌘G 应保持打开并切到 all：open=%v scope=%q", inspectorPaneOpen(s, sidePaneGitID), gitPaneCurrentScope(dp))
	}

	// 已在 Git Tab 且范围为 all 时再按 ⇧⌘G 关闭右栏
	toggleGitPane(s)
	if inspectorPaneOpen(s, sidePaneGitID) {
		t.Fatal("已在 Git Tab 且范围为 all 时再次 ⇧⌘G 应关闭右栏")
	}

	// cmd-toggle-diff 命令面板条目指向新行为
	items := buildPaletteItems(s)
	cmdDiff := paletteItemByID(items, "cmd-toggle-diff")
	if cmdDiff == nil || cmdDiff.run == nil {
		t.Fatal("命令面板缺少 cmd-toggle-diff")
	}
	cmdDiff.run(s)
	if !inspectorPaneOpen(s, sidePaneGitID) || gitPaneCurrentScope(dp) != "turn" {
		t.Fatalf("执行 cmd-toggle-diff 后状态错误：open=%v scope=%q", inspectorPaneOpen(s, sidePaneGitID), gitPaneCurrentScope(dp))
	}
}

func TestGitTabMerged_AllAndTurnScopesRender(t *testing.T) {
	s, ps, h, histClient := gitHistoryTestState(t)
	client := &gitTabMergedFakeClient{gitHistoryFakeClient: histClient}
	s.client = client

	root := t.TempDir()
	histClient.state = GitState{WorkDir: root, Branch: "main", Head: "abc1234"}
	histClient.log = []git.LogCommit{{SHA: "abc1234567890", Subject: "初始提交", Author: "Bram", AuthorTime: time.Now()}}
	histClient.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "working-head",
		Files:        []ReviewFile{{Path: "dirty.go", Status: "M", Additions: 2}},
	})
	histClient.SetReviewSnapshot("git-test", "turn", "", "", ReviewSnapshot{
		Scope:        "turn",
		TurnID:       "turn-1",
		HeadRevision: "turn-head",
		Files:        []ReviewFile{{Path: "turn.go", Status: "M", Additions: 1}},
	})

	ps.mu.Lock()
	ps.state = histClient.state
	ps.visible = true
	ps.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 480, 720)
	// 工作区有改动（all 范围）：上部渲染改动卡片与提交框，下部渲染历史区。
	histClient.entries = []git.StatusEntry{{Path: "dirty.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.entries = slices.Clone(histClient.entries)
	ps.mu.Unlock()
	dp := getDiffPanelState(s)
	s.selectReviewScope(dp, "all")
	waitFor(t, tt, func() bool {
		return tt.HasText("dirty.go") && tt.HasText("提交信息") && tt.HasText("初始提交")
	})
	if _, ok := tt.Find("git-history"); ok {
		// git-history 通过无障碍/key 构建；进一步确认历史状态已加载
	}
	h.mu.Lock()
	loadedCommits := len(h.commits)
	h.mu.Unlock()
	if loadedCommits != 1 {
		t.Fatalf("all 范围应加载历史提交，got %d", loadedCommits)
	}

	// 切换到 turn 范围：渲染本轮改动正文，不渲染历史区和提交框。
	s.selectReviewScope(dp, "turn")
	waitFor(t, tt, func() bool { return tt.HasText("turn.go") })
	if tt.HasText("初始提交") || tt.HasText("提交信息") {
		t.Fatalf("turn 范围不应显示历史区或提交框，texts=%v", tt.Texts())
	}

	// 工作区干净切回 all 范围：只显示历史，不显示提交框和「暂无改动」。
	histClient.entries = nil
	histClient.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "clean"})
	ps.mu.Lock()
	ps.entries = nil
	ps.mu.Unlock()
	s.selectReviewScope(dp, "all")
	waitFor(t, tt, func() bool { return tt.HasText("初始提交") && !tt.HasText("turn.go") })
	if tt.HasText("提交信息") || tt.HasText(diffEmptyTitle) {
		t.Fatalf("工作区干净时应只显示历史，texts=%v", tt.Texts())
	}

	// 非 Git 仓库：all 范围显示「不是 Git 仓库」，turn 范围仍可用。
	histClient.state = GitState{WorkDir: ""}
	ps.mu.Lock()
	ps.state = GitState{}
	ps.mu.Unlock()
	tt.Frame()
	if !tt.HasText("不是 Git 仓库") {
		t.Fatalf("非仓库 all 范围缺少空态，texts=%v", tt.Texts())
	}
	s.selectReviewScope(dp, "turn")
	waitFor(t, tt, func() bool { return tt.HasText("turn.go") })
}
