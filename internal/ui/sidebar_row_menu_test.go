package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// rowMenuRoot 打开两条会话的侧栏：「会话一」在上、「会话二」在下。
func rowMenuRoot(t *testing.T) (*chatState, *ui.Tester) {
	t.Helper()
	fake := NewFakeClient()
	now := time.Now()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "会话一", UpdatedAt: now},
		{ID: "t2", ProjectID: "p1", Title: "会话二", UpdatedAt: now.Add(-time.Minute)},
	}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("会话一") })
	return state, tt
}

// openRowMenu 悬停会话行、点开其「更多操作」按钮。
func openRowMenu(t *testing.T, tt *ui.Tester, title string) ui.Rect {
	t.Helper()
	row, ok := tt.Find("打开会话 " + title)
	if !ok {
		t.Fatalf("找不到会话行 %s", title)
	}
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	btn, ok := tt.Find("更多操作")
	if !ok {
		t.Fatalf("%s 行外没有更多操作按钮", title)
	}
	tt.ClickAt(btn.X+btn.W/2, btn.Y+btn.H/2)
	tt.Frame()
	if tt.Menu() == nil {
		t.Fatalf("%s 的更多操作菜单没有打开", title)
	}
	return btn
}

func rowPinned(state *chatState, id string) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, th := range state.threadsByProj["p1"] {
		if th.ID == id {
			return th.Pinned
		}
	}
	return false
}

// TestRowMenuChoiceAfterPointerLeaves 指针移到菜单上（行失去悬停）再选菜单项，
// 选择必须作用到打开菜单时的那条会话。
func TestRowMenuChoiceAfterPointerLeaves(t *testing.T) {
	state, tt := rowMenuRoot(t)
	btn := openRowMenu(t, tt, "会话一")
	tt.Move(btn.X+btn.W+60, btn.Y+btn.H+60)
	tt.Frame()
	if err := tt.ChooseMenuItem("置顶"); err != nil {
		t.Fatalf("选择置顶失败: %v", err)
	}
	waitFor(t, tt, func() bool { return rowPinned(state, "t1") })
	if rowPinned(state, "t2") {
		t.Fatalf("置顶作用到了会话二")
	}
}

// TestRowMenuChoiceOnOtherRow 菜单仍开着时指针移到另一条会话行上，
// 选择不得转而作用到被悬停的那条会话。
func TestRowMenuChoiceOnOtherRow(t *testing.T) {
	state, tt := rowMenuRoot(t)
	openRowMenu(t, tt, "会话一")
	row2, ok := tt.Find("打开会话 会话二")
	if !ok {
		t.Fatalf("找不到会话二")
	}
	tt.Move(row2.X+row2.W/2, row2.Y+row2.H/2)
	tt.Frame()
	if err := tt.ChooseMenuItem("置顶"); err != nil {
		t.Fatalf("选择置顶失败: %v", err)
	}
	waitFor(t, tt, func() bool { return rowPinned(state, "t1") })
	if rowPinned(state, "t2") {
		t.Fatalf("置顶作用到了被悬停的会话二")
	}
}

// TestRowMenuDelete 菜单里的「删除…」走确认框，指针已离开该行也要打开。
func TestRowMenuDelete(t *testing.T) {
	state, tt := rowMenuRoot(t)
	btn := openRowMenu(t, tt, "会话一")
	tt.Move(btn.X+btn.W+60, btn.Y+btn.H+60)
	tt.Frame()
	if err := tt.ChooseMenuItem("删除…"); err != nil {
		t.Fatalf("选择删除…失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.deleteModalOpen
	})
	state.mu.Lock()
	if state.deleteThreadID != "t1" {
		t.Fatalf("删除确认目标是 %q，应为 t1", state.deleteThreadID)
	}
	state.mu.Unlock()
}

// TestRowMenuDeleteClosesPinning 选完菜单项后，指针离开该行，按钮应随悬停一起消失。
func TestRowMenuDeleteClosesPinning(t *testing.T) {
	_, tt := rowMenuRoot(t)
	btn := openRowMenu(t, tt, "会话一")
	if err := tt.ChooseMenuItem("删除…"); err != nil {
		t.Fatalf("选择删除…失败: %v", err)
	}
	tt.Frame()
	tt.Move(btn.X+btn.W/2, btn.Y-40)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find("更多操作"); ok {
		t.Fatalf("菜单选择后更多操作按钮仍显示")
	}
}

// TestRowMenuButtonKeepsNoFocus 会话行带 KeepFocus，点开「更多操作」菜单后，
// 焦点留在原处，不落到「更多操作」按钮上。
func TestRowMenuButtonKeepsNoFocus(t *testing.T) {
	_, tt := rowMenuRoot(t)
	openRowMenu(t, tt, "会话一")
	buttonFocused := false
	for _, info := range tt.AccessAll() {
		if info.Name == "更多操作" && info.Focused {
			buttonFocused = true
		}
	}
	if buttonFocused {
		t.Fatal("点击更多操作后焦点不应落到按钮上")
	}
}

// TestRowMenuRowClickKeepsComposerFocus 输入框聚焦时点击另一条有消息的会话行，
// 焦点不得从输入框移到会话行上。
func TestRowMenuRowClickKeepsComposerFocus(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "会话一", UpdatedAt: now},
		{ID: "t2", ProjectID: "p1", Title: "会话二", UpdatedAt: now.Add(-time.Minute)},
	}
	fake.messages["t1"] = []Message{
		{ID: "m1", Role: "user", Kind: "text", Text: "第一条", IsFinal: true, TurnIndex: 1},
	}
	fake.messages["t2"] = []Message{
		{ID: "m2", Role: "user", Kind: "text", Text: "第二条", IsFinal: true, TurnIndex: 1},
	}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		if !tt.HasText("会话一") {
			return false
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.composerFocused
	})

	row2, ok := tt.Find("打开会话 会话二")
	if !ok {
		t.Fatal("找不到会话二")
	}
	tt.ClickAt(row2.X+row2.W/2, row2.Y+row2.H/2)
	tt.Frame()
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "t2"
	})

	if tt.Focused("打开会话 会话二") {
		t.Fatal("点击会话行后焦点不应落到会话行上")
	}
	state.mu.Lock()
	composerFocused := state.composerFocused
	state.mu.Unlock()
	if !composerFocused {
		t.Fatal("点击有消息的会话行后聊天输入框应保持焦点")
	}
}

// TestRowMenuActionsHideAfterRowClick 点击会话行并把指针移出该行后，
// 行尾「更多操作」按钮必须隐藏。
func TestRowMenuActionsHideAfterRowClick(t *testing.T) {
	_, tt := rowMenuRoot(t)
	row, ok := tt.Find("打开会话 会话一")
	if !ok {
		t.Fatal("找不到会话一")
	}
	tt.ClickAt(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	tt.Move(row.X+row.W+200, row.Y+row.H+200)
	tt.Frame()
	if _, ok := tt.Find("更多操作"); ok {
		t.Fatal("点击会话行并移出指针后更多操作按钮仍可见")
	}
}
