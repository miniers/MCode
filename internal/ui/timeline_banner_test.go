package mcodeui

import (
	"fmt"
	"testing"
	"time"
)

// TestClassifyProviderNotice 覆盖三类供应商提示的分类与未命中情形。
func TestClassifyProviderNotice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want providerNoticeKind
	}{
		{"英文限流", "429 Too Many Requests: rate limit exceeded", providerNoticeRateLimit},
		{"中文限流", "请求过于频繁，已被限流", providerNoticeRateLimit},
		{"裸 429", "provider returned 429", providerNoticeRateLimit},
		{"英文额度", "You hit your usage limit. Resets in 2h30m", providerNoticeQuota},
		{"中文额度", "当前账号额度已用完，请稍后重试", providerNoticeQuota},
		{"余额不足", "账户余额不足，请充值", providerNoticeQuota},
		{"英文未登录", "401 Unauthorized: not logged in", providerNoticeAuth},
		{"claude 未登录原文", "出错：Not logged in · Please run /login", providerNoticeAuth},
		{"提示运行 login", "Invalid API key · Please run /login", providerNoticeAuth},
		{"中文未登录", "请先登录后再发送消息", providerNoticeAuth},
		{"普通错误不分类", "连接失败：connection reset by peer", providerNoticeNone},
		{"空文本不分类", "   ", providerNoticeNone},
		{"长数字里不含状态码", "turn 242990 finished", providerNoticeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyProviderNotice(tc.text)
			if got != tc.want {
				t.Fatalf("classifyProviderNotice(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestParseProviderResetHint 覆盖重置提示的几种写法与解析不到时返回空串。
func TestParseProviderResetHint(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"英文时长组合", "rate limit reached, resets in 2h30m", "2h30m"},
		{"英文秒", "Rate limit exceeded, try again in 30s", "30s"},
		{"英文分钟单词", "usage limit reached, retry in ~5 minutes", "5m"},
		{"中文秒", "请求过于频繁，30 秒后重试", "30s"},
		{"中文小时", "额度已用完，2 小时后重置", "2h"},
		{"中文天", "配额用尽，3 天后恢复", "3d"},
		{"具体时刻", "resets at 3:00 pm", "3:00 pm"},
		{"中文重置时间", "重置时间：15:30", "15:30"},
		{"解析不到", "请检查网络连接", ""},
		{"空文本", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProviderResetHint(tc.text); got != tc.want {
				t.Fatalf("parseProviderResetHint(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestClassifyProviderNoticeReturnsResetHint 验证分类与重置提示在同一次调用中一起返回。
func TestClassifyProviderNoticeReturnsResetHint(t *testing.T) {
	kind, hint := classifyProviderNotice("Rate limit exceeded, resets in 45m")
	if kind != providerNoticeRateLimit {
		t.Fatalf("kind = %v, want providerNoticeRateLimit", kind)
	}
	if hint != "45m" {
		t.Fatalf("resetHint = %q, want 45m", hint)
	}
	if title := providerNoticeTitle(kind); title != "触发限流" {
		t.Fatalf("title = %q, want 触发限流", title)
	}
}

// TestClassifyMessageNoticeErrKind 验证后端上报的失败类别优先于原文关键词：
// 类别明确时不再靠文本猜（原文可能没有任何关键词），判断不出的类别与空串仍回退到关键词。
func TestClassifyMessageNoticeErrKind(t *testing.T) {
	cases := []struct {
		name    string
		message Message
		want    providerNoticeKind
	}{
		{"未登录（原文无关键词）", Message{Text: "出错：process exited unexpectedly", ErrKind: errKindAuthRequired}, providerNoticeAuth},
		{"API key 被拒绝", Message{Text: "401", ErrKind: errKindAPIKeyRejected}, providerNoticeAuth},
		{"限流（原文写的是额度）", Message{Text: "额度已用完", ErrKind: errKindRateLimited}, providerNoticeRateLimit},
		{"上下文已满（原文无关键词）", Message{Text: "出错：prompt is too long", ErrKind: errKindContextExhausted}, providerNoticeContext},
		{"只有关键词", Message{Text: "Rate limit exceeded, resets in 45m"}, providerNoticeRateLimit},
		{"兜底类别回退关键词", Message{Text: "not logged in", ErrKind: "api_error"}, providerNoticeAuth},
		{"未知类别回退关键词", Message{Text: "请求过于频繁", ErrKind: "some_future_kind"}, providerNoticeRateLimit},
		{"都没命中", Message{Text: "连接失败：connection reset by peer"}, providerNoticeNone},
		{"空消息", Message{}, providerNoticeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyMessageNotice(tc.message)
			if got != tc.want {
				t.Fatalf("classifyMessageNotice(%+v) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}

	// 类别明确时不解析重置时机：错误原文里的时长与限流无关，猜出来的时间会误导。
	if _, hint := classifyMessageNotice(Message{Text: "resets in 45m", ErrKind: errKindAuthRequired}); hint != "" {
		t.Fatalf("resetHint = %q, want empty for explicit err kind", hint)
	}
}

// TestProviderNoticeActionHint 验证各类横幅的底部指引：未登录按类别区分终端登录与设置页登录，
// 上下文已满提示新建会话或压缩；其余分类没有额外指引。
func TestProviderNoticeActionHint(t *testing.T) {
	cases := []struct {
		name    string
		kind    providerNoticeKind
		errKind string
		want    string
	}{
		{"未分类未登录沿用设置页提示", providerNoticeAuth, "", "前往 设置 → Agent 登录 重新登录后再发送。"},
		{"后端判出未登录走终端指引", providerNoticeAuth, errKindAuthRequired, "请在终端运行 claude /login 重新登录后再发送。"},
		{"API key 被拒绝", providerNoticeAuth, errKindAPIKeyRejected, "API Key 无效或已过期，请到 设置 → 模型与供应商 → Claude 检查。"},
		{"上下文已满", providerNoticeContext, errKindContextExhausted, "上下文已满，请新建会话或使用 /compact 后再发送。"},
		{"限流无指引", providerNoticeRateLimit, errKindRateLimited, ""},
		{"额度无指引", providerNoticeQuota, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerNoticeActionHint(tc.kind, tc.errKind); got != tc.want {
				t.Fatalf("providerNoticeActionHint(%v, %q) = %q, want %q", tc.kind, tc.errKind, got, tc.want)
			}
		})
	}
}

// rateLimitRetryMessage 造一条会渲染满载横幅的错误消息。
// 后端把这类中断写成 Role "assistant" + Kind "error" 的消息（工具结果与正文都还在），
// 只有 Kind 是 "error" 时才会走 renderErrorMessage 的横幅分支。
func rateLimitRetryMessage() Message {
	return Message{ID: "e1", Role: "assistant", Kind: "error", Text: "429 Too Many Requests: rate limit exceeded", ErrKind: errKindRateLimited, TurnIndex: 1, IsFinal: true}
}

// TestRateLimitBannerShowsAutoRetry 验证满载横幅显示倒计时并在到点后自动发一条「继续」。
func TestRateLimitBannerShowsAutoRetry(t *testing.T) {
	fake, _, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		rateLimitRetryMessage(),
	})
	waitFor(t, tt, func() bool { return tt.HasText("秒后自动重试") })
	if !tt.HasText("第 1/5 次") {
		t.Fatalf("横幅应显示重试次数，当前文案: %q", tt.Texts())
	}
	if !tt.HasText(capacityRetryCancelText(5)) {
		t.Fatalf("横幅应可取消自动重试，当前文案: %q", tt.Texts())
	}

	fake.ResetCalls()
	tt.Advance(5 * time.Second)
	waitFor(t, tt, func() bool { return fake.SendCalls() > 0 })
	if got := fake.LastSentText(); got != continuationPromptCapacity {
		t.Fatalf("自动重试发出 = %q, want %q", got, continuationPromptCapacity)
	}
}

// TestRateLimitBannerCancelStopsAutoRetry 验证点取消后不再自动重试。
func TestRateLimitBannerCancelStopsAutoRetry(t *testing.T) {
	fake, _, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		rateLimitRetryMessage(),
	})
	waitFor(t, tt, func() bool { return tt.HasText(capacityRetryCancelText(5)) })
	if err := tt.Click(capacityRetryCancelText(5)); err != nil {
		t.Fatalf("点不到取消按钮: %v", err)
	}

	fake.ResetCalls()
	tt.Advance(30 * time.Second)
	tt.Frame()
	if got := fake.SendCalls(); got != 0 {
		t.Fatalf("取消后仍自动重试了 %d 次", got)
	}
	if tt.HasText("秒后自动重试") {
		t.Fatalf("取消后不应再倒计时，当前文案: %q", tt.Texts())
	}
}

// capacityRetryCancelText 复现取消按钮在倒计时 secs 秒时的文案。
func capacityRetryCancelText(secs int) string {
	return fmt.Sprintf("取消自动重试（%d 秒后重试）", secs)
}
