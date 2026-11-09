package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// stewardSectionFake 造一个两项目的假客户端：p1 放普通会话 t1，p2 放总管会话 t9。
// 总管会话比 t1 旧，所以启动时默认选中的仍是 t1，跳转断言才有意义。
func stewardSectionFake() *stewardFakeClient {
	now := time.Now()
	fake := newStewardFakeClient("")
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "普通项目", Path: "/path/to/p1"},
			{ID: "p2", Name: "总管的家", Path: "/path/to/p2"},
		},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "测试会话1", UpdatedAt: now}},
			"p2": {{ID: "t9", ProjectID: "p2", Title: "总管会话", UpdatedAt: now.Add(-time.Hour)}},
		},
	)
	fake.openID = "t9"
	fake.threadID = "t9"
	return fake
}

// 总管段把「头像 + 昵称 · 总管 + 未处理数」合成一行，点行打开总管会话。
func TestSidebarStewardSectionMergedRow(t *testing.T) {
	fake := stewardSectionFake()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("总管") && selectedThreadForTest(state) == "t1"
	})
	// 没有昵称时标签只有「总管」，不能拼成「总管 · 总管」。
	if textIndex(tt, "总管") < 0 {
		t.Fatalf("无昵称时侧栏标签应就是「总管」，当前文本: %q", tt.Texts())
	}
	if tt.HasText("总管 · 总管") {
		t.Fatalf("无昵称时不应拼出「总管 · 总管」，当前文本: %q", tt.Texts())
	}
	// 头像在昵称或头像为空时回落成显示名的第一个字，没有昵称就是「总」。
	if textIndex(tt, "总") < 0 {
		t.Fatalf("合并行应渲染圆形头像字符，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("总管"); err != nil {
		t.Fatalf("点击总管段失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t9" })
	if calls := fake.openCalls(); calls != 0 {
		t.Fatalf("会话已存在时不应创建，实际创建 %d 次", calls)
	}
}

// 昵称与头像都由客户端给出时，行内用客户端给的字。
func TestSidebarStewardSectionCustomIdentity(t *testing.T) {
	fake := stewardSectionFake()
	fake.name = "阿默"
	fake.avatar = "✦"
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("阿默 · 总管") })
	if textIndex(tt, "✦") < 0 {
		t.Fatalf("合并行应渲染客户端给的头像字符，当前文本: %q", tt.Texts())
	}
	// 有昵称时标签是「昵称 · 总管」，不再有裸的「总管」行。
	if textIndex(tt, "总管") >= 0 {
		t.Fatalf("有昵称时不应再出现默认名行，当前文本: %q", tt.Texts())
	}
}

// 有昵称、没有头像时：标签是「阿默 · 总管」，头像回落成昵称首字「阿」。
func TestSidebarStewardSectionNicknameAvatarFallback(t *testing.T) {
	fake := stewardSectionFake()
	fake.name = "阿默"
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("阿默 · 总管") })
	if textIndex(tt, "阿") < 0 {
		t.Fatalf("无头像时应用昵称首字作头像，当前文本: %q", tt.Texts())
	}
	if tt.HasText("总管 · 总管") {
		t.Fatalf("有昵称时不应拼出「总管 · 总管」，当前文本: %q", tt.Texts())
	}
}

// stewardName 与 stewardLabel 的兜底规则：昵称空（含只有空白）时回「总管」，否则带「 · 总管」后缀。
func TestStewardNameAndLabel(t *testing.T) {
	for _, tc := range []struct {
		nickname string
		name     string
		label    string
	}{
		{nickname: "", name: "总管", label: "总管"},
		{nickname: "   ", name: "总管", label: "总管"},
		{nickname: "阿默", name: "阿默", label: "阿默 · 总管"},
		{nickname: " 阿默 ", name: "阿默", label: "阿默 · 总管"},
	} {
		if got := stewardName(tc.nickname); got != tc.name {
			t.Errorf("stewardName(%q) = %q, want %q", tc.nickname, got, tc.name)
		}
		if got := stewardLabel(tc.nickname); got != tc.label {
			t.Errorf("stewardLabel(%q) = %q, want %q", tc.nickname, got, tc.label)
		}
	}
}

