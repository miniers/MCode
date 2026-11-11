package mcodeui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

var (
	terminalMu     sync.Mutex
	terminalStates = make(map[*chatState]*terminalState)
	newTerminal    = terminal.New
	// closeTerminal 供测试替换，避免关闭零值终端。
	closeTerminal = func(t *terminal.Terminal) error { return t.Close() }
	// terminalDone 供测试替换，获取终端退出通知 channel。
	terminalDone = func(t *terminal.Terminal) <-chan struct{} { return t.Done() }
	sendTerminal = func(t *terminal.Terminal, data []byte) { t.Send(data) }
)

// 终端 dock 高度相关常量：默认高度、最小高度、拖拽条高度，以及 dock 之上聊天区至少保留的高度。
const (
	TerminalDockDefaultHeight float32 = 260
	TerminalDockMinHeight     float32 = 120
	// terminalDockResizerHeight 是 dock 上边缘拖拽条的高度（pt）。
	terminalDockResizerHeight float32 = 5
	// terminalHeaderHeight 是 dock 顶栏高度：比 ControlLG 高 4pt，容纳 14pt 图标与标签页。
	terminalHeaderHeight float32 = 36
	// terminalDockChatMinHeight 是 dock 之上聊天区至少保留的高度，用于计算高度上限。
	terminalDockChatMinHeight float32 = 200
)

// clampTerminalDockHeight 把终端 dock 高度夹取到合法范围：下限 TerminalDockMinHeight，
// 上限为窗口高度减去聊天区保留高度，且不低于下限。winH 未知（<=0）时只做下限钳制。
func clampTerminalDockHeight(h, winH float32) float32 {
	if h < TerminalDockMinHeight {
		h = TerminalDockMinHeight
	}
	if winH <= 0 {
		return h
	}
	maxHeight := winH - terminalDockChatMinHeight
	if maxHeight < TerminalDockMinHeight {
		maxHeight = TerminalDockMinHeight
	}
	if h > maxHeight {
		h = maxHeight
	}
	return h
}

// terminalDockPrefs 保存每个窗口的 dock 高度偏好，独立于 terminalState 存放：
// 关闭终端会删掉 terminalState，偏好仍要保留，供下次打开终端与写盘时使用。
var terminalDockPrefs = make(map[*chatState]float32)

// setTerminalDockHeightPreference 记录 dock 高度偏好（来自 ui-state.json）；
// h<=0 表示没有偏好，打开终端时用 TerminalDockDefaultHeight。
func setTerminalDockHeightPreference(s *chatState, h float32) {
	if s == nil {
		return
	}
	terminalMu.Lock()
	terminalDockPrefs[s] = h
	if st, ok := terminalStates[s]; ok && h > 0 {
		st.height = h
	}
	terminalMu.Unlock()
}

// terminalDockHeightForPersist 返回写盘用的 dock 高度：优先当前终端状态的高度，
// 没有终端状态时回落到上次加载的偏好值；都没有返回 0（表示按默认高度）。
func terminalDockHeightForPersist(s *chatState) float32 {
	if s == nil {
		return 0
	}
	terminalMu.Lock()
	defer terminalMu.Unlock()
	if st, ok := terminalStates[s]; ok && st.height > 0 {
		return st.height
	}
	return terminalDockPrefs[s]
}

// releaseTerminalDockPreference 在窗口关闭时清掉 dock 高度偏好，避免按 chatState 累积。
func releaseTerminalDockPreference(s *chatState) {
	if s == nil {
		return
	}
	terminalMu.Lock()
	delete(terminalDockPrefs, s)
	terminalMu.Unlock()
}

// terminalTab 表示单个终端标签页实例。
type terminalTab struct {
	id   int
	term *terminal.Terminal
	dir  string
	main bool
	err  string
	// fontSize 记录已经应用到该终端上的字号，用于发现设置里的字号变化。
	fontSize int
}

// terminalSearchState 是终端搜索条的状态。它是纯数据值类型，变换都用值方法返回新值，
// 方便单测直接覆盖打开、改词、收状态、关窗等切换。
type terminalSearchState struct {
	open      bool
	tabID     int
	text      string
	total     int
	selected  int
	searching bool
}

// opened 打开搜索条：绑定到 tabID 标签，清空上一次的搜索词与匹配计数。
func (ss terminalSearchState) opened(tabID int) terminalSearchState {
	return terminalSearchState{open: true, tabID: tabID}
}

// closed 关闭搜索条，同时清空匹配计数与绑定标签。
func (ss terminalSearchState) closed() terminalSearchState {
	return terminalSearchState{text: ss.text}
}

// withText 设置搜索词；词变了就重置计数等待新的搜索状态，清空则回到未搜索状态。
func (ss terminalSearchState) withText(text string) terminalSearchState {
	if ss.text == text {
		return ss
	}
	ss.text = text
	ss.total, ss.selected, ss.searching = 0, 0, text != ""
	return ss
}

// withStatus 写入 mygo 回报的搜索状态；与当前搜索词不一致的旧状态（用户已经改了词）忽略。
func (ss terminalSearchState) withStatus(text string, total, selected int, searching bool) terminalSearchState {
	if text != ss.text {
		return ss
	}
	ss.total, ss.selected, ss.searching = total, selected, searching
	return ss
}

// matchLabel 返回匹配计数文案：无词时为空，搜索中提示进度，无匹配时明说，否则显示「第几个/共几个」。
func (ss terminalSearchState) matchLabel() string {
	switch {
	case ss.text == "":
		return ""
	case ss.searching && ss.total == 0:
		return "搜索中…"
	case ss.total == 0:
		return "无匹配"
	case ss.selected <= 0:
		return fmt.Sprintf("%d 个匹配", ss.total)
	default:
		return fmt.Sprintf("%d/%d", ss.selected, ss.total)
	}
}

// terminalState 维护单个聊天会话视图下的内嵌终端状态。
type terminalState struct {
	visible    bool
	height     float32
	tabs       map[string][]*terminalTab
	active     map[string]int
	nextTabID  int
	newTabOpen bool
	errors     map[string]string
	closed     bool
	// search 是终端搜索条的状态，作用于 active 标签。
	search terminalSearchState
}

// getTerminalState 获取或初始化当前 chatState 的终端面板状态。
func getTerminalState(s *chatState) *terminalState {
	if s == nil {
		return nil
	}
	terminalMu.Lock()
	defer terminalMu.Unlock()
	st, ok := terminalStates[s]
	if !ok {
		// 新建状态沿用上次持久化的高度偏好，没有偏好时用默认高度。
		height := TerminalDockDefaultHeight
		if pref := terminalDockPrefs[s]; pref > 0 {
			height = pref
		}
		st = &terminalState{
			height:     height,
			tabs:       make(map[string][]*terminalTab),
			active:     make(map[string]int),
			errors:     make(map[string]string),
			nextTabID:  1,
			newTabOpen: false,
		}
		terminalStates[s] = st
	}
	return st
}

