package mcodeui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// providerNoticeKind 供应商提示的分类，决定横幅的标题、图标与配色。
// 与 timeline_rows.go 里的 noticeKind（普通系统通知的通用分类）互不相关：
// 这里只认限流、额度耗尽、登录失效与上下文已满这四类需要用户干预的提示；
// 前三类可由原文关键词识别，上下文已满只能由后端上报的失败类别识别。
type providerNoticeKind int

const (
	providerNoticeNone      providerNoticeKind = iota // 未命中，按原有样式渲染
	providerNoticeRateLimit                           // 触发限流：429、请求过于频繁
	providerNoticeQuota                               // 额度耗尽：quota、余额不足、额度用完
	providerNoticeAuth                                // 未登录或认证失败：401、需要重新登录
	providerNoticeContext                             // 上下文已满：需要新建会话或压缩上下文
)

// 与 internal/harness 的 ErrKind* 常量取值一致；ui 不依赖 harness 包，这里按字符串对齐。
const (
	errKindAuthRequired     = "auth_required"
	errKindRateLimited      = "rate_limited"
	errKindContextExhausted = "context_exhausted"
)

var (
	// providerNoticeKeywords 按 providerNoticeRateLimit、providerNoticeQuota、providerNoticeAuth 的顺序匹配
	// （小写文本子串匹配）。顺序上先认限流再认额度：多数供应商的 429 原文里同时出现 limit 字样，
	// 归类为限流更贴近用户的实际处理动作（等一会儿再发）。
	providerNoticeKeywords = []struct {
		kind     providerNoticeKind
		keywords []string
	}{
		{providerNoticeRateLimit, []string{
			"rate limit", "rate_limit", "ratelimit", "too many requests", "throttl",
			"请求过于频繁", "请求太频繁", "限流", "限速",
		}},
		{providerNoticeQuota, []string{
			"quota", "usage limit", "usage_limit", "limit reached", "hit your limit",
			"insufficient", "out of credits", "credit balance", "spend limit", "exhausted your capacity",
			"额度", "余额不足", "配额",
		}},
		{providerNoticeAuth, []string{
			"unauthorized", "not logged in", "login required", "authentication", "invalid api key",
			"invalid_api_key", "/login", "please log in", "not signed in", "sign in again",
			"请先登录", "未登录", "登录已失效", "重新登录", "认证失败",
		}},
	}

	// providerNoticeStatusRe 匹配裸状态码（401），要求左右不是数字，避免把长数字串里的片段当状态码。
	providerNoticeStatusRe = regexp.MustCompile(`(?:^|[^0-9])(401|429)(?:[^0-9]|$)`)

	// providerResetDurationRe 匹配英文时长提示：resets in 2h30m / try again in 30s / retry in ~5 minutes。
	providerResetDurationRe = regexp.MustCompile(`(?i)(?:reset(?:s)?\s+(?:in|after)|try\s+again\s+in|retry\s+in|available\s+in)\s*~?\s*([0-9][0-9a-zA-Z .,]*)`)
	// providerResetCNRe 匹配中文时长提示：30 秒后重试 / 2 小时后重置 / 3 天后恢复。
	providerResetCNRe = regexp.MustCompile(`([0-9]+)\s*(秒|分钟|小时|天)\s*(?:后|以后|之后)`)
	// providerResetClockRe 匹配具体重置时刻：resets at 3:00 pm / 重置时间 15:30。
	providerResetClockRe = regexp.MustCompile(`(?i)(?:reset(?:s)?\s+(?:at|@)|重置(?:时间|于|于北京时间|时刻))\s*[:：]?\s*([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)?)`)
	// providerResetEpochRe 匹配供应商原文里的 Unix 时间戳（「| 1757865600」，毫秒时间戳也兼容）。
	providerResetEpochRe = regexp.MustCompile(`\|\s*([0-9]{10,13})`)
	// providerResetUnitRe 拆出时长片段里的数值与单位，单位按长到短排列，避免 minutes 被 m 抢先匹配。
	providerResetUnitRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*(milliseconds?|minutes?|mins?|seconds?|secs?|hours?|hrs?|days?|ms|m|s|h|d)`)
)

// providerNoticeUnit 把时长单位归一为 d/h/m/s；不是可识别单位时返回空串。
func providerNoticeUnit(raw string) string {
	unit := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(unit, "day"), unit == "d":
		return "d"
	case strings.HasPrefix(unit, "hour"), unit == "h", unit == "hr", unit == "hrs":
		return "h"
	case strings.HasPrefix(unit, "min"), unit == "m":
		return "m"
	case strings.HasPrefix(unit, "sec"), unit == "s":
		return "s"
	}
	return ""
}

// parseProviderResetHint 从提示原文里解析重置（或可重试）时机，返回给用户看的简短提示；
// 解析不到时返回空串。优先级：时长 > 具体时刻 > Unix 时间戳。
func parseProviderResetHint(text string) string {
	if text == "" {
		return ""
	}
	if m := providerResetDurationRe.FindStringSubmatch(text); len(m) > 1 {
		if hint := compactProviderDuration(m[1]); hint != "" {
			return hint
		}
	}
	if m := providerResetCNRe.FindStringSubmatch(text); len(m) > 2 {
		switch m[2] {
		case "秒":
			return m[1] + "s"
		case "分钟":
			return m[1] + "m"
		case "小时":
			return m[1] + "h"
		case "天":
			return m[1] + "d"
		}
	}
	if m := providerResetClockRe.FindStringSubmatch(text); len(m) > 1 {
		if hint := strings.Join(strings.Fields(m[1]), " "); hint != "" {
			return hint
		}
	}
	if m := providerResetEpochRe.FindStringSubmatch(text); len(m) > 1 {
		if sec, ok := parseProviderEpoch(m[1]); ok {
			return sec.Local().Format("01-02 15:04")
		}
	}
	return ""
}

// compactProviderDuration 把「2h30m」「5 minutes」「1.5 hours」这类时长片段压缩成 2h30m 形式；
// 片段里没有可识别单位时返回空串。
func compactProviderDuration(raw string) string {
	matches := providerResetUnitRe.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range matches {
		unit := providerNoticeUnit(m[2])
		if unit == "" {
			continue
		}
		b.WriteString(m[1])
		b.WriteString(unit)
	}
	return b.String()
}

// parseProviderEpoch 解析秒或毫秒级 Unix 时间戳，13 位以上按毫秒处理。
func parseProviderEpoch(raw string) (time.Time, bool) {
	var sec int64
	for _, r := range raw {
		if r < '0' || r > '9' {
			return time.Time{}, false
		}
		sec = sec*10 + int64(r-'0')
	}
	if sec <= 0 {
		return time.Time{}, false
	}
	if sec > 1e11 {
		sec /= 1000
	}
	return time.Unix(sec, 0), true
}

// classifyProviderNotice 判断一条 warning/error 文本是否属于限流、额度耗尽或登录失效，
// 并尽量解析出重置提示。未命中任何关键词时返回 providerNoticeNone 与空提示。
func classifyProviderNotice(text string) (providerNoticeKind, string) {
	if strings.TrimSpace(text) == "" {
		return providerNoticeNone, ""
	}
	lower := strings.ToLower(text)
	kind := providerNoticeNone
	for _, rule := range providerNoticeKeywords {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				kind = rule.kind
				break
			}
		}
		if kind != providerNoticeNone {
			break
		}
	}
	if kind == providerNoticeNone {
		// 裸状态码：401 视为登录失效，429 视为限流。
		if m := providerNoticeStatusRe.FindStringSubmatch(lower); len(m) > 1 {
			if m[1] == "401" {
				kind = providerNoticeAuth
			} else {
				kind = providerNoticeRateLimit
			}
		}
	}
	if kind == providerNoticeNone {
		return providerNoticeNone, ""
	}
	return kind, parseProviderResetHint(text)
}

// providerNoticeKindFromErrKind 把后端上报的失败类别映射为横幅分类；四类以外的类别
// （max_turns、cancelled、api_error）与空串返回 providerNoticeNone，继续按原文关键词识别。
func providerNoticeKindFromErrKind(errKind string) providerNoticeKind {
	switch errKind {
	case errKindAuthRequired:
		return providerNoticeAuth
	case errKindRateLimited:
		return providerNoticeRateLimit
	case errKindContextExhausted:
		return providerNoticeContext
	}
	return providerNoticeNone
}

// classifyMessageNotice 按消息携带的失败类别优先选横幅样式，类别缺失或未命中时
// 回退到原文关键词识别，其他 provider 的旧行为不变。
func classifyMessageNotice(m Message) (providerNoticeKind, string) {
	if kind := providerNoticeKindFromErrKind(m.ErrKind); kind != providerNoticeNone {
		// 类别已明确时不解析重置时机：错误原文里可能夹着无关时长，猜出来的时间反而误导。
		return kind, ""
	}
	return classifyProviderNotice(m.Text)
}

// providerNoticeTitle 返回横幅标题。
func providerNoticeTitle(kind providerNoticeKind) string {
	switch kind {
	case providerNoticeRateLimit:
		return "触发限流"
	case providerNoticeQuota:
		return "额度已用完"
	case providerNoticeAuth:
		return "需要重新登录"
	case providerNoticeContext:
		return "上下文已满"
	}
	return ""
}

// providerNoticeActionHint 返回横幅底部的处理指引；没有需要额外说明的动作时返回空串。
func providerNoticeActionHint(kind providerNoticeKind, errKind string) string {
	switch kind {
	case providerNoticeAuth:
		// 后端判出未登录时，Claude CLI 的凭证要在终端刷新，设置页的登录入口帮不上忙。
		if errKind == errKindAuthRequired {
			return "请在终端运行 claude /login 重新登录后再发送。"
		}
		return "前往 设置 → Agent 登录 重新登录后再发送。"
	case providerNoticeContext:
		return "上下文已满，请新建会话或使用 /compact 后再发送。"
	}
	return ""
}

// providerNoticeStyle 返回横幅的图标与主色：限流用警告色、额度耗尽用危险色、
// 登录失效用强调色（提示用户去设置里重新登录）。
func providerNoticeStyle(c *ui.Context, kind providerNoticeKind) (*ui.SVG, ui.Color) {
	switch kind {
	case providerNoticeRateLimit:
		return IconOctagonAlert(), ThemeWarning(c)
	case providerNoticeQuota:
		return IconOctagonAlert(), ThemeDanger(c)
	case providerNoticeAuth:
		return IconLogIn(), ThemeAccent(c)
	case providerNoticeContext:
		return IconFoldVertical(), ThemeWarning(c)
	}
	return IconInfo(), ThemeSecondaryText(c)
}

// renderProviderNoticeBanner 渲染限流 / 额度耗尽 / 登录失效 / 上下文已满的横幅行：
// 标题行（图标 + 标题）、原文正文、可选的「预计 X 后恢复」与处理引导。
// 未命中分类的 warning、error 仍走原来的药丸样式，由调用方决定。
func renderProviderNoticeBanner(c *ui.Context, s *chatState, m Message, kind providerNoticeKind, resetHint string) ui.Element {
	icon, fg := providerNoticeStyle(c, kind)
	return ui.Row(c.Key("msg-provider-banner-" + m.ID)).MaxWidth(timelineContentMaxWidth(s)).Children(func() {
		ui.Column(c.Key("provider-banner-"+m.ID)).
			FillWidth().
			Padding(Space3, Space4).
			Radius(RadiusLG).
			Gap(Space1).
			Background(fg.Alpha(0.08)).
			Border(Hairline, fg.Alpha(0.28)).
			Children(func() {
				ui.Row(c.Key("provider-banner-head-" + m.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(fg)
					ui.Text(c, providerNoticeTitle(kind)).
						FontSize(scaledFont(TypeFootnote)).
						Bold().
						TextColor(fg)
				})
				ui.Text(c, strings.TrimSpace(m.Text)).
					FontSize(scaledFont(TypeFootnote)).
					LineHeight(1.45).
					TextColor(ThemeSecondaryText(c)).
					Selectable()
				if resetHint != "" {
					ui.Text(c, "预计 "+resetHint+" 后恢复").
						FontSize(scaledFont(TypeFootnote)).
						TextColor(fg.Alpha(0.85))
				}
				if hint := providerNoticeActionHint(kind, m.ErrKind); hint != "" {
					ui.Text(c, hint).
						FontSize(scaledFont(TypeFootnote)).
						TextColor(fg.Alpha(0.85))
				}
				if kind == providerNoticeRateLimit || kind == providerNoticeQuota {
					renderCapacityAutoRetry(c, s, m, fg)
				}
			})
	})
}

// providerNoticeProgress 读横幅需要的会话状态：这条消息是否会话里的最新一条、会话是否还在跑。
func providerNoticeProgress(s *chatState, m Message) (isLast, running bool) {
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	if n := len(s.messages); n > 0 {
		isLast = s.messages[n-1].ID == m.ID
	}
	threadID := s.selectedThreadID
	s.mu.Unlock()
	return isLast, s.isThreadRunning(threadID)
}

// renderCapacityAutoRetry 渲染满载/限流横幅里的自动重试进度：等待期间显示倒计时与取消按钮，
// 到点就由 tickCapacityRetry 发一条「继续」；次数用尽后换成手动「继续」按钮。
func renderCapacityAutoRetry(c *ui.Context, s *chatState, m Message, fg ui.Color) {
	if s == nil {
		return
	}
	isLast, running := providerNoticeProgress(s, m)
	remaining, attempts, armed := s.tickCapacityRetry(c.Now(), m, isLast, running)
	if armed {
		// 到点前登记一次重绘：即便这一帧没有别的重绘，也能按时自动重试。
		c.After(remaining)
		secs := capacityRetrySeconds(remaining)
		ui.Row(c.Key("provider-banner-retry-" + m.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("将在 %d 秒后自动重试（第 %d/%d 次）", secs, attempts+1, capacityRetryMaxAttempts)).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(fg.Alpha(0.85))
			Button(c, ButtonOpts{
				Key:     "provider-banner-cancel-" + m.ID,
				Text:    fmt.Sprintf("取消自动重试（%d 秒后重试）", secs),
				Variant: ButtonSecondary,
				Size:    ControlSM,
				Label:   "取消自动重试",
				OnClick: func() { s.cancelCapacityRetry(m) },
			})
		})
		return
	}
	if errorRetryVisible(isLast, running) && attempts < capacityRetryMaxAttempts {
		// 额度窗口这类要几个小时才恢复：自动重发没有意义，直接说明并给手动「继续」。
		if kind, _ := classifyMessageNotice(m); !capacityRetryWorthwhile(kind, m.Text) {
			ui.Text(c, "这个额度要等到重置时间才恢复，不会自动重试。恢复后可以点「继续」。").
				FontSize(scaledFont(TypeFootnote)).
				TextColor(fg.Alpha(0.85))
			renderErrorActionButton(c, "err-retry-btn-"+m.ID, "继续", "从中断处继续执行，不重发上一条消息", IconRefreshCw(), fg, func() {
				retryErrorMessage(s, m)
			})
			return
		}
	}
	if attempts >= capacityRetryMaxAttempts && errorRetryVisible(isLast, running) {
		ui.Text(c, "自动重试已达上限，可以点「继续」手动重试。").
			FontSize(scaledFont(TypeFootnote)).
			TextColor(fg.Alpha(0.85))
		renderErrorActionButton(c, "err-retry-btn-"+m.ID, "继续", "从中断处继续执行，不重发上一条消息", IconRefreshCw(), fg, func() {
			retryErrorMessage(s, m)
		})
	}
}
