package swarm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BeeAnswer 是主线程回复里的一个答案块：对某个子线程的提问给出答案、跳过或交还用户回答。
// json 键沿用旧名 head，历史会话里的答案块照旧可读；新生成的示例只用 bee，见 buildForwardQuestionMessage。
type BeeAnswer struct {
	BeeThreadID string              `json:"head"`
	RequestID   string              `json:"request"`
	Answers     map[string][]string `json:"answers"`
	Skip        bool                `json:"skip"`
	Defer       bool                `json:"defer"`
}

// UnmarshalJSON 解码一条答案：子线程 ID 优先读新键 bee，兼容旧键 head。
func (a *BeeAnswer) UnmarshalJSON(data []byte) error {
	var raw struct {
		Bee        string              `json:"bee"`
		LegacyHead string              `json:"head"`
		RequestID  string              `json:"request"`
		Answers    map[string][]string `json:"answers"`
		Skip       bool                `json:"skip"`
		Defer      bool                `json:"defer"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id := strings.TrimSpace(raw.Bee)
	if id == "" {
		id = strings.TrimSpace(raw.LegacyHead)
	}
	*a = BeeAnswer{
		BeeThreadID: id,
		RequestID:   raw.RequestID,
		Answers:     raw.Answers,
		Skip:        raw.Skip,
		Defer:       raw.Defer,
	}
	return nil
}

// ForwardQuestion 是转交主线程代为回答的一道题。
type ForwardQuestion struct {
	ID             string
	Header         string
	Prompt         string
	Choices        []string
	AllowsMultiple bool
	AllowsOther    bool
}

// answerOpenerRegex 匹配行首的答案块起始行：三个反引号紧跟 swarm-answer，旧名 hydra-answer 继续兼容。
// 派发器只认 ```swarm / ```hydra 后跟空白或 -sent/-leaving，因此这个块不会被当成派发块。
var answerOpenerRegex = regexp.MustCompile("(?im)^```[ \\t]*(?:swarm-answer|hydra-answer)[ \\t]*\\r?\\n")

// ParseBeeAnswers 解析主线程回复里的答案块，块体可以是一个 JSON 对象或对象数组。
// bee 或 request 为空、以及 JSON 非法的块会被忽略；块尾缺少结束围栏时（例如回复被截断、
// 或系统在消息里给出的示例只保留了开头一行）按到文本末尾解析。
func ParseBeeAnswers(text string) []BeeAnswer {
	var out []BeeAnswer
	for _, loc := range answerOpenerRegex.FindAllStringIndex(text, -1) {
		start := loc[1]
		end := len(text)
		if fences := findLineStartFences(text, start); len(fences) > 0 {
			end = fences[0]
		}
		out = append(out, parseBeeAnswerBody(text[start:end])...)
	}
	return out
}

// parseBeeAnswerBody 解析单个答案块的块体，兼容单个对象与对象数组两种写法。
func parseBeeAnswerBody(body string) []BeeAnswer {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var one BeeAnswer
	if err := json.Unmarshal([]byte(body), &one); err == nil {
		if a, ok := validBeeAnswer(one); ok {
			return []BeeAnswer{a}
		}
		return nil
	}
	var many []BeeAnswer
	if err := json.Unmarshal([]byte(body), &many); err != nil {
		return nil
	}
	res := make([]BeeAnswer, 0, len(many))
	for _, a := range many {
		if v, ok := validBeeAnswer(a); ok {
			res = append(res, v)
		}
	}
	return res
}

// validBeeAnswer 去掉首尾空白并校验 bee 与 request，缺一即视为无效条目。
func validBeeAnswer(a BeeAnswer) (BeeAnswer, bool) {
	a.BeeThreadID = strings.TrimSpace(a.BeeThreadID)
	a.RequestID = strings.TrimSpace(a.RequestID)
	if a.BeeThreadID == "" || a.RequestID == "" {
		return BeeAnswer{}, false
	}
	return a, true
}

// BuildForwardQuestionMessage 生成把子线程提问交给主线程回答的注入消息。
func BuildForwardQuestionMessage(beeName, beeThreadID, requestID string, qs []ForwardQuestion) string {
	return buildForwardQuestionMessage(beeName, beeThreadID, requestID, qs, "")
}

// BuildAutoForwardQuestionMessage 生成提问超时后自动转交主线程回答的消息；
// 正文与手动转交相同，只多一句等待时长与作答要求。
func BuildAutoForwardQuestionMessage(beeName, beeThreadID, requestID string, waited time.Duration, qs []ForwardQuestion) string {
	notice := ""
	if waited > 0 {
		notice = fmt.Sprintf("用户 %s内未回答，已自动转交给你；请尽量直接作答，确实需要用户决定时再使用 defer。\n",
			formatForwardWaited(waited))
	}
	return buildForwardQuestionMessage(beeName, beeThreadID, requestID, qs, notice)
}

// formatForwardWaited 把等待时长写成「N 分钟」或「N 秒」。
func formatForwardWaited(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
	return fmt.Sprintf("%d 秒", int(d/time.Second))
}

// buildForwardQuestionMessage 拼接转交消息，notice 非空时插在首行之后。
func buildForwardQuestionMessage(beeName, beeThreadID, requestID string, qs []ForwardQuestion, notice string) string {
	var b strings.Builder
	name := strings.TrimSpace(beeName)
	if name == "" {
		name = beeThreadID
	}
	b.WriteString(NoticePrefix + "子代理提问：子线程「" + name + "」需要你回答下面的问题\n")
	if notice != "" {
		b.WriteString(notice)
	}
	b.WriteString("子线程：" + beeThreadID + "\n")
	b.WriteString("请求 ID：" + requestID + "\n")

	for i, q := range qs {
		fmt.Fprintf(&b, "\n题目 %d：%s\n", i+1, q.ID)
		if h := strings.TrimSpace(q.Header); h != "" {
			b.WriteString("标题：" + h + "\n")
		}
		if p := strings.TrimSpace(q.Prompt); p != "" {
			b.WriteString("问题：" + p + "\n")
		}
		if len(q.Choices) > 0 {
			b.WriteString("选项：" + strings.Join(q.Choices, " / ") + "\n")
		}
		multi := "只能单选"
		if q.AllowsMultiple {
			multi = "可以多选"
		}
		other := "不能自填其他答案"
		if q.AllowsOther {
			other = "可以自填其他答案"
		}
		b.WriteString(multi + "，" + other + "\n")
	}

	example := fmt.Sprintf(`{"bee":%s,"request":%s,"answers":{"题目ID":["选项文字"]}}`,
		strconv.Quote(beeThreadID), strconv.Quote(requestID))
	deferExample := fmt.Sprintf(`{"bee":%s,"request":%s,"defer":true}`,
		strconv.Quote(beeThreadID), strconv.Quote(requestID))
	skipExample := fmt.Sprintf(`{"bee":%s,"request":%s,"skip":true}`,
		strconv.Quote(beeThreadID), strconv.Quote(requestID))

	b.WriteString("\n把答案写成一个答案块放进你的回复里，系统会自动把答案提交给该子线程。" +
		"答案块的开头一行是三个反引号紧跟 swarm-answer，内容是一段 JSON：\n\n")
	b.WriteString("```swarm-answer\n" + example + "\n```\n\n")
	b.WriteString("示例里的题目 ID 和选项文字换成实际值：answers 的键是题目 ID，值是选中的选项文字；" +
		"可以多选时给多个值，允许自填时直接写要提交的答案。\n")
	b.WriteString("没有把握替用户决定时写 " + deferExample +
		"，并在回复正文里说明需要用户回答的原因；想直接跳过这道提问时写 " + skipExample + "。\n")
	return b.String()
}
