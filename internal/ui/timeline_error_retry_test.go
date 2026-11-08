package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// TestErrorRetryTargetFor 验证重试目标取错误之前最近一条用户消息的正文与附件：
// 正文为空但带附件的消息也算目标，前面没有用户消息时返回空。
func TestErrorRetryTargetFor(t *testing.T) {
	media := []MessageMedia{{ID: "m1", Name: "shot.png", MimeType: "image/png", Width: 10, Height: 10}}
	cases := []struct {
		name     string
		messages []Message
		errorID  string
		wantText string
		wantLen  int
	}{
		{
			name: "正文加附件",
			messages: []Message{
				{ID: "u1", Role: "user", Text: "看看这个", Media: media},
				{ID: "a1", Role: "assistant", Text: "好的"},
				{ID: "e1", Role: "error", Text: "失败了"},
			},
			errorID:  "e1",
			wantText: "看看这个",
			wantLen:  1,
		},
		{
			name: "只有正文",
			messages: []Message{
				{ID: "u1", Role: "user", Text: "跑一下"},
				{ID: "e1", Role: "error", Text: "失败了"},
			},
			errorID:  "e1",
			wantText: "跑一下",
			wantLen:  0,
		},
		{
			name: "只有附件无正文",
			messages: []Message{
				{ID: "u1", Role: "user", Text: "  \n ", Media: media},
				{ID: "e1", Role: "error", Text: "失败了"},
			},
			errorID:  "e1",
			wantText: "  \n ",
			wantLen:  1,
		},
		{
			name: "跳过空白且无附件的用户消息",
			messages: []Message{
				{ID: "u1", Role: "user", Text: "第一句", Media: media},
				{ID: "u2", Role: "user", Text: "   "},
				{ID: "e1", Role: "error", Text: "失败了"},
			},
			errorID:  "e1",
			wantText: "第一句",
			wantLen:  1,
		},
		{
			name: "前面没有用户消息",
			messages: []Message{
				{ID: "a1", Role: "assistant", Text: "只有助手"},
				{ID: "e1", Role: "error", Text: "失败了"},
			},
			errorID: "e1",
		},
		{
			name:     "空列表",
			messages: nil,
			errorID:  "e1",
		},
		{
			name: "错误不在列表时从末尾找",
			messages: []Message{
				{ID: "u1", Role: "user", Text: "上一句"},
				{ID: "a1", Role: "assistant", Text: "在跑"},
			},
			errorID:  "e-missing",
			wantText: "上一句",
			wantLen:  0,
		},
	}
	for _, tc := range cases {
		got := errorRetryTargetFor(tc.messages, tc.errorID)
		if got.text != tc.wantText || len(got.media) != tc.wantLen {
			t.Fatalf("%s: errorRetryTargetFor() = (%q, %d 个附件), want (%q, %d 个附件)",
				tc.name, got.text, len(got.media), tc.wantText, tc.wantLen)
		}
	}
}

