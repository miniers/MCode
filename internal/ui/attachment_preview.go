package mcodeui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
)

const (
	attachPreviewModalWidth     float32 = 580
	attachPreviewModalHeight    float32 = 540
	attachPreviewModalMinWidth  float32 = 360
	attachPreviewModalMinHeight float32 = 280
	attachPreviewLineGutterW    float32 = 42
	attachPreviewLogContext     int     = 50
)

var attachPreviewRevealItem = openItemInFinder

// attachPreviewKind 表示附件分类预览弹窗的展示类别。
type attachPreviewKind int

const (
	attachPreviewBinary attachPreviewKind = iota
	attachPreviewImage
	attachPreviewCode
	attachPreviewMarkdown
	attachPreviewLog
	attachPreviewDoc
	attachPreviewArchive
	attachPreviewSnippet
)

type archiveTreeNode struct {
	Name     string
	Path     string
	IsDir    bool
	Size     int64
	Children []*archiveTreeNode
}

type archiveTreeRow struct {
	Name  string
	Path  string
	Depth int
	IsDir bool
	Size  int64
}

// attachPreviewItem 保存单个待预览附件的快照信息与后台加载结果，渲染期间不触发 IO。
type attachPreviewItem struct {
	Kind       attachPreviewKind
	Name       string
	MimeType   string
	Ext        string
	Size       int
	SourcePath string

	// 待发附件标识
	LocalID      string
	SnippetID    int
	InspectorRef *inspectorFileRef

	// 已发附件标识
	ThreadID  string
	MessageID string
	Media     MessageMedia
	ParsedRef *parsedTimelineInspectorRef
	ReadOnly  bool

	// 打开或后台加载时计算好的内容快照
	Text              string
	Lines             []string
	ErrorCount        int
	Truncated         bool
	RawBytes          []byte
	ImageSrc          ui.ImageSource
	SHA256            string
	SHA256Loading     bool
	Loading           bool
	LoadErr           string
	ArchiveEntries    []archiveEntry
	ArchiveTree       []*archiveTreeNode
	ArchiveTruncated  bool
	ArchiveLoaded     bool
	ArchiveSelected   map[string]bool
	ArchiveCollapsed  map[string]bool
	ArchiveRows       []archiveTreeRow
	ArchiveExtracting bool
}

// attachmentPreviewState 是附件分类预览弹窗的运行时状态。
type attachmentPreviewState struct {
	mu                  sync.Mutex
	open                bool
	readOnly            bool
	threadID            string
	generation          uint64
	items               []attachPreviewItem
	index               int
	selStart            int
	selEnd              int
	mdRawMode           bool
	mdEditText          string
	switchGen           uint64
	renderedSwitchGen   uint64
	mdSwitchGen         uint64
	renderedMdSwitchGen uint64
	codeList            ui.ListState
	archiveList         ui.ListState
	bodyScroll          ui.ScrollState
}

type attachPreviewSnapshot struct {
	open        bool
	readOnly    bool
	threadID    string
	items       []attachPreviewItem
	index       int
	selStart    int
	selEnd      int
	mdRawMode   bool
	mdEditText  string
	switchGen   uint64
	mdSwitchGen uint64
}

func (st *attachmentPreviewState) observeRenderedSwitchGen(gen uint64) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.renderedSwitchGen == gen {
		return false
	}
	st.renderedSwitchGen = gen
	return true
}

func (st *attachmentPreviewState) observeRenderedMdSwitchGen(gen uint64) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.renderedMdSwitchGen == gen {
		return false
	}
	st.renderedMdSwitchGen = gen
	return true
}

func (s *chatState) getOrCreateAttachmentPreviewState() *attachmentPreviewState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attachmentPreview == nil {
		s.attachmentPreview = &attachmentPreviewState{}
	}
	return s.attachmentPreview
}

// stripLineRangeSuffix 剥离文件名末尾的「:起始-结束」或「:行号」后缀，便于按原始扩展名分类。
func stripLineRangeSuffix(name string) string {
	idx := strings.LastIndexByte(name, ':')
	if idx <= 0 || idx == len(name)-1 {
		return name
	}
	tail := name[idx+1:]
	if dash := strings.IndexByte(tail, '-'); dash >= 0 {
		a, b := tail[:dash], tail[dash+1:]
		if isDigitsOnly(a) && isDigitsOnly(b) {
			return name[:idx]
		}
		return name
	}
	if isDigitsOnly(tail) {
		return name[:idx]
	}
	return name
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func attachmentDisplayExt(name string) string {
	clean := stripLineRangeSuffix(strings.TrimSpace(name))
	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(clean), "."))
	if ext == "" {
		return "FILE"
	}
	return ext
}

var attachPreviewDocExts = map[string]bool{
	".pdf":     true,
	".doc":     true,
	".docx":    true,
	".xls":     true,
	".xlsx":    true,
	".ppt":     true,
	".pptx":    true,
	".key":     true,
	".pages":   true,
	".numbers": true,
}

var attachPreviewBinaryExts = map[string]bool{
	".bin":   true,
	".zip":   true,
	".tar":   true,
	".gz":    true,
	".tgz":   true,
	".bz2":   true,
	".xz":    true,
	".7z":    true,
	".rar":   true,
	".exe":   true,
	".dll":   true,
	".so":    true,
	".dylib": true,
	".a":     true,
	".dmg":   true,
	".iso":   true,
	".apk":   true,
	".ipa":   true,
	".wasm":  true,
}

// looksLikeLogText 判断纯文本内容是否包含大量 ERROR/WARN 日志行。
func looksLikeLogText(text string) bool {
	if text == "" {
		return false
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 500 {
		lines = lines[:500]
	}
	nonEmpty := 0
	matches := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		nonEmpty++
		if strings.Contains(ln, "ERROR") || strings.Contains(ln, "WARN") {
			matches++
		}
	}
	if matches < 2 || nonEmpty == 0 {
		return false
	}
	return matches >= 3 || matches*100 >= nonEmpty*15
}

func countLogErrorLines(lines []string) int {
	n := 0
	for _, ln := range lines {
		if strings.Contains(ln, "ERROR") {
			n++
		}
	}
	return n
}

// classifyAttachPreviewKind 按扩展名、MIME 与文本特征判定附件预览分类。
func classifyAttachPreviewKind(name, mimeType, text string, isSnippet, hasInspectorRef bool) attachPreviewKind {
	if isSnippet {
		return attachPreviewSnippet
	}
	cleanName := stripLineRangeSuffix(strings.TrimSpace(name))
	if isArchiveName(cleanName) {
		return attachPreviewArchive
	}
	ext := strings.ToLower(filepath.Ext(cleanName))
	mimeLower := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))

	if isAllowedImageMime(mimeLower) || strings.HasPrefix(mimeLower, "image/") {
		return attachPreviewImage
	}
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".tiff" || ext == ".tif" {
		return attachPreviewImage
	}
	if mimeLower == "application/pdf" || attachPreviewDocExts[ext] {
		return attachPreviewDoc
	}
	if ext == ".md" || ext == ".markdown" || mimeLower == "text/markdown" {
		return attachPreviewMarkdown
	}
	if ext == ".log" {
		return attachPreviewLog
	}
	if attachPreviewBinaryExts[ext] || mimeLower == "application/octet-stream" || mimeLower == "application/zip" || mimeLower == "application/x-zip-compressed" {
		return attachPreviewBinary
	}
	if ext != "" && ext != ".txt" {
		if _, isCode := commonCodeAndTextExts[ext]; isCode {
			return attachPreviewCode
		}
	}
	if looksLikeLogText(text) {
		return attachPreviewLog
	}
	if hasInspectorRef || ext == ".txt" || isTextLikeMime(mimeLower) || text != "" {
		return attachPreviewCode
	}
	return attachPreviewBinary
}

// trimLinesRange 按 1-based 闭区间裁剪文本行范围，自动修正顺序并钳位边界。
func trimLinesRange(text string, first, last int) (string, int, int) {
	if text == "" {
		return "", 0, 0
	}
	lines := strings.Split(text, "\n")
	total := len(lines)
	if first > last {
		first, last = last, first
	}
	if first < 1 {
		first = 1
	}
	if last < first {
		last = first
	}
	if last > total {
		last = total
	}
	if first > total {
		first = total
	}
	return strings.Join(lines[first-1:last], "\n"), first, last
}

type lineInterval struct {
	start int
	end   int
}

// trimLogAroundErrors 仅保留含 ERROR 行前后各 contextLines 行的内容，合并重叠或相邻区间；
// 未找到 ERROR 行时返回原文与 false。
func trimLogAroundErrors(text string, contextLines int) (string, bool) {
	if text == "" {
		return "", false
	}
	if contextLines < 0 {
		contextLines = 0
	}
	lines := strings.Split(text, "\n")
	var intervals []lineInterval
	for i, ln := range lines {
		if !strings.Contains(ln, "ERROR") {
			continue
		}
		iv := lineInterval{
			start: max(0, i-contextLines),
			end:   min(len(lines)-1, i+contextLines),
		}
		if n := len(intervals); n > 0 && iv.start <= intervals[n-1].end+1 {
			if iv.end > intervals[n-1].end {
				intervals[n-1].end = iv.end
			}
		} else {
			intervals = append(intervals, iv)
		}
	}
	if len(intervals) == 0 {
		return text, false
	}
	var out []string
	for idx, iv := range intervals {
		if idx > 0 {
			omitted := iv.start - intervals[idx-1].end - 1
			out = append(out, fmt.Sprintf("... (已省略 %d 行) ...", omitted))
		}
		out = append(out, lines[iv.start:iv.end+1]...)
	}
	return strings.Join(out, "\n"), true
}

