package mcodeui

import (
	"context"
	"fmt"
	"hash/maphash"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// toolDiffMaxLines 为内嵌 diff 中单个文件最多显示的行数，超出部分提示已截断。
const toolDiffMaxLines = 200

// toolDiffLineNumberWidth 保证四位行号有足够空间。 // token-exempt: diff 行号列的固定宽度
const toolDiffLineNumberWidth float32 = 32

// timelineDiffLineMarkWidth 是改动行里 +− 标记列的固定宽度。
const timelineDiffLineMarkWidth float32 = 14

// toolDiffFoldKeep 为折叠上下文时靠近改动一侧保留的行数，
// toolDiffFoldMinKeep 为触发折叠的最少连续上下文行数。
const (
	toolDiffFoldKeep    = 3
	toolDiffFoldMinKeep = 6
)

// toolDiffFoldState 记录内嵌 diff 中被折叠上下文段的展开状态，按 "消息-文件-段" 定位。
var (
	toolDiffFoldMu    sync.Mutex
	toolDiffFoldState = map[string]bool{}
)

// toolDiffFoldSnapshot 复制一份已展开的折叠段，渲染期间只用快照，
// 避免每个折叠段各加一次锁。
func toolDiffFoldSnapshot() map[string]bool {
	toolDiffFoldMu.Lock()
	defer toolDiffFoldMu.Unlock()
	if len(toolDiffFoldState) == 0 {
		return nil
	}
	folds := make(map[string]bool, len(toolDiffFoldState))
	for key, open := range toolDiffFoldState {
		if open {
			folds[key] = true
		}
	}
	return folds
}

// toggleToolDiffFold 切换某段被折叠上下文的展开状态。
func toggleToolDiffFold(c *ui.Context, s *chatState, key string) {
	toolDiffFoldMu.Lock()
	if toolDiffFoldState == nil {
		toolDiffFoldState = make(map[string]bool)
	}
	toolDiffFoldState[key] = !toolDiffFoldState[key]
	toolDiffFoldMu.Unlock()
	s.holdChatPlace()
	c.Invalidate()
}

// toolDiffDisplayLine 表示内嵌 diff 的一行显示内容：普通 diff 行，或被折叠的上下文占位行。
type toolDiffDisplayLine struct {
	Line   git.Line // 普通行是对应的 diff 行；折叠占位行取被折叠区间的首行，用于对齐行号
	Fold   int      // 折叠占位行折叠掉的行数，0 表示普通行
	FoldID int      // 折叠占位行在该 hunk 内的序号（从 0 开始），展开状态按它记录
}

// toolDiffDisplayLines 把一个 hunk 的行序列转成显示行序列：连续未改动的上下文行超过
// toolDiffFoldMinKeep 行时，只保留靠近改动的一侧各 toolDiffFoldKeep 行，中间合并成一行折叠占位行。
// hunk 首尾的连续上下文只保留靠近改动的那侧；expanded 返回 true 的折叠段完整显示，可为 nil。
func toolDiffDisplayLines(lines []git.Line, expanded func(foldID int) bool) []toolDiffDisplayLine {
	out := make([]toolDiffDisplayLine, 0, len(lines))
	foldID := 0
	for i := 0; i < len(lines); {
		if lines[i].Kind != ' ' {
			out = append(out, toolDiffDisplayLine{Line: lines[i]})
			i++
			continue
		}
		start := i
		for i < len(lines) && lines[i].Kind == ' ' {
			i++
		}
		run := lines[start:i]
		if len(run) <= toolDiffFoldMinKeep {
			for _, line := range run {
				out = append(out, toolDiffDisplayLine{Line: line})
			}
			continue
		}
		// 折叠段序号按候选段分配，与是否已展开无关，展开时编号保持稳定
		id := foldID
		foldID++
		if expanded != nil && expanded(id) {
			for _, line := range run {
				out = append(out, toolDiffDisplayLine{Line: line})
			}
			continue
		}
		head, tail := toolDiffFoldKeep, toolDiffFoldKeep
		switch {
		case start == 0 && i == len(lines):
			// 整个 hunk 都是上下文行，首尾都保留
		case start == 0:
			head = 0 // hunk 开头的上下文只保留靠近改动的尾部
		case i == len(lines):
			tail = 0 // hunk 结尾的上下文只保留靠近改动的头部
		}
		for _, line := range run[:head] {
			out = append(out, toolDiffDisplayLine{Line: line})
		}
		hidden := run[head : len(run)-tail]
		out = append(out, toolDiffDisplayLine{Line: hidden[0], Fold: len(hidden), FoldID: id})
		for _, line := range run[len(run)-tail:] {
			out = append(out, toolDiffDisplayLine{Line: line})
		}
	}
	return out
}

// toolDiffInlineCacheLimit 限制内嵌 diff 缓存的消息条数，防止长会话内存只增不减。
const toolDiffInlineCacheLimit = 32

// toolDiffParse 是内嵌 diff 的解析入口，测试可替换以统计解析次数。
var toolDiffParse = git.ParseUnifiedDiff

// toolDiffHashSeed 用于内嵌 diff 的内容指纹，避免每帧比较整段补丁正文。
var toolDiffHashSeed = maphash.MakeSeed()

// toolDiffState 保存文件修改工具步骤的内嵌 diff 状态，读写需持有 chatState.mu。
type toolDiffState struct {
	generation uint64
	open       map[string]bool                 // 按消息 ID 记录是否展开
	turnKey    map[string]string               // "threadID|#轮次序号" → "threadID|turnID"
	entries    map[string]*toolDiffEntry       // 按 "threadID|turnID" 缓存；轮次尚未解析出时按 "threadID|#轮次序号" 临时记录
	inline     map[string]*toolDiffInlineEntry // 按消息 ID 缓存内嵌 diff 的解析结果与显示行
	inlineSeq  []string                        // 内嵌 diff 缓存的最近使用顺序，超出上限时淘汰最旧条目
}

// toolDiffInlineEntry 缓存一条消息内嵌 diff 的解析结果：文件、增删统计与各 hunk 的显示行。
// 指纹不一致时整条缓存作废；折叠状态变化时只重算受影响的 hunk。
type toolDiffInlineEntry struct {
	fingerprint string
	files       []git.FileDiff
	stats       []toolDiffFileStats
	display     map[string]toolDiffHunkLines // "文件序号-hunk 序号" → 显示行
}

// toolDiffFileStats 记录单个文件的增删与总行数，避免每帧重扫全部行。
type toolDiffFileStats struct {
	adds  int
	dels  int
	total int
}

// toolDiffHunkLines 是某个 hunk 的显示行缓存，foldSig 为折叠状态指纹。
type toolDiffHunkLines struct {
	foldSig string
	lines   []toolDiffDisplayLine
}

// toolDiffInlineData 是一次渲染所需的缓存数据副本。
type toolDiffInlineData struct {
	files   []git.FileDiff
	stats   []toolDiffFileStats
	display map[string][]toolDiffDisplayLine // "文件序号-hunk 序号" → 显示行
}

// toolDiffHunkCacheKey 返回某个 hunk 在缓存中的键。
func toolDiffHunkCacheKey(fileIdx, hunkIdx int) string {
	return strconv.Itoa(fileIdx) + "-" + strconv.Itoa(hunkIdx)
}

// toolDiffFileStatsFor 统计单个文件的增删与总行数，只在解析或加载时调用一次。
func toolDiffFileStatsFor(file git.FileDiff) toolDiffFileStats {
	var stats toolDiffFileStats
	for _, hunk := range file.Hunks {
		stats.total += len(hunk.Lines)
		for _, line := range hunk.Lines {
			switch line.Kind {
			case '+':
				stats.adds++
			case '-':
				stats.dels++
			}
		}
	}
	return stats
}

// toolDiffFingerprint 返回内嵌 diff 的输入指纹：来源、正文长度与内容哈希（含工具输入）。
func toolDiffFingerprint(source, text, toolInput string) string {
	sum := maphash.String(toolDiffHashSeed, text)
	if toolInput != "" {
		sum ^= maphash.String(toolDiffHashSeed, toolInput)
	}
	return source + ":" + strconv.Itoa(len(text)) + ":" + strconv.FormatUint(sum, 16)
}

// toolDiffFoldSignature 返回某个 hunk 已展开折叠段的指纹，指纹不变时显示行可直接复用。
func toolDiffFoldSignature(folds map[string]bool, hunkKey string) string {
	if len(folds) == 0 {
		return ""
	}
	prefix := hunkKey + "-fold-"
	ids := make([]int, 0, len(folds))
	for key, open := range folds {
		if !open || !strings.HasPrefix(key, prefix) {
			continue
		}
		if id, err := strconv.Atoi(key[len(prefix):]); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Ints(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(strconv.Itoa(id))
		b.WriteByte(',')
	}
	return b.String()
}

// view 返回渲染数据；折叠状态变了才重算对应 hunk 的显示行，其余 hunk 直接复用。
// 调用方需持有 chatState.mu。
func (e *toolDiffInlineEntry) view(msgID string, folds map[string]bool) toolDiffInlineData {
	display := make(map[string][]toolDiffDisplayLine, len(e.display))
	fresh := make(map[string]toolDiffHunkLines, len(e.display))
	for fileIdx, file := range e.files {
		for hunkIdx, hunk := range file.Hunks {
			cacheKey := toolDiffHunkCacheKey(fileIdx, hunkIdx)
			hunkKey := fmt.Sprintf("%s-%d-%d", msgID, fileIdx, hunkIdx)
			sig := toolDiffFoldSignature(folds, hunkKey)
			if cached, ok := e.display[cacheKey]; ok && cached.foldSig == sig {
				fresh[cacheKey] = cached
				display[cacheKey] = cached.lines
				continue
			}
			lines := toolDiffDisplayLines(hunk.Lines, func(foldID int) bool {
				return folds[fmt.Sprintf("%s-fold-%d", hunkKey, foldID)]
			})
			fresh[cacheKey] = toolDiffHunkLines{foldSig: sig, lines: lines}
			display[cacheKey] = lines
		}
	}
	e.display = fresh
	return toolDiffInlineData{files: e.files, stats: e.stats, display: display}
}

// touchToolDiffInlineLocked 记录内嵌 diff 缓存的最近使用顺序，调用方需持有 s.mu。
func (s *chatState) touchToolDiffInlineLocked(key string) {
	for i, existing := range s.toolDiff.inlineSeq {
		if existing == key {
			s.toolDiff.inlineSeq = append(s.toolDiff.inlineSeq[:i], s.toolDiff.inlineSeq[i+1:]...)
			break
		}
	}
	s.toolDiff.inlineSeq = append(s.toolDiff.inlineSeq, key)
}

// trimToolDiffInlineLocked 按最近使用顺序淘汰超出上限的内嵌 diff 缓存，调用方需持有 s.mu。
func (s *chatState) trimToolDiffInlineLocked() {
	for len(s.toolDiff.inlineSeq) > toolDiffInlineCacheLimit {
		oldest := s.toolDiff.inlineSeq[0]
		s.toolDiff.inlineSeq = s.toolDiff.inlineSeq[1:]
		delete(s.toolDiff.inline, oldest)
	}
}

// toolDiffDataFor 返回一条消息内嵌 diff 的渲染数据。命中缓存时不再解析补丁、不再筛选路径；
// load 只在未命中时调用，调用期间不持有 s.mu，避免阻塞其它读写。
func (s *chatState) toolDiffDataFor(m Message, source string, folds map[string]bool, load func() []git.FileDiff) (toolDiffInlineData, bool) {
	fingerprint := toolDiffFingerprint(source, m.Text, m.ToolInput)
	key := m.ID
	if key == "" {
		key = fingerprint
	}

	s.mu.Lock()
	if e := s.toolDiff.inline[key]; e != nil && e.fingerprint == fingerprint {
		s.touchToolDiffInlineLocked(key)
		data := e.view(m.ID, folds)
		s.mu.Unlock()
		return data, true
	}
	s.mu.Unlock()

	files := load()
	if len(files) == 0 {
		return toolDiffInlineData{}, false
	}
	stats := make([]toolDiffFileStats, len(files))
	for i, file := range files {
		stats[i] = toolDiffFileStatsFor(file)
	}

	s.mu.Lock()
	if s.toolDiff.inline == nil {
		s.toolDiff.inline = make(map[string]*toolDiffInlineEntry)
	}
	entry := &toolDiffInlineEntry{fingerprint: fingerprint, files: files, stats: stats}
	s.toolDiff.inline[key] = entry
	s.touchToolDiffInlineLocked(key)
	s.trimToolDiffInlineLocked()
	data := entry.view(m.ID, folds)
	s.mu.Unlock()
	return data, true
}

// toolDiffEntry 是某一轮改动的加载结果。
type toolDiffEntry struct {
	loading   bool
	err       string
	failure   string
	files     []git.FileDiff
	truncated bool
}

// fileEditToolNames 为算作文件修改的工具名（已转小写并去掉 _ 与 -）。
var fileEditToolNames = map[string]bool{
	"edit":                    true,
	"multiedit":               true,
	"write":                   true,
	"writefile":               true,
	"editfile":                true,
	"createfile":              true,
	"writetofile":             true,
	"applypatch":              true,
	"patch":                   true,
	"strreplace":              true,
	"strreplaceeditor":        true,
	"strreplacebasededittool": true,
	"filechange":              true,
	"notebookedit":            true,
	"editnotebook":            true,
	"replacefilecontent":      true,
	"multireplacefilecontent": true,
}

// toolPathKeyRe 从工具输入 JSON 中提取路径字段的值。
var toolPathKeyRe = regexp.MustCompile(`"(?:file_path|filePath|filepath|path|target_file|targetFile|TargetFile|notebook_path|AbsolutePath)"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// 路径后必须有行结束标记，避免把被截断的输入末尾误当成完整路径。
var (
	toolPatchFileRe = regexp.MustCompile(`\*\*\* (?:Update|Add|Delete) File: ([^\n"\\]+)(?:\n|\\n|")`)
	toolPatchMoveRe = regexp.MustCompile(`\*\*\* Move to: ([^\n"\\]+)(?:\n|\\n|")`)
)

// toolMessageName 返回工具消息的工具名。优先使用 m.ToolName，为空时从工具文本“工具名 输入JSON\n输出”中提取。
func toolMessageName(m Message) string {
	if m.ToolName != "" {
		return m.ToolName
	}
	line := strings.TrimSpace(m.Text)
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}
	if idx := strings.IndexAny(line, " \t{"); idx != -1 {
		line = line[:idx]
	}
	return line
}

// isFileEditTool 判断工具消息是否为文件修改类工具（edit / write / apply_patch / str_replace 等）。
func isFileEditTool(m Message) bool {
	if m.Role != "tool" {
		return false
	}
	return fileEditToolNames[toolNameKey(toolMessageName(m))]
}

// toolEditedPaths 从工具消息文本或结构化输入中提取被修改的文件路径（去重，保持出现顺序）。
func toolEditedPaths(m Message) []string {
	var paths []string
	seen := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	// 优先从 m.ToolInput 的 JSON 中读取文件路径字段
	filePathKeys := []string{
		"file_path", "filePath", "filepath", "path",
		"target_file", "targetFile", "TargetFile",
		"notebook_path", "AbsolutePath",
		"Path", "FilePath",
	}
	foundInJSON := false
	if fields := toolInputFields(m); len(fields) > 0 {
		for _, k := range filePathKeys {
			if val, ok := fields[k]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					add(s)
					foundInJSON = true
				} else if arr, ok := val.([]any); ok {
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							add(s)
							foundInJSON = true
						}
					}
				}
			}
		}
	}

	// 解析失败或 ToolInput 为空时，回退到正则提取
	if !foundInJSON {
		firstLine := m.Text
		if idx := strings.IndexByte(firstLine, '\n'); idx != -1 {
			firstLine = firstLine[:idx]
		}
		for _, match := range toolPathKeyRe.FindAllStringSubmatch(firstLine, -1) {
			raw := match[1]
			if unq, err := strconv.Unquote(`"` + raw + `"`); err == nil {
				raw = unq
			}
			add(raw)
		}
	}

	// apply_patch 补丁头的正则优先在 m.ToolInput 上匹配，m.ToolInput 为空时再匹配 m.Text
	patchSource := m.ToolInput
	if strings.TrimSpace(patchSource) == "" {
		patchSource = m.Text
	}
	for _, match := range toolPatchFileRe.FindAllStringSubmatch(patchSource, -1) {
		add(match[1])
	}
	for _, match := range toolPatchMoveRe.FindAllStringSubmatch(patchSource, -1) {
		add(match[1])
	}
	return paths
}

// toolEmbeddedDiff 返回工具消息文本中自带的 unified diff（从行首的 "diff --git " 开始），没有则返回空。
func toolEmbeddedDiff(text string) string {
	idx := strings.Index(text, "diff --git ")
	for idx != -1 {
		if idx == 0 || text[idx-1] == '\n' {
			return text[idx:]
		}
		next := strings.Index(text[idx+1:], "diff --git ")
		if next == -1 {
			break
		}
		idx += 1 + next
	}
	return ""
}

// toolDiffPathMatch 判断 diff 中的文件路径与工具编辑的路径是否指向同一文件（按后缀匹配）。
func toolDiffPathMatch(filePath, edited string) bool {
	filePath = strings.TrimPrefix(strings.ReplaceAll(filePath, "\\", "/"), "./")
	edited = strings.TrimPrefix(strings.ReplaceAll(edited, "\\", "/"), "./")
	if filePath == "" || edited == "" {
		return false
	}
	return filePath == edited ||
		strings.HasSuffix(edited, "/"+filePath) ||
		strings.HasSuffix(filePath, "/"+edited)
}

// filterToolDiffFiles 按工具编辑的路径筛选文件；未能提取到路径时返回全部文件。
func filterToolDiffFiles(files []git.FileDiff, paths []string) []git.FileDiff {
	if len(paths) == 0 {
		return files
	}
	var out []git.FileDiff
	for _, f := range files {
		for _, p := range paths {
			if toolDiffPathMatch(f.NewPath, p) || toolDiffPathMatch(f.OldPath, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// isToolDiffOpen 返回消息的内嵌 diff 是否展开。
func (s *chatState) isToolDiffOpen(msgID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolDiff.open[msgID]
}

// toggleToolDiff 切换工具消息内嵌 diff 的展开状态；重新展开时清掉失败的缓存以便重试。
func (s *chatState) toggleToolDiff(m Message) {
	s.mu.Lock()
	if s.toolDiff.open == nil {
		s.toolDiff.open = make(map[string]bool)
	}
	open := !s.toolDiff.open[m.ID]
	s.toolDiff.open[m.ID] = open
	if open {
		tid := s.selectedThreadID
		key := s.toolDiffKeyLocked(tid, m.TurnIndex)
		if m.TurnID != "" {
			key = tid + "|" + m.TurnID
		}
		if e := s.toolDiff.entries[key]; e != nil && !e.loading && (e.err != "" || e.failure != "") {
			// 连同轮次映射一起清掉，重新加载时从轮次列表开始
			delete(s.toolDiff.entries, key)
			delete(s.toolDiff.turnKey, tid+"|#"+strconv.Itoa(m.TurnIndex))
		}
	}
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// toolDiffKeyLocked 返回某一轮改动在缓存中的 key，调用方需持有 s.mu。
func (s *chatState) toolDiffKeyLocked(threadID string, turnIndex int) string {
	idxKey := threadID + "|#" + strconv.Itoa(turnIndex)
	if k, ok := s.toolDiff.turnKey[idxKey]; ok {
		return k
	}
	return idxKey
}

// toolDiffEntryFor 返回某一轮改动的缓存；尚未加载时发起异步加载并返回加载中状态。
func (s *chatState) toolDiffEntryFor(threadID string, m Message) toolDiffEntry {
	if threadID == "" {
		return toolDiffEntry{err: "未选择会话"}
	}
	s.mu.Lock()
	if s.toolDiff.entries == nil {
		s.toolDiff.entries = make(map[string]*toolDiffEntry)
	}
	if s.toolDiff.turnKey == nil {
		s.toolDiff.turnKey = make(map[string]string)
	}
	key := s.toolDiffKeyLocked(threadID, m.TurnIndex)
	if m.TurnID != "" {
		key = threadID + "|" + m.TurnID
	}
	if e := s.toolDiff.entries[key]; e != nil {
		res := *e
		s.mu.Unlock()
		return res
	}
	idxKey := threadID + "|#" + strconv.Itoa(m.TurnIndex)
	delete(s.toolDiff.turnKey, idxKey)
	s.toolDiff.entries[key] = &toolDiffEntry{loading: true}
	generation := s.toolDiff.generation
	s.mu.Unlock()

	go s.loadToolDiff(threadID, m.TurnIndex, m.TurnID, generation)
	return toolDiffEntry{loading: true}
}

// loadToolDiff 优先按消息的 TurnID 取 diff；旧消息没有 ID 时才查询轮次摘要。
func (s *chatState) loadToolDiff(threadID string, turnIndex int, turnID string, generation uint64) {
	ctx := context.Background()
	idxKey := threadID + "|#" + strconv.Itoa(turnIndex)
	entry := &toolDiffEntry{}
	resolved := ""

	var all ThreadDiff
	var err error
	if turnID == "" {
		all, err = s.client.Diff(ctx, threadID, "")
		for _, t := range all.Turns {
			if t.Index == turnIndex {
				turnID = t.TurnID
				break
			}
		}
	}
	if err != nil {
		entry.err = err.Error()
	} else {
		switch {
		case turnID != "":
			data, err := s.client.Diff(ctx, threadID, turnID)
			resolved = threadID + "|" + turnID
			switch {
			case err != nil:
				entry.err = err.Error()
			case data.FailureReason != "":
				entry.failure = data.FailureReason
			default:
				if data.Diff != "" {
					files, parseErr := git.ParseUnifiedDiff([]byte(data.Diff))
					if parseErr != nil {
						entry.err = "解析 diff 失败: " + parseErr.Error()
					} else {
						entry.files = files
					}
				}
				entry.truncated = data.Truncated
			}
		case all.FailureReason != "":
			entry.failure = all.FailureReason
		default:
			// 轮次可能还没写入改动记录，记为失败以便重新展开时重试
			entry.err = "未找到该轮次的改动记录"
		}
	}

	s.mu.Lock()
	if s.toolDiff.generation != generation {
		s.mu.Unlock()
		return
	}
	if s.toolDiff.entries == nil {
		s.toolDiff.entries = make(map[string]*toolDiffEntry)
	}
	if s.toolDiff.turnKey == nil {
		s.toolDiff.turnKey = make(map[string]string)
	}
	if resolved != "" {
		s.toolDiff.entries[resolved] = entry
		s.toolDiff.turnKey[idxKey] = resolved
		delete(s.toolDiff.entries, idxKey)
	} else {
		s.toolDiff.entries[idxKey] = entry
	}
	s.mu.Unlock()
	s.invalidate()
}

// renderInlineToolDiff 在文件修改工具步骤下方渲染该步骤涉及文件的行级 diff。
func renderInlineToolDiff(c *ui.Context, s *chatState, m Message) {
	folds := toolDiffFoldSnapshot()
	var (
		data     toolDiffInlineData
		entry    toolDiffEntry
		embedded bool
	)
	if raw := toolEmbeddedDiff(m.Text); raw != "" {
		// 工具输出自带 unified diff 时直接解析，解析结果按消息与补丁内容缓存
		if d, ok := s.toolDiffDataFor(m, "embedded", folds, func() []git.FileDiff {
			files, err := toolDiffParse([]byte(raw))
			if err != nil {
				return nil
			}
			return files
		}); ok {
			data = d
			embedded = true
		}
	}
	if !embedded {
		s.mu.Lock()
		tid := s.selectedThreadID
		s.mu.Unlock()
		entry = s.toolDiffEntryFor(tid, m)
		if !entry.loading && entry.err == "" && entry.failure == "" && len(entry.files) > 0 {
			// 路径筛选与统计只算一次；来源带上轮次与文件数，重新加载后自动失效
			source := "turn:" + tid + "|" + strconv.Itoa(m.TurnIndex) + "|" + m.TurnID + "|" + strconv.Itoa(len(entry.files))
			files := entry.files
			if d, ok := s.toolDiffDataFor(m, source, folds, func() []git.FileDiff {
				return filterToolDiffFiles(files, toolEditedPaths(m))
			}); ok {
				data = d
			}
		}
	}

	ui.Column(c.Key("tool-diff-" + m.ID)).
		FillWidth().
		Gap(Space3).
		Children(func() {
			switch {
			case entry.loading:
				ui.Row(c.Key("tool-diff-loading-"+m.ID)).
					Gap(Space3).
					AlignItems(ui.Center).
					Padding(Space2, Space3).
					Children(func() {
						LoadingIndicator(c, nil, "正在读取改动…", IconSM)
					})
				return
			case entry.err != "":
				ui.Row(c.Key("tool-diff-error-"+m.ID)).
					Padding(Space3, Space3).
					Radius(RadiusMD).
					Background(ThemeDanger(c).Alpha(0.12)).
					Children(func() {
						ui.Text(c, "读取改动失败："+entry.err).
							FontSize(scaledFont(TypeFootnote)).
							TextColor(ThemeDanger(c)).
							Selectable()
					})
				return
			case entry.failure != "":
				ui.Row(c.Key("tool-diff-failure-"+m.ID)).
					Padding(Space3, Space3).
					Radius(RadiusMD).
					Background(ThemeHover(c)).
					Children(func() {
						ui.Text(c, entry.failure).
							FontSize(scaledFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					})
				return
			case len(data.files) == 0:
				ui.Text(c, "这一步没有可显示的改动").
					FontSize(scaledFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					Padding(Space2, Space3)
				return
			}

			for fileIdx, file := range data.files {
				idx := fileIdx
				stats := toolDiffFileStats{}
				if idx < len(data.stats) {
					stats = data.stats[idx]
				}
				renderInlineToolDiffFileData(c, s, m.ID, idx, file, stats, func(hunkIdx int) []toolDiffDisplayLine {
					return data.display[toolDiffHunkCacheKey(idx, hunkIdx)]
				})
			}
			if entry.truncated {
				ui.Text(c, "改动过多，已截断").
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeWarning(c))
			}
		})
}

// renderInlineToolDiffFile 按当前折叠状态现算统计与显示行，供未接入缓存的调用方使用。
func renderInlineToolDiffFile(c *ui.Context, s *chatState, msgID string, fileIdx int, file git.FileDiff) {
	folds := toolDiffFoldSnapshot()
	renderInlineToolDiffFileData(c, s, msgID, fileIdx, file, toolDiffFileStatsFor(file), func(hunkIdx int) []toolDiffDisplayLine {
		if hunkIdx >= len(file.Hunks) {
			return nil
		}
		hunkKey := fmt.Sprintf("%s-%d-%d", msgID, fileIdx, hunkIdx)
		return toolDiffDisplayLines(file.Hunks[hunkIdx].Lines, func(foldID int) bool {
			return folds[fmt.Sprintf("%s-fold-%d", hunkKey, foldID)]
		})
	})
}

// renderInlineToolDiffFileData 渲染单个文件：一行路径与 +N/-M，下面逐行显示 hunk，行样式与改动面板一致。
// stats 为预先算好的增删统计，hunkLines 返回某个 hunk 的显示行，两者由调用方从缓存取出。
func renderInlineToolDiffFileData(c *ui.Context, s *chatState, msgID string, fileIdx int, file git.FileDiff, stats toolDiffFileStats, hunkLines func(hunkIdx int) []toolDiffDisplayLine) {
	filePath := file.NewPath
	if file.Status == "deleted" || filePath == "" {
		filePath = file.OldPath
	}
	fileKey := fmt.Sprintf("%s-%d", msgID, fileIdx)

	adds, dels, total := stats.adds, stats.dels, stats.total

	ui.Column(c.Key("tool-diff-file-"+fileKey)).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Clip().
		FillWidth().
		Children(func() {
			ui.Row(c.Key("tool-diff-file-hdr-"+fileKey)).
				Gap(Space2).
				AlignItems(ui.Center).
				FillWidth().
				Padding(Space2, Space3).
				Children(func() {
					ui.Icon(c, IconFile()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					ui.Text(c, filePath).
						FontSize(scaledFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						SingleLine()
					ui.Text(c, fmt.Sprintf("+%d", adds)).
						FontSize(scaledFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemeSuccess(c))
					ui.Text(c, fmt.Sprintf("-%d", dels)).
						FontSize(scaledFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemeDanger(c))
				})

			if file.Binary {
				ui.Row(c.Key("tool-diff-binary-"+fileKey)).
					Padding(Space2, Space3).
					Children(func() {
						ui.Text(c, "二进制文件").
							FontSize(scaledFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					})
				return
			}

			ui.Column(c.Key("tool-diff-hunks-"+fileKey)).
				FillWidth().
				BorderWidth(Hairline, 0, 0, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					shown := 0
					for hunkIdx, hunk := range file.Hunks {
						if shown >= toolDiffMaxLines {
							break
						}
						hunkKey := fmt.Sprintf("%s-%d", fileKey, hunkIdx)

						ui.Row(c.Key("tool-diff-hunk-hdr-"+hunkKey)).
							FillWidth().
							Height(ControlMD).
							AlignItems(ui.Center).
							Padding(Space0, Space3).
							Background(ThemeHover(c)).
							Children(func() {
								ui.Text(c, hunk.Header).
									Font("monospace").
									FontSize(scaledFont(TypeCode)).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							})

						displayLines := hunkLines(hunkIdx)
						for lineIdx, disp := range displayLines {
							if shown >= toolDiffMaxLines {
								break
							}
							shown++

							if disp.Fold > 0 {
								foldKey := fmt.Sprintf("%s-fold-%d", hunkKey, disp.FoldID)
								foldText := fmt.Sprintf("⋯ %d 行未变", disp.Fold)
								foldRow := ui.Row(c.Key("tool-diff-fold-"+foldKey)).
									FillWidth().
									Padding(Hairline, Space3, Hairline, Space2).
									AlignItems(ui.Center).
									Cursor(ui.CursorPointer).
									Label(foldText).
									OnClick(func() {
										toggleToolDiffFold(c, s, foldKey)
									}).
									Children(func() {
										ui.Text(c, foldText).
											Font("monospace").
											FontSize(scaledFont(TypeCode)).
											TextColor(ThemeSecondaryText(c))
									})
								if foldRow.Hovered() {
									foldRow.Background(ThemeHover(c))
								}
								continue
							}

							line := disp.Line
							lineBg := ui.Transparent
							markColor := ThemeSecondaryText(c)
							markStr := " "
							switch line.Kind {
							case '+':
								lineBg = ThemeSuccess(c).Alpha(0.12)
								markColor = ThemeSuccess(c)
								markStr = "+"
							case '-':
								lineBg = ThemeDanger(c).Alpha(0.12)
								markColor = ThemeDanger(c)
								markStr = "-"
							}

							oldNoStr := ""
							if line.OldNo > 0 {
								oldNoStr = strconv.Itoa(line.OldNo)
							}
							newNoStr := ""
							if line.NewNo > 0 {
								newNoStr = strconv.Itoa(line.NewNo)
							}

							ui.Row(c.Key(fmt.Sprintf("tool-diff-line-%s-%d", hunkKey, lineIdx))).
								FillWidth().
								Background(lineBg).
								Padding(Hairline, Space3, Hairline, Space2).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, oldNoStr).
										Width(toolDiffLineNumberWidth).
										Font("monospace").
										FontSize(scaledFont(TypeCode)).
										SingleLine().
										TextColor(ThemeSecondaryText(c))
									ui.Text(c, newNoStr).
										Width(toolDiffLineNumberWidth).
										Font("monospace").
										FontSize(scaledFont(TypeCode)).
										SingleLine().
										TextColor(ThemeSecondaryText(c))
									ui.Text(c, markStr).
										Width(timelineDiffLineMarkWidth).
										Font("monospace").
										FontSize(scaledFont(TypeCode)).
										TextColor(markColor)
									ui.Text(c, line.Text).
										Font("monospace").
										FontSize(scaledFont(TypeCode)).
										TextColor(ThemePrimaryText(c)).
										Grow(1).
										Selectable()
								})
						}
					}

					if total > shown {
						ui.Row(c.Key("tool-diff-cut-"+fileKey)).
							FillWidth().
							Padding(Space1, Space3).
							Background(ThemeWarning(c).Alpha(0.12)).
							Children(func() {
								ui.Text(c, fmt.Sprintf("已截断，还有 %d 行未显示", total-shown)).
									FontSize(scaledFont(TypeCaption)).
									TextColor(ThemeWarning(c))
							})
					}
				})
		})
}
