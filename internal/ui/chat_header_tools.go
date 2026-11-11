package mcodeui

import (
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// headerScriptsState 维护标题栏脚本菜单的交互状态，只在 UI 线程读写（按 chatState 附加）。
// expanded 控制「全部脚本」折叠行是否展开，仅在列表较长时使用。
type headerScriptsState struct {
	expanded bool
}

// headerScriptsCollapseThreshold 是脚本菜单超过多少项时折叠「全部脚本」。
const headerScriptsCollapseThreshold = 10

// 标题栏右侧动作胶囊组与辅助面板胶囊组（chat_header.go 的 renderChatHeaderActionGroup、
// renderChatHeaderAuxGroup）共用的尺寸：
// 按钮 24pt 见方、外层内边距 2pt，两组胶囊据此固定为同一高度 28pt，矮的不会随内容变化。
const (
	headerCapsuleButtonSize = 24
	headerCapsulePadding    = 2
	headerCapsuleHeight     = headerCapsuleButtonSize + 2*headerCapsulePadding
)

// headerGitMenuOption 在标题栏弹出菜单里渲染一行可点击选项；左侧图标由调用方给出，
// 便于脚本/打开方式菜单与渲染逻辑区分。
func headerGitMenuOption(c *ui.Context, key, label, title, detail string, icon *ui.SVG, onClick func()) {
	opt := ui.ButtonBase(c.Key(key)).
		Label(label).
		Padding(Space2, Space3).
		Radius(RadiusPill).
		FillWidth()
	if opt.Hovered() {
		opt.Background(ThemeHover(c))
	}
	if onClick != nil {
		opt.OnClick(onClick)
	}
	opt.Children(func() {
		ui.Row(c.Key(key + "-row")).
			Gap(Space3).
			AlignItems(ui.Center).
			Grow(1).
			Children(func() {
				if icon != nil {
					ui.Icon(c, icon).Size(14, 14).TextColor(ThemeSecondaryText(c))
				}
				ui.Text(c, title).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				if detail != "" {
					ui.Text(c, detail).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				}
			})
	})
}

// renderHeaderScriptsMenuButton 渲染标题栏执行命令按钮：有项目工作目录就显示，
// 没有脚本时菜单给出空状态与「添加脚本…」入口；菜单同时列出项目脚本（在前）与 package.json
// 脚本（只读展示），只要存在项目脚本就提供「编辑脚本…」入口打开应用内的脚本编辑弹窗
// （scripts_sheet.go）。
// 数据复用 chat_header.go 的缓存（loadHeaderScripts 加载、getChatHeaderState 读取），不重复实现。
func renderHeaderScriptsMenuButton(c *ui.Context, s *chatState, threadID string) {
	if s == nil {
		return
	}
	// 脚本编辑弹窗独立于菜单与按钮可见性：打开后每帧都要渲染。
	renderScriptsSheet(c, s)
	if !s.headerScriptsButtonVisible(threadID) {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	scripts := append([]Script(nil), hs.scripts[threadID]...)
	_, loaded := hs.scripts[threadID]
	chatHeaderMu.Unlock()
	// 项目脚本排在 package.json 脚本之前（各自保持原有顺序）。
	ordered := make([]Script, 0, len(scripts))
	for _, sc := range scripts {
		if sc.Kind == "project" {
			ordered = append(ordered, sc)
		}
	}
	for _, sc := range scripts {
		if sc.Kind != "project" {
			ordered = append(ordered, sc)
		}
	}
	scripts = ordered
	// 本会话还没拉取过脚本时补拉一次（非 Git 会话不会走 renderChatHeaderGit 的加载路径）；
	// 加载完成后 scripts 里会出现条目（可能为空列表）；失败时不缓存并按指数退避，退避期内不会每帧重试。
	if !loaded {
		s.loadHeaderScripts(threadID)
	}

	mn := headerScriptsViewState(s)
	trigger := IconButton(c.Key("chat-header-scripts-btn-unified"), IconButtonProps{
		Icon:   IconPlay(),
		Label:  "运行项目脚本",
		Active: hs.scriptsMenuOpen,
		OnClick: func() {
			hs.scriptsMenuOpen = !hs.scriptsMenuOpen
			closeProjectActionMenus(hs, "scripts")
			hs.titleMenuOpen = false
			hs.branchMenuOpen = false
			hs.commitMenuOpen = false
			s.invalidate()
		},
		Variant: ButtonGhost,
		Size:    ControlSM,
	})

	appPopover(c.Key("chat-header-scripts-popover-unified"), trigger, &hs.scriptsMenuOpen, func() {
		hs.scriptsMenuOpen = false
	}, func() {
		popoverCard(c, ui.Column(c.Key("chat-header-scripts-menu-unified"))).
			Gap(Space0).
			Padding(Space1).
			MinWidth(220).
			MaxWidth(320).
			Children(func() {
				ui.Text(c, "执行命令").
					FontSize(uiFont(TypeCaption)).
					Bold().
					TextColor(ThemeSecondaryText(c)).
					Padding(Space1, Space3, Space0, Space3)
				if loaded && len(scripts) == 0 {
					ui.Text(c, "暂无可用脚本").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						Padding(Space1, Space3)
				}
				visible := scripts
				if len(scripts) > headerScriptsCollapseThreshold && !mn.expanded {
					visible = scripts[:headerScriptsCollapseThreshold]
				}
				for _, sc := range visible {
					script := sc
					key := "chat-header-script-" + script.Name
					if script.ID != "" {
						key = "chat-header-script-" + script.ID
					}
					headerGitMenuOption(c, key, script.Name, script.Name, script.Command, IconTerminal(), func() {
						hs.scriptsMenuOpen = false
						s.runScriptInTerminal(script)
						s.invalidate()
					})
				}
				if len(scripts) > headerScriptsCollapseThreshold {
					renderHeaderScriptsExpandRow(c, s, mn, len(scripts))
				}

				if len(scripts) > 0 {
					ui.Box(c.Key("chat-header-scripts-divider")).
						FillWidth().
						Height(Hairline).
						Background(ThemeDivider(c)).
						Margin(4, 4, 4, 4)
				}

				// 只有项目脚本能在弹窗里编辑，package.json 脚本只读展示。
				label := "添加脚本…"
				for _, sc := range scripts {
					if sc.Kind == "project" {
						label = "编辑脚本…"
						break
					}
				}
				headerGitMenuOption(c, "chat-header-scripts-edit", label, label, "", IconPencilLine(), func() {
					hs.scriptsMenuOpen = false
					openScriptsSheet(s, threadID)
				})
			})
	})
}

// renderHeaderScriptsExpandRow 渲染脚本菜单的「全部脚本（n）」折叠行；点击只展开/收起，不关闭菜单。
func renderHeaderScriptsExpandRow(c *ui.Context, s *chatState, mn *headerScriptsState, total int) {
	label := "全部脚本（" + strconv.Itoa(total) + "）"
	more := ui.ButtonBase(c.Key("chat-header-script-toggle-all")).
		Label(label).
		Padding(Space2, Space3).
		Radius(RadiusPill).
		FillWidth()
	if more.Hovered() {
		more.Background(ThemeHover(c))
	}
	if more.Clicked() {
		mn.expanded = !mn.expanded
		s.invalidate()
	}
	more.Children(func() {
		ui.Row(c.Key("chat-header-script-toggle-all-row")).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				chevron := IconChevronRight()
				if mn.expanded {
					chevron = IconChevronDown()
				}
				ui.Icon(c, chevron).Size(14, 14).TextColor(ThemeSecondaryText(c))
				ui.Text(c, label).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			})
	})
}

