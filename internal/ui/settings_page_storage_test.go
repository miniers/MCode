package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func openStorageTestPage(t *testing.T, fake *FakeClient) (*SettingsState, *ui.Tester) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()
	st.selectSettingsPage("storage")

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsDetail(c, st)
	}, 1000, 800)
	tt.Frame()
	return st, tt
}

func TestSettingsStoragePage_GroupsAndDefaultAllSelected(t *testing.T) {
	fake := NewFakeClient()
	fake.SetStorageReport(StorageReport{
		TotalBytes: 10240,
		Items: []StorageItem{
			{ID: "media/th-1", Category: StorageCategoryDeletedSessions, Bytes: 1024},
			{ID: "swarm-patches/p1.patch", Category: StorageCategoryOrphanPatches, Bytes: 2048},
			{ID: "swarm-worktree-backup-1", Category: StorageCategoryOldBackups, Bytes: 4096},
			{ID: "logs/mcode.log.1", Category: StorageCategoryOldLogs, Bytes: 512},
		},
	}, nil)

	_, tt := openStorageTestPage(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("已删会话残留")
	})

	for _, want := range []string{
		"已删会话残留",
		"无主补丁",
		"旧备份",
		"旧日志",
		"7.5 KB",
		"总占用 10 KB",
		"移到废纸篓（7.5 KB）",
		"Worktree 清理",
	} {
		if !tt.HasText(want) {
			t.Fatalf("页面缺少 %q，实际文本: %v", want, tt.Texts())
		}
	}
}

func TestSettingsStoragePage_UncheckUpdatesReleasableSize(t *testing.T) {
	fake := NewFakeClient()
	fake.SetStorageReport(StorageReport{
		TotalBytes: 10240,
		Items: []StorageItem{
			{ID: "media/th-1", Category: StorageCategoryDeletedSessions, Bytes: 1024},
			{ID: "swarm-patches/p1.patch", Category: StorageCategoryOrphanPatches, Bytes: 2048},
			{ID: "swarm-worktree-backup-1", Category: StorageCategoryOldBackups, Bytes: 4096},
		},
	}, nil)

	_, tt := openStorageTestPage(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("7 KB")
	})

	if err := tt.Click("选择 旧备份"); err != nil {
		t.Fatalf("取消勾选旧备份失败: %v", err)
	}
	tt.Frame()

	if !tt.HasText("3 KB") || !tt.HasText("移到废纸篓（3 KB）") {
		t.Fatalf("取消勾选后可释放大小未更新为 3 KB，实际文本: %v", tt.Texts())
	}

	// 取消全部勾选后主按钮仍存在但已不可清理（0 B）。
	if err := tt.Click("选择 已删会话残留"); err != nil {
		t.Fatalf("取消勾选已删会话残留失败: %v", err)
	}
	if err := tt.Click("选择 无主补丁"); err != nil {
		t.Fatalf("取消勾选无主补丁失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("0 B") {
		t.Fatalf("全不选后可释放大小应为 0 B，实际文本: %v", tt.Texts())
	}
}

func TestSettingsStoragePage_EmptyState(t *testing.T) {
	fake := NewFakeClient()
	fake.SetStorageReport(StorageReport{TotalBytes: 4096, Items: nil}, nil)

	_, tt := openStorageTestPage(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("没有可清理的内容")
	})

	for _, forbidden := range []string{"已删会话残留", "无主补丁", "旧备份", "旧日志", "移到废纸篓"} {
		if tt.HasText(forbidden) {
			t.Fatalf("空态不应显示 %q，实际文本: %v", forbidden, tt.Texts())
		}
	}
}

func TestSettingsStoragePage_NoNextFrameAfterClockAdvances(t *testing.T) {
	fake := NewFakeClient()
	fake.SetStorageReport(StorageReport{
		TotalBytes: 8192,
		Items: []StorageItem{
			{ID: "media/th-1", Category: StorageCategoryDeletedSessions, Bytes: 1024},
			{ID: "logs/mcode.log.1", Category: StorageCategoryOldLogs, Bytes: 512},
		},
	}, nil)

	_, tt := openStorageTestPage(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("已删会话残留")
	})

	// 扫描完成静止后推进时钟，不应再请求下一帧。
	for i := 0; i < 30; i++ {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("扫描完成静止后不应继续请求下一帧")
	}

	// 展开路径列表触发折叠动画，推进时钟至动画结束后，不应再请求下一帧。
	if err := tt.Click("展开 已删会话残留"); err != nil {
		t.Fatalf("展开已删会话残留失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("media/th-1") {
		t.Fatalf("展开后应显示具体路径 media/th-1，实际文本: %v", tt.Texts())
	}
	for i := 0; i < 40; i++ {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("展开动画结束后不应继续请求下一帧")
	}

	// 收起后再推进时钟，同样应停止请求下一帧。
	if err := tt.Click("展开 已删会话残留"); err != nil {
		t.Fatalf("收起已删会话残留失败: %v", err)
	}
	for i := 0; i < 40; i++ {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("收起动画结束后不应继续请求下一帧")
	}
}

func TestSettingsStoragePage_CleanConfirmAndWorktreeNav(t *testing.T) {
	fake := NewFakeClient()
	fake.SetStorageReport(StorageReport{
		TotalBytes: 8192,
		Items: []StorageItem{
			{ID: "media/th-1", Category: StorageCategoryDeletedSessions, Bytes: 2048},
		},
	}, nil)

	st, tt := openStorageTestPage(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("移到废纸篓（2 KB）")
	})

	if err := tt.Click("移到废纸篓（2 KB）"); err != nil {
		t.Fatalf("点击主按钮失败: %v", err)
	}
	tt.Frame()

	// 确认弹窗出现后，再次点击确认按钮。
	if err := tt.Click("确认移到废纸篓"); err != nil {
		t.Fatalf("点击确认弹窗按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return len(fake.StorageCleanCalls()) == 1 && tt.HasText("没有可清理的内容")
	})

	if err := tt.Click("Worktree 清理"); err != nil {
		t.Fatalf("点击 Worktree 清理失败: %v", err)
	}
	tt.Frame()
	st.mu.Lock()
	page, sheet := st.currentPage, st.openSheet
	st.mu.Unlock()
	if page != "sourceControl" || sheet != "worktree" {
		t.Fatalf("跳转目标错误: page=%q sheet=%q, want sourceControl/worktree", page, sheet)
	}
}
