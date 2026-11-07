package mcodeui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 快捷回复页维护输入框上方那排按钮的标题与正文。
// 草稿与配置刻意分开：用户常把标题填了、内容还没填，这种半截状态必须能继续输入，
// 而配置里只存合法条目，所以草稿常驻内存，只有整组合法时才写进配置并落盘。

// modalDragQuickReplyDelete 是「删除快捷回复」确认弹窗的拖拽 key。
const modalDragQuickReplyDelete = "modal-drag-quick-reply-delete"

// quickReplyRow 是一行正在编辑的快捷回复，行序即最终的按钮顺序。
type quickReplyRow struct {
	Label   string
	Message string
}

// quickRepliesPageState 是「快捷回复」页的编辑态。
type quickRepliesPageState struct {
	mu       sync.Mutex
	rows     []*quickReplyRow    // 草稿行；用指针是为了列表增删后已渲染的输入框仍指向有效对象
	errs     []string            // 与 rows 等长的行内错误，空字符串表示该行合法
	deleteAt int                 // 待确认删除的行索引，-1 表示没有
	synced   []config.QuickReply // 上次与配置对齐的内容，用来发现别处（其它窗口）的修改
}

// getQuickRepliesPage 取该设置面板的快捷回复页状态；第一次访问时在 st.mu 下按配置内容建草稿。
func getQuickRepliesPage(st *SettingsState) *quickRepliesPageState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.quickRepliesPage == nil {
		st.quickRepliesPage = newQuickRepliesPageState(st.cfg.QuickReplies)
	}
	return st.quickRepliesPage
}

// newQuickRepliesPageState 按配置内容初始化编辑态。
func newQuickRepliesPageState(items []config.QuickReply) *quickRepliesPageState {
	rows := quickReplyRowsFromConfig(items)
	return &quickRepliesPageState{
		rows:     rows,
		errs:     make([]string, len(rows)),
		deleteAt: -1,
		synced:   append([]config.QuickReply(nil), items...),
	}
}

// quickReplyRowsFromConfig 把配置条目转成草稿行。
func quickReplyRowsFromConfig(items []config.QuickReply) []*quickReplyRow {
	rows := make([]*quickReplyRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, &quickReplyRow{Label: item.Label, Message: item.Message})
	}
	return rows
}

// syncQuickRepliesPage 渲染前把草稿与配置对齐：配置里的内容变了（别的窗口保存过），
// 就以配置为准重建草稿，免得多年前打开的页面把别人的修改静默盖回去。
func syncQuickRepliesPage(st *SettingsState, page *quickRepliesPageState) {
	current := st.cfg.QuickReplies
	page.mu.Lock()
	same := slices.Equal(page.synced, current)
	page.mu.Unlock()
	if same {
		return
	}
	rows := quickReplyRowsFromConfig(current)
	page.mu.Lock()
	page.rows = rows
	page.errs = make([]string, len(rows))
	page.synced = append([]config.QuickReply(nil), current...)
	page.mu.Unlock()
}

// quickReplyConfigRows 把草稿转成配置条目，并去掉每条的首尾空白。
func quickReplyConfigRows(page *quickRepliesPageState) []config.QuickReply {
	page.mu.Lock()
	defer page.mu.Unlock()
	items := make([]config.QuickReply, 0, len(page.rows))
	for _, row := range page.rows {
		items = append(items, config.QuickReply{
			Label:   strings.TrimSpace(row.Label),
			Message: strings.TrimSpace(row.Message),
		})
	}
	return items
}

// stageQuickReplies 校验草稿并把结果写进内存配置：整组合法才替换，非法时保留上一次的合法值，
// 只把逐行错误记下来供界面标红。返回是否写入以及第一条错误文案。
func stageQuickReplies(st *SettingsState, page *quickRepliesPageState) (bool, string) {
	items := quickReplyConfigRows(page)
	errs, firstErr := config.ValidateQuickReplies(items)
	page.mu.Lock()
	page.errs = errs
	page.mu.Unlock()
	if firstErr != "" {
		return false, firstErr
	}
	normalized := config.NormalizeQuickReplies(items)
	page.mu.Lock()
	page.synced = normalized
	page.mu.Unlock()
	// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu）；save、noteTextEdit 与 invalidate
	// 由调用方在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.QuickReplies = normalized
	st.cfgMu.Unlock()
	st.mu.Unlock()
	return true, ""
}

// persistQuickReplies 结构改动（增删、排序）后立即落盘：这些不是连续输入，
// 当场写盘才能保证重启后顺序一致。仍不合法时只提示，不写盘。
func persistQuickReplies(st *SettingsState, page *quickRepliesPageState) {
	if ok, msg := stageQuickReplies(st, page); !ok {
		st.noteError(msg)
		st.invalidate()
		return
	}
	st.save()
	st.invalidate()
}