// headerScriptsMu / headerScriptsMenus 按 chatState 缓存脚本菜单的交互状态（同 chatToolbarState）。
var (
	headerScriptsMu    sync.Mutex
	headerScriptsMenus = make(map[*chatState]*headerScriptsState)
)

func headerScriptsViewState(s *chatState) *headerScriptsState {
	headerScriptsMu.Lock()
	defer headerScriptsMu.Unlock()
	st, ok := headerScriptsMenus[s]
	if !ok {
		st = &headerScriptsState{}
		headerScriptsMenus[s] = st
	}
	return st
}

// currentWorkbenchConfig 返回设置面板里当前加载的工作台配置；没有设置状态时回落磁盘配置，再回落默认值。
func currentWorkbenchConfig(s *chatState) config.WorkbenchConfig {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		wb := st.cfg.Workbench
		st.mu.Unlock()
		return wb
	}
	return config.Defaults().Workbench
}

// installedOpenInApps 把配置里的打开方式转成菜单项：只保留已启用、本机可用的应用。
// 可用指应用路径存在，或（macOS 上）配置了非空 BundleID，后者用 open -b 唤起。
// 文件管理器与终端由调用方单独列出，这里只处理配置中的应用。
func installedOpenInApps(wb config.WorkbenchConfig, statFn func(string) bool) []openInApp {
	if statFn == nil {
		statFn = defaultOpenInAppStat
	}
	var apps []openInApp
	for _, app := range wb.OpenInAppsOrDefault() {
		if !app.Enabled {
			continue
		}
		path := strings.TrimSpace(app.AppPath)
		bundle := strings.TrimSpace(app.BundleID)
		switch {
		case path != "" && statFn(path):
			apps = append(apps, openInApp{Name: app.Name, AppPath: path, BundleID: bundle})
		case bundle != "" && openInGOOS == "darwin":
			apps = append(apps, openInApp{Name: app.Name, AppPath: path, BundleID: bundle})
		}
	}
	return apps
}

