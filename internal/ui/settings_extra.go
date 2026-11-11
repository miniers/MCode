package mcodeui

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
)

// settingsShortcutSaveDebounce 是快捷键输入框的落盘防抖：输入期间只改本地草稿，
// 停手这么久或输入框失焦之后才统一写一次配置，免得每敲一个键就在 UI 线程 fsync 一次。
const settingsShortcutSaveDebounce = 500 * time.Millisecond

// 包级可替换扫描函数指针（便于单元测试中注入假实现）
var scanSkills = agentconf.ScanSkills
var scanMCP = agentconf.ScanMCP

// scanStaticAuth 是静态请求头鉴权服务集合的扫描函数，同样留出替换入口便于测试打桩。
var scanStaticAuth = agentconf.StaticAuthServerKeys

// currentProjectDir 获取当前选中会话所在项目的路径；若无或查不到则返回空字符串。
func (s *SettingsState) currentProjectDir() string {
	s.mu.Lock()
	chat := s.chat
	s.mu.Unlock()
	if chat == nil {
		return ""
	}

	chat.mu.Lock()
	defer chat.mu.Unlock()

	if chat.selectedThreadID == "" {
		return ""
	}

	var projectID string
	for _, th := range chat.threads {
		if th.ID == chat.selectedThreadID {
			projectID = th.ProjectID
			break
		}
	}
	if projectID == "" {
		for pid, list := range chat.threadsByProj {
			for _, th := range list {
				if th.ID == chat.selectedThreadID {
					projectID = pid
					break
				}
			}
			if projectID != "" {
				break
			}
		}
	}
	if projectID == "" {
		return ""
	}

	for _, p := range chat.projects {
		if p.ID == projectID {
			return p.Path
		}
	}
	return ""
}

// loadAgentConf 异步加载当前环境与项目的 Skills 和 MCP 服务配置。
// projectDir 会被记下来（agentConfProjectDir），页面展示的条目与后续写盘都以它为准。
func (s *SettingsState) loadAgentConf(projectDir string) {
	s.mu.Lock()
	if s.agentConfLoading {
		s.mu.Unlock()
		return
	}
	s.agentConfLoading = true
	s.agentConfErr = ""
	// 重扫会重建列表，旧的启停写盘错误也随之失效。
	s.agentConfWriteErr = ""
	dataDir := s.dataDir
	s.mu.Unlock()
	s.invalidate()

	go func() {
		home, _ := os.UserHomeDir()
		opts := agentconf.Options{
			Home:       home,
			ProjectDir: projectDir,
			DataDir:    dataDir,
		}

		skills, errSkills := scanSkills(opts)
		mcps, errMCP := scanMCP(opts)
		// 静态鉴权服务集合单独扫一遍带凭据的配置，只在列表扫描成功后才有意义。
		staticAuth := map[string]bool{}
		if errMCP == nil {
			staticAuth = scanStaticAuth(opts)
		}

		s.mu.Lock()
		s.agentConfLoading = false
		s.agentConfLoaded = true
		// 记下本批列表对应的项目目录：即使扫描报错也记，否则会与当前项目永远对不上而每帧重扫。
		s.agentConfProjectDir = projectDir
		if errSkills != nil {
			s.agentConfErr = errSkills.Error()
		} else if errMCP != nil {
			s.agentConfErr = errMCP.Error()
		} else {
			s.agentConfErr = ""
		}
		if errSkills == nil {
			s.skills = skills
		}
		if errMCP == nil {
			s.mcpServers = mcps
			s.mcpStaticAuth = staticAuth
		}
		s.mu.Unlock()

		s.invalidate()
	}()
}

// ensureAgentConfCurrent 在扫描时的项目目录与当前项目不一致（或尚未扫过）时重扫技能与 MCP，
// 否则切项目后会继续显示上一个项目的技能与服务（J-07）。
func (s *SettingsState) ensureAgentConfCurrent() {
	dir := s.currentProjectDir()

	s.mu.Lock()
	loading, loaded, scannedDir := s.agentConfLoading, s.agentConfLoaded, s.agentConfProjectDir
	s.mu.Unlock()

	if loading || (loaded && scannedDir == dir) {
		return
	}
	s.loadAgentConf(dir)
}