// getSelectedThreadDirs 获取当前选中会话的 threadID、工作目录与主项目目录。
// workDir 优先使用 s.client.ThreadWorkDir(threadID)，否则使用主项目 Path。
// mainDir 始终为 s.threads 里该会话所属 ProjectID 对应的 s.projects[].Path。
// 读写 s 字段时持有 s.mu。
func getSelectedThreadDirs(s *chatState) (threadID, workDir, mainDir string) {
	if s == nil {
		return "", "", ""
	}
	s.mu.Lock()
	threadID = s.selectedThreadID
	var projectID string
	if threadID != "" {
		for _, th := range s.threads {
			if th.ID == threadID {
				projectID = th.ProjectID
				break
			}
		}
	}
	if projectID != "" {
		for _, p := range s.projects {
			if p.ID == projectID {
				mainDir = p.Path
				break
			}
		}
	}
	s.mu.Unlock()

	if threadID != "" && s.client != nil {
		if dir, ok := s.client.ThreadWorkDir(threadID); ok {
			workDir = dir
		}
	}
	if workDir == "" {
		workDir = mainDir
	}
	return threadID, workDir, mainDir
}

// getSelectedThreadWorkDir 获取当前选中会话所属的 threadID 与工作目录。
func getSelectedThreadWorkDir(s *chatState) (threadID string, workDir string) {
	tid, wdir, _ := getSelectedThreadDirs(s)
	return tid, wdir
}

// getTerminalScrollbackLines 从 chatState 关联的设置状态读取当前配置的终端缓冲行数。
func getTerminalScrollbackLines(s *chatState) int {
	if s != nil {
		if st := getChatSettings(s); st != nil {
			st.mu.Lock()
			lines := st.cfg.Workbench.TerminalScrollbackLines
			st.mu.Unlock()
			if lines > 0 {
				return lines
			}
		}
	}
	return config.DefaultTerminalScrollback
}

// clampTerminalFontSize 把终端字号夹取到配置允许的范围，零值与负值取默认值。
func clampTerminalFontSize(size int) int {
	if size <= 0 {
		return config.DefaultTerminalFontSize
	}
	if size < config.MinTerminalFontSize {
		return config.MinTerminalFontSize
	}
	if size > config.MaxTerminalFontSize {
		return config.MaxTerminalFontSize
	}
	return size
}

// getTerminalFontSize 读取当前主题的终端字号。
func getTerminalFontSize(s *chatState) int { return terminalFontSize() }

// getTerminalConfiguredShell 从 chatState 关联的设置状态读取当前配置的终端 Shell。
func getTerminalConfiguredShell(s *chatState) string {
	if s != nil {
		if st := getChatSettings(s); st != nil {
			st.mu.Lock()
			sh := st.cfg.Workbench.TerminalShell
			st.mu.Unlock()
			return sh
		}
	}
	return ""
}

// defaultFallbackShell 返回系统默认 shell。
func defaultFallbackShell() string {
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	return "/bin/sh"
}

// shellPathExists 检查指定路径或可执行文件是否存在且不为目录。
func shellPathExists(path string) bool {
	if fi, err := os.Stat(path); err == nil {
		return !fi.IsDir()
	}
	if lp, err := exec.LookPath(path); err == nil {
		if fi, err := os.Stat(lp); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// resolveTerminalShell 根据配置与环境变量解析终端启动所使用的 shell。
// 若 configured 去掉首尾空白后非空且对应路径存在，则优先采用配置；
// 为空或路径不存在时退回默认 shell（$SHELL 或系统默认值），保证终端不启动失败。
func resolveTerminalShell(configured, envShell string) string {
	defShell := strings.TrimSpace(envShell)
	if defShell == "" {
		defShell = defaultFallbackShell()
	}

	conf := strings.TrimSpace(configured)
	if conf == "" {
		return defShell
	}

	// 配置值是外部终端应用名（Terminal、iTerm、WezTerm 等）时，内嵌终端仍用系统默认 shell，
	// 避免把终端应用本身当成 shell 启动。
	if _, ok := resolveTerminalApp(conf); ok {
		return defShell
	}

	if shellPathExists(conf) {
		return conf
	}
	return defShell
}

// terminalAppCandidate 描述一个可选的外部终端应用：Name 是配置里保存与下拉中显示的名字，Bundle 是 macOS 上的 .app 名称，
// WinExe 是 Windows 上的可执行文件名。
type terminalAppCandidate struct {
	Name   string
	Bundle string
	WinExe string
	// System 表示系统自带应用，始终列入选项。
	System bool
}

// terminalAppCandidates 按固定顺序列出 macOS 支持的外部终端应用。
var terminalAppCandidates = []terminalAppCandidate{
	{Name: "Terminal", Bundle: "Terminal.app", System: true},
	{Name: "iTerm", Bundle: "iTerm.app"},
	{Name: "Ghostty", Bundle: "Ghostty.app"},
	{Name: "Warp", Bundle: "Warp.app"},
	{Name: "WezTerm", Bundle: "WezTerm.app"},
	{Name: "Alacritty", Bundle: "Alacritty.app"},
	{Name: "kitty", Bundle: "kitty.app"},
}

// windowsTerminalAppCandidates 按固定顺序列出 Windows 支持的外部终端应用；
// Windows Terminal 需要能在 PATH 中找到，PowerShell 与命令提示符系统自带，始终可用。
var windowsTerminalAppCandidates = []terminalAppCandidate{
	{Name: "Windows Terminal", WinExe: "wt.exe"},
	{Name: "PowerShell", WinExe: "powershell.exe", System: true},
	{Name: "命令提示符", WinExe: "cmd.exe", System: true},
}

// terminalGOOS 是终端分支使用的平台标识，测试中可替换。
var terminalGOOS = runtime.GOOS

// terminalAppLookPath 在 PATH 中查找可执行文件，测试中可替换。
var terminalAppLookPath = exec.LookPath

// defaultTerminalAppName 是未配置或配置无法识别时用于外部打开目录的终端应用（macOS）。
const defaultTerminalAppName = "Terminal"

// terminalAppCandidatesForGOOS 返回当前平台支持的终端应用候选。
func terminalAppCandidatesForGOOS() []terminalAppCandidate {
	if terminalGOOS == "windows" {
		return windowsTerminalAppCandidates
	}
	return terminalAppCandidates
}

// terminalAppInstalled 检查应用路径是否为已安装的 .app，测试中可替换。
var terminalAppInstalled = func(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// terminalAppCandidatePaths 返回应用可能的安装位置：系统自带应用在 /System/Applications/Utilities，其余在 /Applications 与 ~/Applications。
func terminalAppCandidatePaths(app terminalAppCandidate) []string {
	if app.System {
		return []string{filepath.Join("/System/Applications/Utilities", app.Bundle)}
	}
	paths := []string{filepath.Join("/Applications", app.Bundle)}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "Applications", app.Bundle))
	}
	return paths
}

