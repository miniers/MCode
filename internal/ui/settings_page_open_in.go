package mcodeui

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// settingsPageOpenIn 是设置面板里「打开方式」所在页的导航 id；打开方式已并入「工作台」页，
// 页面注册与渲染分支见 settings.go。
const settingsPageOpenIn = "workbench"

// openInPageState 维护「打开方式」页的表单状态，只在 UI 线程读写。
type openInPageState struct {
	adding    bool
	draftName string
	draftPath string
	err       string
}

// getOpenInPageState 取该设置面板的「打开方式」表单状态；第一次访问时在 st.mu 下懒初始化。
func getOpenInPageState(st *SettingsState) *openInPageState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.openInPage == nil {
		st.openInPage = &openInPageState{}
	}
	return st.openInPage
}

// openInAppsDraft 返回配置里的打开方式列表；尚未配置时基于默认列表做一份草稿，方便直接编辑。
func (st *SettingsState) openInAppsDraft() []config.OpenInAppConfig {
	if len(st.cfg.Workbench.OpenInApps) == 0 {
		return config.DefaultOpenInApps()
	}
	apps := make([]config.OpenInAppConfig, len(st.cfg.Workbench.OpenInApps))
	copy(apps, st.cfg.Workbench.OpenInApps)
	return apps
}

// saveOpenInApps 写回打开方式列表并保存配置；列表清空时保存为空，恢复“未配置即默认”的行为。
func (st *SettingsState) saveOpenInApps(apps []config.OpenInAppConfig) {
	// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Workbench.OpenInApps = apps
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
}

// pickOpenInAppDialog 唤起系统原生选择器挑一个应用；测试中可替换。
var pickOpenInAppDialog = func() (string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:    "选择应用",
		Multiple: false,
	})
	if err != nil || len(paths) == 0 {
		return "", false, err
	}
	return paths[0], true, nil
}

// openInAppPathSuffix 返回当前平台应用路径应有的后缀：macOS 是 .app，Windows 是 .exe。
func openInAppPathSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ".app"
}

// openInAppNameFromPath 从应用路径推断应用名：去掉平台对应的可执行文件后缀。
func openInAppNameFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), openInAppPathSuffix())
}

// openInAppPathPlaceholder 返回「应用路径」输入框的示例占位符。
func openInAppPathPlaceholder() string {
	if runtime.GOOS == "windows" {
		return `%LOCALAPPDATA%\Programs\cursor\Cursor.exe`
	}
	return "/Applications/Cursor.app"
}

// openInDefaultBundleID 为常见应用补一个默认的 Bundle ID，仅作记录，打开时仍以 AppPath 为准。
func openInDefaultBundleID(name, path string) string {
	lower := strings.ToLower(name + " " + path)
	switch {
	case strings.Contains(lower, "visual studio code"):
		return "com.microsoft.VSCode"
	case strings.Contains(lower, "cursor"):
		return "com.todesktop.230313mzl4w4u92"
	case strings.Contains(lower, "zed"):
		return "dev.zed.Zed"
	case strings.Contains(lower, "xcode"):
		return "com.apple.dt.Xcode"
	case strings.Contains(lower, "iterm"):
		return "com.googlecode.iterm2"
	case strings.Contains(lower, "qspace"):
		return "com.jinghaoshe.qspace.pro"
	}
	return ""
}

// openInIconButton 渲染打开方式行内的图标按钮：hover 时出现底色，danger 为 true 时用危险色。
func openInIconButton(c *ui.Context, key, label string, icon *ui.SVG, danger bool, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(label).
		Size(ControlSM, ControlSM).
		Radius(RadiusSM).
		Center()
	if btn.Hovered() {
		if danger {
			btn.Background(ThemeDanger(c).Alpha(0.12))
		} else {
			btn.Background(ThemeHover(c))
		}
	}
	btn.OnClick(onClick)
	color := ThemePrimaryText(c).Alpha(0.70)
	if danger {
		color = ThemeDanger(c)
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
	})
}

func renderPageOpenIn(c *ui.Context, st *SettingsState) {
	ps := getOpenInPageState(st)
	apps := st.openInAppsDraft()
	ui.Column(c.Key("openin-sections-container")).Gap(Space5).Children(func() {
		renderSettingsSection(c, "sec-openin-apps", "应用列表", func() {
			for i := range apps {
				renderOpenInAppRow(c, st, apps, i)
			}
			renderOpenInAddApp(c, st, ps)
		})
	})
}

// renderOpenInAppRow 渲染打开方式列表中的一项：名称、开关、上移/下移、删除。
func renderOpenInAppRow(c *ui.Context, st *SettingsState, apps []config.OpenInAppConfig, i int) {
	app := apps[i]
	if i > 0 {
		renderSettingsDivider(c, "div-openin-"+app.Name)
	}
	renderSettingRow(c, "row-openin-"+app.Name, app.Name, func() {
		toggleFunc(
			func(*SettingsState) (bool, bool) { return app.Enabled, false },
			func(st *SettingsState, on bool) {
				next := append([]config.OpenInAppConfig(nil), apps...)
				next[i].Enabled = on
				st.saveOpenInApps(next)
				st.invalidate()
			},
		).render(c, st, setting{ID: "openin-" + app.Name, Title: app.Name})

		openInIconButton(c, "btn-openin-up-"+app.Name, "上移", IconArrowUp(), false, func() {
			if i == 0 {
				return
			}
			next := append([]config.OpenInAppConfig(nil), apps...)
			next[i-1], next[i] = next[i], next[i-1]
			st.saveOpenInApps(next)
			st.invalidate()
		})

		openInIconButton(c, "btn-openin-down-"+app.Name, "下移", IconMoveDown(), false, func() {
			if i >= len(apps)-1 {
				return
			}
			next := append([]config.OpenInAppConfig(nil), apps...)
			next[i+1], next[i] = next[i], next[i+1]
			st.saveOpenInApps(next)
			st.invalidate()
		})

		openInIconButton(c, "btn-openin-delete-"+app.Name, "删除", IconTrash2(), true, func() {
			next := append([]config.OpenInAppConfig(nil), apps[:i]...)
			next = append(next, apps[i+1:]...)
			st.saveOpenInApps(next)
			st.invalidate()
		})
	})
}

