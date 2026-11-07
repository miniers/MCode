package mcodeui

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/projctx"

	"github.com/egoist/mygo/ui"
)

const (
	// ContextPanelWidth/Height 为项目文档弹窗的默认尺寸。
	ContextPanelWidth  float32 = 680
	ContextPanelHeight float32 = 600
	// contextSavedBadgeDuration 是「已保存」徽标的显示时长。
	contextSavedBadgeDuration = 2 * time.Second
	// contextGlobalPathErr 是全局层取不到用户主目录时的错误文案。
	contextGlobalPathErr = "无法确定用户主目录，全局文档不可用"
)

var (
	contextPanelMu     sync.Mutex
	contextPanelStates = make(map[*chatState]*contextPanelState)
)

// contextPanelCloseWait 是关闭弹窗时等待后台任务结束的上限：超时后放弃等待并记一条日志，
// 避免监听注册卡住时把关窗线程无限拖住。用变量而不是常量，方便测试调小。
var contextPanelCloseWait = 2 * time.Second

// contextPanelState 维护单个聊天窗口下的项目文档弹窗状态。
type contextPanelState struct {
	open         bool
	projectPath  string
	global       bool
	manifest     projctx.Manifest
	selected     string // 当前打开文档的文件名，空表示未加载任何文档
	section      string // 当前文档文件名
	editing      bool
	draft        string
	savedContent string
	baseHash     string
	dirty        bool
	conflict     bool
	conflictMsg  string
	err          string
	stopWatch    func()
	work         *sync.WaitGroup
	loading      bool
	generation   uint64
	selectionSeq uint64
	closed       bool
	saving       bool
	showSaved    bool
	savedSeq     uint64
	autoSave     *time.Timer
	drafts       map[string]contextDraft
	// 待确认的离开操作：leaveDoc 为目标文档，leaveClos 表示关闭弹窗。
	leaveOpen bool
	leaveDoc  string
	leaveTo   string
	leaveClos bool
	creating  bool
	newName   string
	// pendingToggle 为挂起的开关请求，由根层渲染时经 applyPendingContextToggle 消费
	// （项目同步与 watcher 注册需在渲染层执行）；打开状态 cp.open 在 toggle 时已同步改变。
	pendingToggle bool
}

type contextDraft struct {
	content, saved, hash string
}

type contextTarget struct {
	generation, selection uint64
}

func (cp *contextPanelState) target() contextTarget {
	return contextTarget{cp.generation, cp.selectionSeq}
}

func (cp *contextPanelState) accepts(targets []contextTarget) bool {
	return !cp.closed && (len(targets) == 0 || cp.target() == targets[0])
}

// 调用方持有 contextPanelMu；离开文档时保留尚未保存的草稿。
func (cp *contextPanelState) keepDraft() {
	if cp.autoSave != nil {
		cp.autoSave.Stop()
		cp.autoSave = nil
	}
	if cp.selected != "" {
		if cp.drafts == nil {
			cp.drafts = make(map[string]contextDraft)
		}
		key := filepath.Join(cp.activePath(), cp.selected)
		if cp.draft != cp.savedContent {
			cp.drafts[key] = contextDraft{cp.draft, cp.savedContent, cp.baseHash}
		} else {
			delete(cp.drafts, key)
		}
	}
}

func (cp *contextPanelState) setProject(path string) {
	if cp.projectPath == path {
		return
	}
	cp.keepDraft()
	cp.generation++
	cp.selectionSeq++
	cp.projectPath = path
	cp.global = false
	cp.manifest = projctx.Manifest{}
	cp.selected, cp.draft, cp.savedContent, cp.baseHash = "", "", "", ""
	cp.err, cp.conflictMsg, cp.newName = "", "", ""
	cp.dirty, cp.conflict = false, false
	cp.section, cp.creating, cp.leaveOpen, cp.leaveClos = "", false, false, false
}

// activePath 返回当前层的存储根目录；调用方持有 contextPanelMu。
func (cp *contextPanelState) activePath() string {
	if cp.global {
		home, _ := os.UserHomeDir()
		return home
	}
	return cp.projectPath
}