func formatTrimmedAttachmentName(name string, first, last int) string {
	base := stripLineRangeSuffix(strings.TrimSpace(name))
	if base == "" {
		base = "snippet.txt"
	}
	return fmt.Sprintf("%s:%d-%d", base, first, last)
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256HexFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func buildComposerPreviewItem(a composerAttachment) attachPreviewItem {
	text := a.PreviewText
	if text == "" && len(a.RetryData) > 0 && a.InspectorRef == nil && !isAllowedImageMime(a.MimeType) {
		if utf8.Valid(a.RetryData) {
			text = snapshotAttachmentPreviewText(a.Name, a.MimeType, a.RetryData, a.InspectorRef)
		}
	}
	kind := classifyAttachPreviewKind(a.Name, a.MimeType, text, false, a.InspectorRef != nil)
	if kind == attachPreviewCode && text == "" && len(a.RetryData) > 0 && utf8.Valid(a.RetryData) {
		sample := a.RetryData
		if len(sample) > composerPreviewTextLimit {
			sample = sample[:composerPreviewTextLimit]
		}
		if utf8.Valid(sample) {
			text = string(sample)
		}
	}
	var lines []string
	var errCount int
	if kind == attachPreviewCode || kind == attachPreviewLog || kind == attachPreviewMarkdown {
		if text != "" {
			lines = strings.Split(text, "\n")
			if kind == attachPreviewLog {
				errCount = countLogErrorLines(lines)
			}
		}
	}
	var imgSrc ui.ImageSource
	if kind == attachPreviewImage {
		if img := decodeAttachmentImage(a.MimeType, a.Data); img != nil {
			imgSrc = ui.NewBitmap(img)
		} else if a.Thumb != nil {
			imgSrc = a.Thumb
		}
	}
	var rawCopy []byte
	if kind == attachPreviewBinary && a.SourcePath == "" && len(a.RetryData) > 0 {
		rawCopy = append([]byte(nil), a.RetryData...)
	}
	return attachPreviewItem{
		Kind:         kind,
		Name:         a.Name,
		MimeType:     a.MimeType,
		Ext:          attachmentDisplayExt(a.Name),
		Size:         a.Size,
		SourcePath:   a.SourcePath,
		LocalID:      a.LocalID,
		InspectorRef: a.InspectorRef,
		Text:         text,
		Lines:        lines,
		ErrorCount:   errCount,
		Truncated:    a.Size > composerPreviewTextLimit,
		RawBytes:     rawCopy,
		ImageSrc:     imgSrc,
	}
}

func buildSnippetPreviewItem(sn composerSnippet) attachPreviewItem {
	title := snippetChipTitle(sn.Text)
	if title == "" {
		title = "长文本片段"
	}
	return attachPreviewItem{
		Kind:      attachPreviewSnippet,
		Name:      title,
		MimeType:  "text/plain",
		Ext:       "TEXT",
		Size:      len(sn.Text),
		SnippetID: sn.ID,
		Text:      sn.Text,
		Lines:     strings.Split(sn.Text, "\n"),
	}
}

// openAttachmentPreviewForComposer 打开待发附件（含长文本卡片）的分类预览弹窗。
func openAttachmentPreviewForComposer(s *chatState, focusLocalID string, focusSnippetID int) {
	st := s.getOrCreateAttachmentPreviewState()
	if st == nil {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	snippets := append([]composerSnippet(nil), s.snippets...)
	atts := append([]composerAttachment(nil), s.attachments...)
	s.mu.Unlock()

	items := make([]attachPreviewItem, 0, len(snippets)+len(atts))
	focusIdx := 0
	for _, sn := range snippets {
		if focusSnippetID != 0 && sn.ID == focusSnippetID {
			focusIdx = len(items)
		}
		items = append(items, buildSnippetPreviewItem(sn))
	}
	for _, a := range atts {
		if focusLocalID != "" && a.LocalID == focusLocalID {
			focusIdx = len(items)
		}
		items = append(items, buildComposerPreviewItem(a))
	}
	if len(items) == 0 {
		return
	}

	st.mu.Lock()
	st.generation++
	gen := st.generation
	st.open = true
	st.readOnly = false
	st.threadID = threadID
	st.items = items
	st.index = focusIdx
	st.selStart = 0
	st.selEnd = 0
	st.mdRawMode = false
	st.mdEditText = ""
	st.switchGen++
	st.codeList = ui.ListState{}
	st.archiveList = ui.ListState{}
	st.bodyScroll = ui.ScrollState{}
	st.mu.Unlock()

	ensureAttachmentPreviewItemReady(s, st, gen, focusIdx)
	s.invalidate()
}

// openTimelineAttachmentPreview 从已发送消息的文件胶囊打开只读分类预览弹窗。
func openTimelineAttachmentPreview(s *chatState, threadID string, m Message, focusIdx int) {
	st := s.getOrCreateAttachmentPreviewState()
	if st == nil || len(m.Media) == 0 {
		return
	}
	if focusIdx < 0 || focusIdx >= len(m.Media) {
		focusIdx = 0
	}

	items := make([]attachPreviewItem, 0, len(m.Media))
	for _, med := range m.Media {
		if isImageMedia(med) {
			name := med.Name
			if name == "" {
				name = "image"
			}
			items = append(items, attachPreviewItem{
				Kind:      attachPreviewImage,
				Name:      name,
				MimeType:  med.MimeType,
				Ext:       attachmentDisplayExt(name),
				ThreadID:  threadID,
				MessageID: m.ID,
				Media:     med,
				ReadOnly:  true,
			})
			continue
		}
		ref := cachedTimelineAttachmentRef(threadID, m.ID, med.ID)
		name := timelineFileCapsuleDisplayName(med, ref)
		kind := classifyAttachPreviewKind(name, med.MimeType, "", false, ref != nil && ref.ok)
		items = append(items, attachPreviewItem{
			Kind:      kind,
			Name:      name,
			MimeType:  med.MimeType,
			Ext:       attachmentDisplayExt(name),
			ThreadID:  threadID,
			MessageID: m.ID,
			Media:     med,
			ParsedRef: ref,
			ReadOnly:  true,
		})
	}

	st.mu.Lock()
	st.generation++
	gen := st.generation
	st.open = true
	st.readOnly = true
	st.threadID = threadID
	st.items = items
	st.index = focusIdx
	st.selStart = 0
	st.selEnd = 0
	st.mdRawMode = false
	st.mdEditText = ""
	st.switchGen++
	st.codeList = ui.ListState{}
	st.archiveList = ui.ListState{}
	st.bodyScroll = ui.ScrollState{}
	st.mu.Unlock()

	ensureAttachmentPreviewItemReady(s, st, gen, focusIdx)
	s.invalidate()
}

func closeAttachmentPreview(s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.attachmentPreview
	s.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	wasOpen := st.open
	st.open = false
	st.generation++
	st.items = nil
	st.index = 0
	st.selStart = 0
	st.selEnd = 0
	st.mdRawMode = false
	st.mdEditText = ""
	st.mu.Unlock()
	if wasOpen {
		s.invalidate()
	}
}

func stepAttachmentPreview(s *chatState, delta int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.attachmentPreview
	s.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	if !st.open || len(st.items) < 2 {
		st.mu.Unlock()
		return
	}
	n := len(st.items)
	st.index = (st.index + delta%n + n) % n
	st.selStart = 0
	st.selEnd = 0
	st.mdRawMode = false
	st.mdEditText = ""
	st.switchGen++
	st.codeList = ui.ListState{}
	st.archiveList = ui.ListState{}
	st.bodyScroll = ui.ScrollState{}
	gen := st.generation
	idx := st.index
	st.mu.Unlock()

	ensureAttachmentPreviewItemReady(s, st, gen, idx)
	s.invalidate()
}

func ensureAttachmentPreviewItemReady(s *chatState, st *attachmentPreviewState, gen uint64, idx int) {
	if s == nil || st == nil {
		return
	}
	st.mu.Lock()
	if !st.open || st.generation != gen || idx < 0 || idx >= len(st.items) {
		st.mu.Unlock()
		return
	}
	item := st.items[idx]
	if item.ReadOnly {
		if item.Kind == attachPreviewImage {
			st.mu.Unlock()
			prefetchMessageMedia(s.uiServices(), s, item.ThreadID, item.MessageID, item.Media.ID, "full")
			return
		}
		needLoad := !item.Loading && item.LoadErr == "" && item.Text == "" && item.SHA256 == "" && item.Size == 0 && !item.ArchiveLoaded
		if !needLoad {
			st.mu.Unlock()
			return
		}
		if _, ok := timelineMediaReader(s); !ok || item.ThreadID == "" || item.MessageID == "" || item.Media.ID == "" {
			if item.ParsedRef == nil || !item.ParsedRef.ok {
				st.items[idx].LoadErr = "当前客户端不支持读取附件内容"
			}
			st.mu.Unlock()
			return
		}
		st.items[idx].Loading = true
		st.mu.Unlock()
		go loadSentAttachmentPreview(s, st, gen, idx, item)
		return
	}

	if item.Kind == attachPreviewArchive && !item.Loading && !item.ArchiveLoaded && item.LoadErr == "" {
		if item.SourcePath == "" {
			st.items[idx].LoadErr = "无法读取压缩包内容"
			st.mu.Unlock()
			return
		}
		st.items[idx].Loading = true
		name := item.Name
		sourcePath := item.SourcePath
		st.mu.Unlock()
		go loadPendingArchivePreview(s, st, gen, idx, name, sourcePath)
		return
	}

	if item.Kind == attachPreviewBinary && item.SHA256 == "" && !item.SHA256Loading && (item.SourcePath != "" || len(item.RawBytes) > 0) {
		st.items[idx].SHA256Loading = true
		sourcePath := item.SourcePath
		raw := item.RawBytes
		st.mu.Unlock()
		go computeBinarySHA256Async(s, st, gen, idx, sourcePath, raw)
		return
	}
	st.mu.Unlock()
}

func loadPendingArchivePreview(s *chatState, st *attachmentPreviewState, gen uint64, idx int, name, sourcePath string) {
	f, err := os.Open(sourcePath)
	if err != nil {
		setAttachmentPreviewArchiveError(s, st, gen, idx, shortMediaError(err))
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		setAttachmentPreviewArchiveError(s, st, gen, idx, shortMediaError(err))
		return
	}

	entries, truncated, err := listArchiveEntries(name, f, info.Size())
	if err != nil {
		setAttachmentPreviewArchiveError(s, st, gen, idx, shortMediaError(err))
		return
	}

	tree := buildArchiveTree(entries)
	st.mu.Lock()
	if st.open && st.generation == gen && idx < len(st.items) {
		cur := &st.items[idx]
		cur.Loading = false
		cur.LoadErr = ""
		cur.ArchiveLoaded = true
		cur.ArchiveEntries = entries
		cur.ArchiveTree = tree
		cur.ArchiveTruncated = truncated
		cur.ArchiveRows = flattenArchiveTree(tree, cur.ArchiveCollapsed)
	}
	st.mu.Unlock()
	s.invalidate()
}

func setAttachmentPreviewArchiveError(s *chatState, st *attachmentPreviewState, gen uint64, idx int, reason string) {
	if reason == "" {
		reason = "无法读取压缩包内容"
	}
	st.mu.Lock()
	if st.open && st.generation == gen && idx < len(st.items) {
		st.items[idx].Loading = false
		st.items[idx].LoadErr = reason
	}
	st.mu.Unlock()
	s.invalidate()
}

func loadSentAttachmentPreview(s *chatState, st *attachmentPreviewState, gen uint64, idx int, item attachPreviewItem) {
	mimeType, data, err := readTimelineAttachmentFull(s, item.ThreadID, item.MessageID, item.Media.ID)
	if err != nil {
		st.mu.Lock()
		if st.open && st.generation == gen && idx < len(st.items) {
			st.items[idx].Loading = false
			st.items[idx].LoadErr = shortMediaError(err)
		}
		st.mu.Unlock()
		s.invalidate()
		return
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = item.Media.MimeType
	}
	var parsedRef *parsedTimelineInspectorRef
	if path, first, last, ok := parseInspectorRefContent(data); ok {
		ref := parsedTimelineInspectorRef{path: path, first: first, last: last, ok: true}
		if abs := fileManagerWorkspacePath(s, item.ThreadID, path); abs != "" {
			if info, statErr := os.Stat(abs); statErr == nil {
				ref.isDir = info.IsDir()
			}
		}
		parsedRef = &ref
	}

	displayName := timelineFileCapsuleDisplayName(item.Media, parsedRef)
	cleanDisplayName := stripLineRangeSuffix(displayName)
	var text string
	var truncated bool
	if len(data) > 0 && !attachPreviewDocExts[strings.ToLower(filepath.Ext(cleanDisplayName))] &&
		!attachPreviewBinaryExts[strings.ToLower(filepath.Ext(cleanDisplayName))] &&
		!isArchiveName(cleanDisplayName) &&
		mimeType != "application/pdf" {
		sample := data
		if len(sample) > composerPreviewTextLimit {
			truncated = true
			sample = sample[:composerPreviewTextLimit]
			for trimmed := 0; len(sample) > 0 && !utf8.Valid(sample); trimmed++ {
				if trimmed >= utf8.UTFMax {
					sample = nil
					break
				}
				sample = sample[:len(sample)-1]
			}
		}
		if len(sample) > 0 && utf8.Valid(sample) {
			text = string(sample)
		}
	}

	kind := classifyAttachPreviewKind(displayName, mimeType, text, false, parsedRef != nil && parsedRef.ok)
	var lines []string
	var errCount int
	if text != "" && (kind == attachPreviewCode || kind == attachPreviewLog || kind == attachPreviewMarkdown) {
		lines = strings.Split(text, "\n")
		if kind == attachPreviewLog {
			errCount = countLogErrorLines(lines)
		}
	}

	var archiveEntries []archiveEntry
	var archiveTree []*archiveTreeNode
	var archiveTruncated bool
	var archiveLoaded bool
	var archiveErr string
	if kind == attachPreviewArchive {
		if len(data) == 0 {
			archiveErr = "无法读取压缩包内容"
		} else if entries, trunc, listErr := listArchiveEntries(displayName, bytes.NewReader(data), int64(len(data))); listErr != nil {
			archiveErr = shortMediaError(listErr)
		} else {
			archiveEntries = entries
			archiveTree = buildArchiveTree(entries)
			archiveTruncated = trunc
			archiveLoaded = true
		}
	}
	sha := sha256HexBytes(data)

	st.mu.Lock()
	if st.open && st.generation == gen && idx < len(st.items) {
		cur := &st.items[idx]
		cur.Loading = false
		cur.LoadErr = archiveErr
		cur.Kind = kind
		cur.Name = displayName
		cur.MimeType = mimeType
		cur.Ext = attachmentDisplayExt(displayName)
		cur.Size = len(data)
		cur.ParsedRef = parsedRef
		cur.Text = text
		cur.Lines = lines
		cur.ErrorCount = errCount
		cur.Truncated = truncated
		cur.SHA256 = sha
		cur.SHA256Loading = false
		cur.ArchiveEntries = archiveEntries
		cur.ArchiveTree = archiveTree
		cur.ArchiveTruncated = archiveTruncated
		cur.ArchiveLoaded = archiveLoaded
		cur.ArchiveRows = flattenArchiveTree(archiveTree, cur.ArchiveCollapsed)
	}
	st.mu.Unlock()
	s.invalidate()
}

func computeBinarySHA256Async(s *chatState, st *attachmentPreviewState, gen uint64, idx int, sourcePath string, raw []byte) {
	var sha string
	if len(raw) > 0 {
		sha = sha256HexBytes(raw)
	} else if sourcePath != "" {
		if sum, err := sha256HexFile(sourcePath); err == nil {
			sha = sum
		}
	}
	st.mu.Lock()
	if st.open && st.generation == gen && idx < len(st.items) {
		st.items[idx].SHA256Loading = false
		st.items[idx].SHA256 = sha
		st.items[idx].RawBytes = nil
	}
	st.mu.Unlock()
	s.invalidate()
}

// replacePendingAttachmentWithText 用裁剪后的文本替换指定待发附件（沿用 removeAttachment 与 addAttachmentToThreadID）。
func replacePendingAttachmentWithText(s *chatState, oldLocalID, newName, mimeType, newText string) (string, bool) {
	if s == nil || oldLocalID == "" || newText == "" {
		return "", false
	}
	data := []byte(newText)
	if len(data) > maxAttachmentBytes {
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：超过单个 20MB 上限", newName))
		return "", false
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "text/plain"
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	if s.sending {
		s.mu.Unlock()
		return "", false
	}
	var oldAtt *composerAttachment
	for i := range s.attachments {
		if s.attachments[i].LocalID == oldLocalID {
			copyAtt := s.attachments[i]
			oldAtt = &copyAtt
			break
		}
	}
	s.mu.Unlock()
	if oldAtt == nil {
		return "", false
	}

	s.removeAttachment(oldLocalID)
	newID, ok := s.addAttachmentToThreadID(threadID, newName, mimeType, data, nil)
	if !ok {
		return "", false
	}
	return newID, true
}

func selectCodePreviewLine(s *chatState, st *attachmentPreviewState, lineNum int, shift bool) {
	if st == nil || lineNum < 1 {
		return
	}
	st.mu.Lock()
	if shift && st.selStart > 0 {
		st.selEnd = lineNum
	} else {
		st.selStart = lineNum
		st.selEnd = lineNum
	}
	st.mu.Unlock()
	if s != nil {
		s.invalidate()
	}
}

func snapshotAttachmentPreview(st *attachmentPreviewState, currentThreadID string) attachPreviewSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.open && st.threadID != currentThreadID {
		st.open = false
		st.generation++
		st.items = nil
		st.index = 0
		st.selStart = 0
		st.selEnd = 0
		st.mdRawMode = false
		st.mdEditText = ""
	}
	items := make([]attachPreviewItem, len(st.items))
	copy(items, st.items)
	for i := range items {
		if len(st.items[i].ArchiveSelected) > 0 {
			sel := make(map[string]bool, len(st.items[i].ArchiveSelected))
			for k, v := range st.items[i].ArchiveSelected {
				if v {
					sel[k] = true
				}
			}
			items[i].ArchiveSelected = sel
		}
		if len(st.items[i].ArchiveCollapsed) > 0 {
			col := make(map[string]bool, len(st.items[i].ArchiveCollapsed))
			for k, v := range st.items[i].ArchiveCollapsed {
				if v {
					col[k] = true
				}
			}
			items[i].ArchiveCollapsed = col
		}
	}
	return attachPreviewSnapshot{
		open:        st.open,
		readOnly:    st.readOnly,
		threadID:    st.threadID,
		items:       items,
		index:       st.index,
		selStart:    st.selStart,
		selEnd:      st.selEnd,
		mdRawMode:   st.mdRawMode,
		mdEditText:  st.mdEditText,
		switchGen:   st.switchGen,
		mdSwitchGen: st.mdSwitchGen,
	}
}

// renderAttachmentPreviewModal 渲染非图片附件分类预览弹窗，支持连续翻看与分类交互。
func renderAttachmentPreviewModal(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.attachmentPreview
	currentThreadID := s.selectedThreadID
	sending := s.sending
	s.mu.Unlock()
	if st == nil {
		return
	}

	snap := snapshotAttachmentPreview(st, currentThreadID)
	if !snap.open || len(snap.items) == 0 || snap.index < 0 || snap.index >= len(snap.items) {
		return
	}
	item := snap.items[snap.index]

	winW, winH := c.Size()
	cardW := min(attachPreviewModalWidth, max(attachPreviewModalMinWidth, winW-Space8))
	cardH := min(attachPreviewModalHeight, max(attachPreviewModalMinHeight, winH-Space8))

	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
		panel.MaxWidth(winW - Space8).MaxHeight(winH - Space8).Padding(0)

		if len(snap.items) > 1 && !c.FocusTakesText() {
			if panel.OverlayShortcut(0, ui.KeyLeft) {
				stepAttachmentPreview(s, -1)
			}
			if panel.OverlayShortcut(0, ui.KeyRight) {
				stepAttachmentPreview(s, 1)
			}
		}

		ui.Column(c.Key("attach-preview-modal")).
			Label("附件预览 "+item.Name).
			Width(cardW).
			Height(cardH).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c)).
			Radius(RadiusLG).
			Transition(motionDialog(c)).
			Clip().
			Children(func() {
				// 标题栏：名称、← / → 计数导航、关闭
				ui.Row(c.Key("attach-preview-head")).
					FillWidth().
					Padding(Space3, Space4).
					Gap(Space3).
					AlignItems(ui.Center).
					BorderWidth(0, 0, Hairline, 0).
					BorderColor(ThemeBorder(c)).
					Children(func() {
						ui.Text(c, item.Name).
							FontSize(uiFont(TypeTitle)).
							FontWeight(weightFor(c, 600)).
							TextColor(ThemePrimaryText(c)).
							SingleLine().
							Ellipsis("…").
							MinWidth(0).
							Grow(1)
						if len(snap.items) > 1 {
							ui.Row(c.Key("attach-preview-nav-group")).
								Gap(Space1).
								AlignItems(ui.Center).
								Shrink(0).
								Children(func() {
									Button(c, ButtonOpts{
										Key:     "attach-preview-prev",
										Text:    "←",
										Label:   "上一个",
										Tooltip: "上一个 (←)",
										Size:    ControlXS,
										Variant: ButtonGhost,
										OnClick: func() { stepAttachmentPreview(s, -1) },
									})
									ui.Text(c, fmt.Sprintf("%d / %d", snap.index+1, len(snap.items))).
										FontSize(uiFont(TypeCaption)).
										TextColor(ThemeSecondaryText(c)).
										SingleLine()
									Button(c, ButtonOpts{
										Key:     "attach-preview-next",
										Text:    "→",
										Label:   "下一个",
										Tooltip: "下一个 (→)",
										Size:    ControlXS,
										Variant: ButtonGhost,
										OnClick: func() { stepAttachmentPreview(s, 1) },
									})
								})
						}
						IconButton(c, IconButtonProps{
							Key:     "attach-preview-close-x",
							Icon:    IconX(),
							Label:   "关闭预览",
							Tooltip: "关闭",
							Size:    ControlSM,
							Variant: ButtonGhost,
							OnClick: func() { closeAttachmentPreview(s) },
						})
					})

				if item.Loading {
					ui.Column(c.Key("attach-preview-loading")).
						Grow(1).
						MinHeight(0).
						FillWidth().
						Center().
						Gap(Space2).
						Children(func() {
							renderAttachmentSpinner(c, "attach-preview-spinner", IconMD, ThemeAccent(c), "读取附件中")
							ui.Text(c, "正在读取附件内容…").
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c))
						})
					renderAttachPreviewFooter(c, s, nil)
					return
				}

				if item.LoadErr != "" {
					ui.Column(c.Key("attach-preview-error")).
						Grow(1).
						MinHeight(0).
						FillWidth().
						Padding(Space4).
						Center().
						Gap(Space2).
						Children(func() {
							ui.Text(c, "读取附件失败："+item.LoadErr).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeDanger(c))
						})
					renderAttachPreviewFooter(c, s, nil)
					return
				}

				renderAttachmentPreviewByKind(c, s, st, snap, item, sending)
			})
	})
	if !open {
		closeAttachmentPreview(s)
	}
}

