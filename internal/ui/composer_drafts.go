package mcodeui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/fsutil"
)

// 草稿按会话隔离并在重启后恢复文本。
// 内存中每个会话的草稿包含文本与附件；附件依赖后端上传 ID，只在本进程内保留，
// 磁盘格式（composer-drafts.json）只记录文本。

// composerDraftsFileName 是草稿文件名，保存在 mcode 数据目录（默认 ~/.mcode）下。
const composerDraftsFileName = "composer-drafts.json"

// composerDraftSaveDebounce 是草稿落盘防抖时长：连续输入只在停顿后写一次盘。
const composerDraftSaveDebounce = time.Second

// composerDraftsWriteMu 串行化草稿落盘：快照在锁内读取，保证后写入的一定是更新的内容，
// 避免防抖定时器、切换会话与窗口关闭几路写盘乱序后用旧快照覆盖新快照。
var composerDraftsWriteMu sync.Mutex

// composerDraft 是单个会话的草稿内存态；threadID 为空字符串表示新会话页。
type composerDraft struct {
	Text        string
	Attachments []composerAttachment // 已上传附件及待完成的上下文引用，仅内存保留，不落盘
	Snippets    []composerSnippet    // 长文本粘贴卡片；落盘时并入 Text，重启后以原文回到输入框
}

// composerDraftRecord 是单条草稿的磁盘格式，只持久化文本。
type composerDraftRecord struct {
	Text string `json:"text"`
}

// composerDraftsFile 是 composer-drafts.json 的顶层结构。
type composerDraftsFile struct {
	Drafts map[string]composerDraftRecord `json:"drafts"`
}

// composerDraftsPath 返回草稿文件路径；找不到数据目录时返回空字符串。
func composerDraftsPath() string {
	dir := resolveDataDirFallback()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, composerDraftsFileName)
}

// loadComposerDrafts 读取草稿文件；文件缺失或损坏时返回空 map。
func loadComposerDrafts() map[string]string {
	drafts := make(map[string]string)
	path := composerDraftsPath()
	if path == "" {
		return drafts
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return drafts
	}
	var parsed composerDraftsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return drafts
	}
	for id, rec := range parsed.Drafts {
		drafts[id] = rec.Text
	}
	return drafts
}

