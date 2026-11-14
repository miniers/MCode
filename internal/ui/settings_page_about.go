package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
	"mcode/internal/datadir"
)

// AppUpdateStatus 描述一次更新检查的结果。
type AppUpdateStatus struct {
	Current     string
	Latest      string
	URL         string
	Available   bool
	Installable bool
}

// appUpdateClient 由 internal/app 的 Client 实现；未实现时关于页隐藏检查更新入口，
// 版本号回退显示 dev。
type appUpdateClient interface {
	CheckForUpdate(ctx context.Context) (AppUpdateStatus, error)
	AppVersion() string
}

// appUpdateLatestClient 提供后台最近一次更新检查结果。
type appUpdateLatestClient interface {
	LatestUpdate() AppUpdateStatus
}

func updateAvailable(client any) bool {
	latest, ok := client.(appUpdateLatestClient)
	return ok && latest.LatestUpdate().Available
}

func renderUpdateDot(c *ui.Context, key string, appearing bool) ui.Element {
	dot := renderUnreadDot(c, key).Label("有新版本")
	progress := float32(1)
	if !reduceMotion(c) {
		if appearing {
			dot.Jump("update-dot", 0)
		}
		progress = dot.AnimateTo("update-dot", 1, ui.Animation{Duration: animateDuration(c, DurFast), Ease: motionEaseStandard})
	}
	return dot.Scale(progress, progress).Opacity(progress)
}

// appDiagnosticsClient 由 internal/app 的 Client 实现；未实现时关于页隐藏诊断导出入口。
type appDiagnosticsClient interface {
	ExportDiagnosticsTo(dst string) error
}

// aboutPageState 维护关于页的运行时状态：一次性加载的版本与能力快照，
// 以及「检查更新」「导出诊断日志」的交互状态。
type aboutPageState struct {
	mu sync.Mutex

	// loaded/version/hasUpdate/hasDiag 是数据入口取好的快照，渲染只读这些字段。
	loaded    bool
	version   string
	hasUpdate bool
	hasDiag   bool

	checking   bool
	done       bool
	status     AppUpdateStatus
	errMsg     string
	installing bool
	progress   float64

	// 诊断日志导出状态。
	exporting bool
	diagMsg   string
	diagErr   string

	dataDirTarget  string
	dataDirConfirm bool
	dataDirErr     string
}

var globalAboutState aboutPageState

// resetAboutPageStateForTest 重置关于页状态，供单元测试隔离使用。
func resetAboutPageStateForTest() {
	globalAboutState.mu.Lock()
	defer globalAboutState.mu.Unlock()
	globalAboutState.loaded = false
	globalAboutState.version = ""
	globalAboutState.hasUpdate = false
	globalAboutState.hasDiag = false
	globalAboutState.checking = false
	globalAboutState.done = false
	globalAboutState.status = AppUpdateStatus{}
	globalAboutState.errMsg = ""
	globalAboutState.installing = false
	globalAboutState.progress = 0
	globalAboutState.exporting = false
	globalAboutState.diagMsg = ""
	globalAboutState.diagErr = ""
	globalAboutState.dataDirTarget = ""
	globalAboutState.dataDirConfirm = false
	globalAboutState.dataDirErr = ""
}

// aboutUpdateClient 取设置面板关联的 Client，并在其支持更新检查时返回对应接口。
func aboutUpdateClient(st *SettingsState) appUpdateClient {
	client, ok := archiveClient(st).(appUpdateClient)
	if !ok {
		return nil
	}
	return client
}

// aboutDiagnosticsClient 取设置面板关联的 Client，并在其支持诊断导出时返回对应接口。
func aboutDiagnosticsClient(st *SettingsState) appDiagnosticsClient {
	client, ok := archiveClient(st).(appDiagnosticsClient)
	if !ok {
		return nil
	}
	return client
}

// aboutVersionLoader 读取客户端版本号；测试替换它来统计外部调用次数。
var aboutVersionLoader = func(client appUpdateClient) string { return client.AppVersion() }

