package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestAnsweredQuestionAnswerText 覆盖单题答案的格式化：单选、多选、自由填写与未回答。
func TestAnsweredQuestionAnswerText(t *testing.T) {
	tests := []struct {
		name    string
		answers []string
		want    string
	}{
		{name: "单选", answers: []string{"先做界面"}, want: "先做界面"},
		{name: "多选", answers: []string{"单元测试", "文档", "示例"}, want: "单元测试、文档、示例"},
		{name: "自由填写", answers: []string{"  自定义答案  "}, want: "自定义答案"},
		{name: "未回答", answers: nil, want: ""},
		{name: "只有空白答案视为未回答", answers: []string{"  ", ""}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := answeredQuestionAnswerText(tc.answers); got != tc.want {
				t.Fatalf("answeredQuestionAnswerText(%q) = %q, want %q", tc.answers, got, tc.want)
			}
		})
	}
}

// TestNewAnsweredQuestionKeepsQuestionsAndAnswers 覆盖答案按题目 ID 归位，以及没答的题保留空答案。
func TestNewAnsweredQuestionKeepsQuestionsAndAnswers(t *testing.T) {
	a := Approval{
		RequestID: "req-q1",
		TurnID:    "turn-1",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{
			{ID: "q-single", Header: "范围", Prompt: "先做哪一项？", Choices: []ApprovalQuestionChoice{{Label: "界面"}, {Label: "存储"}}},
			{ID: "q-other", Prompt: "补充说明", AllowsOther: true},
			{ID: "q-multi", Prompt: "要哪些产物？", AllowsMultiple: true},
			{ID: "q-empty", Prompt: "没答的题"},
		},
	}
	answers := map[string][]string{
		"q-single": {"界面"},
		"q-other":  {"  自己写  "},
		"q-multi":  {"单测", "文档"},
	}
	q := newAnsweredQuestion(a, answers, false)

	if q.ID != "req-q1" {
		t.Fatalf("ID = %q, want %q", q.ID, "req-q1")
	}
	if q.Skip {
		t.Fatalf("Skip 应为 false")
	}
	if len(q.Items) != 4 {
		t.Fatalf("题目数 = %d, want 4", len(q.Items))
	}
	want := []struct {
		prompt string
		answer string
	}{
		{"范围 先做哪一项？", "界面"},
		{"补充说明", "自己写"},
		{"要哪些产物？", "单测、文档"},
		{"没答的题", ""},
	}
	for i, w := range want {
		if q.Items[i].Prompt != w.prompt {
			t.Errorf("第 %d 题题目 = %q, want %q", i+1, q.Items[i].Prompt, w.prompt)
		}
		if got := answeredQuestionAnswerText(q.Items[i].Answers); got != w.answer {
			t.Errorf("第 %d 题答案 = %q, want %q", i+1, got, w.answer)
		}
	}
}

// TestRecordAnsweredQuestionSkip 覆盖整次跳过：只留跳过标记，不保留答案。
func TestRecordAnsweredQuestionSkip(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"

	a := Approval{
		ThreadID:  "t1",
		RequestID: "req-skip",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{ID: "q1", Prompt: "要继续吗？"}},
	}
	s.recordAnsweredQuestion(a, nil, true)

	list := s.answeredQuestions["t1"]
	if len(list) != 1 {
		t.Fatalf("记录数 = %d, want 1", len(list))
	}
	if !list[0].Skip {
		t.Errorf("Skip 应为 true")
	}
	if len(list[0].Items) != 0 {
		t.Errorf("跳过时不应保留问答条目，got %+v", list[0].Items)
	}
}

// TestRecordAnsweredQuestionDedupByRequestID 覆盖按 RequestID 去重：同一请求重复答复只保留一条。
func TestRecordAnsweredQuestionDedupByRequestID(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"

	a := Approval{
		ThreadID:  "t1",
		RequestID: "req-1",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{ID: "q1", Prompt: "先做哪一项？"}},
	}
	s.recordAnsweredQuestion(a, map[string][]string{"q1": {"界面"}}, false)
	s.recordAnsweredQuestion(a, map[string][]string{"q1": {"存储"}}, false)

	list := s.answeredQuestions["t1"]
	if len(list) != 1 {
		t.Fatalf("同一 RequestID 应去重，got %d 条：%+v", len(list), list)
	}
	if got := answeredQuestionAnswerText(list[0].Items[0].Answers); got != "存储" {
		t.Errorf("去重时应以最后一次答复覆盖，got %q", got)
	}
}