// agentConfWriteOptions 返回与当前展示的技能/MCP 列表同源的扫描参数：
// 项目目录取自扫描时记下的值，避免切项目后把改动写进另一个项目的 .mcode 配置（J-07）。
func (s *SettingsState) agentConfWriteOptions() agentconf.Options {
	s.mu.Lock()
	dataDir, projectDir := s.dataDir, s.agentConfProjectDir
	s.mu.Unlock()
	home, _ := os.UserHomeDir()
	return agentconf.Options{Home: home, ProjectDir: projectDir, DataDir: dataDir}
}

// setAgentConfWriteErr 记录（或清空）技能启停写盘失败的原因并请求重绘。
func (s *SettingsState) setAgentConfWriteErr(msg string) {
	s.mu.Lock()
	s.agentConfWriteErr = msg
	s.mu.Unlock()
	s.invalidate()
}

// renderBadge 渲染小号圆角浅底标签
// renderBadge 渲染小号圆角浅底标签
func renderBadge(c *ui.Context, key string, text string) {
	ui.Box(c.Key(key)).
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeOverlay(c, 0.08)).
		Children(func() {
			ui.Text(c, text).
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderGlobalShortcutControl 渲染全局快捷键的开关与组合输入框（「通用」页的一行）。
func renderGlobalShortcutControl(c *ui.Context, st *SettingsState) {
	ui.Row(c.Key("global-shortcut-control")).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			toggle(val(func(cfg *config.Config) *bool { return &cfg.Shortcuts.GlobalShortcutEnabled }, false)).
				render(c, st, setting{ID: "sw-global-shortcut", Title: "全局快捷键"})

			// 输入框只绑稳定的草稿指针，不直接绑 st.cfg 字段；改动在锁内写回 cfg，落盘走文本防抖。
			st.cfgMu.Lock()
			draft := st.globalShortcutDraft
			st.cfgMu.Unlock()
			if draft == nil {
				return
			}
			TextField(c, TextFieldProps{
				Key:         "input-global-shortcut",
				Value:       draft,
				Label:       "全局快捷键输入",
				Placeholder: "CmdOrCtrl+Shift+Space",
				Width:       280,
				Disabled:    !st.cfg.Shortcuts.GlobalShortcutEnabled,
				OnChange: func(text string) {
					st.setGlobalShortcutDraft(text)
				},
			})
		})
}

// groupAppShortcuts 按分组归并快捷键，分组顺序沿用默认表里的出现顺序。
func groupAppShortcuts(shortcuts []appShortcut) ([]string, map[string][]appShortcut) {
	var groups []string
	grouped := make(map[string][]appShortcut)
	for _, sc := range shortcuts {
		if _, ok := grouped[sc.group]; !ok {
			groups = append(groups, sc.group)
		}
		grouped[sc.group] = append(grouped[sc.group], sc)
	}
	return groups, grouped
}

// renderShortcutGroup 渲染一个功能分组的快捷键表。
func renderShortcutGroup(c *ui.Context, st *SettingsState, grp string, items []appShortcut, conflicts map[string][]string) {
	renderSettingsSection(c, "sec-shortcuts-"+grp, grp, func() {
		for i, it := range items {
			if i > 0 {
				renderSettingsDivider(c, "div-shortcut-"+it.id)
			}
			itVal := it
			renderSettingRow(c, "row-shortcut-"+itVal.id, itVal.title, func() {
				if itVal.customizable {
					renderShortcutOverrideControl(c, st, itVal, conflicts[itVal.id])
					return
				}
				renderShortcutKeysBadge(c, itVal)
			})
		}
	})
}

// renderShortcutResetAll 渲染「全部恢复默认」按钮。
func renderShortcutResetAll(c *ui.Context, st *SettingsState) {
	resetAll := ui.ButtonBase(c.Key("btn-reset-all-shortcuts")).
		Label("全部恢复默认").
		Radius(RadiusSM).
		Padding(settingsPillPadX, Space4).
		Background(ThemeHover(c))
	if resetAll.Hovered() {
		resetAll.Background(ThemeOverlay(c, 0.15))
	}
	if resetAll.Clicked() {
		st.resetShortcutOverrides()
	}
	resetAll.Children(func() {
		ui.Text(c, "全部恢复默认").
			FontSize(uiFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemePrimaryText(c))
	})
}