// detectInstalledTerminalApps 按固定顺序返回已安装的终端应用名；系统自带的应用始终列举。
func detectInstalledTerminalApps(installed func(string) bool) []string {
	if terminalGOOS == "windows" {
		return detectWindowsTerminalApps(terminalAppLookPath)
	}
	apps := make([]string, 0, len(terminalAppCandidates))
	for _, app := range terminalAppCandidates {
		if app.System {
			apps = append(apps, app.Name)
			continue
		}
		for _, path := range terminalAppCandidatePaths(app) {
			if installed != nil && installed(path) {
				apps = append(apps, app.Name)
				break
			}
		}
	}
	return apps
}

// detectWindowsTerminalApps 返回 Windows 上可用的终端应用：Windows Terminal 需在 PATH 中找到 wt.exe，
// PowerShell 与命令提示符系统自带，始终列出。
func detectWindowsTerminalApps(lookPath func(string) (string, error)) []string {
	apps := make([]string, 0, len(windowsTerminalAppCandidates))
	for _, app := range windowsTerminalAppCandidates {
		if app.System {
			apps = append(apps, app.Name)
			continue
		}
		if lookPath == nil {
			continue
		}
		if _, err := lookPath(app.WinExe); err == nil {
			apps = append(apps, app.Name)
		}
	}
	return apps
}

var (
	terminalAppsOnce  sync.Once
	terminalAppsCache []string
)

// installedTerminalApps 返回已安装的终端应用名，按固定顺序排列；只检测一次并缓存结果。
func installedTerminalApps() []string {
	terminalAppsOnce.Do(func() {
		terminalAppsCache = detectInstalledTerminalApps(terminalAppInstalled)
	})
	return append([]string(nil), terminalAppsCache...)
}

// terminalAppOptions 返回「默认终端」下拉的选项与选中项：已安装的终端应用按固定顺序排列，
// 当前配置值不在其中时（旧版本保存的 shell 路径等）也作为一项保留。
func terminalAppOptions(current string) (options []string, selected string) {
	options = installedTerminalApps()
	cur := strings.TrimSpace(current)
	if cur == "" {
		if len(options) > 0 {
			return options, options[0]
		}
		return options, ""
	}
	for _, opt := range options {
		if opt == cur {
			return options, cur
		}
	}
	return append(options, cur), cur
}

// resolveTerminalApp 把配置值解析为终端应用：接受应用名、macOS 的 .app 名称或路径、Windows 的可执行文件名，忽略大小写与首尾空白。
func resolveTerminalApp(configured string) (terminalAppCandidate, bool) {
	v := strings.TrimSpace(configured)
	if v == "" {
		return terminalAppCandidate{}, false
	}
	base := strings.TrimSuffix(filepath.Base(v), ".app")
	for _, app := range terminalAppCandidatesForGOOS() {
		if strings.EqualFold(v, app.Name) {
			return app, true
		}
		if app.Bundle != "" && strings.EqualFold(base, strings.TrimSuffix(app.Bundle, ".app")) {
			return app, true
		}
		if app.WinExe != "" && (strings.EqualFold(base, app.WinExe) || strings.EqualFold(base, strings.TrimSuffix(app.WinExe, ".exe"))) {
			return app, true
		}
	}
	return terminalAppCandidate{}, false
}

// externalTerminalRunner 执行打开外部终端的命令，测试中可替换。
var externalTerminalRunner = func(args ...string) error {
	cmd := exec.Command("open", args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// externalTerminalExecRunner 在 Windows 上直接启动外部终端命令，测试中可替换。
var externalTerminalExecRunner = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// windowsDefaultTerminalApp 返回 Windows 上的默认终端应用：装了 Windows Terminal 就用它，否则用 PowerShell。
func windowsDefaultTerminalApp() terminalAppCandidate {
	if _, err := terminalAppLookPath("wt.exe"); err == nil {
		return terminalAppCandidate{Name: "Windows Terminal", WinExe: "wt.exe"}
	}
	return terminalAppCandidate{Name: "PowerShell", WinExe: "powershell.exe"}
}

// windowsTerminalCommand 返回在 Windows 上用指定终端打开 dir 的命令名与参数。
// Windows Terminal 直接以 -d 指定目录；PowerShell 与命令提示符借 cmd /c start /D 切换工作目录。
func windowsTerminalCommand(app terminalAppCandidate, dir string) (string, []string) {
	switch app.Name {
	case "Windows Terminal":
		return "wt.exe", []string{"-d", dir}
	case "命令提示符":
		return "cmd", []string{"/c", "start", "", "/D", dir, "cmd.exe"}
	default:
		return "cmd", []string{"/c", "start", "", "/D", dir, "powershell.exe", "-NoExit"}
	}
}

// openDirInExternalTerminal 用配置的终端应用在 dir 打开外部终端窗口。
// macOS 上用 open -a <App名> <目录> 打开；Windows 上直接启动对应终端命令。
// 未配置或配置值不是已知终端应用时，分别退回 Terminal 与 Windows Terminal（未安装则 PowerShell）。
func openDirInExternalTerminal(dir, configured string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("工作目录为空")
	}
	if terminalGOOS == "windows" {
		app, ok := resolveTerminalApp(configured)
		if !ok {
			app = windowsDefaultTerminalApp()
		}
		name, args := windowsTerminalCommand(app, dir)
		return externalTerminalExecRunner(name, args...)
	}
	app := defaultTerminalAppName
	if resolved, ok := resolveTerminalApp(configured); ok {
		app = resolved.Name
	}
	return externalTerminalRunner("-a", app, dir)
}

// startTerminalProcess 根据指定目录启动底层终端实例，缓冲行数、字体字号与终端 Shell 读取当前配置。
func startTerminalProcess(s *chatState, dir string) (*terminal.Terminal, error) {
	opts := terminal.Options{
		Dir:        dir,
		Scrollback: getTerminalScrollbackLines(s),
		// 字体族里带上 Nerd Font 回退，否则 oh-my-zsh 等主题的图标会显示成方块。
		Font: terminal.Font{Family: terminalFontFamily(), Size: float32(getTerminalFontSize(s))},
		// 搜索状态回报给搜索条的匹配计数；按键里处理 ⌘F 打开搜索条。
		OnSearch: func(status terminal.SearchStatus) { handleTerminalSearchStatus(s, status) },
		OnKey:    func(mods ui.Modifiers, key ui.Key) bool { return handleTerminalSearchKey(s, mods, key) },
		// Command+click 打开链接：URL 交回 mygo 用系统浏览器打开，文件路径在 handleTerminalOpenLink 里处理。
		OpenLink: func(link string) bool { return handleTerminalOpenLink(s, dir, link) },
	}
	if shellCmd := resolveTerminalShell(getTerminalConfiguredShell(s), os.Getenv("SHELL")); shellCmd != "" {
		opts.Command = []string{shellCmd}
	}
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" {
		opts.Env = append(opts.Env, "LANG=en_US.UTF-8")
	}
	return newTerminal(opts)
}

