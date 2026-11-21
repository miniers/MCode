package mcodeui

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestSplitRecentAndDormantProjects(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	projects := []Project{
		{ID: "p-recent", Name: "近期项目"},
		{ID: "p-boundary", Name: "边界项目"},
		{ID: "p-empty", Name: "空项目"},
		{ID: "p-zero", Name: "零时间项目"},
		{ID: "p-dormant-1", Name: "静默项目一"},
		{ID: "p-selected", Name: "选中会话项目"},
		{ID: "p-pinned", Name: "置顶会话项目"},
		{ID: "p-busy", Name: "忙碌会话项目"},
		{ID: "p-lastmsg-recent", Name: "回退时间近期项目"},
		{ID: "p-dormant-2", Name: "静默项目二"},
		{ID: "p-lastmsg-dormant", Name: "回退时间静默项目"},
	}
	threadsByProj := map[string][]Thread{
		"p-recent": {{ID: "t-recent", ProjectID: "p-recent", UpdatedAt: now.Add(-3 * 24 * time.Hour)}},
		"p-boundary": {{
			ID:        "t-boundary",
			ProjectID: "p-boundary",
			UpdatedAt: now.Add(-sidebarDormantProjectWindow),
		}},
		"p-empty": nil,
		"p-zero":  {{ID: "t-zero", ProjectID: "p-zero"}},
		"p-dormant-1": {{
			ID:        "t-dormant-1",
			ProjectID: "p-dormant-1",
			UpdatedAt: now.Add(-15 * 24 * time.Hour),
		}},
		"p-selected": {{
			ID:        "t-selected",
			ProjectID: "p-selected",
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
		}},
		"p-pinned": {{
			ID:        "t-pinned",
			ProjectID: "p-pinned",
			Pinned:    true,
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
		}},
		"p-busy": {{
			ID:        "t-busy",
			ProjectID: "p-busy",
			IsRunning: true,
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
		}},
		"p-lastmsg-recent": {{
			ID:                "t-lastmsg-recent",
			ProjectID:         "p-lastmsg-recent",
			LastUserMessageAt: now.Add(-5 * 24 * time.Hour),
		}},
		"p-dormant-2": {{
			ID:        "t-dormant-2",
			ProjectID: "p-dormant-2",
			UpdatedAt: now.Add(-20 * 24 * time.Hour),
		}},
		"p-lastmsg-dormant": {{
			ID:                "t-lastmsg-dormant",
			ProjectID:         "p-lastmsg-dormant",
			LastUserMessageAt: now.Add(-16 * 24 * time.Hour),
		}},
	}

	recent, dormant := splitRecentAndDormantProjects(
		projects,
		threadsByProj,
		"t-selected",
		func(th Thread) bool { return th.IsRunning },
		now,
	)

	var recentIDs, dormantIDs []string
	for _, p := range recent {
		recentIDs = append(recentIDs, p.ID)
	}
	for _, p := range dormant {
		dormantIDs = append(dormantIDs, p.ID)
	}

	wantRecent := []string{
		"p-recent",
		"p-boundary",
		"p-empty",
		"p-zero",
		"p-selected",
		"p-pinned",
		"p-busy",
		"p-lastmsg-recent",
	}
	wantDormant := []string{
		"p-dormant-1",
		"p-dormant-2",
		"p-lastmsg-dormant",
	}
	if !slices.Equal(recentIDs, wantRecent) {
		t.Fatalf("recent = %v, want %v", recentIDs, wantRecent)
	}
	if !slices.Equal(dormantIDs, wantDormant) {
		t.Fatalf("dormant = %v, want %v", dormantIDs, wantDormant)
	}
}

