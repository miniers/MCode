package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// backgroundWaitTestState 造一个空闲会话：选中 t1，消息里只有给定的后台消息。
func backgroundWaitTestState(messages ...Message) *chatState {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.messages = messages
	return s
}

// TestShouldShowBackgroundWait 验证等待条的显示条件：只有会话空闲且仍有后台项时才显示；
// 会话运行中由运行中工具组与「回复中」指示表达，不再重复显示等待条。
func TestShouldShowBackgroundWait(t *testing.T) {
	withTask := backgroundTasksItem{tasks: []Message{backgroundTasksTestBackground("bg1")}}
	cases := []struct {
		name    string
		running bool
		data    backgroundTasksItem
		want    bool
	}{
		{"运行中不显示", true, withTask, false},
		{"空闲且有任务显示", false, withTask, true},
		{"空闲无任务不显示", false, backgroundTasksItem{}, false},
		{"运行中且无任务不显示", true, backgroundTasksItem{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldShowBackgroundWait(tc.running, tc.data); got != tc.want {
				t.Fatalf("shouldShowBackgroundWait(%v, total=%d) = %v, want %v",
					tc.running, tc.data.total(), got, tc.want)
			}
		})
	}
}

// TestRenderBackgroundWaitBar 验证 Tester 渲染：空闲且有一个后台任务时出现可点击的等待条，
// 显示任务数、最早任务名与扫光，不再重复续跑提示（该提示只在输入区浮层里）；
// 任务清空后等待条整行不渲染。
func TestRenderBackgroundWaitBar(t *testing.T) {
	s := backgroundWaitTestState(backgroundTasksTestBackground("bg1"))
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundWaitBar(c, s, "background-wait-t1")
	}, 800, 300)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	if _, ok := tt.Find("等待后台任务"); !ok {
		t.Fatalf("等待条应带「等待后台任务」Label，当前文本 %v", tt.Texts())
	}
	if !tt.HasText("等待 1 个后台任务") {
		t.Fatalf("等待条应显示后台任务数，当前文本 %v", tt.Texts())
	}
	if !tt.HasText("npm run build") {
		t.Fatalf("等待条应显示最早任务名，当前文本 %v", tt.Texts())
	}
	if _, ok := tt.Find(backgroundWaitShimmerLabel); !ok {
		t.Fatalf("等待条应渲染扫光条，当前文本 %v", tt.Texts())
	}
	if tt.HasText(backgroundTasksAutoContinueHint) {
		t.Fatalf("等待条不应再重复续跑提示，当前文本 %v", tt.Texts())
	}

	// 后台任务结束：命令已完成，不再有后台项，等待条整行不渲染。
	finished := backgroundWaitTestState(backgroundTasksTestCommand("c1", 1, "completed"))
	empty := ui.NewTester(func(c *ui.Context) {
		renderBackgroundWaitBar(c, finished, "background-wait-t1")
	}, 800, 300)
	for i := 0; i < 3; i++ {
		empty.Frame()
	}
	if _, ok := empty.Find("等待后台任务"); ok {
		t.Fatalf("没有后台项时不应渲染等待条，当前文本 %v", empty.Texts())
	}
	if empty.HasText("等待") {
		t.Fatalf("没有后台项时不应出现等待文案，当前文本 %v", empty.Texts())
	}
}

// TestRenderBackgroundWaitBarCount 验证等待条正文不再重复计数：总数已经写在前面，
// 状态文案不再追加「+N」。
func TestRenderBackgroundWaitBarCount(t *testing.T) {
	s := backgroundWaitTestState(
		backgroundTasksTestBackground("bg1"),
		backgroundTasksTestBackground("bg2"),
	)
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundWaitBar(c, s, "background-wait-t1")
	}, 800, 300)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	if !tt.HasText("等待 2 个后台任务") {
		t.Fatalf("等待条应显示后台任务总数，当前文本 %v", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "+1") {
			t.Fatalf("等待条不应再追加「+N」，当前文本 %v", tt.Texts())
		}
	}
}

// TestRenderBackgroundWaitBarHasNoHint 验证无论后台项是什么，等待条都不渲染续跑提示：
// 该提示只在输入区胶囊的浮层里出现。
func TestRenderBackgroundWaitBarHasNoHint(t *testing.T) {
	render := func(messages ...Message) *ui.Tester {
		s := backgroundWaitTestState(messages...)
		tt := ui.NewTester(func(c *ui.Context) {
			renderBackgroundWaitBar(c, s, "background-wait-t1")
		}, 800, 300)
		for i := 0; i < 3; i++ {
			tt.Frame()
		}
		return tt
	}

	// 回合结束后仍在跑的命令：不显示提示。
	commands := render(backgroundTasksTestCommand("c1", 1, "running"))
	if !commands.HasText("等待 1 个后台任务") {
		t.Fatalf("等待条应显示后台命令，当前文本 %v", commands.Texts())
	}
	if commands.HasText(backgroundTasksAutoContinueHint) {
		t.Fatalf("后台命令不应显示续跑提示，当前文本 %v", commands.Texts())
	}

	// provider 托管的后台任务：同样不显示提示。
	tasks := render(backgroundTasksTestBackground("bg1"))
	if tasks.HasText(backgroundTasksAutoContinueHint) {
		t.Fatalf("等待条不应显示续跑提示，当前文本 %v", tasks.Texts())
	}
}

// TestBackgroundWaitBarOpensTasksPanel 验证点击等待条会打开输入区胶囊上已有的后台任务浮层。
func TestBackgroundWaitBarOpensTasksPanel(t *testing.T) {
	s := backgroundWaitTestState(backgroundTasksTestBackground("bg1"))
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundWaitBar(c, s, "background-wait-t1")
	}, 800, 300)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	clickText(t, tt, "等待后台任务")
	if !s.composerView.backgroundTasksOpen {
		t.Fatalf("点击等待条后应打开后台任务浮层")
	}
}
