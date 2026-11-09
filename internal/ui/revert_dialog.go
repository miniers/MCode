package mcodeui

import (
	"context"
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
)

const (
	// revertDialogSideMargin 是弹窗卡片左右两侧留白之和（每侧 40）。
	revertDialogSideMargin float32 = 80
	// revertListMinHeight 是文件列表在窗口很矮时保留的最小高度。
	revertListMinHeight float32 = 120
	// revertDialogChromeHeight 是弹窗标题、统计与底部按钮占用的总高度。
	revertDialogChromeHeight float32 = 320
)

// revertDialogState 维护“撤销此轮改动”预览弹窗的状态，按 chatState 附加（同 worktreeUIState）。
type revertDialogState struct {
	mu sync.Mutex

	open      bool
	threadID  string
	turnID    string
	loading   bool
	err       string
	preview   *RevertPreview
	executing bool
	seq       uint64 // 递增序号，丢弃过期的异步结果
}

var (
	revertDialogMu     sync.Mutex
	revertDialogStates = make(map[*chatState]*revertDialogState)
)

// getRevertDialogState 获取或初始化当前 chatState 的撤销预览状态。
func getRevertDialogState(s *chatState) *revertDialogState {
	revertDialogMu.Lock()
	defer revertDialogMu.Unlock()
	st, ok := revertDialogStates[s]
	if !ok {
		st = &revertDialogState{}
		revertDialogStates[s] = st
	}
	return st
}

// openRevertPreview 打开撤销预览弹窗，并异步加载指定轮次的改动预览。
func (s *chatState) openRevertPreview(threadID, turnID string) {
	if s == nil || threadID == "" || turnID == "" {
		return
	}
	st := getRevertDialogState(s)
	st.mu.Lock()
	st.open = true
	st.threadID = threadID
	st.turnID = turnID
	st.loading = true
	st.err = ""
	st.preview = nil
	st.executing = false
	st.seq++
	seq := st.seq
	st.mu.Unlock()
	s.invalidate()

	go func(tid, turn string) {
		preview, err := s.client.PreviewRevert(context.Background(), tid, turn)
		st.mu.Lock()
		if st.seq != seq {
			st.mu.Unlock()
			return
		}
		st.loading = false
		if err != nil {
			st.err = "加载撤销预览失败: " + err.Error()
		} else {
			st.preview = preview
		}
		st.mu.Unlock()
		s.invalidate()
	}(threadID, turnID)
}

// closeRevertPreview 关闭撤销预览弹窗，并丢弃在途请求的结果。
func (s *chatState) closeRevertPreview() {
	if s == nil {
		return
	}
	st := getRevertDialogState(s)
	st.mu.Lock()
	st.open = false
	st.loading = false
	st.err = ""
	st.preview = nil
	st.executing = false
	st.seq++
	st.mu.Unlock()
	s.invalidate()
}

// confirmRevert 执行撤销：成功后提示并刷新当前会话历史，失败则在弹窗内展示错误。
func (s *chatState) confirmRevert() {
	if s == nil {
		return
	}
	st := getRevertDialogState(s)
	st.mu.Lock()
	if !st.open || st.loading || st.executing || st.preview == nil {
		st.mu.Unlock()
		return
	}
	tid, turnID := st.threadID, st.turnID
	count, _ := revertCounts(st.preview)
	st.executing = true
	st.err = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.ExecuteRevert(context.Background(), tid, turnID)
		st.mu.Lock()
		if st.threadID != tid || st.turnID != turnID {
			st.mu.Unlock()
			return
		}
		st.executing = false
		if err != nil {
			st.err = "撤销失败: " + err.Error()
			st.mu.Unlock()
			s.invalidate()
			return
		}
		st.open = false
		st.preview = nil
		st.seq++
		st.mu.Unlock()

		s.setToast(fmt.Sprintf("已还原 %d 个文件", count))
		s.mu.Lock()
		current := s.selectedThreadID
		s.mu.Unlock()
		if current == tid {
			// 复用会话切换路径重新加载历史消息
			s.selectThread(tid)
		}
	}()
}

// revertCounts 统计预览中将被还原与被保留的文件数。
func revertCounts(p *RevertPreview) (int, int) {
	if p == nil {
		return 0, 0
	}
	revert, kept := 0, 0
	for _, f := range p.Files {
		if f.IsKept {
			kept++
		} else {
			revert++
		}
	}
	return revert, kept
}