// renderAttachmentPreviewByKind 按附件分类渲染弹窗正文与底栏操作按钮。
// 后续新增分类（如压缩包清单）在此函数内扩展分支。
func renderAttachmentPreviewByKind(c *ui.Context, s *chatState, st *attachmentPreviewState, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	bodyWrap := ui.Column(c.Key("attach-preview-body")).
		Grow(1).
		MinHeight(0).
		FillWidth().
		Padding(Space4).
		Gap(Space3)

	switched := st.observeRenderedSwitchGen(snap.switchGen)
	var op float32
	if reduceMotion(c) {
		op = bodyWrap.Jump("attach-preview-switch", 1)
	} else {
		if switched {
			bodyWrap.Jump("attach-preview-switch", 0)
		}
		op = bodyWrap.AnimateTo("attach-preview-switch", 1, ui.Animation{Duration: DurFast, Ease: EaseOut})
	}
	bodyWrap.Opacity(op).Translate(0, (1-op)*motionRiseOffset)

	switch item.Kind {
	case attachPreviewImage:
		renderAttachPreviewImage(c, s, bodyWrap, item)
	case attachPreviewCode:
		renderAttachPreviewCode(c, s, st, bodyWrap, snap, item, sending)
	case attachPreviewMarkdown:
		renderAttachPreviewMarkdown(c, s, st, bodyWrap, snap, item, sending)
	case attachPreviewLog:
		renderAttachPreviewLog(c, s, st, bodyWrap, snap, item, sending)
	case attachPreviewSnippet:
		renderAttachPreviewSnippet(c, s, st, bodyWrap, snap, item, sending)
	case attachPreviewDoc:
		renderAttachPreviewDoc(c, s, bodyWrap, snap, item)
	case attachPreviewArchive:
		renderAttachPreviewArchive(c, s, st, bodyWrap, snap, item, sending)
	default:
		renderAttachPreviewBinary(c, s, bodyWrap, snap, item)
	}
}

