package mcodeui

import "testing"

// 总管最新一条定稿回复第一行就是「需要你决定：…」时命中。
func TestStewardPendingDecisionHit(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "看下构建为什么挂了"},
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "需要你决定：要不要直接回滚这次发版\n细节如下。"},
	}

	id, title, ok := stewardPendingDecision(messages)
	if !ok {
		t.Fatal("最新一轮写了「需要你决定：」，应命中")
	}
	if id != "a1" {
		t.Fatalf("命中消息 ID = %q，想要 %q", id, "a1")
	}
	if title != "要不要直接回滚这次发版" {
		t.Fatalf("标题 = %q，想要 %q", title, "要不要直接回滚这次发版")
	}
}

// 总管说完之后用户又开口了，说明那件事已经得到回应，不再显示置顶条。
func TestStewardPendingDecisionAnsweredByUser(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "需要你决定：要不要回滚发版"},
		{ID: "u2", Role: "user", Text: "回滚"},
	}

	if _, _, ok := stewardPendingDecision(messages); ok {
		t.Fatal("用户已经开口，不该再命中")
	}
}

// 总管唤醒消息与宿主事件是系统输入，落在后面也不算用户开口。
func TestStewardPendingDecisionIgnoresHiddenTrigger(t *testing.T) {
	for _, kind := range []string{stewardWakeKind, hostEventKind} {
		messages := []Message{
			{ID: "a1", Role: "assistant", IsFinal: true, Text: "需要你决定：要不要回滚发版"},
			{ID: "u2", Role: "user", Kind: kind, Text: "宿主转来"},
		}
		if id, _, ok := stewardPendingDecision(messages); !ok || id != "a1" {
			t.Fatalf("kind=%q 是系统输入，应仍命中 a1，实际 ok=%v id=%q", kind, ok, id)
		}
	}
}

// 第一行不是那个句式时不命中，即使正文后面提到了「需要你决定」。
func TestStewardPendingDecisionOtherFirstLine(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "都办好了。\n需要你决定：要不要回滚发版"},
	}

	if _, _, ok := stewardPendingDecision(messages); ok {
		t.Fatal("第一行不是该句式，不该命中")
	}
}

// 句式后面没有内容时不算一条决策。
func TestStewardPendingDecisionEmptyTitle(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "需要你决定：   \n细节如下。"},
	}

	if _, _, ok := stewardPendingDecision(messages); ok {
		t.Fatal("句式后为空，不该命中")
	}
}

// 标题带 ** 加粗、行首带 markdown 标记时也能解析出干净的一句话。
func TestStewardPendingDecisionStripsMarkdown(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "## 需要你决定：**先回滚还是先修**\n细节如下。"},
	}

	id, title, ok := stewardPendingDecision(messages)
	if !ok || id != "a1" {
		t.Fatalf("应命中 a1，实际 ok=%v id=%q", ok, id)
	}
	if title != "先回滚还是先修" {
		t.Fatalf("标题 = %q，想要 %q", title, "先回滚还是先修")
	}
}

// 还没定稿的流式回复不算待决定，继续往前找上一条定稿的。
func TestStewardPendingDecisionSkipsUnfinal(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: "assistant", IsFinal: true, Text: "需要你决定：要不要回滚发版"},
		{ID: "a2", Role: "assistant", IsFinal: false, Text: "需要你决定：还没写完"},
	}

	id, title, ok := stewardPendingDecision(messages)
	if !ok || id != "a1" {
		t.Fatalf("未定稿的消息应跳过，命中 a1，实际 ok=%v id=%q", ok, id)
	}
	if title != "要不要回滚发版" {
		t.Fatalf("标题 = %q，想要 %q", title, "要不要回滚发版")
	}
}
