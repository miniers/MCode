package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/richtext"
)

const (
	mentionTabAll    = "all"
	mentionTabFile   = "file"
	mentionTabThread = "thread"

	// 方案 B 宽面板尺寸（DIP），对齐 .mcode/design/at-mention-menu.html .variant-b。
	mentionMenuWidth          float32 = 640
	mentionMenuPreviewMinW    float32 = 560
	mentionMenuListWidth      float32 = 340
	mentionMenuBodyHeight     float32 = 268
	mentionMenuCrumbSepSize   float32 = 12
	mentionMenuDirSlideOffset float32 = 12
	mentionMenuIgnoredOpacity float32 = 0.48
	mentionMenuNameMaxRatio   float32 = 0.58

	mentionSearchDebounce    = 150 * time.Millisecond
	mentionSearchResultLimit = 50
	mentionPreviewReadBytes  = 4096
	mentionPreviewMaxLines   = 9
	mentionPreviewMaxDirRows = 7
	mentionPreviewLineNumW   = 18
	mentionPreviewTimeout    = 5 * time.Second
	mentionListTimeout       = 10 * time.Second
)

var mentionTabOrder = []string{mentionTabAll, mentionTabFile, mentionTabThread}

var mentionTabSpecs = []scopeTabSpec{
	{ID: mentionTabAll, Label: "全部"},
	{ID: mentionTabFile, Label: "文件"},
	{ID: mentionTabThread, Label: "会话"},
}

// mentionItemPreview 缓存右侧预览区读取到的文件或目录内容，按「会话 + 路径」索引。
type mentionItemPreview struct {
	loading    bool
	err        string
	isDir      bool
	kind       string // "text" | "image" | "binary"
	lines      []string
	dirEntries []FileEntry
	totalSize  int64
}

func mentionPreviewCacheKey(threadID, path string, isDir bool) string {
	kind := "file"
	if isDir {
		kind = "dir"
	}
	return threadID + "\x00" + kind + "\x00" + path
}

func trimMentionSlashes(p string) string {
	return strings.Trim(strings.TrimSpace(p), "/")
}

func baseNameOfPath(p string) string {
	clean := trimMentionSlashes(p)
	if clean == "" {
		return ""
	}
	if idx := strings.LastIndex(clean, "/"); idx >= 0 {
		return clean[idx+1:]
	}
	return clean
}

func parentDirOfPath(p string) string {
	clean := trimMentionSlashes(p)
	if clean == "" {
		return ""
	}
	idx := strings.LastIndex(clean, "/")
	if idx < 0 {
		return ""
	}
	return clean[:idx] + "/"
}

func parentDirClean(dir string) string {
	clean := trimMentionSlashes(dir)
	if clean == "" {
		return ""
	}
	idx := strings.LastIndex(clean, "/")
	if idx < 0 {
		return ""
	}
	return clean[:idx]
}

// resetMentionStateLocked 复位 '@' 菜单的目录、页签与防抖定时器。调用方需持有 s.mu。
func (s *chatState) resetMentionStateLocked() {
	if s.mentionDebounceTimer != nil {
		s.mentionDebounceTimer.Stop()
		s.mentionDebounceTimer = nil
	}
	s.mentionTab = ""
	s.mentionDir = ""
	s.mentionErr = ""
	s.mentionFiles = nil
	s.mentionFilesLoaded = false
	s.mentionFilesLoading = false
	s.mentionDirAnim = ""
}

// cycleMentionTabLocked 在 '@' 补全打开时按 ←/→ 循环切换「全部 / 文件 / 会话」页签。调用方需持有 s.mu。
func (s *chatState) cycleMentionTabLocked(step int) {
	curTab := s.mentionTab
	if curTab == "" {
		curTab = mentionTabAll
	}
	cur := 0
	for i, id := range mentionTabOrder {
		if id == curTab {
			cur = i
			break
		}
	}
	next := ((cur+step)%len(mentionTabOrder) + len(mentionTabOrder)) % len(mentionTabOrder)
	s.setMentionTabLocked(mentionTabOrder[next])
}

// setMentionTabLocked 切换 '@' 菜单页签并重建候选；切换到需要文件的页签且尚未加载时自动拉取。调用方需持有 s.mu。
func (s *chatState) setMentionTabLocked(tab string) {
	if tab == "" {
		tab = mentionTabAll
	}
	if s.mentionTab == tab {
		return
	}
	s.mentionTab = tab
	s.completionSelected = 0
	s.completionScrollSelection = true
	if tab == mentionTabThread {
		s.completionLoading = false
		s.completionLoaded = true
	} else {
		if !s.mentionFilesLoaded && !s.mentionFilesLoading && s.mentionErr == "" {
			s.startMentionFileFetchLocked(s.completionThreadID, s.mentionDir, s.completionQuery, false)
		} else {
			s.completionLoading = s.mentionFilesLoading
			s.completionLoaded = s.mentionFilesLoaded || s.mentionErr != ""
		}
	}
	s.rebuildMentionItemsLocked()
	s.ensureSelectableCompletionLocked()
	s.ensureMentionSelectionPreviewLocked()
}

