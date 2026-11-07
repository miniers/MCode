package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// systemNoticeTestWidth 是窄内容宽度：短通知放得下，长通知的整段文字放不下。
// 测试窗口比它宽，好让超出的药丸不被窗口裁掉，宽度断言才能看出溢出。
const systemNoticeTestWidth = 320

// systemNoticeTestRects 保存一条系统通知渲染后按 Label 定位到的元素矩形。
type systemNoticeTestRects struct {
	pill  ui.Rect
	text  ui.Rect
	left  ui.Rect
	right ui.Rect
}

// renderSystemNoticeForTest 在窄内容列里渲染一条系统通知，返回 Tester 与药丸、正文、左右发丝线的矩形。
// 用 Label 而不是 Key 定位，避免测试绑定内部 key 命名。
func renderSystemNoticeForTest(t *testing.T, id, text string) (*ui.Tester, systemNoticeTestRects) {
	t.Helper()
	s := &chatState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("sys-notice-test-col")).Width(systemNoticeTestWidth).Children(func() {
			renderSystemMessage(c, s, Message{ID: id, Role: "system", Text: text})
		})
	}, 800, 200)
	find := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("未找到 %q，当前文本：%v", label, tt.Texts())
		}
		return r
	}
	return tt, systemNoticeTestRects{
		pill:  find("sys-pill-" + id),
		text:  find(text),
		left:  find("sys-rule-left-" + id),
		right: find("sys-rule-right-" + id),
	}
}

// assertRulesCentered 校验两条发丝线的纵向中心与药丸一致（允许半像素取整误差）。
func assertRulesCentered(t *testing.T, rects systemNoticeTestRects) {
	t.Helper()
	pillMid := rects.pill.Y + rects.pill.H/2
	for name, r := range map[string]ui.Rect{"左": rects.left, "右": rects.right} {
		if mid := r.Y + r.H/2; mid < pillMid-1 || mid > pillMid+1 {
			t.Fatalf("%s发丝线中心 %.1f 与药丸中心 %.1f 未垂直对齐", name, mid, pillMid)
		}
	}
}

// TestRenderSystemMessageLongTextShrinksPill 验证长文本通知在窄宽度下把药丸压回行内、
// 正文单行截断不再溢出药丸，两条发丝线宽度为 0（不可见），药丸仍与发丝线垂直居中对齐。
func TestRenderSystemMessageLongTextShrinksPill(t *testing.T) {
	const text = "[编排] 已在本地提交 3cf1882；自动推送未开启，可在 Git 菜单手动推送"
	tt, rects := renderSystemNoticeForTest(t, "sys-notice-long", text)
	if !tt.HasText(text) {
		t.Fatalf("长文本通知未渲染，当前文本：%v", tt.Texts())
	}
	if rects.pill.X < -0.5 || rects.pill.W > systemNoticeTestWidth+0.5 || rects.pill.X+rects.pill.W > systemNoticeTestWidth+0.5 {
		t.Fatalf("药丸 [%.1f, %.1f] 超出内容宽度 %d", rects.pill.X, rects.pill.X+rects.pill.W, systemNoticeTestWidth)
	}
	if rects.text.H > rects.pill.H+0.5 {
		t.Fatalf("正文高 %.1f 超过药丸高 %.1f，说明文字换行溢出药丸", rects.text.H, rects.pill.H)
	}
	if rects.left.W > 0.5 || rects.right.W > 0.5 {
		t.Fatalf("药丸占满行时发丝线应不可见，左宽 %.1f、右宽 %.1f", rects.left.W, rects.right.W)
	}
	assertRulesCentered(t, rects)
}

// TestRenderSystemMessageShortTextKeepsRules 验证短文本通知外观不变：药丸两侧仍有发丝线，
// 发丝线在药丸左右留出间隙，并与药丸垂直居中对齐。
func TestRenderSystemMessageShortTextKeepsRules(t *testing.T) {
	const text = "已保存到草稿"
	_, rects := renderSystemNoticeForTest(t, "sys-notice-short", text)
	if rects.left.W <= 0.5 || rects.right.W <= 0.5 {
		t.Fatalf("短文本通知两侧应保留发丝线，左宽 %.1f、右宽 %.1f", rects.left.W, rects.right.W)
	}
	if rects.left.X+rects.left.W > rects.pill.X+0.5 {
		t.Fatalf("左发丝线 [%.1f, %.1f] 压到药丸左边界 %.1f", rects.left.X, rects.left.X+rects.left.W, rects.pill.X)
	}
	if rects.right.X < rects.pill.X+rects.pill.W-0.5 {
		t.Fatalf("右发丝线起点 %.1f 压到药丸右边界 %.1f", rects.right.X, rects.pill.X+rects.pill.W)
	}
	if rects.text.H > rects.pill.H+0.5 {
		t.Fatalf("短文本正文高 %.1f 超过药丸高 %.1f", rects.text.H, rects.pill.H)
	}
	assertRulesCentered(t, rects)
}
