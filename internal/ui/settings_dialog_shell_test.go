package mcodeui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsDialogShell(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, 1600, 1000)
	tt.Frame()

	card, ok := tt.Find("设置")
	if !ok {
		t.Fatal("找不到设置卡片")
	}
	if card.W > 960 || card.H > 680 || math.Abs(float64(card.X-(1600-card.W)/2)) > 2 || math.Abs(float64(card.Y-(1000-card.H)/2)) > 2 {
		t.Fatalf("设置卡片尺寸或位置不正确：%+v", card)
	}
	if tt.HasText("返回") {
		t.Fatal("设置导航仍显示返回")
	}
	if err := tt.Click("关闭设置"); err != nil {
		t.Fatalf("点击关闭设置失败：%v", err)
	}
	if st.IsOpen() {
		t.Fatal("点击关闭后设置仍然打开")
	}
}

func TestSettingsDialogSheetEscape(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	openSettingsSheet(st, "shortcuts", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, 1600, 1000)
	tt.Frame()

	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if sheet, _ := settingsTreeSheetState(st); sheet != "" || !st.IsOpen() {
		t.Fatalf("第一次 Esc 后 sheet = %q，设置打开 = %v", sheet, st.IsOpen())
	}
	tt.Frame()
	if !st.IsOpen() {
		t.Fatal("子面板关闭后的下一帧不应关闭设置")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if st.IsOpen() {
		t.Fatal("第二次 Esc 后设置仍然打开")
	}
}

func TestSettingsDialogDrag(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, 1600, 1000)
	tt.Frame()
	before, ok := tt.Find("设置")
	if !ok {
		t.Fatal("找不到设置卡片")
	}
	detail, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatal("找不到设置详情列")
	}
	x, y := detail.X+Space6+8, detail.Y+Space5+TypePageTitle/2
	tt.Press(x, y)
	tt.Move(x+40, y+30)
	tt.Release(x+40, y+30)
	tt.Frame()
	after, ok := tt.Find("设置")
	if !ok || math.Abs(float64(after.X-before.X-40)) > 2 || math.Abs(float64(after.Y-before.Y-30)) > 2 {
		t.Fatalf("拖动前 %v，拖动后 %v", before, after)
	}
}

func TestSettingsDialogResize(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, 1600, 1000)
	tt.Frame()
	before, ok := tt.Find("设置")
	if !ok {
		t.Fatal("找不到设置卡片")
	}
	x, y := before.X+before.W-8, before.Y+before.H-8
	tt.Press(x, y)
	tt.Move(x+30, y+30)
	tt.Release(x+30, y+30)
	tt.Frame()
	larger, ok := tt.Find("设置")
	if !ok || larger.W <= before.W || larger.H <= before.H {
		t.Fatalf("缩放前 %v，放大后 %v", before, larger)
	}
	x, y = larger.X+larger.W-8, larger.Y+larger.H-8
	tt.Press(x, y)
	tt.Move(x-1000, y-1000)
	tt.Release(x-1000, y-1000)
	tt.Frame()
	smaller, ok := tt.Find("设置")
	if !ok || smaller.W != 640 || smaller.H != 420 {
		t.Fatalf("最小尺寸应为 640×420，实际 %v", smaller)
	}
}

func TestSettingsDialogReopenCentered(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, 1600, 1000)
	tt.Frame()
	detail, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatal("找不到设置详情列")
	}
	x, y := detail.X+Space6+8, detail.Y+Space5+TypePageTitle/2
	tt.Press(x, y)
	tt.Move(x+40, y+30)
	tt.Release(x+40, y+30)
	tt.Frame()
	if err := tt.Click("关闭设置"); err != nil {
		t.Fatalf("点击关闭设置失败：%v", err)
	}
	if st.IsOpen() {
		t.Fatal("设置未关闭")
	}
	st.open = true
	tt.Frame()
	card, ok := tt.Find("设置")
	if !ok || math.Abs(float64(card.X-(1600-card.W)/2)) > 2 || math.Abs(float64(card.Y-(1000-card.H)/2)) > 2 {
		t.Fatalf("重新打开后未居中：%v", card)
	}
}
