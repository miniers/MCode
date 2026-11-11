package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 脚本编辑弹窗：一份可增删的脚本列表，逐行编辑名称、命令与
// 「新建 worktree 时运行」开关，保存后刷新标题栏「执行命令」菜单。
//
// 这里编辑的是 mcode 自己的项目级存储（remote.ProjectScript，按项目 ID 保存在数据目录），
// 不再读写工作区 package.json；package.json 的 scripts 仍在执行命令菜单里只读展示。

// scriptsSheetRow 是弹窗里的一行脚本；用指针存行，编辑中的输入框绑定不会因增删行失效。
const (
	// scriptsSheetMinHeight 是脚本弹窗在窗口很矮时保留的最小高度。
	scriptsSheetMinHeight float32 = 320
	// scriptsSheetChromeHeight 是脚本弹窗标题与底部按钮占用的总高度。
	scriptsSheetChromeHeight float32 = 120
	// scriptsSheetListMinHeight 是脚本列表在窗口很矮时保留的最小高度。
	scriptsSheetListMinHeight float32 = 140
	// scriptsSheetListChromeHeight 是列表之外所有区块占用的总高度。
	scriptsSheetListChromeHeight float32 = 360
)

type scriptsSheetRow struct {
	id          string
	name        string
	command     string
	symbol      string
	runOnCreate bool
}

// scriptsSheetState 维护脚本编辑弹窗的状态，按 chatState 附加（同 revertDialogState）。
type scriptsSheetState struct {
	mu       sync.Mutex
	open     bool
	loading  bool
	threadID string
	dir      string
	rows     []*scriptsSheetRow
	saving   bool
	err      string
}

var (
	scriptsSheetMu     sync.Mutex
	scriptsSheetStates = make(map[*chatState]*scriptsSheetState)
)

// getScriptsSheetState 获取或初始化当前 chatState 的脚本编辑弹窗状态。
func getScriptsSheetState(s *chatState) *scriptsSheetState {
	scriptsSheetMu.Lock()
	defer scriptsSheetMu.Unlock()
	st, ok := scriptsSheetStates[s]
	if !ok {
		st = &scriptsSheetState{}
		scriptsSheetStates[s] = st
	}
	return st
}

// openScriptsSheet 打开脚本编辑弹窗，并在后台加载当前会话所属项目的项目级脚本。
func openScriptsSheet(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	if s.client == nil {
		s.setToast("脚本服务不可用")
		return
	}
	dir := s.headerProjectDir(threadID)

	st := getScriptsSheetState(s)
	st.mu.Lock()
	st.open = true
	st.loading = true
	st.threadID = threadID
	st.dir = dir
	st.rows = nil
	st.saving = false
	st.err = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		scripts, err := s.client.ProjectScripts(context.Background(), threadID)
		st.mu.Lock()
		// 加载期间可能已关闭弹窗或切换会话，此时丢弃结果。
		if !st.open || st.threadID != threadID {
			st.mu.Unlock()
			return
		}
		st.loading = false
		if err != nil {
			st.err = "读取项目脚本失败: " + err.Error()
			st.mu.Unlock()
			s.invalidate()
			return
		}
		rows := make([]*scriptsSheetRow, len(scripts))
		for i, p := range scripts {
			rows[i] = &scriptsSheetRow{
				id:          p.ID,
				name:        p.Name,
				command:     p.Command,
				symbol:      p.Symbol,
				runOnCreate: p.RunOnWorktreeCreate,
			}
		}
		st.rows = rows
		st.mu.Unlock()
		s.invalidate()
	}()
}

// closeScriptsSheet 关闭脚本编辑弹窗并丢弃未保存的编辑。
func closeScriptsSheet(s *chatState) {
	if s == nil {
		return
	}
	st := getScriptsSheetState(s)
	st.mu.Lock()
	st.open = false
	st.loading = false
	st.rows = nil
	st.saving = false
	st.err = ""
	st.mu.Unlock()
	s.invalidate()
}

// addScriptsSheetRow 在列表末尾追加一行空脚本。
func addScriptsSheetRow(s *chatState) {
	st := getScriptsSheetState(s)
	st.mu.Lock()
	st.rows = append(st.rows, &scriptsSheetRow{name: "新建脚本"})
	st.mu.Unlock()
	s.invalidate()
}