// enterMentionDirLocked 进入或返回子目录：把草稿里当前触发词的查询清成只剩 '@'，
// 保持菜单打开并立即列出目标目录。调用方需持有 s.mu。
func (s *chatState) enterMentionDirLocked(dir, anim string) (newDraft string) {
	cleanDir := trimMentionSlashes(dir)
	if s.mentionDebounceTimer != nil {
		s.mentionDebounceTimer.Stop()
		s.mentionDebounceTimer = nil
	}

	s.syncDraftTags()
	totalRunes := s.draftText.Len()
	compStart := min(max(s.completionStart, 0), len(s.draft))
	start := utf8.RuneCountInString(s.draft[:compStart])
	if start > totalRunes {
		start = totalRunes
	}
	end := totalRunes
	if s.completionCaret >= 0 {
		end = min(max(s.completionCaret, start), totalRunes)
	}
	s.draftText = s.draftText.Replace(start, end, richtext.NewText("@"))
	s.draft = s.draftText.String()
	target := start + 1
	s.completionCaretTarget = target
	if s.completionCaret >= 0 {
		s.completionCaret = target
	}
	s.completionCaretToEnd = true

	s.mentionDir = cleanDir
	s.completionQuery = ""
	if s.mentionTab == mentionTabThread {
		s.mentionTab = mentionTabFile
	}
	s.mentionDirAnim = anim
	s.mentionDirAnimRun++
	s.mentionErr = ""
	s.mentionFiles = nil
	s.mentionFilesLoaded = false
	s.mentionFilesLoading = false
	s.completionSelected = 0
	s.completionScrollSelection = true
	s.completionItems = nil
	s.mentionSeq++

	threadID := s.completionThreadID
	if threadID == "" {
		threadID = s.selectedThreadID
	}
	s.rebuildMentionItemsLocked()
	s.startMentionFileFetchLocked(threadID, cleanDir, "", false)
	s.ensureSelectableCompletionLocked()
	s.ensureMentionSelectionPreviewLocked()
	return s.draft
}

// updateMentionCompletionLocked 在 updateCompletion 中处理 '@' 补全的更新逻辑。调用方需持有 s.mu。
func (s *chatState) updateMentionCompletionLocked(threadID, query string, start int, prevKind completionKind) (changed bool) {
	triggerChanged := prevKind != completionMention || s.completionStart != start || s.completionThreadID != threadID
	queryChanged := s.completionQuery != query
	changed = triggerChanged || queryChanged

	if triggerChanged {
		s.resetMentionStateLocked()
		s.mentionTab = mentionTabAll
	}
	if changed {
		if s.mentionDebounceTimer != nil {
			s.mentionDebounceTimer.Stop()
			s.mentionDebounceTimer = nil
		}
		s.completionItems = nil
		s.completionSelected = 0
		s.completionScrollSelection = true
		s.completionLoading = false
		s.completionLoaded = false
		s.mentionErr = ""
		s.mentionFiles = nil
		s.mentionFilesLoaded = false
		s.mentionFilesLoading = false
		s.mentionSeq++
		noteCompletionLevel(completionMention, query)
	}
	if s.mentionTab == "" {
		s.mentionTab = mentionTabAll
	}

	s.completionDismissedFor = ""
	s.completionKind = completionMention
	s.completionStart = start
	s.completionQuery = query
	s.completionThreadID = threadID

	if s.mentionTab == mentionTabThread {
		if changed || !s.completionLoaded {
			s.completionLoading = false
			s.completionLoaded = true
			s.rebuildMentionItemsLocked()
			s.ensureSelectableCompletionLocked()
			s.ensureMentionSelectionPreviewLocked()
		}
		return changed
	}

	if !s.mentionFilesLoaded && !s.mentionFilesLoading && s.mentionErr == "" {
		s.startMentionFileFetchLocked(threadID, s.mentionDir, query, query != "")
	} else {
		s.completionLoading = s.mentionFilesLoading
		s.completionLoaded = s.mentionFilesLoaded || s.mentionErr != ""
	}
	if changed {
		s.rebuildMentionItemsLocked()
		s.ensureSelectableCompletionLocked()
		s.ensureMentionSelectionPreviewLocked()
	}
	return changed
}

// startMentionFileFetchLocked 发起目录列举或防抖搜索请求。调用方需持有 s.mu。
func (s *chatState) startMentionFileFetchLocked(threadID, dir, query string, debounce bool) {
	if s.mentionDebounceTimer != nil {
		s.mentionDebounceTimer.Stop()
		s.mentionDebounceTimer = nil
	}
	s.mentionFilesLoading = true
	s.completionLoading = true
	s.completionLoaded = false
	seq := s.mentionSeq
	client := s.client

	run := func() {
		s.mu.Lock()
		stale := s.closed || s.mentionSeq != seq || s.completionKind != completionMention || s.completionThreadID != threadID || s.completionQuery != query || s.mentionDir != dir
		s.mu.Unlock()
		if stale || client == nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), mentionListTimeout)
		defer cancel()
		var items []completionItem
		var err error
		if query == "" {
			var listing FileListing
			listing, err = client.FilesList(ctx, threadID, dir)
			if err == nil {
				items = mentionFileEntriesToItems(dir, listing.Entries)
			}
		} else {
			items, err = searchMentionFileItems(ctx, client, threadID, dir, query)
		}

		s.mu.Lock()
		if s.closed || s.mentionSeq != seq || s.completionKind != completionMention || s.completionThreadID != threadID || s.completionQuery != query || s.mentionDir != dir {
			s.mu.Unlock()
			return
		}
		s.mentionFilesLoading = false
		s.mentionFilesLoaded = true
		s.mentionDebounceTimer = nil
		if err != nil {
			s.mentionErr = err.Error()
			s.mentionFiles = nil
		} else {
			s.mentionErr = ""
			s.mentionFiles = items
		}
		if s.mentionTab != mentionTabThread {
			s.completionLoading = false
			s.completionLoaded = true
			s.rebuildMentionItemsLocked()
			s.ensureSelectableCompletionLocked()
			s.ensureMentionSelectionPreviewLocked()
		}
		s.mu.Unlock()
		s.invalidate()
	}

	if debounce {
		s.mentionDebounceTimer = time.AfterFunc(mentionSearchDebounce, run)
	} else {
		go run()
	}
}

