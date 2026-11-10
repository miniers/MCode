package mcodeui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	git "mcode/internal/git"
)

// 输入区状态胶囊行的用例：只按可见文字与 a11y Label 定位，不认元素 Key。

// seedCapsuleThread 把 t1 会话的运行态与最后状态改到位（调用方持 s.mu）。
func seedCapsuleThread(s *chatState, running bool, lastStatus string) {
	for i := range s.threads {
		if s.threads[i].ID != "t1" {
			continue
		}
		s.threads[i].IsRunning = running
		s.threads[i].SwarmBusy = false
		s.threads[i].LastStatus = lastStatus
	}
}

// TestComposerRunningChangesCapsule 验证运行中回合的改动胶囊：有 2 个改动文件时
// 显示文件数与增删行数，点击区域在同一个胶囊行里；回合结束后胶囊与整行一起消失。
func TestComposerRunningChangesCapsule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.go")
	bPath := filepath.Join(dir, "b.go")
	for _, path := range []string{aPath, bPath} {
		if err := os.WriteFile(path, []byte("package demo\n"), 0o600); err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
	}
	msgs := []Message{
		{ID: "m1", Role: "user", Text: "改两个文件", TurnIndex: 1},
		{ID: "m2", Role: "tool", ToolName: "edit", TurnIndex: 2, TurnID: "turn-2",
			ToolInput: fmt.Sprintf(`{"file_path":%q}`, aPath), ToolStatus: "completed"},
		{ID: "m3", Role: "tool", ToolName: "edit", TurnIndex: 2, TurnID: "turn-2",
			ToolInput: fmt.Sprintf(`{"file_path":%q}`, bPath), ToolStatus: "completed"},
	}

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = msgs
	s.messagesRev++ // 回合号按 messagesRev 缓存，直接改消息必须让它失效。
	seedCapsuleThread(s, true, "")
	// 种下回合级 diff 缓存：两个文件的延迟补丁统计，合计 +3 −1。
	if s.toolDiff.entries == nil {
		s.toolDiff.entries = make(map[string]*toolDiffEntry)
	}
	s.toolDiff.entries["t1|turn-2"] = &toolDiffEntry{files: []git.FileDiff{
		{NewPath: "a.go", IsDeferred: true, Additions: 2, Deletions: 1},
		{NewPath: "b.go", IsDeferred: true, Additions: 1, Deletions: 0},
	}}
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { return tt.HasText("2 个文件") })
	if !tt.HasText("2 个文件 +3 −1") {
		t.Fatalf("运行中改动胶囊应显示文件数与增删行数，当前可见文字: %v", tt.Texts())
	}
	if _, ok := tt.Find("运行中改动"); !ok {
		t.Fatal("应能找到运行中改动胶囊")
	}
	// 改动统计行出现时胶囊并入那一行，不再单独占一行；没有统计行时仍保留独立胶囊行。
	if _, hasChangesRow := tt.Find("2 个文件"); hasChangesRow {
		if _, hasCapsuleRow := tt.Find("输入区状态胶囊"); hasCapsuleRow {
			t.Fatal("改动统计行出现时不应再渲染独立的胶囊行")
		}
	}

	// 回合结束后胶囊消失，由时间线里的改动卡接管。
	s.mu.Lock()
	seedCapsuleThread(s, false, "")
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { _, ok := tt.Find("运行中改动"); return !ok })
	if _, ok := tt.Find("输入区状态胶囊"); ok {
		t.Fatal("没有胶囊时不应渲染胶囊行容器")
	}
}

// TestComposerCapsulesShareOneRow 验证待办进度与继续提示两枚胶囊同时出现时在同一行：
// 两者的中心 Y 相同，且都落在同一个带 Label 的胶囊行容器内。
func TestComposerPlanCapsuleEmbeddedBee(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	s.embeddedBee = true
	s.selectedThreadID = "bee"
	s.messages = []Message{{ID: "todo", Role: "tool", ToolName: "todo_write", ToolInput: `{"todos":[{"content":"一","status":"completed"},{"content":"二","status":"completed"},{"content":"三","status":"in_progress"}]}`}}
	capsule, ok := composerPlanCapsule(nil, s, "bee")
	if !ok || capsule.Label != "待办进度 2/3" {
		t.Fatalf("子代理待办胶囊 = %+v, ok=%v", capsule, ok)
	}
	s.composerView.planPillOpen = true
	s.messages[0].ToolInput = `{"todos":[{"content":"一","status":"completed"},{"content":"二","status":"completed"},{"content":"三","status":"completed"}]}`
	if _, ok := composerPlanCapsule(nil, s, "bee"); ok || s.composerView.planPillOpen {
		t.Fatal("全部完成后应隐藏胶囊并关闭浮层")
	}
}

func TestComposerCapsulesShareOneRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	msgs := []Message{
		{ID: "m1", Role: "user", Text: "开始", TurnIndex: 1},
		{ID: "m2", Role: "tool", ToolName: "todowrite", TurnIndex: 1,
			ToolInput: `{"todos":[{"content":"写文档","status":"pending"},{"content":"写测试","status":"completed"}]}`},
		{ID: "m3", Role: "assistant", Text: "先停一下", TurnIndex: 1},
	}
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = msgs
	s.messagesRev++
	// 上一轮中断 → 继续胶囊；待办未完成且出现在最后一条用户消息之后 → 待办胶囊。
	seedCapsuleThread(s, false, "interrupted")
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { _, ok := tt.Find("待办进度 1/2"); return ok })
	planRect, ok := tt.Find("待办进度 1/2")
	if !ok {
		t.Fatalf("应能找到待办进度胶囊，当前可见文字: %v", tt.Texts())
	}
	continueRect, ok := tt.Find("上一轮已中断 · 继续")
	if !ok {
		t.Fatalf("应能找到继续胶囊，当前可见文字: %v", tt.Texts())
	}
	rowRect, ok := tt.Find("输入区状态胶囊")
	if !ok {
		t.Fatal("应能找到胶囊行容器")
	}

	if planRect.H != ControlSM || continueRect.H != ControlSM {
		t.Fatalf("胶囊高度应为 %v: 待办 %v / 继续 %v", ControlSM, planRect.H, continueRect.H)
	}
	planY := planRect.Y + planRect.H/2
	continueY := continueRect.Y + continueRect.H/2
	if math.Abs(float64(planY-continueY)) > 1 {
		t.Fatalf("两枚胶囊应在同一行，中心 Y 不一致: 待办 %.1f / 继续 %.1f", planY, continueY)
	}
	if !rowRect.Contains(planRect.X+planRect.W/2, planY) || !rowRect.Contains(continueRect.X+continueRect.W/2, continueY) {
		t.Fatalf("两枚胶囊都应落在同一个胶囊行容器内: row=%+v plan=%+v continue=%+v", rowRect, planRect, continueRect)
	}
}

// TestComposerCapsuleRowHiddenWhenEmpty 验证没有任何胶囊时整行不渲染。
func TestComposerCapsuleRowHiddenWhenEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = []Message{
		{ID: "m1", Role: "user", Text: "你好", TurnIndex: 1},
		{ID: "m2", Role: "assistant", Text: "普通回复", TurnIndex: 1, IsFinal: true},
	}
	s.messagesRev++
	seedCapsuleThread(s, false, "")
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { return tt.HasText("普通回复") })
	if _, ok := tt.Find("输入区状态胶囊"); ok {
		t.Fatalf("没有胶囊时不应渲染胶囊行容器，当前可见文字: %v", tt.Texts())
	}
}

// TestComposerBackgroundTasksCapsule 验证后台任务胶囊的显示条件与文案：
// 有后台任务时是实时状态胶囊，正文取最早任务名、计时与「+N」；清空后胶囊消失并把浮层状态复位，
// 子代理浮动面板里不显示这枚胶囊。
func TestComposerBackgroundTasksCapsule(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	t.Cleanup(func() {
		swarmPanelMu.Lock()
		delete(swarmPanelStates, s)
		swarmPanelMu.Unlock()
	})

	now := time.Now()
	s.mu.Lock()
	s.messages = []Message{
		{ID: "u1", Role: "user", Text: "开始", TurnIndex: 1, IsFinal: true},
		backgroundTasksTestBackgroundAt("bg2", "拉取依赖", now.Add(-30*time.Second)),
		backgroundTasksTestBackgroundAt("bg1", "运行 ui 包全量测试", now.Add(-90*time.Second)),
	}
	s.mu.Unlock()

	capsule, ok := composerBackgroundTasksCapsule(nil, s, "t1")
	if !ok {
		t.Fatalf("有后台任务时应显示后台任务胶囊")
	}
	if !capsule.Live {
		t.Fatalf("后台任务胶囊应为实时状态胶囊")
	}
	if capsule.Icon != nil {
		t.Fatalf("实时状态胶囊不应再画前导图标")
	}
	if !strings.HasPrefix(capsule.Text, "运行 ui 包全量测试 · ") || !strings.HasSuffix(capsule.Text, " +1") {
		t.Fatalf("胶囊正文 = %q, want 最早任务名开头并以 +1 结尾", capsule.Text)
	}
	if capsule.Label != "后台任务 · 2 个运行中" {
		t.Fatalf("胶囊 Label = %q, want 后台任务 · 2 个运行中", capsule.Label)
	}

	// 后台项清空：胶囊消失，已打开的浮层同步复位。
	s.mu.Lock()
	s.messages = []Message{{ID: "u1", Role: "user", Text: "开始", TurnIndex: 1, IsFinal: true}}
	s.mu.Unlock()
	s.composerView.backgroundTasksOpen = true
	if _, ok := composerBackgroundTasksCapsule(nil, s, "t1"); ok {
		t.Fatalf("没有后台项时不应显示后台任务胶囊")
	}
	if s.composerView.backgroundTasksOpen {
		t.Fatalf("后台项清空后应收起后台任务浮层")
	}

	// 子代理浮动面板里不显示这枚胶囊。
	s.mu.Lock()
	s.messages = []Message{backgroundTasksTestBackground("bg1")}
	s.mu.Unlock()
	s.embeddedBee = true
	if _, ok := composerBackgroundTasksCapsule(nil, s, "t1"); ok {
		t.Fatalf("子代理浮动面板里不应显示后台任务胶囊")
	}
}

