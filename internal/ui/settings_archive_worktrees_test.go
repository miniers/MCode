package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestArchiveWtItemState(t *testing.T) {
	clean := archiveWorktreeItem{Path: "/wt/clean", Clean: true, Merged: true, SizeBytes: 100}
	dirty := archiveWorktreeItem{Path: "/wt/dirty", Clean: false, SizeBytes: 200}
	orphan := archiveWorktreeItem{Path: "/wt/orphan", Orphan: true}
	inUse := archiveWorktreeItem{Path: "/wt/inuse", Clean: true, InUse: true, SizeBytes: 50}

	if archiveWtItemDirty(clean) {
		t.Fatal("干净项不应视为有未提交改动")
	}
	if !archiveWtItemDirty(dirty) {
		t.Fatal("有未提交改动的项应被识别")
	}
	if archiveWtItemDirty(orphan) {
		t.Fatal("孤立项（目录已不存在）不应视为有未提交改动")
	}

	if !archiveWtSelectable(clean) {
		t.Fatal("干净项应可勾选")
	}
	if archiveWtSelectable(inUse) {
		t.Fatal("使用中的 worktree 不可勾选")
	}
	if !archiveWtSelectable(orphan) {
		t.Fatal("孤立项可勾选")
	}
}

func TestArchiveWtSelectionSummary(t *testing.T) {
	items := []archiveWorktreeItem{
		{Path: "/wt/clean", Clean: true, SizeBytes: 100},
		{Path: "/wt/dirty", Clean: false, SizeBytes: 200},
		{Path: "/wt/inuse", Clean: true, InUse: true, SizeBytes: 50},
		{Path: "/wt/orphan", Orphan: true},
	}
	selected := map[string]bool{
		"/wt/clean":  true,
		"/wt/dirty":  true,
		"/wt/inuse":  true, // 使用中：即使被标记也应收割掉
		"/wt/orphan": false,
	}

	sel := archiveWtSelectedItems(items, selected)
	if len(sel) != 2 || sel[0].Path != "/wt/clean" || sel[1].Path != "/wt/dirty" {
		t.Fatalf("选中项错误: %+v", sel)
	}
	if got := archiveWtTotalSize(sel); got != 300 {
		t.Fatalf("总大小 = %d, want 300", got)
	}

	dirtyItems := archiveWtDirtyItems(sel)
	if len(dirtyItems) != 1 || dirtyItems[0].Path != "/wt/dirty" {
		t.Fatalf("脏改动项错误: %+v", dirtyItems)
	}
	if !archiveWtNeedsDirtyConfirm(sel) {
		t.Fatal("含有未提交改动时应需要额外确认")
	}
}

func TestArchiveWtCanConfirm(t *testing.T) {
	dirtySel := []archiveWorktreeItem{{Path: "/wt/dirty", Clean: false}}
	cleanSel := []archiveWorktreeItem{{Path: "/wt/clean", Clean: true}}
	orphanSel := []archiveWorktreeItem{{Path: "/wt/orphan", Orphan: true}}

	if archiveWtCanConfirm(dirtySel, false) {
		t.Fatal("含有未提交改动时必须先勾选确认丢弃")
	}
	if !archiveWtCanConfirm(dirtySel, true) {
		t.Fatal("已勾选确认丢弃后应可执行")
	}
	if !archiveWtCanConfirm(cleanSel, false) {
		t.Fatal("无未提交改动时无需额外确认")
	}
	if !archiveWtCanConfirm(orphanSel, false) {
		t.Fatal("孤立项无需脏改动确认")
	}
	if archiveWtCanConfirm(nil, true) {
		t.Fatal("未选择任何项时不可确认")
	}
	if archiveWtNeedsDirtyConfirm(cleanSel) || archiveWtNeedsDirtyConfirm(orphanSel) {
		t.Fatal("干净项与孤立项不应触发脏改动确认")
	}
}