func contextRead(client Client, path, name string, global bool) (ContextDoc, error) {
	if !global {
		return client.ReadContextDoc(path, name)
	}
	svc, err := projctx.New(path)
	if err != nil {
		return ContextDoc{}, err
	}
	d, err := svc.ReadRef(name)
	return ContextDoc{Name: name, Content: d.Content, Hash: d.Hash, ModTime: d.ModTime}, err
}

func contextWrite(client Client, path, name, content, hash string, global bool) (ContextDoc, error) {
	if !global {
		return client.WriteContextDoc(path, name, content, hash)
	}
	svc, err := projctx.New(path)
	if err != nil {
		return ContextDoc{}, err
	}
	d, err := svc.WriteRef(name, content, hash)
	if errors.Is(err, projctx.ErrConflict) {
		return ContextDoc{}, ErrContextConflict
	}
	return ContextDoc{Name: name, Content: d.Content, Hash: d.Hash, ModTime: d.ModTime}, err
}

func contextCreate(client Client, path, name string, global bool) error {
	if !global {
		_, err := client.CreateContextDoc(path, name)
		return err
	}
	svc, err := projctx.New(path)
	if err != nil {
		return err
	}
	_, err = svc.CreateDoc(name)
	return err
}

func contextDocs(client Client, path string, global bool) ([]ContextDoc, error) {
	if !global {
		return client.ContextDocs(path)
	}
	svc, err := projctx.New(path)
	if err != nil {
		return nil, err
	}
	manifest, err := svc.Manifest()
	if err != nil {
		return nil, err
	}
	var docs []ContextDoc
	for _, info := range manifest.Docs {
		if !info.Exists {
			continue
		}
		name := contextDocRef(info)
		d, err := svc.ReadRef(name)
		if err != nil {
			return nil, err
		}
		docs = append(docs, ContextDoc{Name: name, Content: d.Content, Hash: d.Hash, ModTime: d.ModTime})
	}
	return docs, nil
}

func contextWatch(client Client, path string, global bool, change func()) (func(), error) {
	if !global {
		return client.WatchContextDocs(path, change)
	}
	svc, err := projctx.New(path)
	if err != nil {
		return nil, err
	}
	watcher, err := svc.Watch(change)
	if err != nil {
		return nil, err
	}
	return func() { _ = watcher.Close() }, nil
}

func (s *chatState) switchContextLayer(global bool) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if cp.global == global {
		contextPanelMu.Unlock()
		return
	}
	cp.keepDraft()
	cp.global = global
	cp.generation++
	cp.selectionSeq++
	cp.manifest = projctx.Manifest{}
	cp.selected, cp.section, cp.draft, cp.savedContent, cp.baseHash = "", "", "", "", ""
	cp.dirty, cp.conflict = false, false
	cp.err, cp.conflictMsg = "", ""
	cp.creating, cp.leaveOpen = false, false
	contextPanelMu.Unlock()
	s.syncContextProject(true)
	s.invalidate()
}

// getContextPanelState 获取或初始化当前 chatState 的项目文档面板状态。
func getContextPanelState(s *chatState) *contextPanelState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	st, ok := contextPanelStates[s]
	if !ok {
		st = &contextPanelState{editing: true, work: &sync.WaitGroup{}}
		contextPanelStates[s] = st
	}
	return st
}

// IsOpen 返回项目文档弹窗当前是否显示。
func (st *contextPanelState) IsOpen() bool {
	if st == nil {
		return false
	}
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	return st.open
}

// getSelectedProjectPath 解析当前选中会话所关联的项目根路径。
func (s *chatState) getSelectedProjectPath() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID := s.selectedThreadID
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
				return p.Path
			}
		}
	}
	if threadID == "" && len(s.projects) > 0 {
		return s.projects[0].Path
	}
	return ""
}