// openInDefaultEditor 用「打开方式」列表里第一个可用应用打开目录（默认列表首位是编辑器），
// 一个可用应用都没有时退回系统文件管理器打开目录本身。
func openInDefaultEditor(s *chatState, dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	if apps := installedOpenInApps(currentWorkbenchConfig(s), openInStatFn); len(apps) > 0 {
		openInWithApp(apps[0], dir)
		return
	}
	openInWithApp(openInApp{Name: openInFileManagerName()}, dir)
}

// renderHeaderOpenInButton 渲染标题栏「打开方式」按钮：菜单列出配置中已启用且已安装的应用，
// 末尾是「自定义…」，跳到设置里的「打开方式」页。打开逻辑复用 openInWithApp，不改原函数。
func renderHeaderOpenInButton(c *ui.Context, s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	dir := s.headerProjectDir(threadID)
	if dir == "" {
		return
	}
	hs := getChatHeaderState(s)
	apps := installedOpenInApps(currentWorkbenchConfig(s), openInStatFn)

	trigger := IconButton(c.Key("chat-header-open-in-btn-unified"), IconButtonProps{
		Icon:   IconSquareArrowOutUpRight(),
		Label:  "在其它应用中打开",
		Active: hs.openInMenuOpen,
		OnClick: func() {
			hs.openInMenuOpen = !hs.openInMenuOpen
			closeProjectActionMenus(hs, "openin")
			hs.titleMenuOpen = false
			hs.branchMenuOpen = false
			hs.commitMenuOpen = false
			s.invalidate()
		},
		Variant: ButtonGhost,
		Size:    ControlSM,
	})

	openSettings := func() {
		hs.openInMenuOpen = false
		openSettingsWindow(s, "openIn")
		s.invalidate()
	}

	appPopover(c.Key("chat-header-open-in-popover-unified"), trigger, &hs.openInMenuOpen, func() {
		hs.openInMenuOpen = false
	}, func() {
		popoverCard(c, ui.Column(c.Key("chat-header-open-in-menu-unified"))).
			Gap(Space0).
			Padding(Space1).
			MinWidth(200).
			MaxWidth(300).
			Children(func() {
				ui.Text(c, "打开方式").
					FontSize(uiFont(TypeCaption)).
					Bold().
					TextColor(ThemeSecondaryText(c)).
					Padding(Space1, Space3, Space0, Space3)
				headerGitMenuOption(c, "chat-header-open-in-unified-finder", openInFileManagerName(), openInFileManagerName(), "", IconFolderOpen(), func() {
					hs.openInMenuOpen = false
					openInWithApp(openInApp{Name: openInFileManagerName()}, dir)
					s.invalidate()
				})
				// 终端读取 General.TerminalShell 配置的终端应用，空则默认 Terminal。
				headerGitMenuOption(c, "chat-header-open-in-unified-terminal", "在终端中打开", "在终端中打开", "", IconTerminal(), func() {
					hs.openInMenuOpen = false
					if err := openDirInExternalTerminal(dir, getTerminalConfiguredShell(s)); err != nil {
						s.setToast("打开终端失败: " + err.Error())
					}
					s.invalidate()
				})
				for _, app := range apps {
					a := app
					headerGitMenuOption(c, "chat-header-open-in-unified-"+a.Name, a.Name, a.Name, "", IconExternalLink(), func() {
						hs.openInMenuOpen = false
						openInWithApp(a, dir)
						s.invalidate()
					})
				}
				ui.Box(c.Key("chat-header-open-in-unified-divider")).
					FillWidth().
					Height(Hairline).
					Background(ThemeDivider(c)).
					Margin(4, 4, 4, 4)
				headerGitMenuOption(c, "chat-header-open-in-unified-copy-path", "复制路径", "复制路径", "", IconCopy(), func() {
					hs.openInMenuOpen = false
					c.WriteClipboard(dir)
					s.setToast("已复制路径")
					s.invalidate()
				})
				headerGitMenuOption(c, "chat-header-open-in-unified-custom", "自定义…", "自定义…", "", IconSettings(), openSettings)
			})
	})
}