// saveComposerDrafts 写入草稿文件：先写同目录临时文件，再重命名覆盖，避免半截文件。
// map 为空时同样写出（{"drafts":{}}），保证"清空草稿"这一动作也落盘。
func saveComposerDrafts(m map[string]string) error {
	path := composerDraftsPath()
	if path == "" {
		return errors.New("未找到 mcode 数据目录")
	}
	file := composerDraftsFile{Drafts: make(map[string]composerDraftRecord, len(m))}
	for id, text := range m {
		file.Drafts[id] = composerDraftRecord{Text: text}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// applyComposerDrafts 把持久化的草稿文本填入 state（仅文本，附件不落盘），窗口构建时调用一次。
func applyComposerDrafts(s *chatState, drafts map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts == nil {
		s.drafts = make(map[string]composerDraft)
	}
	for id, text := range drafts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		entry := s.drafts[id]
		entry.Text = text
		s.drafts[id] = entry
	}
}

// draftAttachments 保留已上传附件，以及需要在来源会话继续上传或重试的上下文引用。
func draftAttachments(atts []composerAttachment) []composerAttachment {
	var out []composerAttachment
	for _, att := range atts {
		if att.InspectorRef == nil && (att.ID == "" || att.Uploading || att.Err != "") {
			continue
		}
		out = append(out, att)
	}
	return out
}

// stashDraftLocked 把会话草稿写回草稿表；上下文引用保留上传与失败状态，便于返回来源会话重试。
// threadID 为空字符串表示新会话页。调用方需持有 s.mu。
func (s *chatState) stashDraftLocked(threadID, text string, atts []composerAttachment, snippets []composerSnippet) {
	if s.drafts == nil {
		s.drafts = make(map[string]composerDraft)
	}
	uploaded := draftAttachments(atts)
	if strings.TrimSpace(text) == "" && len(uploaded) == 0 && len(snippets) == 0 {
		delete(s.drafts, threadID)
		return
	}
	s.drafts[threadID] = composerDraft{Text: text, Attachments: uploaded, Snippets: append([]composerSnippet(nil), snippets...)}
}

// takeDraftLocked 读取会话草稿并复制附件与卡片切片；条目缺失时返回零值。调用方需持有 s.mu。
func (s *chatState) takeDraftLocked(threadID string) composerDraft {
	d := s.drafts[threadID]
	d.Attachments = append([]composerAttachment(nil), d.Attachments...)
	d.Snippets = append([]composerSnippet(nil), d.Snippets...)
	return d
}

// clearDraftLocked 删除会话草稿条目。调用方需持有 s.mu。
func (s *chatState) clearDraftLocked(threadID string) {
	delete(s.drafts, threadID)
}

// snapshotDraftTextsLocked 拷贝草稿文本表用于落盘，粘贴卡片并入文本：重启后卡片没了，原文还在。
// 调用方需持有 s.mu。
func (s *chatState) snapshotDraftTextsLocked() map[string]string {
	m := make(map[string]string, len(s.drafts))
	for id, d := range s.drafts {
		m[id] = composeWithSnippets(d.Text, d.Snippets)
	}
	return m
}

// persistDraftsAsync 把草稿文本写入磁盘（写盘在后台 goroutine）；未开启界面状态持久化时不写盘。
// 内部自行加锁，调用方不得持锁调用。
func (s *chatState) persistDraftsAsync() {
	if s == nil || !uiStatePersistenceEnabled(s) {
		return
	}
	go func() {
		composerDraftsWriteMu.Lock()
		defer composerDraftsWriteMu.Unlock()
		s.mu.Lock()
		texts := s.snapshotDraftTextsLocked()
		s.mu.Unlock()
		if err := saveComposerDrafts(texts); err != nil {
			s.setToast("保存草稿失败: " + err.Error())
		}
	}()
}

// flushComposerDrafts 在窗口关闭时同步保存草稿：把当前输入框文本写回草稿表后落盘（非 goroutine）。
// 只在开启界面状态持久化的窗口生效。
func (s *chatState) flushComposerDrafts() {
	if s == nil || !uiStatePersistenceEnabled(s) {
		return
	}
	s.mu.Lock()
	text := s.draft
	atts := s.attachments
	snippets := s.snippets
	if s.editingFollowUpID != "" {
		// 正在编辑排队项时，输入框里是排队项文本，用户自己的草稿在暂存字段里。
		text = s.editStashDraft
		atts = s.editStashAttachments
		snippets = s.editStashSnippets
	}
	s.stashDraftLocked(s.selectedThreadID, text, atts, snippets)
	s.mu.Unlock()
	// 窗口正在关闭，写盘失败只忽略，不再弹 Toast。
	composerDraftsWriteMu.Lock()
	defer composerDraftsWriteMu.Unlock()
	s.mu.Lock()
	texts := s.snapshotDraftTextsLocked()
	s.mu.Unlock()
	_ = saveComposerDrafts(texts)
}

// noteComposerDraft 记录输入框文本（UI 帧调用）：更新对应会话草稿的文本并保留其附件，
// 1 秒防抖后落盘；文本与已记录相同时不做任何事。持久化关闭时仍更新内存，只是不写盘。
func (s *chatState) noteComposerDraft(threadID, text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.drafts == nil {
		s.drafts = make(map[string]composerDraft)
	}
	entry := s.drafts[threadID]
	if entry.Text == text {
		s.mu.Unlock()
		return
	}
	entry.Text = text
	if strings.TrimSpace(text) == "" && len(draftAttachments(entry.Attachments)) == 0 && len(entry.Snippets) == 0 {
		delete(s.drafts, threadID)
	} else {
		s.drafts[threadID] = entry
	}
	if s.draftSaveTimer != nil {
		s.draftSaveTimer.Stop()
	}
	s.draftSaveTimer = time.AfterFunc(composerDraftSaveDebounce, s.persistDraftsAsync)
	s.mu.Unlock()
	// 待创建会话在首个非空输入时真正创建（草稿有内容后会话才出现在列表）。
	if threadID == "" && pendingDraftHasContent(text) {
		s.createPendingThread(nil)
	}
}