// manifestFromDocs 把 Client 返回的文档列表整理为 Manifest：根部文件名（instructions.md）
// 归为四篇标准文档（缺失时先用内置元信息补齐，Exists=false），docs/ 引用归为自定义文档。
func manifestFromDocs(docs []ContextDoc) projctx.Manifest {
	standards := projctx.StandardDocs()
	m := projctx.Manifest{Docs: make([]projctx.DocInfo, 0, len(docs)+len(standards))}
	byFile := make(map[string]ContextDoc, len(docs))
	customs := make([]ContextDoc, 0, len(docs))
	for _, d := range docs {
		// Client 返回自定义文档的引用形如 docs/x.md，与标准文档同处一个列表。
		if strings.HasPrefix(d.Name, projctx.DocsDir+"/") {
			d.Name = strings.TrimPrefix(d.Name, projctx.DocsDir+"/")
			customs = append(customs, d)
			continue
		}
		byFile[d.Name] = d
	}
	for _, std := range standards {
		info := projctx.DocInfo{
			Name:     std.Name,
			File:     std.File,
			Title:    std.Title,
			Subtitle: std.Subtitle,
			Icon:     std.Icon,
			Standard: true,
		}
		if d, ok := byFile[std.File]; ok {
			info.Exists = true
			info.Hash = d.Hash
			info.Size = len(d.Content)
			info.ModTime = d.ModTime
		}
		m.Docs = append(m.Docs, info)
	}
	for _, d := range customs {
		if d.Name == "" {
			continue
		}
		m.Docs = append(m.Docs, projctx.DocInfo{
			Name:     strings.TrimSuffix(d.Name, ".md"),
			File:     d.Name,
			Title:    projctx.TitleOf(d.Name),
			Subtitle: "自定义文档 · docs/",
			Icon:     projctx.IconFile,
			Exists:   true,
			Hash:     d.Hash,
			Size:     len(d.Content),
			ModTime:  d.ModTime,
		})
	}
	return m
}

// loadContextDocsSync 同步刷新指定项目的项目文档清单（标准文档 + 自定义文档）。
func loadContextDocsSync(s *chatState, projectPath string, targets ...contextTarget) error {
	if s == nil || s.client == nil {
		return nil
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return nil
	}

	contextPanelMu.Lock()
	if !cp.accepts(targets) {
		contextPanelMu.Unlock()
		return nil
	}
	cp.setProject(projectPath)
	path, global := cp.activePath(), cp.global
	generation := cp.generation
	cp.loading = true
	cp.err = ""
	contextPanelMu.Unlock()

	if strings.TrimSpace(path) == "" {
		contextPanelMu.Lock()
		cp.loading = false
		cp.manifest = projctx.Manifest{}
		// 全局层只有取到用户主目录才能定位 ~/.mcode/context：取不到时把原因写进错误条。
		if global {
			cp.err = contextGlobalPathErr
		}
		contextPanelMu.Unlock()
		return nil
	}

	docs, err := contextDocs(s.client, path, global)

	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if cp.closed || cp.generation != generation {
		return nil
	}
	cp.loading = false
	if err != nil {
		cp.err = err.Error()
		return err
	}
	cp.manifest = manifestFromDocs(docs)
	return nil
}

// contextDocFile 把文档名或引用规整为项目文档引用，读取、保存与 section 都用它：标准文档为根部文件名
// （instructions.md 等），自定义文档为 docs/x.md，与 ContextDocs 返回的 Name 一致。
// 注意 projctx.ParseRef 会先去掉 docs/ 再匹配标准名，所以 docs/notes.md 会被当作根部 notes.md。
func contextDocFile(name string) string {
	if ref, err := projctx.ParseRef(name); err == nil {
		if ref.Standard {
			return ref.Name
		}
		return projctx.DocsDir + "/" + ref.Name
	}
	return name
}

// contextDocRef 返回 DocInfo 的项目文档引用，与 contextDocFile 的结果一致（chip 高亮与切换都用它比较 section）。
func contextDocRef(doc projctx.DocInfo) string {
	if doc.Standard {
		return doc.File
	}
	return doc.RefID()
}

// selectContextDocSync 同步读取并选中文档，设置草稿内容与基础哈希。
// 标准文档尚未创建时展示内置模板（baseHash 为空），首次保存即落盘。
func selectContextDocSync(s *chatState, name string, targets ...contextTarget) error {
	if s == nil || s.client == nil {
		return nil
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return nil
	}
	file := contextDocFile(name)

	contextPanelMu.Lock()
	if !cp.accepts(targets) {
		contextPanelMu.Unlock()
		return nil
	}
	path := cp.activePath()
	global := cp.global
	generation := cp.generation
	if len(targets) == 0 {
		cp.selectionSeq++
	}
	seq := cp.selectionSeq
	contextPanelMu.Unlock()

	if path == "" || file == "" {
		return nil
	}

	doc, err := readContextDocOrDefault(s.client, path, file, global)

	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if cp.closed || cp.generation != generation || cp.selectionSeq != seq {
		return nil
	}
	if err != nil {
		cp.err = err.Error()
		return err
	}
	cp.keepDraft()
	cp.selected = file
	cp.section = file
	cp.draft = doc.Content
	cp.savedContent = doc.Content
	cp.baseHash = doc.Hash
	cp.dirty = false
	cp.conflict = false
	cp.conflictMsg = ""
	cp.err = ""
	if draft, ok := cp.drafts[filepath.Join(path, file)]; ok {
		cp.draft, cp.savedContent, cp.baseHash = draft.content, draft.saved, draft.hash
		cp.dirty = cp.draft != cp.savedContent
		cp.conflict = doc.Hash != cp.baseHash
	}
	return nil
}