// renderRevertDialog 渲染“撤销此轮改动”预览弹窗，由 renderChat 每帧调用。
func renderRevertDialog(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getRevertDialogState(s)
	st.mu.Lock()
	open := st.open
	loading := st.loading
	errText := st.err
	executing := st.executing
	preview := st.preview
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragRevert)
		return
	}

	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragRevert, 480, max(440, winH-120), 360, 240, winW, winH)
	modalDragModal(c, modalDragRevert, &st.open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeRevertPreview()
			return
		}

		ui.Column(c.Key("modal-revert-card")).
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragRevert, ui.Row(c.Key("modal-revert-title-row")).FillWidth().Children(func() {
					ui.Text(c, "撤销此轮改动").
						FontSize(TypeTitle).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				}))

				switch {
				case loading:
					ui.Row(c.Key("modal-revert-loading")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							LoadingIndicator(c, nil, "正在预览改动…", IconSM)
						})
				case errText != "":
					ui.Text(c, errText).
						FontSize(TypeBody).
						TextColor(ThemeDanger(c))
				case preview == nil || len(preview.Files) == 0:
					ui.Text(c, "此轮没有可撤销的文件改动").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				default:
					renderRevertFileList(c, preview, height)
				}

				renderRevertDialogActions(c, s, loading, errText, executing, preview)
				modalResizeGrips(c, modalDragRevert, width, height)
			})
	})
}

// renderRevertDialogActions 渲染弹窗底部：加载与错误态只提供关闭入口，其余展示统计与“取消 / 还原”。
func renderRevertDialogActions(c *ui.Context, s *chatState, loading bool, errText string, executing bool, preview *RevertPreview) {
	ui.Row(c.Key("modal-revert-actions")).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			switch {
			case errText != "":
				ui.Box(c.Key("modal-revert-actions-spacer")).Grow(1)
				Button(c.Key("modal-revert-close-btn"), ButtonOpts{
					Text:    "关闭",
					Variant: ButtonSecondary,
					OnClick: func() {
						s.closeRevertPreview()
					},
				})
			case loading:
				ui.Box(c.Key("modal-revert-actions-spacer")).Grow(1)
				Button(c.Key("modal-revert-cancel-btn"), ButtonOpts{
					Text:    "取消",
					Variant: ButtonSecondary,
					OnClick: func() {
						s.closeRevertPreview()
					},
				})
			default:
				revert, kept := revertCounts(preview)
				ui.Text(c, fmt.Sprintf("将还原 %d 个文件，保留 %d 个文件", revert, kept)).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)

				Button(c.Key("modal-revert-cancel-btn"), ButtonOpts{
					Text:    "取消",
					Variant: ButtonSecondary,
					OnClick: func() {
						s.closeRevertPreview()
					},
				})

				confirmText := "还原"
				if executing {
					confirmText = "还原中…"
				}
				Button(c.Key("modal-revert-confirm-btn"), ButtonOpts{
					Text:     confirmText,
					Variant:  ButtonPrimary,
					Disabled: revert == 0 || executing,
					OnClick: func() {
						s.confirmRevert()
					},
				})
			}
		})
}

// renderRevertFileList 渲染文件清单：可还原的文件在前，被保留的排在最后。
func renderRevertFileList(c *ui.Context, preview *RevertPreview, height float32) {
	var revertable, kept []RevertFile
	for _, f := range preview.Files {
		if f.IsKept {
			kept = append(kept, f)
		} else {
			revertable = append(revertable, f)
		}
	}
	ui.Scroll(c.Key("modal-revert-scroll")).
		FillWidth().
		Grow(1).
		MinHeight(0).
		Children(func() {
			ui.Column(c.Key("modal-revert-list")).
				FillWidth().
				Gap(Space0).
				Children(func() {
					for _, f := range append(revertable, kept...) {
						renderRevertFileRow(c, f)
					}
				})
		})
}

// renderRevertFileRow 渲染单个文件行：路径、增删行数，被保留的文件用次要色标注。
func renderRevertFileRow(c *ui.Context, f RevertFile) {
	ui.Row(c.Key("modal-revert-row-"+f.Path)).
		FillWidth().
		Padding(Space1, Space3).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, f.Path).
				FontSize(TypeFootnote).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
			if f.Additions > 0 {
				ui.Text(c, fmt.Sprintf("+%d", f.Additions)).
					FontSize(TypeFootnote).
					FontWeight(500).
					TextColor(ThemeSuccess(c))
			}
			if f.Deletions > 0 {
				ui.Text(c, fmt.Sprintf("-%d", f.Deletions)).
					FontSize(TypeFootnote).
					FontWeight(500).
					TextColor(ThemeDanger(c))
			}
			if f.IsKept {
				ui.Text(c, "已保留（轮次结束后被修改过）").
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))
			}
		})
}
