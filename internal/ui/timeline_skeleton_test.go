package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSkeletonRowsReduceMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	view := func(c *ui.Context) { renderSkeletonRows(c, "test-skeleton", 3, 30, Space2) }
	reduced := ui.NewTesterWith(view, 300, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	reduced.Frame()
	if reduced.FramePending() {
		t.Fatal("减少动效时骨架仍请求帧")
	}
	appReduceMotion.Store(false)
	normal := ui.NewTester(view, 300, 200)
	normal.Advance(50 * time.Millisecond)
	if !normal.FramePending() {
		t.Fatal("骨架未开始呼吸")
	}
}

func TestLoadingSkeletonViews(t *testing.T) {
	t.Run("inbox", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		tt := ui.NewTester(func(c *ui.Context) { renderAttentionInboxPopover(c, s) }, 500, 500)
		tt.Advance(50 * time.Millisecond)
		if tt.HasText("加载中…") || !tt.FramePending() {
			t.Fatal("收件箱首次加载未显示呼吸骨架")
		}
		st := getAttentionInboxState(s)
		st.loaded = true
		st.items = []AttentionItem{{ID: "old", Title: "旧事项"}}
		st.open = false
		st.open = true
		tt.Frame()
		if !tt.HasText("旧事项") {
			t.Fatal("重新打开后未保留收件箱数据")
		}
	})
	t.Run("swarm", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = "t1"
		hp := getSwarmPanelState(s)
		hp.open, hp.loading = true, true
		tt := ui.NewTester(func(c *ui.Context) { renderSwarmPanel(c, s) }, 800, 600)
		tt.Advance(50 * time.Millisecond)
		if tt.HasText("正在加载…") || !tt.FramePending() {
			t.Fatal("Swarm 首次加载未显示呼吸骨架")
		}
		hp.loaded = true
		hp.state = SwarmState{Available: true}
		tt.Frame()
		if !tt.HasText("暂无子代理") {
			t.Fatal("刷新时未显示已有数据")
		}
	})
	t.Run("mention", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.threads = []Thread{{ID: "skeleton-thread", Title: "目标会话"}}
		key := "skeleton-preview"
		mentionPreviewMu.Lock()
		mentionPreviewOpen[key] = true
		mentionPreviewDigests["skeleton-thread"] = &mentionPreviewDigest{loading: true}
		mentionPreviewMu.Unlock()
		t.Cleanup(func() {
			mentionPreviewMu.Lock()
			delete(mentionPreviewOpen, key)
			delete(mentionPreviewDigests, "skeleton-thread")
			mentionPreviewMu.Unlock()
		})
		tt := ui.NewTester(func(c *ui.Context) {
			anchor := ui.Box(c.Key("anchor")).Size(50, 30)
			renderMentionPreview(c, s, anchor, key, mentionSegment{ThreadID: "skeleton-thread", Text: "@目标会话"})
		}, 800, 600)
		tt.Advance(50 * time.Millisecond)
		if tt.HasText("正在读取会话摘要…") || !tt.FramePending() {
			t.Fatal("会话引用首次加载未显示呼吸骨架")
		}
		cached := &mentionPreviewDigest{goal: "已缓存摘要"}
		mentionPreviewMu.Lock()
		mentionPreviewDigests["skeleton-thread"] = cached
		mentionPreviewMu.Unlock()
		startMentionPreviewLoad(s, "skeleton-thread")
		if mentionPreviewDigestOf("skeleton-thread") != cached {
			t.Fatal("重新打开时缓存被覆盖")
		}
	})
}