func searchMentionFileItems(ctx context.Context, client Client, threadID, dir, query string) ([]completionItem, error) {
	var matches []FileMatch
	if scoped, ok := client.(FileScopedSearcher); ok {
		res, err := scoped.SearchFileEntriesIn(ctx, threadID, dir, query)
		if err != nil {
			return nil, err
		}
		matches = res.Entries
	} else {
		all, err := client.SearchFiles(ctx, threadID, query)
		if err != nil {
			return nil, err
		}
		if dir == "" {
			matches = all
		} else {
			prefix := dir + "/"
			for _, m := range all {
				clean := trimMentionSlashes(m.Path)
				if strings.HasPrefix(clean, prefix) {
					matches = append(matches, m)
				}
			}
		}
	}
	if len(matches) > mentionSearchResultLimit {
		matches = matches[:mentionSearchResultLimit]
	}
	return mentionFileMatchesToItems(dir, matches), nil
}

func mentionFileEntriesToItems(dir string, entries []FileEntry) []completionItem {
	filtered := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		if e.Protected {
			continue
		}
		if e.Path == "" && e.Name != "" {
			if dir != "" {
				e.Path = dir + "/" + e.Name
			} else {
				e.Path = e.Name
			}
		}
		if e.Name == "" {
			e.Name = baseNameOfPath(e.Path)
		}
		filtered = append(filtered, e)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		if a.Ignored != b.Ignored {
			return !a.Ignored
		}
		al, bl := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if al != bl {
			return al < bl
		}
		return a.Name < b.Name
	})

	items := make([]completionItem, len(filtered))
	for i, e := range filtered {
		cleanPath := trimMentionSlashes(e.Path)
		name := baseNameOfPath(cleanPath)
		if name == "" {
			name = e.Name
		}
		insert := "@" + cleanPath
		if e.IsDir {
			name += "/"
			insert += "/"
		}
		items[i] = completionItem{
			Label:   name,
			Path:    cleanPath,
			Insert:  insert,
			IsDir:   e.IsDir,
			Ignored: e.Ignored,
		}
	}
	return items
}

func mentionFileMatchesToItems(dir string, matches []FileMatch) []completionItem {
	items := make([]completionItem, len(matches))
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	for i, m := range matches {
		cleanPath := trimMentionSlashes(m.Path)
		rel := cleanPath
		if prefix != "" && strings.HasPrefix(cleanPath, prefix) {
			rel = strings.TrimPrefix(cleanPath, prefix)
		}
		name := baseNameOfPath(cleanPath)
		if name == "" {
			name = m.Name
		}
		dirHint := parentDirOfPath(rel)
		insert := "@" + cleanPath
		if m.IsDir {
			name += "/"
			insert += "/"
		}
		items[i] = completionItem{
			Label:   name,
			Detail:  dirHint,
			Path:    cleanPath,
			Insert:  insert,
			IsDir:   m.IsDir,
			Ignored: m.Ignored,
		}
	}
	return items
}

// rebuildMentionItemsLocked 按当前页签、目录与已拉取的文件结果拼装候选列表。调用方需持有 s.mu。
func (s *chatState) rebuildMentionItemsLocked() {
	tab := s.mentionTab
	if tab == "" {
		tab = mentionTabAll
	}
	dir := s.mentionDir
	if s.mentionErr != "" && tab != mentionTabThread {
		s.completionItems = nil
		return
	}

	var threadItems []completionItem
	if (tab == mentionTabAll || tab == mentionTabThread) && dir == "" {
		limit := 5
		if tab == mentionTabThread {
			limit = 20
		}
		threadItems = recentThreadMentionItems(s.threads, s.projectIDForThreadLocked(s.completionThreadID), s.completionThreadID, s.completionQuery, time.Now(), limit)
	}

	var fileItems []completionItem
	if tab == mentionTabAll || tab == mentionTabFile {
		fileItems = s.mentionFiles
	}

	out := make([]completionItem, 0, len(threadItems)+len(fileItems)+2)
	switch tab {
	case mentionTabAll:
		if len(threadItems) > 0 {
			out = append(out, completionItem{Label: "会话", Header: true})
			out = append(out, threadItems...)
		}
		if len(fileItems) > 0 {
			label := "文件"
			if dir != "" {
				label = dir + "/"
			}
			out = append(out, completionItem{Label: label, Header: true})
			out = append(out, fileItems...)
		}
	case mentionTabFile:
		out = append(out, fileItems...)
	case mentionTabThread:
		out = append(out, threadItems...)
	}
	s.completionItems = out
}

