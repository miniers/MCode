package mcodeui

import (
	"encoding/json"
	"strings"
)

const planDecisionMessageKind = "plan_decision"

func isPlanDecisionMessage(m Message) bool { return m.Kind == planDecisionMessageKind }

func parsePlanDecisionMessage(m Message) (resolvedPlan, bool) {
	if !isPlanDecisionMessage(m) {
		return resolvedPlan{}, false
	}
	var payload struct {
		RequestID string `json:"requestID"`
		Plan      string `json:"plan"`
		Approved  *bool  `json:"approved"`
		Feedback  string `json:"feedback"`
	}
	if json.Unmarshal([]byte(m.Text), &payload) != nil || strings.TrimSpace(payload.RequestID) == "" || payload.Approved == nil {
		return resolvedPlan{}, false
	}
	plan := resolvedPlan{ID: strings.TrimSpace(payload.RequestID), Plan: payload.Plan, Approved: *payload.Approved, Feedback: payload.Feedback, TurnIndex: m.TurnIndex, TurnID: m.TurnID}
	plan.Time, _ = parseRFC3339Time(m.CreatedAt)
	return plan, true
}

// 消息是持久化后的权威记录，内存条目只填补响应到消息到达之间的空档。
func dropResolvedPlansRecordedInMessages(plans []resolvedPlan, messages []Message) []resolvedPlan {
	if len(plans) == 0 || len(messages) == 0 {
		return plans
	}
	persisted := make(map[string]bool)
	for _, m := range messages {
		if p, ok := parsePlanDecisionMessage(m); ok {
			persisted[p.ID] = true
		}
	}
	if len(persisted) == 0 {
		return plans
	}
	var kept []resolvedPlan
	for _, p := range plans {
		if !persisted[p.ID] {
			kept = append(kept, p)
		}
	}
	return kept
}

// 待批计划放在最后一个回合之后，正文和时间线共用滚动区域。
func attachPendingPlanItems(items []timelineItem, approvals []Approval, threadID string) []timelineItem {
	for _, a := range approvals {
		if a.Kind != ApprovalKindPlan || (a.ThreadID != "" && a.ThreadID != threadID) {
			continue
		}
		items = append(items, timelineItem{kind: timelineItemPendingPlan, id: pendingPlanCardID(a.RequestID), turnIndex: -1,
			resolvedPlan: resolvedPlan{ID: a.RequestID, Plan: a.Detail, TurnID: a.TurnID, TurnIndex: -1}})
	}
	return items
}

func pendingPlanCardID(requestID string) string { return "pending-plan-" + requestID }

func resolvedPlanExport(p resolvedPlan) string {
	status := "已驳回"
	if p.Approved {
		status = "已批准"
	}
	text := "## 计划 · " + status + "\n\n" + p.Plan
	if !p.Approved && p.Feedback != "" {
		text += "\n\n> " + strings.ReplaceAll(p.Feedback, "\n", "\n> ")
	}
	return text
}