// removeScriptsSheetRow 从列表里删掉指定行。
func removeScriptsSheetRow(s *chatState, row *scriptsSheetRow) {
	st := getScriptsSheetState(s)
	st.mu.Lock()
	kept := make([]*scriptsSheetRow, 0, len(st.rows))
	for _, r := range st.rows {
		if r != row {
			kept = append(kept, r)
		}
	}
	st.rows = kept
	st.mu.Unlock()
	s.invalidate()
}

// scriptsSheetValidation 返回不能保存的原因：名称与命令都不能为空、名称不能重复。
// 任一脚本缺少命令时保存按钮保持禁用（并说明缺什么）。
func scriptsSheetValidation(rows []*scriptsSheetRow) string {
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		name := strings.TrimSpace(r.name)
		if name == "" {
			return "脚本名称不能为空"
		}
		if strings.TrimSpace(r.command) == "" {
			return "每个脚本都需要命令"
		}
		if seen[name] {
			return "脚本名称不能重复"
		}
		seen[name] = true
	}
	return ""
}

// saveScriptsSheet 把弹窗里的列表写回 mcode 的项目级存储，并刷新标题栏菜单。
func saveScriptsSheet(s *chatState) {
	st := getScriptsSheetState(s)
	st.mu.Lock()
	if !st.open || st.saving || st.loading {
		st.mu.Unlock()
		return
	}
	if note := scriptsSheetValidation(st.rows); note != "" {
		st.err = note
		st.mu.Unlock()
		s.invalidate()
		return
	}
	// 拷贝一份值快照，避免保存期间继续输入与写盘并发读写同一批行。
	scripts := make([]ProjectScript, len(st.rows))
	for i, r := range st.rows {
		scripts[i] = ProjectScript{
			ID:                  r.id,
			Name:                strings.TrimSpace(r.name),
			Command:             strings.TrimSpace(r.command),
			Symbol:              r.symbol,
			RunOnWorktreeCreate: r.runOnCreate,
		}
	}
	threadID := st.threadID
	st.saving = true
	st.err = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.SetProjectScripts(context.Background(), threadID, scripts)
		st.mu.Lock()
		st.saving = false
		if err != nil {
			st.err = "保存失败: " + err.Error()
			st.mu.Unlock()
			s.invalidate()
			return
		}
		st.open = false
		st.rows = nil
		st.mu.Unlock()
		s.setToast(fmt.Sprintf("已保存 %d 个项目脚本", len(scripts)))
		// 复用标题栏脚本的加载入口刷新菜单里的脚本列表。
		s.loadHeaderScripts(threadID)
	}()
}