func renderAttachPreviewFooter(c *ui.Context, s *chatState, leftActions func()) {
	ui.Row(c.Key("attach-preview-foot")).
		FillWidth().
		Padding(Space3, Space4).
		Gap(Space2).
		AlignItems(ui.Center).
		Justify(ui.End).
		BorderWidth(Hairline, 0, 0, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			if leftActions != nil {
				leftActions()
			}
			Button(c, ButtonOpts{
				Key:     "attach-preview-foot-close",
				Text:    "关闭",
				Label:   "关闭",
				Size:    ControlSM,
				Variant: ButtonSecondary,
				OnClick: func() { closeAttachmentPreview(s) },
			})
		})
}

func attachPreviewSizeLabel(item attachPreviewItem) string {
	if item.Size > 0 {
		return formatFileSize(int64(item.Size))
	}
	if item.Text != "" {
		return formatFileSize(int64(len(item.Text)))
	}
	return "未知大小"
}

func renderAttachPreviewInspectorOpenButton(c *ui.Context, s *chatState, item attachPreviewItem, threadID string) {
	if item.InspectorRef != nil {
		ref := *item.InspectorRef
		tid := ref.ThreadID
		if tid == "" {
			tid = threadID
		}
		Button(c, ButtonOpts{
			Key:     "attach-preview-open-split",
			Text:    "在右侧打开",
			Label:   "在右侧打开",
			Size:    ControlSM,
			Variant: ButtonSecondary,
			OnClick: func() {
				openTimelineInspectorRef(s, tid, parsedTimelineInspectorRef{
					path:  ref.Path,
					first: ref.First,
					last:  ref.Last,
					ok:    true,
				})
				closeAttachmentPreview(s)
			},
		})
		return
	}
	if item.ParsedRef != nil && item.ParsedRef.ok {
		ref := *item.ParsedRef
		Button(c, ButtonOpts{
			Key:     "attach-preview-open-split",
			Text:    "在右侧打开",
			Label:   "在右侧打开",
			Size:    ControlSM,
			Variant: ButtonSecondary,
			OnClick: func() {
				openTimelineInspectorRef(s, threadID, ref)
				closeAttachmentPreview(s)
			},
		})
	}
}

