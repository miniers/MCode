package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// AppUpdateStatus 描述一次更新检查的结果。
type AppUpdateStatus struct {
	Current   string
	Latest    string
	URL       string
	Available bool
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
	dot := renderUnreadDot(c, key)
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

	checking bool
	done     bool
	status   AppUpdateStatus
	errMsg   string

	// 诊断日志导出状态。
	exporting bool
	diagMsg   string
	diagErr   string
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
	globalAboutState.exporting = false
	globalAboutState.diagMsg = ""
	globalAboutState.diagErr = ""
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
	if client, ok := archiveClient(st).(appUpdateLatestClient); ok {
		status := client.LatestUpdate()
		if status.Available {
			globalAboutState.mu.Lock()
			if !globalAboutState.checking && !globalAboutState.done {
				globalAboutState.status = status
				globalAboutState.done = true
			}
			globalAboutState.mu.Unlock()
		}
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

// renderPageAbout 保留旧路由入口。
func renderPageAbout(c *ui.Context, st *SettingsState) { renderPageGeneral(c, st) }

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
	ui.Text(c, aboutVersion(st)).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
}

func renderAboutDataDirValue(c *ui.Context, st *SettingsState) {
	ui.Text(c, st.dataDir).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
}

// renderAboutUpdateControls 渲染「检查更新」行的状态文本与按钮，只读快照。
func renderAboutUpdateControls(c *ui.Context, st *SettingsState) {
	globalAboutState.mu.Lock()
	checking := globalAboutState.checking
	done := globalAboutState.done
	status := globalAboutState.status
	errMsg := globalAboutState.errMsg
	globalAboutState.mu.Unlock()

	ui.Row(c.Key("row-about-update-controls")).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			switch {
			case checking:
				ui.Text(c, "检查中…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			case errMsg != "":
				ui.Text(c, errMsg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			case done && status.Available:
				ui.Text(c, fmt.Sprintf("发现新版本 v%s", status.Latest)).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
			case done:
				ui.Text(c, "已是最新版本").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			}

			if done && status.Available && status.URL != "" {
				downloadBtn := ui.ButtonBase(c.Key("btn-about-download")).
					Radius(RadiusSM).
					Padding(settingsPillPadX, Space4).
					Background(ThemeOverlay(c, 0.06))
				if downloadBtn.Hovered() {
					downloadBtn.Background(ThemeHover(c))
				}
				if downloadBtn.Clicked() {
					openExternalURL(status.URL)
				}
				downloadBtn.Children(func() {
					ui.Text(c, "前往下载页").FontSize(TypeBody).TextColor(ThemePrimaryText(c))
				})
			}

			checkBtn := ui.ButtonBase(c.Key("btn-about-check-update")).
				Label("btn-about-check-update").
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			if checkBtn.Hovered() {
				checkBtn.Background(ThemeHover(c))
			}
			if checkBtn.Clicked() && !checking {
				startUpdateCheck(st)
			}
			checkBtn.Children(func() {
				ui.Text(c, "检查更新").FontSize(TypeBody).TextColor(ThemePrimaryText(c))
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
				ui.Text(c, "导出中…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			case diagErr != "":
				ui.Text(c, diagErr).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			case diagMsg != "":
				ui.Text(c, diagMsg).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			}

			exportBtn := ui.ButtonBase(c.Key("btn-about-export-diag")).
				Label("btn-about-export-diag").
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			if exportBtn.Hovered() {
				exportBtn.Background(ThemeHover(c))
			}
			if exportBtn.Clicked() && !exporting {
				startDiagnosticsExport(st, aboutDiagnosticsClient(st))
			}
			exportBtn.Children(func() {
				ui.Text(c, "导出…").FontSize(TypeBody).TextColor(ThemePrimaryText(c))
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
