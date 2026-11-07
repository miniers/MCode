package remote

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// ConciseRepliesPrompt 是开启简洁回复引导时注入的系统提示词，语义取自简洁回复策略并改写为中文。
const ConciseRepliesPrompt = `[简洁回复与专注执行引导]
给出最简短且完整的回答，以结果为导向。使用质朴、自然的语言，省去客套套话、空泛开场白、重复总结和无意义收尾。保留关键细节、事实、不确定性、阻碍项和必要的验证结论。
做聚焦修改并复用现有模式。仅在非明显原因、约束或易踩陷阱处添加注释，不添加叙述性废话或推测性的 TODO。保留现有文档，避免无关的清理、重构与抽象。
对于前端 UI 工作，遵循既有的组件、排版与配色，保持实用结构与真实内容，兼顾无障碍与加载、空状态和错误展示。
复用证据，查阅相关内容并批量查找，避免重复扫描与回显大段日志。执行必要的校验，简练绝不代表偷工减料。一次性汇报变更、受影响的文件与关键限制。用户要求与项目规范优先。`

// AutoContinueMessage 是额度恢复后自动续跑会话时发送的跟进提示词。
const AutoContinueMessage = "Your usage quota is available again. Pick up from the last step you completed and carry the task through to the end."

// autoContinueWait 记录单个会话处于等待额度重置续跑的状态。
type autoContinueWait struct {
	token  string
	cancel context.CancelFunc
}

