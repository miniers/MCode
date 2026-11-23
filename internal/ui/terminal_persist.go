package mcodeui

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"mcode/internal/fsutil"
)

// 终端会话的持久化：把每个会话（thread）的标签列表与最近一段回滚输出写到
// <数据目录>/terminals/<threadID>.json，重启后再按记录重建标签并把历史灰显出来。
//
// mygo 终端（github.com/egoist/mygo，包路径 plugins/terminal）的能力结论：
//
//   - Terminal.Text() 返回屏幕加回滚区的纯文本，被终端宽度软换行的长行已经拼接好，
//     适合做输出快照；Terminal.Snapshot() 返回带样式的转义序列（VT），包含光标和
//     终端模式但不包含调色板；这里只恢复灰显纯文本，不用它。
//   - Terminal.Feed([]byte) 把数据「当作程序写出的内容」显示：只进显示区，不发给 shell，
//     用来写入灰显历史；Terminal.Send([]byte) 才是发给 shell 的输入，不能拿来恢复。
//   - terminal.Options.OpenLink func(link string) bool 是唯一的链接点击钩子：Command+click
//     命中 OSC 8 超链接、文本里的 URL，或文本里的文件路径（设置了 OpenLink 才会识别路径）时
//     在主线程回调，link 保留原样的 :line:col 后缀。回调返回 false 时 URL/超链接交给系统浏览器
//     （Context.OpenURL），路径则不处理；返回 true 表示钩子已经打开。
//   - 没有「给指定行加装饰/链接角标」之类的接口：HoveredLink() 只能读到鼠标当前命中的链接，
//     因此 terminal_links.go 里的折行拼接识别不参与渲染，只作为纯函数与单测交付。

const (
	// terminalSnapshotsDirName 是数据目录下存放终端快照的子目录名。
	terminalSnapshotsDirName = "terminals"
	// terminalSnapshotMaxLines 是每个标签最多保存的回滚行数。
	terminalSnapshotMaxLines = 2000
	// terminalSnapshotMaxBytes 是每个标签快照的字节上限（按 rune 边界截断）。
	terminalSnapshotMaxBytes = 256 << 10
	// terminalSnapshotMarker 是恢复出来的历史输出后面的分隔说明。
	terminalSnapshotMarker = "- 已恢复上次会话输出 -"
)

var (
	// terminalPersistMu 串行化快照文件的写盘与删盘，避免退出监听与主动关闭标签并发写同一文件时顺序错乱。
	terminalPersistMu sync.Mutex
	// loadTerminalSnapshotForRestore 供测试替换恢复读盘入口，验证异步恢复不在渲染栈中同步执行。
	loadTerminalSnapshotForRestore = loadTerminalThreadSnapshot
)

// terminalTabSnapshot 是单个终端标签的磁盘格式。
type terminalTabSnapshot struct {
	Dir string `json:"dir"`
	// Main 表示该标签开在主项目目录（而不是 worktree 目录）。
	Main bool `json:"main,omitempty"`
	// Title 记录退出前 shell 报告的标题；重建时无法回填（标题由新 shell 自己设置），仅作留档。
	Title string `json:"title,omitempty"`
	// Output 是回滚输出的纯文本快照，已去掉控制字符并限制行数与字节数。
	Output string `json:"output,omitempty"`
}

// terminalThreadSnapshot 是单个会话的终端快照文件内容。
type terminalThreadSnapshot struct {
	ThreadID string `json:"thread_id,omitempty"`
	// Active 是活动标签在 Tabs 里的下标，越界时按最后一个标签处理。
	Active int `json:"active"`
	// SavedAt 是落盘时间，便于排查旧快照。
	SavedAt string                `json:"saved_at,omitempty"`
	Tabs    []terminalTabSnapshot `json:"tabs"`
}

// terminalSnapshotsDir 返回终端快照目录；数据目录不可用时返回空字符串。
func terminalSnapshotsDir() string {
	dir := resolveDataDirFallback()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, terminalSnapshotsDirName)
}