// loadAboutPageData 是关于页唯一的数据入口：第一帧取一次版本号与可用能力，
// 之后渲染只读快照，重复渲染不会再调用客户端。
func loadAboutPageData(st *SettingsState) {
	if status, ok := latestUpdateStatus(archiveClient(st)); ok && status.Available {
		globalAboutState.mu.Lock()
		if !globalAboutState.checking && !globalAboutState.done {
			globalAboutState.status = status
			globalAboutState.done = true
		}
		globalAboutState.mu.Unlock()
	}
	globalAboutState.mu.Lock()
	if globalAboutState.loaded {
		globalAboutState.mu.Unlock()
		return
	}
	globalAboutState.mu.Unlock()

	updater := aboutUpdateClient(st)
	diagnostics := aboutDiagnosticsClient(st)
	version := "dev"
	if updater != nil {
		if v := aboutVersionLoader(updater); v != "" {
			version = v
		}
	}

	globalAboutState.mu.Lock()
	globalAboutState.loaded = true
	globalAboutState.version = version
	globalAboutState.hasUpdate = updater != nil
	globalAboutState.hasDiag = diagnostics != nil
	globalAboutState.mu.Unlock()
}

// aboutVersion 返回关于页展示的版本号快照；客户端不支持更新检查时回退为 dev。
func aboutVersion(st *SettingsState) string {
	loadAboutPageData(st)
	globalAboutState.mu.Lock()
	defer globalAboutState.mu.Unlock()
	if globalAboutState.version == "" {
		return "dev"
	}
	return globalAboutState.version
}

// runAppUpdateCheck 同步执行一次更新检查并写入页面状态；拆出便于单元测试直接驱动。
func runAppUpdateCheck(st *SettingsState, client appUpdateClient) {
	status, err := client.CheckForUpdate(context.Background())

	globalAboutState.mu.Lock()
	globalAboutState.checking = false
	globalAboutState.done = true
	if err != nil {
		globalAboutState.errMsg = err.Error()
		globalAboutState.status = AppUpdateStatus{}
	} else {
		globalAboutState.status = status
		globalAboutState.errMsg = ""
	}
	globalAboutState.mu.Unlock()

	if st != nil {
		st.invalidate()
	}
}

// startUpdateCheck 在后台发起更新检查，结果写入页面状态并触发重绘。
func startUpdateCheck(st *SettingsState) {
	client := aboutUpdateClient(st)
	if client == nil {
		return
	}

	globalAboutState.mu.Lock()
	if globalAboutState.checking {
		globalAboutState.mu.Unlock()
		return
	}
	globalAboutState.checking = true
	globalAboutState.done = false
	globalAboutState.errMsg = ""
	globalAboutState.mu.Unlock()

	st.invalidate()

	go runAppUpdateCheck(st, client)
}

// openExternalURL 用系统默认应用（浏览器）打开外链。
func openExternalURL(url string) {
	if url == "" {
		return
	}
	_ = mygo.Shell.OpenExternal(url)
}

var pageAbout = settingsPage{
	ID: "about", Title: "关于", Icon: IconInfo(),
	Sections: []settingsSection{
		{ID: "about.app", Title: "关于 MCode", Rows: []setting{{ID: "row-about-app", Control: customFullWidth(renderAboutFooter)}}},
		{ID: "about.changelog", Title: "更新日志", Rows: []setting{{ID: "row-about-changelog", Control: customFullWidth(renderAboutChangelog)}}},
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageAbout) }

func renderPageAbout(c *ui.Context, st *SettingsState) { renderSettingsTreePage(c, st, pageAbout) }

var aboutChangelogExpanded = map[string]bool{}

func renderAboutChangelog(c *ui.Context, st *SettingsState) {
	ui.Column(c.Key("about-changelog-content")).FillWidth().Children(func() {
		for _, version := range changelogVersions() {
			expanded := aboutChangelogExpanded[version.Version]
			header := ui.ButtonBase(c.Key("about-changelog-header-"+version.Version)).
				Label("about-changelog-header-"+version.Version).
				Padding(settingsRowPadY, settingsRowPadX).
				Radius(RadiusLG)
			if header.Hovered() {
				header.Background(ThemeHover(c))
			} else {
				header.Background(ui.Transparent)
			}
			header.OnClick(func() {
				aboutChangelogExpanded[version.Version] = !expanded
				st.invalidate()
			})
			header.Children(func() {
				ui.Row(c.Key("about-changelog-row-" + version.Version)).
					Grow(1).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Children(func() {
						ui.Text(c, version.Version).FontSize(uiFont(TypeProse)).TextColor(ThemePrimaryText(c))
						icon := IconChevronRight()
						if expanded {
							icon = IconChevronDown()
						}
						ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})
			})
			if expanded {
				ui.Column(c.Key("about-changelog-body-"+version.Version)).FillWidth().Padding(settingsRowPadY, settingsRowPadX).Children(func() {
					markdown.View(c.Key("about-changelog-markdown-"+version.Version), version.Body, swarmReportMarkdownOptions(c, st.chat, TypeBody))
				})
			}
		}
	})
}