// TestRecordAnsweredQuestionIgnoresNonQuestionKinds 覆盖只记提问类审批：命令、计划等不产生问答记录。
func TestRecordAnsweredQuestionIgnoresNonQuestionKinds(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"

	for _, kind := range []string{ApprovalKindCommand, ApprovalKindPlan, ApprovalKindFileChange, ApprovalKindCommit, ""} {
		a := Approval{ThreadID: "t1", RequestID: "req-" + kind, Kind: kind}
		s.recordAnsweredQuestion(a, map[string][]string{"q1": {"答"}}, false)
	}
	if got := len(s.answeredQuestions["t1"]); got != 0 {
		t.Fatalf("非提问类型不应记录，got %d 条", got)
	}
}

// TestAttachAnsweredQuestionItemsPlacesAfterTurn 覆盖问答条目按回合插在该回合末尾。
func TestAttachAnsweredQuestionItemsPlacesAfterTurn(t *testing.T) {
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", turnIndex: 0},
		{kind: timelineItemSingle, id: "a1", turnIndex: 0},
		{kind: timelineItemSingle, id: "u2", turnIndex: 1},
		{kind: timelineItemSingle, id: "a2", turnIndex: 1},
	}
	qs := []answeredQuestion{
		{ID: "q-first", TurnIndex: 0},
		{ID: "q-loose", TurnIndex: -1},
	}
	got := attachAnsweredQuestionItems(items, qs)

	wantIDs := []string{"u1", "a1", "answered-question-q-first", "u2", "a2", "answered-question-q-loose"}
	if len(got) != len(wantIDs) {
		t.Fatalf("条目数 = %d, want %d", len(got), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got[i].id != want {
			t.Errorf("第 %d 项 id = %q, want %q", i, got[i].id, want)
		}
	}
}

// TestTimelineAssemblyKeyIncludesAnsweredQuestions 覆盖拼装缓存键：新增问答记录会让键变化。
func TestTimelineAssemblyKeyIncludesAnsweredQuestions(t *testing.T) {
	if got := timelineAssemblyKey(nil, nil); got != "" {
		t.Errorf("计划与问答都为空时缓存键应为空串，got %q", got)
	}
	qs := []answeredQuestion{{ID: "q1", TurnIndex: 0, Items: []answeredQuestionItem{{Prompt: "问", Answers: []string{"答"}}}}}
	first := timelineAssemblyKey(nil, qs)
	if first == "" {
		t.Fatalf("有问答记录时缓存键不应为空")
	}
	qs = append(qs, answeredQuestion{ID: "q2", TurnIndex: 1, Skip: true})
	second := timelineAssemblyKey(nil, qs)
	if first == second {
		t.Errorf("新增问答记录后缓存键应变化，两者都是 %q", first)
	}
}

// questionAnswerTestMessage 构造一条后端写入的问答记录消息（Kind 为 question_answer，正文为 JSON）。
func questionAnswerTestMessage(id, text string, turnIndex int) Message {
	return Message{
		ID:        id,
		Role:      "system",
		Kind:      questionAnswerMessageKind,
		TurnIndex: turnIndex,
		CreatedAt: "2026-01-02T03:04:05Z",
		Text:      text,
	}
}