func TestArchiveWorktreePathInUse(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	if archiveWorktreePathInUse(nil, a) {
		t.Fatal("空集合不应命中")
	}
	inUse := map[string]bool{a: true}
	if !archiveWorktreePathInUse(inUse, a) {
		t.Fatal("完全匹配应命中")
	}
	if archiveWorktreePathInUse(inUse, b) {
		t.Fatal("不相关路径不应命中")
	}

	// 符号链接指向使用中的目录时应命中。
	symlinkBase := t.TempDir()
	real := filepath.Join(symlinkBase, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(symlinkBase, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if !archiveWorktreePathInUse(map[string]bool{resolved: true}, link) {
		t.Fatal("符号链接应解析后命中")
	}
}

// TestScanProjectWorktreesSingleScan 验证归档页扫描只发起一次 Client.ScanWorktrees，
// 并把结果字段（含 Clean、Orphan、Active）直接映射到清理项。
func TestScanProjectWorktreesSingleScan(t *testing.T) {
	client := &FakeClient{scanWorktreesResult: []WorktreeInfo{
		{Path: "/wt/a", Branch: "feat/a", ProjectPath: "/repo/a", SizeBytes: 10, Merged: true, Clean: true, Active: true},
		{Path: "/wt/b", Branch: "feat/b", ProjectPath: "/repo/b", SizeBytes: 20, Orphan: true},
	}}

	items, err := scanProjectWorktrees(client, t.TempDir())
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if client.scanWorktreesCalls != 1 {
		t.Fatalf("ScanWorktrees 调用次数 = %d, want 1", client.scanWorktreesCalls)
	}
	if len(items) != 2 {
		t.Fatalf("扫描结果数量 = %d, want 2", len(items))
	}
	first := items[0]
	if first.Path != "/wt/a" || first.Branch != "feat/a" || first.RepoRoot != "/repo/a" ||
		first.SizeBytes != 10 || !first.Merged || !first.Clean || !first.InUse || first.Orphan {
		t.Fatalf("首项映射错误: %+v", first)
	}
	second := items[1]
	if second.RepoRoot != "/repo/b" || !second.Orphan || second.InUse || archiveWtItemDirty(second) {
		t.Fatalf("次项映射错误: %+v", second)
	}
}

func TestResetArchiveWorktreeState(t *testing.T) {
	globalArchiveWorktreeState.mu.Lock()
	globalArchiveWorktreeState.items = []archiveWorktreeItem{{Path: "/wt/a"}}
	globalArchiveWorktreeState.selected = map[string]bool{"/wt/a": true}
	globalArchiveWorktreeState.scanning = true
	globalArchiveWorktreeState.confirmOpen = true
	globalArchiveWorktreeState.confirmDirty = true
	globalArchiveWorktreeState.mu.Unlock()

	resetArchiveWorktreeStateForTest()

	globalArchiveWorktreeState.mu.Lock()
	defer globalArchiveWorktreeState.mu.Unlock()
	if globalArchiveWorktreeState.scanning || globalArchiveWorktreeState.confirmOpen ||
		globalArchiveWorktreeState.confirmDirty || len(globalArchiveWorktreeState.items) != 0 ||
		len(globalArchiveWorktreeState.selected) != 0 {
		// 只打印字段值，避免以值传递含锁的 archiveWorktreeState（go vet copylocks）。
		t.Fatalf("重置后状态未清空: scanning=%v confirmOpen=%v confirmDirty=%v items=%d selected=%d",
			globalArchiveWorktreeState.scanning, globalArchiveWorktreeState.confirmOpen,
			globalArchiveWorktreeState.confirmDirty, len(globalArchiveWorktreeState.items),
			len(globalArchiveWorktreeState.selected))
	}
}

func TestArchiveWorktreeCheckbox(t *testing.T) {
	t.Run("SingleSelectable", func(t *testing.T) {
		resetArchiveWorktreeStateForTest()
		t.Cleanup(resetArchiveWorktreeStateForTest)

		const wtPath = "/tmp/wt-alpha"
		globalArchiveWorktreeState.mu.Lock()
		globalArchiveWorktreeState.loaded = true
		globalArchiveWorktreeState.items = []archiveWorktreeItem{
			{Path: wtPath, Branch: "feat/alpha", RepoRoot: "/tmp/repo", SizeBytes: 1024, Clean: true},
		}
		globalArchiveWorktreeState.mu.Unlock()

		st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
		openSettingsSheet(st, "worktree", "")
		t.Cleanup(func() { closeSettingsSheet(st) })

		tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetWorktree) }, 1000, 800)
		waitFor(t, tt, func() bool { return tt.HasText(wtPath) })
		tt.Advance(300 * time.Millisecond)
		tt.Frame()

		// sheet 标题与内部 section 不应重复出现「Worktree 清理」。
		titleCount := 0
		for _, txt := range tt.Texts() {
			if txt == "Worktree 清理" {
				titleCount++
			}
		}
		if titleCount != 1 {
			t.Fatalf("Worktree sheet 中「Worktree 清理」标题出现 %d 次，期望 1 次: %v", titleCount, tt.Texts())
		}

		// 未勾选前「清理所选」处于禁用态，点击不会弹出确认框。
		_ = tt.Click("清理所选")
		tt.Frame()
		globalArchiveWorktreeState.mu.Lock()
		confirmBefore := globalArchiveWorktreeState.confirmOpen
		globalArchiveWorktreeState.mu.Unlock()
		if confirmBefore {
			t.Fatal("未勾选任何 worktree 时「清理所选」应保持禁用")
		}

		if err := tt.Click("wt-alpha"); err != nil {
			t.Fatalf("点击 checkbox 失败: %v", err)
		}
		tt.Frame()

		globalArchiveWorktreeState.mu.Lock()
		checked := globalArchiveWorktreeState.selected[wtPath]
		globalArchiveWorktreeState.mu.Unlock()
		if !checked {
			t.Fatal("点击 checkbox 后 globalArchiveWorktreeState.selected 应变为 true")
		}

		if err := tt.Click("清理所选"); err != nil {
			t.Fatalf("点击「清理所选」失败: %v", err)
		}
		tt.Frame()
		globalArchiveWorktreeState.mu.Lock()
		confirmAfter := globalArchiveWorktreeState.confirmOpen
		globalArchiveWorktreeState.mu.Unlock()
		if !confirmAfter || !tt.HasText("确认删除") {
			t.Fatalf("勾选后「清理所选」应变为可用并弹出确认框，texts: %v", tt.Texts())
		}
	})

	t.Run("BackendDuplicatePaths", func(t *testing.T) {
		resetArchiveWorktreeStateForTest()
		t.Cleanup(resetArchiveWorktreeStateForTest)

		const dupPath = "/tmp/wt-dup"
		fake := NewFakeClient()
		fake.scanWorktreesResult = []WorktreeInfo{
			{Path: dupPath, Branch: "feat/dup", ProjectPath: "/tmp/repo", SizeBytes: 2048, Clean: true},
			{Path: dupPath, Branch: "feat/dup", ProjectPath: "/tmp/repo", SizeBytes: 2048, Clean: true},
		}

		st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
		openSettingsSheet(st, "worktree", "")
		t.Cleanup(func() { closeSettingsSheet(st) })

		tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetWorktree) }, 1000, 800)
		waitFor(t, tt, func() bool {
			globalArchiveWorktreeState.mu.Lock()
			ready := globalArchiveWorktreeState.loaded && !globalArchiveWorktreeState.scanning
			globalArchiveWorktreeState.mu.Unlock()
			return ready && tt.HasText(dupPath)
		})
		tt.Advance(300 * time.Millisecond)
		tt.Frame()

		globalArchiveWorktreeState.mu.Lock()
		itemCount := len(globalArchiveWorktreeState.items)
		globalArchiveWorktreeState.mu.Unlock()
		if itemCount != 1 {
			t.Fatalf("后端返回重复路径时 UI 应去重为 1 项，实际 %d 项", itemCount)
		}

		if err := tt.Click("wt-dup"); err != nil {
			t.Fatalf("点击重复路径去重后的 checkbox 失败: %v", err)
		}
		tt.Frame()

		globalArchiveWorktreeState.mu.Lock()
		checked := globalArchiveWorktreeState.selected[dupPath]
		globalArchiveWorktreeState.mu.Unlock()
		if !checked {
			t.Fatal("后端返回重复路径时点击 checkbox 后 selected 应变为 true")
		}

		if err := tt.Click("清理所选"); err != nil {
			t.Fatalf("点击「清理所选」失败: %v", err)
		}
		tt.Frame()
		globalArchiveWorktreeState.mu.Lock()
		confirmOpen := globalArchiveWorktreeState.confirmOpen
		globalArchiveWorktreeState.mu.Unlock()
		if !confirmOpen || !tt.HasText("确认删除") {
			t.Fatalf("勾选后「清理所选」应变为可用并弹出确认框，texts: %v", tt.Texts())
		}
	})
}