// ensureMentionSelectionPreviewLocked 在选中项变化时按需触发右侧预览区异步加载。调用方需持有 s.mu。
func (s *chatState) ensureMentionSelectionPreviewLocked() {
	if s.completionKind != completionMention || s.client == nil {
		return
	}
	if s.mentionErr != "" && s.mentionTab != mentionTabThread {
		return
	}
	if s.completionSelected < 0 || s.completionSelected >= len(s.completionItems) {
		return
	}
	item := s.completionItems[s.completionSelected]
	if item.Header {
		return
	}
	threadID := s.completionThreadID
	if item.ThreadID != "" {
		if completionThreadUnavailableLocked(s, item) != "" {
			return
		}
		if mentionPreviewDigestOf(item.ThreadID) == nil {
			targetID := item.ThreadID
			go startMentionPreviewLoad(s, targetID)
		}
		return
	}
	if item.Path == "" {
		return
	}
	if s.mentionPreviewCache == nil {
		s.mentionPreviewCache = make(map[string]*mentionItemPreview)
	}
	key := mentionPreviewCacheKey(threadID, item.Path, item.IsDir)
	if _, ok := s.mentionPreviewCache[key]; ok {
		return
	}
	entry := &mentionItemPreview{loading: true, isDir: item.IsDir}
	s.mentionPreviewCache[key] = entry
	path := item.Path
	isDir := item.IsDir
	client := s.client

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mentionPreviewTimeout)
		defer cancel()
		result := &mentionItemPreview{isDir: isDir}
		if isDir {
			listing, err := client.FilesList(ctx, threadID, path)
			if err != nil {
				result.err = err.Error()
			} else {
				entries := make([]FileEntry, 0, len(listing.Entries))
				for _, e := range listing.Entries {
					if !e.Protected {
						if e.Name == "" {
							e.Name = baseNameOfPath(e.Path)
						}
						entries = append(entries, e)
					}
				}
				sort.SliceStable(entries, func(i, j int) bool {
					if entries[i].IsDir != entries[j].IsDir {
						return entries[i].IsDir
					}
					if entries[i].Ignored != entries[j].Ignored {
						return !entries[i].Ignored
					}
					return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
				})
				result.dirEntries = entries
			}
		} else {
			content, err := client.FilesRead(ctx, threadID, path, 0, mentionPreviewReadBytes)
			if err != nil {
				if !isMentionPreviewUnsupportedErr(err) {
					result.err = err.Error()
				}
			} else {
				result.kind = content.Kind
				result.totalSize = content.TotalSize
				if content.Kind == "" || content.Kind == "text" {
					if content.Text != "" {
						raw := strings.ReplaceAll(content.Text, "\r\n", "\n")
						lines := strings.Split(raw, "\n")
						if len(lines) > mentionPreviewMaxLines {
							lines = lines[:mentionPreviewMaxLines]
						}
						result.lines = lines
					}
				}
			}
		}

		s.mu.Lock()
		if !s.closed {
			if s.mentionPreviewCache == nil {
				s.mentionPreviewCache = make(map[string]*mentionItemPreview)
			}
			s.mentionPreviewCache[key] = result
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// projectNameForThreadLocked 返回会话所属项目在面包屑根结点显示的短名称。调用方需持有 s.mu。
func (s *chatState) projectNameForThreadLocked(threadID string) string {
	pid := s.projectIDForThreadLocked(threadID)
	if pid != "" {
		for i := range s.projects {
			if s.projects[i].ID == pid {
				if name := strings.TrimSpace(s.projects[i].Name); name != "" {
					return name
				}
				if p := strings.TrimSpace(s.projects[i].Path); p != "" {
					if base := filepath.Base(p); base != "" && base != "." && base != string(filepath.Separator) {
						return base
					}
				}
				break
			}
		}
	}
	return "工作区"
}

// renderMentionBreadcrumbs 渲染方案 B 顶部固定高度的路径面包屑；「会话」页签下也保留同高度。
func renderMentionBreadcrumbs(c *ui.Context, s *chatState, tab, dir, rootName string) {
	ui.Column(c.Key("mention-breadcrumbs-wrap")).FillWidth().Gap(0).Children(func() {
		ui.Row(c.Key("mention-breadcrumbs")).
			FillWidth().
			Height(ControlMD).
			Padding(0, Space3).
			Gap(Space0).
			AlignItems(ui.Center).
			Clip().
			Children(func() {
				if tab == mentionTabThread {
					ui.Text(c, "最近会话").
						FontSize(uiFont(TypeCaption)).
						FontWeight(weightFor(c, 500)).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					return
				}
				parts := []string(nil)
				if clean := trimMentionSlashes(dir); clean != "" {
					parts = strings.Split(clean, "/")
				}
				renderMentionCrumbButton(c, s, "mention-crumb-root", rootName, "", len(parts) == 0)
				acc := ""
				for i, part := range parts {
					if acc == "" {
						acc = part
					} else {
						acc += "/" + part
					}
					targetDir := acc
					isLast := i == len(parts)-1
					ui.Icon(c, IconChevronRight()).
						Size(mentionMenuCrumbSepSize, mentionMenuCrumbSepSize).
						TextColor(ThemeTertiaryText(c))
					renderMentionCrumbButton(c, s, fmt.Sprintf("mention-crumb-%d", i), part, targetDir, isLast)
				}
			})
		ui.Box(c.Key("mention-breadcrumbs-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c))
	})
}

func renderMentionCrumbButton(c *ui.Context, s *chatState, key, label, targetDir string, current bool) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Height(ControlXS).
		Padding(0, Space1).
		Radius(RadiusXS)
	color := ThemeSecondaryText(c)
	if current {
		color = ThemePrimaryText(c)
	} else {
		btn.Cursor(ui.CursorPointer).OnClick(func() {
			s.mu.Lock()
			if s.completionKind == completionMention {
				s.enterMentionDirLocked(targetDir, "back")
			}
			s.mu.Unlock()
			s.invalidate()
		})
		if btn.Hovered() {
			btn.Background(ThemeOverlayHover(c))
			color = ThemePrimaryText(c)
		}
	}
	btn.Children(func() {
		txt := ui.Text(c, label).
			FontSize(uiFont(TypeCaption)).
			TextColor(color).
			SingleLine()
		if current {
			txt.FontWeight(weightFor(c, 500))
		}
	})
}

// mentionMatchHits 计算查询词在候选行（文件名与目录提示，或会话标题）中的 rune 命中下标。
func mentionMatchHits(query string, item completionItem) (nameHits, dirHits []int) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if item.ThreadID != "" {
		return matchSubstrOrSubseq(q, item.Label), nil
	}
	nameNoSlash := strings.TrimSuffix(item.Label, "/")
	dirHint := item.Detail
	if hits := matchSubstr(q, nameNoSlash); len(hits) > 0 {
		return hits, nil
	}
	if dirHint != "" {
		rel := dirHint + nameNoSlash
		dirLen := utf8.RuneCountInString(dirHint)
		if hits := matchSubstr(q, rel); len(hits) > 0 {
			for _, idx := range hits {
				if idx < dirLen {
					dirHits = append(dirHits, idx)
				} else {
					nameHits = append(nameHits, idx-dirLen)
				}
			}
			return nameHits, dirHits
		}
	}
	if hits := matchSubseq(q, nameNoSlash); len(hits) > 0 {
		return hits, nil
	}
	if dirHint != "" {
		rel := dirHint + nameNoSlash
		dirLen := utf8.RuneCountInString(dirHint)
		if hits := matchSubseq(q, rel); len(hits) > 0 {
			for _, idx := range hits {
				if idx < dirLen {
					dirHits = append(dirHits, idx)
				} else {
					nameHits = append(nameHits, idx-dirLen)
				}
			}
			return nameHits, dirHits
		}
	}
	return nil, nil
}

func matchSubstr(query, target string) []int {
	qr := lowerRunes(query)
	tr := lowerRunes(target)
	if len(qr) == 0 || len(qr) > len(tr) {
		return nil
	}
	for start := 0; start <= len(tr)-len(qr); start++ {
		matched := true
		for j := 0; j < len(qr); j++ {
			if tr[start+j] != qr[j] {
				matched = false
				break
			}
		}
		if matched {
			hits := make([]int, len(qr))
			for j := range qr {
				hits[j] = start + j
			}
			return hits
		}
	}
	return nil
}

func matchSubseq(query, target string) []int {
	qr := lowerRunes(query)
	tr := lowerRunes(target)
	if len(qr) == 0 {
		return nil
	}
	hits := make([]int, 0, len(qr))
	qi := 0
	for ti := 0; ti < len(tr) && qi < len(qr); ti++ {
		if tr[ti] == qr[qi] {
			hits = append(hits, ti)
			qi++
		}
	}
	if qi != len(qr) {
		return nil
	}
	return hits
}

func matchSubstrOrSubseq(query, target string) []int {
	if hits := matchSubstr(query, target); len(hits) > 0 {
		return hits
	}
	return matchSubseq(query, target)
}

func lowerRunes(s string) []rune {
	runes := []rune(s)
	for i, r := range runes {
		runes[i] = unicode.ToLower(r)
	}
	return runes
}

// renderMentionHighlightedText 用 ui.RichText 对命中字符分段着色；无命中时直接渲染 ui.Text。
func renderMentionHighlightedText(c *ui.Context, text string, hits []int, mono bool, size float32, baseColor ui.Color, maxW float32) ui.Element {
	var el ui.Element
	if len(hits) == 0 {
		el = ui.Text(c, text)
	} else {
		runes := []rune(text)
		hitSet := make(map[int]bool, len(hits))
		for _, h := range hits {
			hitSet[h] = true
		}
		spans := make([]ui.Span, 0, len(hits)*2+1)
		hlColor := ThemeAccent(c)
		hlBg := ThemeAccentSubtle(c)
		hlWeight := weightFor(c, 600)
		start := 0
		curHit := hitSet[0]
		for i := 1; i <= len(runes); i++ {
			nextHit := i < len(runes) && hitSet[i]
			if i == len(runes) || nextHit != curHit {
				chunk := string(runes[start:i])
				sp := ui.Span{Text: chunk}
				if curHit {
					sp.Color = hlColor
					sp.Background = hlBg
					sp.Weight = hlWeight
				}
				spans = append(spans, sp)
				start = i
				curHit = nextHit
			}
		}
		el = ui.RichText(c, spans...)
	}
	el.FontSize(size).TextColor(baseColor).SingleLine().Ellipsis("…")
	if mono {
		el.Font("monospace")
	}
	if maxW > 0 {
		el.MaxWidth(maxW)
	}
	return el
}

// renderMentionBody 渲染方案 B 固定高度主体：左侧候选列表 +（宽面板下）右侧预览区。
func renderMentionBody(
	c *ui.Context,
	s *chatState,
	threadID string,
	panelW float32,
	items []completionItem,
	reasons []string,
	selected int,
	scrollSelection bool,
	tab string,
	query string,
	errMsg string,
	loading bool,
	dirAnim string,
	dirAnimRun uint64,
	draft string,
	hoveredIdx *int,
) {
	showPreview := panelW >= mentionMenuPreviewMinW
	listW := panelW
	if showPreview {
		listW = mentionMenuListWidth
	}

	ui.Row(c.Key("mention-body")).
		FillWidth().
		Height(mentionMenuBodyHeight).
		Gap(0).
		Clip().
		Children(func() {
			listPane := ui.Column(c.Key("mention-list-pane")).
				FillHeight().
				Gap(0).
				Clip()
			if showPreview {
				listPane.Width(listW).Shrink(0)
			} else {
				listPane.Grow(1).MinWidth(0)
			}
			listPane.Children(func() {
				renderMentionListPane(c, s, listW, items, reasons, selected, scrollSelection, tab, query, errMsg, loading, dirAnim, dirAnimRun, draft, hoveredIdx)
			})
			if showPreview {
				ui.Box(c.Key("mention-split-divider")).Width(Hairline).FillHeight().Background(ThemeBorder(c))
				var selItem *completionItem
				var selReason string
				previewErr := errMsg
				if tab == mentionTabThread {
					previewErr = ""
				}
				if previewErr == "" && (!loading || len(items) > 0) && selected >= 0 && selected < len(items) && !items[selected].Header {
					it := items[selected]
					selItem = &it
					if selected < len(reasons) {
						selReason = reasons[selected]
					}
				}
				renderMentionPreviewPane(c, s, threadID, selItem, selReason, loading && len(items) == 0, previewErr)
			}
		})
}

func renderMentionListPane(
	c *ui.Context,
	s *chatState,
	listW float32,
	items []completionItem,
	reasons []string,
	selected int,
	scrollSelection bool,
	tab string,
	query string,
	errMsg string,
	loading bool,
	dirAnim string,
	dirAnimRun uint64,
	draft string,
	hoveredIdx *int,
) {
	if errMsg != "" && tab != mentionTabThread {
		ui.Column(c.Key("mention-error-state")).
			FillWidth().
			FillHeight().
			Center().
			Padding(Space4).
			Gap(Space2).
			Children(func() {
				ui.Icon(c, IconAlertTriangle()).Size(IconMD, IconMD).TextColor(ThemeDanger(c))
				ui.Text(c, "搜索出错").FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				ui.Text(c, errMsg).
					FontSize(uiFont(TypeCaption)).
					Font("monospace").
					TextColor(ThemeSecondaryText(c)).
					MaxLines(2).
					Ellipsis("…")
			})
		return
	}
	if loading && len(items) == 0 {
		ui.Column(c.Key("mention-loading-state")).
			FillWidth().
			FillHeight().
			Center().
			Padding(Space4).
			Gap(Space2).
			Children(func() {
				if reduceMotion(c) {
					ui.Icon(c, IconLoader()).Size(IconMD, IconMD).TextColor(ThemeSecondaryText(c))
				} else {
					renderRunningGlyph(c, "mention-loading-glyph", IconMD, ThemeSecondaryText(c))
				}
				ui.Text(c, "正在搜索工作区…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			})
		return
	}
	if len(items) == 0 {
		emptyMsg := "没有匹配的文件"
		if tab == mentionTabThread {
			emptyMsg = "没有匹配的会话"
		}
		ui.Column(c.Key("mention-empty-state")).
			FillWidth().
			FillHeight().
			Center().
			Padding(Space4).
			Gap(Space2).
			Children(func() {
				ui.Icon(c, IconSearch()).Size(IconMD, IconMD).TextColor(ThemeSecondaryText(c))
				ui.Text(c, emptyMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			})
		return
	}

	ui.Scroll(c.Key("composer-complete-scroll")).
		FillWidth().
		FillHeight().
		Children(func() {
			listCol := ui.Column(c.Key("composer-complete-list")).
				FillWidth().
				Padding(Space1).
				Gap(Space0)
			slide := float32(0)
			if !reduceMotion(c) && dirAnimRun > 0 && dirAnim != "" {
				startX := mentionMenuDirSlideOffset
				if dirAnim == "back" {
					startX = -mentionMenuDirSlideOffset
				}
				slide = listCol.Keyframes("mention-dir-slide", []ui.Keyframe{
					{At: 0, Value: startX},
					{At: DurFast, Value: 0, Ease: EaseOut},
				}, ui.KeyframeOptions{Run: dirAnimRun})
			}
			if slide != 0 {
				listCol.Translate(slide, 0)
			}
			listCol.Children(func() {
				for i, item := range items {
					idx := i
					it := item
					if it.Header {
						ui.Row(c.Key(fmt.Sprintf("composer-complete-header-%d", idx))).
							FillWidth().
							Padding(Space1, Space3).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, it.Label).
									FontSize(uiFont(TypeCaption)).
									FontWeight(weightFor(c, 600)).
									TextColor(ThemeTertiaryText(c))
							})
						continue
					}
					reason := ""
					if idx < len(reasons) {
						reason = reasons[idx]
					}
					renderMentionRow(c, s, listW, idx, it, reason, idx == selected, scrollSelection, query, draft, hoveredIdx)
				}
				if loading {
					ui.Row(c.Key("composer-complete-loading")).
						FillWidth().
						Padding(Space1, Space3).
						Children(func() {
							ui.Text(c, "正在加载…").
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c))
						})
				}
			})
		})
}

