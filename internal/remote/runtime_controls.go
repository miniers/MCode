package remote

import (
	"context"

	"mcode/internal/remote/wire"
)

type queueComposeEdit struct {
	followUpID string
	stashed    wire.RuntimeDraft
}

// QueueEdit 借出队列条目而不移除 slot，保存或取消时返回原草稿。
func (b *MemoryBackend) QueueEdit(ctx context.Context, p wire.QueueEditParams) (wire.QueueEditResult, error) {
	b.mu.Lock()
	td, ok := b.threads[p.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.QueueEditResult{}, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "thread not found"}
	}
	if p.Action == "begin" {
		for _, fu := range td.followUps {
			if fu.ID != p.FollowUpID {
				continue
			}
			if td.composeEdit == nil {
				if p.Draft == nil {
					b.mu.Unlock()
					return wire.QueueEditResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "draft is required"}
				}
				stash := *p.Draft
				stash.Attachments = append([]string{}, stash.Attachments...)
				td.composeEdit = &queueComposeEdit{stashed: stash}
			}
			td.composeEdit = &queueComposeEdit{followUpID: fu.ID, stashed: td.composeEdit.stashed}
			draft := wire.RuntimeDraft{Text: fu.Text, Attachments: []string{}}
			for _, att := range fu.Attachments {
				draft.Attachments = append(draft.Attachments, att.ID)
			}
			b.mu.Unlock()
			return wire.QueueEditResult{ThreadID: p.ThreadID, FollowUpID: fu.ID, Draft: draft}, nil
		}
		b.mu.Unlock()
		return wire.QueueEditResult{}, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "follow-up not found"}
	}
	edit := td.composeEdit
	if edit == nil || (p.Action != "save" && p.Action != "cancel") {
		b.mu.Unlock()
		return wire.QueueEditResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "no active queue edit or invalid action"}
	}
	found := false
	for _, fu := range td.followUps {
		found = found || fu.ID == edit.followUpID
	}
	if p.Action == "save" && found {
		if p.Draft == nil {
			b.mu.Unlock()
			return wire.QueueEditResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "draft is required"}
		}
		b.mu.Unlock()
		if _, err := b.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
			ThreadID: p.ThreadID, FollowUpID: edit.followUpID, Text: &p.Draft.Text, Attachments: p.Draft.Attachments,
		}); err != nil {
			return wire.QueueEditResult{}, err
		}
		b.mu.Lock()
	}
	if td.composeEdit == edit {
		td.composeEdit = nil
	}
	b.mu.Unlock()
	return wire.QueueEditResult{ThreadID: p.ThreadID, Draft: edit.stashed, Vanished: !found}, nil
}

func (b *MemoryBackend) LimitHandoffText(ctx context.Context, threadID string) (wire.LimitTextResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return wire.LimitTextResult{}, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "thread not found"}
	}
	// C-ST-07：交接文本取自最近一轮的内存消息，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return wire.LimitTextResult{}, err
	}
	b.touchThreadLocked(td)
	var messages []wire.ThreadMessage
	if len(td.messages) > 0 {
		index := td.messages[len(td.messages)-1].TurnIndex
		for _, msg := range td.messages {
			if msg.TurnIndex == index {
				messages = append(messages, msg)
			}
		}
	}
	return wire.LimitTextResult{ThreadID: threadID, Text: BuildLimitHandoffText(messages, td.summary.Title)}, nil
}

func (b *MemoryBackend) Compact(ctx context.Context, threadID string) (wire.SendMessageResult, error) {
	// 压缩前先把已有对话同步到 Nowledge：压缩之后早期消息可能不再出现在会话历史里。
	b.syncNowledgeThread(threadID, nowledgeSyncCompactTimeout)
	return b.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "/compact", ActionID: newUUID()})
}

func (b *MemoryBackend) publishCompactSuggestion(threadID string) {
	if !b.ShouldOfferCompact(threadID) {
		return
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	offer := wire.CompactSuggestion{ThreadID: threadID, IdleSince: td.summary.UpdatedAt}
	for _, usage := range b.usage[threadID] {
		offer.UsedTokens += usage.InputTokens + usage.OutputTokens
	}
	b.mu.Unlock()
	b.hub.Notify(threadID, "threads.compactSuggestion", offer)
}
