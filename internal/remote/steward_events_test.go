package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
)

// newStewardEventBackend 构造一个带 mock harness 与 p1 项目的后端，供总管相关用例复用。
func newStewardEventBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	return b
}

// threadMessages 返回后端里某个会话当前的可见消息副本。
func threadMessages(t *testing.T, b *MemoryBackend, threadID string) []wire.ThreadMessage {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		t.Fatalf("会话 %s 不存在", threadID)
	}
	return append([]wire.ThreadMessage(nil), td.messages...)
}

// StartSession 的 Prompt 为空时只建会话不发首条消息：总管会话先建好，开场白由运行时另写。
func TestStartSession_EmptyPromptSkipsFirstMessage(t *testing.T) {
	ctx := context.Background()
	b := newStewardEventBackend(t)

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Title: "总管", Prompt: "   ",
	})
	if err != nil {
		t.Fatalf("空 Prompt 建会话失败: %v", err)
	}
	if threadID == "" {
		t.Fatal("应返回会话 ID")
	}
	thread := waitThreadIdle(t, b, ctx, threadID)
	if thread.Title != "总管" {
		t.Fatalf("标题应为 总管，得到 %q", thread.Title)
	}
	if msgs := threadMessages(t, b, threadID); len(msgs) != 0 {
		t.Fatalf("空 Prompt 不应发首条消息，实际 %d 条", len(msgs))
	}
}

// SendHostEvent 落库的用户消息 Kind 应为 host_event，客户端据此把它当事件而不是用户输入。
func TestSendHostEventMarksKind(t *testing.T) {
	ctx := context.Background()
	b := newStewardEventBackend(t)

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Title: "总管",
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	if err := b.SendHostEvent(ctx, threadID, "宿主汇总了 2 条待处理事件"); err != nil {
		t.Fatalf("SendHostEvent 失败: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	var hostEvents int
	for _, msg := range threadMessages(t, b, threadID) {
		if msg.Role != "user" {
			continue
		}
		hostEvents++
		if msg.Kind == nil || *msg.Kind != threadMessageKindHostEvent {
			t.Fatalf("宿主事件消息的 Kind 应为 %q，得到 %v", threadMessageKindHostEvent, msg.Kind)
		}
		if !strings.Contains(msg.Text, "宿主汇总了 2 条待处理事件") {
			t.Fatalf("宿主事件消息正文不对: %q", msg.Text)
		}
	}
	if hostEvents != 1 {
		t.Fatalf("应落库 1 条宿主事件消息，实际 %d 条", hostEvents)
	}
}

// SendStewardWake 落库的用户消息 Kind 应为 steward_wake，客户端据此把它当隐藏的系统输入。
func TestSendStewardWakeMarksKind(t *testing.T) {
	ctx := context.Background()
	b := newStewardEventBackend(t)

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Title: "总管",
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	if err := b.SendStewardWake(ctx, threadID, "巡检：还有 1 条待处理事件"); err != nil {
		t.Fatalf("SendStewardWake 失败: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	var wakes int
	for _, msg := range threadMessages(t, b, threadID) {
		if msg.Role != "user" {
			continue
		}
		wakes++
		if msg.Kind == nil || *msg.Kind != threadMessageKindStewardWake {
			t.Fatalf("唤醒消息的 Kind 应为 %q，得到 %v", threadMessageKindStewardWake, msg.Kind)
		}
		if !strings.Contains(msg.Text, "巡检") {
			t.Fatalf("唤醒消息正文不对: %q", msg.Text)
		}
	}
	if wakes != 1 {
		t.Fatalf("应落库 1 条唤醒消息，实际 %d 条", wakes)
	}
}

// AppendAssistantMessage 写入的是可见的最终助手回复，没有 Kind（不是事件也不是通知）。
func TestAppendAssistantMessageIsFinalReply(t *testing.T) {
	ctx := context.Background()
	b := newStewardEventBackend(t)

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Title: "总管",
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	const opening = "我是总管，你的同事。"
	if err := b.AppendAssistantMessage(threadID, opening); err != nil {
		t.Fatalf("AppendAssistantMessage 失败: %v", err)
	}

	msgs := threadMessages(t, b, threadID)
	if len(msgs) != 1 {
		t.Fatalf("应只有 1 条开场白，实际 %d 条", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "assistant" || msg.Text != opening {
		t.Fatalf("开场白应为助手消息，得到 %+v", msg)
	}
	if msg.Kind != nil {
		t.Fatalf("开场白不应带 Kind，得到 %q", *msg.Kind)
	}
	if msg.IsFinalResponse == nil || !*msg.IsFinalResponse {
		t.Fatalf("开场白应标记为最终回复，得到 %+v", msg.IsFinalResponse)
	}
}
