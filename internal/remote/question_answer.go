package remote

import (
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// ThreadMessageKindQuestionAnswer 是问答记录消息的类型标识。
// 这类消息只供界面在重启后回放，和 info 一样不参与 Provider 上下文。
const ThreadMessageKindQuestionAnswer = "question_answer"

// questionAnswerItem 是问答记录里的单条题目。
type questionAnswerItem struct {
	Question string   `json:"question"`
	Answers  []string `json:"answers"`
}

// questionAnswerRecord 是问答记录消息的正文结构，序列化成 Text 字段的 JSON。
type questionAnswerRecord struct {
	RequestID string               `json:"requestID"`
	Skipped   bool                 `json:"skipped"`
	Items     []questionAnswerItem `json:"items"`
}

// questionAnswerText 把提问的题目列表与用户答复合成为问答记录的 JSON 正文。
// 题目顺序沿用请求里的原顺序；某题没有作答时 answers 是空数组。
func questionAnswerText(req wire.RemoteRequest, answers map[string][]string, skipped bool) string {
	items := make([]questionAnswerItem, 0, len(req.Questions))
	for _, q := range req.Questions {
		header := strings.TrimSpace(q.Header)
		prompt := strings.TrimSpace(q.Prompt)
		question := prompt
		switch {
		case header != "" && prompt != "":
			question = header + " " + prompt
		case header != "":
			question = header
		}
		values := answers[q.ID]
		item := questionAnswerItem{Question: question, Answers: make([]string, 0, len(values))}
		for _, v := range values {
			if text := strings.TrimSpace(v); text != "" {
				item.Answers = append(item.Answers, text)
			}
		}
		items = append(items, item)
	}
	record := questionAnswerRecord{RequestID: req.RequestID, Skipped: skipped, Items: items}
	raw, err := json.Marshal(record)
	if err != nil {
		// 结构固定且全是字符串，序列化不会失败；兜底返回空对象，避免写入非法正文。
		return "{}"
	}
	return string(raw)
}

// questionAnswerRecorded 判断会话里是否已经存在该请求的问答记录，用于避免重复写入。
func questionAnswerRecorded(messages []wire.ThreadMessage, requestID string) bool {
	if requestID == "" {
		return false
	}
	for _, m := range messages {
		if m.Kind == nil || *m.Kind != ThreadMessageKindQuestionAnswer {
			continue
		}
		var record questionAnswerRecord
		if err := json.Unmarshal([]byte(m.Text), &record); err != nil {
			continue
		}
		if record.RequestID == requestID {
			return true
		}
	}
	return false
}

// appendQuestionAnswer 把「提问 + 答复」追加为一条 question_answer 系统消息，落库并推送给客户端。
// 同一 requestID 只写一次；turnID 缺失时按最近一条消息的回合兜底。
func (b *MemoryBackend) appendQuestionAnswer(threadID string, req wire.RemoteRequest, answers map[string][]string, skipped bool) {
	text := questionAnswerText(req, answers, skipped)
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
	if questionAnswerRecorded(td.messages, req.RequestID) {
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

	kind := ThreadMessageKindQuestionAnswer
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

// appendSystemNotice 追加一条系统提示条目（例如撤卡说明），复用 info 消息的落库与广播路径。
// 调用方不得持有 b.mu：appendSystemMessage 会自行加锁，并会在锁外广播。
func (b *MemoryBackend) appendSystemNotice(threadID, text string) error {
	if text == "" {
		return nil
	}
	return b.appendSystemMessage(threadID, "info", text)
}
