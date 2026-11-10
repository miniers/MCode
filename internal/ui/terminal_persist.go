package mcodeui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"mcode/internal/fsutil"
)

// 终端会话的持久化：把每个会话（thread）的标签列表与最近一段回滚输出写到
// <数据目录>/terminals/<threadID>.json，重启后再按记录重建标签并把历史灰显出来。
//
// mygo 终端（github.com/egoist/mygo v0.6.4，包路径 plugins/terminal）的能力结论：
//
//   - Terminal.Text() 返回屏幕加回滚区的纯文本，被终端宽度软换行的长行已经拼接好，
//     适合做输出快照；Terminal.Snapshot() 返回带样式的转义序列（VT），体积大且会把
//     主题、光标状态一起带进来，这里不用。
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

// terminalRestoring 表示正在按持久化记录重建标签。恢复期间 createTerminalTab 不落盘，
// 否则会用「只重建了一半」的标签列表覆盖原快照，还会把刚写入的灰显历史当成输出再存一遍。
// 恢复与新标签都只在主线程触发，普通 bool 足够。
var terminalRestoring bool

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
	dir  string
	main bool
	id   int
	term *terminal.Terminal
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
			rec.Output = limitTerminalOutput(stripRestoredTerminalHistory(in.term.Text()))
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
	terminalMu.Lock()
	st, ok := terminalStates[s]
	var inputs []terminalTabInput
	activeID := 0
	if ok {
		inputs = make([]terminalTabInput, 0, len(st.tabs[threadID]))
		for _, tab := range st.tabs[threadID] {
			if tab == nil {
				continue
			}
			// 锁内只拷字段值：锁外再读 tab.term 会与退出监听写 tab.term 竞争。
			inputs = append(inputs, terminalTabInput{dir: tab.dir, main: tab.main, id: tab.id, term: tab.term})
		}
		activeID = st.active[threadID]
	}
	terminalMu.Unlock()
	if !ok {
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

// restoreTerminalTabs 在会话第一次打开终端面板时按快照重建标签（每标签新起一个 shell）。
// 没有记录、记录损坏或目录都不存在时，按 fallbackDir 建一个默认标签。
func restoreTerminalTabs(s *chatState, st *terminalState, threadID, fallbackDir string) []*terminalTab {
	if s == nil || st == nil || threadID == "" {
		return nil
	}
	snap, ok := loadTerminalThreadSnapshot(threadID)
	var restored []*terminalTab
	if ok {
		snap.Tabs, snap.Active = filterRestorableTerminalTabs(snap.Tabs, snap.Active)
	}
	if ok && len(snap.Tabs) > 0 {
		// 恢复期间不落盘，见 terminalRestoring 的说明。
		terminalRestoring = true
		activeID := 0
		for i, rec := range snap.Tabs {
			tab, err := createTerminalTab(s, st, rec.Dir, rec.Main)
			if err != nil || tab == nil {
				continue
			}
			if len(restored) == 0 {
				activeID = tab.id
			}
			if i == snap.Active {
				activeID = tab.id
			}
			feedTerminalSnapshot(tab.term, rec.Output)
			restored = append(restored, tab)
		}
		terminalRestoring = false
		if len(restored) > 0 {
			terminalMu.Lock()
			for _, tab := range st.tabs[threadID] {
				if tab.id == activeID {
					st.active[threadID] = activeID
					break
				}
			}
			terminalMu.Unlock()
		}
	}
	if len(restored) == 0 {
		tab, err := createTerminalTab(s, st, fallbackDir, false)
		if err != nil || tab == nil {
			return nil
		}
		restored = append(restored, tab)
	}
	return restored
}

// handleTerminalOpenLink 是 terminal.Options.OpenLink 的实现：URL 交回 mygo 用系统浏览器打开
// （返回 false），文件路径在这里解析后打开（返回 true）。
func handleTerminalOpenLink(s *chatState, dir, link string) bool {
	raw := strings.TrimSpace(link)
	if kind, ok := classifyTerminalLink(raw); !ok || kind == terminalLinkURL {
		return false
	}
	cwd := dir
	if cwd == "" {
		_, cwd = getSelectedThreadWorkDir(s)
	}
	path, line, col, ok := resolveTerminalLinkTarget(cwd, raw)
	if !ok {
		if s != nil {
			s.setToast("找不到终端里的这个文件: " + raw)
		}
		return false
	}
	openTerminalLinkTarget(s, path, line, col)
	return true
}

// openTerminalLinkTarget 打开终端里点到的文件：带行号时优先用支持跳行的编辑器（VS Code、Cursor、Windsurf、Zed）
// 直接打开到对应行；没有行号或找不到可用编辑器时，工作目录内用文件面板预览，目录外退回源码编辑器窗口。
func openTerminalLinkTarget(s *chatState, path string, line, col int) {
	if s == nil || path == "" {
		return
	}
	if openPathAtLine(s, path, line, col) {
		return
	}
	threadID, workDir := getSelectedThreadWorkDir(s)
	if threadID != "" && workDir != "" {
		if rel, err := filepath.Rel(workDir, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			if !isFileManagerOpen(s) {
				toggleFileManager(s)
			}
			// 从终端路径打开的文件也是固定 tab，且属于外部入口：
			// 关掉最后一个 tab 时收起整个侧栏。
			s.openFileManagerFileExternal(threadID, rel)
			return
		}
	}
	openMiniEditorPath(path)
}
