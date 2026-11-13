package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// 时间线里 Kind 为 split_session 的消息渲染成分割线，线上的历史照常显示。
func TestSplitDivider_RendersAndKeepsEarlierMessages(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "分割前的问题"},
		{ID: "m2", Role: "assistant", Text: "分割前的回答"},
		{ID: "m3", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice},
		{ID: "m4", Role: "user", Text: "分割后的问题"},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("分割前的问题") && tt.HasText("分割后的问题")
	})
	if !tt.HasText(splitSessionNotice) || !tt.HasText("分割前的回答") {
		t.Fatalf("分割线与历史消息应保持可见，当前文本: %q", tt.Texts())
	}
}

// splitDividerText 把正文拆成标题与说明：空正文退回常量，单行只有标题，
// 多行的剩余内容压成一行说明。
func TestSplitDividerText(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantTitle  string
		wantDetail string
	}{
		{name: "空正文退回常量", text: "  ", wantTitle: splitSessionNotice},
		{name: "单行只有标题", text: " " + splitSessionNotice + "\n", wantTitle: splitSessionNotice},
		{name: "两行拆出说明", text: splitSessionNotice + "\n已带走：账本 2 条、话题 1 个、待处理 3 条", wantTitle: splitSessionNotice, wantDetail: "已带走：账本 2 条、话题 1 个、待处理 3 条"},
		{name: "多行说明压成一行", text: splitSessionNotice + "\n已带走：账本 2 条、\n话题 1 个", wantTitle: splitSessionNotice, wantDetail: "已带走：账本 2 条、 话题 1 个"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, detail := splitDividerText(tc.text)
			if title != tc.wantTitle || detail != tc.wantDetail {
				t.Fatalf("splitDividerText = (%q, %q), want (%q, %q)", title, detail, tc.wantTitle, tc.wantDetail)
			}
		})
	}
}

// 带计数的分割线在时间线上渲染出标题与「已带走」两段文字。
func TestSplitDivider_RendersCarriedCounts(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(waitUIStateWrites)
	const counts = "已带走：账本 12 条、话题 3 个、待处理 2 条"
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "分割前的问题", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "m2", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice + "\n" + counts},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("分割前的问题") && tt.HasText(counts) })
	if !tt.HasText(splitSessionNotice) {
		t.Fatalf("缺少分割线标题: %q", tt.Texts())
	}
}
