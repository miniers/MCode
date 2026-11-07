package steward

import (
	"context"
	"strings"
	"testing"
)

// TestSplitPreambleUsesSplitIntro 验证分割前言用分割版开头说明，正文段落与换新前言共用。
func TestSplitPreambleUsesSplitIntro(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()

	rotation := rig.rt.RotationPreamble(ctx)
	split := rig.rt.SplitPreamble(ctx)

	if !strings.Contains(split, splitIntro) {
		t.Fatalf("分割前言缺少分割版开头说明:\n%s", split)
	}
	if strings.Contains(split, rotationIntro) {
		t.Fatalf("分割前言不应带换新版开头说明:\n%s", split)
	}
	if !strings.Contains(rotation, rotationIntro) || strings.Contains(rotation, splitIntro) {
		t.Fatalf("换新前言应仍用换新版开头说明:\n%s", rotation)
	}
	// 正文段落共用：两个前言都要有同样的段落标题。
	for _, heading := range []string{"## 交接摘要", "## 账本", "## 话题索引", "## 未消解事件"} {
		if !strings.Contains(split, heading) {
			t.Fatalf("分割前言缺少 %q:\n%s", heading, split)
		}
		if !strings.Contains(rotation, heading) {
			t.Fatalf("换新前言缺少 %q:\n%s", heading, rotation)
		}
	}
	// 除开头说明外，正文完全一致。
	if tail := strings.TrimPrefix(split, splitIntro); tail != strings.TrimPrefix(rotation, rotationIntro) {
		t.Fatalf("分割与换新的正文应完全一致:\n分割: %s\n换新: %s", tail, strings.TrimPrefix(rotation, rotationIntro))
	}
}