// renderOpenInAddApp 渲染「添加应用…」：展开后填写名称与 .app 路径，也可以从系统选择器挑选应用。
func renderOpenInAddApp(c *ui.Context, st *SettingsState, ps *openInPageState) {
	ui.Column(c.Key("openin-add")).Padding(settingsCardPad, Space5).Gap(Space3).Children(func() {
		if !ps.adding {
			addBtn := ui.ButtonBase(c.Key("btn-openin-add")).
				Label("添加应用…").
				Padding(Space2, Space4).
				Radius(RadiusSM).
				Border(Hairline, ThemeBorder(c))
			if addBtn.Hovered() {
				addBtn.Background(ThemeHover(c))
			}
			if addBtn.Clicked() {
				ps.adding = true
				ps.err = ""
				st.invalidate()
			}
			addBtn.Children(func() {
				ui.Row(c.Key("btn-openin-add-row")).Gap(Space2).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, IconPlus()).Size(IconSM, IconSM).TextColor(ThemePrimaryText(c))
					ui.Text(c, "添加应用…").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
				})
			})
			return
		}

		textFunc(
			func(*SettingsState) (string, bool) { return ps.draftName, false },
			func(_ *SettingsState, v string) { ps.draftName = v },
			"例如 Cursor",
		).render(c, st, setting{ID: "input-openin-name", Title: "应用名称"})
		ui.Row(c.Key("row-openin-path")).Gap(Space2).AlignItems(ui.Center).Children(func() {
			textFunc(
				func(*SettingsState) (string, bool) { return ps.draftPath, false },
				func(_ *SettingsState, v string) { ps.draftPath = v },
				openInAppPathPlaceholder(),
			).render(c, st, setting{ID: "input-openin-path", Title: "应用路径"})

			// 复用系统的 mygo.Dialog.Open 选择 .app；对话框阻塞，放到后台，选完再由 UI 更新草稿。
			chooseBtn := ui.ButtonBase(c.Key("btn-openin-choose")).
				Label("选择…").
				Padding(Space2, settingsCompactPadX).
				Radius(RadiusSM).
				Border(Hairline, ThemeBorder(c))
			if chooseBtn.Hovered() {
				chooseBtn.Background(ThemeHover(c))
			}
			if chooseBtn.Clicked() {
				go func() {
					path, ok, err := pickOpenInAppDialog()
					if err != nil || !ok || strings.TrimSpace(path) == "" {
						return
					}
					ps.draftPath = path
					if strings.TrimSpace(ps.draftName) == "" {
						ps.draftName = openInAppNameFromPath(path)
					}
					st.invalidate()
				}()
			}
			chooseBtn.Children(func() {
				ui.Text(c, "选择…").FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
			})
		})

		if ps.err != "" {
			ui.Text(c, ps.err).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
		}

		ui.Row(c.Key("row-openin-add-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
			add := ui.ButtonBase(c.Key("btn-openin-add-confirm")).
				Label("添加").
				Padding(Space2, Space4).
				Radius(RadiusSM).
				Background(ThemeAccent(c))
			if add.Clicked() {
				name := strings.TrimSpace(ps.draftName)
				path := strings.TrimSpace(ps.draftPath)
				if path == "" || !strings.HasSuffix(strings.ToLower(path), openInAppPathSuffix()) {
					ps.err = "请填写 " + openInAppPathSuffix() + " 应用的完整路径"
				} else {
					if name == "" {
						name = openInAppNameFromPath(path)
					}
					app := config.OpenInAppConfig{
						Name:     name,
						BundleID: openInDefaultBundleID(name, path),
						AppPath:  path,
						Enabled:  true,
					}
					next := append(st.openInAppsDraft(), app)
					st.saveOpenInApps(next)
					ps.adding = false
					ps.draftName = ""
					ps.draftPath = ""
					ps.err = ""
					st.invalidate()
				}
			}
			add.Children(func() {
				ui.Text(c, "添加").FontSize(uiFont(TypeFootnote)).FontWeight(600)
			})

			cancel := ui.ButtonBase(c.Key("btn-openin-add-cancel")).
				Label("取消").
				Padding(Space2, Space4).
				Radius(RadiusSM)
			if cancel.Hovered() {
				cancel.Background(ThemeHover(c))
			}
			if cancel.Clicked() {
				ps.adding = false
				ps.err = ""
				st.invalidate()
			}
			cancel.Children(func() {
				ui.Text(c, "取消").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			})
		})
	})
}