// selectContextDoc 异步选中文档。
func (s *chatState) selectContextDoc(name string) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if cp.closed {
		contextPanelMu.Unlock()
		return
	}
	cp.selectionSeq++
	target := cp.target()
	cp.work.Add(1)
	contextPanelMu.Unlock()
	go func(n string) {
		defer cp.work.Done()
		_ = selectContextDocSync(s, n, target)
		s.invalidate()
	}(name)
}

// switchContextSection 切换文档 chip。有未保存修改时先确认。
func (s *chatState) switchContextSection(ref string) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if cp.section == ref {
		contextPanelMu.Unlock()
		return
	}
	if cp.dirty {
		cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = true, cp.selected, ref, false
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	cp.section = ref
	contextPanelMu.Unlock()
	s.invalidate()
	s.selectContextDoc(ref)
}

// discardDraftLocked 丢弃当前文档的本地草稿，调用方持有 contextPanelMu。
func (cp *contextPanelState) discardDraftLocked() {
	if cp.selected == "" {
		return
	}
	delete(cp.drafts, filepath.Join(cp.activePath(), cp.selected))
	if cp.autoSave != nil {
		cp.autoSave.Stop()
		cp.autoSave = nil
	}
	cp.dirty = false
	cp.draft = cp.savedContent
}

// resolveContextLeave 处理「保存 / 放弃 / 取消」：save 为真时先保存，
// 保存失败（冲突或写错误）则保持弹窗不动。
func (s *chatState) resolveContextLeave(save bool) {
	if !save {
		cp := getContextPanelState(s)
		if cp == nil {
			return
		}
		contextPanelMu.Lock()
		cp.discardDraftLocked()
		contextPanelMu.Unlock()
		s.applyPendingContextLeave()
		return
	}
	s.contextAction(func(target contextTarget) error {
		if err := writeContextDocSync(s, false, target); err != nil {
			return nil
		}
		s.applyPendingContextLeave()
		return nil
	})
}

// applyPendingContextLeave 执行待确认的切换或关闭；草稿仍为脏时不做任何事。
func (s *chatState) applyPendingContextLeave() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	to, closing := cp.leaveTo, cp.leaveClos
	leaveOpen := cp.leaveOpen
	if leaveOpen && cp.dirty {
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = false, "", "", false
	if leaveOpen && !closing {
		cp.section = to
	}
	contextPanelMu.Unlock()

	if !leaveOpen {
		return
	}
	if closing {
		s.closeContextPanel()
		return
	}
	if to != "" {
		s.selectContextDoc(to)
	}
	s.invalidate()
}

// cancelContextLeave 取消待确认的切换或关闭。
func (s *chatState) cancelContextLeave() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = false, "", "", false
	contextPanelMu.Unlock()
	s.invalidate()
}

// readContextDocOrDefault 读取文档；四篇标准文档缺失时按 projctx 模板返回（Hash 为空，
// 首次保存即落盘）。自定义文档缺失仍返回错误，与 projctx.ReadOrDefault 的行为一致。
func readContextDocOrDefault(client Client, projectPath, file string, global bool) (ContextDoc, error) {
	doc, err := contextRead(client, projectPath, file, global)
	if err == nil || !isContextDocMissing(err) {
		return doc, err
	}
	template, ok := projctx.TemplateFor(file)
	if !ok {
		return ContextDoc{}, err
	}
	return ContextDoc{Name: file, Content: template}, nil
}

