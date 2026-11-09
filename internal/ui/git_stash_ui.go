package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// modalDragGitStash 是「贮藏列表」弹窗的拖拽 key。
const modalDragGitStash = "modal-drag-git-stash"

// gitStashCardWidth 是贮藏列表弹窗卡片的宽度。
const gitStashCardWidth float32 = 480

// gitStashClient 是贮藏列表弹窗需要的后端能力，由 app.Client 实现。
// 这里用小接口加类型断言接入，不往 ui/client.go 的大接口上加方法。
type gitStashClient interface {
	// GitStashList 列出会话所在仓库的贮藏记录，最新一条在最前。
	GitStashList(ctx context.Context, threadID string) ([]git.StashEntry, error)
	// GitStashApply 应用指定序号的贮藏，记录保留。
	GitStashApply(ctx context.Context, threadID string, index int) error
	// GitStashDrop 删除指定序号的贮藏记录。
	GitStashDrop(ctx context.Context, threadID string, index int) error
	// GitStashPopAt 应用并删除指定序号的贮藏。
	GitStashPopAt(ctx context.Context, threadID string, index int) error
}

// gitStashDialogState 是「贮藏列表」弹窗的状态，按 gitForm 附加：
// 同一个 Git 操作窗口里的弹窗共用一份状态。
type gitStashDialogState struct {
	mu sync.Mutex

	open      bool
	loading   bool
	err       string
	busy      string // 正在执行的条目操作文字，非空时禁用全部按钮
	entries   []git.StashEntry
	confirmAt int    // 等待二次确认删除的序号，-1 表示没有
	seq       uint64 // 递增序号，丢弃过期的异步结果
}

var (
	gitStashDialogMu sync.Mutex
	gitStashDialogs  = make(map[*gitForm]*gitStashDialogState)
)

// getGitStashDialogState 取得或初始化指定 Git 窗口的贮藏列表状态。
func getGitStashDialogState(f *gitForm) *gitStashDialogState {
	gitStashDialogMu.Lock()
	defer gitStashDialogMu.Unlock()
	st, ok := gitStashDialogs[f]
	if !ok {
		st = &gitStashDialogState{confirmAt: -1}
		gitStashDialogs[f] = st
	}
	return st
}

// dropStashDialogState 在 Git 窗口关闭时清掉它的贮藏列表弹窗状态。
func (f *gitForm) dropStashDialogState() {
	gitStashDialogMu.Lock()
	delete(gitStashDialogs, f)
	gitStashDialogMu.Unlock()
	modalDragReset(modalDragGitStash)
}

// stashClient 从当前会话客户端里取出贮藏能力，客户端不支持时返回 false。
func (f *gitForm) stashClient() (gitStashClient, bool) {
	if f == nil || f.s == nil || f.s.client == nil {
		return nil, false
	}
	client, ok := f.s.client.(gitStashClient)
	return client, ok
}

// openStashDialog 打开贮藏列表弹窗并异步读取列表。
func (f *gitForm) openStashDialog() {
	if f == nil {
		return
	}
	st := getGitStashDialogState(f)
	st.mu.Lock()
	st.open = true
	st.loading = true
	st.err = ""
	st.busy = ""
	st.entries = nil
	st.confirmAt = -1
	st.seq++
	st.mu.Unlock()
	f.invalidate()
	f.fetchStashEntries()
}

// closeStashDialog 关闭贮藏列表弹窗，并丢弃在途请求的结果。
func (f *gitForm) closeStashDialog() {
	if f == nil {
		return
	}
	st := getGitStashDialogState(f)
	st.mu.Lock()
	st.open = false
	st.loading = false
	st.err = ""
	st.busy = ""
	st.confirmAt = -1
	st.seq++
	st.mu.Unlock()
	modalDragReset(modalDragGitStash)
	f.invalidate()
}

// fetchStashEntries 在后台读取贮藏列表；只保留最后一次请求的结果。
func (f *gitForm) fetchStashEntries() {
	st := getGitStashDialogState(f)
	client, ok := f.stashClient()
	if !ok {
		st.mu.Lock()
		st.loading = false
		st.err = "当前客户端不支持贮藏列表"
		st.mu.Unlock()
		f.invalidate()
		return
	}

	st.mu.Lock()
	st.loading = true
	st.seq++
	seq := st.seq
	st.mu.Unlock()

	threadID := f.threadID
	go func() {
		entries, err := client.GitStashList(context.Background(), threadID)
		st.mu.Lock()
		if st.seq != seq {
			// 弹窗已关闭或有更新的请求：丢弃这次结果。
			st.mu.Unlock()
			return
		}
		st.loading = false
		if err != nil {
			st.err = "读取贮藏列表失败: " + err.Error()
		} else {
			st.entries = entries
			st.err = ""
		}
		st.mu.Unlock()
		f.invalidate()
	}()
}