func renderAttachPreviewImage(c *ui.Context, s *chatState, bodyWrap ui.Element, item attachPreviewItem) {
	src := item.ImageSrc
	var loadErr error
	if src == nil && item.ReadOnly && item.ThreadID != "" && item.MessageID != "" && item.Media.ID != "" {
		if full := cachedMessageMedia(c, s, item.ThreadID, item.MessageID, item.Media.ID, "full"); full != nil {
			src = full
		} else if thumb := cachedMessageMedia(c, s, item.ThreadID, item.MessageID, item.Media.ID, "thumb"); thumb != nil {
			src = thumb
		} else {
			state, errText := messageMediaStatus(item.ThreadID, item.MessageID, item.Media.ID, "full")
			if state == messageMediaFailed {
				loadErr = fmt.Errorf("%s", errText)
			}
		}
	}

	bodyWrap.Children(func() {
		stage := ui.Column(c.Key("attach-preview-img-stage")).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Padding(Space3).
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			Center()
		stage.Children(func() {
			if src != nil {
				dispW, dispH := previewFitSize(460, 340, src)
				ui.Image(c.Key("attach-preview-img"), src).
					Width(dispW).
					Height(dispH).
					Radius(RadiusSM)
				return
			}
			if loadErr != nil {
				ui.Text(c, "图片加载失败").FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				return
			}
			renderAttachmentSpinner(c, "attach-preview-img-spinner", IconMD, ThemeAccent(c), "加载图片中")
		})
	})
	renderAttachPreviewFooter(c, s, nil)
}

func attachPreviewCodeFontForState(s *chatState) (string, float32) {
	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	return codeFontFamily(appCfg), codeFontSize()
}

func renderAttachPreviewCode(c *ui.Context, s *chatState, st *attachmentPreviewState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	fontFam, codeSz := attachPreviewCodeFontForState(s)
	hasSel := snap.selStart > 0 && snap.selEnd > 0
	lo, hi := snap.selStart, snap.selEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	lines := item.Lines
	if len(lines) == 0 && item.Text != "" {
		lines = strings.Split(item.Text, "\n")
	}

	bodyWrap.Children(func() {
		ui.Row(c.Key("attach-preview-toolbar")).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				hint := fmt.Sprintf("%s · %s", item.Ext, attachPreviewSizeLabel(item))
				if !snap.readOnly && len(lines) > 0 {
					hint += " · 点击行号选起始行，Shift+点击选结束行"
				}
				ui.Text(c, hint).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Ellipsis("…").
					MinWidth(0).
					Grow(1)
				renderAttachPreviewInspectorOpenButton(c, s, item, snap.threadID)
			})

		viewer := ui.Column(c.Key("attach-preview-code-viewer")).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			Padding(Space1, 0).
			Clip()
		viewer.Children(func() {
			if len(lines) == 0 {
				ui.Box(c.Key("attach-preview-code-empty")).Padding(Space3).Children(func() {
					ui.Text(c, "暂无可显示文本内容").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
				})
				return
			}
			st.codeList.Key = func(i int) any { return i }
			ui.List(c.Key(fmt.Sprintf("attach-preview-code-list-%d", snap.index)), &st.codeList, len(lines), func(i int) {
				lineNum := i + 1
				lineText := lines[i]
				if lineText == "" {
					lineText = " "
				}
				selected := hasSel && lineNum >= lo && lineNum <= hi
				row := ui.Row(c.Key(fmt.Sprintf("attach-preview-code-row-%d-%d", snap.index, lineNum))).
					FillWidth().
					Padding(Hairline, Space3, Hairline, Space2).
					Gap(Space3).
					AlignItems(ui.Start)
				if selected {
					row.Background(ThemeAccentSubtle(c))
				}
				row.Children(func() {
					var lnBtn ui.Element
					lnBtn = ui.ButtonBase(c.Key(fmt.Sprintf("attach-preview-ln-%d-%d", snap.index, lineNum))).
						Label(fmt.Sprintf("选择第 %d 行", lineNum)).
						MinWidth(attachPreviewLineGutterW).
						Padding(0, Space1).
						Justify(ui.End).
						Cursor(ui.CursorPointer).
						Background(ui.Transparent).
						OnClick(func() {
							shift := (lnBtn.ClickModifiers()|c.Modifiers())&ui.Shift != 0
							selectCodePreviewLine(s, st, lineNum, shift)
						})
					lnCol := ThemeTertiaryText(c)
					if selected {
						lnCol = ThemeAccent(c)
					}
					lnBtn.Children(func() {
						ui.Text(c, strconv.Itoa(lineNum)).
							Font(fontFam).
							FontSize(uiFont(codeSz)).
							TextColor(lnCol).
							SingleLine()
					})
					ui.Text(c, lineText).
						Font(fontFam).
						FontSize(uiFont(codeSz)).
						LineHeight(1.5).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						MinWidth(0)
				})
			}).FillWidth().FillHeight().MinHeight(0)
		})
	})

	renderAttachPreviewFooter(c, s, func() {
		if !snap.readOnly && hasSel && len(lines) > 0 {
			Button(c, ButtonOpts{
				Key:      "attach-preview-trim-lines",
				Text:     fmt.Sprintf("只附带第 %d–%d 行", lo, hi),
				Label:    fmt.Sprintf("只附带第 %d–%d 行", lo, hi),
				Size:     ControlSM,
				Variant:  ButtonPrimary,
				Disabled: sending,
				OnClick: func() {
					trimmed, first, last := trimLinesRange(item.Text, lo, hi)
					if trimmed == "" {
						return
					}
					newName := formatTrimmedAttachmentName(item.Name, first, last)
					if _, ok := replacePendingAttachmentWithText(s, item.LocalID, newName, item.MimeType, trimmed); ok {
						s.setToast("已裁剪为 " + newName)
						closeAttachmentPreview(s)
					}
				},
			})
		}
	})
}

func renderAttachPreviewMarkdown(c *ui.Context, s *chatState, st *attachmentPreviewState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	fontFam, codeSz := attachPreviewCodeFontForState(s)
	editable := !snap.readOnly && !item.Truncated && item.LocalID != "" && !sending

	toggleLabel := "查看原文"
	if snap.mdRawMode {
		toggleLabel = "切回排版预览"
	} else if editable {
		toggleLabel = "查看 / 编辑原文"
	}

	bodyWrap.Children(func() {
		ui.Row(c.Key("attach-preview-md-toolbar")).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, fmt.Sprintf("Markdown · %s", attachPreviewSizeLabel(item))).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Grow(1).
					MinWidth(0)
				renderAttachPreviewInspectorOpenButton(c, s, item, snap.threadID)
				Button(c, ButtonOpts{
					Key:     "attach-preview-md-toggle",
					Text:    toggleLabel,
					Label:   toggleLabel,
					Size:    ControlSM,
					Variant: ButtonSecondary,
					OnClick: func() {
						st.mu.Lock()
						st.mdRawMode = !st.mdRawMode
						if st.mdRawMode {
							st.mdEditText = item.Text
						}
						st.mdSwitchGen++
						st.mu.Unlock()
						s.invalidate()
					},
				})
			})

		mdPanel := ui.Column(c.Key("attach-preview-md-panel")).
			Grow(1).
			MinHeight(0).
			FillWidth()
		mdToggled := st.observeRenderedMdSwitchGen(snap.mdSwitchGen)
		var mdOp float32
		if reduceMotion(c) {
			mdOp = mdPanel.Jump("attach-preview-md-fade", 1)
		} else {
			if mdToggled {
				mdPanel.Jump("attach-preview-md-fade", 0)
			}
			mdOp = mdPanel.AnimateTo("attach-preview-md-fade", 1, ui.Animation{Duration: DurFast, Ease: EaseOut})
		}
		mdPanel.Opacity(mdOp)

		mdPanel.Children(func() {
			if !snap.mdRawMode {
				ensureChatMarkdown(c, s)
				ui.Scroll(c.Key(fmt.Sprintf("attach-preview-md-scroll-%d", snap.index))).
					Grow(1).
					MinHeight(0).
					FillWidth().
					Padding(Space3, Space4).
					Radius(RadiusMD).
					Background(ThemeSurfaceMain(c)).
					Border(Hairline, ThemeHairline(c)).
					TrackScroll(&st.bodyScroll).
					Children(func() {
						if item.Text == "" {
							ui.Text(c, "暂无 Markdown 内容").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
							return
						}
						markdown.View(c.Key(fmt.Sprintf("attach-preview-md-view-%d-%d", snap.switchGen, snap.index)), item.Text, &s.mdStaticOpts)
					})
				return
			}

			if editable {
				draft := snap.mdEditText
				ui.TextArea(c.Key(fmt.Sprintf("attach-preview-md-editor-%d", snap.index)), &draft).
					Label("编辑 Markdown 原文").
					Grow(1).
					MinHeight(0).
					FillWidth().
					Font(fontFam).
					FontSize(uiFont(codeSz)).
					TextColor(ThemePrimaryText(c)).
					Background(ThemeSurfaceMain(c)).
					Border(Hairline, ThemeBorder(c)).
					Radius(RadiusMD).
					Padding(Space3).
					OnChange(func() {
						st.mu.Lock()
						st.mdEditText = draft
						st.mu.Unlock()
					})
				return
			}

			ui.Scroll(c.Key(fmt.Sprintf("attach-preview-md-raw-scroll-%d", snap.index))).
				Grow(1).
				MinHeight(0).
				FillWidth().
				Padding(Space3).
				Radius(RadiusMD).
				Background(ThemeSurfaceMain(c)).
				Border(Hairline, ThemeHairline(c)).
				TrackScroll(&st.bodyScroll).
				Children(func() {
					ui.Text(c, item.Text).
						FillWidth().
						Font(fontFam).
						FontSize(uiFont(codeSz)).
						LineHeight(1.5).
						TextColor(ThemePrimaryText(c)).
						Selectable()
				})
		})
	})

	renderAttachPreviewFooter(c, s, func() {
		if snap.mdRawMode && editable {
			Button(c, ButtonOpts{
				Key:      "attach-preview-md-save",
				Text:     "保存",
				Label:    "保存修改",
				Size:     ControlSM,
				Variant:  ButtonPrimary,
				Disabled: sending || len(snap.mdEditText) == 0 || snap.mdEditText == item.Text,
				OnClick: func() {
					st.mu.Lock()
					newText := st.mdEditText
					idx := st.index
					st.mu.Unlock()
					if newText == "" {
						return
					}
					if s.updateTextAttachment(item.LocalID, newText) {
						st.mu.Lock()
						if idx >= 0 && idx < len(st.items) {
							st.items[idx].Text = newText
							st.items[idx].Lines = strings.Split(newText, "\n")
							st.items[idx].Size = len(newText)
						}
						st.mdRawMode = false
						st.mdSwitchGen++
						st.mu.Unlock()
						s.setToast("已更新 " + item.Name)
					}
				},
			})
		}
	})
}

