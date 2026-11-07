package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestComposerQueueCollapsedRowAndExpand 覆盖 D-14：
// 1. 三条排队消息时默认只显示 28pt 折叠行「排队 3」和首条摘要，看不到第二、三条；
// 2. 点开折叠行后三条和原有操作按钮（编辑/删除/立即发送）都按原 Label 出现。
func TestComposerQueueCollapsedRowAndExpand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "第一条排队跟进"},
		{ID: "q2", Text: "第二条排队跟进"},
		{ID: "q3", Text: "第三条排队跟进"},
	})

	// 折叠态：只剩数量、首条摘要和展开箭头。
	waitFor(t, tt, func() bool {
		return tt.HasText("排队 3") &&
			tt.HasText("第一条排队跟进") &&
			!tt.HasText("第二条排队跟进")
	})
	// HasText 是子串匹配，会命中顶部权限胶囊的「自动接受编辑 · 改文件不询问，其他操作询问」；
	// 排队项按钮的 Label 是整段文字，这里用精确匹配确认折叠态没有渲染它们。
	if hasExactText(tt, "编辑") || hasExactText(tt, "删除") || hasExactText(tt, "立即发送") {
		t.Fatalf("折叠态不应渲染排队项按钮，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("排队 3"); err != nil {
		t.Fatalf("click 排队 3 failed: %v", err)
	}

	// 展开态：三条都可见，原有按钮 Label 不变。
	waitFor(t, tt, func() bool {
		return tt.HasText("第二条排队跟进") &&
			tt.HasText("第三条排队跟进") &&
			hasExactText(tt, "编辑") &&
			hasExactText(tt, "删除") &&
			hasExactText(tt, "立即发送")
	})
}

// TestQueueCollapsedSummary 覆盖折叠行摘要的边界：空文本与只有附件时给出可用提示。
func TestQueueCollapsedSummary(t *testing.T) {
	cases := []struct {
		name  string
		items []QueuedFollowUp
		want  string
	}{
		{name: "无排队项", items: nil, want: ""},
		{name: "普通文本", items: []QueuedFollowUp{{Text: "改一下标题"}}, want: "改一下标题"},
		{name: "空文本仅有附件", items: []QueuedFollowUp{{AttachmentCount: 2}}, want: "2 个附件"},
		{name: "空文本无附件", items: []QueuedFollowUp{{}}, want: "（空消息）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queueCollapsedSummary(tc.items); got != tc.want {
				t.Fatalf("queueCollapsedSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestComposerDropOverlayPillFollowsDragState 覆盖 D-9：拖拽悬停时出现「松开以添加附件」，
// 松开或离开后淡出移除；覆盖层不参与点击，只有提示文案。
func TestComposerDropOverlayPillFollowsDragState(t *testing.T) {
	s := &chatState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("composer-drop-overlay-test-root")).Fill().Children(func() {
			renderComposerDropOverlay(c, s)
		})
	}, 600, 300)

	tt.Frame()
	if tt.HasText("松开以添加附件") {
		t.Fatalf("没有拖拽时不应出现覆盖层药丸，当前文本: %q", tt.Texts())
	}

	setComposerFileDragOver(s, true)
	// 测试时钟不会自己前进：先建出带动画的覆盖层节点，再推进一帧淡入时长。
	tt.Frame()
	tt.Advance(motionFast)
	waitFor(t, tt, func() bool {
		return tt.HasText("松开以添加附件")
	})

	setComposerFileDragOver(s, false)
	tt.Frame()
	tt.Advance(motionFast)
	waitFor(t, tt, func() bool {
		return !tt.HasText("松开以添加附件")
	})
}

// TestShouldShowTimelineSkeleton 表驱动覆盖 D-18 的显示条件。
func TestShouldShowTimelineSkeleton(t *testing.T) {
	cases := []struct {
		name        string
		hasMessages bool
		loading     bool
		firstEnter  bool
		want        bool
	}{
		{name: "首次进入且历史加载中", hasMessages: false, loading: true, firstEnter: true, want: true},
		{name: "已有消息", hasMessages: true, loading: true, firstEnter: true, want: false},
		{name: "加载完成", hasMessages: false, loading: false, firstEnter: true, want: false},
		{name: "再次进入同一会话", hasMessages: false, loading: true, firstEnter: false, want: false},
		{name: "三个条件都不满足", hasMessages: false, loading: false, firstEnter: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldShowTimelineSkeleton(tc.hasMessages, tc.loading, tc.firstEnter); got != tc.want {
				t.Fatalf("shouldShowTimelineSkeleton(%v, %v, %v) = %v, want %v",
					tc.hasMessages, tc.loading, tc.firstEnter, got, tc.want)
			}
		})
	}
}

// TestTimelineSkeletonObserve 验证以 messagesRev 增长判定历史落地，以及加载失败时的超时兜底。
func TestTimelineSkeletonObserve(t *testing.T) {
	now := time.Now()
	st := &timelineSkeletonState{seenThreads: map[string]bool{}}

	if !st.observe("t1", 7, now) {
		t.Fatal("首次进入且 rev 未变时应视为加载中")
	}
	if !st.observe("t1", 7, now.Add(time.Second)) {
		t.Fatal("rev 未变且未超时仍应视为加载中")
	}
	if st.observe("t1", 8, now.Add(2*time.Second)) {
		t.Fatal("rev 增长后应视为加载完成")
	}
	if st.observe("t1", 8, now.Add(3*time.Second)) {
		t.Fatal("已落地过的会话不应重新进入加载中")
	}

	st2 := &timelineSkeletonState{seenThreads: map[string]bool{}}
	if !st2.observe("t2", 1, now) {
		t.Fatal("新会话首次观测应视为加载中")
	}
	if st2.observe("t2", 1, now.Add(timelineSkeletonMaxWait+time.Second)) {
		t.Fatal("加载失败导致 rev 不动时，超过上限应收起骨架")
	}
}
