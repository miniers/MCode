package mcodeui

import (
	"testing"
)

// TestContinuationOrigin_ForwardedThread 覆盖情况 (a)：
// 转发会话标题与源相同、ForkedFromThreadID 指向源、ForkedFromMessageID 为空时，
// continuationStartInfoFor 返回 Label '继续自'、SourceTitle 为源标题、TargetThreadID 为源 ID。
func TestContinuationOrigin_ForwardedThread(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "src-1", ProjectID: "p1", Title: "原始会话标题"},
		{ID: "fwd-1", ProjectID: "p1", Title: "原始会话标题", ForkedFromThreadID: "src-1", ForkedFromMessageID: ""},
	}
	s.mu.Unlock()

	info, ok := continuationStartInfoFor(s, "fwd-1")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if info.Label != "继续自" {
		t.Errorf("Label = %q, want %q", info.Label, "继续自")
	}
	if info.SourceTitle != "原始会话标题" {
		t.Errorf("SourceTitle = %q, want %q", info.SourceTitle, "原始会话标题")
	}
	if info.TargetThreadID != "src-1" {
		t.Errorf("TargetThreadID = %q, want %q", info.TargetThreadID, "src-1")
	}
}

// TestContinuationOrigin_ForkedThread 覆盖情况 (b)：
// ForkedFromMessageID 非空时 Label 为 '分叉自'。
func TestContinuationOrigin_ForkedThread(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "src-1", ProjectID: "p1", Title: "原始会话标题"},
		{ID: "fork-1", ProjectID: "p1", Title: "原始会话标题", ForkedFromThreadID: "src-1", ForkedFromMessageID: "msg-1"},
	}
	s.mu.Unlock()

	info, ok := continuationStartInfoFor(s, "fork-1")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if info.Label != "分叉自" {
		t.Errorf("Label = %q, want %q", info.Label, "分叉自")
	}
	if info.SourceTitle != "原始会话标题" {
		t.Errorf("SourceTitle = %q, want %q", info.SourceTitle, "原始会话标题")
	}
	if info.TargetThreadID != "src-1" {
		t.Errorf("TargetThreadID = %q, want %q", info.TargetThreadID, "src-1")
	}
}

// TestContinuationOrigin_LegacyTitlePrefix 覆盖情况 (c)：
// 旧标题 'Continued: X' 且没有字段时仍返回 '继续自' 和 X。
func TestContinuationOrigin_LegacyTitlePrefix(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "src-1", ProjectID: "p1", Title: "旧会话标题"},
		{ID: "legacy-1", ProjectID: "p1", Title: "Continued: 旧会话标题"},
	}
	s.mu.Unlock()

	info, ok := continuationStartInfoFor(s, "legacy-1")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if info.Label != "继续自" {
		t.Errorf("Label = %q, want %q", info.Label, "继续自")
	}
	if info.SourceTitle != "旧会话标题" {
		t.Errorf("SourceTitle = %q, want %q", info.SourceTitle, "旧会话标题")
	}
	if info.TargetThreadID != "src-1" {
		t.Errorf("TargetThreadID = %q, want %q", info.TargetThreadID, "src-1")
	}
}

// TestContinuationOrigin_WithForkOriginItem 覆盖情况 (d)：
// withForkOriginItem 对转发会话不插入 timelineItemForkOrigin，对分支会话仍然插入。
func TestContinuationOrigin_WithForkOriginItem(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "src-1", ProjectID: "p1", Title: "原会话"},
		{ID: "fwd-1", ProjectID: "p1", Title: "原会话", ForkedFromThreadID: "src-1", ForkedFromMessageID: ""},
		{ID: "fork-1", ProjectID: "p1", Title: "原会话", ForkedFromThreadID: "src-1", ForkedFromMessageID: "msg-1"},
	}
	s.mu.Unlock()

	sampleItems := []timelineItem{{kind: timelineItemSingle}}

	// 转发会话：不插入 timelineItemForkOrigin
	s.mu.Lock()
	s.selectedThreadID = "fwd-1"
	s.mu.Unlock()
	fwdItems := s.withForkOriginItem(sampleItems)
	for _, it := range fwdItems {
		if it.kind == timelineItemForkOrigin {
			t.Fatalf("withForkOriginItem 对转发会话不应插入 timelineItemForkOrigin")
		}
	}
	if len(fwdItems) != len(sampleItems) {
		t.Fatalf("withForkOriginItem 对转发会话改变了条目数量: got %d, want %d", len(fwdItems), len(sampleItems))
	}

	// 分支会话：仍然插入 timelineItemForkOrigin
	s.mu.Lock()
	s.selectedThreadID = "fork-1"
	s.mu.Unlock()
	forkItems := s.withForkOriginItem(sampleItems)
	if len(forkItems) == 0 || forkItems[0].kind != timelineItemForkOrigin {
		t.Fatalf("withForkOriginItem 对分支会话应插入 timelineItemForkOrigin，got %+v", forkItems)
	}
	if forkItems[0].forkFromThreadID != "src-1" {
		t.Fatalf("timelineItemForkOrigin forkFromThreadID = %q, want %q", forkItems[0].forkFromThreadID, "src-1")
	}
}

// TestContinuationOrigin_SourceNotInThreads 覆盖情况 (e)：
// 源会话不在列表里时 TargetThreadID 为空。
func TestContinuationOrigin_SourceNotInThreads(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	st := getForwardedContextState(s)
	st.mu.Lock()
	st.loaded["fwd-deleted-src"] = true
	st.contexts["fwd-deleted-src"] = ForwardedContext{SourceTitle: "已不在列表的原会话"}
	st.mu.Unlock()

	s.mu.Lock()
	s.threads = []Thread{
		{
			ID:                  "fwd-deleted-src",
			ProjectID:           "p1",
			Title:               "新转发会话标题",
			ForkedFromThreadID:  "deleted-src-id",
			ForkedFromMessageID: "",
		},
	}
	s.mu.Unlock()

	info, ok := continuationStartInfoFor(s, "fwd-deleted-src")
	if !ok {
		t.Fatalf("源会话不在列表但有转发上下文时仍应返回卡片信息")
	}
	if info.Label != "继续自" {
		t.Errorf("Label = %q, want %q", info.Label, "继续自")
	}
	if info.SourceTitle != "已不在列表的原会话" {
		t.Errorf("SourceTitle = %q, want %q", info.SourceTitle, "已不在列表的原会话")
	}
	if info.TargetThreadID != "" {
		t.Errorf("源会话不在列表里时 TargetThreadID 应为空，got %q", info.TargetThreadID)
	}
}