// TestErrorRetryTargetAvailable 验证「重试」的显示判定：有正文或有附件即可，两者都没有时不显示。
func TestErrorRetryTargetAvailable(t *testing.T) {
	cases := []struct {
		name   string
		target errorRetryTarget
		want   bool
	}{
		{"正文", errorRetryTarget{text: "跑一下"}, true},
		{"只有附件", errorRetryTarget{media: []MessageMedia{{ID: "m1"}}}, true},
		{"正文为空白且无附件", errorRetryTarget{text: " \n\t "}, false},
		{"空目标", errorRetryTarget{}, false},
	}
	for _, tc := range cases {
		if got := tc.target.available(); got != tc.want {
			t.Fatalf("%s: available() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestErrorRetryMediaCandidates 验证附件数量上限：超过单条消息上限的部分截掉，未超过时原样返回。
func TestErrorRetryMediaCandidates(t *testing.T) {
	makeMedia := func(n int) []MessageMedia {
		out := make([]MessageMedia, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, MessageMedia{ID: "m" + string(rune('1'+i))})
		}
		return out
	}

	limit := config.DefaultComposerAttachmentLimit
	if got := errorRetryMediaCandidates(nil, limit); len(got) != 0 {
		t.Fatalf("空列表应返回空，got %d 个", len(got))
	}
	if got := errorRetryMediaCandidates(makeMedia(2), limit); len(got) != 2 {
		t.Fatalf("未超过上限应原样返回，got %d 个", len(got))
	}
	if got := errorRetryMediaCandidates(makeMedia(limit), limit); len(got) != limit {
		t.Fatalf("正好上限应保留全部，got %d 个", len(got))
	}
	got := errorRetryMediaCandidates(makeMedia(limit+2), limit)
	if len(got) != limit {
		t.Fatalf("超过上限应截断到 %d 个，got %d 个", limit, len(got))
	}
	if got[limit-1].ID != makeMedia(limit)[limit-1].ID {
		t.Fatalf("截断应保留前面的附件，got 最后一个 %q", got[limit-1].ID)
	}
}

// TestErrorRetryMediaFits 验证单文件大小上限：空内容与超过 20MiB 的原图不能重新发送。
func TestErrorRetryMediaFits(t *testing.T) {
	cases := []struct {
		name string
		size int
		want bool
	}{
		{"空内容", 0, false},
		{"负数", -1, false},
		{"1 字节", 1, true},
		{"正好 20MiB", maxAttachmentBytes, true},
		{"超过 20MiB 一个字节", maxAttachmentBytes + 1, false},
	}
	for _, tc := range cases {
		if got := errorRetryMediaFits(tc.size); got != tc.want {
			t.Fatalf("%s: errorRetryMediaFits(%d) = %v, want %v", tc.name, tc.size, got, tc.want)
		}
	}
}

// TestErrorRetryMediaMeta 验证重新上传的文件名与 MIME 取值优先级与兜底。
func TestErrorRetryMediaMeta(t *testing.T) {
	pngData := []byte("\x89PNG\r\n\x1a\nfake")

	name, mimeType := errorRetryMediaMeta(MessageMedia{Name: "shot.png", MimeType: "image/png"}, "", pngData)
	if name != "shot.png" || mimeType != "image/png" {
		t.Fatalf("应取消息里记录的文件名与 MIME，got %q %q", name, mimeType)
	}

	name, mimeType = errorRetryMediaMeta(MessageMedia{Name: "shot.png", MimeType: "image/png"}, " image/jpeg ", pngData)
	if name != "shot.png" || mimeType != "image/jpeg" {
		t.Fatalf("读取接口返回的 MIME 优先，got %q %q", name, mimeType)
	}

	name, mimeType = errorRetryMediaMeta(MessageMedia{}, "", pngData)
	if name == "" || mimeType == "" {
		t.Fatalf("文件名与 MIME 都缺失时应兜底，got %q %q", name, mimeType)
	}
}

// TestErrorRetryInFlight 验证「重试中」标记只能被同一个错误消息占用一次，重置后可再次占用。
func TestErrorRetryInFlight(t *testing.T) {
	const id = "e-err-retry-inflight"
	if !beginErrorRetry(id) {
		t.Fatalf("首次应标记成功")
	}
	if !errorRetryInProgress(id) {
		t.Fatalf("标记后应读作进行中")
	}
	if beginErrorRetry(id) {
		t.Fatalf("进行中不应再次标记成功")
	}
	finishErrorRetry(nil, id)
	if errorRetryInProgress(id) {
		t.Fatalf("清除后不应再是进行中")
	}
	if !beginErrorRetry(id) {
		t.Fatalf("清除后应能再次标记")
	}
	finishErrorRetry(nil, id)
}

// newRetryTestState 起一个带指定消息的界面，返回 Root 内部真实使用的 chatState。
func newRetryTestState(t *testing.T, msgs []Message) (*FakeClient, *chatState, *ui.Tester) {
	t.Helper()
	fake := NewFakeClient()
	fake.SetMessages("t1", msgs)
	var s *chatState
	tt := ui.NewTester(Root(fake, func(cs *chatState) { s = cs }), 1000, 700)
	waitFor(t, tt, func() bool { return s != nil && s.chatSelectedThreadID() == "t1" })
	return fake, s, tt
}

// TestContinuationPromptFor 验证失败卡片发的是「继续」提示，满载时用满载文案。
func TestContinuationPromptFor(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"普通失败", Message{ID: "e1", Text: "连接中断", ErrKind: errKindAuthRequired}, continuationPrompt},
		{"文本命中限流", Message{ID: "e1", Text: "429 Too Many Requests"}, continuationPromptCapacity},
		{"错误类别命中限流", Message{ID: "e1", Text: "Provider error", ErrKind: errKindRateLimited}, continuationPromptCapacity},
		{"文本命中额度", Message{ID: "e1", Text: "You hit your usage limit"}, continuationPromptCapacity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := continuationPromptFor(tc.msg); got != tc.want {
				t.Fatalf("continuationPromptFor() = %q, want %q", got, tc.want)
			}
		})
	}
	if strings.Contains(continuationPrompt, "重发") || strings.Contains(continuationPromptCapacity, "重发") {
		t.Fatalf("继续提示不应要求重发上一条消息")
	}
}

