package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// TestComposerDraftsSaveLoadRoundTrip 验证 composer-drafts.json 的保存与读取往返。
func TestComposerDraftsSaveLoadRoundTrip(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)

	want := map[string]string{"t1": "草稿A", "": "新会话草稿"}
	if err := saveComposerDrafts(want); err != nil {
		t.Fatalf("saveComposerDrafts failed: %v", err)
	}
	path := filepath.Join(dataDir, composerDraftsFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s written: %v", composerDraftsFileName, err)
	}
	if !strings.Contains(string(raw), `"drafts"`) || !strings.Contains(string(raw), `"text"`) {
		t.Fatalf(`磁盘结构应为 {"drafts": {id: {"text": ...}}}, got %s`, raw)
	}
	got := loadComposerDrafts()
	if len(got) != len(want) || got["t1"] != "草稿A" || got[""] != "新会话草稿" {
		t.Fatalf("loadComposerDrafts = %+v, want %+v", got, want)
	}

	// 清空草稿也要写出文件（而不是删文件）
	if err := saveComposerDrafts(map[string]string{}); err != nil {
		t.Fatalf("saveComposerDrafts(empty) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("空 map 也应写出文件: %v", err)
	}
	if got := loadComposerDrafts(); len(got) != 0 {
		t.Fatalf("清空后应读回空 map, got %+v", got)
	}
}

// TestComposerDraftsLoadMissingOrCorrupt 验证草稿文件缺失或损坏时回落到空 map。
func TestComposerDraftsLoadMissingOrCorrupt(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)

	got := loadComposerDrafts()
	if got == nil || len(got) != 0 {
		t.Fatalf("缺失文件时应返回可写的空 map, got %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dataDir, composerDraftsFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadComposerDrafts(); got == nil || len(got) != 0 {
		t.Fatalf("损坏文件时应返回空 map, got %+v", got)
	}
}

// TestComposerDraftsStashTakeClear 验证草稿表写入/读取/清空语义与附件过滤。
func TestComposerDraftsStashTakeClear(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	uploaded := composerAttachment{LocalID: "loc-1", ID: "up-1", Name: "a.txt"}
	uploading := composerAttachment{LocalID: "loc-2", Uploading: true}
	failed := composerAttachment{LocalID: "loc-3", ID: "up-3", Err: "boom"}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 空白文本且没有已上传附件：不留下条目
	s.stashDraftLocked("t1", "   ", []composerAttachment{uploading, failed}, nil)
	if _, ok := s.drafts["t1"]; ok {
		t.Fatalf("空草稿不应留下条目: %+v", s.drafts["t1"])
	}
	// 文本 + 附件：只保留已上传成功的附件
	s.stashDraftLocked("t1", "草稿A", []composerAttachment{uploaded, uploading, failed}, nil)
	entry := s.drafts["t1"]
	if entry.Text != "草稿A" || len(entry.Attachments) != 1 || entry.Attachments[0].ID != "up-1" {
		t.Fatalf("stashDraftLocked = %+v, want 文本 + 仅 up-1", entry)
	}
	// 已有草稿后清空文本且没有附件：删除条目
	s.stashDraftLocked("t1", "", nil, nil)
	if _, ok := s.drafts["t1"]; ok {
		t.Fatal("清空后不应保留条目")
	}
	// take 返回附件切片拷贝，缺失条目返回零值
	s.stashDraftLocked("t2", "草稿B", []composerAttachment{uploaded}, nil)
	d := s.takeDraftLocked("t2")
	if d.Text != "草稿B" || len(d.Attachments) != 1 {
		t.Fatalf("takeDraftLocked = %+v", d)
	}
	d.Attachments[0].Name = "changed"
	if s.drafts["t2"].Attachments[0].Name != "a.txt" {
		t.Fatal("takeDraftLocked 应返回附件切片拷贝")
	}
	s.clearDraftLocked("t2")
	if _, ok := s.drafts["t2"]; ok {
		t.Fatal("clearDraftLocked 应删除条目")
	}
	if d2 := s.takeDraftLocked("t2"); d2.Text != "" || d2.Attachments != nil {
		t.Fatalf("缺失条目应返回零值, got %+v", d2)
	}
}