func TestSidebarDormantProjectsFoldAndExpand(t *testing.T) {
	testenv.TempHome(t)
	resetSidebarHoverCardForTest()

	fixedNow := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	prevNow := sidebarProjectNow
	sidebarProjectNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { sidebarProjectNow = prevNow })

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p-active", Name: "活跃项目"},
			{ID: "p-old", Name: "陈旧项目"},
		},
		map[string][]Thread{
			"p-active": {{
				ID:        "t-active",
				ProjectID: "p-active",
				Title:     "近期活跃会话",
				UpdatedAt: fixedNow.Add(-2 * 24 * time.Hour),
			}},
			"p-old": {{
				ID:        "t-old",
				ProjectID: "p-old",
				Title:     "二十天前会话",
				UpdatedAt: fixedNow.Add(-20 * 24 * time.Hour),
			}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("活跃项目") && tt.HasText("更多项目 (1)")
	})
	if tt.HasText("陈旧项目") || tt.HasText("二十天前会话") {
		t.Fatalf("静默项目默认应折叠进「更多项目」，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("更多项目 (1)"); err != nil {
		t.Fatalf("点击「更多项目 (1)」展开失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("陈旧项目")
	})

	if err := tt.Click("更多项目 (1)"); err != nil {
		t.Fatalf("再次点击「更多项目 (1)」收起失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("陈旧项目")
	})

	if err := tt.Click("搜索对话..."); err != nil {
		t.Fatalf("聚焦搜索框失败: %v", err)
	}
	tt.Type("陈旧")
	waitFor(t, tt, func() bool {
		return tt.HasText("陈旧项目") && !tt.HasText("更多项目 (1)")
	})
}

func TestSidebarThreadDraftIndicator(t *testing.T) {
	testenv.TempHome(t)
	resetSidebarHoverCardForTest()

	now := time.Now()
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "测试项目"}},
		map[string][]Thread{
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "选中且有草稿", UpdatedAt: now},
				{ID: "t2", ProjectID: "p1", Title: "未选中且有草稿", UpdatedAt: now.Add(-time.Minute)},
				{ID: "t3", ProjectID: "p1", Title: "未选中且无草稿", UpdatedAt: now.Add(-2 * time.Minute)},
				{ID: "t4", ProjectID: "p1", Title: "忙碌且有草稿", IsRunning: true, UpdatedAt: now.Add(-3 * time.Minute)},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("选中且有草稿") &&
			tt.HasText("未选中且有草稿") &&
			tt.HasText("未选中且无草稿") &&
			tt.HasText("忙碌且有草稿")
	})

	state.selectThread("t1")
	state.mu.Lock()
	state.draft = "当前输入框草稿"
	state.drafts["t4"] = composerDraft{Text: "忙碌会话草稿"}
	state.mu.Unlock()
	state.invalidate()
	tt.Frame()

	if _, ok := tt.Find("草稿"); ok {
		t.Fatalf("当前选中会话与忙碌会话不应显示草稿图标")
	}

	state.mu.Lock()
	state.drafts["t2"] = composerDraft{Text: "待发送草稿"}
	state.mu.Unlock()
	state.invalidate()

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("草稿")
		return ok
	})

	draftBox, ok := tt.Find("草稿")
	if !ok {
		t.Fatalf("未找到草稿图标")
	}
	t2Row, ok := tt.Find("打开会话 未选中且有草稿")
	if !ok {
		t.Fatalf("未找到 t2 会话行")
	}
	if draftBox.Y < t2Row.Y || draftBox.Y+draftBox.H > t2Row.Y+t2Row.H {
		t.Fatalf("草稿图标应位于 t2 行内: draft=%+v row=%+v", draftBox, t2Row)
	}

	// 清空草稿后，即使不额外 invalidate，下一帧也应自动移除草稿图标。
	state.mu.Lock()
	delete(state.drafts, "t2")
	state.mu.Unlock()
	tt.Frame()

	if _, ok := tt.Find("草稿"); ok {
		t.Fatalf("清空草稿后不应继续显示草稿图标")
	}

	// 仅附件草稿同样应显示草稿图标。
	state.mu.Lock()
	state.drafts["t2"] = composerDraft{
		Attachments: []composerAttachment{{ID: "att-1", Name: "note.png"}},
	}
	state.mu.Unlock()
	tt.Frame()

	if _, ok := tt.Find("草稿"); !ok {
		t.Fatalf("包含附件的未发送草稿应显示草稿图标")
	}
}