// attachTerminalListener 监听终端退出事件并在退出后标记标签状态。
func attachTerminalListener(s *chatState, threadID string, tab *terminalTab, term *terminal.Terminal) <-chan struct{} {
	if term == nil {
		return nil
	}
	doneCh := terminalDone(term)
	if doneCh == nil {
		return nil
	}
	finished := make(chan struct{})
	closeTerm := closeTerminal
	go func(tid string, t *terminal.Terminal, targetTab *terminalTab) {
		defer close(finished)
		<-doneCh
		// 退出前先落盘：此时终端的回滚文本仍可读，清空 term 之后就只剩目录信息了。
		persistTerminalState(s, tid)
		terminalMu.Lock()
		owned := false
		if curSt, ok := terminalStates[s]; ok {
			for _, tb := range curSt.tabs[tid] {
				if tb == targetTab && tb.term == t {
					tb.term = nil
					tb.err = "终端已退出"
					owned = true
					break
				}
			}
		}
		terminalMu.Unlock()
		if owned {
			_ = closeTerm(t)
			s.invalidate()
		}
	}(threadID, term, tab)
	return finished
}

// createTerminalTab 为会话新建一个终端标签页。
func createTerminalTab(s *chatState, st *terminalState, dir string, main bool) (*terminalTab, error) {
	if dir == "" {
		return nil, errors.New("工作目录为空")
	}
	threadID, _ := getSelectedThreadWorkDir(s)
	if threadID == "" {
		return nil, errors.New("没有选中会话")
	}
	terminalMu.Lock()
	if st.closed {
		terminalMu.Unlock()
		return nil, errors.New("窗口已关闭")
	}
	terminalMu.Unlock()

	term, err := startTerminalProcess(s, dir)
	if err != nil {
		terminalMu.Lock()
		st.errors[threadID] = err.Error()
		terminalMu.Unlock()
		return nil, err
	}

	// 字号在拿 terminalMu 之前读，避免与设置侧的加锁顺序交叉。
	fontSize := getTerminalFontSize(s)

	terminalMu.Lock()
	if st.closed {
		terminalMu.Unlock()
		if term != nil {
			_ = closeTerminal(term)
		}
		return nil, errors.New("窗口已关闭")
	}

	tabID := st.nextTabID
	st.nextTabID++
	tab := &terminalTab{
		id:   tabID,
		term: term,
		dir:  dir,
		main: main,
		// 记录已应用的字号，避免首帧再把同样的字号写一遍。
		fontSize: fontSize,
	}
	st.tabs[threadID] = append(st.tabs[threadID], tab)
	st.active[threadID] = tabID
	delete(st.errors, threadID)
	terminalMu.Unlock()

	attachTerminalListener(s, threadID, tab, term)
	if !terminalRestoring {
		persistTerminalState(s, threadID)
	}
	return tab, nil
}

// activeTerminalTab 返回 active id 对应的标签，找不到时返回 nil。调用方需持有 terminalMu。
func activeTerminalTab(st *terminalState, threadID string) *terminalTab {
	if st == nil || threadID == "" {
		return nil
	}
	activeID, ok := st.active[threadID]
	if !ok {
		return nil
	}
	for _, tab := range st.tabs[threadID] {
		if tab.id == activeID {
			return tab
		}
	}
	return nil
}

// switchTerminalTab 切换当前会话的活动标签页。
func switchTerminalTab(s *chatState, st *terminalState, threadID string, id int) {
	if s == nil || st == nil || threadID == "" {
		return
	}
	terminalMu.Lock()
	found := false
	for _, tab := range st.tabs[threadID] {
		if tab.id == id {
			found = true
			break
		}
	}
	if found {
		st.active[threadID] = id
	}
	terminalMu.Unlock()
	if found {
		persistTerminalState(s, threadID)
		s.invalidate()
	}
}

// closeTerminalTab 关闭指定终端标签页并清理底层终端进程。
func closeTerminalTab(s *chatState, st *terminalState, threadID string, id int) {
	if s == nil || st == nil || threadID == "" {
		return
	}
	terminalMu.Lock()
	tabs := st.tabs[threadID]
	idx := -1
	for i, tab := range tabs {
		if tab.id == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		terminalMu.Unlock()
		return
	}

	targetTab := tabs[idx]
	termToClose := targetTab.term
	wasActive := st.active[threadID] == id

	newTabs := make([]*terminalTab, 0, len(tabs)-1)
	newTabs = append(newTabs, tabs[:idx]...)
	newTabs = append(newTabs, tabs[idx+1:]...)
	st.tabs[threadID] = newTabs

	if len(newTabs) == 0 {
		delete(st.active, threadID)
		delete(st.errors, threadID)
		st.visible = false
		st.newTabOpen = false
	} else if wasActive {
		if idx < len(newTabs) {
			st.active[threadID] = newTabs[idx].id
		} else {
			st.active[threadID] = newTabs[len(newTabs)-1].id
		}
	}
	terminalMu.Unlock()

	if termToClose != nil {
		_ = closeTerminal(termToClose)
	}
	persistTerminalState(s, threadID)
	s.invalidate()
}

