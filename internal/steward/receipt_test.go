package steward

import (
	"context"
	"strings"
	"testing"
)

// TestTopicConclusionTexts 校验结项回执里那句结论：没建看板目标时说清原因，
// 目标查不到时只说已结项，两者都不再拼「最终阶段：」前缀。
func TestTopicConclusionTexts(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()

	if got := rig.rt.topicConclusion(ctx, Topic{ThreadID: "topic-1", Title: "空话题"}); got != "已结项（没有建看板目标）" {
		t.Fatalf("没有看板目标时应说明未建目标，实际 %q", got)
	}

	got := rig.rt.topicConclusion(ctx, Topic{ThreadID: "topic-2", Title: "目标丢了", GoalID: "goal-missing"})
	if got != "已结项" {
		t.Fatalf("目标查不到时只写已结项，实际 %q", got)
	}
	if strings.Contains(got, "最终阶段") {
		t.Fatalf("文案里不应再出现「最终阶段」，实际 %q", got)
	}
}
