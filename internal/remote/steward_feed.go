package remote

import (
	"context"
	"errors"
	"log"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// StewardDesk 接收总管主会话的消息。
type StewardDesk interface {
	IsFeed(threadID string) bool
	Enqueue(threadID, messageID, text string, images []string)
}

// SetStewardDesk 注入前台消息接收器。
func (b *MemoryBackend) SetStewardDesk(desk StewardDesk) {
	b.mu.Lock()
	b.stewardDesk = desk
	b.mu.Unlock()
}

// StewardFeedThreadID 返回已缓存的前台会话 ID；首次读取时从设置恢复。
func (b *MemoryBackend) StewardFeedThreadID() string {
	b.mu.Lock()
	id, st, loaded := b.stewardFeedID, b.store, b.stewardFeedLoaded
	b.mu.Unlock()
	if loaded || st == nil {
		return id
	}
	id, _, err := st.GetSetting(context.Background(), "steward.feed_thread_id")
	if err != nil {
		return ""
	}
	b.mu.Lock()
	if !b.stewardFeedLoaded {
		b.stewardFeedID = id
		b.stewardFeedLoaded = true
	}
	id = b.stewardFeedID
	b.mu.Unlock()
	return id
}

// EnsureStewardFeedThread 创建或复用不启动 provider 的总管会话。
func (b *MemoryBackend) EnsureStewardFeedThread(ctx context.Context, projectID string) (string, error) {
	b.stewardFeedMu.Lock()
	defer b.stewardFeedMu.Unlock()
	if b.store == nil {
		return "", errors.New("会话存储未接入")
	}
	id, _, err := b.store.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil {
		return "", err
	}
	if id != "" {
		if _, err := b.store.GetThread(ctx, id); err == nil {
			b.mu.Lock()
			b.stewardFeedID = id
			b.stewardFeedLoaded = true
			b.mu.Unlock()
			return id, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	now := time.Now().UTC()
	summary := wire.ThreadSummary{ID: newUUID(), ProjectID: projectID, Title: "总管", Provider: "steward", UpdatedAt: now.Format(time.RFC3339Nano)}
	if err := b.store.CreateThread(ctx, threadToStore(summary, "", 0)); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.threads[summary.ID] = &memoryThread{summary: summary, queueRevision: 1, messagesLoaded: true, persisted: make(map[string]bool), pending: make(map[string]*pendingRequest), alwaysAllow: make(map[string]bool)}
	b.mu.Unlock()
	if err := b.store.SetSetting(ctx, "steward.feed_thread_id", summary.ID); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.stewardFeedID = summary.ID
	b.stewardFeedLoaded = true
	b.mu.Unlock()
	return summary.ID, nil
}

// ResetStewardFeedThread 归档当前总管会话并创建新的主会话，保留旧会话及其消息。
func (b *MemoryBackend) ResetStewardFeedThread(ctx context.Context, projectID string) (string, error) {
	b.stewardFeedMu.Lock()
	if b.store == nil {
		b.stewardFeedMu.Unlock()
		return "", errors.New("会话存储未接入")
	}
	oldID, _, err := b.store.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil {
		b.stewardFeedMu.Unlock()
		return "", err
	}
	if oldID != "" {
		title := "总管 · 归档 " + time.Now().UTC().Format("2006-01-02")
		archived := true
		if _, err := b.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: oldID, Title: &title, Archived: &archived}); err != nil {
			b.stewardFeedMu.Unlock()
			return "", err
		}
	}
	if err := b.store.SetSetting(ctx, "steward.feed_thread_id", ""); err != nil {
		b.stewardFeedMu.Unlock()
		return "", err
	}
	b.mu.Lock()
	b.stewardFeedID = ""
	b.stewardFeedLoaded = true
	b.mu.Unlock()
	b.stewardFeedMu.Unlock()
	return b.EnsureStewardFeedThread(ctx, projectID)
}

// AppendStewardMessage 将前台消息落库并广播。
func (b *MemoryBackend) AppendStewardMessage(ctx context.Context, threadID, role, kind, text string) error {
	_, _, err := b.appendStewardMessage(ctx, threadID, role, kind, text, nil)
	return err
}

func (b *MemoryBackend) appendStewardMessage(ctx context.Context, threadID, role, kind, text string, attachments []string) (wire.ThreadMessage, []string, error) {
	b.stewardFeedMu.Lock()
	defer b.stewardFeedMu.Unlock()
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadMessage{}, nil, store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return wire.ThreadMessage{}, nil, err
	}
	b.ensureMessagesLoadedLocked(td)
	if b.store == nil {
		b.mu.Unlock()
		return wire.ThreadMessage{}, nil, errors.New("会话存储未接入")
	}
	batch, err := b.prepareAttachmentsLocked(threadID, attachments, false, DeviceIDFromContext(ctx))
	if err != nil {
		b.mu.Unlock()
		return wire.ThreadMessage{}, nil, err
	}
	b.mu.Unlock()
	media, paths, err := batch.copy()
	if err != nil {
		return wire.ThreadMessage{}, nil, err
	}
	b.mu.Lock()
	now := time.Now().UTC()
	final := role == "assistant"
	msg := wire.ThreadMessage{ID: newUUID(), Role: role, Text: text, CreatedAt: now.Format(time.RFC3339Nano), IsFinalResponse: &final, Media: media}
	if kind != "" {
		msg.Kind = &kind
	}
	if len(td.messages) > 0 {
		msg.TurnIndex = td.messages[len(td.messages)-1].TurnIndex
	}
	b.mu.Unlock()
	saved, err := b.store.AppendMessage(ctx, messageToStore(threadID, msg))
	if err != nil {
		log.Printf("总管消息持久化失败: %v", err)
		return wire.ThreadMessage{}, nil, err
	}
	seq := int(saved.Seq)
	msg.Seq = &seq
	b.mu.Lock()
	td.messages = append(td.messages, msg)
	td.persisted[msg.ID] = true
	td.summary.UpdatedAt = msg.CreatedAt
	if role == "user" {
		td.summary.LastUserMessageAt = &msg.CreatedAt
	}
	unread := role != "user" && kind != "steward_reply" && kind != "steward_decision_answer"
	if unread {
		td.summary.HasUnread = true
	}
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	if unread {
		b.persistThreadUnread(threadID, true)
	}
	if err := b.store.TouchThread(ctx, threadID, now); err != nil {
		log.Printf("总管会话时间持久化失败: %v", err)
	}
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, summary)
	return msg, paths, nil
}

func (b *MemoryBackend) acceptStewardMessage(ctx context.Context, params wire.SendMessageParams, desk StewardDesk) (wire.SendMessageResult, error) {
	if params.ActionID != "" && b.isTombstoned(params.ActionID) {
		return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "动作已被客户端结算取消，未曾执行"}
	}
	msg, paths, err := b.appendStewardMessage(ctx, params.ThreadID, "user", "", params.Text, params.Attachments)
	if err != nil {
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID, State: wire.ActionStateFailed, ThreadID: &params.ThreadID,
				Error: &wire.ActionError{Code: wire.InternalError, Message: err.Error()},
			})
		}
		return wire.SendMessageResult{}, err
	}
	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{ActionID: params.ActionID, State: wire.ActionStateAccepted, ThreadID: &params.ThreadID, MessageID: &msg.ID})
	}
	desk.Enqueue(params.ThreadID, msg.ID, params.Text, paths)
	return wire.SendMessageResult{ActionID: params.ActionID, ThreadID: params.ThreadID, MessageID: &msg.ID}, nil
}