// restartTerminalTab 对已退出的标签重新启动终端。
func restartTerminalTab(s *chatState, st *terminalState, threadID string, id int) (*terminal.Terminal, error) {
	if s == nil || st == nil || threadID == "" {
		return nil, errors.New("没有选中会话")
	}
	terminalMu.Lock()
	if st.closed {
		terminalMu.Unlock()
		return nil, errors.New("窗口已关闭")
	}
	var targetTab *terminalTab
	for _, tb := range st.tabs[threadID] {
		if tb.id == id {
			targetTab = tb
			break
		}
	}
	if targetTab == nil {
		terminalMu.Unlock()
		return nil, errors.New("标签已关闭")
	}
	if targetTab.term != nil {
		term := targetTab.term
		terminalMu.Unlock()
		return term, nil
	}
	dir := targetTab.dir
	terminalMu.Unlock()

	term, err := startTerminalProcess(s, dir)
	if err != nil {
		terminalMu.Lock()
		if targetTab.term == nil {
			targetTab.err = err.Error()
		}
		terminalMu.Unlock()
		s.invalidate()
		return nil, err
	}

	terminalMu.Lock()
	found := false
	for _, tab := range st.tabs[threadID] {
		if tab == targetTab {
			found = true
			break
		}
	}
	if st.closed || !found || targetTab.term != nil {
		existing := targetTab.term
		closed := st.closed
		terminalMu.Unlock()
		if term != nil {
			_ = closeTerminal(term)
		}
		if existing != nil && !closed && found {
			return existing, nil
		}
		return nil, errors.New("标签已关闭")
	}
	targetTab.term = term
	targetTab.err = ""
	terminalMu.Unlock()

	attachTerminalListener(s, threadID, targetTab, term)
	s.invalidate()
	return term, nil
}

// getOrCreateTerminal 为当前会话获取或创建终端实例。
// 已退出的活动标签在原目录重启；没有标签时才创建。
func getOrCreateTerminal(s *chatState, st *terminalState, workDir string) (*terminal.Terminal, error) {
	threadID, _ := getSelectedThreadWorkDir(s)
	if threadID == "" {
		return nil, errors.New("没有选中会话")
	}
	terminalMu.Lock()
	closed := st.closed
	actTab := activeTerminalTab(st, threadID)
	if !closed && actTab != nil && actTab.term != nil {
		term := actTab.term
		terminalMu.Unlock()
		return term, nil
	}
	terminalMu.Unlock()
	if closed {
		return nil, errors.New("窗口已关闭")
	}
	if actTab != nil {
		return restartTerminalTab(s, st, threadID, actTab.id)
	}

	// 没有标签：有持久化记录时按记录重建（含历史输出），否则新建一个默认标签。
	restored := restoreTerminalTabs(s, st, threadID, workDir)
	if len(restored) == 0 {
		return nil, errors.New("创建终端失败")
	}
	terminalMu.Lock()
	active := activeTerminalTab(st, threadID)
	var term *terminal.Terminal
	if active != nil {
		term = active.term
	}
	terminalMu.Unlock()
	if term == nil {
		return nil, errors.New("创建终端失败")
	}
	return term, nil
}

// toggleTerminal 切换显示状态，然后 s.invalidate()。
func toggleTerminal(s *chatState) {
	if s == nil {
		return
	}
	st := getTerminalState(s)
	terminalMu.Lock()
	st.visible = !st.visible
	terminalMu.Unlock()
	s.invalidate()
}

// runTerminalSearch 把搜索词交给 mygo 终端搜索：高亮匹配并滚动到最新的一个；空词表示结束搜索。
func runTerminalSearch(term *terminal.Terminal, text string) {
	if term == nil {
		return
	}
	if text == "" {
		term.EndSearch()
		return
	}
	term.Search(text)
}

// terminalSearchStep 切到下一个（back=false）或上一个（back=true）匹配。
// mygo 的 SearchNext 朝更早的输出走，SearchPrevious 相反。
func terminalSearchStep(term *terminal.Terminal, back bool) {
	if term == nil {
		return
	}
	if back {
		term.SearchPrevious()
		return
	}
	term.SearchNext()
}

// handleTerminalSearchStatus 记录 mygo 回报的搜索状态（匹配总数、当前第几个），供搜索条显示。
// 它在渲染终端时由 mygo 在主线程回调，因此这里只改状态并请求重绘。
func handleTerminalSearchStatus(s *chatState, status terminal.SearchStatus) {
	if s == nil {
		return
	}
	terminalMu.Lock()
	st, ok := terminalStates[s]
	if !ok {
		terminalMu.Unlock()
		return
	}
	next := st.search.withStatus(status.Text, status.Total, status.Selected, status.Searching)
	changed := next != st.search
	st.search = next
	terminalMu.Unlock()
	if changed {
		s.invalidate()
	}
}

// handleTerminalSearchKey 在终端聚焦时处理搜索快捷键：打开搜索条与关闭搜索条。
// OnKey 只能在终端视图拿到键盘焦点时被调用，所以这里是终端范围内的局部按键处理。
func handleTerminalSearchKey(s *chatState, mods ui.Modifiers, key ui.Key) bool {
	if s == nil {
		return false
	}
	// macOS 用 ⌘F；其他平台用 Ctrl+Shift+F，把 Ctrl+F（shell 的前移一个字符）留给终端。
	openKey := mods == ui.Cmd && key == ui.KeyF
	if runtime.GOOS != "darwin" {
		openKey = mods == ui.Ctrl|ui.Shift && key == ui.KeyF
	}
	if !openKey && !(key == ui.KeyEscape && mods == 0) {
		return false
	}
	// 取 threadID 要先拿 s.mu，不能在持有 terminalMu 时取，避免与别处的加锁顺序相反。
	threadID, _ := getSelectedThreadWorkDir(s)
	terminalMu.Lock()
	st, ok := terminalStates[s]
	if !ok {
		terminalMu.Unlock()
		return false
	}
	if openKey {
		tab := activeTerminalTab(st, threadID)
		if tab == nil || tab.term == nil {
			terminalMu.Unlock()
			return false
		}
		st.search = st.search.opened(tab.id)
		term := tab.term
		terminalMu.Unlock()
		// 重新打开时清掉上一次的高亮，搜索词从空开始。
		term.EndSearch()
		s.invalidate()
		return true
	}
	if !st.search.open {
		terminalMu.Unlock()
		return false
	}
	tabID := st.search.tabID
	st.search = st.search.closed()
	var term *terminal.Terminal
	for _, tab := range st.tabs[threadID] {
		if tab.id == tabID && tab.term != nil {
			term = tab.term
		}
	}
	terminalMu.Unlock()
	if term != nil {
		term.EndSearch()
	}
	s.invalidate()
	return true
}

// closeTerminalSearch 关闭搜索条，并清掉对应终端上的高亮。
func closeTerminalSearch(s *chatState, st *terminalState) {
	if st == nil {
		return
	}
	terminalMu.Lock()
	tabID := st.search.tabID
	st.search = st.search.closed()
	var term *terminal.Terminal
	for _, tabs := range st.tabs {
		for _, tab := range tabs {
			if tab.id == tabID && tab.term != nil {
				term = tab.term
			}
		}
	}
	terminalMu.Unlock()
	if term != nil {
		term.EndSearch()
	}
	if s != nil {
		s.invalidate()
	}
}