// quickReplyTextChanged 处理输入框改动：合法就走设置页统一的防抖落盘，非法只标红不写盘。
func quickReplyTextChanged(st *SettingsState, page *quickRepliesPageState) {
	if ok, msg := stageQuickReplies(st, page); ok {
		st.noteTextEdit()
		return
	} else if msg != "" {
		st.noteError(msg)
	}
	st.invalidate()
}

// addQuickReplyRow 追加一条空行。空行还没填完，不写盘。
func addQuickReplyRow(st *SettingsState, page *quickRepliesPageState) {
	page.mu.Lock()
	page.rows = append(page.rows, &quickReplyRow{})
	page.errs = append(page.errs, "")
	page.mu.Unlock()
	st.invalidate()
}

// moveQuickReplyRow 把第 i 行上移（delta=-1）或下移（delta=1）；越界时什么都不做。
func moveQuickReplyRow(st *SettingsState, page *quickRepliesPageState, i, delta int) {
	page.mu.Lock()
	j := i + delta
	if i < 0 || i >= len(page.rows) || j < 0 || j >= len(page.rows) {
		page.mu.Unlock()
		return
	}
	page.rows[i], page.rows[j] = page.rows[j], page.rows[i]
	page.errs[i], page.errs[j] = page.errs[j], page.errs[i]
	page.mu.Unlock()
	persistQuickReplies(st, page)
}

// requestDeleteQuickReply 记下待删除的行并弹出确认框；快捷回复删掉只能重敲，值得兜一下。
func requestDeleteQuickReply(st *SettingsState, page *quickRepliesPageState, i int) {
	page.mu.Lock()
	page.deleteAt = i
	page.mu.Unlock()
	st.invalidate()
}

// cancelDeleteQuickReply 关掉删除确认，草稿保持原样。
func cancelDeleteQuickReply(st *SettingsState, page *quickRepliesPageState) {
	page.mu.Lock()
	page.deleteAt = -1
	page.mu.Unlock()
	st.invalidate()
}

// confirmDeleteQuickReply 删除待确认的行，并按新顺序立即落盘。
func confirmDeleteQuickReply(st *SettingsState, page *quickRepliesPageState) {
	page.mu.Lock()
	i := page.deleteAt
	page.deleteAt = -1
	if i >= 0 && i < len(page.rows) {
		page.rows = append(page.rows[:i], page.rows[i+1:]...)
		page.errs = append(page.errs[:i], page.errs[i+1:]...)
	}
	page.mu.Unlock()
	persistQuickReplies(st, page)
}

// renderPageQuickReplies 渲染「快捷回复」设置页：列表里维护标题与正文，可增删和上下排序。
func renderPageQuickReplies(c *ui.Context, st *SettingsState) {
	page := getQuickRepliesPage(st)
	syncQuickRepliesPage(st, page)

	page.mu.Lock()
	rows := append([]*quickReplyRow(nil), page.rows...)
	errs := append([]string(nil), page.errs...)
	deleteAt := page.deleteAt
	page.mu.Unlock()

	ui.Column(c.Key("quick-replies-container")).Gap(Space5).Children(func() {
		renderSettingsSection(c, "sec-quick-replies", "快捷回复列表", func() {
			if len(rows) == 0 {
				ui.Column(c.Key("quick-replies-empty")).Padding(settingsRowPadY, settingsRowPadX).Gap(Space1).Children(func() {
					settingStatus(c, "quick-replies-empty-status", "还没有快捷回复。")
				})
			}
			for i := range rows {
				if i > 0 {
					renderSettingsDivider(c, "div-quick-replies-"+strconv.Itoa(i))
				}
				renderQuickReplyRow(c, st, page, i, rows[i], quickReplyRowErr(errs, i), len(rows))
			}
			renderSettingsDivider(c, "div-quick-replies-add")
			renderQuickReplyAddRow(c, st, page)
		})
	})

	renderQuickReplyDeleteConfirm(c, st, page, rows, deleteAt)
}

// quickReplyRowErr 取第 i 行的行内错误，越界时返回空串。
func quickReplyRowErr(errs []string, i int) string {
	if i < 0 || i >= len(errs) {
		return ""
	}
	return errs[i]
}

