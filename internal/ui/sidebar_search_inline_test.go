package mcodeui

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func buildSidebarInlineTestFixture(t *testing.T, reduceMotion bool) (*ui.Tester, *chatState) {
	t.Helper()
	testenv.TempHome(t)
	setAppReduceMotion(t, reduceMotion)
	resetSidebarStateGroupLatch()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		resetSidebarStateGroupLatch()
		resetSidebarHoverCardForTest()
	})

	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "测试项目", Path: "/tmp/mcode-inline-p1"},
		},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊会话", UpdatedAt: now},
			},
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now.Add(-time.Minute)},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduceMotion}})
	waitFor(t, tt, func() bool {
		return tt.HasText("临时会话") && tt.HasText("测试项目") && tt.HasText("项目会话")
	})
	return tt, state
}

func rectMaxChannelDelta(t *testing.T, img *image.RGBA, r ui.Rect) int {
	t.Helper()
	if img == nil {
		t.Fatal("Tester.Image() 返回 nil")
	}
	x0 := max(img.Rect.Min.X, int(r.X)+1)
	y0 := max(img.Rect.Min.Y, int(r.Y)+1)
	x1 := min(img.Rect.Max.X-1, int(r.X+r.W)-2)
	y1 := min(img.Rect.Max.Y-1, int(r.Y+r.H)-2)
	if x1 <= x0 || y1 <= y0 {
		t.Fatalf("按钮矩形过小，无法采样像素: %+v", r)
	}
	baseOff := img.PixOffset(x0, y0)
	br, bg, bb := int(img.Pix[baseOff]), int(img.Pix[baseOff+1]), int(img.Pix[baseOff+2])
	maxDelta := 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			off := img.PixOffset(x, y)
			for ci, base := range []int{br, bg, bb} {
				d := int(img.Pix[off+ci]) - base
				if d < 0 {
					d = -d
				}
				if d > maxDelta {
					maxDelta = d
				}
			}
		}
	}
	return maxDelta
}

// TestSidebarSearchInlineControls 验证过滤和动态视图按钮内嵌在搜索框内部右侧、尺寸为 ControlXS，
// 点击能打开下拉与切换动态视图，且不会把焦点抢到搜索输入框。
func TestSidebarSearchInlineControls(t *testing.T) {
	tt, state := buildSidebarInlineTestFixture(t, true)

	searchOuter, ok := tt.Find("侧栏搜索框")
	if !ok {
		t.Fatalf("找不到侧栏搜索框: %q", tt.Texts())
	}
	filterBtn, ok := tt.Find("过滤对话")
	if !ok {
		t.Fatalf("找不到过滤对话按钮: %q", tt.Texts())
	}
	bellLabel := "开启动态视图 (" + shortcutDisplay("⌥⌘U") + ")"
	bellBtn, ok := tt.Find(bellLabel)
	if !ok {
		t.Fatalf("找不到动态视图按钮: %q", tt.Texts())
	}

	if filterBtn.W != ControlXS || filterBtn.H != ControlXS {
		t.Fatalf("过滤按钮尺寸应为 ControlXS(%v): got %+v", ControlXS, filterBtn)
	}
	if bellBtn.W != ControlXS || bellBtn.H != ControlXS {
		t.Fatalf("动态视图按钮尺寸应为 ControlXS(%v): got %+v", ControlXS, bellBtn)
	}
	if !rectContains(searchOuter, filterBtn) {
		t.Fatalf("过滤按钮应完全位于搜索框内部: search=%+v, filter=%+v", searchOuter, filterBtn)
	}
	if !rectContains(searchOuter, bellBtn) {
		t.Fatalf("动态视图按钮应完全位于搜索框内部: search=%+v, bell=%+v", searchOuter, bellBtn)
	}

	// 点击过滤按钮打开下拉，且不把焦点抢到搜索框。
	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("点击过滤按钮失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("过滤对话：运行中"); !ok {
		t.Fatalf("点击过滤按钮后应展开过滤下拉菜单: %q", tt.Texts())
	}
	if tt.Focused("搜索对话...") {
		t.Fatal("点击内嵌过滤按钮不应把焦点抢到搜索输入框")
	}

	// 关闭过滤下拉后点击铃铛切换动态视图，同样不抢焦点。
	if err := tt.Click("过滤对话：全部"); err != nil {
		t.Fatalf("选择过滤项失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click(bellLabel); err != nil {
		t.Fatalf("点击动态视图按钮失败: %v", err)
	}
	tt.Frame()
	if !state.activityViewEnabled() {
		t.Fatal("点击铃铛后应开启动态视图")
	}
	if _, ok := tt.Find("关闭动态视图 (" + shortcutDisplay("⌥⌘U") + ")"); !ok {
		t.Fatalf("开启动态视图后按钮文案应切换为关闭态: %q", tt.Texts())
	}
	if tt.Focused("搜索对话...") {
		t.Fatal("点击内嵌动态视图按钮不应把焦点抢到搜索输入框")
	}
}

// TestSidebarTopGapBelowTrafficLights 验证 darwin 下搜索框顶边位于交通灯留白之后，
// windows 注入下保持与改动前一致的顶栏居中位置。
func TestSidebarTopGapBelowTrafficLights(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		withTitleBarGOOS(t, "darwin")
		tt, _ := buildSidebarInlineTestFixture(t, true)
		tt.SetTitleBar(ui.TitleBar{Height: 32, Left: 78})

		searchOuter, ok := tt.Find("侧栏搜索框")
		if !ok {
			t.Fatalf("找不到侧栏搜索框: %q", tt.Texts())
		}
		wantY := TrafficLightTopOffset + Space2 + Space1
		if searchOuter.Y != wantY {
			t.Fatalf("darwin 搜索框顶边 Y = %v, want %v", searchOuter.Y, wantY)
		}
	})

	t.Run("windows", func(t *testing.T) {
		withTitleBarGOOS(t, "windows")
		tt, _ := buildSidebarInlineTestFixture(t, true)
		tt.SetTitleBar(ui.TitleBar{Height: HeaderHeight, Right: 138})

		searchOuter, ok := tt.Find("侧栏搜索框")
		if !ok {
			t.Fatalf("找不到侧栏搜索框: %q", tt.Texts())
		}
		wantY := (HeaderHeight - ControlMD) / 2
		if searchOuter.Y < Space1 || searchOuter.Y+searchOuter.H > HeaderHeight-Space1 || math.Abs(float64(searchOuter.Y-wantY)) > 0.5 {
			t.Fatalf("windows 搜索框顶边 Y = %v, want ~%v (HeaderHeight=%v, ControlMD=%v)", searchOuter.Y, wantY, HeaderHeight, ControlMD)
		}
	})
}