// 总管会话所在的项目整组不在侧栏项目树里渲染，普通项目照常显示。
func TestSidebarHidesStewardProject(t *testing.T) {
	fake := stewardSectionFake()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("普通项目") })
	if !tt.HasText("测试会话1") {
		t.Fatalf("普通项目的会话应照常显示，当前文本: %q", tt.Texts())
	}
	if tt.HasText("总管的家") {
		t.Fatalf("总管会话所在项目不应出现在项目树，当前文本: %q", tt.Texts())
	}
	if tt.HasText("总管会话") {
		t.Fatalf("总管会话本身不应在项目树里重复列出，当前文本: %q", tt.Texts())
	}
	// 侧栏只过滤本地副本：s.projects 仍保留总管项目。
	state.mu.Lock()
	kept := len(state.projects)
	state.mu.Unlock()
	if kept != 2 {
		t.Fatalf("侧栏不应修改 s.projects，期望 2 个项目，实际 %d", kept)
	}
}

// 客户端只有待处理接口、没有总管会话时，退回原来的「待处理」行，收件箱依然可达。
func TestSidebarStewardSectionFallsBackToAttentionRow(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if tt.HasText("总管") {
		t.Fatalf("没有总管会话时不应渲染总管段，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击待处理行失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("标记已处理") })
}

// 宿主转来的事件以 Role user 落库，但渲染成卡片而不是用户气泡，也不带编辑等操作。
func TestHostEventRendersAsCard(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{{
		ID:        "m-host",
		Role:      "user",
		Kind:      "host_event",
		Text:      "后台任务连续失败两次，需要你判断是否继续重试。",
		CreatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
	}})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("宿主转来") })
	if !tt.HasText("后台任务连续失败两次，需要你判断是否继续重试。") {
		t.Fatalf("卡片应显示事件正文，当前文本: %q", tt.Texts())
	}
	// 卡片正文是可选中文本，右键只会拿到框架自带的「Copy」「Select All」；用户气泡专有的
	// 操作（编辑、引用到回复等）一个都不该出现，否则说明宿主事件被当成了用户提问。
	if err := tt.RightClick("后台任务连续失败两次，需要你判断是否继续重试。"); err != nil {
		t.Fatalf("右键宿主事件卡失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	menu := tt.Menu()
	// 菜单分隔符由 Tester 记成 "-"，其余只允许文本选择自带的 Copy 与 Select All。
	allowed := map[string]bool{"Copy": true, "Select All": true, "-": true}
	hasCopy := false
	for _, item := range menu {
		if !allowed[item] {
			t.Fatalf("宿主事件卡右键菜单只应有 Copy/Select All，实际菜单项: %v", menu)
		}
		if item == "Copy" {
			hasCopy = true
		}
	}
	if !hasCopy {
		t.Fatalf("右键可选中文本应给出 Copy 菜单项，实际菜单项: %v", menu)
	}
}

// 起步胶囊只在总管会话且用户还没开口时出现，点一下就按正常发送路径发出去。
func TestStewardStarterCapsules(t *testing.T) {
	fake := stewardSectionFake()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("总管") })
	// 普通会话里不显示胶囊。
	if tt.HasText("今天发生了什么？") {
		t.Fatalf("非总管会话不应显示起步胶囊，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("总管"); err != nil {
		t.Fatalf("点击总管段失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("今天发生了什么？") })

	if err := tt.Click("今天发生了什么？"); err != nil {
		t.Fatalf("点击起步胶囊失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, call := range fake.sendCalls {
			if call.ThreadID == "t9" && call.Text == "今天发生了什么？" {
				return true
			}
		}
		return false
	})

	// 用户开口后条件不再满足，胶囊消失：重选会话会按 History 重拉一次消息。
	fake.SetMessages("t9", []Message{{
		ID:        "m-user",
		Role:      "user",
		Text:      "今天发生了什么？",
		CreatedAt: time.Now().Format(time.RFC3339),
	}})
	state.selectThread("t9")
	waitFor(t, tt, func() bool { return !tt.HasText("我想做一件事") })
}

// 唤醒消息以 Role user 落库，但不是用户自己说的话：既不算用户开口（起步胶囊照常显示），
// 也不在时间线里出现。
func TestStewardWakeDoesNotCountAsUserOpening(t *testing.T) {
	fake := stewardSectionFake()
	fake.SetMessages("t9", []Message{{
		ID:        "m-wake",
		Role:      "user",
		Kind:      stewardWakeKind,
		Text:      "宿主汇总了 2 条待处理事件。",
		CreatedAt: time.Now().Format(time.RFC3339),
	}})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("总管") })
	if err := tt.Click("总管"); err != nil {
		t.Fatalf("点击总管段失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("今天发生了什么？") })
	if !stewardWantsStarter(state) {
		t.Fatalf("唤醒消息不算用户开口，起步胶囊应继续显示")
	}
	if tt.HasText("宿主汇总了 2 条待处理事件。") {
		t.Fatalf("唤醒消息不应渲染成时间线行，当前文本: %q", tt.Texts())
	}
}

// 打开总管会话后未读点消失，切走后收到新的结果才再次显示。
func TestSidebarStewardRowUnreadDot(t *testing.T) {
	now := time.Now()
	fake := newStewardFakeClient("t9")
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "普通项目", Path: "/path/to/p1"},
			{ID: "p2", Name: "总管的家", Path: "/path/to/p2"},
		},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "测试会话1", UpdatedAt: now}},
			// 总管会话更新更近，启动即选中它，此时不应有未读点。
			"p2": {{ID: "t9", ProjectID: "p2", Title: "总管会话", HasUnread: true, UpdatedAt: now.Add(time.Minute)}},
		},
	)
	fake.openID = "t9"
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t9" && tt.HasText("总管")
	})
	if _, ok := tt.Find("未读消息"); ok {
		t.Fatal("当前选中的总管会话不应画未读点")
	}

	if err := tt.Click("测试会话1"); err != nil {
		t.Fatalf("点击普通会话失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	if _, ok := tt.Find("未读消息"); ok {
		t.Fatal("打开过的总管会话切走后不应画未读点")
	}
	state.onWatchUpdate(ThreadUpdate{
		ThreadID: "t9",
		Thread:   Thread{ID: "t9", ProjectID: "p2", Title: "总管会话", HasUnread: true, UpdatedAt: now.Add(time.Minute)},
		Messages: []Message{{ID: "m-result", Role: "assistant", Kind: "steward_result"}},
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("未读消息")
		return ok
	})
}

