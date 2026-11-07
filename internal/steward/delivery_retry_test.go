package steward

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// errSendUnavailable 模拟唤醒投递暂时不可用（隐藏入口写消息失败）。
var errSendUnavailable = errors.New("投递通道暂时不可用")

// setWakeFails 让接下来 n 次 SendStewardWake 失败，用于模拟投递暂时不可用。
func (f *fakeSessions) setWakeFails(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakeFails = n
}

// wakeAttemptCount 返回 SendStewardWake 的累计调用次数（含失败的那些）。
func (f *fakeSessions) wakeAttemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakeAttempts
}

// deliveryFailureNotices 返回「投递连续失败」的用户通知：它走可见助手消息，不是隐藏唤醒。
func (h *stewardRig) deliveryFailureNotices() []string {
	var out []string
	for _, msg := range h.sessions.appendedMessages() {
		if strings.Contains(msg.Text, "唤醒连续投递失败") {
			out = append(out, msg.Text)
		}
	}
	return out
}

// TestDeliveryRetriesAfterSendFailure 验证发送失败后整批保留，按退避重发且只成功投递一次。
func TestDeliveryRetriesAfterSendFailure(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	h.sessions.setWakeFails(1)
	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()

	h.advanceUntil("失败后重发成功", time.Second, 5*time.Minute, h.msgCountIs(1))
	if got := h.sessions.wakeAttemptCount(); got != 2 {
		t.Fatalf("投递尝试次数 = %d, 期望失败一次后重发一次", got)
	}
	if n := len(h.sessions.wakeMessages()); n != 1 {
		t.Fatalf("成功投递次数 = %d, 期望 1", n)
	}
	if text := h.lastText(); !strings.Contains(text, "标题 ev-1") {
		t.Fatalf("重发内容应仍包含之前失败的那条事件，实际为：\n%s", text)
	}

	// 再等一段时间确认没有重复投递：重试只补发一次。
	h.advance(5 * time.Minute)
	h.settle()
	if n := len(h.sessions.wakeMessages()); n != 1 {
		t.Fatalf("重发成功后不应再投递，实际投递 %d 条", n)
	}
}

// TestDeliveryGivesUpAfterRepeatedFailures 验证连续失败用尽退避后丢弃这一批，
// 并给用户发一条「请到收件箱查看」的通知（只发一次）。
func TestDeliveryGivesUpAfterRepeatedFailures(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	// 未消解数按 0 计，避免兜底巡检在退避期间插进来投递。
	h.events.setCount(0)
	h.sessions.setWakeFails(len(deliveryRetryBackoffs) + 1)
	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()

	h.advanceUntil("放弃这一批并通知用户", 10*time.Second, 20*time.Minute, func() bool {
		return len(h.deliveryFailureNotices()) == 1
	})
	if got := h.sessions.wakeAttemptCount(); got != len(deliveryRetryBackoffs)+1 {
		t.Fatalf("投递尝试次数 = %d, 期望 %d", got, len(deliveryRetryBackoffs)+1)
	}
	if n := len(h.sessions.wakeMessages()); n != 0 {
		t.Fatalf("连续失败后不应投递成功，实际 %d 条", n)
	}
	notice := h.deliveryFailureNotices()[0]
	if !strings.Contains(notice, "1 件待处理事项请到收件箱查看") {
		t.Fatalf("通知应写明待处理条数与收件箱去向，实际为：\n%s", notice)
	}

	// 放弃后这一批已清空：再等也不会重试，通知也只发一条。
	h.advance(10 * time.Minute)
	h.settle()
	if got := h.sessions.wakeAttemptCount(); got != len(deliveryRetryBackoffs)+1 {
		t.Fatalf("放弃后不应再重试，投递尝试次数 = %d", got)
	}
	if n := len(h.deliveryFailureNotices()); n != 1 {
		t.Fatalf("投递失败通知只应发一次，实际 %d 条", n)
	}
}

// TestWakeLimitCarryOverToNextDay 验证达到每日上限时事件结转到次日第一次唤醒，
// 消息里注明这些事项是昨天留下来的，投递成功后结转清空。
func TestWakeLimitCarryOverToNextDay(t *testing.T) {
	cfg := enabledConfig()
	cfg.DailyWakeLimit = 1
	h := newStewardRig(t, cfg)
	h.startSteward()

	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()
	h.advanceUntil("第一次唤醒", time.Second, 30*time.Second, h.msgCountIs(1))

	// 未消解数按 0 计，避免兜底巡检干扰上限断言。
	h.events.setCount(0)
	h.events.emit(ev("ev-2", "thread-2", "proj-1"))
	h.settle()
	h.advanceUntil("达到上限后结转", 5*time.Second, 3*time.Minute, func() bool {
		return len(h.wakeLimitNotices()) == 1
	})
	if n := len(h.sessions.messages()); n != 1 {
		t.Fatalf("达到上限后不应再投递，实际 %d 条", n)
	}

	// 次日：第一次唤醒把昨天结转的事件一起带上，并注明是昨天留下来的。
	h.advance(24 * time.Hour)
	h.settle()
	h.events.emit(ev("ev-3", "thread-3", "proj-1"))
	h.settle()
	h.advanceUntil("次日补上结转事项", time.Second, time.Minute, h.msgCountIs(2))

	text := h.lastText()
	if !strings.Contains(text, "以下是昨天超过上限后留到今天的事项") {
		t.Fatalf("次日唤醒应注明结转事项的来源，实际为：\n%s", text)
	}
	if !strings.Contains(text, "标题 ev-2") {
		t.Fatalf("次日唤醒应带上昨天被上限挡下的事件，实际为：\n%s", text)
	}
	if !strings.Contains(text, "标题 ev-3") {
		t.Fatalf("次日唤醒应同时带上当天的新事件，实际为：\n%s", text)
	}

	// 第三天：结转已清空，不应再重复列出昨天补过的事项。
	h.advance(24 * time.Hour)
	h.settle()
	h.events.emit(ev("ev-4", "thread-4", "proj-1"))
	h.settle()
	h.advanceUntil("第三天正常唤醒", time.Second, time.Minute, h.msgCountIs(3))
	if text := h.lastText(); strings.Contains(text, "以下是昨天超过上限后留到今天的事项") {
		t.Fatalf("结转补上后应清空，第三天不应再重复列出，实际为：\n%s", text)
	}
}

// TestDeliveryDroppedWhenDisabled 验证投递时配置已关闭：整批直接丢弃，不重试也不通知。
func TestDeliveryDroppedWhenDisabled(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	disabled := enabledConfig()
	disabled.Enabled = false
	h.cfg.set(disabled)
	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()

	h.advance(5 * time.Minute)
	h.settle()
	if got := h.sessions.wakeAttemptCount(); got != 0 {
		t.Fatalf("配置关闭时不应尝试投递，实际尝试 %d 次", got)
	}
	if n := len(h.deliveryFailureNotices()); n != 0 {
		t.Fatalf("配置关闭时不应发投递失败通知，实际 %d 条", n)
	}
	// 再推进一段虚拟时间，确认这批没有被挂起重试。
	h.advance(30 * time.Minute)
	h.settle()
	if got := h.sessions.wakeAttemptCount(); got != 0 {
		t.Fatalf("配置关闭后仍不应重试，实际尝试 %d 次", got)
	}
}