func renderMentionRow(
	c *ui.Context,
	s *chatState,
	listW float32,
	idx int,
	it completionItem,
	reason string,
	isSelected bool,
	scrollSelection bool,
	query string,
	draft string,
	hoveredIdx *int,
) {
	row := ui.Row(c.Key(fmt.Sprintf("composer-complete-%d", idx))).
		FillWidth().
		Padding(Space1, Space3).
		Radius(RadiusSM).
		AlignItems(ui.Center).
		Gap(Space2)
	if reason == "" {
		row.Cursor(ui.CursorPointer).OnClick(func() {
			s.mu.Lock()
			if s.draft == draft && s.completionKind == completionMention && !s.isComposing && !s.completionSuspended {
				if it.IsDir {
					s.enterMentionDirLocked(it.Path, "enter")
				} else {
					s.confirmCompletion(it)
				}
			}
			s.mu.Unlock()
			s.invalidate()
		})
		if row.Hovered() {
			*hoveredIdx = idx
		}
	}
	if isSelected {
		row.Background(ThemeOverlaySelected(c))
		if scrollSelection {
			row.ScrollIntoView()
		}
	}

	row.Children(func() {
		icon := IconFile()
		switch {
		case it.ThreadID != "" || it.Icon == "thread":
			icon = IconMessageSquarePlus()
		case it.IsDir:
			icon = IconFolder()
		}
		iconColor := ThemeSecondaryText(c)
		if isSelected && reason == "" {
			iconColor = ThemeAccent(c)
		} else if it.Ignored && reason == "" {
			iconColor = ThemeSecondaryText(c).Alpha(mentionMenuIgnoredOpacity)
		}
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(iconColor)

		if reason != "" {
			ui.Text(c, "会话引用已失效 · "+reason).
				FontSize(uiFont(TypeBody)).
				TextColor(ThemeSecondaryText(c)).
				Shrink(0).
				MaxWidth(listW*mentionMenuNameMaxRatio).
				SingleLine().
				Ellipsis("…")
			ui.Spacer(c)
			ui.Text(c, reason).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeDanger(c)).
				SingleLine().
				Ellipsis("…").
				Shrink(1).
				MinWidth(0)
			return
		}

		nameHits, dirHits := mentionMatchHits(query, it)
		if it.ThreadID != "" {
			nameColor := ThemePrimaryText(c)
			renderMentionHighlightedText(c, it.Label, nameHits, false, uiFont(TypeBody), nameColor, 0).
				Shrink(1).
				MinWidth(0)
			ui.Spacer(c)
			if it.Detail != "" {
				ui.Text(c, it.Detail).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeTertiaryText(c)).
					SingleLine().
					Shrink(0)
			}
			return
		}

		nameColor := ThemePrimaryText(c)
		hintColor := ThemeSecondaryText(c)
		if it.Ignored {
			nameColor = ThemePrimaryText(c).Alpha(mentionMenuIgnoredOpacity)
			hintColor = ThemeSecondaryText(c).Alpha(mentionMenuIgnoredOpacity)
		}
		nameEl := renderMentionHighlightedText(c, it.Label, nameHits, true, uiFont(TypeBody), nameColor, listW*mentionMenuNameMaxRatio)
		if it.Detail != "" {
			nameEl.Shrink(0)
			renderMentionHighlightedText(c, it.Detail, dirHits, true, uiFont(TypeFootnote), hintColor, 0).
				Shrink(1).
				MinWidth(0)
		} else {
			nameEl.Shrink(1).MinWidth(0)
		}
		ui.Spacer(c)
		if it.IsDir {
			ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c))
		}
	})
}