// TestCapacityRetryDelay 验证退避节奏是 5s、10s、20s、40s、80s 且到顶后不再增长。
func TestCapacityRetryDelay(t *testing.T) {
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 80 * time.Second}
	for i, w := range want {
		if got := capacityRetryDelay(i); got != w {
			t.Fatalf("capacityRetryDelay(%d) = %v, want %v", i, got, w)
		}
	}
	if got := capacityRetryDelay(-3); got != 5*time.Second {
		t.Fatalf("capacityRetryDelay(-3) = %v, want 5s", got)
	}
}

// TestCapacityRetryEligible 验证只有最新一条满载/限流错误、且会话没在跑时才自动重试；
// 重置时间超出退避阶梯总时长的额度窗口不自动重试。
func TestCapacityRetryEligible(t *testing.T) {
	limited := Message{ID: "e1", Text: "请求过于频繁，已被限流"}
	cases := []struct {
		name    string
		msg     Message
		isLast  bool
		running bool
		want    bool
	}{
		{"最新一条满载错误", limited, true, false, true},
		{"不是最新一条", limited, false, false, false},
		{"会话还在运行", limited, true, true, false},
		{"普通错误", Message{ID: "e1", Text: "连接中断"}, true, false, false},
		{"没有 ID", Message{ID: "", Text: "429 Too Many Requests"}, true, false, false},
		{"额度用尽", Message{ID: "e1", Text: "You hit your usage limit"}, true, false, false},
		{"Claude 5 小时窗口", usageWindowRetryMessage(), true, false, false},
		{"长重置提示", Message{ID: "e1", Text: "Rate limit exceeded, resets in 45m"}, true, false, false},
		{"短重置提示", Message{ID: "e1", Text: "429 Too Many Requests, try again in 30s"}, true, false, true},
		{"短暂额度容量", Message{ID: "e1", Text: "You have exhausted your capacity on this model. Your quota will reset after 2s"}, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capacityRetryEligible(tc.msg, tc.isLast, tc.running); got != tc.want {
				t.Fatalf("capacityRetryEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCapacityRetryDue 验证到点判定。
func TestCapacityRetryDue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if capacityRetryDue(&capacityRetryState{}, now) {
		t.Fatalf("还没开始计时时不应到点")
	}
	if capacityRetryDue(&capacityRetryState{nextAt: now.Add(time.Second)}, now) {
		t.Fatalf("还没到点时不应到点")
	}
	if !capacityRetryDue(&capacityRetryState{nextAt: now}, now) {
		t.Fatalf("刚好到点应当到点")
	}
	if !capacityRetryDue(&capacityRetryState{nextAt: now.Add(-time.Second)}, now) {
		t.Fatalf("过了点应当到点")
	}
}

// TestOnlyLatestErrorShowsRetry 验证只有最新一条错误才提供「继续」入口。
func TestOnlyLatestErrorShowsRetry(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我改配置"},
		{ID: "e1", Role: "assistant", Text: "连接中断"},
		{ID: "u2", Role: "user", Text: "再试一次"},
		{ID: "e2", Role: "assistant", Text: "还是失败"},
	}
	if errorRetryVisible(errorIsLastMessage(msgs, msgs[1]), false) {
		t.Fatalf("不是最新一条错误时不应显示继续")
	}
	if !errorRetryVisible(errorIsLastMessage(msgs, msgs[3]), false) {
		t.Fatalf("最新一条错误应显示继续")
	}
	if errorRetryVisible(errorIsLastMessage(msgs, msgs[3]), true) {
		t.Fatalf("会话运行中不应显示继续")
	}
}

// TestRetrySendsContinuationPrompt 验证点「继续」发的是从中断处继续的提示，而不是重发上一条用户消息。
func TestRetrySendsContinuationPrompt(t *testing.T) {
	errMsg := Message{ID: "e1", Role: "assistant", Text: "连接中断", TurnIndex: 1, IsFinal: true}
	fake, s, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "帮我改一下配置", TurnIndex: 1, IsFinal: true},
		errMsg,
	})
	waitFor(t, tt, func() bool { return tt.HasText("连接中断") })
	fake.ResetCalls()

	retryErrorMessage(s, errMsg)
	waitFor(t, tt, func() bool { return fake.SendCalls() > 0 })

	if got := fake.LastSentText(); got != continuationPrompt {
		t.Fatalf("继续按钮发出的文案 = %q, want %q", got, continuationPrompt)
	}
	if got := fake.LastSentText(); strings.Contains(got, "帮我改一下配置") {
		t.Fatalf("继续按钮不应重发上一条用户消息，实际发出 %q", got)
	}
}

