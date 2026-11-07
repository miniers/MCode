package mcodeui

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// errorRetryTarget 描述「重试」要重新发送的内容：错误之前最近一条用户消息的正文与附件。
type errorRetryTarget struct {
	text  string
	media []MessageMedia
}

// available 判断这条目标是否值得显示「重试」：有正文或有附件即可。
func (t errorRetryTarget) available() bool {
	return strings.TrimSpace(t.text) != "" || len(t.media) > 0
}

// errorRetryTargetFor 找出错误消息之前最近一条用户消息，返回它的正文与附件列表。
// 正文为空但带附件的用户消息也算找到，这样只发了图片的消息出错后仍能重试。
func errorRetryTargetFor(messages []Message, errorID string) errorRetryTarget {
	end := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ID == errorID {
			end = i
			break
		}
	}
	for i := end - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		text := messages[i].Text
		media := messages[i].Media
		if strings.TrimSpace(text) == "" && len(media) == 0 {
			continue
		}
		return errorRetryTarget{text: text, media: media}
	}
	return errorRetryTarget{}
}

// errorRetryMediaCandidates 返回本次重试要重新上传的附件：超过单条消息附件上限的部分直接截掉。
func errorRetryMediaCandidates(media []MessageMedia) []MessageMedia {
	if len(media) <= maxAttachments {
		return media
	}
	return media[:maxAttachments]
}

// errorRetryMediaFits 判断取回的原图能否重新上传：空内容或超过单文件 20MiB 上限都不行。
func errorRetryMediaFits(size int) bool {
	return size > 0 && size <= maxAttachmentBytes
}

