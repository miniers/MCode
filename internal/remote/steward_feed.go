package remote

import (
	"context"
	"errors"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// StewardDesk 接收总管主会话的消息。
type StewardDesk interface {
	IsFeed(threadID string) bool
	Enqueue(threadID, text string, images []string)
}

// SetStewardDesk 注入前台消息接收器。
func (b *MemoryBackend) SetStewardDesk(desk StewardDesk) {
	b.mu.Lock()
	b.stewardDesk = desk
	b.mu.Unlock()
}

// EnsureStewardFeedThread 创建或复用不启动 provider 的总管会话。
func (b *MemoryBackend) EnsureStewardFeedThread(ctx context.Context, projectID string) (string, error) {
	b.stewardFeedMu.Lock()
	defer b.stewardFeedMu.Unlock()
	if b.store == nil {
		return "", errors.New("会话存储未接入")
	}
	id, _, err := b.store.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil { return "", err }
	if id != "" {
		if _, err := b.store.GetThread(ctx, id); err == nil { return id, nil } else if !errors.Is(err, store.ErrNotFound) { return "", err }
	}
	now := time.Now().UTC()
	summary := wire.ThreadSummary{ID: newUUID(), ProjectID: projectID, Title: "总管", Provider: "steward", UpdatedAt: now.Format(time.RFC3339Nano)}
	if err := b.store.CreateThread(ctx, threadToStore(summary, "", 0)); err != nil { return "", err }
	b.mu.Lock()
	b.threads[summary.ID] = &memoryThread{summary: summary, queueRevision: 1, messagesLoaded: true, persisted: make(map[string]bool), pending: make(map[string]*pendingRequest), alwaysAllow: make(map[string]bool)}
	b.mu.Unlock()
	if err := b.store.SetSetting(ctx, "steward.feed_thread_id", summary.ID); err != nil { return "", err }
	return summary.ID, nil
}

// AppendStewardMessage 将前台消息落库并广播。
func (b *MemoryBackend) AppendStewardMessage(ctx context.Context, threadID, role, kind, text string) error {
	_, _, err := b.appendStewardMessage(ctx, threadID, role, kind, text, nil)
	return err
}

func (b *MemoryBackend) appendStewardMessage(ctx context.Context, threadID, role, kind, text string, attachments []string) (wire.ThreadMessage, []string, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok { b.mu.Unlock(); return wire.ThreadMessage{}, nil, store.ErrNotFound }
	if err := b.ensureMessagesLocked(td, threadID); err != nil { b.mu.Unlock(); return wire.ThreadMessage{}, nil, err }
	b.ensureMessagesLoadedLocked(td)
	media, paths, err := b.consumeAttachmentsLocked(threadID, attachments, false, DeviceIDFromContext(ctx))
	if err != nil { b.mu.Unlock(); return wire.ThreadMessage{}, nil, err }
	now := time.Now().UTC()
	final := role == "assistant"
	msg := wire.ThreadMessage{ID: newUUID(), Role: role, Text: text, CreatedAt: now.Format(time.RFC3339Nano), IsFinalResponse: &final, Media: media}
	if kind != "" { msg.Kind = &kind }
	if len(td.messages) > 0 { msg.TurnIndex = td.messages[len(td.messages)-1].TurnIndex }
	if b.store == nil { b.mu.Unlock(); return wire.ThreadMessage{}, nil, errors.New("会话存储未接入") }
	saved, err := b.store.AppendMessage(ctx, messageToStore(threadID, msg))
	if err != nil { b.mu.Unlock(); return wire.ThreadMessage{}, nil, err }
	seq := int(saved.Seq)
	msg.Seq = &seq
	td.messages = append(td.messages, msg)
	td.persisted[msg.ID] = true
	td.summary.UpdatedAt = msg.CreatedAt
	if role == "user" { td.summary.LastUserMessageAt = &msg.CreatedAt }
	unread := role != "user" && kind != "steward_reply" && kind != "steward_decision_answer"
	if unread { td.summary.HasUnread = true }
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	_ = b.store.TouchThread(ctx, threadID, now)
	if unread { _ = b.store.SetThreadUnread(ctx, threadID, true) }
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, summary)
	return msg, paths, nil
}

func (b *MemoryBackend) acceptStewardMessage(ctx context.Context, params wire.SendMessageParams, desk StewardDesk) (wire.SendMessageResult, error) {
	if params.ActionID != "" && b.isTombstoned(params.ActionID) { return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "动作已被客户端结算取消，未曾执行"} }
	msg, paths, err := b.appendStewardMessage(ctx, params.ThreadID, "user", "", params.Text, params.Attachments)
	if err != nil { return wire.SendMessageResult{}, err }
	if params.ActionID != "" { b.actions.record(wire.ActionStatusResult{ActionID: params.ActionID, State: wire.ActionStateAccepted, ThreadID: &params.ThreadID, MessageID: &msg.ID}) }
	desk.Enqueue(params.ThreadID, params.Text, paths)
	return wire.SendMessageResult{ActionID: params.ActionID, ThreadID: params.ThreadID, MessageID: &msg.ID}, nil
}