func renderMentionPreviewPane(c *ui.Context, s *chatState, threadID string, item *completionItem, unavailableReason string, listLoading bool, listErr string) {
	ui.Column(c.Key("mention-preview-pane")).
		Grow(1).
		MinWidth(0).
		FillHeight().
		Padding(Space3).
		Gap(Space2).
		Background(ThemeSurfaceMain(c)).
		Clip().
		Children(func() {
			if listLoading || listErr != "" || item == nil {
				ui.Column(c.Key("mention-preview-empty")).FillWidth().FillHeight().Center().Children(func() {
					ui.Text(c, "—").FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c))
				})
				return
			}

			if item.ThreadID != "" {
				renderMentionThreadPreview(c, s, item, unavailableReason)
				return
			}

			s.mu.Lock()
			var cached *mentionItemPreview
			if s.mentionPreviewCache != nil {
				cached = s.mentionPreviewCache[mentionPreviewCacheKey(threadID, item.Path, item.IsDir)]
			}
			s.mu.Unlock()

			if item.IsDir {
				renderMentionDirPreview(c, item, cached)
				return
			}
			renderMentionFilePreview(c, item, cached)
		})
}

func renderMentionItemPreviewHeader(c *ui.Context, icon *ui.SVG, title, meta string, mono bool) {
	ui.Column(c.Key("mention-preview-head-wrap")).FillWidth().Gap(0).Children(func() {
		ui.Row(c.Key("mention-preview-head")).
			FillWidth().
			Padding(0, 0, Space2, 0).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				t := ui.Text(c, title).
					FontSize(uiFont(TypeFootnote)).
					FontWeight(weightFor(c, 600)).
					TextColor(ThemePrimaryText(c)).
					SingleLine().
					Ellipsis("…").
					Shrink(1).
					MinWidth(0)
				if mono {
					t.Font("monospace")
				}
				ui.Spacer(c)
				if meta != "" {
					ui.Text(c, meta).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeTertiaryText(c)).
						SingleLine().
						Shrink(0)
				}
			})
		ui.Box(c.Key("mention-preview-head-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c))
	})
}