// sanitizeTerminalThreadID 把 threadID 收敛成安全的文件名片段：
// 只保留字母数字与 . _ -，其余替换成下划线，避免路径穿越或非法字符。
func sanitizeTerminalThreadID(threadID string) string {
	trimmed := strings.TrimSpace(threadID)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if len(out) > 120 {
		out = out[:120]
	}
	if out == "" || out == "_" {
		return ""
	}
	return out
}

// terminalSnapshotPath 返回某个会话的快照文件路径；路径不可用时返回空字符串。
func terminalSnapshotPath(threadID string) string {
	dir := terminalSnapshotsDir()
	name := sanitizeTerminalThreadID(threadID)
	if dir == "" || name == "" {
		return ""
	}
	return filepath.Join(dir, name+".json")
}

// saveTerminalThreadSnapshot 原子写入快照：临时文件 + rename，权限 0600。
func saveTerminalThreadSnapshot(snap terminalThreadSnapshot) error {
	path := terminalSnapshotPath(snap.ThreadID)
	if path == "" {
		return errors.New("未找到 mcode 数据目录")
	}
	snap.SavedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// loadTerminalThreadSnapshot 读取快照；文件缺失或内容损坏时返回 false（按没有记录处理）。
func loadTerminalThreadSnapshot(threadID string) (terminalThreadSnapshot, bool) {
	path := terminalSnapshotPath(threadID)
	if path == "" {
		return terminalThreadSnapshot{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return terminalThreadSnapshot{}, false
	}
	var snap terminalThreadSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return terminalThreadSnapshot{}, false
	}
	if snap.ThreadID == "" {
		snap.ThreadID = threadID
	}
	return snap, true
}

// removeTerminalSnapshot 删除某个会话的快照文件；会话被删除或归档时可以调用。
func removeTerminalSnapshot(threadID string) error {
	path := terminalSnapshotPath(threadID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// filterRestorableTerminalTabs 丢弃目录已不存在的标签记录：重启后目录可能已被删除，
// 重建出来的 shell 会立刻报错，不如按「没有这条记录」处理。
// 返回过滤后的标签与重映射过的新活动标签下标。
func filterRestorableTerminalTabs(tabs []terminalTabSnapshot, active int) ([]terminalTabSnapshot, int) {
	out := make([]terminalTabSnapshot, 0, len(tabs))
	newActive := 0
	for i, tab := range tabs {
		dir := strings.TrimSpace(tab.Dir)
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		tab.Dir = dir
		if i == active {
			newActive = len(out)
		}
		out = append(out, tab)
	}
	if len(out) == 0 {
		return out, 0
	}
	return out, newActive
}

// terminalTabInput 是构建快照需要的标签字段：字段值都在 terminalMu 内拷贝出来，
// 之后只用这份局部值，不再读 tab 结构体本身（退出监听会持锁给 tab.term 置 nil）。
type terminalTabInput struct {
	dir        string
	main       bool
	id         int
	term       *terminal.Terminal
	lastOutput string
}

// captureTerminalOutput 从活着的终端抓取清洗后的回滚输出。
func captureTerminalOutput(term *terminal.Terminal) string {
	if term == nil {
		return ""
	}
	return limitTerminalOutput(stripRestoredTerminalHistory(term.Text()))
}

// captureTerminalThreadSnapshot 抓取当前标签列表与各自回滚输出。
// inputs 必须是调用方在 terminalMu 内拷贝出来的值；终端的 Text/Title 在锁外调用，
// 避免把 terminalMu 带进 mygo 终端自己的锁（它的回调会反过来取 terminalMu）。
// 快照期间终端可能被别的 goroutine 关掉：mygo 的 Title/Text 在终端自己的锁内先判断
// t.term == nil 再取值，关闭后只会返回空串，不会 panic，所以这里不需要额外保护。
func captureTerminalThreadSnapshot(threadID string, inputs []terminalTabInput, activeID int) terminalThreadSnapshot {
	snap := terminalThreadSnapshot{ThreadID: threadID, Tabs: make([]terminalTabSnapshot, 0, len(inputs))}
	for _, in := range inputs {
		rec := terminalTabSnapshot{Dir: in.dir, Main: in.main}
		if in.term != nil {
			rec.Title = in.term.Title()
			rec.Output = captureTerminalOutput(in.term)
		} else {
			rec.Output = in.lastOutput
		}
		if in.id == activeID {
			snap.Active = len(snap.Tabs)
		}
		snap.Tabs = append(snap.Tabs, rec)
	}
	return snap
}

// stripRestoredTerminalHistory 去掉显示区里此前恢复进来的那一段：
// 快照里带着上次恢复的灰显历史时，反复重启会把历史一层层累加，所以只保留最后一个分隔标记之后的内容。
func stripRestoredTerminalHistory(text string) string {
	idx := strings.LastIndex(text, terminalSnapshotMarker)
	if idx < 0 {
		return text
	}
	rest := text[idx+len(terminalSnapshotMarker):]
	return strings.TrimLeft(rest, "\r\n")
}

// limitTerminalOutput 把回滚文本整理成可落盘的形式：去掉除换行、制表符外的控制字符，
// 只保留最后 terminalSnapshotMaxLines 行，并截到 terminalSnapshotMaxBytes 以内。
func limitTerminalOutput(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// 丢掉其他控制字符（包括 ANSI 序列残留的 ESC）。
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		return ""
	}
	if lines := strings.Split(out, "\n"); len(lines) > terminalSnapshotMaxLines {
		out = strings.Join(lines[len(lines)-terminalSnapshotMaxLines:], "\n")
	}
	if len(out) > terminalSnapshotMaxBytes {
		runes := []rune(out)
		// 从尾部按字节上限向前数，避免把一个多字节字符切开。
		kept, start := 0, len(runes)
		for i := len(runes) - 1; i >= 0; i-- {
			next := kept + len(string(runes[i]))
			if next > terminalSnapshotMaxBytes {
				break
			}
			kept = next
			start = i
		}
		out = string(runes[start:])
	}
	return out
}

// persistTerminalState 把某个会话的终端标签与输出写盘；没有标签时删掉旧快照。
// 由标签新建、切换、关闭、终端退出与退出前调用。
func persistTerminalState(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	terminalPersistMu.Lock()
	defer terminalPersistMu.Unlock()

	terminalMu.Lock()
	st, ok := terminalStates[s]
	var inputs []terminalTabInput
	activeID := 0
	var hasThread bool
	if ok {
		var tabs []*terminalTab
		tabs, hasThread = st.tabs[threadID]
		inputs = make([]terminalTabInput, 0, len(tabs))
		for _, tab := range tabs {
			if tab == nil {
				continue
			}
			// 锁内只拷字段值：锁外再读 tab.term 会与退出监听写 tab.term 竞争。
			inputs = append(inputs, terminalTabInput{
				dir:        tab.dir,
				main:       tab.main,
				id:         tab.id,
				term:       tab.term,
				lastOutput: tab.lastOutput,
			})
		}
		activeID = st.active[threadID]
	}
	terminalMu.Unlock()
	if !ok || !hasThread {
		return
	}
	snap := captureTerminalThreadSnapshot(threadID, inputs, activeID)
	if len(snap.Tabs) == 0 {
		_ = removeTerminalSnapshot(threadID)
		return
	}
	_ = saveTerminalThreadSnapshot(snap)
}

// persistAllTerminalSnapshots 在退出前把所有会话的终端快照写盘。
func persistAllTerminalSnapshots(s *chatState) {
	if s == nil {
		return
	}
	terminalMu.Lock()
	st, ok := terminalStates[s]
	var ids []string
	if ok {
		for threadID := range st.tabs {
			ids = append(ids, threadID)
		}
	}
	terminalMu.Unlock()
	if !ok {
		return
	}
	for _, threadID := range ids {
		persistTerminalState(s, threadID)
	}
}

// feedTerminalSnapshot 把上次的输出作为灰显历史写进新终端：Feed 只影响显示区，
// 不会发给 shell，用户回车后提示符仍照常工作。
func feedTerminalSnapshot(term *terminal.Terminal, output string) {
	if term == nil || output == "" {
		return
	}
	var b strings.Builder
	// 2 是 dim（变暗）属性，恢复出来的历史统一灰显，与当前会话区分开。
	b.WriteString("\x1b[2m")
	for _, line := range strings.Split(output, "\n") {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.WriteString(terminalSnapshotMarker)
	b.WriteString("\x1b[0m\r\n")
	term.Feed([]byte(b.String()))
}

type terminalRestorePlan struct {
	fromSnapshot bool
	tabs         []terminalTabSnapshot
	active       int
}

type preparedTerminalTab struct {
	term     *terminal.Terminal
	dir      string
	main     bool
	fontSize int
}

// buildTerminalRestorePlan 读取磁盘快照并过滤失效目录，不操作终端状态锁。
func buildTerminalRestorePlan(threadID, fallbackDir string) terminalRestorePlan {
	if snap, ok := loadTerminalSnapshotForRestore(threadID); ok {
		tabs, active := filterRestorableTerminalTabs(snap.Tabs, snap.Active)
		if len(tabs) > 0 {
			return terminalRestorePlan{fromSnapshot: true, tabs: tabs, active: active}
		}
	}
	if fallbackDir == "" {
		return terminalRestorePlan{}
	}
	return terminalRestorePlan{
		fromSnapshot: false,
		tabs:         []terminalTabSnapshot{{Dir: fallbackDir}},
		active:       0,
	}
}

// prepareTerminalRestoreTabs 根据恢复计划启动终端进程并写入灰显历史。
func prepareTerminalRestoreTabs(s *chatState, plan terminalRestorePlan, fallbackDir string) (prepared []preparedTerminalTab, activeIdx int, usedFallback bool, lastErr string) {
	if len(plan.tabs) == 0 {
		return nil, 0, false, ""
	}
	fontSize := getTerminalFontSize(s)
	if plan.fromSnapshot {
		for i, rec := range plan.tabs {
			term, err := startTerminalProcess(s, rec.Dir)
			if err != nil {
				continue
			}
			if len(prepared) == 0 {
				activeIdx = 0
			}
			if i == plan.active {
				activeIdx = len(prepared)
			}
			feedTerminalSnapshot(term, rec.Output)
			prepared = append(prepared, preparedTerminalTab{
				term:     term,
				dir:      rec.Dir,
				main:     rec.Main,
				fontSize: fontSize,
			})
		}
		if len(prepared) > 0 {
			return prepared, activeIdx, false, ""
		}
		if fallbackDir == "" {
			return nil, 0, false, ""
		}
		plan = terminalRestorePlan{tabs: []terminalTabSnapshot{{Dir: fallbackDir}}, active: 0}
	}

	usedFallback = true
	for _, rec := range plan.tabs {
		term, err := startTerminalProcess(s, rec.Dir)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		prepared = append(prepared, preparedTerminalTab{
			term:     term,
			dir:      rec.Dir,
			main:     rec.Main,
			fontSize: fontSize,
		})
	}
	return prepared, 0, usedFallback, lastErr
}

// applyTerminalRestoreTabs 在 terminalMu 锁内原子提交恢复出的标签；若状态已变化则释放新建终端。
func applyTerminalRestoreTabs(s *chatState, st *terminalState, threadID string, requireRestoring bool, prepared []preparedTerminalTab, activeIdx int, usedFallback bool, lastErr string) []*terminalTab {
	terminalMu.Lock()
	abort := st.closed || st.restored[threadID] || len(st.tabs[threadID]) > 0
	if requireRestoring && !st.restoring[threadID] {
		abort = true
	}
	if abort {
		delete(st.restoring, threadID)
		terminalMu.Unlock()
		for _, p := range prepared {
			if p.term != nil {
				_ = closeTerminal(p.term)
			}
		}
		return nil
	}

	delete(st.restoring, threadID)

	var created []*terminalTab
	if len(prepared) == 0 {
		if lastErr != "" {
			st.errors[threadID] = lastErr
		}
		terminalMu.Unlock()
		return nil
	}

	st.restored[threadID] = true

	created = make([]*terminalTab, 0, len(prepared))
	for _, p := range prepared {
		tabID := st.nextTabID
		st.nextTabID++
		created = append(created, &terminalTab{
			id:       tabID,
			term:     p.term,
			dir:      p.dir,
			main:     p.main,
			fontSize: p.fontSize,
		})
	}
	st.tabs[threadID] = created
	if activeIdx < 0 || activeIdx >= len(created) {
		activeIdx = 0
	}
	st.active[threadID] = created[activeIdx].id
	delete(st.errors, threadID)
	terminalMu.Unlock()

	for _, tab := range created {
		attachTerminalListener(s, threadID, tab, tab.term)
	}
	if usedFallback {
		persistTerminalState(s, threadID)
	}
	return created
}

// startTerminalRestoreAsync 在后台 goroutine 执行读盘与目录校验，再应用到会话终端状态并触发重绘。
func startTerminalRestoreAsync(s *chatState, st *terminalState, threadID, fallbackDir string) {
	go func() {
		plan := buildTerminalRestorePlan(threadID, fallbackDir)
		prepared, activeIdx, usedFallback, lastErr := prepareTerminalRestoreTabs(s, plan, fallbackDir)
		_ = applyTerminalRestoreTabs(s, st, threadID, true, prepared, activeIdx, usedFallback, lastErr)
		s.invalidate()
	}()
}

// restoreTerminalTabs 在会话需要立即可用终端时同步按快照重建标签（每标签新起一个 shell）。
// 没有记录、记录损坏或目录都不存在时，按 fallbackDir 建一个默认标签。
func restoreTerminalTabs(s *chatState, st *terminalState, threadID, fallbackDir string) []*terminalTab {
	if s == nil || st == nil || threadID == "" {
		return nil
	}
	plan := buildTerminalRestorePlan(threadID, fallbackDir)
	prepared, activeIdx, usedFallback, lastErr := prepareTerminalRestoreTabs(s, plan, fallbackDir)
	return applyTerminalRestoreTabs(s, st, threadID, false, prepared, activeIdx, usedFallback, lastErr)
}

// isAllowedTerminalExternalURL 判断外部链接是否允许交给系统打开：
// 仅放行 scheme 为 http 或 https（不区分大小写）且 Host 非空的 URL。
func isAllowedTerminalExternalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	return u.Host != ""
}

// handleTerminalOpenLink 是 terminal.Options.OpenLink 的实现：本地文件路径在这里解析后打开
// （返回 true），其余链接仅当 scheme 为 http/https 且 Host 非空时交回 mygo 用系统浏览器打开
// （返回 false），其他协议（如 file://、smb://、javascript:、自定义协议）一律吞掉（返回 true）。
func handleTerminalOpenLink(s *chatState, dir, link string) bool {
	raw := strings.TrimSpace(link)
	if raw == "" {
		return true
	}
	if kind, ok := classifyTerminalLink(raw); ok && kind == terminalLinkPath {
		cwd := dir
		if cwd == "" {
			_, cwd = getSelectedThreadWorkDir(s)
		}
		path, line, col, resolved := resolveTerminalLinkTarget(cwd, raw)
		if !resolved {
			if s != nil {
				s.setToast("找不到终端里的这个文件: " + raw)
			}
			return true
		}
		openTerminalLinkTarget(s, path, line, col)
		return true
	}
	if isAllowedTerminalExternalURL(raw) {
		return false
	}
	return true
}

// openTerminalLinkTarget 打开终端里点到的文件：通过统一跳转总线优先使用外部编辑器（带行号直接跳行），
// 不可用或失败时，工作目录内退回内置文件面板并保留行号高亮，目录外退回系统默认应用打开。
func openTerminalLinkTarget(s *chatState, path string, line, col int) {
	if s == nil || path == "" {
		return
	}
	s.openFileAtLocation(fileJumpRequest{
		Path:   path,
		Line:   line,
		Col:    col,
		Target: fileJumpExternal,
	})
}
