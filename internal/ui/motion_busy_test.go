package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionBusyLoops(t *testing.T) {
	plan := &chatState{
		threads:  []Thread{{ID: "thread", IsRunning: true}},
		messages: []Message{todoToolMsg("plan", `{"todos":[{"content":"进行中","status":"in_progress"}]}`)},
	}
	cases := []struct {
		name  string
		label string
		view  func(*ui.Context)
	}{
		{"总管角标", "", func(c *ui.Context) {
			renderStewardSectionAvatarBusy(c, "steward", "管", ThemeAccent(c))
		}},
		{"子代理运行状态", "", func(c *ui.Context) {
			renderRunningGlyph(c, "bee", IconMD, ThemeAccent(c))
		}},
		{"派发占位", "正在派发", func(c *ui.Context) {
			renderSwarmDispatchPlaceholder(c, "dispatch", swarmDispatchPlaceholderView{Visible: true, Text: "正在派发"})
		}},
		{"实时胶囊", "实时胶囊", func(c *ui.Context) {
			renderComposerCapsule(c, nil, composerCapsule{Key: "live", Label: "实时胶囊", Text: "运行中", Live: true})
		}},
		{"计划进度", "待办进度 0/1", func(c *ui.Context) {
			renderComposerPlanPill(c, plan, "thread")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visible := true
			view := func(c *ui.Context) {
				ui.Column(c).Fill().Children(func() {
					if visible {
						ui.Column(c.Key("busy")).Label("忙碌图形").Children(func() { tc.view(c) })
					}
				})
			}
			tester := ui.NewTesterWith(view, 400, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			tester.Frame()
			if rect, ok := tester.Find("忙碌图形"); !ok || rect.W <= 0 || rect.H <= 0 {
				t.Fatalf("忙碌图形不可见：%+v，存在=%v", rect, ok)
			}
			if tc.label != "" {
				if _, ok := tester.Find(tc.label); !ok {
					t.Fatalf("找不到忙碌内容 %q", tc.label)
				}
			}
			if tester.FramePending() {
				t.Fatal("减少动效时不应请求后续帧")
			}

			tester.SetPreferences(ui.Preferences{})
			tester.Frame()
			if !tester.FramePending() {
				t.Fatal("正常模式应请求后续帧")
			}
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			tester.Frame()
			tester.Advance(2 * time.Second)
			if tester.FramePending() {
				t.Fatal("切换减少动效后仍持续请求帧")
			}
			visible = false
			tester.Frame()
			if _, ok := tester.Find("忙碌图形"); ok {
				t.Fatal("结束后忙碌图形仍可见")
			}
		})
	}
}

func TestMotionBusyMarqueePreferenceChange(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Width(100).Children(func() {
			renderSidebarScrollingTitle(c, "title", "很长的会话标题很长的会话标题很长的会话标题", true, 0)
		})
	}, 120, 40)
	tester.Frame()
	tester.Frame()
	if !tester.FramePending() {
		t.Fatal("悬停溢出标题应请求跑马灯帧")
	}
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()
	tester.Advance(2 * time.Second)
	if tester.FramePending() {
		t.Fatal("切换减少动效后跑马灯仍持续请求帧")
	}
}
