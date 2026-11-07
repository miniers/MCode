package swarm

import (
	"strings"
	"testing"
	"time"
)

func TestParseBeeAnswersObject(t *testing.T) {
	text := "结论如下。\n\n```swarm-answer\n" +
		`{"bee":"bee-1","request":"req-1","answers":{"q1":["后端"]}}` +
		"\n```\n\n其余说明。\n"
	got := ParseBeeAnswers(text)
	if len(got) != 1 {
		t.Fatalf("expected 1 answer, got %d: %+v", len(got), got)
	}
	a := got[0]
	if a.BeeThreadID != "bee-1" || a.RequestID != "req-1" || a.Skip || a.Defer {
		t.Fatalf("unexpected answer: %+v", a)
	}
	if answers := a.Answers["q1"]; len(answers) != 1 || answers[0] != "后端" {
		t.Fatalf("unexpected answers: %+v", a.Answers)
	}
}

func TestParseBeeAnswersArray(t *testing.T) {
	text := "```swarm-answer\n" +
		`[{"bee":"bee-1","request":"req-1","answers":{"q1":["A"]}},{"bee":"bee-2","request":"req-2","defer":true}]` +
		"\n```\n"
	got := ParseBeeAnswers(text)
	if len(got) != 2 {
		t.Fatalf("expected 2 answers, got %d: %+v", len(got), got)
	}
	if got[0].BeeThreadID != "bee-1" || got[1].BeeThreadID != "bee-2" || !got[1].Defer {
		t.Fatalf("unexpected answers: %+v", got)
	}
}

func TestParseBeeAnswersSkipsInvalid(t *testing.T) {
	text := "```swarm-answer\n{broken}\n```\n\n" +
		"```swarm-answer\n" + `{"bee":"","request":"req-1","answers":{"q1":["A"]}}` + "\n```\n\n" +
		"```swarm-answer\n" + `[{"bee":"bee-1","request":""}]` + "\n```\n"
	if got := ParseBeeAnswers(text); len(got) != 0 {
		t.Fatalf("expected no answers, got %+v", got)
	}
}

func TestParseBeeAnswersIgnoresDispatchBlocks(t *testing.T) {
	text := "```swarm\n" + `[{"prompt":"HEAD_WORK","bee":"Ives"}]` + "\n```\n\n" +
		"```swarm-sent\n" + `[{"prompt":"HEAD_WORK"}]` + "\n```\n\n" +
		"```swarm-answer\n" + `{"bee":"bee-1","request":"req-1","skip":true}` + "\n```\n\n" +
		"```swarm-leaving\n" + `[{"prompt":"HEAD_WORK"}]` + "\n```\n"
	got := ParseBeeAnswers(text)
	if len(got) != 1 || got[0].BeeThreadID != "bee-1" || !got[0].Skip {
		t.Fatalf("expected only the answer block, got %+v", got)
	}
	// 派发块仍按原规则解析，答案块不会被当成派发。
	if delegations, found, _ := ParseDelegations("```swarm\n" + `[{"prompt":"HEAD_WORK"}]` + "\n```\n"); !found || len(delegations) != 1 {
		t.Fatalf("dispatch parsing broken: %+v", delegations)
	}
	block := "```swarm-answer\n" + `{"bee":"bee-1","request":"req-1","answers":{"q1":["A"]}}` + "\n```\n"
	if _, found, _ := ParseDelegations(block); found {
		t.Fatal("answer block must not parse as a delegation")
	}
}

func TestBuildAutoForwardQuestionMessageAddsWaitedNotice(t *testing.T) {
	qs := []ForwardQuestion{{
		ID:             "q1",
		Header:         "范围",
		Prompt:         "要改哪个模块？",
		Choices:        []string{"后端", "前端"},
		AllowsMultiple: false,
		AllowsOther:    true,
	}}
	msg := BuildAutoForwardQuestionMessage("Ives", "bee-1", "req-1", 3*time.Minute, qs)
	if !strings.HasPrefix(msg, NoticePrefix+"子代理提问：") {
		t.Fatalf("unexpected prefix: %s", msg)
	}
	if !strings.Contains(msg, "用户 3 分钟内未回答，已自动转交给你") {
		t.Fatalf("message missing auto-forward notice: %s", msg)
	}
	// 说明句在首行之后、正文之前。
	if firstLine := strings.Index(msg, "子线程：bee-1"); firstLine < 0 ||
		strings.Index(msg, "已自动转交给你") > firstLine {
		t.Fatalf("notice must sit before the body: %s", msg)
	}
	for _, want := range []string{"bee-1", "req-1", "q1", "范围", "后端", "可以自填其他答案", "```swarm-answer"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %s", want, msg)
		}
	}
	if _, found, _ := ParseDelegations(msg); found {
		t.Fatal("auto-forwarded question message must not parse as a delegation")
	}

	// 不足一分钟时按秒显示。
	short := BuildAutoForwardQuestionMessage("", "bee-1", "req-1", 45*time.Second, qs)
	if !strings.Contains(short, "用户 45 秒内未回答") || !strings.Contains(short, "子线程「bee-1」") {
		t.Fatalf("unexpected short-wait message: %s", short)
	}

	// 等待时长为 0 时与手动转交完全一致。
	if got, want := BuildAutoForwardQuestionMessage("Ives", "bee-1", "req-1", 0, qs), BuildForwardQuestionMessage("Ives", "bee-1", "req-1", qs); got != want {
		t.Fatalf("zero wait must match manual forward:\n%s\n---\n%s", got, want)
	}
}

