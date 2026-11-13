package mcodeui

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionTimelineMessageEnter(t *testing.T) {
	setAppReduceMotion(t, true)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	view := func(animate bool) func(*ui.Context) {
		return func(c *ui.Context) {
			row := ui.Box(c.Key("message")).Size(40, 20).Background(ui.Color{R: 255, A: 255}).Label("message")
			animateMessageEnter(c, row, "new", animate)
		}
	}
	actual := ui.NewTesterWith(view(true), 100, 60, options)
	expected := ui.NewTesterWith(view(false), 100, 60, options)
	actual.Frame()
	expected.Frame()
	if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
		t.Fatal("减少动效时新消息未立即完全显示")
	}
	if actual.FramePending() {
		t.Fatal("减少动效时消息入场仍请求帧")
	}

	appReduceMotion.Store(false)
	normal := ui.NewTester(view(true), 100, 60)
	normal.Advance(DurBase / 2)
	if !normal.FramePending() {
		t.Fatal("正常模式下消息应正在入场")
	}
	normal.Advance(DurBase)
	if normal.FramePending() {
		t.Fatal("消息入场结束后仍请求帧")
	}
}

func TestMotionTimelineSkeletonAndShimmer(t *testing.T) {
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	t.Run("skeleton", func(t *testing.T) {
		setAppReduceMotion(t, true)
		view := func(c *ui.Context) {
			renderTimelineSkeleton(c)
		}
		tester := ui.NewTesterWith(view, 200, 100, options)
		tester.Frame()
		if tester.FramePending() {
			t.Fatal("减少动效时骨架屏仍请求帧")
		}
		appReduceMotion.Store(false)
		normal := ui.NewTester(view, 200, 100)
		normal.Advance(50 * time.Millisecond)
		if !normal.FramePending() {
			t.Fatal("正常模式下骨架屏应持续呼吸")
		}
		appReduceMotion.Store(true)
		normal.SetPreferences(ui.Preferences{ReduceMotion: true})
		normal.Frame()
		normal.Advance(50 * time.Millisecond)
		if normal.FramePending() {
			t.Fatal("切换减少动效后骨架屏仍持续请求帧")
		}
	})
	t.Run("shimmer", func(t *testing.T) {
		setAppReduceMotion(t, true)
		view := func(c *ui.Context) {
			renderShimmerTitle(c, "shimmer", "扫光", true, func() ui.Element {
				return ui.Text(c, "标题")
			})
		}
		tester := ui.NewTesterWith(view, 200, 100, options)
		tester.Frame()
		if _, ok := tester.Find("扫光"); ok || tester.FramePending() {
			t.Fatal("减少动效时扫光仍存在或请求帧")
		}
		appReduceMotion.Store(false)
		normal := ui.NewTester(view, 200, 100)
		normal.Frame()
		normal.Advance(50 * time.Millisecond)
		if _, ok := normal.Find("扫光"); !ok || !normal.FramePending() {
			t.Fatal("正常模式下扫光应运行")
		}
		appReduceMotion.Store(true)
		normal.SetPreferences(ui.Preferences{ReduceMotion: true})
		normal.Frame()
		normal.Advance(50 * time.Millisecond)
		if _, ok := normal.Find("扫光"); ok || normal.FramePending() {
			t.Fatal("切换减少动效后扫光仍存在或持续请求帧")
		}
	})
}

func TestMotionTimelineSkeletonDisappears(t *testing.T) {
	setAppReduceMotion(t, true)
	s := newChatState(NewFakeClient())
	rev := uint64(1)
	tester := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() {
			renderTimelineSkeletonLayer(c, s, "motion-skeleton", rev, false)
		})
	}, 300, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tester.Frame()
	before := append([]byte(nil), tester.Image().Pix...)
	rev++
	tester.Frame()
	if bytes.Equal(before, tester.Image().Pix) {
		t.Fatal("历史加载完成后骨架屏未消失")
	}
	if tester.FramePending() {
		t.Fatal("骨架屏消失后仍请求帧")
	}
}

func TestMotionTimelineReducedDock(t *testing.T) {
	setAppReduceMotion(t, true)
	var s *chatState
	tester := ui.NewTesterWith(Root(NewFakeClient(), func(cs *chatState) { s = cs }), 1000, 700,
		ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	waitFor(t, tester, func() bool { return s != nil && tester.HasText("测试会话1") })
	if err := tester.Click("测试会话1"); err != nil {
		t.Fatalf("打开会话失败：%v", err)
	}
	waitFor(t, tester, func() bool { return tester.HasText("构建") })
	setTestThreadMessages(s, testDockMessages())
	tester.Frame()
	dockedY, ok := composerToggleY(tester)
	if !ok {
		t.Fatal("找不到停靠输入框")
	}
	setTestThreadMessages(s, nil)
	tester.Frame()
	s.mu.Lock()
	s.armComposerDockPromote("t1")
	s.mu.Unlock()
	tester.Frame()
	if y, ok := composerToggleY(tester); !ok || y < dockedY-1 || y > dockedY+1 {
		t.Fatalf("减少动效时输入框未立即停靠：实际 %.1f，期望 %.1f，存在=%v", y, dockedY, ok)
	}
}
