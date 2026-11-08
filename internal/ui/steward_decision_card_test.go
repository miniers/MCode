package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestParseStewardDecision(t *testing.T) {
	decision, ok := parseStewardDecision(`{"id":"D-0001","task":"T-1","question":"选择？","options":[{"key":"A","text":"方案一"}],"recommended":"A"}`)
	if !ok || decision.ID != "D-0001" || decision.Task != "T-1" || decision.Question != "选择？" || len(decision.Options) != 1 || decision.Options[0].Text != "方案一" || decision.Recommended != "A" {
		t.Fatalf("解析结果不正确: %#v, %v", decision, ok)
	}
	for _, text := range []string{`{`, `{}`, `{"id":"D-0001"}`} {
		if _, ok := parseStewardDecision(text); ok {
			t.Fatalf("无效数据被接受: %s", text)
		}
	}
}

func TestStewardDecisionResult(t *testing.T) {
	decision := stewardDecision{ID: "D-0001", Options: []stewardDecisionOption{{Key: "A", Text: "方案一"}}}
	cases := []struct {
		name     string
		messages []Message
		text     string
		answered bool
	}{
		{"未回答", nil, "", false},
		{"选项", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A"}`}}, "已选 A · 方案一", true},
		{"忽略", []Message{{Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"忽略"}`}}, "已忽略", true},
		{"自定义", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"稍后再说"}`}}, "已选 稍后再说", true},
		{"其他编号", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0002","answer":"A"}`}}, "", false},
		{"改答", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A"}`}, {Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"忽略"}`}}, "已忽略", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, answered := stewardDecisionResult(decision, tc.messages)
			if text != tc.text || answered != tc.answered {
				t.Fatalf("得到 %q, %v", text, answered)
			}
		})
	}
}

func TestStewardDecisionShortcutAndMessage(t *testing.T) {
	options := []stewardDecisionOption{{Key: "A"}, {Key: "B"}}
	for _, tc := range []struct {
		key    ui.Key
		answer string
		ok     bool
	}{
		{ui.Key1, "A", true}, {ui.Key2, "B", true}, {ui.Key3, "", false}, {ui.Key9, "", false}, {ui.KeyEnter, "", false},
	} {
		answer, ok := stewardDecisionShortcut(tc.key, options)
		if answer != tc.answer || ok != tc.ok {
			t.Fatalf("按键 %v 得到 %q, %v", tc.key, answer, ok)
		}
	}
	if got := stewardDecisionMessage("D-0001", "A"); got != "D-0001 A" {
		t.Fatalf("发送内容: %q", got)
	}
}

func TestStewardDecisionDraft(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draft string
		want  string
	}{
		{"空草稿", "", "D-0001 "},
		{"空白草稿", "  ", "D-0001 "},
		{"非空草稿", "再等等", "D-0001 再等等"},
		{"已带前缀", "D-0001 再等等", "D-0001 再等等"},
		{"只有编号", "D-0001", "D-0001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stewardDecisionDraft(tc.draft, "D-0001"); got != tc.want {
				t.Fatalf("得到 %q", got)
			}
		})
	}
}

func TestLastInteractiveStewardDecision(t *testing.T) {
	messages := []Message{
		{ID: "one", Kind: stewardDecisionKind, Text: `{"id":"D-0001","question":"一"}`},
		{ID: "two", Kind: stewardDecisionKind, Text: `{"id":"D-0002","question":"二"}`},
		{Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0002","answer":"A"}`},
	}
	if got := lastInteractiveStewardDecision(messages); got != "one" {
		t.Fatalf("最后一张可交互卡片: %q", got)
	}
}

func TestStewardDecisionAnswerHidden(t *testing.T) {
	for _, role := range []string{"user", "system"} {
		if !isHiddenTimelineMessage(Message{Role: role, Kind: stewardDecisionAnswerKind}) {
			t.Fatalf("%s 回答不应产生时间线行", role)
		}
	}
}
