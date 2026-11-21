package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
)

func TestUIReview35ReviewDraftKept(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	_, tt, dp := newReviewTester(t, fake)

	clickText(t, tt, "a.go")
	waitFor(t, tt, func() bool { return len(dp.views) == 1 })
	tt.Advance(DurSheet + 50*time.Millisecond)

	var view *reviewDiffView
	for _, item := range dp.views {
		view = item
	}
	if view == nil || view.state.OnGutterButton == nil {
		t.Fatal("没有加载 diff 视图或评论回调")
	}

	lineA := editor.DiffLineRange{Side: editor.DiffOld, Start: 0, End: 0}
	lineB := editor.DiffLineRange{Side: editor.DiffOld, Start: 1, End: 1}

	// 1. A 行写正文后切到 B 行，再切回 A 行，正文恢复。
	view.state.OnGutterButton(lineA)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("A行草稿")
	tt.Frame()
	if dp.commentDraft == nil || dp.commentDraft.body != "A行草稿" {
		t.Fatalf("写入 A 行草稿失败: %+v", dp.commentDraft)
	}

	view.state.OnGutterButton(lineB)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if dp.commentDraft == nil || dp.commentDraft.lines != lineB || dp.commentDraft.body != "" {
		t.Fatalf("切到 B 行后草稿未清空: %+v", dp.commentDraft)
	}
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("B行草稿")
	tt.Frame()

	view.state.OnGutterButton(lineA)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if dp.commentDraft == nil || dp.commentDraft.lines != lineA || dp.commentDraft.body != "A行草稿" {
		t.Fatalf("切回 A 行后正文未恢复: %+v", dp.commentDraft)
	}

	// 2. 点外面关闭浮层后再点回同一行，正文恢复。
	tt.ClickAt(1, 1)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if dp.commentDraft != nil && dp.commentDraft.open {
		t.Fatal("点击外部后草稿浮层未关闭")
	}

	view.state.OnGutterButton(lineA)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if dp.commentDraft == nil || !dp.commentDraft.open || dp.commentDraft.lines != lineA || dp.commentDraft.body != "A行草稿" {
		t.Fatalf("关闭浮层再点回 A 行后正文未恢复: %+v", dp.commentDraft)
	}

	// 3. 添加评论后再点该行，草稿为空。
	clickText(t, tt, "添加")
	tt.Advance(DurBase + 50*time.Millisecond)
	if !tt.HasText("A行草稿") {
		t.Fatal("添加后评论注解不可见")
	}
	diffPanelMu.Lock()
	commentCount := len(dp.comments)
	diffPanelMu.Unlock()
	if commentCount != 1 {
		t.Fatalf("期望已添加 1 条评论，实际 %d 条", commentCount)
	}

	view.state.OnGutterButton(lineA)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if dp.commentDraft == nil || !dp.commentDraft.open || dp.commentDraft.lines != lineA || dp.commentDraft.body != "" {
		t.Fatalf("添加评论后再点 A 行草稿应为空: %+v", dp.commentDraft)
	}
}
