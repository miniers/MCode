package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestUIReview26NarrowChatHeaderAndComposerBounds(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)

	longTitle := strings.Repeat("甲乙丙丁戊己庚辛", 10)             // 80 个中文字符
	longBranch := strings.Repeat("feature/long-branch-", 3) // 60 个字符
	if len([]rune(longTitle)) != 80 {
		t.Fatalf("标题长度 = %d, want 80", len([]rune(longTitle)))
	}
	if len(longBranch) != 60 {
		t.Fatalf("分支名长度 = %d, want 60", len(longBranch))
	}

	fake := newGitFakeClient()
	fake.gitState.Branch = longBranch
	fake.gitState.Branches = []string{longBranch, "main"}
	fake.gitState.Worktrees = []GitWorktree{
		{Path: "/repo", Branch: longBranch, Current: true},
	}
	fake.SetProviders([]ProviderInfo{{
		ID:        "agy",
		Name:      "Antigravity",
		Available: true,
		Models: []ModelInfo{{
			ID:        "claude-sonnet-4-5",
			Name:      "Claude Sonnet 4.5",
			IsDefault: true,
		}},
	}})
	fake.threads["p1"][0].Title = longTitle
	fake.threads["p1"][0].Provider = "agy"
	fake.threads["p1"][0].Model = "claude-sonnet-4-5"

	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	SetChatQuotaWindowsForProvider(s, "agy", []ProviderQuotaWindow{
		{Label: "a@example.com · Gemini Models", UsedPercent: 42, Active: true},
		{Label: "a@example.com · Gemini Models 7d", UsedPercent: 58, Active: true},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, "agy", nil) })

	tt := ui.NewTester(root, WindowMinWidth, 700)
	waitFor(t, tt, func() bool {
		_, hasBranch := tt.Find("切换分支与同步")
		_, hasSend := tt.Find("发送")
		return hasBranch && hasSend
	})

	s.mu.Lock()
	s.sidebarCollapsed = false
	s.sidebarWidth = SidebarMinWidth
	s.threadUsage = ThreadUsage{Turns: map[int]TokenUsage{0: {ContextTokens: 20, ContextWindow: 100}}}
	s.mu.Unlock()
	inspectorSelect(s, sidePaneFileID, false)
	waitFor(t, tt, func() bool {
		_, hasInspectorTab := tt.Find("文件")
		return hasInspectorTab
	})
	// 推进时钟让右侧检查器的入场滑入动画落位，再测稳定布局坐标。
	tt.Advance(500 * time.Millisecond)
	tt.Frame()

	cardRect, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatal("未找到消息输入框")
	}
	mainLeft := cardRect.X - Space6
	mainRight := cardRect.X + cardRect.W + Space6
	mainWidth := mainRight - mainLeft

	inspectorTab, ok := tt.Find("文件")
	if !ok {
		t.Fatal("未找到检查器标签页")
	}
	actualSplitRight := inspectorTab.X - sidePaneHandleWidth
	t.Logf("main area: left=%.1f right=%.1f width=%.1f actualSplitRight=%.1f (window=%d)",
		mainLeft, mainRight, mainWidth, actualSplitRight, WindowMinWidth)

	if mainWidth < inspectorChatMinWidth-1 || mainWidth > inspectorChatMinWidth+30 {
		t.Fatalf("主区宽度 = %.1f，未接近 %v", mainWidth, inspectorChatMinWidth)
	}
	if actualSplitRight > mainRight+1 {
		t.Errorf("主区内容把检查器向右挤出：actualSplitRight=%.1f > mainRight=%.1f", actualSplitRight, mainRight)
	}
	if closeRect, ok := tt.Find("关闭右栏"); !ok || closeRect.W <= 0 || closeRect.X+closeRect.W > float32(WindowMinWidth)+0.5 {
		t.Errorf("检查器关闭按钮超出窗口或被压扁: ok=%v rect=%+v", ok, closeRect)
	}

	assertInsideMain := func(name string, rect ui.Rect) {
		t.Helper()
		if rect.W <= 0 || rect.H <= 0 {
			t.Errorf("%s 尺寸无效: %+v", name, rect)
			return
		}
		if rect.X < mainLeft-0.5 || rect.X+rect.W > mainRight+0.5 {
			t.Errorf("%s 超出主区 [%.1f, %.1f]: rect=%+v (right=%.1f)", name, mainLeft, mainRight, rect, rect.X+rect.W)
		}
	}

	// 检查顶栏右侧所有可见按钮。
	rightLabels := []string{
		"在其它应用中打开",
		"运行项目脚本",
		"提交与推送",
		"更多头部操作",
		"终端",
		"右栏",
		"更多工具",
	}
	foundAnyRight := false
	for _, label := range rightLabels {
		if rect, ok := tt.Find(label); ok {
			foundAnyRight = true
			t.Logf("header right %q: %+v", label, rect)
			assertInsideMain("顶栏右侧按钮 "+label, rect)
		}
	}
	if !foundAnyRight {
		t.Fatal("未找到任何顶栏右侧按钮")
	}

	// 检查输入框发送按钮。
	sendRect, ok := tt.Find("发送")
	if !ok {
		t.Fatal("未找到发送按钮")
	}
	t.Logf("composer send btn: %+v", sendRect)
	assertInsideMain("输入框发送按钮", sendRect)
	if sendRect.X+sendRect.W > cardRect.X+cardRect.W+0.5 {
		t.Errorf("发送按钮超出输入卡片右缘: send=%+v card=%+v", sendRect, cardRect)
	}

	// 场景 B：输入框左侧槽位存在较宽文案（如编辑排队消息胶囊）时，发送/保存按钮仍应完全在主区与卡片内。
	s.mu.Lock()
	s.queue["t1"] = []QueuedFollowUp{{ID: "q1", Text: "排队消息"}}
	s.mu.Unlock()
	s.beginEditQueued("q1")
	tt.Frame()
	saveRect, ok := tt.Find("保存修改")
	if !ok {
		t.Fatal("未找到保存修改按钮")
	}
	t.Logf("composer save-edit btn: %+v (card=%+v)", saveRect, cardRect)
	assertInsideMain("输入框保存修改按钮", saveRect)
	if saveRect.X+saveRect.W > cardRect.X+cardRect.W+0.5 {
		t.Errorf("保存修改按钮超出输入卡片右缘: save=%+v card=%+v", saveRect, cardRect)
	}
}