func renderAttachPreviewLog(c *ui.Context, s *chatState, st *attachmentPreviewState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	fontFam, codeSz := attachPreviewCodeFontForState(s)
	lines := item.Lines
	if len(lines) == 0 && item.Text != "" {
		lines = strings.Split(item.Text, "\n")
	}

	bodyWrap.Children(func() {
		ui.Row(c.Key("attach-preview-log-toolbar")).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, fmt.Sprintf("日志 · %d 处 ERROR", item.ErrorCount)).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Grow(1).
					MinWidth(0)
				renderAttachPreviewInspectorOpenButton(c, s, item, snap.threadID)
			})

		viewer := ui.Column(c.Key("attach-preview-log-viewer")).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			Padding(Space1, 0).
			Clip()
		viewer.Children(func() {
			if len(lines) == 0 {
				ui.Box(c.Key("attach-preview-log-empty")).Padding(Space3).Children(func() {
					ui.Text(c, "日志内容为空").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
				})
				return
			}
			st.codeList.Key = func(i int) any { return i }
			ui.List(c.Key(fmt.Sprintf("attach-preview-log-list-%d", snap.index)), &st.codeList, len(lines), func(i int) {
				lineNum := i + 1
				lineText := lines[i]
				if lineText == "" {
					lineText = " "
				}
				isErr := strings.Contains(lineText, "ERROR")
				row := ui.Row(c.Key(fmt.Sprintf("attach-preview-log-row-%d-%d", snap.index, lineNum))).
					FillWidth().
					Padding(Hairline, Space3, Hairline, Space2).
					Gap(Space3).
					AlignItems(ui.Start)
				if isErr {
					row.Background(ThemeDanger(c).Alpha(0.12))
				}
				row.Children(func() {
					lnColor := ThemeTertiaryText(c)
					txtColor := ThemePrimaryText(c)
					lnWeight := 400
					if isErr {
						lnColor = ThemeDanger(c)
						txtColor = ThemeDanger(c)
						lnWeight = weightFor(c, 600)
					}
					ui.Box(c.Key(fmt.Sprintf("attach-preview-log-ln-%d-%d", snap.index, lineNum))).
						MinWidth(attachPreviewLineGutterW).
						Padding(0, Space1).
						Justify(ui.End).
						Children(func() {
							ui.Text(c, strconv.Itoa(lineNum)).
								Font(fontFam).
								FontSize(uiFont(codeSz)).
								FontWeight(lnWeight).
								TextColor(lnColor).
								SingleLine()
						})
					ui.Text(c, lineText).
						Font(fontFam).
						FontSize(uiFont(codeSz)).
						LineHeight(1.5).
						TextColor(txtColor).
						Grow(1).
						MinWidth(0)
				})
			}).FillWidth().FillHeight().MinHeight(0)
		})
	})

	renderAttachPreviewFooter(c, s, func() {
		if !snap.readOnly && item.ErrorCount > 0 {
			Button(c, ButtonOpts{
				Key:      "attach-preview-keep-err",
				Text:     "只留报错前后 50 行",
				Label:    "只留报错前后 50 行",
				Size:     ControlSM,
				Variant:  ButtonPrimary,
				Disabled: sending,
				OnClick: func() {
					trimmed, matched := trimLogAroundErrors(item.Text, attachPreviewLogContext)
					if !matched || trimmed == "" {
						return
					}
					newID, ok := replacePendingAttachmentWithText(s, item.LocalID, item.Name, item.MimeType, trimmed)
					if !ok {
						return
					}
					newLines := strings.Split(trimmed, "\n")
					errCnt := countLogErrorLines(newLines)
					st.mu.Lock()
					if snap.index >= 0 && snap.index < len(st.items) {
						st.items[snap.index].LocalID = newID
						st.items[snap.index].Text = trimmed
						st.items[snap.index].Lines = newLines
						st.items[snap.index].ErrorCount = errCnt
						st.items[snap.index].Size = len(trimmed)
						st.items[snap.index].SourcePath = ""
						st.codeList = ui.ListState{}
					}
					st.mu.Unlock()
					s.setToast("已裁剪日志，仅保留报错上下文")
					s.invalidate()
				},
			})
		}
	})
}

func renderAttachPreviewSnippet(c *ui.Context, s *chatState, st *attachmentPreviewState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	fontFam, codeSz := attachPreviewCodeFontForState(s)

	bodyWrap.Children(func() {
		ui.Text(c, fmt.Sprintf("长文本片段 · %s", formatContextTextStats(item.Text))).
			FontSize(uiFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()

		ui.Scroll(c.Key(fmt.Sprintf("attach-preview-snippet-scroll-%d", snap.index))).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Padding(Space3).
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			TrackScroll(&st.bodyScroll).
			Children(func() {
				ui.Text(c, item.Text).
					FillWidth().
					Font(fontFam).
					FontSize(uiFont(codeSz)).
					LineHeight(1.5).
					TextColor(ThemePrimaryText(c)).
					Selectable()
			})
	})

	renderAttachPreviewFooter(c, s, func() {
		if !snap.readOnly && item.SnippetID != 0 {
			Button(c, ButtonOpts{
				Key:      "attach-preview-insert-snippet",
				Text:     "放入输入框",
				Label:    "放入输入框",
				Size:     ControlSM,
				Variant:  ButtonPrimary,
				Disabled: sending,
				OnClick: func() {
					s.expandSnippet(item.SnippetID)
					closeAttachmentPreview(s)
				},
			})
		}
	})
}

func attachPreviewCanOpenSent(s *chatState, item attachPreviewItem) bool {
	if !item.ReadOnly || item.ThreadID == "" || item.MessageID == "" || item.Media.ID == "" {
		return false
	}
	_, ok := timelineMediaReader(s)
	return ok
}

func attachPreviewOpenDocumentAction(s *chatState, item attachPreviewItem, fn func(string) error) {
	if item.ReadOnly {
		openTimelineAttachmentTemp(s, item.ThreadID, item.MessageID, item.Media, fn)
		return
	}
	if item.SourcePath == "" {
		return
	}
	_ = fn(item.SourcePath)
}

func renderAttachPreviewDoc(c *ui.Context, s *chatState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem) {
	canOpen := item.SourcePath != ""
	if snap.readOnly {
		canOpen = attachPreviewCanOpenSent(s, item)
	}

	bodyWrap.Children(func() {
		ui.Column(c.Key("attach-preview-doc-card")).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Padding(Space5).
			Gap(Space3).
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeBorder(c)).
			Center().
			Children(func() {
				cat := categoryFile
				if item.Ext == "PDF" {
					cat = categoryPDF
				}
				renderAttachmentExtBadge(c, "attach-preview-doc-badge", cat, item.Ext, categoryColor(c, cat))
				ui.Text(c, item.Name).
					FontSize(uiFont(TypeBody)).
					FontWeight(weightFor(c, 500)).
					TextColor(ThemePrimaryText(c)).
					SingleLine().
					Ellipsis("…")
				ui.Text(c, attachPreviewSizeLabel(item)).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()

				hint := "使用系统默认应用打开此文档"
				if fileManagerGOOS == "darwin" {
					hint = "macOS 调用原生快速查看预览页面内容"
				}
				if !canOpen {
					hint = "当前附件缺少本地文件路径，无法调用外部预览"
				}
				ui.Text(c, hint).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c))

				ui.Row(c.Key("attach-preview-doc-actions")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						Button(c, ButtonOpts{
							Key:      "attach-preview-quick-look",
							Text:     "快速查看",
							Label:    "快速查看",
							Size:     ControlSM,
							Variant:  ButtonPrimary,
							Disabled: !canOpen,
							OnClick: func() {
								attachPreviewOpenDocumentAction(s, item, fileManagerPreviewItem)
							},
						})
						Button(c, ButtonOpts{
							Key:      "attach-preview-default-app",
							Text:     "用默认应用打开",
							Label:    "用默认应用打开",
							Size:     ControlSM,
							Variant:  ButtonSecondary,
							Disabled: !canOpen,
							OnClick: func() {
								attachPreviewOpenDocumentAction(s, item, fileManagerOpenDefault)
							},
						})
						Button(c, ButtonOpts{
							Key:      "attach-preview-finder",
							Text:     "在 Finder 中显示",
							Label:    "在 Finder 中显示",
							Size:     ControlSM,
							Variant:  ButtonSecondary,
							Disabled: !canOpen,
							OnClick: func() {
								attachPreviewOpenDocumentAction(s, item, func(p string) error {
									attachPreviewRevealItem(p)
									return nil
								})
							},
						})
					})
			})
	})

	renderAttachPreviewFooter(c, s, nil)
}

