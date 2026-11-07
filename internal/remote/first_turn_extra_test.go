package remote

import (
	"strings"
	"testing"
)

// TestFirstTurnExtraOnlyInFirstTurn 验证额外的首轮段落（普通会话的移交提示）只在首轮
// 随指导词一起前置，第二轮及之后不再出现，避免随历史累积。
func TestFirstTurnExtraOnlyInFirstTurn(t *testing.T) {
	rig := newPersonaTestRig(t)
	rig.backend.SetFirstTurnExtra(func(string) string { return "EXTRA" })

	threadID := rig.newThread(t, "sonnet")

	first := rig.sendTurn(t, threadID, "第一条消息")
	if !strings.Contains(first.Prompt, "EXTRA") {
		t.Fatalf("首轮提示词应包含额外段落: %q", first.Prompt)
	}
	// 指导词在前、额外段落在后，两段之间空一行。
	if !strings.Contains(first.Prompt, "GUIDANCE\n\nEXTRA") {
		t.Fatalf("额外段落应排在指导词之后: %q", first.Prompt)
	}

	second := rig.sendTurn(t, threadID, "第二条消息")
	if strings.Contains(second.Prompt, "EXTRA") {
		t.Fatalf("第二轮提示词不应再包含额外段落: %q", second.Prompt)
	}
}

// TestFirstTurnExtraEmptyKeepsGuidance 验证额外段落返回空串时只注入指导词。
func TestFirstTurnExtraEmptyKeepsGuidance(t *testing.T) {
	rig := newPersonaTestRig(t)
	rig.backend.SetFirstTurnExtra(func(string) string { return "" })

	threadID := rig.newThread(t, "sonnet")
	turn := rig.sendTurn(t, threadID, "第一条消息")
	// 简洁回复模式下提示词里可能还有别的前置段落，所以不假设指导词在最开头；
	// 只要求指导词后面直接跟用户消息（中间没有额外段落），且指导词只出现一次。
	if !strings.Contains(turn.Prompt, "GUIDANCE\n\n第一条消息") {
		t.Fatalf("额外段落为空时应只前置指导词: %q", turn.Prompt)
	}
	if n := strings.Count(turn.Prompt, "GUIDANCE"); n != 1 {
		t.Fatalf("指导词应只出现一次, got %d 次: %q", n, turn.Prompt)
	}
}