// isContextDocMissing 判断读取错误是否表示文档尚未创建（标准文档首次打开时的正常情况）。
// 产品客户端与 fake 都包装 projctx.ErrNotFound。
func isContextDocMissing(err error) bool {
	return errors.Is(err, projctx.ErrNotFound)
}

// saveContextDocSync 同步保存当前文档草稿（CAS）。
func saveContextDocSync(s *chatState) error {
	return writeContextDocSync(s, false)
}

// writeContextDocSync 同步调用 Client.WriteContextDoc 写回草稿并更新 baseHash。
// overwrite 为真时以空哈希强制覆盖。冲突时置 conflict 并返回 ErrContextConflict。
func writeContextDocSync(s *chatState, overwrite bool, targets ...contextTarget) error {
	if s == nil || s.client == nil {
		return nil
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return nil
	}

	contextPanelMu.Lock()
	if !cp.accepts(targets) {
		contextPanelMu.Unlock()
		return nil
	}
	path := cp.activePath()
	global := cp.global
	selected := cp.selected
	draft := cp.draft
	baseHash := cp.baseHash
	generation, seq := cp.generation, cp.selectionSeq
	if cp.closed || cp.saving || (!overwrite && cp.conflict) {
		conflict := cp.conflict
		contextPanelMu.Unlock()
		if conflict {
			return ErrContextConflict
		}
		return nil
	}
	if overwrite {
		baseHash = ""
	}
	cp.saving = true
	contextPanelMu.Unlock()

	if path == "" || selected == "" {
		contextPanelMu.Lock()
		cp.saving = false
		contextPanelMu.Unlock()
		return nil
	}

	doc, err := contextWrite(s.client, path, selected, draft, baseHash, global)

	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	cp.saving = false
	if cp.closed || cp.generation != generation || cp.selectionSeq != seq {
		// 保存完成前已离开该文档，仍需更新它的草稿基线。
		key := filepath.Join(path, selected)
		if cached, ok := cp.drafts[key]; ok && err == nil {
			cached.saved, cached.hash = doc.Content, doc.Hash
			cp.drafts[key] = cached
		}
		return err
	}
	if err != nil {
		if errors.Is(err, ErrContextConflict) {
			cp.conflict = true
			cp.conflictMsg = "文件已被外部修改，存在并发冲突"
		} else {
			cp.err = err.Error()
		}
		return err
	}

	cp.baseHash = doc.Hash
	cp.savedContent = doc.Content
	cp.dirty = cp.draft != doc.Content
	cp.conflict = false
	cp.conflictMsg = ""
	cp.err = ""
	if !cp.dirty {
		delete(cp.drafts, filepath.Join(path, selected))
	}
	cp.savedSeq++
	cp.showSaved = true
	scheduleSavedBadgeClear(s, cp, cp.savedSeq)
	return nil
}

// scheduleSavedBadgeClear 在 2 秒后清除「已保存」徽标。
func scheduleSavedBadgeClear(s *chatState, cp *contextPanelState, seq uint64) {
	time.AfterFunc(contextSavedBadgeDuration, func() {
		contextPanelMu.Lock()
		if cp.closed || cp.savedSeq != seq {
			contextPanelMu.Unlock()
			return
		}
		cp.showSaved = false
		contextPanelMu.Unlock()
		s.invalidate()
	})
}

// reloadContextDocSync 同步重新拉取当前文档内容，覆盖本地草稿并清除冲突状态。
func reloadContextDocSync(s *chatState, targets ...contextTarget) error {
	if s == nil || s.client == nil {
		return nil
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return nil
	}

	contextPanelMu.Lock()
	if !cp.accepts(targets) {
		contextPanelMu.Unlock()
		return nil
	}
	path := cp.activePath()
	global := cp.global
	selected := cp.selected
	generation, seq, draft := cp.generation, cp.selectionSeq, cp.draft
	contextPanelMu.Unlock()

	if path == "" || selected == "" {
		return nil
	}

	doc, err := readContextDocOrDefault(s.client, path, selected, global)

	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if cp.closed || cp.generation != generation || cp.selectionSeq != seq || cp.draft != draft {
		return nil
	}
	if err != nil {
		cp.err = err.Error()
		return err
	}

	cp.draft = doc.Content
	cp.savedContent = doc.Content
	cp.baseHash = doc.Hash
	cp.dirty = false
	cp.conflict = false
	cp.conflictMsg = ""
	cp.err = ""
	delete(cp.drafts, filepath.Join(path, selected))
	return nil
}

