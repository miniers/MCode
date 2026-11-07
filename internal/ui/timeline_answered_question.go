package mcodeui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// answeredQuestion 记录一次已答复提问的历史态：每题的问题原文与用户答案。
// 答复当时只记在内存里（chatState.answeredQuestions），用于后端写入记录前即时显示；
// 后端把同一次答复写成 question_answer 消息后，重启也能从消息历史里还原同一张卡片。
type answeredQuestion struct {
	ID        string    // 审批请求 ID（Approval.RequestID）
	Time      time.Time // 答复时间
	Skip      bool      // true 表示整次跳过，没有作答
	TurnIndex int       // 所属 turn 序号，-1 表示定位不到
	TurnID    string    // 所属 turn ID
	Items     []answeredQuestionItem
}

// answeredQuestionItem 是一道题的问答对。
type answeredQuestionItem struct {
	Prompt  string   // 题目文本
	Answers []string // 该题选中的选项文本与用户填写的文本，按提交顺序
}

// questionAnswerMessageKind 是后端把已答复提问写成消息时使用的 Kind。
// 这类消息 Role 为 system，Text 是 JSON，TurnIndex 与提问所在回合一致。
const questionAnswerMessageKind = "question_answer"

// questionAnswerMessagePayload 是 question_answer 消息正文的 JSON 结构，与后端的写入格式一一对应。
type questionAnswerMessagePayload struct {
	RequestID string `json:"requestID"` // 审批请求 ID
	// RequestIDAlt 是 requestID 的另一种写法（requestId）：后端序列化时若用小驼峰，
	// 也照同一个请求 ID 处理，不让大小写差异把卡片退回成原文。
	RequestIDAlt string                      `json:"requestId"`
	Skipped      bool                        `json:"skipped"` // true 表示整次跳过提问
	Items        []questionAnswerMessageItem `json:"items"`   // 每题的问答
}

// questionAnswerMessageItem 是消息正文里的一道题的问答。
type questionAnswerMessageItem struct {
	Question string   `json:"question"` // 题目文本
	Answers  []string `json:"answers"`  // 该题选中的选项文本与用户填写的文本
}

// answeredQuestionAnswerLimit 是答案行最多显示的行数，超出的部分省略，避免长答案把时间线撑开。
const answeredQuestionAnswerLimit = 3

// newAnsweredQuestion 把一次答复整理成历史态。answers 的键是题目 ID，值与 buildQuestionAnswers 一致；
// skip 为 true 时只记跳过标记，不保留答案。
func newAnsweredQuestion(a Approval, answers map[string][]string, skip bool) answeredQuestion {
	q := answeredQuestion{ID: a.RequestID, Time: time.Now(), Skip: skip, TurnIndex: -1, TurnID: a.TurnID}
	if skip {
		return q
	}
	for _, aq := range a.Questions {
		q.Items = append(q.Items, answeredQuestionItem{Prompt: answeredQuestionPromptText(aq), Answers: cleanAnsweredValues(answers[aq.ID])})
	}
	if len(q.Items) == 0 && strings.TrimSpace(a.Detail) != "" {
		// 没有结构化题目时（旧版后端只给正文），把正文当成唯一的问题。
		q.Items = append(q.Items, answeredQuestionItem{Prompt: strings.TrimSpace(a.Detail)})
	}
	return q
}

// answeredQuestionPromptText 拼出题目文本：题头（Header）与题面（Prompt）都可能存在，两者用空格连接。
func answeredQuestionPromptText(q ApprovalQuestion) string {
	header := strings.TrimSpace(q.Header)
	prompt := strings.TrimSpace(q.Prompt)
	switch {
	case header == "":
		return prompt
	case prompt == "":
		return header
	default:
		return header + " " + prompt
	}
}