func renderMentionThreadPreview(c *ui.Context, s *chatState, item *completionItem, unavailableReason string) {
	renderMentionItemPreviewHeader(c, IconMessageSquarePlus(), item.Label, item.Detail, false)
	if unavailableReason != "" {
		ui.Text(c, unavailableReason).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
		return
	}
	digest := mentionPreviewDigestOf(item.ThreadID)
	switch {
	case digest == nil || digest.loading:
		ui.Text(c, "正在加载…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	case digest.err != "":
		ui.Text(c, "加载失败："+digest.err).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			MaxLines(4).
			Ellipsis("…")
	default:
		summary := mentionThreadPreviewSummary(digest)
		if summary == "" {
			summary = "该会话还没有可展示的内容"
		}
		ui.Text(c, summary).
			FontSize(uiFont(TypeFootnote)).
			LineHeight(1.5).
			TextColor(ThemeSecondaryText(c)).
			FillWidth().
			MinWidth(0).
			MaxLines(8).
			Ellipsis("…")
	}
}

func mentionThreadPreviewSummary(d *mentionPreviewDigest) string {
	if d == nil {
		return ""
	}
	var parts []string
	if g := strings.TrimSpace(d.goal); g != "" {
		parts = append(parts, g)
	}
	if con := strings.TrimSpace(d.conclusion); con != "" && con != strings.TrimSpace(d.goal) {
		parts = append(parts, con)
	}
	if len(parts) == 0 && len(d.recent) > 0 {
		last := d.recent[len(d.recent)-1]
		if t := strings.TrimSpace(last.text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderMentionDirPreview(c *ui.Context, item *completionItem, preview *mentionItemPreview) {
	meta := ""
	if preview != nil && !preview.loading && preview.err == "" {
		meta = strconv.Itoa(len(preview.dirEntries)) + " 项"
	}
	renderMentionItemPreviewHeader(c, IconFolderOpen(), item.Path+"/", meta, true)
	switch {
	case preview == nil || preview.loading:
		ui.Text(c, "正在加载…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	case preview.err != "":
		ui.Text(c, "加载失败："+preview.err).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			MaxLines(4).
			Ellipsis("…")
	case len(preview.dirEntries) == 0:
		ui.Text(c, "空目录").FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c))
	default:
		shown := preview.dirEntries
		extra := 0
		if len(shown) > mentionPreviewMaxDirRows {
			extra = len(shown) - mentionPreviewMaxDirRows
			shown = shown[:mentionPreviewMaxDirRows]
		}
		ui.Column(c.Key("mention-preview-dir-list")).FillWidth().Gap(Space1).Children(func() {
			for i, entry := range shown {
				icon := IconFile()
				name := entry.Name
				if entry.IsDir {
					icon = IconFolder()
					if !strings.HasSuffix(name, "/") {
						name += "/"
					}
				}
				ui.Row(c.Key(fmt.Sprintf("mention-preview-dir-item-%d", i))).
					FillWidth().
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, icon).Size(mentionMenuCrumbSepSize, mentionMenuCrumbSepSize).TextColor(ThemeTertiaryText(c))
						ui.Text(c, name).
							FontSize(uiFont(TypeCaption)).
							Font("monospace").
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Ellipsis("…").
							Shrink(1).
							MinWidth(0)
					})
			}
			if extra > 0 {
				ui.Text(c, fmt.Sprintf("… 另有 %d 项", extra)).
					FontSize(uiFont(TypeCaption)).
					Font("monospace").
					TextColor(ThemeTertiaryText(c))
			}
		})
	}
}

func isMentionPreviewUnsupportedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported") || strings.Contains(msg, "not supported")
}

func renderMentionFilePreview(c *ui.Context, item *completionItem, preview *mentionItemPreview) {
	meta := ""
	if item.Ignored {
		meta = ".gitignore"
	} else if preview != nil && !preview.loading && preview.err == "" {
		switch preview.kind {
		case "binary":
			meta = "二进制"
		case "image":
			meta = "图片"
		default:
			if len(preview.lines) > 0 {
				meta = strconv.Itoa(len(preview.lines)) + " 行"
			}
		}
	}
	renderMentionItemPreviewHeader(c, IconFile(), item.Path, meta, true)
	switch {
	case preview == nil || preview.loading:
		ui.Text(c, "正在加载…").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	case preview.err != "":
		ui.Text(c, "加载失败："+preview.err).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			MaxLines(4).
			Ellipsis("…")
	case preview.kind == "binary":
		ui.Text(c, "二进制文件，无法文本预览").FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c))
	case preview.kind == "image":
		ui.Text(c, "图片文件，无法文本预览").FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c))
	case preview.kind == "" && len(preview.lines) == 0 && preview.totalSize == 0 && preview.err == "":
		// 客户端不支持或空响应时保持空白。
	case len(preview.lines) == 0:
		ui.Text(c, "空文件").FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c))
	default:
		ui.Column(c.Key("mention-preview-code")).FillWidth().Gap(0).Clip().Children(func() {
			for i, line := range preview.lines {
				ui.Row(c.Key(fmt.Sprintf("mention-preview-code-line-%d", i))).
					FillWidth().
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, strconv.Itoa(i+1)).
							Width(mentionPreviewLineNumW).
							FontSize(uiFont(TypeCaption)).
							Font("monospace").
							TextColor(ThemeTertiaryText(c)).
							Shrink(0)
						ui.Text(c, line).
							FontSize(uiFont(TypeCaption)).
							Font("monospace").
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Ellipsis("…").
							Shrink(1).
							MinWidth(0)
					})
			}
		})
	}
}
