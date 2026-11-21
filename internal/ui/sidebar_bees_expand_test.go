package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func newIdleBeesExpandSidebar(t *testing.T, threads []Thread) (*chatState, *ui.Tester) {
	t.Helper()
	ResetBeeFolded()
	ResetBeeShowAll()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		ResetBeeFolded()
		ResetBeeShowAll()
		resetSidebarHoverCardForTest()
	})

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-expand", Path: "/path/to/expand"}},
		map[string][]Thread{"p1": threads},
	)
	state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
	cs := getChatSettings(state)
	if cs == nil || !cs.cfg.Orchestration.AutoHidesIdleBees {
		t.Fatalf("期望默认开启 AutoHidesIdleBees，实际 settings=%+v", cs)
	}
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return len(threads) == 0 || tt.HasText(threads[0].Title)
	})
	if len(threads) > 0 && selectedThreadIDOf(state) != threads[0].ID {
		if err := tt.Click(threads[0].Title); err != nil {
			t.Fatalf("选中父会话 %s 失败: %v", threads[0].Title, err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == threads[0].ID })
	}
	return state, tt
}

// TestSidebarIdleBeesExpand 验证开启自动隐藏空闲子代理后，按父会话展开查看全部历史子代理的两态交互：
// (a) 全部子代理都空闲被隐藏时渲染展开触发行，点击展开后显示全部历史子代理并支持再次收起；
// (b) 部分运行中 + 部分空闲时在容器末尾显示 +N 按钮，点击后展示全部子代理并支持再次收起。
func TestSidebarIdleBeesExpand(t *testing.T) {
	t.Run("all_idle_hidden_expand_and_collapse", func(t *testing.T) {
		now := time.Now()
		state, tt := newIdleBeesExpandSidebar(t, []Thread{
			{ID: "lead-1", ProjectID: "p1", Title: "全空闲主会话", UpdatedAt: now},
			{ID: "bee-1", ProjectID: "p1", Title: "Aspen: 历史任务 1", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "completed", UpdatedAt: now.Add(-2 * time.Minute)},
			{ID: "bee-2", ProjectID: "p1", Title: "Birch: 历史任务 2", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "failed", UpdatedAt: now.Add(-1 * time.Minute)},
		})

		if IsBeeShowAll("lead-1") {
			t.Fatalf("初始不应处于手动展开全部状态")
		}
		if !tt.HasText("2 个子代理") {
			t.Fatalf("全空闲被隐藏时应显示展开触发行 '2 个子代理'，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Aspen"); ok {
			t.Fatalf("展开前不应显示已隐藏的 Aspen 头像，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); ok {
			t.Fatalf("展开前不应显示已隐藏的 Birch 头像，texts: %q", tt.Texts())
		}

		if err := tt.Click("2 个子代理"); err != nil {
			t.Fatalf("点击展开触发行失败: %v", err)
		}
		tt.Frame()

		if !IsBeeShowAll("lead-1") {
			t.Fatalf("点击后应标记为手动展开全部状态")
		}
		if _, ok := tt.Find("Aspen"); !ok {
			t.Fatalf("展开后应显示历史子代理 Aspen，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); !ok {
			t.Fatalf("展开后应显示历史子代理 Birch，texts: %q", tt.Texts())
		}
		if !tt.HasText("收起") {
			t.Fatalf("手动展开全部后应显示 '收起' 按钮，texts: %q", tt.Texts())
		}

		// 验证展开后的历史子代理可正常点击打开
		if err := tt.Click("Aspen"); err != nil {
			t.Fatalf("点击展开后的子代理 Aspen 失败: %v", err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "bee-1" })

		// 切回主会话后点击收起，恢复自动隐藏状态
		if err := tt.Click("全空闲主会话"); err != nil {
			t.Fatalf("切回主会话失败: %v", err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "lead-1" })

		if err := tt.Click("收起"); err != nil {
			t.Fatalf("点击 '收起' 按钮失败: %v", err)
		}
		tt.Frame()

		if IsBeeShowAll("lead-1") {
			t.Fatalf("点击收起后应清除手动展开全部状态")
		}
		if !tt.HasText("2 个子代理") {
			t.Fatalf("收起后应回到 '2 个子代理' 展开入口，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Aspen"); ok {
			t.Fatalf("收起后不应再显示空闲子代理 Aspen，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); ok {
			t.Fatalf("收起后不应再显示空闲子代理 Birch，texts: %q", tt.Texts())
		}
	})

	t.Run("partial_running_plus_n_expand_and_collapse", func(t *testing.T) {
		now := time.Now()
		_, tt := newIdleBeesExpandSidebar(t, []Thread{
			{ID: "lead-2", ProjectID: "p1", Title: "混合主会话", UpdatedAt: now.Add(-5 * time.Minute)},
			{ID: "bee-run", ProjectID: "p1", Title: "Cove: 运行中任务", ParentThreadID: "lead-2", IsHelper: true, IsRunning: true, UpdatedAt: now},
			{ID: "bee-idle-1", ProjectID: "p1", Title: "Aspen: 已完成任务", ParentThreadID: "lead-2", IsHelper: true, LastStatus: "completed", UpdatedAt: now.Add(-1 * time.Minute)},
			{ID: "bee-idle-2", ProjectID: "p1", Title: "Birch: 已停止任务", ParentThreadID: "lead-2", IsHelper: true, LastStatus: "stopped", UpdatedAt: now.Add(-2 * time.Minute)},
		})

		if _, ok := tt.Find("Cove"); !ok {
			t.Fatalf("运行中子代理 Cove 应可见，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Aspen"); ok {
			t.Fatalf("未展开前空闲子代理 Aspen 应隐藏，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); ok {
			t.Fatalf("未展开前空闲子代理 Birch 应隐藏，texts: %q", tt.Texts())
		}
		if !tt.HasText("+2") {
			t.Fatalf("部分可见且有隐藏时应显示 '+2' 展开按钮，texts: %q", tt.Texts())
		}

		if err := tt.Click("+2"); err != nil {
			t.Fatalf("点击 '+2' 展开按钮失败: %v", err)
		}
		tt.Frame()

		if !IsBeeShowAll("lead-2") {
			t.Fatalf("点击 '+2' 后应标记为手动展开全部状态")
		}
		for _, name := range []string{"Cove", "Aspen", "Birch"} {
			if _, ok := tt.Find(name); !ok {
				t.Fatalf("点击 '+2' 后应展示子代理 %s，texts: %q", name, tt.Texts())
			}
		}
		if tt.HasText("+2") {
			t.Fatalf("全部展开后不应再显示 '+2'，texts: %q", tt.Texts())
		}
		if !tt.HasText("收起") {
			t.Fatalf("全部展开后应显示 '收起' 按钮，texts: %q", tt.Texts())
		}

		if err := tt.Click("收起"); err != nil {
			t.Fatalf("点击 '收起' 按钮失败: %v", err)
		}
		tt.Frame()

		if IsBeeShowAll("lead-2") {
			t.Fatalf("点击收起后应清除手动展开全部状态")
		}
		if !tt.HasText("+2") {
			t.Fatalf("收起后应恢复 '+2' 入口，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Cove"); !ok {
			t.Fatalf("收起后运行中子代理 Cove 仍应可见，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Aspen"); ok {
			t.Fatalf("收起后空闲子代理 Aspen 应重新隐藏，texts: %q", tt.Texts())
		}
	})
}
