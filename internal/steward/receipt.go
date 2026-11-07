package steward

import (
	"context"
	"log"
	"strings"
	"unicode/utf8"
)

// 本文件实现话题回执：话题结项、或它的目标完成或取消时，把「话题标题 + 一句结论」写进
// 主会话账本的已定决策，删掉该话题的在办条目，并在下一次唤醒批次里告诉主会话一声。
// 话题会话与主会话是两条会话，没有回执的话，主会话换新后就不知道这个话题已经收尾。

// phaseLabels 是话题阶段的中文名，回执与换新前言的话题索引共用。
var phaseLabels = map[string]string{
	PhaseDrafting:   "追问中",
	PhasePlanning:   "规划中",
	PhasePlanFailed: "规划失败",
	PhasePlanReview: "计划待审",
	PhaseRunning:    "执行中",
	PhasePaused:     "已暂停",
	PhaseCompleted:  "已完成",
	PhaseCancelled:  "已取消",
}

// phaseLabel 返回阶段的中文名，未知阶段原样返回。
func phaseLabel(phase string) string {
	if label, ok := phaseLabels[phase]; ok {
		return label
	}
	return phase
}

// ReceiptTopic 把话题的结论回执进主会话：写一条 decision、删掉该话题的 in_progress 条目，
// 再把同一行文本并入下一次唤醒批次。同一个话题只回执一次（看 Topic.ReceiptAt）；
// threadID 不是话题时什么也不做。返回 true 表示这次真的回执了。
func (r *Runtime) ReceiptTopic(ctx context.Context, threadID string) (bool, error) {
	threadID = strings.TrimSpace(threadID)
	r.receiptMu.Lock()
	defer r.receiptMu.Unlock()
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil {
		return false, err
	}
	if !ok || topic.ReceiptAt != nil {
		return false, nil
	}
	text := clipRunes("话题「"+topic.Title+"」："+r.topicConclusion(ctx, topic), LedgerTextMaxRunes)
	// 先写账本再打回执标记：标记写失败时下次还会重试，账本那边按话题替换旧决策，不会重复。
	if err := r.receiptLedger(ctx, threadID, text); err != nil {
		return false, err
	}
	now := r.now()
	if _, err := r.updateTopic(ctx, threadID, func(t *Topic) { t.ReceiptAt = &now }); err != nil {
		return false, err
	}
	r.enqueueNote(strings.Join(strings.Fields(text), " "))
	return true, nil
}

// topicConclusion 给出话题的一句结论：优先取目标的结果总结，没有就写最终阶段。
func (r *Runtime) topicConclusion(ctx context.Context, topic Topic) string {
	if strings.TrimSpace(topic.GoalID) == "" {
		return "已结项（没有建看板目标）"
	}
	goal, err := r.findGoal(ctx, topic.GoalID)
	if err != nil {
		return "已结项"
	}
	if goal.ResultSummary != nil {
		if summary := strings.Join(strings.Fields(*goal.ResultSummary), " "); summary != "" {
			return summary
		}
	}
	return "最终阶段：" + phaseLabel(PhaseFromGoalStatus(goal.Status))
}

// enqueueNote 把一行通知交给聚合循环，与待处理事件一起并入下一次唤醒；
// 缓冲满时丢弃通知，结论已在账本里，主会话下次读账本仍能看到。
func (r *Runtime) enqueueNote(note string) {
	select {
	case r.notes <- note:
	default:
		log.Printf("steward: 通知缓冲已满，丢弃一条唤醒通知: %s", note)
	}
}

// clipRunes 按 rune 截断到最多 max 个字符（含省略号），保证截断后仍不超过上限。
func clipRunes(text string, max int) string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max-1]) + "…"
}