// aboutFooterRows 是关于页底部的四行声明：版本号与数据目录常驻，
// 「检查更新」「导出诊断日志」只在客户端支持对应能力时出现。
var aboutFooterRows = []setting{
	{ID: "row-about-version", Title: "版本号", Control: custom(renderAboutVersionValue)},
	{ID: "row-about-update", Title: "检查更新", Control: custom(renderAboutUpdateControls)},
	{ID: "row-about-auto-update", Title: "自动检查更新", Control: toggle(ptr(func(c *config.Config) **bool { return &c.AutoCheckUpdate }, true))},
	{ID: "row-about-diag", Title: "导出诊断日志", Control: custom(renderAboutDiagnosticsControls)},
	{ID: "row-about-datadir", Title: "数据目录", Control: custom(renderAboutDataDirValue)},
}

// aboutFooterRowVisible 报告一行在当前能力快照下是否出现。
func aboutFooterRowVisible(id string, hasUpdate, hasDiag bool) bool {
	switch id {
	case "row-about-update", "row-about-auto-update":
		return hasUpdate
	case "row-about-diag":
		return hasDiag
	}
	return true
}

// aboutRowDivider 返回一行上方的分隔线 key，沿用原有 key 以免索引与快照变动。
func aboutRowDivider(id string) string {
	switch id {
	case "row-about-update":
		return "div-about-update"
	case "row-about-auto-update":
		return "div-about-auto-update"
	case "row-about-diag":
		return "div-about-diag"
	case "row-about-datadir":
		return "div-about-1"
	}
	return ""
}

func renderAboutFooter(c *ui.Context, st *SettingsState) {
	loadAboutPageData(st)

	globalAboutState.mu.Lock()
	hasUpdate := globalAboutState.hasUpdate
	hasDiag := globalAboutState.hasDiag
	globalAboutState.mu.Unlock()

	for _, s := range aboutFooterRows {
		if !aboutFooterRowVisible(s.ID, hasUpdate, hasDiag) {
			continue
		}
		if key := aboutRowDivider(s.ID); key != "" {
			renderSettingsDivider(c, key)
		}
		renderSettingsTreeSetting(c, st, s)
	}
}

func renderAboutVersionValue(c *ui.Context, st *SettingsState) {
	ui.Text(c, aboutVersion(st)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
}

func renderAboutDataDirValue(c *ui.Context, st *SettingsState) {
	migrationErr := datadir.MigrationError()
	ui.Row(c.Key("about-data-dir-value")).AlignItems(ui.Center).Gap(Space3).Children(func() {
		if migrationErr != nil {
			ui.Column(c.Key("about-data-dir-path")).Grow(1).Gap(Space1).Children(func() {
				ui.Text(c, st.dataDir).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
				ui.Text(c, "迁移失败："+migrationErr.Error()).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).FillWidth()
			})
		} else {
			ui.Text(c, st.dataDir).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine().Grow(1)
		}
		if datadir.Pinned() {
			return
		}
		change := ui.ButtonBase(c.Key("btn-about-change-data-dir")).
			Label("更改…").
			Padding(Space2, Space3).
			Radius(RadiusSM).
			Background(ThemeOverlay(c, 0.06))
		{
			scale := motionPressScale(c, change, "button-press-scale", change.Pressed())
			change.Scale(scale, scale)
		}
		if change.Hovered() {
			change.Background(ThemeHover(c))
		}
		if change.Clicked() {
			startAboutDataDirChange(st)
		}
		change.Children(func() {
			ui.Text(c, "更改…").FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
		})
	})

	globalAboutState.mu.Lock()
	target := globalAboutState.dataDirTarget
	open := globalAboutState.dataDirConfirm
	errMsg := globalAboutState.dataDirErr
	globalAboutState.mu.Unlock()
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          "about-data-dir-move",
		Open:         open,
		Tone:         ConfirmSafe,
		Title:        "移动数据目录",
		Body:         target,
		ConfirmLabel: "移动并重启",
		Footer: func() {
			if errMsg != "" {
				ui.Text(c, errMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
			}
		},
		OnCancel: func() {
			clearAboutDataDirChange()
			st.invalidate()
		},
		OnConfirm: func() {
			confirmAboutDataDirMove(st, target)
		},
	})
}