// overwriteContextDocSync 同步以 expectedHash 为空强制覆盖写回当前文档并清除冲突。
func overwriteContextDocSync(s *chatState) error {
	return writeContextDocSync(s, true)
}

// dismissContextError 关闭保存失败横幅。
func (s *chatState) dismissContextError() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	cp.err = ""
	contextPanelMu.Unlock()
	s.invalidate()
}

// editContextDraft 更新草稿并按 400ms 防抖自动保存。
func (s *chatState) editContextDraft(content string, targets ...contextTarget) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if !cp.accepts(targets) {
		return
	}
	cp.draft = content
	cp.dirty = content != cp.savedContent
	s.scheduleContextSaveLocked(cp)
}

// 调用方持有 contextPanelMu。
func (s *chatState) scheduleContextSaveLocked(cp *contextPanelState) {
	if cp.autoSave != nil {
		cp.autoSave.Stop()
	}
	target := cp.target()
	content := cp.draft
	cp.autoSave = time.AfterFunc(400*time.Millisecond, func() {
		contextPanelMu.Lock()
		current := cp.accepts([]contextTarget{target}) && cp.open &&
			cp.draft == content && cp.dirty && !cp.conflict
		if current {
			cp.work.Add(1)
		}
		contextPanelMu.Unlock()
		if current {
			defer cp.work.Done()
			_ = writeContextDocSync(s, false, target)
			s.invalidate()
		}
	})
}

// saveContextDoc 异步保存当前文档草稿。
func (s *chatState) saveContextDoc() {
	s.contextAction(func(target contextTarget) error { return writeContextDocSync(s, false, target) })
}

// reloadContextDoc 异步重新加载当前文档。
func (s *chatState) reloadContextDoc() {
	s.contextAction(func(target contextTarget) error { return reloadContextDocSync(s, target) })
}

// overwriteContextDoc 异步强制覆盖保存当前文档。
func (s *chatState) overwriteContextDoc() {
	s.contextAction(func(target contextTarget) error { return writeContextDocSync(s, true, target) })
}

func (s *chatState) contextAction(action func(contextTarget) error) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if cp.closed {
		contextPanelMu.Unlock()
		return
	}
	target := cp.target()
	cp.work.Add(1)
	contextPanelMu.Unlock()
	go func() {
		defer cp.work.Done()
		_ = action(target)
		s.invalidate()
	}()
}

// requestCreateContextDoc 打开「新建自定义文档」对话框。
func (s *chatState) requestCreateContextDoc() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	cp.creating, cp.newName = true, ""
	contextPanelMu.Unlock()
	s.invalidate()
}

// cancelCreateContextDoc 关闭「新建自定义文档」对话框。
func (s *chatState) cancelCreateContextDoc() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	cp.creating, cp.newName = false, ""
	contextPanelMu.Unlock()
	s.invalidate()
}

// createContextDoc 按对话框中的名称新建自定义文档（已存在时报错并保持对话框）。
func (s *chatState) createContextDoc() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if cp.closed {
		contextPanelMu.Unlock()
		return
	}
	name, target := cp.newName, cp.target()
	cp.work.Add(1)
	contextPanelMu.Unlock()
	go func() {
		defer cp.work.Done()
		err := createContextDocSync(s, name, target)
		contextPanelMu.Lock()
		if cp.accepts([]contextTarget{target}) {
			if err != nil {
				cp.err = err.Error()
			} else {
				cp.creating, cp.newName = false, ""
			}
		}
		contextPanelMu.Unlock()
		s.invalidate()
	}()
}

