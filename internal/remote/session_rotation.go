package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// SessionRotationInfo 是询问换新钩子时提供的会话现状。
type SessionRotationInfo struct {
	ThreadID      string
	Idle          time.Duration // 距上一轮结束的时间，未知为 0
	ContextTokens int64         // 最近一轮的 context_tokens，未知为 0
	// Manual 为真表示用户主动要求分割：钩子应无条件同意，并给出分割版前言；
	// 这一轮不写自动换新提示，也不把最近几条对话或整段历史回放给 provider。
	Manual bool
}

const (
	// splitSessionKind 是分割线系统消息的 Kind：它既是时间线上可见的一道线，
	// 也是「下一轮从新会话开始」的持久标记（重启后依然有效）。
	splitSessionKind = "split_session"
	// splitSessionText 是分割线的文案。
	splitSessionText = "已分割 · 以上历史不再进入上下文"
)

// 分割请求的可识别错误，由远程协议映射成 ErrCodeOperationRejected。
var (
	// ErrSplitRunning 表示本线程有回合正在跑，此时不允许分割。
	ErrSplitRunning = errors.New("remote: turn is running")
	// ErrSplitEmpty 表示会话里还没有任何用户或助手消息，分割没有意义。
	ErrSplitEmpty = errors.New("remote: thread is empty")
)

// lastSplitIndex 返回历史里最后一条分割线的下标，没有分割线时为 -1。
func lastSplitIndex(history []wire.ThreadMessage) int {
	last := -1
	for i, msg := range history {
		if msg.Role == "system" && msg.Kind != nil && *msg.Kind == splitSessionKind {
			last = i
		}
	}
	return last
}

// pendingSplit 报告历史是否处于「已分割、还没有新消息」的待分割状态：
// 存在分割线，且最后一条分割线之后没有用户或助手消息。
func pendingSplit(history []wire.ThreadMessage) bool {
	last := lastSplitIndex(history)
	if last < 0 {
		return false
	}
	for _, msg := range history[last+1:] {
		if msg.Role == "user" || msg.Role == "assistant" {
			return false
		}
	}
	return true
}

// historyAfterLastSplit 返回最后一条分割线之后的消息；没有分割线时原样返回。
// 所有把历史回放给 provider 的地方都先过一遍，保证分割线以上的内容不会被送回模型。
func historyAfterLastSplit(history []wire.ThreadMessage) []wire.ThreadMessage {
	last := lastSplitIndex(history)
	if last < 0 {
		return history
	}
	return history[last+1:]
}

// SessionRotator 判定本轮是否丢掉 provider 侧旧会话、改开新会话；rotate 为 true 时
// preamble 放在新会话本轮 Prompt 的最前面。
type SessionRotator func(ctx context.Context, info SessionRotationInfo) (rotate bool, preamble string)

// sessionRotatorTimeout 是单次询问换新钩子的时限，超时视为不换新；测试可调小。
var sessionRotatorTimeout = 2 * time.Second

// 换新会话时随本轮带上的最近对话：条数、单条与总量上限（按 rune 计）。
const (
	recentConversationCount        = 6
	recentConversationMessageRunes = 2400
	recentConversationTotalRunes   = 8000
)

// SetSessionRotator 设置换新会话钩子：每轮本要续接 provider 会话时，在锁外询问一次；
// nil 表示从不换新。
func (b *MemoryBackend) SetSessionRotator(fn SessionRotator) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionRotator = fn
}

// sessionRotationInfoLocked 汇总询问钩子用的会话现状：Idle 以本轮之前最后一条消息的时间
// 近似上一轮结束时刻，ContextTokens 取本轮之前最近一轮的用量。调用方需持有 b.mu。
func (b *MemoryBackend) sessionRotationInfoLocked(threadID string, turnIndex int, priorHistory []wire.ThreadMessage) SessionRotationInfo {
	info := SessionRotationInfo{ThreadID: threadID}

	var lastAt time.Time
	for _, msg := range priorHistory {
		at, err := time.Parse(time.RFC3339Nano, msg.CreatedAt)
		if err != nil {
			continue
		}
		if at.After(lastAt) {
			lastAt = at
		}
	}
	if !lastAt.IsZero() {
		if idle := b.nowLocked().Sub(lastAt); idle > 0 {
			info.Idle = idle
		}
	}

	latestIndex := -1
	for idx, u := range b.usage[threadID] {
		if idx < turnIndex && idx > latestIndex {
			latestIndex = idx
			info.ContextTokens = u.ContextTokens
		}
	}
	return info
}

