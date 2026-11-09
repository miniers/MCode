package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type answerChannelClient struct {
	*FakeClient
	answers chan string
}

type blockingDecisionClient struct {
	*FakeClient
	entered chan struct{}
	release chan struct{}
}

func (c *blockingDecisionClient) AnswerStewardDecision(_ context.Context, _, _ string) error {
	c.entered <- struct{}{}
	<-c.release
	return nil
}

func TestStewardDecisionIgnoresDuplicateSubmission(t *testing.T) {
	client := &blockingDecisionClient{FakeClient: NewFakeClient(), entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := newChatState(client)
	s.answerStewardDecision("D-1", "A")
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("提交未开始")
	}
	s.answerStewardDecision("D-1", "B")
	select {
	case <-client.entered:
		t.Fatal("提交中再次调用了回答接口")
	default:
	}
	close(client.release)
}

func (c *answerChannelClient) AnswerStewardDecision(_ context.Context, id, answer string) error {
	c.answers <- id + " " + answer
	return nil
}

type legacyAnswerClient struct {
	*FakeClient
	sent chan string
}

func (c *legacyAnswerClient) Send(_ context.Context, _ string, text string) (bool, error) {
	c.sent <- text
	return true, nil
}

func TestStewardDecisionAnswerChannel(t *testing.T) {
	client := &answerChannelClient{FakeClient: NewFakeClient(), answers: make(chan string, 1)}
	s := newChatState(client)
	s.answerStewardDecision("D-0001", "A")
	select {
	case got := <-client.answers:
		if got != "D-0001 A" || client.SendCalls() != 0 {
			t.Fatalf("回答通道错误: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未调用回答接口")
	}

	legacy := &legacyAnswerClient{FakeClient: NewFakeClient(), sent: make(chan string, 1)}
	old := newChatState(legacy)
	old.selectedThreadID = "t1"
	old.answerStewardDecision("D-0001", "忽略")
	select {
	case got := <-legacy.sent:
		if got != "D-0001 忽略" {
			t.Fatalf("旧通道错误: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未发送旧版回答")
	}
}

func TestParseStewardDecision(t *testing.T) {
	decision, ok := parseStewardDecision(`{"id":"D-0001","task":"任务一","task_id":"T-1","thread":"thread-1","question":"选择？","options":[{"key":"A","text":"方案一"}],"recommended":"A"}`)
	if !ok || decision.ID != "D-0001" || decision.Task != "任务一" || decision.TaskID != "T-1" || decision.Thread != "thread-1" || decision.Question != "选择？" || len(decision.Options) != 1 || decision.Options[0].Text != "方案一" || decision.Recommended != "A" {
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
		remote   bool
		answered bool
	}{
		{"未回答", nil, "", false, false},
		{"旧版选项", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A"}`}}, "已选 A · 方案一", false, true},
		{"本机选项", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A","by":"owner"}`}}, "已选 A · 方案一", false, true},
		{"远程选项", []Message{{Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"A","by":"remote"}`}}, "已选 A · 方案一", true, true},
		{"远程忽略", []Message{{Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"忽略","by":"remote"}`}}, "已忽略", true, true},
		{"自定义", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"稍后再说"}`}}, "已选 稍后再说", false, true},
		{"其他编号", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0002","answer":"A"}`}}, "", false, false},
		{"远程改答", []Message{{Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A","by":"owner"}`}, {Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"忽略","by":"remote"}`}}, "已忽略", true, true},
		{"本机改答", []Message{{Kind: stewardDecisionAnswerKind, Role: "system", Text: `{"id":"D-0001","answer":"A","by":"remote"}`}, {Kind: stewardDecisionAnswerKind, Role: "user", Text: `{"id":"D-0001","answer":"A","by":"owner"}`}}, "已选 A · 方案一", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, remote, answered := stewardDecisionResult(decision, tc.messages)
			if text != tc.text || remote != tc.remote || answered != tc.answered {
				t.Fatalf("得到 %q, %v, %v", text, remote, answered)
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
