// 此文件包含 Client 的消息队列方法。
package app

import (
	"context"
	"errors"
	"sync"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// Queue 调用 backend.QueueList 列出指定会话排队等待发送的消息列表。
func (c *Client) Queue(ctx context.Context, threadID string) ([]mcodeui.QueuedFollowUp, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	snap, err := c.backend.QueueList(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return FollowUpsWireToUI(snap.FollowUps), nil
}

// Enqueue 调用 backend.SendMessage 在会话运行中把消息加入排队队列。
func (c *Client) Enqueue(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        text,
		Attachments: attachmentIDs,
		ActionID:    newUUID(),
		WhenRunning: "queue",
	})
	return err
}

// RemoveQueued 调用 backend.QueueRemove 从指定会话的排队队列中移除跟进消息。
func (c *Client) RemoveQueued(ctx context.Context, threadID, followUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		ActionID:   newUUID(),
	})
	return err
}

// SendQueuedNow 调用 backend.QueueSendNow 立即从队列发送指定跟进项。
func (c *Client) SendQueuedNow(ctx context.Context, threadID, followUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		ActionID:   newUUID(),
	})
	return err
}

// MoveQueued 调用 backend.QueueMove 把排队项（连同所在 bundle）移到 beforeFollowUpID 之前，空表示移到队尾。
func (c *Client) MoveQueued(ctx context.Context, threadID, followUpID, beforeFollowUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueMove(ctx, wire.ThreadsQueueMoveParams{
		ThreadID:         threadID,
		FollowUpID:       followUpID,
		BeforeFollowUpID: beforeFollowUpID,
		ActionID:         newUUID(),
	})
	return err
}

// SetThreadMCPDisabled 调用 backend.SetThreadMCPDisabled 保存会话级外部 MCP 禁用名单。
func (c *Client) SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadMCPDisabled(ctx, threadID, keys)
	return err
}

// UpdateQueued 调用 backend.QueueUpdate 更新指定会话排队队列中跟进项的文本内容。
func (c *Client) UpdateQueued(ctx context.Context, threadID, followUpID, text string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		Text:       &text,
		ActionID:   newUUID(),
	})
	return err
}

func (c *Client) QueuedAttachments(ctx context.Context, threadID, followUpID string) ([]mcodeui.Attachment, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	queue, err := c.backend.QueueList(ctx, threadID)
	if err != nil {
		return nil, err
	}
	for _, item := range queue.FollowUps {
		if item.ID != followUpID {
			continue
		}
		var attachments []mcodeui.Attachment
		for _, att := range item.Attachments {
			size := 0
			if att.ByteCount != nil {
				size = *att.ByteCount
			}
			attachments = append(attachments, mcodeui.Attachment{
				ID: att.ID, Name: att.Name, MimeType: att.MimeType, ByteCount: size,
			})
		}
		return attachments, nil
	}
	return nil, errors.New("排队消息已派发或删除")
}

func (c *Client) UpdateQueuedWithAttachments(ctx context.Context, threadID, followUpID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	// 非 nil 空切片表示清空附件；nil 在后端表示保留原附件。
	ids := append([]string{}, attachmentIDs...)
	_, err := c.backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: followUpID, Text: &text, Attachments: ids, ActionID: newUUID(),
	})
	return err
}

// InterruptAndSend 调用 backend.QueueInterruptAndSend 中断当前轮次并在停止后优先发送或排队新消息。
func (c *Client) InterruptAndSend(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID:    threadID,
		Text:        &text,
		Attachments: attachmentIDs,
		ActionID:    newUUID(),
	})
	return err
}

// WatchQueue 调用 backend.AddQueueChangedListener 监听队列变动并转发给 onChange。
func (c *Client) WatchQueue(onChange func(threadID string, items []mcodeui.QueuedFollowUp)) (stop func()) {
	if c.backend == nil {
		return func() {}
	}
	// 后端在锁外通知，并发提交可能倒序到达，不能让旧快照覆盖新队列。
	var mu sync.Mutex
	revisions := make(map[string]int)
	return c.backend.AddQueueChangedListener(func(snap wire.ThreadsQueueSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		if snap.Revision <= revisions[snap.ThreadID] {
			return
		}
		revisions[snap.ThreadID] = snap.Revision
		if onChange != nil {
			onChange(snap.ThreadID, FollowUpsWireToUI(snap.FollowUps))
		}
	})
}