// errorRetryMediaMeta 决定重新上传用的文件名与 MIME 类型：依次取读取接口返回的、消息里记录的、
// 按文件名与内容嗅探的，最后兜底为二进制流。
func errorRetryMediaMeta(media MessageMedia, readMimeType string, data []byte) (string, string) {
	name := strings.TrimSpace(media.Name)
	if name == "" {
		name = "附件"
	}
	mimeType := strings.TrimSpace(readMimeType)
	if mimeType == "" {
		mimeType = strings.TrimSpace(media.MimeType)
	}
	if mimeType == "" {
		if detected, ok := detectAttachmentMime(name, data); ok {
			mimeType = detected
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return name, mimeType
}

// errorRetryInFlight 记录正在重新上传附件、还没发出去的「重试」，避免重复点击叠加多次发送。
var (
	errorRetryMu       sync.Mutex
	errorRetryInFlight = make(map[string]bool)
)

// beginErrorRetry 尝试把这条错误标记为「重试中」，已经在重试时返回 false。
func beginErrorRetry(messageID string) bool {
	if messageID == "" {
		return false
	}
	errorRetryMu.Lock()
	defer errorRetryMu.Unlock()
	if errorRetryInFlight[messageID] {
		return false
	}
	errorRetryInFlight[messageID] = true
	return true
}

// errorRetryInProgress 读取这条错误是否正在重试，用于把按钮显示成进行中。
func errorRetryInProgress(messageID string) bool {
	if messageID == "" {
		return false
	}
	errorRetryMu.Lock()
	defer errorRetryMu.Unlock()
	return errorRetryInFlight[messageID]
}

// finishErrorRetry 清除「重试中」标记并请求重绘，让按钮恢复可点击。
func finishErrorRetry(s *chatState, messageID string) {
	if messageID != "" {
		errorRetryMu.Lock()
		delete(errorRetryInFlight, messageID)
		errorRetryMu.Unlock()
	}
	if s != nil {
		s.invalidate()
	}
}

// retryErrorMessage 点「继续」时发送一条从中断处往下做的提示，而不是把上一条用户消息重发一遍。
// 重发用户消息会把已经完成的那部分工作丢掉，同一份上下文也要再喂一遍。
func retryErrorMessage(s *chatState, m Message) {
	if s == nil || m.ID == "" || s.client == nil {
		return
	}
	if !beginErrorRetry(m.ID) {
		// 已经在发（自动重试刚刚到点，或者用户连点了两下）：不重复发送。
		return
	}
	s.invalidate()
	s.sendMessage(continuationPromptFor(m))
	finishErrorRetry(s, m.ID)
}

// continuationPrompt 是「继续」发出的正文：让 agent 从中断处往下做，不要从头重来。
const continuationPrompt = "继续从中断处往下做，不要从头重来。上一轮因为出错中断了。"

// continuationPromptCapacity 是模型满载/限流中断时用的正文，自动退避重试也发这一条。
const continuationPromptCapacity = "继续从中断处往下做，不要从头重来。上一轮因为模型满载中断了。"

// continuationPromptFor 按中断原因选继续提示的文案：满载/限流说清是被模型挡住的。
func continuationPromptFor(m Message) string {
	switch kind, _ := classifyMessageNotice(m); kind {
	case providerNoticeRateLimit, providerNoticeQuota:
		return continuationPromptCapacity
	}
	return continuationPrompt
}

// capacityRetryState 记录一条满载/限流错误的自动重试进度。
type capacityRetryState struct {
	threadID    string
	messageID   string
	attempts    int       // 已经自动重试过几次
	nextAt      time.Time // 下一次重试的时间点；零值表示还没开始计时
	ladderUntil time.Time // 退避阶梯的有效期：期内出现的下一条满载错误接着往上走
	cancelled   bool      // 用户点了取消，这条错误不再自动重试
}

// capacityRetryLadderWindow 是退避阶梯的有效期：一次退避阶梯总共只等 155 秒，
// 留 5 分钟的窗口足够把「重试又满载」的几条新错误连成一条阶梯。
const capacityRetryLadderWindow = 5 * time.Minute

// capacityRetryMaxAttempts 是自动重试次数上限：满载通常几十秒到几分钟恢复，
// 再久就该让用户自己决定，而不是一直悄悄重发。
const capacityRetryMaxAttempts = 5

// capacityRetryLadderTotal 是整条退避阶梯的总等待（5+10+20+40+80 秒）：
// 重置提示比这更久（按小时、按天或具体时刻）时，等完阶梯也恢复不了，不如让用户手动继续。
const capacityRetryLadderTotal = 155 * time.Second

// providerUsageWindowRe 匹配「hit your ... limit」这类按额度窗口计的重置提示：
// Claude 的 5 小时窗口、每周窗口都要等窗口滚动，不是几十秒的退避阶梯能等到的。
var providerUsageWindowRe = regexp.MustCompile(`(?i)hit\s+your\s+(?:[\w-]+\s+)*limit`)

// capacityRetryDelay 返回第 attempt 次（从 0 开始）自动重试前的等待：5s、10s、20s、40s、80s。
func capacityRetryDelay(attempt int) time.Duration {
	const lastAttempt = capacityRetryMaxAttempts - 1
	if attempt < 0 {
		attempt = 0
	}
	if attempt > lastAttempt {
		// 上限之外的调用不再继续翻倍，避免位移溢出。
		attempt = lastAttempt
	}
	return 5 * time.Second << attempt
}

// capacityRetryDue 判断这次自动重试是否已经到点。
func capacityRetryDue(st *capacityRetryState, now time.Time) bool {
	return st != nil && !st.cancelled && !st.nextAt.IsZero() && !now.Before(st.nextAt)
}

// capacityRetryWorthwhile 判断等完退避阶梯有没有希望恢复：重置提示明确比阶梯总时长还久、
// 命中按额度窗口计的提示，或者纯额度耗尽，都不值得自动重试。
func capacityRetryWorthwhile(kind providerNoticeKind, text string) bool {
	if hint := parseProviderResetHint(text); hint != "" {
		// 解析失败说明提示是具体时刻或按天计（3d、3:00 pm、01-02 15:04），都等不到。
		d, err := time.ParseDuration(hint)
		return err == nil && d <= capacityRetryLadderTotal
	}
	if providerUsageWindowRe.MatchString(text) {
		return false
	}
	if kind == providerNoticeQuota {
		return false
	}
	return true
}

// capacityRetryEligible 判断这条错误是否适合自动重试：只有满载/限流类、
// 必须是会话里最新的一条，而且当前没有在跑（否则等于插一条消息进正在跑的回合）；
// 重置时间超出退避阶梯总时长的也不试，额度窗口要几小时才恢复，自动重发没有意义。
func capacityRetryEligible(m Message, isLast, running bool) bool {
	if !isLast || running || m.ID == "" {
		return false
	}
	kind, _ := classifyMessageNotice(m)
	if kind != providerNoticeRateLimit && kind != providerNoticeQuota {
		return false
	}
	return capacityRetryWorthwhile(kind, m.Text)
}

var (
	capacityRetryMu     sync.Mutex
	capacityRetryStates = make(map[*chatState]*capacityRetryState)
)

// getCapacityRetryState 获取或初始化会话的自动重试进度，调用方需持有 capacityRetryMu。
func getCapacityRetryState(s *chatState) *capacityRetryState {
	st, ok := capacityRetryStates[s]
	if !ok {
		st = &capacityRetryState{}
		capacityRetryStates[s] = st
	}
	return st
}

// tickCapacityRetry 驱动满载/限流错误的自动重试：返回还要等多久、已经重试几次、是否正在等待。
// now 用渲染帧的时钟（ui.Context.Now），无头测试里 Advance 才能把倒计时推下去。
// 到点就在这里发一条「继续」，到次数上限或已被取消就不再重试。
// 调用方只负责按 remaining 登记一次重绘，不另起计时协程。
func (s *chatState) tickCapacityRetry(now time.Time, m Message, isLast, running bool) (remaining time.Duration, attempts int, armed bool) {
	if s == nil || m.ID == "" {
		return 0, 0, false
	}
	threadID := s.chatSelectedThreadID()

	capacityRetryMu.Lock()
	st := getCapacityRetryState(s)
	if st.messageID != m.ID || st.threadID != threadID {
		// 换了一条错误（含切会话）：取消只对原来那条有效；上一轮退避还在阶梯期内时
		// 沿用已经重试的次数，让 5s、10s、20s 的节奏跨过「重试又满载」的新错误继续往上走。
		carry := 0
		if !st.cancelled && st.attempts > 0 && now.Before(st.ladderUntil) {
			carry = st.attempts
		}
		ladderUntil := st.ladderUntil
		*st = capacityRetryState{threadID: threadID, messageID: m.ID, attempts: carry, ladderUntil: ladderUntil}
	}
	if !capacityRetryEligible(m, isLast, running) {
		// 不再是最后一条，或者会话又在跑：停止计时，等下次重新判断。
		st.nextAt = time.Time{}
		attempts = st.attempts
		capacityRetryMu.Unlock()
		return 0, attempts, false
	}
	if st.cancelled || st.attempts >= capacityRetryMaxAttempts {
		attempts = st.attempts
		capacityRetryMu.Unlock()
		return 0, attempts, false
	}
	if st.nextAt.IsZero() {
		// 第一次看到这条满载错误：等一个 5 秒起步的间隔再自动重试。
		st.nextAt = now.Add(capacityRetryDelay(st.attempts))
		if st.ladderUntil.IsZero() {
			st.ladderUntil = now.Add(capacityRetryLadderWindow)
		}
	}
	if !capacityRetryDue(st, now) {
		remaining, attempts = st.nextAt.Sub(now), st.attempts
		capacityRetryMu.Unlock()
		return remaining, attempts, true
	}
	st.attempts++
	attempts = st.attempts
	if st.attempts >= capacityRetryMaxAttempts {
		st.nextAt = time.Time{}
	} else {
		st.nextAt = now.Add(capacityRetryDelay(st.attempts))
	}
	capacityRetryMu.Unlock()
	// 自动重试和点「继续」走同一条发送路径，只是发起时机不同。
	// 发送成功后 composer 会在下一帧渲染输入框时清掉草稿占用标记，但自动重试不经过输入框，
	// 连续到点（或激进的退避阶梯）时上一帧的标记还在，prepareOutgoingMessageLocked
	// 会直接拒发，阶梯就断在第二次。所以发前先把残留标记按掉。
	s.clearSentDraftGate()
	s.sendMessage(continuationPromptFor(m))
	return 0, attempts, false
}

// clearSentDraftGate 清掉上一次发送留下的草稿占用标记（composer 正常在渲染时清）。
func (s *chatState) clearSentDraftGate() {
	s.mu.Lock()
	s.sentDraft = nil
	s.mu.Unlock()
}

// cancelCapacityRetry 记录用户取消了这条错误的自动重试：取消后不再自动重发，直到换一条错误。
func (s *chatState) cancelCapacityRetry(m Message) {
	if s == nil || m.ID == "" {
		return
	}
	threadID := s.chatSelectedThreadID()
	capacityRetryMu.Lock()
	st := getCapacityRetryState(s)
	st.messageID = m.ID
	st.threadID = threadID
	st.cancelled = true
	st.nextAt = time.Time{}
	st.ladderUntil = time.Time{}
	capacityRetryMu.Unlock()
	s.invalidate()
}

// capacityRetrySeconds 把剩余等待换算成界面上显示的正整数秒。
func capacityRetrySeconds(remaining time.Duration) int {
	if remaining <= 0 {
		return 0
	}
	secs := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return secs
}