// createContextDocSync 在 docs/ 下按内置模板新建自定义文档并选中它。
func createContextDocSync(s *chatState, name string, targets ...contextTarget) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("文档名称不能为空")
	}
	normalized, ok := projctx.NormalizeName(name)
	if !ok {
		return fmt.Errorf("「%s」不是合法的文档名：请使用简单的 .md 文件名，不含文件夹或「..」", name)
	}
	cp := getContextPanelState(s)
	if cp == nil || s.client == nil {
		return nil
	}
	contextPanelMu.Lock()
	if !cp.accepts(targets) {
		contextPanelMu.Unlock()
		return nil
	}
	path := cp.activePath()
	global := cp.global
	projectPath := cp.projectPath
	target := cp.target()
	contextPanelMu.Unlock()
	if path == "" {
		return nil
	}
	if err := contextCreate(s.client, path, projctx.DocsDir+"/"+normalized, global); err != nil {
		return err
	}
	if err := loadContextDocsSync(s, projectPath, target); err != nil {
		return err
	}
	contextPanelMu.Lock()
	if !cp.accepts([]contextTarget{target}) {
		contextPanelMu.Unlock()
		return nil
	}
	cp.selectionSeq++
	target = cp.target()
	contextPanelMu.Unlock()
	if err := selectContextDocSync(s, projctx.DocsDir+"/"+normalized, target); err != nil {
		return err
	}
	contextPanelMu.Lock()
	if cp.accepts([]contextTarget{target}) {
		cp.editing = true
	}
	contextPanelMu.Unlock()
	return nil
}