func confirmAboutDataDirMove(st *SettingsState, target string) {
	if err := datadir.SetPending(st.dataDir, target); err != nil {
		globalAboutState.mu.Lock()
		globalAboutState.dataDirErr = err.Error()
		globalAboutState.mu.Unlock()
		st.invalidate()
		return
	}
	clearAboutDataDirChange()
	datadir.PrepareRelaunch()
	appRelaunch()
}

func startAboutDataDirChange(st *SettingsState) {
	time.AfterFunc(0, func() {
		picker := pickFolderDialog
		if picker == nil {
			picker = defaultPickFolder
		}
		path, ok, err := picker()
		if err != nil || !ok || strings.TrimSpace(path) == "" {
			return
		}
		globalAboutState.mu.Lock()
		globalAboutState.dataDirTarget = aboutDataDirTarget(path)
		globalAboutState.dataDirErr = ""
		globalAboutState.dataDirConfirm = true
		globalAboutState.mu.Unlock()
		st.invalidate()
	})
}

func clearAboutDataDirChange() {
	globalAboutState.mu.Lock()
	globalAboutState.dataDirTarget = ""
	globalAboutState.dataDirErr = ""
	globalAboutState.dataDirConfirm = false
	globalAboutState.mu.Unlock()
}

// renderAboutUpdateControls 渲染「检查更新」行的状态文本与按钮，只读快照。
func renderAboutUpdateControls(c *ui.Context, st *SettingsState) {
	globalAboutState.mu.Lock()
	checking := globalAboutState.checking
	done := globalAboutState.done
	status := globalAboutState.status
	errMsg := globalAboutState.errMsg
	installing := globalAboutState.installing
	progress := globalAboutState.progress
	globalAboutState.mu.Unlock()
	installer, canInstall := archiveClient(st).(appUpdateInstallClient)

	ui.Row(c.Key("row-about-update-controls")).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			switch {
			case installing:
				ui.Text(c, fmt.Sprintf("下载中 %.0f%%", progress*100)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			case checking:
				ui.Text(c, "检查中…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			case errMsg != "":
				ui.Text(c, errMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
			case done && status.Available:
				ui.Text(c, fmt.Sprintf("发现新版本 v%s", status.Latest)).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
			case done:
				ui.Text(c, "已是最新版本").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			}

			if installing {
				return
			}
			if done && status.Available && status.Installable && canInstall {
				installBtn := ui.ButtonBase(c.Key("btn-about-install-update")).
					Label("btn-about-install-update").
					Radius(RadiusSM).
					Padding(settingsPillPadX, Space4).
					Background(ThemeOverlay(c, 0.06))
				{
					scale := motionPressScale(c, installBtn, "button-press-scale", installBtn.Pressed())
					installBtn.Scale(scale, scale)
				}
				if installBtn.Hovered() {
					installBtn.Background(ThemeHover(c))
				}
				if installBtn.Clicked() && !checking {
					startAppUpdateInstall(st, installer)
				}
				installBtn.Children(func() {
					ui.Text(c, "更新并重启").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
				})
			} else if done && status.Available && status.URL != "" {
				downloadBtn := ui.ButtonBase(c.Key("btn-about-download")).
					Radius(RadiusSM).
					Padding(settingsPillPadX, Space4).
					Background(ThemeOverlay(c, 0.06))
				{
					scale := motionPressScale(c, downloadBtn, "button-press-scale", downloadBtn.Pressed())
					downloadBtn.Scale(scale, scale)
				}
				if downloadBtn.Hovered() {
					downloadBtn.Background(ThemeHover(c))
				}
				if downloadBtn.Clicked() {
					openExternalURL(status.URL)
				}
				downloadBtn.Children(func() {
					ui.Text(c, "前往下载页").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
				})
			}

			checkBtn := ui.ButtonBase(c.Key("btn-about-check-update")).
				Label("btn-about-check-update").
				Disabled(checking).
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			{
				scale := motionPressScale(c, checkBtn, "button-press-scale", !checking && checkBtn.Pressed())
				checkBtn.Scale(scale, scale)
			}
			if checkBtn.Hovered() {
				checkBtn.Background(ThemeHover(c))
			}
			if checkBtn.Clicked() && !checking {
				startUpdateCheck(st)
			}
			checkBtn.Children(func() {
				ui.Text(c, "检查更新").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			})
		})
}

// renderAboutDiagnosticsControls 渲染「导出诊断日志」行的状态文本与按钮，只读快照。
func renderAboutDiagnosticsControls(c *ui.Context, st *SettingsState) {
	globalAboutState.mu.Lock()
	exporting := globalAboutState.exporting
	diagMsg := globalAboutState.diagMsg
	diagErr := globalAboutState.diagErr
	globalAboutState.mu.Unlock()

	ui.Row(c.Key("row-about-diag-controls")).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			switch {
			case exporting:
				ui.Text(c, "导出中…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			case diagErr != "":
				ui.Text(c, diagErr).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
			case diagMsg != "":
				ui.Text(c, diagMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			}

			exportBtn := ui.ButtonBase(c.Key("btn-about-export-diag")).
				Label("btn-about-export-diag").
				Disabled(exporting).
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			{
				scale := motionPressScale(c, exportBtn, "button-press-scale", !exporting && exportBtn.Pressed())
				exportBtn.Scale(scale, scale)
			}
			if exportBtn.Hovered() {
				exportBtn.Background(ThemeHover(c))
			}
			if exportBtn.Clicked() && !exporting {
				startDiagnosticsExport(st, aboutDiagnosticsClient(st))
			}
			exportBtn.Children(func() {
				ui.Text(c, "导出…").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			})
		})
}

// diagnosticsExportFileName 生成诊断日志的默认文件名：mcode-diagnostics-<yyyyMMdd-HHmmss>.log。
func diagnosticsExportFileName(now time.Time) string {
	return "mcode-diagnostics-" + now.Format("20060102-150405") + ".log"
}

// diagnosticsSaveDialog 是可注入的保存对话框，测试时替换以避免弹出真实窗口。
var diagnosticsSaveDialog = defaultDiagnosticsSaveDialog

// defaultDiagnosticsSaveDialog 唤起系统原生保存对话框，返回用户选择的路径；取消时路径为空。
func defaultDiagnosticsSaveDialog(defaultPath string) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       "导出诊断日志",
		DefaultPath: defaultPath,
		ButtonLabel: "导出",
		Filters: []mygo.FileFilter{
			{Name: "日志文件", Extensions: []string{"log"}},
		},
		CreateDirectories: true,
	})
}