// renderScriptsSheet 渲染脚本编辑弹窗，由标题栏执行命令按钮每帧调用（菜单关闭时也渲染）。
func renderScriptsSheet(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getScriptsSheetState(s)
	st.mu.Lock()
	open := st.open
	loading := st.loading
	rows := st.rows
	saving := st.saving
	errText := st.err
	threadID := st.threadID
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragScriptsSheet)
		return
	}
	// 切换会话后弹窗里的行属于旧会话的项目：直接关掉，避免存到错误的项目。
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected != threadID {
		closeScriptsSheet(s)
		return
	}

	winWidth, winHeight := c.Size()
	width, height := modalResizeSize(modalDragScriptsSheet, 620, max(scriptsSheetMinHeight, winHeight-scriptsSheetChromeHeight), 360, 240, winWidth, winHeight)
	note := scriptsSheetValidation(rows)
	Sheet(c, SheetProps{Key: modalDragScriptsSheet, PanelKey: "scripts-sheet", PanelLabel: "scripts-sheet", CardKey: "scripts-sheet-card", Label: "scripts-sheet-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).FillWidth().Grow(1).Padding(Space6).Gap(Space4).Children(func() {
			// 标题行同时是弹窗的拖拽把手。
			modalDragHandle(c, modalDragScriptsSheet, ui.Row(c.Key("scripts-sheet-header")).
				FillWidth().
				AlignItems(ui.Center).
				Children(func() {
					ui.Column(c.Key("scripts-sheet-header-text")).Gap(Space0).Grow(1).Children(func() {
						ui.Text(c, "项目脚本").
							FontSize(uiFont(TypeTitle)).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))
					})
					Button(c.Key("scripts-sheet-header-cancel"), ButtonOpts{
						Text:  "取消",
						Label: "取消脚本编辑",
						OnClick: func() {
							closeScriptsSheet(s)
						},
						Variant: ButtonSecondary,
					})
				}))

			if loading {
				ui.Text(c, "正在加载项目脚本…").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))
			} else if len(rows) == 0 {
				ui.Text(c, "还没有脚本，点「添加脚本」新建一个。").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))
			}

			ui.Scroll(c.Key("scripts-sheet-scroll")).
				FillWidth().
				Grow(1).
				MinHeight(0).
				Children(func() {
					ui.Column(c.Key("scripts-sheet-list")).
						FillWidth().
						Gap(Space3).
						Children(func() {
							for i, row := range rows {
								renderScriptsSheetRow(c, s, i, row)
							}
						})
				})

			if errText != "" {
				ui.Text(c, errText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c))
			} else if note != "" {
				ui.Text(c, note).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))
			}

			ui.Row(c.Key("scripts-sheet-footer")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					Button(c.Key("scripts-sheet-add"), ButtonOpts{
						Icon:  IconPlus(),
						Text:  "添加脚本",
						Label: "添加脚本",
						OnClick: func() {
							addScriptsSheetRow(s)
						},
						Variant: ButtonSecondary,
					})
					ui.Box(c.Key("scripts-sheet-footer-spacer")).Grow(1)
					saveText := "保存"
					if saving {
						saveText = "保存中…"
					}
					Button(c.Key("scripts-sheet-save"), ButtonOpts{
						Text:     saveText,
						Label:    "保存脚本",
						Disabled: saving || loading || (len(rows) > 0 && note != ""),
						OnClick: func() {
							saveScriptsSheet(s)
						},
						Variant: ButtonPrimary,
					})
				})
		})
		modalResizeGrips(c, modalDragScriptsSheet, width, height)
	})

	if !open {
		closeScriptsSheet(s)
	}
}

// renderScriptsSheetRow 渲染脚本弹窗里的一行：名称、删除按钮、命令输入与「新建 worktree 时运行」开关。
func renderScriptsSheetRow(c *ui.Context, s *chatState, idx int, row *scriptsSheetRow) {
	ui.Column(c.Key(fmt.Sprintf("scripts-sheet-row-%d", idx))).
		FillWidth().
		Padding(Space3).
		Radius(RadiusMD).
		Gap(Space2).
		Background(ThemeSurface(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key(fmt.Sprintf("scripts-sheet-row-head-%d", idx))).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.TextInput(c.Key(fmt.Sprintf("scripts-sheet-name-%d", idx)), &row.name).
						Label("脚本名称").
						Placeholder("名称").
						FontSize(uiFont(TypeBody)).
						Grow(1)
					IconButton(c, IconButtonProps{
						Key:   fmt.Sprintf("scripts-sheet-remove-%d", idx),
						Icon:  IconTrash2(),
						Label: "删除脚本",
						OnClick: func() {
							removeScriptsSheetRow(s, row)
						},
						Variant: ButtonGhost,
						Size:    ControlSM,
					})
				})

			ui.TextInput(c.Key(fmt.Sprintf("scripts-sheet-command-%d", idx)), &row.command).
				Label("脚本命令").
				Placeholder("命令，例如 npm run dev").
				FontSize(uiFont(TypeFootnote)).
				FillWidth()

			ui.Row(c.Key(fmt.Sprintf("scripts-sheet-row-flags-%d", idx))).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					cb := ui.Checkbox(c.Key(fmt.Sprintf("scripts-sheet-run-on-create-%d", idx)), &row.runOnCreate, "新建 worktree 时运行")
					if cb.Changed() {
						// 复选框已改写 row.runOnCreate，这里只要让弹窗重绘一次。
						s.invalidate()
					}
				})

			if strings.TrimSpace(row.command) == "" {
				ui.Text(c, "缺少命令").
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeDanger(c))
			}
		})
}
