package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestChatHeaderPanelButtonsResponsive(t *testing.T) {
	for _, tc := range []struct {
		name      string
		width     int
		collapsed bool
	}{
		{name: "宽窗口", width: 1200},
		{name: "窄窗口", width: 1000, collapsed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.TempHome(t)
			stubTerminals(t)
			fake := NewFakeClient()
			s, root := buildRoot(fake)
			t.Cleanup(func() { closeTerminals(s) })
			tt := ui.NewTester(root, tc.width, 700)
			waitFor(t, tt, func() bool {
				_, ok := tt.Find(headerPermissionCapsuleLabel)
				return ok
			})
			labels := []string{"终端", "右栏", "更多工具"}
			var first ui.Rect
			for i, label := range labels {
				r, ok := tt.Find(label)
				if ok == tc.collapsed {
					t.Fatalf("按钮 %q 的可见状态 = %v，收起 = %v", label, ok, tc.collapsed)
				}
				if ok {
					if i == 0 {
						first = r
					} else if r.W != first.W || r.H != first.H {
						t.Fatalf("按钮 %q 尺寸 = %v，首个按钮尺寸 = %v", label, r, first)
					}
				}
			}
			if _, ok := tt.Find("更多头部操作"); ok != tc.collapsed {
				t.Fatalf("更多按钮可见状态 = %v，收起 = %v", ok, tc.collapsed)
			}
			if _, ok := tt.Find("新建会话 " + shortcutDisplay("⌘N")); !ok {
				t.Fatal("新建会话按钮不可见")
			}
			if tc.collapsed {
				tt.Key(ui.Cmd, ui.KeyJ)
				waitFor(t, tt, func() bool { return getTerminalState(s).visible })
			}
		})
	}
}

func TestChatHeaderPanelButtonsRemainVisibleWithPreview(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { _, ok := tt.Find("右栏"); return ok })
	inspectorSelect(s, sidePanePreviewID, false)
	waitFor(t, tt, func() bool { return inspectorPaneOpen(s, sidePanePreviewID) })
	for _, label := range []string{"终端", "右栏", "更多工具"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("打开预览后头部开关 %q 不可见", label)
		}
	}
}

func TestChatHeaderClickRenameAndCancel(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	area, ok := tt.Find("会话标题拖动区")
	if !ok {
		t.Fatal("头部缺少会话标题拖动区")
	}
	for _, point := range []struct {
		name string
		x    float32
	}{
		{"文字", area.X + Space2 + 5},
		{"空白", area.X + area.W - 2},
	} {
		tt.Advance(time.Second)
		tt.ClickAt(point.x, area.Y+area.H/2)
		if _, ok := tt.Find("会话标题"); ok {
			t.Fatalf("单击%s后进入了编辑", point.name)
		}
		tt.ClickAt(point.x, area.Y+area.H/2)
		if _, ok := tt.Find("会话标题"); ok {
			t.Fatalf("双击%s后进入了编辑", point.name)
		}
	}

	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.renameBusy = true
	chatHeaderMu.Unlock()
	tt.RightClickAt(area.X+Space2+5, area.Y+area.H/2)
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("会话标题"); ok {
		t.Fatal("重命名进行中进入了编辑")
	}
	chatHeaderMu.Lock()
	hs.renameBusy = false
	chatHeaderMu.Unlock()

	if err := tt.RightClick("会话标题拖动区"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("会话标题"); return ok })
	tt.Command("selectAll")
	tt.Type("取消的标题")
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { _, ok := tt.Find("会话标题"); return !ok })
	if _, ok := tt.Find("打开会话 测试会话1"); !ok {
		t.Fatal("取消重命名后侧栏标题发生变化")
	}

	if err := tt.RightClick("会话标题拖动区"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("会话标题"); return ok })
	tt.Command("selectAll")
	tt.Type("新的会话标题")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { _, ok := tt.Find("打开会话 新的会话标题"); return ok })
}

func TestChatHeaderThreadsLongTitle(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	title := strings.Repeat("长", 30)
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: title})
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	s.toggleSidebar()
	waitFor(t, tt, func() bool { _, ok := tt.Find("会话列表"); return ok })
	if err := tt.Click("会话列表"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText(title) })
}

// TestChatHeaderIconButtonsHaveNoFill 验证头部图标按钮常态透明：按钮内缘与按钮左侧的头部底色一致。
// 漏传 Variant 时按钮落到主按钮样式，内缘是主色或灰块，这里会失败。
func TestChatHeaderIconButtonsHaveNoFill(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(headerPermissionCapsuleLabel)
		return ok
	})
	img := tt.Image()
	pixel := func(x, y int) [3]int {
		i := img.PixOffset(x, y)
		return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	}
	for _, label := range []string{"显示/隐藏侧边栏 " + shortcutDisplay("⌘B"), "新建会话 " + shortcutDisplay("⌘N"), "终端", "右栏", "更多工具"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("按钮 %q 不可见", label)
		}
		y := int(r.Y + r.H/2)
		inside, outside := pixel(int(r.X)+2, y), pixel(int(r.X)-1, y)
		if !colorNear(inside, outside, 2) {
			t.Fatalf("按钮 %q 内缘 %v 与头部底色 %v 不同", label, inside, outside)
		}
	}
}
