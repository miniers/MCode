package mcodeui

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

// confirmCounters 记录确认弹窗回调的触发次数，用来断言「只触发一次」。
type confirmCounters struct {
	confirm int
	cancel  int
	extra   int
}

// accentSwatch 在弹窗 Footer 里画一个主色色块（label 为 accent-swatch），
// 供像素断言拿主色做对照，避免在测试里硬编码主题色。
func accentSwatch(c *ui.Context) {
	ui.Box(c.Key("confirm-test-accent-swatch")).
		Width(12).
		Height(12).
		Background(ThemeAccent(c)).
		Label("accent-swatch")
}

// withAccentSwatch 给弹窗挂上主色对照色块 Footer：Footer 是无参回调，
// 色块却需要渲染用的 *ui.Context，所以在渲染时用闭包把它带上。
func withAccentSwatch(c *ui.Context, d ConfirmDialog) ConfirmDialog {
	d.Footer = func() { accentSwatch(c) }
	return d
}

// confirmPixel 取矩形左侧内缘、竖直居中处的像素：按钮的填充色，避开中间的文字。
// 横向取 4px：按钮的横向内距最小是 Space2(6)，主按钮的文字从内距处开始，
// 取 8px 会落到第一个字的抗锯齿笔画上（MD 按钮的内距正好是 Space3(8)）。
func confirmPixel(t *testing.T, img *image.RGBA, r ui.Rect) [3]int {
	t.Helper()
	x := int(r.X) + 4
	y := int(r.Y + r.H/2)
	if x >= img.Rect.Dx() || y >= img.Rect.Dy() {
		t.Fatalf("采样点 (%d,%d) 超出画面 %v", x, y, img.Rect)
	}
	i := img.PixOffset(x, y)
	return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
}

// colorNear 判断两个像素是否在容差内相同。
func colorNear(a, b [3]int, tol int) bool {
	for i := 0; i < 3; i++ {
		if d := a[i] - b[i]; d > tol || d < -tol {
			return false
		}
	}
	return true
}

// TestConfirmDialogDangerTone 危险态：取消在左、确认在右，确认按钮不用主色（破坏性样式），点击走确认回调。
func TestConfirmDialogDangerTone(t *testing.T) {
	cnt := &confirmCounters{}
	d := ConfirmDialog{
		Key:          "test-danger",
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "删除这个会话？",
		Body:         "会话记录会一并删除，删除后无法恢复。",
		ConfirmLabel: "删除",
		ConfirmA11y:  "确认删除",
		CancelLabel:  "取消",
		OnConfirm:    func() { cnt.confirm++ },
		OnCancel:     func() { cnt.cancel++ },
	}
	tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, withAccentSwatch(c, d)) }, 800, 600)

	if !tt.HasText("删除这个会话？") {
		t.Fatal("危险态弹窗没有显示标题")
	}
	confirmRect, ok := tt.Find("确认删除")
	if !ok {
		t.Fatal("没找到确认按钮")
	}
	cancelRect, ok := tt.Find("取消")
	if !ok {
		t.Fatal("没找到取消按钮")
	}
	// 按钮层级：取消在左、确认在最右。
	if cancelRect.X >= confirmRect.X {
		t.Fatalf("取消按钮应在确认按钮左侧：cancel.X=%v confirm.X=%v", cancelRect.X, confirmRect.X)
	}
	img := tt.Image()
	swatch, ok := tt.Find("accent-swatch")
	if !ok {
		t.Fatal("没找到主色对照色块")
	}
	accent := confirmPixel(t, img, swatch)
	if got := confirmPixel(t, img, confirmRect); colorNear(got, accent, 8) {
		t.Fatalf("危险态的确认按钮不应用主色：按钮像素 %v 接近主色 %v", got, accent)
	}

	if err := tt.Click("确认删除"); err != nil {
		t.Fatalf("点击确认失败: %v", err)
	}
	if cnt.confirm != 1 || cnt.cancel != 0 {
		t.Fatalf("确认回调次数错误: confirm=%d cancel=%d", cnt.confirm, cnt.cancel)
	}
}