// resetCapacityRetry 清掉渲染期已经建立的自动重试计时，让节奏断言从零开始。
func resetCapacityRetry(s *chatState) {
	capacityRetryMu.Lock()
	*getCapacityRetryState(s) = capacityRetryState{}
	capacityRetryMu.Unlock()
}

// waitRetrySend 等异步发送完成。这里不推进帧：一旦渲染横幅，横幅会在渲染期自己 tick，
// 把测试手动喂进去的时钟打乱。
func waitRetrySend(t *testing.T, s *chatState, fake *FakeClient, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fake.SendCalls() < want || testThreadSending(s) {
		if time.Now().After(deadline) {
			t.Fatalf("等发送完成超时：已发送 %d 条，want %d", fake.SendCalls(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCapacityRetryTickCadence 验证满载错误按 5s、10s、20s、40s、80s 的节奏自动重试，并在次数上限后停止。
func TestCapacityRetryTickCadence(t *testing.T) {
	limited := Message{ID: "e1", Role: "assistant", Text: "429 Too Many Requests", ErrKind: errKindRateLimited, TurnIndex: 1, IsFinal: true}
	fake, s, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		limited,
	})
	waitFor(t, tt, func() bool { return tt.HasText("429 Too Many Requests") })
	resetCapacityRetry(s)

	now := time.Unix(1_700_000_000, 0)
	remaining, attempts, armed := s.tickCapacityRetry(now, limited, true, false)
	if !armed || attempts != 0 || remaining != 5*time.Second {
		t.Fatalf("首次计时 = (%v, %d, %v), want (5s, 0, true)", remaining, attempts, armed)
	}
	remaining, _, armed = s.tickCapacityRetry(now.Add(4900*time.Millisecond), limited, true, false)
	if !armed || remaining != 100*time.Millisecond {
		t.Fatalf("4.9 秒时剩余 = %v, want 100ms", remaining)
	}

	due := now.Add(5 * time.Second)
	for i := 1; i <= capacityRetryMaxAttempts; i++ {
		sent := fake.SendCalls()
		if _, got, armed := s.tickCapacityRetry(due, limited, true, false); armed || got != i {
			t.Fatalf("第 %d 次到点 = (attempts=%d, armed=%v), want (%d, false)", i, got, armed, i)
		}
		waitRetrySend(t, s, fake, sent+1)
		if got := fake.LastSentText(); got != continuationPromptCapacity {
			t.Fatalf("第 %d 次自动重试发出 = %q, want %q", i, got, continuationPromptCapacity)
		}
		// 退避间隔逐次翻倍：下一次到点在当前到点时间加 10s、20s、40s、80s。
		due = due.Add(capacityRetryDelay(i))
	}

	sent := fake.SendCalls()
	if _, got, armed := s.tickCapacityRetry(due, limited, true, false); armed || got != capacityRetryMaxAttempts {
		t.Fatalf("次数用尽后 = (attempts=%d, armed=%v), want (%d, false)", got, armed, capacityRetryMaxAttempts)
	}
	if _, _, armed := s.tickCapacityRetry(due.Add(time.Hour), limited, true, false); armed {
		t.Fatalf("次数用尽后不应再自动重试")
	}
	waitRetrySend(t, s, fake, sent)
	if got := fake.SendCalls(); got != sent {
		t.Fatalf("次数用尽后又发出了 %d 条消息", got-sent)
	}
}

// claudeUsageWindowText 是 Claude 5 小时额度窗口的真实错误原文。
const claudeUsageWindowText = "API Error: Request rejected (429) · Claude Code: You've hit your 5-hour limit · resets 9:50am (UTC)"

// usageWindowRetryMessage 造一条会渲染额度窗口横幅的错误消息。
func usageWindowRetryMessage() Message {
	return Message{ID: "e1", Role: "assistant", Kind: "error", Text: claudeUsageWindowText, ErrKind: errKindRateLimited, TurnIndex: 1, IsFinal: true}
}

// TestCapacityRetrySkipsUsageWindow 验证按小时计的额度窗口不会自动重试。
func TestCapacityRetrySkipsUsageWindow(t *testing.T) {
	window := usageWindowRetryMessage()
	fake, s, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		window,
	})
	waitFor(t, tt, func() bool { return tt.HasText("5-hour") })
	resetCapacityRetry(s)
	fake.ResetCalls()

	now := time.Unix(1_700_000_000, 0)
	if _, got, armed := s.tickCapacityRetry(now, window, true, false); armed || got != 0 {
		t.Fatalf("额度窗口不应开始计时 = (attempts=%d, armed=%v), want (0, false)", got, armed)
	}
	if _, got, armed := s.tickCapacityRetry(now.Add(time.Hour), window, true, false); armed || got != 0 {
		t.Fatalf("额度窗口一小时后仍不应计时 = (attempts=%d, armed=%v), want (0, false)", got, armed)
	}
	waitRetrySend(t, s, fake, 0)
	if got := fake.SendCalls(); got != 0 {
		t.Fatalf("额度窗口自动重试了 %d 次", got)
	}
}

// TestUsageWindowBannerSkipsAutoRetry 验证额度窗口横幅只说明不自动重试，并给出手动「继续」。
func TestUsageWindowBannerSkipsAutoRetry(t *testing.T) {
	_, _, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		usageWindowRetryMessage(),
	})
	waitFor(t, tt, func() bool { return tt.HasText("不会自动重试") })
	if tt.HasText("取消自动重试") {
		t.Fatalf("额度窗口不应显示取消按钮，当前文案: %q", tt.Texts())
	}
}

// TestCancelCapacityRetryStops 验证取消后不再自动重试。
func TestCancelCapacityRetryStops(t *testing.T) {
	limited := Message{ID: "e1", Role: "assistant", Text: "429 Too Many Requests", ErrKind: errKindRateLimited, TurnIndex: 1, IsFinal: true}
	fake, s, tt := newRetryTestState(t, []Message{
		{ID: "u1", Role: "user", Text: "跑一下测试", TurnIndex: 1, IsFinal: true},
		limited,
	})
	waitFor(t, tt, func() bool { return tt.HasText("429 Too Many Requests") })
	resetCapacityRetry(s)

	now := time.Unix(1_700_000_000, 0)
	if _, _, armed := s.tickCapacityRetry(now, limited, true, false); !armed {
		t.Fatalf("满载错误应当开始计时")
	}
	s.cancelCapacityRetry(limited)
	fake.ResetCalls()
	if _, _, armed := s.tickCapacityRetry(now.Add(time.Minute), limited, true, false); armed {
		t.Fatalf("取消后不应再计时")
	}
	waitRetrySend(t, s, fake, 0)
	if fake.SendCalls() != 0 {
		t.Fatalf("取消后仍然自动重试了 %d 次", fake.SendCalls())
	}
}