// handleContextDocChange 刷新清单，保留草稿并提示外部修改或删除。
func (s *chatState) handleContextDocChange() {
	if s == nil || s.client == nil {
		return
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}

	contextPanelMu.Lock()
	path := cp.activePath()
	global := cp.global
	selected := cp.selected
	generation, seq := cp.generation, cp.selectionSeq
	baseHash := cp.baseHash
	contextPanelMu.Unlock()

	if path == "" {
		return
	}

	docs, err := contextDocs(s.client, path, global)
	if err != nil {
		contextPanelMu.Lock()
		if !cp.closed && cp.generation == generation {
			cp.err = err.Error()
		}
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	manifest := manifestFromDocs(docs)

	var currentDoc *ContextDoc
	for _, d := range docs {
		if d.Name == selected {
			cd := d
			currentDoc = &cd
			break
		}
	}

	contextPanelMu.Lock()
	if cp.closed || cp.generation != generation || cp.selectionSeq != seq || cp.baseHash != baseHash {
		contextPanelMu.Unlock()
		return
	}
	cp.manifest = manifest
	// 外部修改或删除时提示冲突，绝不静默替换正在浏览/编辑的内容。
	if selected != "" && (currentDoc == nil || currentDoc.Hash != cp.baseHash) {
		cp.conflict = true
		cp.conflictMsg = "文件已被外部修改，存在并发冲突"
	}
	contextPanelMu.Unlock()

	s.invalidate()
}

// toggleContextPanel 切换项目文档弹窗：关闭前若有未保存修改，先确认后再关闭。
// 快捷键 ⇧⌘K 与标题栏按钮都通过它打开/关闭弹窗；打开状态在此同步改变，
// 项目同步与文件监听因涉及异步 IO 留给根层渲染时消费（见 applyPendingContextToggle）。
func (s *chatState) toggleContextPanel() {
	if s == nil {
		return
	}
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	if !cp.open {
		if cp.global {
			cp.keepDraft()
			cp.global = false
			cp.selected, cp.section, cp.draft, cp.savedContent, cp.baseHash = "", "", "", "", ""
			cp.dirty, cp.conflict = false, false
			cp.manifest = projctx.Manifest{}
		}
		cp.open = true
		cp.pendingToggle = true
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	if cp.dirty {
		// 不关闭：弹出「保存 / 放弃 / 取消」确认条，取消后保持打开。
		cp.pendingToggle = false
		cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = true, cp.selected, "", true
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	contextPanelMu.Unlock()
	s.closeContextPanel()
}

// applyPendingContextToggle 在根层渲染时消费挂起的打开请求：绑定项目并注册文件监听。
func (s *chatState) applyPendingContextToggle() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}

	contextPanelMu.Lock()
	pending := cp.pendingToggle
	if pending {
		cp.pendingToggle = false
	}
	open := cp.open
	contextPanelMu.Unlock()
	if !pending || !open {
		return
	}
	if s.syncContextProject(true) {
		return
	}
	// 窗口已在关闭：撤掉刚打开的弹窗状态，避免残留 watcher。
	s.closeContextPanel()
}

// cancelPendingContextToggle 丢弃尚未消费的开关请求（窗口关闭时，调用方持有 contextPanelMu）。
func (cp *contextPanelState) cancelPendingContextToggle() {
	if cp != nil {
		cp.pendingToggle = false
	}
}

// syncContextProject 打开弹窗时绑定项目、注册监听并加载清单；
// 项目切换时重新绑定，旧项目的草稿不会写入新项目。返回是否已绑定（窗口关闭时返回 false）。
func (s *chatState) syncContextProject(force bool) bool {
	cp := getContextPanelState(s)
	if cp == nil || s.client == nil {
		return false
	}
	projectPath := s.getSelectedProjectPath()
	contextPanelMu.Lock()
	if cp.closed || !cp.open || (!force && cp.projectPath == projectPath) {
		contextPanelMu.Unlock()
		return false
	}
	stop := cp.stopWatch
	cp.stopWatch = nil
	cp.setProject(projectPath)
	path, global := cp.activePath(), cp.global
	cp.generation++
	generation := cp.generation
	cp.loading = path != ""
	cp.work.Add(1)
	contextPanelMu.Unlock()
	if stop != nil {
		stop()
	}
	go func() {
		defer cp.work.Done()
		if path == "" {
			if global {
				contextPanelMu.Lock()
				if !cp.closed && cp.generation == generation {
					cp.err = contextGlobalPathErr
				}
				contextPanelMu.Unlock()
			}
			s.invalidate()
			return
		}
		// 每次打开都重新读取磁盘：弹窗关闭期间目录可能已变化。
		_, loadErr := contextDocs(s.client, path, global)
		stop, watchErr := contextWatch(s.client, path, global, func() {
			// Watcher 持锁调用回调，不能在回调内等待 Close。
			contextPanelMu.Lock()
			if cp.closed {
				contextPanelMu.Unlock()
				return
			}
			cp.work.Add(1)
			contextPanelMu.Unlock()
			go func() {
				defer cp.work.Done()
				s.handleContextDocChange()
			}()
		})
		contextPanelMu.Lock()
		if cp.closed || cp.generation != generation {
			contextPanelMu.Unlock()
			if stop != nil {
				stop()
			}
			return
		}
		cp.stopWatch = stop
		cp.loading = false
		if loadErr != nil {
			cp.err = loadErr.Error()
		}
		if watchErr != nil {
			cp.err = "无法监听外部修改：" + watchErr.Error()
		}
		contextPanelMu.Unlock()
		s.handleContextDocChange()
		s.invalidate()
	}()
	return true
}

func closeContextPanel(s *chatState) {
	contextPanelMu.Lock()
	cp := contextPanelStates[s]
	var stop func()
	if cp != nil {
		cp.cancelPendingContextToggle()
		cp.closed = true
		cp.keepDraft()
		stop = cp.stopWatch
		delete(contextPanelStates, s)
	}
	contextPanelMu.Unlock()
	if cp == nil {
		return
	}
	// 注销监听与等待后台任务都放到 goroutine 里：监听注册可能正卡在客户端调用上，
	// 关窗线程不能无限等它，正常情况仍然会等到后台任务结束。
	done := make(chan struct{})
	go func() {
		defer close(done)
		if stop != nil {
			stop()
		}
		cp.work.Wait()
	}()
	timer := time.NewTimer(contextPanelCloseWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("关闭项目文档面板：后台任务在 %s 内没有结束，放弃等待", contextPanelCloseWait.String())
	}
}

// 在项目内打开项目文档目录或文档（Finder / 外部编辑器）。测试可替换。
var (
	revealContextItem      = openItemInFinder
	openContextDocInEditor = openMiniEditorPath
)

// mapContextIcon 把 projctx 的图标 id 映射到 icons.go 中已有的图标。
func mapContextIcon(id string) *ui.SVG {
	switch id {
	case projctx.IconShield:
		return IconScrollText()
	case projctx.IconMap:
		return IconBookOpenText()
	case projctx.IconSeal:
		return IconSealCheck()
	case projctx.IconNote:
		return IconNote()
	default:
		return IconFile()
	}
}

// closeContextPanel 关闭弹窗并注销文件监听，同时保留未保存的草稿。
func (s *chatState) closeContextPanel() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	stop := cp.stopWatch
	cp.stopWatch = nil
	cp.open = false
	cp.cancelPendingContextToggle()
	cp.generation++
	cp.keepDraft()
	cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = false, "", "", false
	cp.creating = false
	contextPanelMu.Unlock()
	if stop != nil {
		stop()
	}
	s.invalidate()
}
