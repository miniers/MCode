package steward

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mcode/internal/attention"
)

// TestBuildWakeMessage_SubjectAndProjectLines 验证事件带对象与项目时逐行输出，字段为空时不输出。
func TestBuildWakeMessage_SubjectAndProjectLines(t *testing.T) {
	full := attention.Event{
		ID:          "ev-1",
		Kind:        string(attention.KindThreadFailed),
		SubjectType: "thread",
		SubjectID:   "th-1",
		ProjectID:   "proj-1",
		ThreadID:    "th-1",
		Title:       "会话失败",
		Detail:      "执行报错",
	}
	msg := buildWakeMessage([]attention.Event{full}, false)
	for _, want := range []string{
		"   对象：thread th-1\n",
		"   项目：proj-1\n",
		"   相关会话：th-1\n",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("唤醒消息缺少 %q: %q", want, msg)
		}
	}

	empty := attention.Event{
		ID:       "ev-2",
		Kind:     string(attention.KindThreadFailed),
		Title:    "会话失败",
		Detail:   "执行报错",
		ThreadID: "th-2",
	}
	msg = buildWakeMessage([]attention.Event{empty}, false)
	for _, banned := range []string{"   对象：", "   项目："} {
		if strings.Contains(msg, banned) {
			t.Fatalf("字段为空时不应输出 %q: %q", banned, msg)
		}
	}
	if !strings.Contains(msg, "   相关会话：th-2\n") {
		t.Fatalf("唤醒消息应保留相关会话: %q", msg)
	}
}

// TestBuildWakeMessage_TruncationHint 验证详情超长时带截断提示，短详情不带。
func TestBuildWakeMessage_TruncationHint(t *testing.T) {
	longDetail := attention.Event{
		ID:     "ev-long",
		Kind:   string(attention.KindThreadFailed),
		Title:  "会话失败",
		Detail: strings.Repeat("长", detailMaxRunes+20),
	}
	msg := buildWakeMessage([]attention.Event{longDetail}, false)
	if !strings.Contains(msg, "（已截断，全文用 attention_list 查看）") {
		t.Fatalf("超长详情应带截断提示: %q", msg)
	}

	shortDetail := attention.Event{
		ID:     "ev-short",
		Kind:   string(attention.KindThreadFailed),
		Title:  "会话失败",
		Detail: "短详情",
	}
	msg = buildWakeMessage([]attention.Event{shortDetail}, false)
	if strings.Contains(msg, "已截断") {
		t.Fatalf("短详情不应带截断提示: %q", msg)
	}
	if !strings.Contains(msg, "   详情：短详情\n") {
		t.Fatalf("唤醒消息应原样保留短详情: %q", msg)
	}
}

// TestBuildWakeMessage_ApprovalGrace 验证只有审批事件行带未处理时长，并提醒总管避免重复通知。
func TestBuildWakeMessage_ApprovalGrace(t *testing.T) {
	approval := evOfKind(attention.KindThreadApproval, "ev-approval", "thread-1", "proj-1")
	failed := evOfKind(attention.KindThreadFailed, "ev-failed", "thread-2", "proj-1")
	msg := buildWakeMessage([]attention.Event{approval, failed}, false)
	want := fmt.Sprintf("1. 事件 ev-approval（会话等待审批）（用户已超过 %d 分钟未处理）\n", ownerActionGrace/time.Minute)
	if !strings.Contains(msg, want) {
		t.Fatalf("审批事件行应说明未处理时长，实际为：\n%s", msg)
	}
	if !strings.Contains(msg, "2. 事件 ev-failed（会话失败）\n") {
		t.Fatalf("非审批事件行不应添加宽限期说明，实际为：\n%s", msg)
	}
	if !strings.Contains(msg, "只在确有必要时再用 notify_owner 提醒一次") {
		t.Fatalf("应提醒总管避免重复通知审批，实际为：\n%s", msg)
	}
}

// TestBuildRideSection 验证顺风车段的首行说明与逐条一行的格式。
func TestBuildRideSection(t *testing.T) {
	text := buildRideSection([]string{"已直通话题「重构登录」：看板任务失败 1 条", "已直通话题「补测试」：会话失败 2 条"})
	want := "话题直通（宿主已交给对应话题会话处理，你不需要再处理）：\n" +
		"- 已直通话题「重构登录」：看板任务失败 1 条\n" +
		"- 已直通话题「补测试」：会话失败 2 条"
	if text != want {
		t.Fatalf("顺风车段格式不正确，实际为：\n%s", text)
	}
}

// TestKindLabelThreadNoProgress 验证无进展事件的中文说明已登记，不会回退成「其他事件」。
func TestKindLabelThreadNoProgress(t *testing.T) {
	if got := kindLabel(string(attention.KindThreadNoProgress)); got != "任务无进展" {
		t.Fatalf("thread_no_progress 的中文说明应为「任务无进展」，实际为 %q", got)
	}
}
