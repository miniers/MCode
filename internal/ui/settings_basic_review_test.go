package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestArchiveLoadWithoutClientShowsError(t *testing.T) {
	resetArchivePageStateForTest()
	t.Cleanup(resetArchivePageStateForTest)

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	ensureArchiveLoaded(st)

	globalArchiveState.mu.Lock()
	loadErr := globalArchiveState.loadErr
	globalArchiveState.mu.Unlock()
	if loadErr == "" {
		t.Fatal("客户端为空时应写入 loadErr，实际为空")
	}
}

func TestArchiveRefreshErrorVisibleAfterLoaded(t *testing.T) {
	resetArchivePageStateForTest()
	t.Cleanup(resetArchivePageStateForTest)

	globalArchiveState.mu.Lock()
	globalArchiveState.loaded = true
	globalArchiveState.attempted = true
	globalArchiveState.loadErr = "刷新失败：网络中断"
	globalArchiveState.projects = map[string]string{"p1": "项目甲"}
	globalArchiveState.threads = []Thread{
		{ID: "th-1", ProjectID: "p1", Title: "已归档会话一", UpdatedAt: time.Now()},
	}
	globalArchiveState.mu.Unlock()

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageArchive(c, st)
	}, 1000, 800)
	tt.Frame()

	if _, ok := tt.Find("row-archive-load-error"); !ok {
		t.Fatalf("已加载且存在 loadErr 时应显示错误行 row-archive-load-error，实际文本: %v", tt.Texts())
	}
	if !tt.HasText("已归档会话一") {
		t.Fatalf("已加载且刷新失败时应保留列表显示，实际文本: %v", tt.Texts())
	}
}

func TestStorageRescanErrorVisibleAfterLoaded(t *testing.T) {
	fake := NewFakeClient()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()
	st.selectSettingsPage("storage")

	st.mu.Lock()
	ps := storageStateLocked(st)
	ps.gen++
	ps.loaded = true
	ps.scanning = false
	ps.attempted = true
	ps.scanErr = "重扫失败：权限不足"
	ps.report = StorageReport{
		TotalBytes: 4096,
		Items: []StorageItem{
			{ID: "media/th-1", Category: StorageCategoryDeletedSessions, Bytes: 1024},
		},
	}
	ps.selected = map[StorageCategory]bool{StorageCategoryDeletedSessions: true}
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsDetail(c, st)
	}, 1000, 800)
	tt.Frame()

	if _, ok := tt.Find("storage-scan-error-row"); !ok {
		t.Fatalf("已加载且重扫失败时应显示错误行 storage-scan-error-row，实际文本: %v", tt.Texts())
	}
	if !tt.HasText("已删会话残留") {
		t.Fatalf("已加载且重扫失败时应同时显示存储列表，实际文本: %v", tt.Texts())
	}
}