// renderShortcutTable 渲染完整快捷键表：恢复默认与各分组。导航页与「快捷键」面板共用；
// hint 非空时在按钮左侧显示一句用法说明。
func renderShortcutTable(c *ui.Context, st *SettingsState, hint string) {
	shortcuts := appShortcuts()
	conflicts := shortcutConflicts(effectiveShortcutBindings())
	groups, grouped := groupAppShortcuts(shortcuts)

	ui.Row(c.Key("shortcuts-toolbar")).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		Margin(Space4, 0, Space1, 0).
		Children(func() {
			if hint != "" {
				ui.Text(c, hint).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)
			}
			renderShortcutResetAll(c, st)
		})

	for _, grp := range groups {
		renderShortcutGroup(c, st, grp, grouped[grp], conflicts)
	}
}

// renderShortcutKeysBadge 渲染快捷键键位小标签。
func renderShortcutKeysBadge(c *ui.Context, sc appShortcut) {
	ui.Box(c.Key("badge-shortcut-"+sc.id)).
		Padding(settingsBadgePadY, Space3).
		Radius(RadiusSM).
		Background(ThemeHover(c)).
		Children(func() {
			ui.Text(c, sc.keys).
				FontSize(uiFont(TypeFootnote)).
				FontWeight(500).
				TextColor(ThemePrimaryText(c))
		})
}

// renderShortcutOverrideControl 渲染可自定义快捷键行的输入框、当前有效键位、重置按钮与冲突/错误提示。
func renderShortcutOverrideControl(c *ui.Context, st *SettingsState, sc appShortcut, conflicts []string) {
	draft := shortcutDraftFor(sc.id)

	ui.Column(c.Key("ctl-shortcut-" + sc.id)).
		Gap(Space1).
		AlignItems(ui.End).
		Children(func() {
			ui.Row(c.Key("ctl-shortcut-row-" + sc.id)).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					TextField(c, TextFieldProps{
						Key:         "input-shortcut-" + sc.id,
						Value:       draft,
						Label:       "快捷键 " + sc.title,
						Placeholder: defaultShortcutKeys(sc),
						Width:       160,
						OnChange: func(string) {
							// 输入只更新草稿；落盘推迟到失焦或防抖到期，避免每个按键都写一次盘。
							st.noteShortcutDraftEdit(sc.id)
						},
						OnFocusChange: func(focused bool) {
							// 失焦立即落盘：获得过焦点后又失去才算，避免输入框刚出现的第一帧误触发。
							if st.trackShortcutDraftFocus(sc.id, focused) {
								st.flushShortcutDrafts(true)
							}
						},
					})

					renderShortcutKeysBadge(c, sc)

					if strings.TrimSpace(st.cfg.Shortcuts.ShortcutOverrides[sc.id]) != "" {
						reset := ui.ButtonBase(c.Key("btn-reset-shortcut-"+sc.id)).
							Label("重置 "+sc.title).
							Radius(RadiusSM).
							Padding(settingsBadgePadY, settingsCompactPadX).
							Background(ThemeOverlay(c, 0.06))
						if reset.Hovered() {
							reset.Background(ThemeHover(c))
						}
						if reset.Clicked() {
							st.applyShortcutOverride(sc.id, "")
						}
						reset.Children(func() {
							ui.Text(c, "重置").
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c))
						})
					}
				})

			if msg := shortcutOverrideError(*draft); msg != "" {
				ui.Text(c, msg).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeDanger(c))
			} else if len(conflicts) > 0 {
				ui.Text(c, shortcutConflictMessage(conflicts)).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeWarning(c))
			}
		})
}

// shortcutDraftsMu 保护设置页快捷键输入框文本。
var shortcutDraftsMu sync.Mutex

// shortcutDrafts 保存设置页输入框文本（快捷键 id -> 文本），保证输入状态跨帧稳定。
var shortcutDrafts = map[string]*string{}