// syncTerminalFontSize 把配置里的终端字号应用到 threadID 下已打开的终端：mygo 的 SetFont
// 支持运行时改字号，所以设置改完不必重开标签。只在记录的已应用字号不同时才调用，
// 避免每帧覆盖用户用滚轮临时缩放的字号。
func syncTerminalFontSize(st *terminalState, threadID string, size int) {
	if st == nil || threadID == "" {
		return
	}
	type pendingFont struct {
		term *terminal.Terminal
		size int
	}
	var pending []pendingFont
	terminalMu.Lock()
	for _, tab := range st.tabs[threadID] {
		if tab.term != nil && tab.fontSize != size {
			tab.fontSize = size
			pending = append(pending, pendingFont{term: tab.term, size: size})
		}
	}
	terminalMu.Unlock()
	for _, p := range pending {
		p.term.SetFont(terminal.Font{Family: terminalFontFamily(), Size: float32(p.size)})
	}
}

// renderTerminalSearchBar 渲染终端搜索条：输入框、匹配计数、上一个/下一个、大小写开关与关闭按钮。
// 样式跟 dock 顶栏保持一致：底部一条分隔线、12 号文本，颜色全部取自主题。
func renderTerminalSearchBar(c *ui.Context, s *chatState, st *terminalState, tab *terminalTab) {
	terminalMu.Lock()
	state := st.search
	terminalMu.Unlock()

	var term *terminal.Terminal
	if tab != nil {
		term = tab.term
	}
	text := state.text

	ui.Row(c.Key("terminal-search-bar")).
		FillWidth().
		Height(ControlLG).
		Padding(0, Space4).
		Gap(Space3).
		AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Icon(c, IconSearch()).
				Size(12, 12).
				TextColor(ThemeSecondaryText(c))

			in := ui.TextInput(c.Key("terminal-search-input"), &text).
				Label("搜索终端输出").
				Placeholder("搜索终端输出").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c)).
				BorderWidth(0).
				Background(ui.Transparent).
				Padding(0).
				Width(220).
				AutoFocus()

			if in.Changed() && text != state.text {
				terminalMu.Lock()
				st.search = st.search.withText(text)
				current := st.search.text
				terminalMu.Unlock()
				runTerminalSearch(term, current)
				s.invalidate()
			}

			in.OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter {
					return false
				}
				// Shift+Enter 与「上一个」一致，直接回车即「下一个」。
				terminalSearchStep(term, k.Mods&ui.Shift != 0)
				s.invalidate()
				return true
			})

			// Esc 登记为输入框自己的快捷键：根窗口的 Esc 会先于 OnTextKey 收到按键。
			if in.Shortcut(0, ui.KeyEscape) {
				closeTerminalSearch(s, st)
			}

			if label := state.matchLabel(); label != "" {
				ui.Text(c, label).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					Width(72)
			}

			Button(c, ButtonOpts{
				Key:   "terminal-search-prev",
				Text:  "上一个",
				Label: "上一个匹配",
				OnClick: func() {
					terminalSearchStep(term, true)
					s.invalidate()
				},
				Variant: ButtonSecondary,
			})
			Button(c, ButtonOpts{
				Key:   "terminal-search-next",
				Text:  "下一个",
				Label: "下一个匹配",
				OnClick: func() {
					terminalSearchStep(term, false)
					s.invalidate()
				},
				Variant: ButtonSecondary,
			})

			// 不提供「区分大小写」开关：mygo 终端搜索固定忽略 ASCII 大小写，上游支持后再加。

			ui.Spacer(c)

			IconButton(c, IconButtonProps{
				Key:   "terminal-search-close",
				Icon:  IconX(),
				Label: "关闭搜索",
				OnClick: func() {
					closeTerminalSearch(s, st)
				},
				Variant: ButtonGhost,
			})
		})
}

// renderTerminalDockResizer 渲染 dock 上边缘的拖拽条：它位于 dock 列最顶部、横跨整宽。
// Dragged 只报每帧增量，向上拖 dy 为负而 dock 应变高，所以每帧 height -= dy；
// 写入后立即钳制并触发重绘。拖拽结束时写盘，重启后沿用同一高度。
func renderTerminalDockResizer(c *ui.Context, s *chatState, st *terminalState, winHeight float32) {
	resizer := ui.Box(c.Key("terminal-dock-resizer")).
		FillWidth().
		Height(terminalDockResizerHeight).
		Shrink(0).
		Cursor(ui.CursorResizeNS).
		Label("拖动调整终端高度").
		Tooltip("拖动调整终端高度")

	_, dy, held := resizer.Dragged()
	if held && dy != 0 {
		terminalMu.Lock()
		next := clampTerminalDockHeight(st.height-dy, winHeight)
		changed := next != st.height
		st.height = next
		terminalMu.Unlock()
		if changed {
			s.invalidate()
		}
	}

	// 悬停或按住时用一条细线提示可拖拽，颜色取主题强调色，不硬编码。
	resizer.Draw(func(p *ui.Painter, r ui.Rect) {
		if !held && !resizer.Hovered() {
			return
		}
		p.Fill(ui.Rect{X: r.X, Y: r.Y + (r.H-1)/2, W: r.W, H: 1}, ThemeAccent(c), 0)
	})

	// 拖拽结束（按住后松开且确实移动过）时持久化高度。
	if _, _, done := resizer.DragEnded(); done {
		persistUIState(s)
	}
}

// terminalDockOccupiedHeight 是终端 dock 当前在窗口底部实际占用的高度，供浮窗等窗口级布局避让。
// 拖拽条和顶栏都是 dock 的子元素，dock 的 Height 按 border-box 计算（边框含在内），
// 因此占用高度就是高度本身，不用额外加上边框或拖拽条。只读：钳制后的写回仍由 renderTerminalDock 负责。
func terminalDockOccupiedHeight(s *chatState, winHeight float32) float32 {
	if s == nil {
		return 0
	}
	// 直接读状态表而不调用 getTerminalState：布局计算不该顺手创建一个终端状态。
	terminalMu.Lock()
	st, ok := terminalStates[s]
	if !ok {
		terminalMu.Unlock()
		return 0
	}
	visible, height := st.visible, st.height
	terminalMu.Unlock()
	if !visible {
		return 0
	}
	return clampTerminalDockHeight(height, winHeight)
}