// callSessionRotator 带时限询问换新钩子；超时、上下文取消或钩子 panic 一律视为不换新。
func callSessionRotator(ctx context.Context, fn SessionRotator, info SessionRotationInfo) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, sessionRotatorTimeout)
	defer cancel()

	type result struct {
		rotate   bool
		preamble string
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("session rotator panic for thread %s: %v", info.ThreadID, r)
				done <- result{}
			}
		}()
		rotate, preamble := fn(ctx, info)
		done <- result{rotate: rotate, preamble: preamble}
	}()

	select {
	case r := <-done:
		// 时限已过才到的答复同样作废，避免与超时分支竞争出不同结果。
		if ctx.Err() != nil {
			return false, ""
		}
		return r.rotate, r.preamble
	case <-ctx.Done():
		log.Printf("session rotator for thread %s gave no answer: %v", info.ThreadID, ctx.Err())
		return false, ""
	}
}

// rotateProviderSession 丢掉线程保存的 provider 会话 ID（内存与数据库），保证本轮与重启后都不再续接旧会话。
// 返回线程的转发上下文供新会话重新送达；线程已不在跑本轮或会话已变化时不动并返回 false。
func (b *MemoryBackend) rotateProviderSession(td *memoryThread, threadID, turnID, sessionID string) (string, bool) {
	b.mu.Lock()
	if b.threads[threadID] != td || td.runTurnID != turnID || td.sessionID != sessionID {
		b.mu.Unlock()
		return "", false
	}
	td.sessionID = ""
	// 本轮若没能建起新会话，下一轮按历史交接补上下文；本轮成功后由运行收尾清除。
	td.needsHandoff = true
	// 新会话里没有转发上下文与首轮指导词，需要重新送达。
	td.continuationSent = false
	delete(b.guidanceDelivered, threadID)
	continuation := td.continuation
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadSession(pCtx, threadID, ""); err != nil {
			log.Printf("store clear thread session %s error: %v", threadID, err)
		}
		pCancel()
	}
	return continuation, true
}

// buildRecentConversationPrompt 把最近几条可见对话原文拼在 prompt 前面，供换新后的 provider 会话接上话头：
// 最多 recentConversationCount 条，单条正文不超过 recentConversationMessageRunes，
// 合计不超过 recentConversationTotalRunes，从新往旧填，超出总量的更早消息直接舍弃。
func buildRecentConversationPrompt(history []wire.ThreadMessage, prompt string) string {
	transcript := handoffTranscript(history)
	if len(transcript) > recentConversationCount {
		transcript = transcript[len(transcript)-recentConversationCount:]
	}
	budget := recentConversationTotalRunes
	var lines []string
	for i := len(transcript) - 1; i >= 0; i-- {
		line := handoffTranscriptLine(transcript[i], recentConversationMessageRunes, false)
		n := len([]rune(line))
		if n > budget {
			break
		}
		budget -= n
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return prompt
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	text := `[mcode: recent conversation]
The previous agent session for this chat was closed. The latest messages follow verbatim, with tool calls left out. They are background only, not a new request.

` + strings.Join(lines, "\n\n") + "\n\n[mcode: end of recent conversation]"
	if prompt == "" {
		return text
	}
	return text + "\n\n" + prompt
}

// sessionRotationNotice 生成换新会话后留在会话里的系统提示，例如「已换新会话：空闲 2 小时，上下文 13 万」。
func sessionRotationNotice(info SessionRotationInfo) string {
	var parts []string
	if info.Idle > 0 {
		parts = append(parts, "空闲 "+formatRotationIdle(info.Idle))
	}
	if info.ContextTokens > 0 {
		parts = append(parts, "上下文 "+formatRotationTokens(info.ContextTokens))
	}
	if len(parts) == 0 {
		return "已换新会话"
	}
	return "已换新会话：" + strings.Join(parts, "，")
}

func formatRotationIdle(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
	}
}

func formatRotationTokens(n int64) string {
	if n < 10000 {
		return fmt.Sprintf("%d tokens", n)
	}
	return fmt.Sprintf("%d 万", (n+5000)/10000)
}