func TestBuildForwardQuestionMessageCarriesIDs(t *testing.T) {
	msg := BuildForwardQuestionMessage("Ives", "bee-1", "req-1", []ForwardQuestion{{
		ID:             "q1",
		Header:         "范围",
		Prompt:         "要改哪个模块？",
		Choices:        []string{"后端", "前端"},
		AllowsMultiple: true,
		AllowsOther:    false,
	}})
	if !strings.HasPrefix(msg, NoticePrefix+"子代理提问：") {
		t.Fatalf("unexpected prefix: %s", msg)
	}
	for _, want := range []string{"Ives", "bee-1", "req-1", "q1", "范围", "要改哪个模块？", "后端", "可以多选", "不能自填其他答案", "```swarm-answer"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %s", want, msg)
		}
	}
	if _, found, _ := ParseDelegations(msg); found {
		t.Fatal("forwarded question message must not parse as a delegation")
	}
}

// TestParseBeeAnswers_LegacyFenceAndKey 验证旧围栏 ```hydra-answer 与旧键 head 仍然能解析。
func TestParseBeeAnswers_LegacyFenceAndKey(t *testing.T) {
	text := "```hydra-answer\n" + `{"head": "bee-9", "request": "req-9", "answers": {"q9": ["A"]}}` + "\n```\n"
	got := ParseBeeAnswers(text)
	if len(got) != 1 {
		t.Fatalf("旧围栏旧键应仍能解析, got %d: %+v", len(got), got)
	}
	if got[0].BeeThreadID != "bee-9" || got[0].RequestID != "req-9" || len(got[0].Answers["q9"]) != 1 {
		t.Fatalf("旧键 head 未被识别: %+v", got[0])
	}

	// 旧围栏的答案块整体也不能被当成派发块。
	if _, found, _ := ParseDelegations(text); found {
		t.Fatal("旧围栏答案块不应被当成派发块")
	}
}

// TestParseBeeAnswers_BeeKeyWins 验证 bee 与旧键 head 同时出现时以 bee 为准。
func TestParseBeeAnswers_BeeKeyWins(t *testing.T) {
	text := "```swarm-answer\n" + `{"bee": "bee-2", "head": "bee-1", "request": "req-1", "skip": true}` + "\n```\n"
	got := ParseBeeAnswers(text)
	if len(got) != 1 || got[0].BeeThreadID != "bee-2" {
		t.Fatalf("bee 应优先于旧键 head: %+v", got)
	}
}

// TestBuildForwardQuestionMessageUsesNewFenceAndKey 验证系统生成的答案块示例只用新围栏与新键。
func TestBuildForwardQuestionMessageUsesNewFenceAndKey(t *testing.T) {
	msg := BuildForwardQuestionMessage("Ives", "bee-1", "req-1", []ForwardQuestion{{ID: "q1", Prompt: "要改哪个模块？"}})
	if !strings.Contains(msg, "```swarm-answer") {
		t.Fatalf("答案块示例应使用 swarm-answer 围栏:\n%s", msg)
	}
	if !strings.Contains(msg, `{"bee":`) || strings.Contains(msg, `{"head":`) {
		t.Fatalf("答案块示例应使用 bee 键:\n%s", msg)
	}
	// 生成的示例本身必须能被解析回同一条答案。
	example := msg[strings.Index(msg, "```swarm-answer"):]
	if idx := strings.Index(example[len("```swarm-answer"):], "```"); idx > 0 {
		example = example[:len("```swarm-answer")+idx]
	}
	if parsed := ParseBeeAnswers(example); len(parsed) != 1 || parsed[0].BeeThreadID != "bee-1" {
		t.Fatalf("示例答案块应能被解析: %+v", parsed)
	}
}
