package remote

import (
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// ThreadMessageKindPlanDecision 是计划裁决记录消息的类型标识。
// 这类消息只供界面在重启后回放，不参与 Provider 上下文。
const ThreadMessageKindPlanDecision = "plan_decision"

// planDecisionRecord 是计划裁决记录消息的正文结构，序列化成 Text 字段的 JSON。
type planDecisionRecord struct {
	RequestID string `json:"requestID"`
	Plan      string `json:"plan"`
	Approved  bool   `json:"approved"`
	Feedback  string `json:"feedback"`
}

// planDecisionText 把计划请求和用户裁决合成为计划记录的 JSON 正文。
func planDecisionText(req wire.RemoteRequest, optionID string, answers map[string][]string) string {
	plan := ""
	if req.Detail != nil {
		plan = *req.Detail
	}
	approved := optionID == "allow" || optionID == "allow_always"
	feedback := ""
	if !approved {
		values := answers["feedback"]
		if len(values) > 0 {
			feedback = strings.TrimSpace(values[0])
		}
	}
	record := planDecisionRecord{
		RequestID: req.RequestID,
		Plan:      plan,
		Approved:  approved,
		Feedback:  feedback,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		// 结构固定且全是字符串与布尔值，序列化不会失败；兜底返回空对象。
		return "{}"
	}
	return string(raw)
}

// planDecisionRecorded 判断会话里是否已经存在该请求的计划裁决记录，用于避免重复写入。
func planDecisionRecorded(messages []wire.ThreadMessage, requestID string) bool {
	if requestID == "" {
		return false
	}
	for _, m := range messages {
		if m.Kind == nil || *m.Kind != ThreadMessageKindPlanDecision {
			continue
		}
		var record planDecisionRecord
		if err := json.Unmarshal([]byte(m.Text), &record); err != nil {
			continue
		}
		if record.RequestID == requestID {
			return true
		}
	}
	return false
}

// appendPlanDecision 把计划裁决追加为一条 plan_decision 系统消息，落库并推送给客户端。
// 同一 requestID 只写一次；turnID 缺失时按最近一条消息的回合兜底。
func (b *MemoryBackend) appendPlanDecision(threadID string, req wire.RemoteRequest, optionID string, answers map[string][]string) {
	text := planDecisionText(req, optionID, answers)
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)

	var turnID string
	if req.TurnID != nil {
		turnID = *req.TurnID
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	b.ensureMessagesLoadedLocked(td)
	if planDecisionRecorded(td.messages, req.RequestID) {
		b.mu.Unlock()
		return
	}
	b.touchThreadLocked(td)
	turnIndex := 0
	if len(td.messages) > 0 {
		turnIndex = td.messages[len(td.messages)-1].TurnIndex
	}
	for _, m := range td.messages {
		if turnID != "" && m.TurnID != nil && *m.TurnID == turnID {
			turnIndex = m.TurnIndex
		}
	}

	kind := ThreadMessageKindPlanDecision
	msg := wire.ThreadMessage{
		ID:        newUUID(),
		Role:      "system",
		Text:      text,
		CreatedAt: now,
		TurnIndex: turnIndex,
		Kind:      &kind,
	}
	if turnID != "" {
		msg.TurnID = &turnID
	}

	td.messages = append(td.messages, msg)
	td.summary.UpdatedAt = now
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.persistMessages(threadID, []wire.ThreadMessage{msg})
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, summary)
}