// stewardEnabledFakeClient 在假客户端上补一个「总管是否启用」的报告：只有实现了这个
// 可选接口，侧栏才知道该不该画未启用入口。
type stewardEnabledFakeClient struct {
	*stewardFakeClient
	enabled bool
}

func (f *stewardEnabledFakeClient) StewardEnabled() bool { return f.enabled }

// 总管还没启用时仍留一行置灰入口：标签带「未启用」，
// 点行不建会话，直接打开设置页并定位到「启用总管」那一行。
func TestSidebarStewardDisabledRowOpensSettings(t *testing.T) {
	fake := &stewardEnabledFakeClient{stewardFakeClient: newStewardFakeClient(""), enabled: false}
	fake.name = "阿默"

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("阿默 · 总管（未启用）") })
	if err := tt.Click("阿默 · 总管（未启用）"); err != nil {
		t.Fatalf("点击未启用入口失败: %v", err)
	}
	tt.Frame()

	if !st.IsOpen() {
		t.Fatal("点未启用入口应打开设置面板")
	}
	st.mu.Lock()
	page, highlight := st.currentPage, st.highlightItemID
	st.mu.Unlock()
	if page != settingsPageSteward {
		t.Fatalf("设置页应切到 %q，实际 %q", settingsPageSteward, page)
	}
	// 高亮标记会保留一会儿，它说明定位落在「启用总管」那一行；待跳转标记可能已在渲染时被消费掉。
	if highlight != "row-steward-enabled" {
		t.Fatalf("设置页应定位并高亮 row-steward-enabled，实际 %q", highlight)
	}
	if calls := fake.openCalls(); calls != 0 {
		t.Fatalf("点未启用入口不应创建会话，实际创建 %d 次", calls)
	}
}

// 总管已启用但会话还在创建时保持原状：不画未启用入口，免得闪一行误导文案。
func TestSidebarStewardNoDisabledRowWhenEnabled(t *testing.T) {
	fake := &stewardEnabledFakeClient{stewardFakeClient: newStewardFakeClient(""), enabled: true}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("未启用") {
		t.Fatalf("总管已启用时不应画未启用入口，当前文本: %q", tt.Texts())
	}
}
