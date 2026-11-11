package mcodeui

import (
	"fmt"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 统一三态确认弹窗：危险 / 安全 / 提示。
//
// 外观（卡片、标题、正文、明细、按钮行）全部由本组件负责，外壳统一走 modalDragModal
// （内部是 ui.DialogBase）；调用方只填文案与回调，不要再自己搭卡片，否则卡片外会多一圈黑边。

// ConfirmTone 是确认弹窗的语气，决定确认按钮的样式与键盘语义。
type ConfirmTone int

const (
	// ConfirmDanger 危险操作：确认按钮用破坏性样式（红），Enter 确认、Esc 取消。
	ConfirmDanger ConfirmTone = iota
	// ConfirmSafe 安全操作：确认按钮用主色，Enter 确认、Esc 取消。
	ConfirmSafe
	// ConfirmNotice 纯提示：只有一个「知道了」按钮，Enter / Esc 都关闭。
	ConfirmNotice
)

// 确认弹窗宽度三档：短文案 400、长文案 520、超长或带明细 720；窗口太窄时再夹紧。
const (
	confirmDialogWidthShort  float32 = 400
	confirmDialogWidthLong   float32 = 520
	confirmDialogWidthWide   float32 = 720
	confirmDialogLongRunes           = 28
	confirmDialogWideRunes           = 60
	confirmDialogWindowInset float32 = 80
)

// modalDragContextLeave 是「保存这次编辑？」确认框的拖拽 key。
// 其余确认框复用 modal_drag.go 里已有的 modalDrag* 常量，这里不重复定义。
const modalDragContextLeave = "modal-drag-context-leave"

// ConfirmDialog 描述一个确认弹窗。零值 Tone 即 ConfirmDanger。
type ConfirmDialog struct {
	Key    string // 拖拽 key，同时用作元素 key 前缀
	Open   bool
	Tone   ConfirmTone
	Title  string
	Body   string
	Detail []string // 可选的逐条明细（如待清理的 worktree），放在限高的滚动区里

	ConfirmLabel string
	CancelLabel  string
	OnConfirm    func()
	OnCancel     func()

	// 以下字段用于迁移既有弹窗时保留原行为，新写的弹窗通常不用填。
	ConfirmA11y     string // 确认按钮的无障碍名，默认取 ConfirmLabel
	CancelA11y      string // 取消按钮的无障碍名，默认取 CancelLabel
	ExtraLabel      string // 可选的第三个动作（如「放弃」），排在确认与取消之间
	OnExtra         func()
	ConfirmDisabled bool   // 确认按钮禁用（如提交进行中）
	Footer          func() // 明细之后、按钮行之前的自定义内容（错误提示、必须勾选的复选框）
}

// RenderConfirmDialog 渲染确认弹窗；d.Open 为 false 时只复位拖拽偏移。
func RenderConfirmDialog(c *ui.Context, d ConfirmDialog) {
	key := d.Key
	if key == "" {
		key = "confirm-dialog"
	}
	if !d.Open {
		modalDragReset(key)
		return
	}

	// 一次渲染只执行一个回调：DialogBase 的 Esc / 点遮罩关闭与本组件自己的快捷键
	// 可能落在同一帧，用 closed 去重，避免 OnCancel 被调用两次。
	closed := false
	run := func(fn func()) {
		if closed || fn == nil {
			return
		}
		closed = true
		fn()
	}
	// 提示态只有「知道了」，Enter / Esc 都等同于按它。
	closeNotice := func() {
		if d.OnConfirm != nil {
			run(d.OnConfirm)
			return
		}
		run(d.OnCancel)
	}

	cancelText := d.CancelLabel
	if cancelText == "" {
		cancelText = "取消"
	}
	confirmText := d.ConfirmLabel
	if confirmText == "" {
		if d.Tone == ConfirmNotice {
			confirmText = "知道了"
		} else {
			confirmText = "确认"
		}
	}
	confirmVariant := ButtonPrimary
	if d.Tone == ConfirmDanger {
		confirmVariant = ButtonDanger
	}

	width, _ := c.Size()
	runes := utf8.RuneCountInString(d.Title + d.Body)
	panelWidth := confirmDialogWidthShort
	switch {
	case len(d.Detail) > 0 || runes > confirmDialogWideRunes:
		panelWidth = confirmDialogWidthWide
	case runes > confirmDialogLongRunes:
		panelWidth = confirmDialogWidthLong
	}

	open := true
	modalDragModal(c, key, &open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			if d.Tone == ConfirmNotice {
				closeNotice()
			} else {
				run(d.OnCancel)
			}
			return
		}
		if c.Shortcut(0, ui.KeyEnter) {
			if d.Tone == ConfirmNotice {
				closeNotice()
			} else if !d.ConfirmDisabled {
				run(d.OnConfirm)
			}
			return
		}

		ui.Column(c.Key("confirm-" + key + "-card")).
			Width(panelWidth).
			MaxWidth(width - confirmDialogWindowInset).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space6).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, key, ui.Row(c.Key("confirm-"+key+"-title-row")).FillWidth().Children(func() {
					ui.Text(c, d.Title).
						FontSize(uiFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				}))
				if d.Body != "" {
					ui.Text(c, d.Body).
						FontSize(uiFont(TypeBody)).
						LineHeight(1.45).
						TextColor(ThemeSecondaryText(c))
				}
				if len(d.Detail) > 0 {
					ui.Scroll(c.Key("confirm-" + key + "-detail-scroll")).
						FillWidth().
						MaxHeight(240).
						Children(func() {
							ui.Column(c.Key("confirm-" + key + "-detail")).
								FillWidth().
								Gap(Space2).
								Children(func() {
									for i, line := range d.Detail {
										itemKey := fmt.Sprintf("confirm-%s-detail-%d", key, i)
										ui.Row(c.Key(itemKey)).FillWidth().Children(func() {
											ui.Text(c, line).
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemePrimaryText(c))
										})
									}
								})
						})
				}
				if d.Footer != nil {
					d.Footer()
				}

				ui.Row(c.Key("confirm-" + key + "-actions")).
					FillWidth().
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						// 提示态没有取消与第三个动作，只有「知道了」。
						if d.Tone != ConfirmNotice {
							Button(c.Key("confirm-"+key+"-cancel"), ButtonOpts{
								Text:    cancelText,
								Label:   d.CancelA11y,
								Variant: ButtonSecondary,
								OnClick: func() { run(d.OnCancel) },
							})
							if d.ExtraLabel != "" && d.OnExtra != nil {
								extraVariant := ButtonSecondary
								if d.Tone == ConfirmDanger {
									extraVariant = ButtonDanger
								}
								Button(c.Key("confirm-"+key+"-extra"), ButtonOpts{
									Text:    d.ExtraLabel,
									Variant: extraVariant,
									OnClick: func() { run(d.OnExtra) },
								})
							}
						}
						// 确认按钮是弹窗的默认焦点：框架里获得焦点的按钮会先吃掉 Enter
						// （ui/input.go 的按钮点击分支），若焦点落在先渲染的「取消」上，
						// Enter 就会被当成取消；AutoFocus 让 Enter 走确认。
						Button(c.Key("confirm-"+key+"-confirm"), ButtonOpts{
							Text:     confirmText,
							Label:    d.ConfirmA11y,
							Variant:  confirmVariant,
							Disabled: d.ConfirmDisabled,
							OnClick:  func() { run(d.OnConfirm) },
						}).AutoFocus()
					})
			})
	})

	// 点遮罩关闭时 DialogBase 只把开关置回 false，这里补上取消回调。
	if !open {
		modalDragReset(key)
		if d.Tone == ConfirmNotice {
			closeNotice()
		} else {
			run(d.OnCancel)
		}
	}
}