func renderAttachPreviewKVRow(c *ui.Context, key, label, val, fontFam string, codeSz float32) {
	ui.Row(c.Key(key)).
		FillWidth().
		Gap(Space3).
		AlignItems(ui.Start).
		Children(func() {
			ui.Text(c, label).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Shrink(0)
			ui.Text(c, val).
				Font(fontFam).
				FontSize(uiFont(codeSz)).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				MinWidth(0).
				Selectable()
		})
}

func renderAttachPreviewBinary(c *ui.Context, s *chatState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem) {
	fontFam, codeSz := attachPreviewCodeFontForState(s)
	pathVal := item.SourcePath
	if pathVal == "" {
		pathVal = "—"
	}
	mimeVal := item.MimeType
	if mimeVal == "" {
		mimeVal = "application/octet-stream"
	}
	shaVal := item.SHA256
	if item.SHA256Loading {
		shaVal = "计算中…"
	} else if shaVal == "" {
		shaVal = "—"
	}

	canOpen := item.SourcePath != ""
	if snap.readOnly {
		canOpen = attachPreviewCanOpenSent(s, item)
	}

	bodyWrap.Children(func() {
		ui.Column(c.Key("attach-preview-bin-kv")).
			FillWidth().
			Padding(Space3).
			Gap(Space2).
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			Children(func() {
				renderAttachPreviewKVRow(c, "attach-preview-kv-path", "本地路径", pathVal, fontFam, codeSz)
				renderAttachPreviewKVRow(c, "attach-preview-kv-size", "大小", attachPreviewSizeLabel(item), fontFam, codeSz)
				renderAttachPreviewKVRow(c, "attach-preview-kv-mime", "类型", mimeVal, fontFam, codeSz)
				renderAttachPreviewKVRow(c, "attach-preview-kv-sha", "SHA-256", shaVal, fontFam, codeSz)
			})

		ui.Row(c.Key("attach-preview-bin-notice")).
			FillWidth().
			Padding(Space2, Space3).
			Radius(RadiusMD).
			Background(ThemeAccentSubtle(c)).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, "模型只读取该文件的本地路径，不解析二进制正文。").
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemePrimaryText(c))
			})
	})

	renderAttachPreviewFooter(c, s, func() {
		Button(c, ButtonOpts{
			Key:      "attach-preview-bin-open",
			Text:     "用默认应用打开",
			Label:    "用默认应用打开",
			Size:     ControlSM,
			Variant:  ButtonPrimary,
			Disabled: !canOpen,
			OnClick: func() {
				attachPreviewOpenDocumentAction(s, item, fileManagerOpenDefault)
			},
		})
	})
}

func buildArchiveTree(entries []archiveEntry) []*archiveTreeNode {
	root := &archiveTreeNode{IsDir: true}
	dirMap := map[string]*archiveTreeNode{"": root}
	fileSeen := make(map[string]bool)

	var ensureDir func(dirPath string) *archiveTreeNode
	ensureDir = func(dirPath string) *archiveTreeNode {
		if dirPath == "" || dirPath == "." {
			return root
		}
		if node, ok := dirMap[dirPath]; ok {
			return node
		}
		parentPath := path.Dir(dirPath)
		if parentPath == "." {
			parentPath = ""
		}
		parent := ensureDir(parentPath)
		node := &archiveTreeNode{
			Name:  path.Base(dirPath),
			Path:  dirPath,
			IsDir: true,
		}
		parent.Children = append(parent.Children, node)
		dirMap[dirPath] = node
		return node
	}

	for _, e := range entries {
		clean := strings.TrimSuffix(e.Path, "/")
		if clean == "" || clean == "." {
			continue
		}
		if e.IsDir {
			ensureDir(clean)
			continue
		}
		if fileSeen[clean] {
			continue
		}
		fileSeen[clean] = true
		parentPath := path.Dir(clean)
		if parentPath == "." {
			parentPath = ""
		}
		parent := ensureDir(parentPath)
		parent.Children = append(parent.Children, &archiveTreeNode{
			Name:  path.Base(clean),
			Path:  clean,
			IsDir: false,
			Size:  e.Size,
		})
	}

	sortArchiveTreeNodes(root.Children)
	return root.Children
}

func sortArchiveTreeNodes(nodes []*archiveTreeNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		li, lj := strings.ToLower(nodes[i].Name), strings.ToLower(nodes[j].Name)
		if li != lj {
			return li < lj
		}
		return nodes[i].Name < nodes[j].Name
	})
	for _, n := range nodes {
		if len(n.Children) > 0 {
			sortArchiveTreeNodes(n.Children)
		}
	}
}