// renderTerminalDock 未显示时什么都不渲染。显示时渲染一个高度为 height 的面板，背景取 ThemeWindowBg、上边框取 ThemeBorder。
func renderTerminalDock(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getTerminalState(s)
	if st == nil {
		return
	}

	terminalMu.Lock()
	visible := st.visible
	height := st.height
	terminalMu.Unlock()

	if !visible {
		return
	}

	// 窗口可能在拖拽后被缩小，渲染前先钳制高度，保证聊天区至少保留一段空间。
	_, winHeight := c.Size()
	if clamped := clampTerminalDockHeight(height, winHeight); clamped != height {
		height = clamped
		terminalMu.Lock()
		st.height = clamped
		terminalMu.Unlock()
	}

	threadID, workDir, mainDir := getSelectedThreadDirs(s)

	if workDir != "" && threadID != "" {
		terminalMu.Lock()
		hasTabs := len(st.tabs[threadID]) > 0
		errText := st.errors[threadID]
		terminalMu.Unlock()

		if !hasTabs && errText == "" {
			// 有持久化记录时按记录重建标签（含上次的回滚输出），否则新建一个默认标签。
			_ = restoreTerminalTabs(s, st, threadID, workDir)
		}
	}

	terminalMu.Lock()
	// 退出监听会修改标签字段，渲染必须在锁内取得值快照。
	tabs := make([]terminalTab, len(st.tabs[threadID]))
	var actTab *terminalTab
	for i, tab := range st.tabs[threadID] {
		tabs[i] = *tab
		if tab.id == st.active[threadID] {
			actTab = &tabs[i]
		}
	}
	currentErrText := st.errors[threadID]
	newTabOpen := st.newTabOpen
	searchState := st.search
	terminalMu.Unlock()

	// 设置里的字号变化同步到已打开的终端；只在字号真的变了时才调用 SetFont。
	syncTerminalFontSize(st, threadID, getTerminalFontSize(s))

	ui.Column(c.Key("terminal-dock")).
		FillWidth().
		Height(height).
		Background(ThemeWindowBg(c)).
		BorderWidth(1, 0, 0, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			renderTerminalDockResizer(c, s, st, winHeight)

			// 顶栏
			ui.Row(c.Key("terminal-dock-header")).
				FillWidth().
				Height(terminalHeaderHeight).
				Padding(0, Space4).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ThemeBorder(c)).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Icon(c, IconTerminal()).
						Size(14, 14).
						TextColor(ThemeSecondaryText(c))

					// 标签栏水平滚动容器
					ui.ScrollHorizontal(c.Key("terminal-tabs-scroll")).
						Shrink(1).
						Children(func() {
							ui.Row(c.Key("terminal-tabs")).
								Gap(Space1).
								AlignItems(ui.Center).
								Children(func() {
									for i, tab := range tabs {
										idx := i
										tb := tab
										isActive := actTab != nil && tb.id == actTab.id

										tabTitle := ""
										if tb.term != nil && tb.term.Title() != "" {
											tabTitle = tb.term.Title()
										} else if tb.dir != "" {
											tabTitle = filepath.Base(tb.dir)
										} else {
											tabTitle = "终端"
										}
										if tb.main {
											tabTitle += " · 主目录"
										}
										label := fmt.Sprintf("%d %s", idx+1, tabTitle)

										textColor := ThemePrimaryText(c)
										if tb.err != "" || tb.term == nil {
											textColor = ThemeSecondaryText(c)
										}

										closeClicked := false
										tabBtn := TabButton(c, TabButtonProps{
											Key: fmt.Sprintf("terminal-tab-%d", tb.id), Label: label, Tooltip: tb.dir, Active: isActive,
										}, func() {
											ui.Row(c.Key(fmt.Sprintf("terminal-tab-inner-%d", tb.id))).
												Gap(Space1).
												AlignItems(ui.Center).
												Children(func() {
													ui.Text(c, label).
														FontSize(uiFont(TypeFootnote)).
														TextColor(textColor)

													if IconButton(c, IconButtonProps{
														Key:     fmt.Sprintf("terminal-tab-close-%d", tb.id),
														Icon:    IconX(),
														Label:   "关闭标签",
														Variant: ButtonGhost,
													}).Clicked() {
														closeClicked = true
													}
												})
										})
										if closeClicked {
											closeTerminalTab(s, st, threadID, tb.id)
										} else if tabBtn.Clicked() {
											switchTerminalTab(s, st, threadID, tb.id)
										}
									}

									// 新建按钮（+）
									isWorktree := workDir != "" && mainDir != "" && workDir != mainDir
									if isWorktree {
										trigger := ui.ButtonBase(c.Key("terminal-tab-new")).
											Label("新建终端").
											Tooltip("新建终端").
											Radius(RowCornerRadius).
											Padding(Space1, Space1)
										if trigger.Hovered() {
											trigger.Background(ThemeHover(c))
										}
										if trigger.Clicked() {
											terminalMu.Lock()
											st.newTabOpen = !st.newTabOpen
											newTabOpen = st.newTabOpen
											terminalMu.Unlock()
											s.invalidate()
										}
										trigger.Children(func() {
											ui.Icon(c, IconPlus()).
												Size(12, 12).
												TextColor(ThemeSecondaryText(c))
										})

										appPopover(c.Key("terminal-tab-new-popover"), trigger, &newTabOpen, func() {
											newTabOpen = false
										}, func() {
											popoverCard(c, ui.Column(c.Key("terminal-tab-new-menu"))).
												Gap(Space0).
												Padding(Space1).
												MinWidth(140).
												Children(func() {
													worktreeBtn := ui.ButtonBase(c.Key("terminal-tab-new-worktree")).
														Label("Worktree 目录").
														Padding(Space1, Space3).
														Radius(RadiusXS).
														FillWidth()
													if worktreeBtn.Hovered() {
														worktreeBtn.Background(ThemeHover(c))
													}
													if worktreeBtn.Clicked() {
														terminalMu.Lock()
														st.newTabOpen = false
														newTabOpen = false
														terminalMu.Unlock()
														_, _ = createTerminalTab(s, st, workDir, false)
														s.invalidate()
													}
													worktreeBtn.Children(func() {
														ui.Text(c, "Worktree 目录").FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
													})

													mainBtn := ui.ButtonBase(c.Key("terminal-tab-new-main")).
														Label("主项目目录").
														Padding(Space1, Space3).
														Radius(RadiusXS).
														FillWidth()
													if mainBtn.Hovered() {
														mainBtn.Background(ThemeHover(c))
													}
													if mainBtn.Clicked() {
														terminalMu.Lock()
														st.newTabOpen = false
														newTabOpen = false
														terminalMu.Unlock()
														_, _ = createTerminalTab(s, st, mainDir, true)
														s.invalidate()
													}
													mainBtn.Children(func() {
														ui.Text(c, "主项目目录").FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
													})
												})
										})

										terminalMu.Lock()
										st.newTabOpen = newTabOpen
										terminalMu.Unlock()
									} else {
										newBtn := ui.ButtonBase(c.Key("terminal-tab-new")).
											Label("新建终端").
											Tooltip("新建终端").
											Radius(RowCornerRadius).
											Padding(Space1, Space1)
										if newBtn.Hovered() {
											newBtn.Background(ThemeHover(c))
										}
										if newBtn.Clicked() {
											targetDir := workDir
											if targetDir == "" {
												targetDir = mainDir
											}
											_, _ = createTerminalTab(s, st, targetDir, false)
											s.invalidate()
										}
										newBtn.Children(func() {
											ui.Icon(c, IconPlus()).
												Size(12, 12).
												TextColor(ThemeSecondaryText(c))
										})
									}
								})
						})

					ui.Spacer(c)

					// 关闭按钮
					IconButton(c, IconButtonProps{
						Key:   "terminal-dock-close",
						Icon:  IconX(),
						Label: "关闭终端",
						OnClick: func() {
							toggleTerminal(s)
						},
						Variant: ButtonGhost,
					})
				})

			// 搜索条：绑定在打开时的标签上；切到别的标签或该终端已退出时收起。
			if searchState.open {
				if actTab != nil && actTab.id == searchState.tabID && actTab.term != nil {
					renderTerminalSearchBar(c, s, st, actTab)
				} else {
					closeTerminalSearch(s, st)
				}
			}

			if currentErrText != "" && actTab != nil {
				ui.Text(c, "新建终端失败: "+currentErrText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					Padding(Space1, Space4)
			}

			// 主体区域
			if threadID == "" {
				ui.Column(c.Key("terminal-dock-empty")).
					Grow(1).
					FillWidth().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, "没有选中会话").
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeSecondaryText(c))
					})
			} else if currentErrText != "" && actTab == nil {
				ui.Column(c.Key("terminal-dock-error")).
					Grow(1).
					FillWidth().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, currentErrText).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeDanger(c))
						Button(c, ButtonOpts{
							Key:   "terminal-retry",
							Text:  "重试",
							Label: "重新创建终端",
							OnClick: func() {
								terminalMu.Lock()
								delete(st.errors, threadID)
								terminalMu.Unlock()
								s.invalidate()
							},
							Variant: ButtonSecondary,
						})
					})
			} else if actTab != nil && actTab.err != "" {
				ui.Column(c.Key("terminal-dock-tab-error")).
					Grow(1).
					FillWidth().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, actTab.err).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeDanger(c))
						ui.Row(c.Key("terminal-dock-tab-actions")).
							Gap(Space3).
							Children(func() {
								Button(c, ButtonOpts{
									Key:   "terminal-tab-restart",
									Text:  "重启",
									Label: "重启终端",
									OnClick: func() {
										restartTerminalTab(s, st, threadID, actTab.id)
									},
									Variant: ButtonSecondary,
								})
								Button(c, ButtonOpts{
									Key:   "terminal-tab-close-exited",
									Text:  "关闭",
									Label: "关闭标签",
									OnClick: func() {
										closeTerminalTab(s, st, threadID, actTab.id)
									},
									Variant: ButtonSecondary,
								})
							})
					})
			} else if actTab != nil && actTab.term != nil {
				terminal.View(c, actTab.term).Fill()
			} else {
				ui.Column(c.Key("terminal-dock-empty-dir")).
					Grow(1).
					FillWidth().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, "未找到工作区目录").
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeSecondaryText(c))
					})
			}
		})
}