// TestParseQuestionAnswerMessage 覆盖问答记录消息的解析：单选、多题多选、未回答、整次跳过，
// 以及脏数据（非法 JSON、空对象、字段类型不符）返回失败。
func TestParseQuestionAnswerMessage(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantOK    bool
		wantID    string
		wantSkip  bool
		wantItems []answeredQuestionItem
	}{
		{
			name:      "单选",
			text:      `{"requestID":"req-1","skipped":false,"items":[{"question":"先做哪一项？","answers":["界面"]}]}`,
			wantOK:    true,
			wantID:    "req-1",
			wantItems: []answeredQuestionItem{{Prompt: "先做哪一项？", Answers: []string{"界面"}}},
		},
		{
			name:   "多题多选",
			text:   `{"requestID":"req-2","skipped":false,"items":[{"question":"要哪些产物？","answers":["单测","文档"]},{"question":"补充说明","answers":["自己写"]}]}`,
			wantOK: true,
			wantID: "req-2",
			wantItems: []answeredQuestionItem{
				{Prompt: "要哪些产物？", Answers: []string{"单测", "文档"}},
				{Prompt: "补充说明", Answers: []string{"自己写"}},
			},
		},
		{
			name:      "未回答",
			text:      `{"requestID":"req-3","items":[{"question":"没答的题","answers":[]}]}`,
			wantOK:    true,
			wantID:    "req-3",
			wantItems: []answeredQuestionItem{{Prompt: "没答的题"}},
		},
		{
			name:     "整次跳过",
			text:     `{"requestID":"req-4","skipped":true,"items":[{"question":"要继续吗？","answers":["继续"]}]}`,
			wantOK:   true,
			wantID:   "req-4",
			wantSkip: true,
		},
		{
			name:      "requestId 写法的请求ID",
			text:      `{"requestId":"req-5","items":[{"question":"问","answers":["答"]}]}`,
			wantOK:    true,
			wantID:    "req-5",
			wantItems: []answeredQuestionItem{{Prompt: "问", Answers: []string{"答"}}},
		},
		{name: "非法 JSON", text: "这不是 JSON"},
		{name: "空对象", text: `{}`},
		{name: "字段类型不符", text: `{"requestID":123}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := questionAnswerTestMessage("qa-test", tc.text, 2)
			q, ok := parseQuestionAnswerMessage(msg)
			if ok != tc.wantOK {
				t.Fatalf("parseQuestionAnswerMessage(%q) ok = %v, want %v", tc.text, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if q.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", q.ID, tc.wantID)
			}
			if q.Skip != tc.wantSkip {
				t.Errorf("Skip = %v, want %v", q.Skip, tc.wantSkip)
			}
			// 回合号照搬消息自身的 TurnIndex，时间用消息时间。
			if q.TurnIndex != 2 {
				t.Errorf("TurnIndex = %d, want 2", q.TurnIndex)
			}
			if q.Time.IsZero() {
				t.Errorf("Time 应取消息的 CreatedAt，got 零值")
			}
			if tc.wantSkip {
				if len(q.Items) != 0 {
					t.Errorf("跳过时不应保留问答条目，got %+v", q.Items)
				}
				return
			}
			if len(q.Items) != len(tc.wantItems) {
				t.Fatalf("题目数 = %d, want %d", len(q.Items), len(tc.wantItems))
			}
			for i, want := range tc.wantItems {
				if q.Items[i].Prompt != want.Prompt {
					t.Errorf("第 %d 题题目 = %q, want %q", i+1, q.Items[i].Prompt, want.Prompt)
				}
				if got, wantAns := strings.Join(q.Items[i].Answers, "、"), strings.Join(want.Answers, "、"); got != wantAns {
					t.Errorf("第 %d 题答案 = %q, want %q", i+1, got, wantAns)
				}
			}
		})
	}
}

// TestBuildTimelineItemsQuestionAnswerInPlace 覆盖问答记录消息在时间线里就地渲染成卡片：
// 位置不变、单独成项（不与工具消息合组）、回合号与消息序号照搬；正文损坏时退回普通系统消息。
func TestBuildTimelineItemsQuestionAnswerInPlace(t *testing.T) {
	payload := `{"requestID":"req-in-place","items":[{"question":"先做哪一项？","answers":["界面"]}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选", TurnIndex: 1},
		questionAnswerTestMessage("qa1", payload, 1),
		{ID: "a1", Role: "assistant", Text: "好的", TurnIndex: 1},
	}
	items := buildTimelineItemsLive(msgs, nil, "")
	if len(items) != 3 {
		t.Fatalf("条目数 = %d, want 3：%+v", len(items), items)
	}
	card := items[1]
	if card.kind != timelineItemAnsweredQuestion {
		t.Fatalf("第 2 项类型 = %v, want timelineItemAnsweredQuestion", card.kind)
	}
	if card.id != "answered-question-req-in-place" {
		t.Errorf("卡片 id = %q, want %q", card.id, "answered-question-req-in-place")
	}
	if card.msg.ID != "qa1" || card.msgIndex != 1 || card.turnIndex != 1 {
		t.Errorf("卡片应指向第 1 项消息并落在回合 1，got msg=%q msgIndex=%d turnIndex=%d", card.msg.ID, card.msgIndex, card.turnIndex)
	}
	if len(card.answeredQuestion.Items) != 1 || card.answeredQuestion.Items[0].Prompt != "先做哪一项？" {
		t.Errorf("卡片题目 = %+v, want 先做哪一项？", card.answeredQuestion.Items)
	}

	// 正文不是合法 JSON：退回普通系统消息单条，等渲染层显示原文。
	msgs[1].Text = "问答记录损坏"
	items = buildTimelineItemsLive(msgs, nil, "")
	if len(items) != 3 {
		t.Fatalf("条目数 = %d, want 3：%+v", len(items), items)
	}
	if items[1].kind != timelineItemSingle || items[1].msg.ID != "qa1" {
		t.Fatalf("损坏的记录应退回普通单条，got kind=%v msg=%q", items[1].kind, items[1].msg.ID)
	}
}

// TestDropAnsweredQuestionsRecordedInMessages 覆盖去重：已经以记录消息落进会话的请求ID
// 不再交给时间线；解析失败的消息不算落盘，对应内存记录保留。
func TestDropAnsweredQuestionsRecordedInMessages(t *testing.T) {
	msgs := []Message{
		questionAnswerTestMessage("qa-ok", `{"requestID":"req-1","items":[{"question":"问","answers":["答"]}]}`, 1),
		questionAnswerTestMessage("qa-bad", "不是 JSON", 2),
	}
	qs := []answeredQuestion{{ID: "req-1"}, {ID: "req-2"}, {ID: ""}}
	got := dropAnsweredQuestionsRecordedInMessages(qs, msgs)
	if len(got) != 2 {
		t.Fatalf("保留条数 = %d, want 2：%+v", len(got), got)
	}
	if got[0].ID != "req-2" || got[1].ID != "" {
		t.Fatalf("保留的请求ID = %q, %q, want req-2 与空串", got[0].ID, got[1].ID)
	}
	// 没有记录消息时原样返回。
	if kept := dropAnsweredQuestionsRecordedInMessages(qs, nil); len(kept) != 3 {
		t.Fatalf("没有记录消息时应原样返回，got %d 条", len(kept))
	}
}

// TestTimelineRowsQuestionAnswerMessageRendersCard 覆盖时间线里出现问答记录消息时渲染成问答卡片，
// 不再把原始 JSON 当系统消息显示。
func TestTimelineRowsQuestionAnswerMessageRendersCard(t *testing.T) {
	payload := `{"requestID":"req-ui","skipped":false,"items":[{"question":"先做哪一项？","answers":["界面","存储"]}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选一个", TurnIndex: 1, IsFinal: true},
		questionAnswerTestMessage("qa1", payload, 1),
		{ID: "a1", Role: "assistant", Text: "好的", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "先做哪一项？")
	if _, ok := tt.Find("问"); !ok {
		t.Fatalf("问答卡片应显示「问」行：%q", tt.Texts())
	}
	if _, ok := tt.Find("答"); !ok {
		t.Fatalf("问答卡片应显示「答」行：%q", tt.Texts())
	}
	if !tt.HasText("界面、存储") {
		t.Fatalf("问答卡片应显示答案：%q", tt.Texts())
	}
	if tt.HasText("requestID") {
		t.Fatalf("不应把记录消息的原始 JSON 渲染到时间线：%q", tt.Texts())
	}
}

// TestTimelineRowsQuestionAnswerShowsRepliedMark 覆盖已回复卡片的标题行：右侧显示「已回复」，
// 跳过的卡片不出现这个标记。
func TestTimelineRowsQuestionAnswerShowsRepliedMark(t *testing.T) {
	payload := `{"requestID":"req-replied","skipped":false,"items":[{"question":"先做哪一项？","answers":["界面"]}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选一个", TurnIndex: 1, IsFinal: true},
		questionAnswerTestMessage("qa1", payload, 1),
	}
	tt := newTimelineTester(t, msgs, "先做哪一项？")
	if !tt.HasText("已回复") {
		t.Fatalf("已回复卡片的标题行应显示「已回复」：%q", tt.Texts())
	}
}

// TestTimelineRowsQuestionAnswerSkippedRendersSkipLine 覆盖整次跳过的记录：卡片只显示跳过说明。
func TestTimelineRowsQuestionAnswerSkippedRendersSkipLine(t *testing.T) {
	payload := `{"requestID":"req-skip-ui","skipped":true}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选一个", TurnIndex: 1, IsFinal: true},
		questionAnswerTestMessage("qa1", payload, 1),
	}
	tt := newTimelineTester(t, msgs, "已跳过提问")
	if _, ok := tt.Find("答"); ok {
		t.Fatalf("跳过时不应显示「答」行：%q", tt.Texts())
	}
	if tt.HasText("已回复") {
		t.Fatalf("跳过时不应显示「已回复」：%q", tt.Texts())
	}
}

// TestTimelineRowsQuestionAnswerInvalidJSONFallsBackToSystemMessage 覆盖记录消息正文损坏时
// 退回普通系统消息显示原文，并且不生成卡片。
func TestTimelineRowsQuestionAnswerInvalidJSONFallsBackToSystemMessage(t *testing.T) {
	raw := "问答记录损坏"
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选一个", TurnIndex: 1, IsFinal: true},
		questionAnswerTestMessage("qa1", raw, 1),
	}
	tt := newTimelineTester(t, msgs, raw)
	if _, ok := tt.Find("问"); ok {
		t.Fatalf("损坏的记录不应渲染问答卡片：%q", tt.Texts())
	}
	if !tt.HasText(raw) {
		t.Fatalf("应退回普通系统消息显示原文：%q", tt.Texts())
	}
}

// TestTimelineRowsQuestionAnswerDedupWithMemoryRecord 覆盖去重的端到端效果：同一 requestID 的
// 内存记录与记录消息同时存在时，时间线里只有一张卡片。
func TestTimelineRowsQuestionAnswerDedupWithMemoryRecord(t *testing.T) {
	payload := `{"requestID":"req-dedup","items":[{"question":"先做哪一项？","answers":["界面"]}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我选一个", TurnIndex: 1, IsFinal: true},
		questionAnswerTestMessage("qa1", payload, 1),
		{ID: "a1", Role: "assistant", Text: "好的", TurnIndex: 1, IsFinal: true},
	}
	fake := NewFakeClient()
	fake.messages["t1"] = msgs
	var s *chatState
	tt := ui.NewTester(Root(fake, func(cs *chatState) { s = cs }), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("先做哪一项？") })
	if s == nil {
		t.Fatal("未能拿到 chatState")
	}

	// 模拟答复当时的内存记录仍在：记录消息已经落进会话，内存记录不能再渲染一张卡片。
	s.mu.Lock()
	s.answeredQuestions["t1"] = []answeredQuestion{{
		ID:        "req-dedup",
		TurnIndex: 1,
		Items:     []answeredQuestionItem{{Prompt: "先做哪一项？", Answers: []string{"存储"}}},
	}}
	s.mu.Unlock()
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if got := countExactTexts(tt.Texts(), "先做哪一项？"); got != 1 {
		t.Fatalf("同一 requestID 只应出现一张卡片，题目出现 %d 次：%q", got, tt.Texts())
	}
	if tt.HasText("存储") {
		t.Fatalf("已落盘的内存记录应被过滤：%q", tt.Texts())
	}
}

// TestUnreadCountedMessageExcludesQuestionAnswer 覆盖未读计数：问答记录消息不计入。
func TestUnreadCountedMessageExcludesQuestionAnswer(t *testing.T) {
	qa := questionAnswerTestMessage("qa1", `{"requestID":"req-unread","skipped":true}`, 1)
	if unreadCountedMessage(qa) {
		t.Fatalf("问答记录消息不应计入未读")
	}
	anchor := newTimelineUnreadAnchor([]Message{{ID: "u0", Role: "user"}})
	msgs := []Message{{ID: "u0", Role: "user"}, qa}
	if got := countUnreadMessages(msgs, anchor); got != 0 {
		t.Fatalf("未读条数 = %d, want 0", got)
	}
}

// countExactTexts 统计当前帧里文本恰好等于 want 的标签个数。
func countExactTexts(texts []string, want string) int {
	n := 0
	for _, s := range texts {
		if s == want {
			n++
		}
	}
	return n
}
