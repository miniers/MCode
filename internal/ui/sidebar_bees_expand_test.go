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

// TestSidebarIdleBeesExpand 验证开启自动隐藏空闲子代理后，按父会话展开查看全部历史子代理的交互：
// (a) 全部子代理都空闲且父会话选中时直接展开全部子代理头像，支持点击连接线折叠为摘要行与再次展开；
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
			t.Fatalf("选中全空闲父会话直接展开时不应写入手动 ShowAll 状态")
		}
		if tt.HasText("2 个子代理") {
			t.Fatalf("选中全空闲父会话后不应再显示 '2 个子代理' 摘要行，texts: %q", tt.Texts())
		}
		for _, txt := range tt.Texts() {
			if txt == "收起" {
				t.Fatalf("全空闲父会话直接展开头像条时不应显示 '收起' 按钮，texts: %q", tt.Texts())
			}
		}
		if _, ok := tt.Find("Aspen"); !ok {
			t.Fatalf("选中全空闲父会话后应直接显示 Aspen 头像，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); !ok {
			t.Fatalf("选中全空闲父会话后应直接显示 Birch 头像，texts: %q", tt.Texts())
		}

		// 验证展开后的历史子代理可正常点击打开，且另一个空闲子代理仍保持可见
		if err := tt.Click("Aspen"); err != nil {
			t.Fatalf("点击展开后的子代理 Aspen 失败: %v", err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "bee-1" })
		if _, ok := tt.Find("Birch"); !ok {
			t.Fatalf("选中子代理 Aspen 后 Birch 仍应保持可见，texts: %q", tt.Texts())
		}

		// 切回主会话后点击连接线可手动折叠为 '2 个子代理'，再点击可重新展开
		if err := tt.Click("全空闲主会话"); err != nil {
			t.Fatalf("切回主会话失败: %v", err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "lead-1" })

		if err := tt.Click("收起子会话"); err != nil {
			t.Fatalf("点击连接线折叠按钮失败: %v", err)
		}
		tt.Advance(DurSlow + 50*time.Millisecond)
		tt.Frame()

		if !IsBeeFolded("lead-1") {
			t.Fatalf("点击连接线后应标记为折叠状态")
		}
		if !tt.HasText("2 个子代理") {
			t.Fatalf("折叠后应显示 '2 个子代理' 摘要行，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Aspen"); ok {
			t.Fatalf("折叠后不应再显示空闲子代理 Aspen，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); ok {
			t.Fatalf("折叠后不应再显示空闲子代理 Birch，texts: %q", tt.Texts())
		}

		if err := tt.Click("2 个子代理"); err != nil {
			t.Fatalf("点击 '2 个子代理' 重新展开失败: %v", err)
		}
		tt.Advance(DurSlow + 50*time.Millisecond)
		tt.Frame()

		if IsBeeFolded("lead-1") {
			t.Fatalf("点击 '2 个子代理' 后应恢复展开状态")
		}
		if _, ok := tt.Find("Aspen"); !ok {
			t.Fatalf("重新展开后应显示 Aspen，texts: %q", tt.Texts())
		}
		if _, ok := tt.Find("Birch"); !ok {
			t.Fatalf("重新展开后应显示 Birch，texts: %q", tt.Texts())
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

// TestSidebarIdleBeesHiddenUntilSelected 验证「侧边栏自动隐藏空闲子代理」开启时的完整隐藏与展开生命周期：
// (a) 未选中且子代理全部空闲时，既不显示 "2 个子代理" 也不显示子代理头像；
// (b) 点击父会话标题选中后直接展开全部子代理头像，且点击其中某个子代理后其余头像保持可见；
// (c) 选中态可点击连接线折叠为 "2 个子代理"，再点击重新展开；
// (d) 切换到另一个会话后，原空闲父会话的子代理头像与 "2 个子代理" 自动隐藏；
// (e) 过渡结束后不再持续请求下一帧，且 ReduceMotion 下即时完成。
func TestSidebarIdleBeesHiddenUntilSelected(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		name := "normal_motion"
		if reduced {
			name = "reduce_motion"
		}
		t.Run(name, func(t *testing.T) {
			ResetBeeFolded()
			ResetBeeShowAll()
			resetSidebarHoverCardForTest()
			setAppReduceMotion(t, reduced)
			t.Cleanup(func() {
				ResetBeeFolded()
				ResetBeeShowAll()
				resetSidebarHoverCardForTest()
			})

			now := time.Now()
			threads := []Thread{
				{ID: "other-1", ProjectID: "p1", Title: "另一个主会话", UpdatedAt: now},
				{ID: "lead-1", ProjectID: "p1", Title: "全空闲主会话", UpdatedAt: now.Add(-time.Minute)},
				{ID: "bee-1", ProjectID: "p1", Title: "Aspen: 历史任务 1", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "completed", UpdatedAt: now.Add(-3 * time.Minute)},
				{ID: "bee-2", ProjectID: "p1", Title: "Birch: 历史任务 2", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "failed", UpdatedAt: now.Add(-2 * time.Minute)},
			}
			fake := NewFakeClient()
			fake.SetProjectsAndThreads(
				[]Project{{ID: "p1", Name: "mcode-expand", Path: "/path/to/expand"}},
				map[string][]Thread{"p1": threads},
			)
			state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
			t.Cleanup(state.close)
			tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{
				Preferences: ui.Preferences{ReduceMotion: reduced},
			})
			waitFor(t, tt, func() bool {
				return tt.HasText("另一个主会话") && tt.HasText("全空闲主会话")
			})
			if selectedThreadIDOf(state) != "other-1" {
				if err := tt.Click("另一个主会话"); err != nil {
					t.Fatalf("选中 other-1 失败: %v", err)
				}
				waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "other-1" })
			}
			tt.Move(0, 0)
			if !reduced {
				tt.Advance(DurSlow + 200*time.Millisecond)
			}
			tt.Frame()

			// (a) 未选中且全部空闲：不显示 "2 个子代理" 也不显示头像
			if tt.HasText("2 个子代理") {
				t.Fatalf("未选中全空闲父会话时不应显示 '2 个子代理'，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Aspen"); ok {
				t.Fatalf("未选中全空闲父会话时不应显示 Aspen，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Birch"); ok {
				t.Fatalf("未选中全空闲父会话时不应显示 Birch，texts: %q", tt.Texts())
			}

			// (b) 点击父会话标题选中后直接展开全部头像，且点选 Aspen 后 Birch 保持可见
			if err := tt.Click("全空闲主会话"); err != nil {
				t.Fatalf("点击全空闲主会话失败: %v", err)
			}
			waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "lead-1" })
			tt.Move(0, 0)
			tt.Advance(DurSlow + 200*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatalf("展开过渡结束后不应持续请求下一帧")
			}
			if tt.HasText("2 个子代理") {
				t.Fatalf("选中全空闲父会话后不应显示 '2 个子代理'，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Aspen"); !ok {
				t.Fatalf("选中父会话后应直接显示 Aspen，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Birch"); !ok {
				t.Fatalf("选中父会话后应直接显示 Birch，texts: %q", tt.Texts())
			}

			if err := tt.Click("Aspen"); err != nil {
				t.Fatalf("点击子代理 Aspen 失败: %v", err)
			}
			waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "bee-1" })
			tt.Move(0, 0)
			tt.Frame()
			if _, ok := tt.Find("Birch"); !ok {
				t.Fatalf("选中 Aspen 后 Birch 仍应保持可见，texts: %q", tt.Texts())
			}

			// (c) 切回父会话选中态，点击连接线折叠为 "2 个子代理"，再点击重新展开
			if err := tt.Click("全空闲主会话"); err != nil {
				t.Fatalf("切回全空闲主会话失败: %v", err)
			}
			waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "lead-1" })
			if err := tt.Click("收起子会话"); err != nil {
				t.Fatalf("点击连接线收起子会话失败: %v", err)
			}
			tt.Move(0, 0)
			tt.Advance(DurSlow + 200*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatalf("折叠过渡结束后不应持续请求下一帧")
			}
			if !tt.HasText("2 个子代理") {
				t.Fatalf("折叠后应显示 '2 个子代理'，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Aspen"); ok {
				t.Fatalf("折叠后不应再显示 Aspen，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Birch"); ok {
				t.Fatalf("折叠后不应再显示 Birch，texts: %q", tt.Texts())
			}

			if err := tt.Click("2 个子代理"); err != nil {
				t.Fatalf("点击 '2 个子代理' 展开失败: %v", err)
			}
			tt.Move(0, 0)
			tt.Advance(DurSlow + 200*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatalf("重新展开过渡结束后不应持续请求下一帧")
			}
			if _, ok := tt.Find("Aspen"); !ok {
				t.Fatalf("重新展开后应显示 Aspen，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Birch"); !ok {
				t.Fatalf("重新展开后应显示 Birch，texts: %q", tt.Texts())
			}

			// (d) 切换到另一个会话后，原空闲父会话的子代理头像与 "2 个子代理" 均自动隐藏
			if err := tt.Click("另一个主会话"); err != nil {
				t.Fatalf("切换到另一个主会话失败: %v", err)
			}
			waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "other-1" })
			tt.Move(0, 0)
			tt.Advance(DurSlow + 200*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatalf("切换会话收起过渡结束后不应持续请求下一帧")
			}
			if tt.HasText("2 个子代理") {
				t.Fatalf("切走后不应显示 '2 个子代理'，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Aspen"); ok {
				t.Fatalf("切走后不应显示 Aspen，texts: %q", tt.Texts())
			}
			if _, ok := tt.Find("Birch"); ok {
				t.Fatalf("切走后不应显示 Birch，texts: %q", tt.Texts())
			}
		})
	}

	t.Run("reduce_motion_immediate_no_pending_frame", func(t *testing.T) {
		ResetBeeFolded()
		ResetBeeShowAll()
		resetSidebarHoverCardForTest()
		setAppReduceMotion(t, true)
		t.Cleanup(func() {
			ResetBeeFolded()
			ResetBeeShowAll()
			resetSidebarHoverCardForTest()
		})

		now := time.Now()
		threads := []Thread{
			{ID: "other-1", ProjectID: "p1", Title: "另一个主会话", UpdatedAt: now},
			{ID: "lead-1", ProjectID: "p1", Title: "全空闲主会话", UpdatedAt: now.Add(-time.Minute)},
			{ID: "bee-1", ProjectID: "p1", Title: "Aspen: 历史任务 1", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "completed", UpdatedAt: now.Add(-3 * time.Minute)},
			{ID: "bee-2", ProjectID: "p1", Title: "Birch: 历史任务 2", ParentThreadID: "lead-1", IsHelper: true, LastStatus: "failed", UpdatedAt: now.Add(-2 * time.Minute)},
		}
		fake := NewFakeClient()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "mcode-expand", Path: "/path/to/expand"}},
			map[string][]Thread{"p1": threads},
		)
		state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
		t.Cleanup(state.close)
		tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		waitFor(t, tt, func() bool {
			return tt.HasText("另一个主会话") && tt.HasText("全空闲主会话")
		})
		// 不通过鼠标悬停切会话，避免 sidebar_hover_card 宽限期干扰 ReduceMotion 的零帧等待断言
		state.selectThread("lead-1")
		tt.Frame()
		if tt.FramePending() {
			t.Fatalf("ReduceMotion 下展开头像条不应请求下一帧")
		}
		if _, ok := tt.Find("Aspen"); !ok {
			t.Fatalf("ReduceMotion 下选中父会话后应立即显示 Aspen，texts: %q", tt.Texts())
		}

		ToggleBeeFold("lead-1")
		state.invalidate()
		tt.Frame()
		if tt.FramePending() {
			t.Fatalf("ReduceMotion 下折叠头像条不应请求下一帧")
		}
		if !tt.HasText("2 个子代理") {
			t.Fatalf("ReduceMotion 下折叠后应立即显示 '2 个子代理'，texts: %q", tt.Texts())
		}

		ToggleBeeFold("lead-1")
		state.invalidate()
		tt.Frame()
		if tt.FramePending() {
			t.Fatalf("ReduceMotion 下重新展开头像条不应请求下一帧")
		}
		if _, ok := tt.Find("Aspen"); !ok {
			t.Fatalf("ReduceMotion 下重新展开应立即显示 Aspen，texts: %q", tt.Texts())
		}

		state.selectThread("other-1")
		tt.Frame()
		if tt.FramePending() {
			t.Fatalf("ReduceMotion 下切走隐藏头像条不应请求下一帧")
		}
		if _, ok := tt.Find("Aspen"); ok || tt.HasText("2 个子代理") {
			t.Fatalf("ReduceMotion 下切走应立即隐藏头像条与摘要行，texts: %q", tt.Texts())
		}
	})
}