func flattenArchiveTree(nodes []*archiveTreeNode, collapsed map[string]bool) []archiveTreeRow {
	var out []archiveTreeRow
	var walk func(list []*archiveTreeNode, depth int)
	walk = func(list []*archiveTreeNode, depth int) {
		for _, n := range list {
			out = append(out, archiveTreeRow{
				Name:  n.Name,
				Path:  n.Path,
				Depth: depth,
				IsDir: n.IsDir,
				Size:  n.Size,
			})
			if n.IsDir && !collapsed[n.Path] && len(n.Children) > 0 {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(nodes, 0)
	return out
}

func toggleArchiveDirCollapsed(s *chatState, st *attachmentPreviewState, idx int, dirPath string) {
	if st == nil || dirPath == "" {
		return
	}
	st.mu.Lock()
	if st.open && idx >= 0 && idx < len(st.items) {
		cur := &st.items[idx]
		if cur.ArchiveCollapsed == nil {
			cur.ArchiveCollapsed = make(map[string]bool)
		}
		if cur.ArchiveCollapsed[dirPath] {
			delete(cur.ArchiveCollapsed, dirPath)
		} else {
			cur.ArchiveCollapsed[dirPath] = true
		}
		cur.ArchiveRows = flattenArchiveTree(cur.ArchiveTree, cur.ArchiveCollapsed)
	}
	st.mu.Unlock()
	if s != nil {
		s.invalidate()
	}
}

func toggleArchiveEntrySelected(s *chatState, st *attachmentPreviewState, idx int, entryPath string, checked bool) {
	if st == nil || entryPath == "" {
		return
	}
	st.mu.Lock()
	if st.open && idx >= 0 && idx < len(st.items) {
		cur := &st.items[idx]
		if checked {
			if cur.ArchiveSelected == nil {
				cur.ArchiveSelected = make(map[string]bool)
			}
			cur.ArchiveSelected[entryPath] = true
		} else if cur.ArchiveSelected != nil {
			delete(cur.ArchiveSelected, entryPath)
		}
	}
	st.mu.Unlock()
	if s != nil {
		s.invalidate()
	}
}

func startExtractArchiveSelection(s *chatState, st *attachmentPreviewState, idx int) {
	if s == nil || st == nil {
		return
	}
	st.mu.Lock()
	if !st.open || st.readOnly || idx < 0 || idx >= len(st.items) {
		st.mu.Unlock()
		return
	}
	item := &st.items[idx]
	if item.ArchiveExtracting || len(item.ArchiveSelected) == 0 {
		st.mu.Unlock()
		return
	}
	var paths []string
	for _, e := range item.ArchiveEntries {
		if !e.IsDir && item.ArchiveSelected[e.Path] {
			paths = append(paths, e.Path)
		}
	}
	if len(paths) == 0 {
		st.mu.Unlock()
		return
	}
	item.ArchiveExtracting = true
	gen := st.generation
	archiveName := item.Name
	sourcePath := item.SourcePath
	threadID := st.threadID
	st.mu.Unlock()

	s.invalidate()
	go extractSelectedArchiveEntries(s, st, gen, idx, archiveName, sourcePath, threadID, paths)
}

func extractSelectedArchiveEntries(s *chatState, st *attachmentPreviewState, gen uint64, idx int, archiveName, sourcePath, threadID string, entryPaths []string) {
	var failed []rejectedAttachment

	finish := func() {
		st.mu.Lock()
		if st.open && st.generation == gen && idx < len(st.items) {
			st.items[idx].ArchiveExtracting = false
			st.items[idx].ArchiveSelected = nil
		}
		st.mu.Unlock()
		if len(failed) > 0 {
			s.setAttachmentNotice(formatRejectionNotice(failed))
		}
		s.invalidate()
	}

	if sourcePath == "" {
		for _, p := range entryPaths {
			name := archiveEntryBaseName(p)
			if name == "" {
				name = path.Base(p)
			}
			failed = append(failed, rejectedAttachment{name: name, reason: "无法读取压缩包内容"})
		}
		finish()
		return
	}

	f, err := os.Open(sourcePath)
	if err != nil {
		reason := shortMediaError(err)
		for _, p := range entryPaths {
			name := archiveEntryBaseName(p)
			if name == "" {
				name = path.Base(p)
			}
			failed = append(failed, rejectedAttachment{name: name, reason: reason})
		}
		finish()
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		reason := shortMediaError(err)
		for _, p := range entryPaths {
			name := archiveEntryBaseName(p)
			if name == "" {
				name = path.Base(p)
			}
			failed = append(failed, rejectedAttachment{name: name, reason: reason})
		}
		finish()
		return
	}

	for _, entryPath := range entryPaths {
		baseName := archiveEntryBaseName(entryPath)
		if baseName == "" {
			baseName = path.Base(entryPath)
		}
		data, readErr := readArchiveEntry(archiveName, f, info.Size(), entryPath, maxAttachmentBytes)
		if readErr != nil {
			failed = append(failed, rejectedAttachment{name: baseName, reason: shortMediaError(readErr)})
			continue
		}
		mimeType, ok := detectAttachmentMime(baseName, data)
		if !ok || mimeType == "" {
			mimeType = "application/octet-stream"
		}
		s.addAttachmentToThread(threadID, baseName, mimeType, data, nil)
	}

	finish()
}

func renderAttachPreviewArchive(c *ui.Context, s *chatState, st *attachmentPreviewState, bodyWrap ui.Element, snap attachPreviewSnapshot, item attachPreviewItem, sending bool) {
	rows := item.ArchiveRows
	selectedCount := len(item.ArchiveSelected)

	canReveal := item.SourcePath != ""
	if snap.readOnly {
		canReveal = attachPreviewCanOpenSent(s, item)
	}

	bodyWrap.Children(func() {
		ui.Row(c.Key("attach-preview-archive-toolbar")).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, fmt.Sprintf("压缩包清单（共 %d 项）", len(item.ArchiveEntries))).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Grow(1).
					MinWidth(0)
				if !snap.readOnly && selectedCount > 0 {
					ui.Text(c, fmt.Sprintf("已选 %d 个文件", selectedCount)).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeAccent(c)).
						SingleLine().
						Shrink(0)
				}
			})

		viewer := ui.Column(c.Key("attach-preview-archive-viewer")).
			Grow(1).
			MinHeight(0).
			FillWidth().
			Radius(RadiusMD).
			Background(ThemeSurfaceMain(c)).
			Border(Hairline, ThemeHairline(c)).
			Padding(Space1, 0).
			Clip()
		viewer.Children(func() {
			if len(rows) == 0 {
				ui.Box(c.Key("attach-preview-archive-empty")).Padding(Space3).Children(func() {
					ui.Text(c, "压缩包为空").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
				})
				return
			}
			st.archiveList.Key = func(i int) any {
				if i >= 0 && i < len(rows) {
					return rows[i].Path
				}
				return i
			}
			ui.List(c.Key(fmt.Sprintf("attach-preview-archive-list-%d", snap.index)), &st.archiveList, len(rows), func(i int) {
				row := rows[i]
				indent := float32(row.Depth) * FolderIndent
				padLeft := Space3 + indent
				if row.IsDir {
					dirPath := row.Path
					collapsed := item.ArchiveCollapsed[dirPath]
					dirKey := fmt.Sprintf("attach-preview-archive-dir-%d-%s", snap.index, dirPath)
					dirBtn := ui.ButtonBase(c.Key(dirKey)).
						Label(row.Name).
						FillWidth().
						MinHeight(RowHeightCompact).
						Padding(Space0, Space3, Space0, padLeft).
						Gap(Space2).
						AlignItems(ui.Center).
						Radius(RadiusSM).
						Cursor(ui.CursorPointer).
						Transition(motionFeedback(c)).
						OnClick(func() {
							toggleArchiveDirCollapsed(s, st, snap.index, dirPath)
						})
					if dirBtn.Hovered() {
						dirBtn.Background(ThemeHover(c))
					}
					dirBtn.Children(func() {
						chev := ui.Icon(c.Key(dirKey+"-chev"), IconChevronDown()).
							Size(IconSM, IconSM).
							TextColor(ThemeSecondaryText(c)).
							Shrink(0)
						targetAngle := float32(0)
						if collapsed {
							targetAngle = -90
						}
						if reduceMotion(c) {
							chev.Jump("archive-dir-chev", targetAngle)
							chev.Rotate(targetAngle)
						} else {
							chev.Rotate(chev.AnimateTo("archive-dir-chev", targetAngle, ui.Animation{
								Duration: animateDuration(c, DurSnap),
								Ease:     EaseSnap,
							}))
						}
						folderIcon := IconFolderOpen()
						if collapsed {
							folderIcon = IconFolder()
						}
						ui.Icon(c, folderIcon).
							Size(IconSM, IconSM).
							TextColor(ThemeSecondaryText(c)).
							Shrink(0)
						ui.Text(c, row.Name).
							FontSize(uiFont(TypeFootnote)).
							FontWeight(weightFor(c, 500)).
							TextColor(ThemePrimaryText(c)).
							SingleLine().
							Ellipsis("…").
							Grow(1).
							MinWidth(0)
					})
					return
				}

				entryPath := row.Path
				fileKey := fmt.Sprintf("attach-preview-archive-file-%d-%s", snap.index, entryPath)
				fileRow := ui.Row(c.Key(fileKey)).
					FillWidth().
					MinHeight(RowHeightCompact).
					Padding(Space0, Space3, Space0, padLeft).
					Gap(Space2).
					AlignItems(ui.Center).
					Radius(RadiusSM).
					Transition(motionFeedback(c))
				checked := !snap.readOnly && item.ArchiveSelected[entryPath]
				if checked {
					fileRow.Background(ThemeAccentSubtle(c))
				}
				fileRow.Children(func() {
					if !snap.readOnly {
						cb := ui.Checkbox(c.Key(fileKey+"-check"), &checked, "").
							Label("选择文件 " + row.Name)
						if item.ArchiveExtracting {
							cb.Disabled(true)
						}
						if cb.Changed() {
							toggleArchiveEntrySelected(s, st, snap.index, entryPath, checked)
						}
					}
					cat, tag := classifyAttachment(row.Name, "")
					renderAttachmentExtBadge(c, fileKey+"-badge", cat, tag, categoryColor(c, cat))
					ui.Text(c, row.Name).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Ellipsis("…").
						Grow(1).
						MinWidth(0)
					ui.Text(c, formatFileSize(row.Size)).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						Shrink(0)
				})
			}).FillWidth().Grow(1).MinHeight(0)

			if item.ArchiveTruncated {
				ui.Text(c, fmt.Sprintf("压缩包条目已截断（仅显示前 %d 项）", archiveListLimit)).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeWarning(c)).
					Padding(Space1, Space3, Space1, Space3)
			}
		})
	})

	renderAttachPreviewFooter(c, s, func() {
		if !snap.readOnly {
			Button(c, ButtonOpts{
				Key:      "attach-preview-archive-extract",
				Text:     "单独附加所选文件",
				Label:    "单独附加所选文件",
				Size:     ControlSM,
				Variant:  ButtonPrimary,
				Disabled: sending || selectedCount == 0 || item.ArchiveExtracting,
				Loading:  item.ArchiveExtracting,
				OnClick: func() {
					startExtractArchiveSelection(s, st, snap.index)
				},
			})
		}
		Button(c, ButtonOpts{
			Key:      "attach-preview-archive-finder",
			Text:     "在 Finder 中显示",
			Label:    "在 Finder 中显示",
			Size:     ControlSM,
			Variant:  ButtonSecondary,
			Disabled: !canReveal,
			OnClick: func() {
				attachPreviewOpenDocumentAction(s, item, func(p string) error {
					attachPreviewRevealItem(p)
					return nil
				})
			},
		})
	})
}