// startDiagnosticsExport 弹保存对话框并后台导出诊断日志，结果写入关于页状态。
func startDiagnosticsExport(st *SettingsState, client appDiagnosticsClient) {
	if client == nil {
		return
	}

	globalAboutState.mu.Lock()
	if globalAboutState.exporting {
		globalAboutState.mu.Unlock()
		return
	}
	globalAboutState.exporting = true
	globalAboutState.diagMsg = ""
	globalAboutState.diagErr = ""
	globalAboutState.mu.Unlock()

	if st != nil {
		st.invalidate()
	}

	go runDiagnosticsExport(st, client)
}

// runDiagnosticsExport 同步执行一次诊断日志导出并写入页面状态；拆出便于单元测试直接驱动。
func runDiagnosticsExport(st *SettingsState, client appDiagnosticsClient) {
	defaultPath := filepath.Join(downloadsDir(), diagnosticsExportFileName(time.Now()))
	path, err := diagnosticsSaveDialog(defaultPath)
	if err == nil && path != "" {
		err = client.ExportDiagnosticsTo(path)
	}

	globalAboutState.mu.Lock()
	globalAboutState.exporting = false
	if err != nil {
		globalAboutState.diagErr = err.Error()
		globalAboutState.diagMsg = ""
	} else if path != "" {
		globalAboutState.diagMsg = "已导出到 " + path
		globalAboutState.diagErr = ""
	}
	globalAboutState.mu.Unlock()

	if st != nil {
		st.invalidate()
	}
}