// TestConfirmDialogSafeUsesPrimaryStyle 安全态：确认按钮用主色，且「取消 < 第三个动作 < 确认」。
func TestConfirmDialogSafeUsesPrimaryStyle(t *testing.T) {
	cnt := &confirmCounters{}
	d := ConfirmDialog{
		Key:          "test-safe",
		Open:         true,
		Tone:         ConfirmSafe,
		Title:        "保存这次编辑？",
		Body:         "有未保存的修改，要先保存吗？",
		ConfirmLabel: "保存并离开",
		ConfirmA11y:  "确认保存",
		ExtraLabel:   "放弃",
		CancelLabel:  "取消",
		OnConfirm:    func() { cnt.confirm++ },
		OnCancel:     func() { cnt.cancel++ },
		OnExtra:      func() { cnt.extra++ },
	}
	tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, withAccentSwatch(c, d)) }, 800, 600)

	confirmRect, ok := tt.Find("确认保存")
	if !ok {
		t.Fatal("没找到确认按钮")
	}
	cancelRect, ok := tt.Find("取消")
	if !ok {
		t.Fatal("没找到取消按钮")
	}
	extraRect, ok := tt.Find("放弃")
	if !ok {
		t.Fatal("没找到第三个动作按钮")
	}
	if !(cancelRect.X < extraRect.X && extraRect.X < confirmRect.X) {
		t.Fatalf("按钮顺序应为 取消 < 放弃 < 确认：cancel=%v extra=%v confirm=%v",
			cancelRect.X, extraRect.X, confirmRect.X)
	}

	img := tt.Image()
	swatch, ok := tt.Find("accent-swatch")
	if !ok {
		t.Fatal("没找到主色对照色块")
	}
	accent := confirmPixel(t, img, swatch)
	if got := confirmPixel(t, img, confirmRect); !colorNear(got, accent, 2) {
		t.Fatalf("安全态的确认按钮应用主色：按钮像素 %v，主色 %v", got, accent)
	}

	if err := tt.Click("放弃"); err != nil {
		t.Fatalf("点击第三个动作失败: %v", err)
	}
	if cnt.extra != 1 || cnt.confirm != 0 || cnt.cancel != 0 {
		t.Fatalf("第三个动作回调次数错误: extra=%d confirm=%d cancel=%d", cnt.extra, cnt.confirm, cnt.cancel)
	}
}

// TestConfirmDialogNoticeHasNoCancel 提示态：没有取消按钮，只有「知道了」，Esc 等同按下它。
func TestConfirmDialogNoticeHasNoCancel(t *testing.T) {
	cnt := &confirmCounters{}
	d := ConfirmDialog{
		Key:       "test-notice",
		Open:      true,
		Tone:      ConfirmNotice,
		Title:     "配对码已刷新",
		Body:      "新配对码 5 分钟内有效。",
		OnConfirm: func() { cnt.confirm++ },
		OnCancel:  func() { cnt.cancel++ },
	}
	tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, d) }, 800, 600)

	if _, ok := tt.Find("取消"); ok {
		t.Fatal("提示态不应有取消按钮")
	}
	if !tt.HasText("知道了") {
		t.Fatal("提示态应显示「知道了」按钮")
	}
	if err := tt.Click("知道了"); err != nil {
		t.Fatalf("点击「知道了」失败: %v", err)
	}
	if cnt.confirm != 1 || cnt.cancel != 0 {
		t.Fatalf("「知道了」回调次数错误: confirm=%d cancel=%d", cnt.confirm, cnt.cancel)
	}

	// Esc 关闭提示态，语义等同按下「知道了」。
	tt.Key(0, ui.KeyEscape)
	if cnt.confirm != 2 || cnt.cancel != 0 {
		t.Fatalf("Esc 应关闭提示态: confirm=%d cancel=%d", cnt.confirm, cnt.cancel)
	}
}