// cleanAnsweredValues 去掉答案里的空白项并整理首尾空白；没有有效答案时返回 nil。
func cleanAnsweredValues(list []string) []string {
	var out []string
	for _, v := range list {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isQuestionAnswerMessage 判断消息是否为后端写入的问答记录消息。
func isQuestionAnswerMessage(m Message) bool {
	return m.Kind == questionAnswerMessageKind
}

// parseQuestionAnswerMessage 把问答记录消息解析成历史态卡片数据：ID 用请求 ID，时间用消息时间，
// 跳过标记与每题的问答照搬；TurnIndex 沿用消息自身的回合号。
// 正文不是合法 JSON（或结构为空、并非问答记录）时返回 false，调用方退回按普通系统消息渲染原文，
// 不能因为一条脏消息让时间线出错。
func parseQuestionAnswerMessage(m Message) (answeredQuestion, bool) {
	var payload questionAnswerMessagePayload
	if err := json.Unmarshal([]byte(m.Text), &payload); err != nil {
		return answeredQuestion{}, false
	}
	q := answeredQuestion{
		ID:        strings.TrimSpace(payload.RequestID),
		Skip:      payload.Skipped,
		TurnIndex: m.TurnIndex,
		TurnID:    m.TurnID,
	}
	if q.ID == "" {
		q.ID = strings.TrimSpace(payload.RequestIDAlt)
	}
	if at, ok := parseRFC3339Time(m.CreatedAt); ok {
		q.Time = at
	}
	if q.Skip {
		return q, true
	}
	for _, item := range payload.Items {
		aq := answeredQuestionItem{Prompt: strings.TrimSpace(item.Question), Answers: cleanAnsweredValues(item.Answers)}
		if aq.Prompt == "" && len(aq.Answers) == 0 {
			// 题目与答案都为空：跳过空条目，避免卡片里多出一行空白。
			continue
		}
		q.Items = append(q.Items, aq)
	}
	if q.ID == "" && len(q.Items) == 0 {
		// 没有任何可用信息（例如正文是 null 或 {}）：当成非法记录，退回原文渲染。
		return answeredQuestion{}, false
	}
	return q, true
}

// answeredQuestionCardID 返回问答卡片的稳定 Key：优先用请求 ID，与答复当时的内存记录用同一个 Key，
// 内存记录换成消息记录的卡片时 Key 不变；拿不到请求 ID 时退回消息 ID。
func answeredQuestionCardID(id, msgID string) string {
	if id == "" {
		id = msgID
	}
	return "answered-question-" + id
}

// dropAnsweredQuestionsRecordedInMessages 去掉已经以 question_answer 消息落进会话的问答记录：
// 这些记录已由消息渲染成卡片，内存里的同一请求再渲染一次就会显示两张卡片。
// 解析失败的记录消息不算落盘，对应内存记录保留（此时消息退回原文渲染）。
func dropAnsweredQuestionsRecordedInMessages(qs []answeredQuestion, messages []Message) []answeredQuestion {
	if len(qs) == 0 || len(messages) == 0 {
		return qs
	}
	persisted := make(map[string]bool, len(qs))
	for _, m := range messages {
		if !isQuestionAnswerMessage(m) {
			continue
		}
		if q, ok := parseQuestionAnswerMessage(m); ok && q.ID != "" {
			persisted[q.ID] = true
		}
	}
	if len(persisted) == 0 {
		return qs
	}
	kept := make([]answeredQuestion, 0, len(qs))
	for _, q := range qs {
		if q.ID != "" && persisted[q.ID] {
			continue
		}
		kept = append(kept, q)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// answeredQuestionAnswerText 把一题的答案拼成一行：多个答案用「、」连接，没有答案返回空。
func answeredQuestionAnswerText(list []string) string {
	return strings.Join(cleanAnsweredValues(list), "、")
}

// answeredQuestionsKey 返回已答复提问的指纹：条目集合变化时才需要重建时间线。
// 卡片只渲染题目与答案，答复时间不参与渲染，所以指纹只要盖住 ID、跳过标记、回合号与问答内容就够了。
func answeredQuestionsKey(qs []answeredQuestion) string {
	if len(qs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, q := range qs {
		b.WriteString(q.ID)
		if q.Skip {
			b.WriteString("!skip")
		}
		b.WriteString(strconv.Itoa(q.TurnIndex))
		for _, item := range q.Items {
			b.WriteByte('|')
			b.WriteString(item.Prompt)
			b.WriteByte('=')
			b.WriteString(answeredQuestionAnswerText(item.Answers))
		}
		b.WriteByte(';')
	}
	return b.String()
}

// attachAnsweredQuestionItems 把已答复提问作为时间线条目，插在对应 turn 的末尾；定位不到 turn 时追加在时间线末尾。
// 规则与已裁决计划一致（见 attachResolvedPlanItems），只是在同回合的已裁决计划卡之后再插，保持先裁决后追问的顺序。
func attachAnsweredQuestionItems(items []timelineItem, qs []answeredQuestion) []timelineItem {
	if len(qs) == 0 {
		return items
	}
	res := items
	for _, q := range qs {
		item := timelineItem{
			kind:             timelineItemAnsweredQuestion,
			id:               answeredQuestionCardID(q.ID, ""),
			turnIndex:        q.TurnIndex,
			answeredQuestion: q,
		}
		if q.TurnIndex >= 0 {
			lastTurnIdx := -1
			for i := len(res) - 1; i >= 0; i-- {
				if res[i].turnIndex == q.TurnIndex {
					lastTurnIdx = i
					break
				}
			}
			if lastTurnIdx >= 0 {
				res = slices.Insert(res, lastTurnIdx+1, item)
				continue
			}
		}
		res = append(res, item)
	}
	return res
}

// renderAnsweredQuestionCard 渲染已答复提问的静态历史态卡片：紧凑卡片，每题一行问题加一行答案。
// 整次跳过时只显示一行「已跳过提问」。
func renderAnsweredQuestionCard(c *ui.Context, s *chatState, q answeredQuestion) {
	id := q.ID
	if id == "" {
		id = fmt.Sprintf("question-%d", q.Time.UnixNano())
	}
	ui.Column(c.Key("answered-question-"+id)).
		Radius(RadiusXL).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Padding(Space4).
		Gap(Space3).
		MaxWidth(timelineContentMaxWidth(s)).
		Children(func() {
			if q.Skip {
				ui.Row(c.Key("answered-question-skip-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, IconCircleEllipsis()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					ui.Text(c, "已跳过提问").FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Selectable()
				})
				return
			}
			ui.Row(c.Key("answered-question-header-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconCircleCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
				ui.Text(c, "提问").Bold().FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).Selectable()
				ui.Spacer(c)
				ui.Text(c, "已回复").FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).Selectable()
			})
			for i, item := range q.Items {
				renderAnsweredQuestionEntry(c, id, i, item)
			}
		})
}

// renderAnsweredQuestionEntry 渲染一道题的问答两行：首行以「问」开头，次行以「答」开头；
// 没有答案的题显示次要色的「未回答」。
func renderAnsweredQuestionEntry(c *ui.Context, cardID string, idx int, item answeredQuestionItem) {
	key := fmt.Sprintf("%s-%d", cardID, idx)
	ui.Column(c.Key("answered-question-entry-" + key)).Gap(Space1).Children(func() {
		ui.Row(c.Key("answered-question-ask-" + key)).Gap(Space3).AlignItems(ui.Start).FillWidth().Children(func() {
			ui.Text(c, "问").Bold().FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			ui.Text(c, item.Prompt).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Grow(1).MaxLines(answeredQuestionAnswerLimit).Selectable()
		})
		ui.Row(c.Key("answered-question-answer-" + key)).Gap(Space3).AlignItems(ui.Start).FillWidth().Children(func() {
			ui.Text(c, "答").Bold().FontSize(scaledFont(TypeFootnote)).TextColor(ThemeAccent(c))
			if answer := answeredQuestionAnswerText(item.Answers); answer != "" {
				ui.Text(c, answer).FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).Grow(1).MaxLines(answeredQuestionAnswerLimit).Selectable()
				return
			}
			ui.Text(c, "未回答").Italic().FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Grow(1).Selectable()
		})
	})
}