// runScriptInTerminal 确保当前工作目录的终端存在并可见，然后发送脚本命令行。
func (s *chatState) runScriptInTerminal(sc Script) {
	if s == nil {
		return
	}
	// 项目级脚本直接用用户填写的命令；package.json 脚本用包管理器 runner 执行脚本名。
	isProject := sc.Kind == "project"
	if !isProject && sc.Runner != "npm" && sc.Runner != "pnpm" && sc.Runner != "yarn" && sc.Runner != "bun" {
		s.setToast("不支持的脚本运行器")
		return
	}
	st := getTerminalState(s)
	terminalMu.Lock()
	st.visible = true
	terminalMu.Unlock()

	threadID, workDir := getSelectedThreadWorkDir(s)
	if workDir == "" {
		s.invalidate()
		return
	}

	term, err := getOrCreateTerminal(s, st, workDir)
	if err != nil || term == nil {
		s.invalidate()
		return
	}
	terminalMu.Lock()
	if tab := activeTerminalTab(st, threadID); tab != nil && tab.term == term {
		workDir = tab.dir
	}
	terminalMu.Unlock()

	var command string
	if isProject {
		// 命令原样写入终端，不当作脚本名转义。
		command = "cd -- " + quoteTerminalArg(workDir) + " && " + sc.Command
		if runtime.GOOS == "windows" {
			command = "Set-Location -LiteralPath " + quoteTerminalArg(workDir) + "; if ($?) { " + sc.Command + " }"
		}
	} else {
		command = sc.Runner + " run " + quoteTerminalArg(sc.Name)
		// 用户可能已在交互 shell 中切换目录，脚本仍须回到活动标签选择的目录。
		command = "cd -- " + quoteTerminalArg(workDir) + " && " + command
		if runtime.GOOS == "windows" {
			command = "Set-Location -LiteralPath " + quoteTerminalArg(workDir) + "; if ($?) { " + sc.Runner + " run " + quoteTerminalArg(sc.Name) + " }"
		}
	}
	sendTerminal(term, []byte(command+"\r"))
	s.invalidate()
}

func quoteTerminalArg(arg string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

// closeTerminals Close 掉该 chatState 的全部终端，并从 map 里删掉这条记录。
func closeTerminals(s *chatState) {
	if s == nil {
		return
	}
	// 关闭前把各会话的标签与回滚输出落盘，下次启动才能恢复。
	persistAllTerminalSnapshots(s)
	terminalMu.Lock()
	st, ok := terminalStates[s]
	var terms []*terminal.Terminal
	if ok {
		st.closed = true
		delete(terminalStates, s)
		for _, tabList := range st.tabs {
			for _, tab := range tabList {
				if tab.term != nil {
					terms = append(terms, tab.term)
					tab.term = nil
				}
			}
		}
	}
	terminalMu.Unlock()
	for _, term := range terms {
		_ = closeTerminal(term)
	}
}
