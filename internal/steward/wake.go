package steward

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/attention"
)

// kindLabels 是事件类型的中文说明，未知类型回退为「其他事件」。
var kindLabels = map[string]string{
	string(attention.KindThreadStalled):       "会话卡住",
	string(attention.KindThreadFailed):        "会话失败",
	string(attention.KindThreadQuota):         "额度用尽",
	string(attention.KindThreadApproval):      "会话等待审批",
	string(attention.KindKanbanFailed):        "看板任务失败",
	string(attention.KindKanbanNeedsApproval): "看板任务待审批",
	string(attention.KindKanbanReview):        "看板任务待复核",
	string(attention.KindAutomationFailed):    "自动化失败",
	string(attention.KindAutomationFinished):  "自动化已完成",
	string(attention.KindBeeFailed):           "子代理失败",
	string(attention.KindThreadNoProgress):    "任务无进展",
}

// kindLabel 返回事件类型的中文说明。
func kindLabel(kind string) string {
	if label, ok := kindLabels[kind]; ok {
		return label
	}
	return "其他事件"
}

// truncateRunes 按字符（而非字节）截断文本，超长时补省略号，避免切坏多字节字符。
func truncateRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}

// buildWakeMessage 生成一次唤醒消息：逐条列出事件 ID、类型、标题、详情、对象、项目与相关会话。
// 详情超出 detailMaxRunes 时截断并提示用 attention_list 查看全文；ID 与摘要留在消息里，全文靠工具回查。
// 超过 maxBatchEvents 的只提示条数，结尾要求自己判断能处理的就处理、处理完用 attention_resolve 标记。
// deferred 为 true 表示事件积压于安静时段，开头追加一行要求合并成一条汇总通知。
func buildWakeMessage(events []attention.Event, deferred bool) string {
	total := len(events)
	shown := events
	extra := 0
	if total > maxBatchEvents {
		shown = events[:maxBatchEvents]
		extra = total - maxBatchEvents
	}

	var b strings.Builder
	if deferred {
		b.WriteString("以下事件积压于安静时段，请合并成一条汇总通知，不要逐条打扰用户。\n")
	}
	fmt.Fprintf(&b, "有 %d 条新情况，需要你先判断一下。\n\n", total)
	for i, ev := range shown {
		fmt.Fprintf(&b, "%d. 事件 %s（%s）", i+1, ev.ID, kindLabel(ev.Kind))
		if ev.Kind == string(attention.KindThreadApproval) {
			fmt.Fprintf(&b, "（用户已超过 %d 分钟未处理）", ownerActionGrace/time.Minute)
		}
		b.WriteByte('\n')
		if title := clipRunes(strings.Join(strings.Fields(ev.Title), " "), titleMaxRunes); title != "" {
			fmt.Fprintf(&b, "   标题：%s\n", title)
		}
		if raw := strings.Join(strings.Fields(ev.Detail), " "); raw != "" {
			detail := truncateRunes(raw, detailMaxRunes)
			if utf8.RuneCountInString(raw) > detailMaxRunes {
				detail += "（已截断，全文用 attention_list 查看）"
			}
			fmt.Fprintf(&b, "   详情：%s\n", detail)
		}
		subjectType := strings.TrimSpace(ev.SubjectType)
		subjectID := strings.TrimSpace(ev.SubjectID)
		if subjectType != "" && subjectID != "" {
			fmt.Fprintf(&b, "   对象：%s %s\n", subjectType, subjectID)
		}
		if projectID := strings.TrimSpace(ev.ProjectID); projectID != "" {
			fmt.Fprintf(&b, "   项目：%s\n", projectID)
		}
		if threadID := strings.TrimSpace(ev.ThreadID); threadID != "" {
			fmt.Fprintf(&b, "   相关会话：%s\n", threadID)
		}
	}
	if extra > 0 {
		fmt.Fprintf(&b, "\n另有 %d 条，用 attention_list 查看。\n", extra)
	}
	b.WriteString("\n请先自己判断：能处理的就处理掉（催促、重跑、改看板都算），只把需要用户亲自处理的事用 notify_owner 简短汇报（同一件事不要反复通知），" +
		"处理完用 attention_resolve 标记；没有需要用户处理的就只标记已处理。" +
		"会话等待审批类事件用户已收到过提醒，只在确有必要时再用 notify_owner 提醒一次。")
	return b.String()
}

// buildPatrolMessage 生成兜底巡检消息：只给未消解总数与调用工具的建议，具体内容由总管自己查询。
func buildPatrolMessage(count int) string {
	return fmt.Sprintf(
		"兜底巡检：目前有 %d 条待处理事件尚未消解。\n\n"+
			"请调用 attention_list 复核一遍，能自己处理的就处理掉，只把需要用户亲自处理的事用 notify_owner 简短汇报，"+
			"处理完用 attention_resolve 标记；没有需要用户处理的就不要打扰用户。",
		count,
	)
}

// buildNotesSection 生成并入唤醒消息的通知段：目前只有话题回执，每条一行。
// 回执已经由宿主写进账本，这里只让总管知道，不需要它再记一遍。
func buildNotesSection(notes []string) string {
	var b strings.Builder
	b.WriteString("话题回执（宿主已把结论记进账本的已定决策，无需再记）：\n")
	for _, note := range notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildRideSection 生成顺风车通知段：这些事件已经由宿主交给对应话题会话，主会话不需要再处理。
func buildRideSection(rides []string) string {
	var b strings.Builder
	b.WriteString("话题直通（宿主已交给对应话题会话处理，你不需要再处理）：\n")
	for _, ride := range rides {
		fmt.Fprintf(&b, "- %s\n", ride)
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildNotesMessage 生成只有通知、没有待处理事件的唤醒消息：先是通知段，结尾说明不需要打扰用户。
func buildNotesMessage(notes []string) string {
	return fmt.Sprintf(
		"宿主汇总了 %d 条话题回执。\n\n%s\n\n"+
			"这些话题已经收尾，不需要打扰用户；账本里与之相关的其他条目若已过时，用 steward_ledger_update 改掉或删掉。",
		len(notes), buildNotesSection(notes),
	)
}