// shortcutDraftFor 返回指定快捷键输入框的文本指针。
func shortcutDraftFor(id string) *string {
	shortcutDraftsMu.Lock()
	defer shortcutDraftsMu.Unlock()
	if p, ok := shortcutDrafts[id]; ok {
		return p
	}
	p := new(string)
	shortcutDrafts[id] = p
	return p
}

// syncShortcutDrafts 用已应用的覆盖刷新输入框文本；未覆盖的条目显示为空（即默认键位）。
func syncShortcutDrafts(overrides map[string]string) {
	shortcutDraftsMu.Lock()
	defer shortcutDraftsMu.Unlock()
	for _, sc := range shortcutDefaults() {
		if !sc.customizable {
			continue
		}
		p, ok := shortcutDrafts[sc.id]
		if !ok {
			p = new(string)
			shortcutDrafts[sc.id] = p
		}
		*p = overrides[sc.id]
	}
}

// setGlobalShortcutDraft 把全局快捷键输入框的草稿写回配置：只在锁内改内存，
// 落盘走设置页统一的文本防抖，避免每按一个键就在界面线程 fsync 一次（E-06）。
func (st *SettingsState) setGlobalShortcutDraft(text string) {
	st.mu.Lock()
	st.cfgMu.Lock()
	changed := st.cfg.Shortcuts.GlobalShortcut != text
	st.cfg.Shortcuts.GlobalShortcut = text
	if st.globalShortcutDraft != nil {
		*st.globalShortcutDraft = text
	}
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if changed {
		st.noteTextEdit()
	}
	st.invalidate()
}

// shortcutOverrideError 返回输入文本的提示：解析失败或属于系统保留组合时给出原因，
// 文本为空或可用时返回空串。
func shortcutOverrideError(text string) string {
	spec := normalizeShortcutSpec(text)
	if spec == "" {
		return ""
	}
	// 先判系统保留再严格解析：Tab 这类键不在可绑定键表内，严格解析只会报「不支持的按键」，
	// 但用户应看到的是「该组合为系统保留」（⌘Tab 等）。
	if mods, key, err := parseShortcutSpecReserved(spec); err == nil && isReservedShortcut(mods, key) {
		return shortcutReservedMessage
	}
	if _, _, err := parseShortcutSpec(spec); err != nil {
		return "无法解析：" + err.Error()
	}
	return ""
}

// shortcutConflictMessage 把冲突的快捷键 id 列表格式化为提示文案。
func shortcutConflictMessage(ids []string) string {
	titles := make([]string, 0, len(ids))
	for _, id := range ids {
		titles = append(titles, shortcutTitle(id))
	}
	return "与「" + strings.Join(titles, "」「") + "」冲突"
}

// noteShortcutDraftEdit 记录一次快捷键输入：草稿本身已由输入框写进 shortcutDrafts，
// 这里只标记待落盘并重排防抖到期时间，不碰配置。落盘与文本输入一样放给界面线程执行
// （见 flushShortcutDrafts），定时器只负责叫醒它，免得后台 goroutine 读写输入框草稿。
func (st *SettingsState) noteShortcutDraftEdit(id string) {
	st.mu.Lock()
	if st.shortcutDirtyIDs == nil {
		st.shortcutDirtyIDs = make(map[string]bool)
	}
	st.shortcutDirtyIDs[id] = true
	st.shortcutSaveDue = time.Now().Add(settingsShortcutSaveDebounce)
	st.mu.Unlock()

	time.AfterFunc(settingsShortcutSaveDebounce+settingsTextSaveGrace, st.invalidate)
}

// trackShortcutDraftFocus 记录输入框的焦点状态，返回是否刚刚失焦（获得过焦点后失去）。
func (st *SettingsState) trackShortcutDraftFocus(id string, focused bool) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.shortcutInputFocus == nil {
		st.shortcutInputFocus = make(map[string]bool)
	}
	if focused {
		st.shortcutInputFocus[id] = true
		return false
	}
	if !st.shortcutInputFocus[id] {
		return false
	}
	delete(st.shortcutInputFocus, id)
	return true
}

