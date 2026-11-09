package mcodeui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMemoryCreateFormAndCancel(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt, path := openMemoryPanel(t, fake, s, view)
	waitFor(t, tt, func() bool { return tt.HasText("新建记忆") })
	if err := tt.Click("新建记忆"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("标题"); return ok })
	if err := tt.Click("标题"); err != nil {
		t.Fatal(err)
	}
	tt.Type("取消的记忆")
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if calls := fake.MemoryCreateCalls(); len(calls) != 0 {
		t.Fatalf("取消后仍创建记忆：%v", calls)
	}
	if err := tt.Click("新建记忆"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("标题"); return ok })
	if err := tt.Click("标题"); err != nil {
		t.Fatal(err)
	}
	tt.Type("新建的记忆")
	if err := tt.Click("内容"); err != nil {
		t.Fatal(err)
	}
	tt.Type("需要确认的正文")
	if err := tt.Click("加入待审"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.MemoryCreateCalls()
		snap := snapshotMemoryPanel(s)
		return len(calls) == 1 && calls[0][0] == path && calls[0][2] == "新建的记忆" && calls[0][3] == "需要确认的正文" && !snap.creating && len(snap.proposals) == 1 && snap.proposals[0].Title == "新建的记忆"
	})
}

func TestMemoryTabsAndPendingBadge(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := getMemoryPanelState(s)
	tt := ui.NewTester(func(c *ui.Context) { renderMemoryTabs(c, s) }, 800, 200)
	if _, ok := tt.Find("0"); ok {
		t.Fatal("待审数量为零时不应显示徽标")
	}
	memoryPanelMu.Lock()
	st.pendingCount = 17
	memoryPanelMu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("17"); !ok {
		t.Fatal("待审数量非零时缺少徽标")
	}
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	if snapshotMemoryPanel(s).tab != memoryTabSearch {
		t.Fatal("点击搜索后没有切换标签")
	}
	if err := tt.Click("待审"); err != nil {
		t.Fatal(err)
	}
	if snapshotMemoryPanel(s).tab != memoryTabPending {
		t.Fatal("点击待审后没有切换标签")
	}
	if _, ok := tt.Find("17"); !ok {
		t.Fatal("切换标签后徽标消失")
	}
}

func TestContextCreateFormNameSync(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	cp := getContextPanelState(s)
	tt := ui.NewTester(func(c *ui.Context) {
		contextPanelMu.Lock()
		name := cp.newName
		contextPanelMu.Unlock()
		renderContextCreateDialog(c, s, name)
	}, 800, 600)
	if err := tt.Click("文档名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("design.md")
	contextPanelMu.Lock()
	name := cp.newName
	contextPanelMu.Unlock()
	if name != "design.md" {
		t.Fatalf("文档名称未同步：%q", name)
	}
}

func TestContextCloseConfirmationEscapeOrder(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	cp := getContextPanelState(s)
	contextPanelMu.Lock()
	cp.open, cp.dirty, cp.draft = true, true, "未保存"
	contextPanelMu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderContextModal(c, s) }, 1000, 700)
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { return tt.HasText("保存这次编辑？") })
	if st := lockedContextPanelCopy(cp); !st.open || !st.leaveOpen || !st.dirty {
		t.Fatal("关闭未保存文档时没有保留面板和确认框")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if st := lockedContextPanelCopy(cp); !st.open || st.leaveOpen || !st.dirty || st.draft != "未保存" {
		t.Fatalf("Escape 应先取消确认并保留草稿：%+v", st)
	}
}

func TestFileOperationEnterOnceAndErrorKeepsDialog(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFileOpsError(errors.New("创建失败"))
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.openFileManagerOpsDialog(fileManagerOpsNewFile, "", "")
	tt := ui.NewTester(func(c *ui.Context) { renderFileManagerOpsDialog(c, s) }, 800, 600)
	if err := tt.Click("文件名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("example.txt")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("创建失败") })
	if calls := fake.FileOpCalls(); len(calls) != 1 || calls[0].Path != "example.txt" {
		t.Fatalf("Enter 提交次数或路径错误：%v", calls)
	}
	if !fileManagerOpsOpen(s) {
		t.Fatal("出错后弹窗应保持打开")
	}
}