// renderQuickReplyRow 渲染一行快捷回复：标题、正文两个输入框，加上移/下移/删除按钮。
func renderQuickReplyRow(c *ui.Context, st *SettingsState, page *quickRepliesPageState, i int, row *quickReplyRow, errText string, total int) {
	idx := i + 1
	ui.Column(c.Key("quick-replies-row-"+strconv.Itoa(i))).Padding(settingsRowPadY, settingsRowPadX).Gap(Space3).Children(func() {
		textFunc(
			func(*SettingsState) (string, bool) { return row.Label, false },
			func(st *SettingsState, v string) {
				row.Label = v
				quickReplyTextChanged(st, page)
			},
			"按钮上的文字",
		).render(c, st, setting{ID: "quick-reply-label-" + strconv.Itoa(i), Title: fmt.Sprintf("第 %d 条标题", idx)})

		textFunc(
			func(*SettingsState) (string, bool) { return row.Message, false },
			func(st *SettingsState, v string) {
				row.Message = v
				quickReplyTextChanged(st, page)
			},
			"点击后填进输入框的内容",
		).render(c, st, setting{ID: "quick-reply-message-" + strconv.Itoa(i), Title: fmt.Sprintf("第 %d 条内容", idx)})

		if errText != "" {
			ui.Text(c, errText).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
		}

		ui.Row(c.Key("quick-replies-row-actions-" + strconv.Itoa(i))).Gap(Space1).AlignItems(ui.Center).Children(func() {
			quickReplyIconButton(c, "btn-quick-reply-up-"+strconv.Itoa(i), fmt.Sprintf("上移第 %d 条", idx), IconArrowUp(), false, i == 0, func() {
				moveQuickReplyRow(st, page, i, -1)
			})
			quickReplyIconButton(c, "btn-quick-reply-down-"+strconv.Itoa(i), fmt.Sprintf("下移第 %d 条", idx), IconMoveDown(), false, i >= total-1, func() {
				moveQuickReplyRow(st, page, i, 1)
			})
			quickReplyIconButton(c, "btn-quick-reply-delete-"+strconv.Itoa(i), fmt.Sprintf("删除第 %d 条", idx), IconTrash2(), true, false, func() {
				requestDeleteQuickReply(st, page, i)
			})
		})
	})
}

// renderQuickReplyAddRow 渲染列表底部的「添加一条」按钮。
func renderQuickReplyAddRow(c *ui.Context, st *SettingsState, page *quickRepliesPageState) {
	ui.Row(c.Key("quick-replies-add")).Padding(settingsRowPadY, settingsRowPadX).Children(func() {
		addBtn := ui.ButtonBase(c.Key("btn-quick-reply-add")).
			Label("添加一条").
			Padding(settingsBadgePadY, settingsCompactPadX).
			Radius(RadiusSM).
			Border(Hairline, ThemeBorder(c))
		if addBtn.Hovered() {
			addBtn.Background(ThemeHover(c))
		}
		if addBtn.Clicked() {
			addQuickReplyRow(st, page)
		}
		addBtn.Children(func() {
			ui.Row(c.Key("btn-quick-reply-add-row")).Gap(Space2).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconPlus()).Size(IconSM, IconSM).TextColor(ThemePrimaryText(c))
				ui.Text(c, "添加一条").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
			})
		})
	})
}

// renderQuickReplyDeleteConfirm 渲染删除确认弹窗；deleteAt 越界（列表刚被改动）时按关闭处理。
func renderQuickReplyDeleteConfirm(c *ui.Context, st *SettingsState, page *quickRepliesPageState, rows []*quickReplyRow, deleteAt int) {
	open := deleteAt >= 0 && deleteAt < len(rows)
	body := "删除这条快捷回复后不可恢复，确定继续？"
	if open {
		name := strings.TrimSpace(rows[deleteAt].Label)
		if name == "" {
			name = strings.TrimSpace(rows[deleteAt].Message)
		}
		if runes := []rune(name); len(runes) > 20 {
			name = string(runes[:20]) + "…"
		}
		if name != "" {
			body = fmt.Sprintf("删除「%s」后不可恢复，确定继续？", name)
		}
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragQuickReplyDelete,
		Open:         open,
		Tone:         ConfirmDanger,
		Title:        "删除快捷回复",
		Body:         body,
		ConfirmLabel: "删除",
		ConfirmA11y:  "确认删除快捷回复",
		CancelA11y:   "取消删除快捷回复",
		OnConfirm:    func() { confirmDeleteQuickReply(st, page) },
		OnCancel:     func() { cancelDeleteQuickReply(st, page) },
	})
}

// quickReplyIconButton 渲染行内的方形图标按钮：hover 有底色，danger 用危险色，disabled 时不可点。
func quickReplyIconButton(c *ui.Context, key, label string, icon *ui.SVG, danger, disabled bool, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(label).
		Size(ControlSM, ControlSM).
		Radius(RadiusSM).
		Center()
	if disabled {
		btn.Disabled(true)
	}
	if btn.Hovered() {
		if danger {
			btn.Background(ThemeDanger(c).Alpha(0.12))
		} else {
			btn.Background(ThemeHover(c))
		}
	}
	if !disabled {
		btn.OnClick(onClick)
	}
	color := ThemePrimaryText(c).Alpha(0.70)
	if danger {
		color = ThemeDanger(c)
	}
	if disabled {
		color = color.Alpha(0.35)
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
	})
}
