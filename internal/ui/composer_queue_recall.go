package mcodeui

import (
	"fmt"
	"strings"
)

// recallAllQueuedDoneHook 在「全部退回编辑」的后台 goroutine 结束时调用。
// 正式运行始终为 nil；测试用它确定性地等待退回完成，避免依赖 sleep。
var recallAllQueuedDoneHook func()

// queuedIntentLabel 返回排队项的意图标签；系统消息不显示标签。
// Intent 为 insert 表示用户本意是插入当前轮次，但后端不支持而退回了排队。
func queuedIntentLabel(item QueuedFollowUp) string {
	if item.System {
		return ""
	}
	if item.Intent == "insert" {
		return "插入未生效"
	}
	return "排队"
}

// mergeQueuedDraftText 把多条排队消息按顺序并入草稿，消息之间用空行分隔，
// 并去掉草稿与结果末尾多余的空白行。
func mergeQueuedDraftText(draft string, texts []string) string {
	parts := make([]string, 0, len(texts)+1)
	if trimmed := strings.TrimRight(draft, " \t\r\n"); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, text := range texts {
		if trimmed := strings.TrimRight(text, " \t\r\n"); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

// queuedRecallBlockedReason 判断「全部退回编辑」是否被阻断，返回给用户看的原因；空串表示可以执行。
// 只统计非系统条目：系统消息既不退回也不计入附件上限。
func queuedRecallBlockedReason(items []QueuedFollowUp, currentAttachments int, supportsAttachments bool, limit int) string {
	recallable := 0
	attachmentCount := 0
	for _, item := range items {
		if item.System {
			continue
		}
		recallable++
		attachmentCount += item.AttachmentCount
	}
	if recallable == 0 {
		return "没有可退回的排队消息"
	}
	if attachmentCount > 0 && !supportsAttachments {
		return "当前客户端不支持退回带附件的排队消息"
	}
	if currentAttachments+attachmentCount > limit {
		return fmt.Sprintf("退回后附件将超过 %d 个上限", limit)
	}
	return ""
}

// queuedThreadProvider 返回判断「插入当前轮次」能力时使用的 provider。
// 与会话选择逻辑保持一致：找不到会话时回退到 pendingThread。
func (s *chatState) queuedThreadProvider(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider := ""
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			provider = s.threads[i].Provider
			break
		}
	}
	if provider == "" && s.pendingThread != nil {
		provider = s.pendingThread.Thread.Provider
	}
	return provider
}