// isUsageLimitError 检查错误文本是否表明供应商额度或速率限制已耗尽。
// 规则覆盖常见的额度耗尽信号与 429 速率限制错误。
func isUsageLimitError(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"usage limit",
		"hit your limit",
		"usage_limit",
		"limit reached",
		"out of extra usage",
		"account's rate limit",
		"rate limit",
		"rate_limit",
		"spend limit",
		"usage credits are required",
		"quota will reset",
		"exhausted your capacity",
		"resource_exhausted",
		"resource exhausted",
		"insufficient_quota",
		"out of credits",
		"too many requests",
		"429",
		"402",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

var (
	pipeTimestampRe  = regexp.MustCompile(`\|\s*(\d{9,})`)
	clockResetRe     = regexp.MustCompile(`(?i)resets?(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)`)
	retryAfterMsRe   = regexp.MustCompile(`(?i)retry-after-ms\s*[:=]\s*(\d+)`)
	providerWaitMsRe = regexp.MustCompile(`(?i)provider requested\s+(\d+)\s*ms\s+wait`)
	textDurationRe   = regexp.MustCompile(`(?i)(?:(?:try again|retry)\s+in\s+~?\s*|resets?\s+(?:after|in)\s+)([0-9a-zA-Z .]+)`)
	unitSegmentRe    = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]+)`)
)

// parseDurationText 从文本中提取时长秒数，支持 d/h/m/s 及其缩写与复数。
func parseDurationText(text string) (time.Duration, bool) {
	matches := unitSegmentRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var totalSec float64
	found := false
	for _, m := range matches {
		val, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		unit := strings.ToLower(m[2])
		if unit == "ms" {
			continue
		}
		var scale float64
		if strings.HasPrefix(unit, "d") {
			scale = 86400
		} else if strings.HasPrefix(unit, "h") {
			scale = 3600
		} else if strings.HasPrefix(unit, "m") {
			scale = 60
		} else if strings.HasPrefix(unit, "s") {
			scale = 1
		} else {
			continue
		}
		totalSec += val * scale
		found = true
	}
	if !found || totalSec <= 0 {
		return 0, false
	}
	return time.Duration(totalSec * float64(time.Second)), true
}

// parseResetTime 解析错误文本中的重置时间与需等待时长。
// 若未解析出具体重置时间，按默认值（60 秒）返回，且 hasExplicitReset 为 false。
func parseResetTime(text string, now time.Time) (resetsAt time.Time, delay time.Duration, hasExplicitReset bool) {
	// 1. Unix 时间戳：| 1757865600
	if m := pipeTimestampRe.FindStringSubmatch(text); len(m) > 1 {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			if sec > 1e11 {
				sec /= 1000
			}
			t := time.Unix(sec, 0).In(now.Location())
			resumeAt := t.Add(60 * time.Second)
			if resumeAt.Before(now.Add(5 * time.Second)) {
				resumeAt = now.Add(5 * time.Second)
			}
			return t, resumeAt.Sub(now), true
		}
	}

	// 2. 时钟时刻：resets at 3:30 pm 或 resets 3pm
	if m := clockResetRe.FindStringSubmatch(text); len(m) > 1 {
		hour, _ := strconv.Atoi(m[1])
		minute := 0
		if m[2] != "" {
			minute, _ = strconv.Atoi(m[2])
		}
		isPM := strings.ToLower(m[3]) == "pm"
		if isPM && hour < 12 {
			hour += 12
		} else if !isPM && hour == 12 {
			hour = 0
		}

		target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !target.After(now) {
			target = target.AddDate(0, 0, 1)
		}
		resumeAt := target.Add(60 * time.Second)
		if resumeAt.Before(now.Add(5 * time.Second)) {
			resumeAt = now.Add(5 * time.Second)
		}
		return target, resumeAt.Sub(now), true
	}

	// 3. 毫秒等待：retry-after-ms 或 provider requested ms wait
	if m := retryAfterMsRe.FindStringSubmatch(text); len(m) > 1 {
		if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil && ms > 0 {
			dur := time.Duration(ms) * time.Millisecond
			target := now.Add(dur)
			resumeAt := target.Add(60 * time.Second)
			if resumeAt.Before(now.Add(5 * time.Second)) {
				resumeAt = now.Add(5 * time.Second)
			}
			return target, resumeAt.Sub(now), true
		}
	}
	if m := providerWaitMsRe.FindStringSubmatch(text); len(m) > 1 {
		if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil && ms > 0 {
			dur := time.Duration(ms) * time.Millisecond
			target := now.Add(dur)
			resumeAt := target.Add(60 * time.Second)
			if resumeAt.Before(now.Add(5 * time.Second)) {
				resumeAt = now.Add(5 * time.Second)
			}
			return target, resumeAt.Sub(now), true
		}
	}

	// 4. 自然语言时长：try again in ~158 min 或 resets after 18h31m10s
	if m := textDurationRe.FindStringSubmatch(text); len(m) > 1 {
		if dur, ok := parseDurationText(m[1]); ok {
			target := now.Add(dur)
			resumeAt := target.Add(60 * time.Second)
			if resumeAt.Before(now.Add(5 * time.Second)) {
				resumeAt = now.Add(5 * time.Second)
			}
			return target, resumeAt.Sub(now), true
		}
	}

	// 5. 未能解析出具体时间：按默认值（等待 60 秒）
	defaultWait := 60 * time.Second
	target := now.Add(defaultWait)
	return target, defaultWait, false
}

// formatResetTime 格式化重置时间，当天显示 15:04，明天显示 明天 15:04，其他显示日期时间。
func formatResetTime(target, now time.Time) string {
	targetLoc := target.In(now.Location())
	timeStr := targetLoc.Format("15:04")
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	targetDay := time.Date(targetLoc.Year(), targetLoc.Month(), targetLoc.Day(), 0, 0, 0, 0, now.Location())

	daysDiff := int(targetDay.Sub(nowDay).Hours() / 24)
	if daysDiff == 0 {
		return timeStr
	}
	if daysDiff == 1 {
		return "明天 " + timeStr
	}
	return targetLoc.Format("2006-01-02 15:04")
}

// cancelAutoContinueLocked 取消指定会话的自动续跑等待（需已持有 b.mu）。
func (b *MemoryBackend) cancelAutoContinueLocked(threadID string) {
	if wait, ok := b.autoContinueWaits[threadID]; ok {
		wait.cancel()
		delete(b.autoContinueWaits, threadID)
	}
}

// cancelAutoContinue 取消指定会话的自动续跑等待。
func (b *MemoryBackend) cancelAutoContinue(threadID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelAutoContinueLocked(threadID)
}

// scheduleAutoContinueLocked 在轮次因额度耗尽失败后排程自动续跑（需已持有 b.mu）。
func (b *MemoryBackend) scheduleAutoContinueLocked(threadID, errText string) {
	b.cancelAutoContinueLocked(threadID)

	now := time.Now()
	resetsAt, delay, _ := parseResetTime(errText, now)
	if delay < time.Second {
		delay = time.Second
	}

	whenFormatted := formatResetTime(resetsAt, now)
	noticeText := fmt.Sprintf("额度用尽，将在 %s 后自动继续", whenFormatted)

	token := newUUID()
	waitCtx, cancel := context.WithCancel(b.ctx)
	b.autoContinueWaits[threadID] = &autoContinueWait{
		token:  token,
		cancel: cancel,
	}

	go func(tID, tok string, waitDur time.Duration, ctx context.Context, noticeMsg string) {
		_ = b.AppendNotice(tID, noticeMsg)

		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDur):
		}

		b.mu.Lock()
		st, ok := b.autoContinueWaits[tID]
		if !ok || st.token != tok {
			b.mu.Unlock()
			return
		}
		delete(b.autoContinueWaits, tID)

		td, exists := b.threads[tID]
		if !exists || td.summary.IsRunning {
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()

		// 触发自动续跑发送
		_, _ = b.SendMessage(context.Background(), wire.SendMessageParams{
			ThreadID: tID,
			Text:     AutoContinueMessage,
		})
	}(threadID, token, delay, waitCtx, noticeText)
}

// isHelperThread 判断给定会话是否为辅助会话（如子会话或 Swarm bee）。
func isHelperThread(s wire.ThreadSummary) bool {
	return s.IsHelper || (s.ParentThreadID != nil && *s.ParentThreadID != "")
}

// compactSuggestionThreshold 按模型上下文窗口上限计算压缩建议的上下文占用阈值。
// window 大于 0 时取 min(100k, window*70%)，窗口未知（<=0）时返回 100k。
func compactSuggestionThreshold(window int64) int64 {
	const (
		minimumTokens int64 = 100_000
		ratioNum      int64 = 7
		ratioDen      int64 = 10
	)
	if window <= 0 {
		return minimumTokens
	}
	threshold := window * ratioNum / ratioDen
	if threshold > minimumTokens {
		return minimumTokens
	}
	return threshold
}

// ShouldOfferCompact 判断会话在恢复时是否应向用户提示压缩上下文。
// 需同时满足以下条件：
//  1. 设置项 AskCompactOnResume 开启；
//  2. 该会话非 helper 且有历史 sessionID（属于需要恢复的旧会话）；
//  3. 当前未在运行；
//  4. 上下文用量达到阈值：最新一轮用量带上下文窗口信息时，按「该轮上下文占用 >= min(100k, 窗口*70%)」判定；
//     拿不到窗口信息时退回旧口径「会话累计消耗 tokens >= 100,000」；
//  5. 距上次活动时间超过 1 小时 (3600 秒)。
func (b *MemoryBackend) ShouldOfferCompact(threadID string) bool {
	if !b.generalSettings().Session.AskCompactOnResume() {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return false
	}
	// helper 辅助会话或运行中会话跳过
	if isHelperThread(td.summary) || td.summary.IsRunning {
		return false
	}
	if td.sessionID == "" {
		return false
	}

	// 检查 token 用量：优先用最新一轮的上下文占用与模型窗口上限，拿不到时退回累计消耗口径。
	var totalTokens int64
	var contextTokens, contextWindow int64
	latestTurn := -1
	if b.usage != nil {
		if turns, ok := b.usage[threadID]; ok {
			for idx, u := range turns {
				totalTokens += u.InputTokens + u.OutputTokens
				if idx > latestTurn {
					latestTurn = idx
					contextTokens, contextWindow = u.ContextTokens, u.ContextWindow
				}
			}
		}
	}
	usedTokens := totalTokens
	var limit int64
	if contextTokens > 0 && contextWindow > 0 {
		usedTokens, limit = contextTokens, contextWindow
	}
	if usedTokens < compactSuggestionThreshold(limit) {
		return false
	}

	// 检查闲置时间是否超过 1 小时
	updatedAtStr := td.summary.UpdatedAt
	if updatedAtStr == "" {
		return false
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtStr)
	if err != nil {
		updatedAt, err = time.Parse(time.RFC3339, updatedAtStr)
		if err != nil {
			return false
		}
	}

	return b.nowLocked().Sub(updatedAt) > 3600*time.Second
}