// TestComposerDraftsSwapOnSelectThread 验证切换会话时草稿互不串台。
func TestComposerDraftsSwapOnSelectThread(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.draft = "草稿A"
	s.mu.Unlock()

	s.selectThread("t2")

	s.mu.Lock()
	if s.drafts["t1"].Text != "草稿A" {
		s.mu.Unlock()
		t.Fatalf("切换前应把 t1 的草稿写回草稿表: %+v", s.drafts)
	}
	if s.pendingDraftFill == nil || *s.pendingDraftFill != "" {
		fill := ""
		if s.pendingDraftFill != nil {
			fill = *s.pendingDraftFill
		}
		s.mu.Unlock()
		t.Fatalf("切到无草稿会话应回填空文本以清空输入框, got %q", fill)
	}
	s.draft = "草稿B"
	s.mu.Unlock()

	s.selectThread("t1")

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts["t2"].Text != "草稿B" {
		t.Fatalf("切换前应把 t2 的草稿写回草稿表: %+v", s.drafts)
	}
	if s.pendingDraftFill == nil || *s.pendingDraftFill != "草稿A" {
		fill := ""
		if s.pendingDraftFill != nil {
			fill = *s.pendingDraftFill
		}
		t.Fatalf("切回 t1 应回填草稿A, got %q", fill)
	}
}

// TestComposerDraftNoteUpdatesMemoryAndSkipsWriteWhenDisabled 验证 noteComposerDraft 更新内存草稿
// 并保留附件；未开启持久化时防抖结束后也不写文件。
func TestComposerDraftNoteUpdatesMemoryAndSkipsWriteWhenDisabled(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.mu.Lock()
	s.drafts["t1"] = composerDraft{Text: "旧草稿", Attachments: []composerAttachment{{LocalID: "loc-1", ID: "up-1", Name: "a.txt"}}}
	s.mu.Unlock()

	s.noteComposerDraft("t1", "新草稿")
	// 文本与已记录相同时是空操作
	s.noteComposerDraft("t1", "新草稿")

	s.mu.Lock()
	entry := s.drafts["t1"]
	s.mu.Unlock()
	if entry.Text != "新草稿" || len(entry.Attachments) != 1 || entry.Attachments[0].ID != "up-1" {
		t.Fatalf("noteComposerDraft 应更新文本并保留附件, got %+v", entry)
	}

	// 等防抖窗口过去，持久化关闭时不应写出文件
	time.Sleep(composerDraftSaveDebounce + 200*time.Millisecond)
	if _, err := os.Stat(filepath.Join(dataDir, composerDraftsFileName)); !os.IsNotExist(err) {
		t.Fatalf("持久化关闭时不应写出草稿文件, stat err = %v", err)
	}
}

// TestComposerDraftPersistWhenEnabled 验证开启持久化后草稿会写入 composer-drafts.json。
func TestComposerDraftPersistWhenEnabled(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)
	t.Cleanup(s.close)

	s.noteComposerDraft("t7", "持久化草稿")
	s.persistDraftsAsync()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := loadComposerDrafts(); got["t7"] == "持久化草稿" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("草稿应写入 composer-drafts.json, got %+v", loadComposerDrafts())
}

// TestComposerDraftFlushOnClose 验证关窗时把当前输入框文本同步落盘。
func TestComposerDraftFlushOnClose(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	s.mu.Lock()
	s.selectedThreadID = "t9"
	s.draft = "关闭前草稿"
	s.mu.Unlock()

	s.close()

	if got := loadComposerDrafts(); got["t9"] != "关闭前草稿" {
		t.Fatalf("关窗应同步落盘当前会话草稿, got %+v", got)
	}
}