// TestConfirmDialogEnterAndEsc 键盘语义：Enter 确认（确认按钮持有焦点或焦点已被移开都一样），
// Esc 取消，各只触发一次。
func TestConfirmDialogEnterAndEsc(t *testing.T) {
	enterCnt := &confirmCounters{}
	enterDialog := ConfirmDialog{
		Key:          "test-enter",
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "移除证书？",
		Body:         "移除后 HTTPS 入口会立即下线。",
		ConfirmLabel: "移除证书",
		CancelLabel:  "取消",
		OnConfirm:    func() { enterCnt.confirm++ },
		OnCancel:     func() { enterCnt.cancel++ },
	}
	// Footer 里放一个可聚焦但不可点击的色块：点它只挪焦点，不触发任何回调，
	// 用来覆盖「焦点不在确认按钮上」时 Enter 仍走窗口快捷键确认的路径。
	tt := ui.NewTester(func(c *ui.Context) {
		d := enterDialog
		d.Footer = func() {
			ui.Box(c.Key("confirm-test-focus-target")).
				Width(40).
				Height(12).
				Focusable().
				Label("focus-target")
		}
		RenderConfirmDialog(c, d)
	}, 800, 600)
	tt.Key(0, ui.KeyEnter)
	if enterCnt.confirm != 1 || enterCnt.cancel != 0 {
		t.Fatalf("Enter 应触发确认且只触发一次: confirm=%d cancel=%d", enterCnt.confirm, enterCnt.cancel)
	}

	// 把焦点挪出确认按钮后再按 Enter，仍只触发确认。
	if err := tt.Click("focus-target"); err != nil {
		t.Fatalf("点击焦点目标失败: %v", err)
	}
	enterCnt.confirm, enterCnt.cancel = 0, 0
	tt.Key(0, ui.KeyEnter)
	if enterCnt.confirm != 1 || enterCnt.cancel != 0 {
		t.Fatalf("焦点不在确认按钮时 Enter 也应只触发确认: confirm=%d cancel=%d", enterCnt.confirm, enterCnt.cancel)
	}

	escCnt := &confirmCounters{}
	escDialog := enterDialog
	escDialog.Key = "test-esc"
	escDialog.OnConfirm = func() { escCnt.confirm++ }
	escDialog.OnCancel = func() { escCnt.cancel++ }
	tt2 := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, escDialog) }, 800, 600)
	tt2.Key(0, ui.KeyEscape)
	if escCnt.cancel != 1 || escCnt.confirm != 0 {
		t.Fatalf("Esc 应触发取消且只触发一次: confirm=%d cancel=%d", escCnt.confirm, escCnt.cancel)
	}

	// 提示态：Enter 只关闭，语义等同按「知道了」，不当作取消。
	noticeCnt := &confirmCounters{}
	noticeDialog := ConfirmDialog{
		Key:       "test-enter-notice",
		Open:      true,
		Tone:      ConfirmNotice,
		Title:     "配对码已刷新",
		Body:      "新配对码 5 分钟内有效。",
		OnConfirm: func() { noticeCnt.confirm++ },
		OnCancel:  func() { noticeCnt.cancel++ },
	}
	tt3 := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, noticeDialog) }, 800, 600)
	tt3.Key(0, ui.KeyEnter)
	if noticeCnt.confirm != 1 || noticeCnt.cancel != 0 {
		t.Fatalf("提示态 Enter 应只关闭: confirm=%d cancel=%d", noticeCnt.confirm, noticeCnt.cancel)
	}
}

// TestConfirmDialogDisabledConfirmIsInert 确认按钮禁用时（如删除进行中），Enter 与点击都不应触发确认。
func TestConfirmDialogDisabledConfirmIsInert(t *testing.T) {
	cnt := &confirmCounters{}
	d := ConfirmDialog{
		Key:             "test-disabled",
		Open:            true,
		Tone:            ConfirmDanger,
		Title:           "删除话题",
		Body:            "话题记录与账本条目会一并移除。",
		ConfirmLabel:    "删除",
		CancelLabel:     "取消",
		ConfirmDisabled: true,
		OnConfirm:       func() { cnt.confirm++ },
		OnCancel:        func() { cnt.cancel++ },
	}
	tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, d) }, 800, 600)

	tt.Key(0, ui.KeyEnter)
	if cnt.confirm != 0 {
		t.Fatalf("禁用时 Enter 不应触发确认: confirm=%d", cnt.confirm)
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("点击确认按钮失败: %v", err)
	}
	if cnt.confirm != 0 {
		t.Fatalf("禁用时点击不应触发确认: confirm=%d", cnt.confirm)
	}
}