// runStashOp 在后台执行一条贮藏操作，成功后刷新贮藏列表与 Git 状态。
func (f *gitForm) runStashOp(name, doneToast string, op func(ctx context.Context) error) {
	if f == nil {
		return
	}
	st := getGitStashDialogState(f)
	st.mu.Lock()
	if st.busy != "" {
		st.mu.Unlock()
		return
	}
	st.busy = name
	st.err = ""
	st.confirmAt = -1
	st.mu.Unlock()
	f.invalidate()

	go func() {
		err := op(context.Background())
		st.mu.Lock()
		st.busy = ""
		if err != nil {
			st.err = name + "失败: " + err.Error()
		}
		st.mu.Unlock()
		if err != nil {
			f.invalidate()
			return
		}
		f.s.loadGitState(f.threadID)
		if doneToast != "" {
			f.s.setToast(doneToast)
		}
		f.fetchStashEntries()
	}()
}

// applyStash 应用指定序号的贮藏，记录保留。
func (f *gitForm) applyStash(index int) {
	client, ok := f.stashClient()
	if !ok {
		return
	}
	threadID := f.threadID
	f.runStashOp("应用贮藏", "已应用贮藏", func(ctx context.Context) error {
		return client.GitStashApply(ctx, threadID, index)
	})
}

// popStash 应用并删除指定序号的贮藏。
func (f *gitForm) popStash(index int) {
	client, ok := f.stashClient()
	if !ok {
		return
	}
	threadID := f.threadID
	f.runStashOp("弹出贮藏", "已弹出贮藏", func(ctx context.Context) error {
		return client.GitStashPopAt(ctx, threadID, index)
	})
}

// dropStash 删除指定序号的贮藏记录。
func (f *gitForm) dropStash(index int) {
	client, ok := f.stashClient()
	if !ok {
		return
	}
	threadID := f.threadID
	f.runStashOp("删除贮藏", "已删除贮藏", func(ctx context.Context) error {
		return client.GitStashDrop(ctx, threadID, index)
	})
}

// confirmDropStash 把某一行的删除按钮切换成二次确认。
func (f *gitForm) confirmDropStash(index int) {
	st := getGitStashDialogState(f)
	st.mu.Lock()
	st.confirmAt = index
	st.mu.Unlock()
	f.invalidate()
}

// cancelDropStash 取消删除的二次确认。
func (f *gitForm) cancelDropStash() {
	st := getGitStashDialogState(f)
	st.mu.Lock()
	st.confirmAt = -1
	st.mu.Unlock()
	f.invalidate()
}

// renderGitStashDialog 渲染「贮藏列表」弹窗，由 Git 操作窗口每帧调用。
func renderGitStashDialog(c *ui.Context, f *gitForm) {
	if f == nil {
		return
	}
	st := getGitStashDialogState(f)
	st.mu.Lock()
	open := st.open
	loading := st.loading
	errText := st.err
	busy := st.busy
	confirmAt := st.confirmAt
	entries := append([]git.StashEntry(nil), st.entries...)
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragGitStash)
		return
	}

	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragGitStash, gitStashCardWidth, max(400, winH-120), 360, 240, winW, winH)
	modalDragModal(c, modalDragGitStash, &open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			f.closeStashDialog()
			return
		}

		ui.Column(c.Key("modal-git-stash-card")).
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
				modalDragHandle(c, modalDragGitStash, ui.Row(c.Key("modal-git-stash-title-row")).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "贮藏列表").
						FontSize(TypeTitle).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					ui.Spacer(c)
					if busy != "" {
						LoadingIndicator(c, nil, "", IconSM)
					}
				}))

				switch {
				case loading:
					ui.Row(c.Key("modal-git-stash-loading")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							LoadingIndicator(c, nil, "正在读取贮藏列表…", IconSM)
						})
				case len(entries) == 0:
					ui.Text(c, "这个仓库还没有贮藏记录").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				default:
					renderGitStashList(c, f, entries, busy, confirmAt, height)
				}

				if errText != "" {
					ui.Text(c, errText).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c)).
						MaxLines(3)
				}

				ui.Row(c.Key("modal-git-stash-actions")).
					FillWidth().
					Gap(Space3).
					AlignItems(ui.Center).
					Justify(ui.End).
					Children(func() {
						ui.Text(c, "Esc 关闭").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
						Button(c, ButtonOpts{
							Key:      "modal-git-stash-refresh",
							Text:     "刷新",
							Variant:  ButtonSecondary,
							Disabled: loading || busy != "",
							OnClick: func() {
								f.fetchStashEntries()
							},
						})
						Button(c, ButtonOpts{
							Key:     "modal-git-stash-close",
							Text:    "关闭",
							Variant: ButtonPrimary,
							OnClick: func() {
								f.closeStashDialog()
							},
						})
					})
				modalResizeGrips(c, modalDragGitStash, width, height)
			})
	})
	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里据此关闭。
	if !open {
		f.closeStashDialog()
	}
}

