package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
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
	if archiveWorktreePathInUse(nil, "/wt/a") {
		t.Fatal("空集合不应命中")
	}
	inUse := map[string]bool{"/wt/a": true}
	if !archiveWorktreePathInUse(inUse, "/wt/a") {
		t.Fatal("完全匹配应命中")
	}
	if archiveWorktreePathInUse(inUse, "/wt/b") {
		t.Fatal("不相关路径不应命中")
	}

	// 符号链接指向使用中的目录时应命中。
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
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