// shortcutDraftChanges 把待落盘的草稿转成规范化后的覆盖值；解析失败或系统保留的草稿跳过，
// 与 applyShortcutOverride 的校验规则一致；空文本表示恢复默认（删除该项）。
func shortcutDraftChanges(ids map[string]bool) map[string]string {
	shortcutDraftsMu.Lock()
	defer shortcutDraftsMu.Unlock()
	changes := make(map[string]string, len(ids))
	for id := range ids {
		p, ok := shortcutDrafts[id]
		if !ok || p == nil || shortcutOverrideError(*p) != "" {
			continue
		}
		changes[id] = normalizeShortcutSpec(*p)
	}
	return changes
}

// flushShortcutDrafts 在界面线程把到期的快捷键草稿一次性写进配置并落盘；force 忽略剩余
// 防抖时间（失焦、关面板）。先清掉待落盘标记再写，重复调用不会多写一次盘。
func (st *SettingsState) flushShortcutDrafts(force bool) bool {
	st.mu.Lock()
	ids, due := st.shortcutDirtyIDs, st.shortcutSaveDue
	if len(ids) == 0 || (!force && time.Now().Before(due)) {
		st.mu.Unlock()
		return true
	}
	st.mu.Unlock()

	changes := shortcutDraftChanges(ids)
	if len(changes) != len(ids) {
		st.noteError("快捷键无效，请修正后重试")
		return false
	}

	var next map[string]string
	st.mu.Lock()
	st.cfgMu.Lock()
	func(cfg *config.Config) {
		overrides := cfg.Shortcuts.ShortcutOverrides
		for id, normalized := range changes {
			if normalized == "" {
				delete(overrides, id)
				continue
			}
			if overrides == nil {
				overrides = make(map[string]string)
				cfg.Shortcuts.ShortcutOverrides = overrides
			}
			overrides[id] = normalized
		}
		if len(overrides) == 0 {
			cfg.Shortcuts.ShortcutOverrides = nil
		}
		next = cfg.Shortcuts.ShortcutOverrides
	}(&st.cfg)
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if !st.save() {
		return false
	}
	st.mu.Lock()
	if st.shortcutSaveDue == due {
		st.shortcutDirtyIDs = nil
	}
	st.mu.Unlock()
	setShortcutOverrides(next)
	st.invalidate()
	return true
}

// applyShortcutOverride 应用单条快捷键覆盖并立即生效、持久化；空文本恢复默认，
// 解析失败或系统保留组合不写入。行内重置按钮等显式操作直接走这里，输入框走草稿防抖
// （见 noteShortcutDraftEdit），与其它文本输入一致（E-06）。
func (st *SettingsState) applyShortcutOverride(id string, text string) {
	if shortcutOverrideError(text) != "" {
		return
	}
	// 存规范化后的写法，避免别名（如 CmdOrCtrl）在回读时解析失败而静默回退默认。
	normalized := normalizeShortcutSpec(text)

	// 统一走 updateAndSave（内部按 mu → cfgMu 顺序加锁），setShortcutOverrides 与
	// invalidate 都在锁外调。重建注册表所需的覆盖表在闭包里拍下，不能在锁外回读 st.cfg。
	var next map[string]string
	st.updateAndSave(func(cfg *config.Config) {
		overrides := cfg.Shortcuts.ShortcutOverrides
		if normalized == "" {
			delete(overrides, id)
			if len(overrides) == 0 {
				cfg.Shortcuts.ShortcutOverrides = nil
			}
		} else {
			if overrides == nil {
				overrides = make(map[string]string)
				cfg.Shortcuts.ShortcutOverrides = overrides
			}
			overrides[id] = normalized
		}
		next = cfg.Shortcuts.ShortcutOverrides
	})

	setShortcutOverrides(next)
	st.invalidate()
}

// resetShortcutOverrides 清空全部快捷键覆盖并立即生效、持久化。
func (st *SettingsState) resetShortcutOverrides() {
	// 清掉尚未落盘的草稿：全部恢复默认后，防抖窗口里的旧输入不能再写回配置。
	st.mu.Lock()
	st.shortcutDirtyIDs = nil
	st.mu.Unlock()

	st.updateAndSave(func(cfg *config.Config) {
		cfg.Shortcuts.ShortcutOverrides = nil
	})
	setShortcutOverrides(nil)
	st.invalidate()
}
