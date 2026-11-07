package remote

import (
	"encoding/json"
	"fmt"
	"strings"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// MaxInlineAnswerChoices 是推送通知内联按钮允许的最大选项数。
// 平台通知栏最多只显示两三个动作，选项再多就必须打开页面。
const MaxInlineAnswerChoices = 2

// maxInlineAnswerLabelRunes 是通知按钮文字的 rune 上限，超出截断并补省略号。
const maxInlineAnswerLabelRunes = 24

// PushAnswerOption 是通知内联作答的一个按钮。
type PushAnswerOption struct {
	Action string `json:"action"` // 固定为 ans0 / ans1，供 service worker 拼出动作 id
	Label  string `json:"label"`  // 通知栏展示的按钮文字，超长时截断
	Value  string `json:"value"`  // 提交给后端的原始选项 label，不做任何改写
}

// PushAnswerTarget 是一次可内联作答的目标：请求、问题与按钮。
type PushAnswerTarget struct {
	RequestID  string
	QuestionID string
	Answers    []PushAnswerOption
}

// inlineAnswerTarget 判断本次推送事件能否在通知内一键作答。
// 与 pushRequestID 一致，只在唯一一条待处理请求时才给按钮：多条时无法确定哪条是本次新增的。
func inlineAnswerTarget(method string, params any) *PushAnswerTarget {
	if method != "requests.changed" {
		return nil
	}
	var requests []wire.RemoteRequest
	switch value := params.(type) {
	case wire.RequestsChangedParams:
		requests = value.Requests
	case *wire.RequestsChangedParams:
		if value == nil {
			return nil
		}
		requests = value.Requests
	case map[string]any:
		// JSON 解码后的表单（转发或测试路径）：转成强类型复用同一套字段，避免手写嵌套断言。
		data, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		var changed wire.RequestsChangedParams
		if err := json.Unmarshal(data, &changed); err != nil {
			return nil
		}
		requests = changed.Requests
	default:
		return nil
	}
	if len(requests) != 1 {
		return nil
	}
	return answerTargetFor(requests[0])
}

// answerTargetFor 判断单条待处理请求能否在通知内一键作答；条件不满足时返回 nil。
// 只开放提问类、恰好一道题、单选、非机密、1~2 个非空选项的请求。
func answerTargetFor(req wire.RemoteRequest) *PushAnswerTarget {
	if req.Kind != string(harness.RequestKindQuestion) {
		return nil
	}
	if strings.TrimSpace(req.RequestID) == "" || len(req.Questions) != 1 {
		return nil
	}
	question := req.Questions[0]
	if question.IsSecret || question.AllowsMultiple || strings.TrimSpace(question.ID) == "" {
		return nil
	}
	if len(question.Choices) == 0 || len(question.Choices) > MaxInlineAnswerChoices {
		return nil
	}
	answers := make([]PushAnswerOption, 0, len(question.Choices))
	for i, choice := range question.Choices {
		if strings.TrimSpace(choice.Label) == "" {
			return nil
		}
		answers = append(answers, PushAnswerOption{
			Action: fmt.Sprintf("ans%d", i),
			Label:  truncateRunes(choice.Label, maxInlineAnswerLabelRunes),
			Value:  choice.Label,
		})
	}
	return &PushAnswerTarget{
		RequestID:  req.RequestID,
		QuestionID: question.ID,
		Answers:    answers,
	}
}

// truncateRunes 按 rune 截断字符串，超出时在末尾补省略号，结果不超过 limit 个字符。
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
