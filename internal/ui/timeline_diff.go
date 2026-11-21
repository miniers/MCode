package mcodeui

import (
	"context"
	"fmt"
	"hash/maphash"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// toolDiffMaxLines 为内嵌 diff 中单个文件最多显示的行数，超出部分提示已截断。
const toolDiffMaxLines = 200

const timelineReviewViewportHeight float32 = 280

// toolDiffInlineCacheLimit 限制内嵌 diff 缓存的消息条数，防止长会话内存只增不减。
const toolDiffInlineCacheLimit = 32

// toolDiffParse 是内嵌 diff 的解析入口，测试可替换以统计解析次数。
var toolDiffParse = git.ParseUnifiedDiff

// toolDiffHashSeed 用于内嵌 diff 的内容指纹，避免每帧比较整段补丁正文。
var toolDiffHashSeed = maphash.MakeSeed()

// toolDiffState 保存文件修改工具步骤的内嵌 diff 状态，读写需持有 chatState.mu。
type toolDiffState struct {
	reviews    map[string]*toolDiffEntry
	reviewSeq  []string
	generation uint64
	open       map[string]bool                 // 按消息 ID 记录是否展开
	turnKey    map[string]string               // "threadID|#轮次序号" → "threadID|turnID"
	entries    map[string]*toolDiffEntry       // 按 "threadID|turnID" 缓存；轮次尚未解析出时按 "threadID|#轮次序号" 临时记录
	inline     map[string]*toolDiffInlineEntry // 按消息 ID 缓存 patch 解析结果
	inlineSeq  []string                        // 内嵌 diff 缓存的最近使用顺序，超出上限时淘汰最旧条目
	threads    []string                        // 最近访问的会话 ID（MRU），切换时据此保留改动缓存
}

// toolDiffInlineEntry 缓存一条消息的 patch 文件与增删统计；指纹变化时作废。
type toolDiffInlineEntry struct {
	fingerprint string
	files       []git.FileDiff
	stats       []toolDiffFileStats
}

// toolDiffFileStats 记录单个文件的增删与总行数，避免每帧重扫全部行。
type toolDiffFileStats struct {
	adds  int
	dels  int
	total int
}

// toolDiffInlineData 是一次渲染所需的缓存数据副本。
type toolDiffInlineData struct {
	files []git.FileDiff
	stats []toolDiffFileStats
}

// 改动缓存按“会话 + 轮次”保留，切换会话时不再整表丢弃：来回切换同一批会话时，
// 每次丢弃都会让可见的改动卡片重跑 git diff，子进程风暴与界面渲染抢 CPU。
// 实测一个 954 条消息、26 轮的会话整份 diff 正文只有 53KB，缓存按会话多留几个很便宜；
// 同时按估算字节数兜底，避免个别超大 diff 无限增长。
const (
	kToolDiffThreadCache = 8
	kToolDiffCacheBytes  = 32 << 20
)

// retainToolDiffThreadsLocked 记录本次访问的会话并裁掉更早会话的改动缓存。
// 在途加载按旧的 generation 已经作废，占位条目可以丢掉：留着会永远停在“正在读取改动”。
// 调用方需持有 s.mu，并自行递增 generation。
func (s *chatState) retainToolDiffThreadsLocked(threadID string) {
	if threadID == "" {
		return
	}
	keep := make([]string, 0, kToolDiffThreadCache)
	seen := make(map[string]bool, kToolDiffThreadCache)
	for _, tid := range append([]string{threadID}, s.toolDiff.threads...) {
		if tid == "" || seen[tid] {
			continue
		}
		seen[tid] = true
		keep = append(keep, tid)
		if len(keep) == kToolDiffThreadCache {
			break
		}
	}
	s.toolDiff.threads = keep
	allowed := func(key string) bool {
		tid, _, _ := strings.Cut(key, "|")
		return seen[tid]
	}
	trim := func(entries map[string]*toolDiffEntry) {
		for key, entry := range entries {
			if entry != nil && entry.loading || !allowed(key) {
				delete(entries, key)
			}
		}
	}
	trim(s.toolDiff.entries)
	trim(s.toolDiff.reviews)
	for key := range s.toolDiff.turnKey {
		if !allowed(key) {
			delete(s.toolDiff.turnKey, key)
		}
	}
	seq := s.toolDiff.reviewSeq[:0]
	for _, key := range s.toolDiff.reviewSeq {
		if allowed(key) {
			seq = append(seq, key)
		}
	}
	s.toolDiff.reviewSeq = seq
	// 字节兜底：从最久没访问的会话开始丢，当前会话不丢。
	for len(s.toolDiff.threads) > 1 && s.toolDiffCacheBytesLocked() > kToolDiffCacheBytes {
		drop := s.toolDiff.threads[len(s.toolDiff.threads)-1]
		s.toolDiff.threads = s.toolDiff.threads[:len(s.toolDiff.threads)-1]
		for key := range s.toolDiff.entries {
			if tid, _, _ := strings.Cut(key, "|"); tid == drop {
				delete(s.toolDiff.entries, key)
			}
		}
		for key := range s.toolDiff.reviews {
			if tid, _, _ := strings.Cut(key, "|"); tid == drop {
				delete(s.toolDiff.reviews, key)
			}
		}
		for key := range s.toolDiff.turnKey {
			if tid, _, _ := strings.Cut(key, "|"); tid == drop {
				delete(s.toolDiff.turnKey, key)
			}
		}
	}
}

// toolDiffCacheBytesLocked 估算当前保留的改动缓存字节数。调用方需持有 s.mu。
func (s *chatState) toolDiffCacheBytesLocked() int {
	total := 0
	for _, entry := range s.toolDiff.entries {
		if entry != nil {
			total += entry.bytes
		}
	}
	return total
}

// toolDiffEntryBytes 估算一轮改动缓存占用的字节数：diff 正文按解析后的行与结构放大两倍。
func toolDiffEntryBytes(diff string) int {
	return len(diff)*2 + 512
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
func (s *chatState) toolDiffDataFor(m Message, source string, load func() []git.FileDiff) (toolDiffInlineData, bool) {
	fingerprint := toolDiffFingerprint(source, m.Text, m.ToolInput)
	key := m.ID
	if key == "" {
		key = fingerprint
	}

	s.mu.Lock()
	if e := s.toolDiff.inline[key]; e != nil && e.fingerprint == fingerprint {
		s.touchToolDiffInlineLocked(key)
		data := toolDiffInlineData{files: e.files, stats: e.stats}
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
	data := toolDiffInlineData{files: entry.files, stats: entry.stats}
	s.mu.Unlock()
	return data, true
}

// toolDiffEntry 是某一轮改动的加载结果。
type toolDiffEntry struct {
	generation uint64
	snapshot   *ReviewSnapshot
	loading    bool
	// stale 表示这一轮的改动内容已经变了，缓存还能先显示，同时后台重取覆盖。
	stale     bool
	err       string
	failure   string
	files     []git.FileDiff
	truncated bool
	// bytes 是这条缓存占用的估算字节数：diff 正文长度乘 2（解析后的行与结构）。
	bytes int
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
		reviewKey := tid + "|#" + strconv.Itoa(m.TurnIndex)
		if m.TurnID != "" {
			reviewKey = tid + "|" + m.TurnID
		}
		if e := s.toolDiff.reviews[reviewKey]; e != nil && !e.loading && (e.err != "" || e.failure != "") {
			delete(s.toolDiff.reviews, reviewKey)
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
		// 过期的缓存继续给界面用，同时补一次后台重取：卡片不会因为重新拉取而短暂没有状态。
		revalidate := e.stale && !e.loading
		if revalidate {
			e.stale = false
		}
		generation := s.toolDiff.generation
		s.mu.Unlock()
		if revalidate {
			go s.loadToolDiff(threadID, m.TurnIndex, m.TurnID, generation)
		}
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

// invalidateToolDiffTurnsLocked 只把本帧消息涉及的那些轮次标记为待重取。
// 用量刷新、以及别的轮次里的工具调用都不该让一张已就绪的卡片丢掉状态。
// 调用方需持有 s.mu。
func (s *chatState) invalidateToolDiffTurnsLocked(threadID string, msgs []Message) {
	if threadID == "" || len(msgs) == 0 {
		return
	}
	for _, m := range msgs {
		if m.Role != "tool" && !m.IsFinal {
			continue
		}
		for _, key := range s.toolDiffKeysLocked(threadID, m) {
			if e := s.toolDiff.entries[key]; e != nil {
				e.stale = true
			}
		}
	}
}

// toolDiffKeysLocked 列出某一轮改动可能占用的缓存键：轮次序号键、轮次 ID 键，
// 以及从序号解析出来的真实键。调用方需持有 s.mu。
func (s *chatState) toolDiffKeysLocked(threadID string, m Message) []string {
	idxKey := threadID + "|#" + strconv.Itoa(m.TurnIndex)
	keys := []string{idxKey}
	if m.TurnID != "" {
		keys = append(keys, threadID+"|"+m.TurnID)
	}
	if k, ok := s.toolDiff.turnKey[idxKey]; ok {
		keys = append(keys, k)
	}
	return keys
}

// loadToolDiff 优先按消息的 TurnID 取 diff；旧消息没有 ID 时才查询轮次摘要。
func (s *chatState) loadToolDiff(threadID string, turnIndex int, turnID string, generation uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idxKey := threadID + "|#" + strconv.Itoa(turnIndex)
	entry := &toolDiffEntry{generation: generation}
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
				entry.bytes = toolDiffEntryBytes(data.Diff)
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
		// 在途期间若目标键或序号占位条目又被标了待重取，写回时保留 stale，让下一帧补拉最新 diff。
		if prev := s.toolDiff.entries[resolved]; prev != nil && prev.stale {
			entry.stale = true
		}
		if prev := s.toolDiff.entries[idxKey]; prev != nil && prev.stale {
			entry.stale = true
		}
		s.toolDiff.entries[resolved] = entry
		s.toolDiff.turnKey[idxKey] = resolved
		delete(s.toolDiff.entries, idxKey)
	} else {
		if prev := s.toolDiff.entries[idxKey]; prev != nil && prev.stale {
			entry.stale = true
		}
		s.toolDiff.entries[idxKey] = entry
	}
	s.mu.Unlock()
	s.invalidate()
}

// renderInlineToolDiff 在文件修改工具步骤下方渲染该步骤涉及文件的行级 diff。
func renderInlineToolDiff(c *ui.Context, s *chatState, m Message) {
	if _, ok := s.client.(reviewClient); ok && (m.TurnID != "" || toolEmbeddedDiff(m.Text) == "") {
		renderTimelineReview(c, s, m)
		return
	}
	var (
		data     toolDiffInlineData
		entry    toolDiffEntry
		embedded bool
	)
	if raw := toolEmbeddedDiff(m.Text); raw != "" {
		// 工具输出自带 unified diff 时直接解析，解析结果按消息与补丁内容缓存
		if d, ok := s.toolDiffDataFor(m, "embedded", func() []git.FileDiff {
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
			if d, ok := s.toolDiffDataFor(m, source, func() []git.FileDiff {
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
				renderInlineToolDiffFileData(c, s, m.ID, idx, file, stats, m.TurnID)
			}
			if entry.truncated {
				ui.Text(c, "改动过多，已截断").
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeWarning(c))
			}
		})
}

func (s *chatState) toolReviewEntryFor(tid string, m Message) toolDiffEntry {
	s.mu.Lock()
	key := tid + "|#" + strconv.Itoa(m.TurnIndex)
	if m.TurnID != "" {
		key = tid + "|" + m.TurnID
	}
	gen := s.toolDiff.generation
	if entry := s.toolDiff.reviews[key]; entry != nil && entry.generation == gen {
		result := *entry
		s.mu.Unlock()
		return result
	}
	if s.toolDiff.reviews == nil {
		s.toolDiff.reviews = map[string]*toolDiffEntry{}
	}
	pending := &toolDiffEntry{generation: gen, loading: true}
	s.toolDiff.reviews[key] = pending
	for i, old := range s.toolDiff.reviewSeq {
		if old == key {
			s.toolDiff.reviewSeq = append(s.toolDiff.reviewSeq[:i], s.toolDiff.reviewSeq[i+1:]...)
			break
		}
	}
	s.toolDiff.reviewSeq = append(s.toolDiff.reviewSeq, key)
	for len(s.toolDiff.reviewSeq) > toolDiffInlineCacheLimit {
		delete(s.toolDiff.reviews, s.toolDiff.reviewSeq[0])
		s.toolDiff.reviewSeq = s.toolDiff.reviewSeq[1:]
	}
	s.mu.Unlock()
	go s.loadToolReview(s.client.(reviewClient), tid, m.TurnIndex, m.TurnID, gen, pending)
	return toolDiffEntry{loading: true}
}

func (s *chatState) loadToolReview(client reviewClient, tid string, index int, turn string, generation uint64, pending ...*toolDiffEntry) {
	requestKey := tid + "|#" + strconv.Itoa(index)
	if turn != "" {
		requestKey = tid + "|" + turn
	}
	if turn == "" {
		s.mu.Lock()
		for _, m := range s.messages {
			if m.TurnIndex == index && m.TurnID != "" {
				turn = m.TurnID
				break
			}
		}
		s.mu.Unlock()
	}
	entry := &toolDiffEntry{generation: generation}
	if turn == "" {
		entry.failure = "该轮次的文件版本不可用"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := client.ReviewGet(ctx, tid, "turn", turn, "")
		if err != nil {
			entry.err = err.Error()
		} else {
			snapshot.Scope, snapshot.TurnID = "turn", turn
			entry.snapshot = &snapshot
			entry.failure = snapshot.Unavailable
		}
	}
	s.mu.Lock()
	if s.toolDiff.generation == generation && (len(pending) == 0 || s.toolDiff.reviews[requestKey] == pending[0]) {
		if s.toolDiff.reviews == nil {
			s.toolDiff.reviews = map[string]*toolDiffEntry{}
		}
		s.toolDiff.reviews[requestKey] = entry
	}
	s.mu.Unlock()
	s.invalidate()
}

// 工具摘要只在拿不到该轮完整版本时回退为 patch，绝不把上下文片段交给 DiffState。
func renderTimelineReview(c *ui.Context, s *chatState, m Message) {
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	entry := s.toolReviewEntryFor(tid, m)
	ui.Column(c.Key("timeline-review-" + m.ID)).FillWidth().Transition(motionCollapse(c)).Children(func() {
		if entry.loading {
			reviewBusy(c, "timeline-review-loading")
			return
		}
		if entry.snapshot == nil || entry.failure != "" || entry.err != "" {
			if raw := toolEmbeddedDiff(m.Text); raw != "" {
				data, ok := s.toolDiffDataFor(m, "embedded", func() []git.FileDiff { files, _ := toolDiffParse([]byte(raw)); return files })
				if ok {
					for i, file := range data.files {
						renderInlineToolDiffFileData(c, s, m.ID, i, file, data.stats[i], m.TurnID)
					}
					return
				}
			}
			reason := entry.failure
			if entry.err != "" {
				reason = entry.err
			}
			if reason == "" {
				reason = "文件版本不可用"
			}
			renderDiffStatusState(c, "timeline-review-unavailable", IconFile(), reason, "", nil)
			for _, path := range toolEditedPaths(m) {
				path := path
				Button(c, ButtonOpts{Key: "timeline-open-" + path, Text: "在改动中打开", Variant: ButtonSecondary, OnClick: func() { openReviewPath(s, m.TurnID, path) }})
			}
			return
		}
		snapshot := *entry.snapshot
		paths := toolEditedPaths(m)
		dp := getDiffPanelState(s)
		shown := false
		for _, file := range snapshot.Files {
			matched := len(paths) == 0
			for _, path := range paths {
				matched = matched || toolDiffPathMatch(file.Path, path) || toolDiffPathMatch(file.OrigPath, path)
			}
			if !matched {
				continue
			}
			file := file
			ui.Text(c, file.Path).FontSize(TypeFootnote).SingleLine()
			if !shown {
				shown = true
				ui.Column(c.Key("timeline-review-file-" + m.ID)).FillWidth().Height(timelineReviewViewportHeight).Children(func() { renderReviewFile(c, s, dp, tid, snapshot, file, false, 0, m.ID) })
			}
			Button(c, ButtonOpts{Key: "timeline-open-" + m.ID + file.Path, Text: "在改动中打开", Variant: ButtonSecondary, OnClick: func() { openReviewPath(s, m.TurnID, file.Path) }})
		}
		if !shown {
			ui.Text(c, "这一步没有可显示的改动").FontSize(TypeFootnote)
		}
	})
}

func openReviewPath(s *chatState, turn, path string) {
	inspectorSelect(s, sidePaneDiffID, false)
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	diffPanelMu.Lock()
	dp.scope = "turn"
	dp.pendingPath = path
	diffPanelMu.Unlock()
	s.loadDiff(tid, turn)
}

// renderInlineToolDiffFile 按当前折叠状态现算统计与显示行，供未接入缓存的调用方使用。
func renderInlineToolDiffFile(c *ui.Context, s *chatState, msgID string, fileIdx int, file git.FileDiff) {
	renderInlineToolDiffFileData(c, s, msgID, fileIdx, file, toolDiffFileStatsFor(file))
}

// renderInlineToolDiffFileData 渲染单个文件：一行路径与 +N/-M，下面逐行显示 hunk，行样式与改动面板一致。
// stats 为预先算好的增删统计，hunkLines 返回某个 hunk 的显示行，两者由调用方从缓存取出。
func renderInlineToolDiffFileData(c *ui.Context, s *chatState, msgID string, fileIdx int, file git.FileDiff, stats toolDiffFileStats, turn ...string) {
	filePath := file.NewPath
	if file.Status == "deleted" || filePath == "" {
		filePath = file.OldPath
	}
	fileKey := fmt.Sprintf("%s-%d", msgID, fileIdx)

	adds, dels := stats.adds, stats.dels

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

			renderPatchSummary(c, "tool-patch-"+fileKey, file)
			if s != nil {
				turnID := ""
				if len(turn) > 0 {
					turnID = turn[0]
				}
				Button(c, ButtonOpts{Key: "tool-open-review-" + fileKey, Text: "在改动中打开", Variant: ButtonSecondary, OnClick: func() { openReviewPath(s, turnID, filePath) }})
			}
		})
}