// TestComposerBackgroundTasksCapsuleOpensPanel 验证点击后台任务胶囊会打开浮层：
// 浮层里显示「后台任务」表头与运行中计数。
func TestComposerBackgroundTasksCapsuleOpensPanel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = []Message{
		{ID: "m1", Role: "user", Text: "跑个后台任务", TurnIndex: 1},
		backgroundTasksTestBackground("m2"),
	}
	s.messagesRev++
	seedCapsuleThread(s, false, "")
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { _, ok := tt.Find("后台任务 · 1 个运行中"); return ok })
	clickText(t, tt, "后台任务 · 1 个运行中")

	if !s.composerView.backgroundTasksOpen {
		t.Fatalf("点击后台任务胶囊后应打开浮层")
	}
	if !tt.HasText("后台任务") || !tt.HasText("1 个运行中") {
		t.Fatalf("浮层应显示表头与运行中计数，当前可见文字: %v", tt.Texts())
	}
}

// TestComposerCapsulesMergeIntoChangesRow 验证有改动统计行时状态胶囊并入同一行：
// 胶囊与「N 个文件」tab 垂直居中于一行，并排在 tab 右侧，不再单独占一行。
func TestComposerCapsulesMergeIntoChangesRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	capsules := []composerCapsule{{
		Key:   "test-pill",
		Label: "测试胶囊",
		Text:  "1 个后台任务",
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderThreadChangesTab(c, s, 10, 2, 3, capsules)
	}, 800, 200)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	tabRect, ok := tt.Find("3 个文件")
	if !ok {
		t.Fatalf("应能找到改动统计 tab，当前可见文字: %v", tt.Texts())
	}
	pillRect, ok := tt.Find("测试胶囊")
	if !ok {
		t.Fatalf("应能找到并入改动统计行的胶囊，当前可见文字: %v", tt.Texts())
	}
	tabY := tabRect.Y + tabRect.H/2
	pillY := pillRect.Y + pillRect.H/2
	if math.Abs(float64(tabY-pillY)) > 2 {
		t.Fatalf("胶囊应与改动统计 tab 同一行，中心 Y 不一致: tab %.1f / 胶囊 %.1f", tabY, pillY)
	}
	if pillRect.X < tabRect.X+tabRect.W {
		t.Fatalf("胶囊应排在改动统计 tab 右侧: tab=%+v 胶囊=%+v", tabRect, pillRect)
	}
}

// TestComposerCapsulePopoverOpensAbove 验证胶囊浮层向上展开：
// 胶囊行贴在窗口底部时，浮层整体落在胶囊上方，不会遮住输入框。
func TestComposerCapsulePopoverOpensAbove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	open := true
	tt := ui.NewTester(func(c *ui.Context) {
		capsules := []composerCapsule{{
			Key:         "test-pill",
			Label:       "测试胶囊",
			Text:        "1 个后台任务",
			PopoverKey:  "test-popover",
			PopoverOpen: &open,
			PopoverBody: func() {
				ui.Column(c.Key("test-popover-body")).
					Label("测试浮层").
					Width(200).
					Padding(8).
					Children(func() {
						ui.Text(c, "浮层内容").FontSize(12)
					})
			},
		}}
		ui.Column(c.Key("test-host")).Fill().Children(func() {
			ui.Spacer(c).Grow(1)
			renderComposerCapsuleRow(c, nil, capsules)
		})
	}, 600, 600)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}

	pillRect, ok := tt.Find("测试胶囊")
	if !ok {
		t.Fatalf("应能找到胶囊，当前可见文字: %v", tt.Texts())
	}
	popoverRect, ok := tt.Find("测试浮层")
	if !ok {
		t.Fatalf("应能找到胶囊浮层，当前可见文字: %v", tt.Texts())
	}
	if popoverRect.Y+popoverRect.H > pillRect.Y+1 {
		t.Fatalf("胶囊浮层应展开在胶囊上方: 浮层=%+v 胶囊=%+v", popoverRect, pillRect)
	}
}