// TestConfirmDialogClicksByAccessibilityLabel 迁移到 Button 后按钮的无障碍名称必须保持不变：
// 这里让 A11y 标签与可见文字不同，再按 Label 定位并点击，断言两种点击各触发一次回调。
func TestConfirmDialogClicksByAccessibilityLabel(t *testing.T) {
	newDialog := func(cnt *confirmCounters) ConfirmDialog {
		return ConfirmDialog{
			Key:          "test-a11y-label",
			Open:         true,
			Tone:         ConfirmSafe,
			Title:        "写入工作记忆",
			Body:         "这条记忆会加入项目待审列表。",
			ConfirmLabel: "写入",
			ConfirmA11y:  "确认写入工作记忆",
			CancelLabel:  "先放着",
			CancelA11y:   "取消写入工作记忆",
			OnConfirm:    func() { cnt.confirm++ },
			OnCancel:     func() { cnt.cancel++ },
		}
	}

	// 弹窗触发一次动作后就关闭（closed 去重），所以两个按钮各用一次独立渲染。
	t.Run("确认", func(t *testing.T) {
		cnt := &confirmCounters{}
		tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, newDialog(cnt)) }, 800, 600)

		if err := tt.Click("确认写入工作记忆"); err != nil {
			t.Fatalf("按无障碍名称点击确认失败: %v", err)
		}
		if cnt.confirm != 1 || cnt.cancel != 0 {
			t.Fatalf("确认回调次数错误: confirm=%d cancel=%d", cnt.confirm, cnt.cancel)
		}
	})

	t.Run("取消", func(t *testing.T) {
		cnt := &confirmCounters{}
		tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, newDialog(cnt)) }, 800, 600)

		if err := tt.Click("取消写入工作记忆"); err != nil {
			t.Fatalf("按无障碍名称点击取消失败: %v", err)
		}
		if cnt.cancel != 1 || cnt.confirm != 0 {
			t.Fatalf("取消回调次数错误: confirm=%d cancel=%d", cnt.confirm, cnt.cancel)
		}
	})
}

// TestConfirmDialogWidthFollowsContent 短文案 400、长文案 520、带明细 720；
// 800 宽窗口里卡片居中，确认按钮右缘因此分别落在 580 / 640 / 740（卡片内距 Space6）。
func TestConfirmDialogWidthFollowsContent(t *testing.T) {
	cases := []struct {
		name  string
		d     ConfirmDialog
		right float32
	}{
		{
			name: "短文案 400",
			d: ConfirmDialog{
				Key:          "test-width-short",
				Open:         true,
				Tone:         ConfirmDanger,
				Title:        "删除这个会话？",
				Body:         "会话记录会一并删除，删除后无法恢复。",
				ConfirmLabel: "删除",
				ConfirmA11y:  "确认删除",
				CancelLabel:  "取消",
			},
			right: 580,
		},
		{
			name: "长文案 520",
			d: ConfirmDialog{
				Key:          "test-width-long",
				Open:         true,
				Tone:         ConfirmDanger,
				Title:        "同时删除这些会话？",
				Body:         "选中的会话记录、附件与运行日志会一并删除，删除后无法恢复。",
				ConfirmLabel: "删除",
				ConfirmA11y:  "确认删除",
				CancelLabel:  "取消",
			},
			right: 640,
		},
		{
			name: "带明细 720",
			d: ConfirmDialog{
				Key:          "test-width-detail",
				Open:         true,
				Tone:         ConfirmDanger,
				Title:        "清理这些工作副本？",
				Detail:       []string{"feature/one", "feature/two"},
				ConfirmLabel: "清理",
				ConfirmA11y:  "确认清理",
				CancelLabel:  "取消",
			},
			right: 740,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) { RenderConfirmDialog(c, tc.d) }, 800, 600)
			label := tc.d.ConfirmA11y
			rect, ok := tt.Find(label)
			if !ok {
				t.Fatalf("没找到确认按钮 %q", label)
			}
			if got := rect.X + rect.W; got < tc.right-2 || got > tc.right+2 {
				t.Fatalf("确认按钮右缘 %v，期望 %v（卡片宽度决定）", got, tc.right)
			}
		})
	}
}