// renderGitStashList 渲染贮藏条目列表：消息在上，序号、分支与相对时间在下，右侧是操作按钮。
func renderGitStashList(c *ui.Context, f *gitForm, entries []git.StashEntry, busy string, confirmAt int, winH float32) {
	ui.Scroll(c.Key("modal-git-stash-scroll")).
		FillWidth().
		Grow(1).
		MinHeight(0).
		Children(func() {
			ui.Column(c.Key("modal-git-stash-list")).
				FillWidth().
				Gap(Space2).
				Children(func() {
					for _, entry := range entries {
						renderGitStashRow(c, f, entry, busy, confirmAt == entry.Index)
					}
				})
		})
}

// renderGitStashRow 渲染一条贮藏记录；待确认删除的那一行把按钮换成「确认删除 / 取消」。
func renderGitStashRow(c *ui.Context, f *gitForm, entry git.StashEntry, busy string, confirming bool) {
	key := fmt.Sprintf("modal-git-stash-row-%d", entry.Index)
	title := strings.TrimSpace(entry.Message)
	if title == "" {
		title = entry.Ref
	}

	ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Gap(Space3).
		AlignItems(ui.Center).
		Background(ThemeHover(c).Alpha(0.35)).
		Children(func() {
			ui.Column(c.Key(key + "-text")).
				Grow(1).
				Gap(Space0).
				Children(func() {
					ui.Text(c, title).
						FontSize(TypeFootnote).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					ui.Text(c, gitStashEntryMeta(entry)).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				})

			disabled := busy != ""
			if confirming {
				Button(c, ButtonOpts{
					Key:      key + "-confirm",
					Text:     "确认删除",
					Variant:  ButtonDanger,
					Disabled: disabled,
					OnClick: func() {
						f.dropStash(entry.Index)
					},
				})
				Button(c, ButtonOpts{
					Key:      key + "-cancel",
					Text:     "取消",
					Variant:  ButtonSecondary,
					Disabled: disabled,
					OnClick: func() {
						f.cancelDropStash()
					},
				})
				return
			}

			Button(c, ButtonOpts{
				Key:      key + "-apply",
				Text:     "应用",
				Variant:  ButtonSecondary,
				Disabled: disabled,
				OnClick: func() {
					f.applyStash(entry.Index)
				},
			})
			Button(c, ButtonOpts{
				Key:      key + "-pop",
				Text:     "弹出",
				Variant:  ButtonSecondary,
				Disabled: disabled,
				OnClick: func() {
					f.popStash(entry.Index)
				},
			})
			Button(c, ButtonOpts{
				Key:      key + "-drop",
				Text:     "删除",
				Variant:  ButtonDanger,
				Disabled: disabled,
				OnClick: func() {
					f.confirmDropStash(entry.Index)
				},
			})
		})
}

// gitStashEntryMeta 拼出条目下方的辅助信息：序号、来源分支与相对时间。
func gitStashEntryMeta(entry git.StashEntry) string {
	ref := entry.Ref
	if ref == "" {
		ref = fmt.Sprintf("stash@{%d}", entry.Index)
	}
	parts := []string{ref}
	if branch := strings.TrimSpace(entry.Branch); branch != "" {
		parts = append(parts, "分支 "+branch)
	}
	if rel := formatRelativeTime(entry.CreatedAt); rel != "" {
		parts = append(parts, rel)
	}
	return strings.Join(parts, " · ")
}
