package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 场景 1：recentDownloads 按修改时间降序排序。
func TestRecentDownloads_Ordering(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create downloads dir: %v", err)
	}

	f1 := filepath.Join(dir, "old.txt")
	f2 := filepath.Join(dir, "middle.txt")
	f3 := filepath.Join(dir, "new.txt")

	for _, p := range []string{f1, f2, f3} {
		if err := os.WriteFile(p, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", p, err)
		}
	}

	base := time.Now()
	_ = os.Chtimes(f1, base.Add(-10*time.Minute), base.Add(-10*time.Minute))
	_ = os.Chtimes(f2, base.Add(-5*time.Minute), base.Add(-5*time.Minute))
	_ = os.Chtimes(f3, base.Add(-1*time.Minute), base.Add(-1*time.Minute))

	entries, err := recentDownloads(dir, 15)
	if err != nil {
		t.Fatalf("recentDownloads failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Name != "new.txt" || entries[1].Name != "middle.txt" || entries[2].Name != "old.txt" {
		t.Fatalf("unexpected order: %+v", entries)
	}
}

// 场景 2：recentDownloads 跳过以 '.' 开头的隐藏文件与子目录。
func TestRecentDownloads_HiddenFileAndDirSkip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create downloads dir: %v", err)
	}

	_ = os.WriteFile(filepath.Join(dir, ".hidden"), []byte("hidden"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("ds"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "subfolder"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "visible.png"), []byte("image"), 0644)

	entries, err := recentDownloads(dir, 15)
	if err != nil {
		t.Fatalf("recentDownloads failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "visible.png" {
		t.Fatalf("expected visible.png, got %s", entries[0].Name)
	}
}

// 场景 3：recentDownloads 在有 20 个普通文件时正确截取最近 15 项。
func TestRecentDownloads_Limit15OutOf20(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create downloads dir: %v", err)
	}

	base := time.Now()
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("file_%02d.txt", i)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(p, mt, mt)
	}

	entries, err := recentDownloads(dir, 15)
	if err != nil {
		t.Fatalf("recentDownloads failed: %v", err)
	}
	if len(entries) != 15 {
		t.Fatalf("expected 15 entries, got %d", len(entries))
	}
	// 最新的是 file_19.txt，第 15 项是 file_05.txt
	if entries[0].Name != "file_19.txt" {
		t.Fatalf("expected first entry to be file_19.txt, got %s", entries[0].Name)
	}
	if entries[14].Name != "file_05.txt" {
		t.Fatalf("expected last entry to be file_05.txt, got %s", entries[14].Name)
	}
}

// 场景 4：downloadsDir 返回基于 HOME 的 ~/Downloads。
func TestDownloadsDir(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)

	expected := filepath.Join(temp, "Downloads")
	if got := downloadsDir(); got != expected {
		t.Fatalf("downloadsDir() = %q, want %q", got, expected)
	}
}

// 场景 5：renderDownloadsPanel 空状态与底部操作测试。
func TestRenderDownloadsPanel_Empty(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	clearDownloadsCache()

	dir := filepath.Join(temp, "Downloads")
	_ = os.MkdirAll(dir, 0755)

	fake := NewFakeClient()
	s := newChatState(fake)

	tt := ui.NewTester(func(c *ui.Context) {
		renderDownloadsPanel(c, s, nil)
	}, 400, 400)

	if !tt.HasText("最近下载") {
		t.Fatalf("expected title '最近下载'")
	}
	if !tt.HasText("下载目录中没有文件") {
		t.Fatalf("expected empty note '下载目录中没有文件'")
	}
	if !tt.HasText("在 Finder 中显示") {
		t.Fatalf("expected '在 Finder 中显示'")
	}
	if !tt.HasText("选择文件…") {
		t.Fatalf("expected '选择文件…'")
	}
}

// 场景 6：renderDownloadsPanel 点击条目添加附件并触发 onDone。
func TestRenderDownloadsPanel_ClickItemAndFooter(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	clearDownloadsCache()

	dir := filepath.Join(temp, "Downloads")
	_ = os.MkdirAll(dir, 0755)

	testPng := filepath.Join(dir, "snap.png")
	// 写入合法的最小 PNG 头部数据
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	if err := os.WriteFile(testPng, pngData, 0644); err != nil {
		t.Fatalf("failed to write test png: %v", err)
	}

	fake := NewFakeClient()
	s := newChatState(fake)

	doneCalls := 0
	onDone := func() {
		doneCalls++
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderDownloadsPanel(c, s, onDone)
	}, 400, 400)

	if !tt.HasText("snap.png") {
		t.Fatalf("expected file row 'snap.png'")
	}

	// 点击该行
	if err := tt.Click("snap.png"); err != nil {
		t.Fatalf("failed to click snap.png: %v", err)
	}
	if doneCalls != 1 {
		t.Fatalf("expected onDone to be called 1 time, got %d", doneCalls)
	}

	// 读盘与解码在后台协程完成（addAttachmentFiles → readAttachmentFiles）。
	waitAttachmentFiles(t, s, 1, "")

	s.mu.Lock()
	hasAttachment := len(s.attachments) > 0
	s.mu.Unlock()
	if !hasAttachment {
		t.Fatalf("expected attachment to be added to chatState")
	}

	// 测试底部“选择文件…”按钮与对 pickFilesDialog 的调用
	oldPicker := pickFilesDialog
	defer func() { pickFilesDialog = oldPicker }()

	pickerCalled := make(chan struct{})
	pickFilesDialog = func() ([]string, bool, error) {
		close(pickerCalled)
		return []string{testPng}, true, nil
	}

	if err := tt.Click("选择文件…"); err != nil {
		t.Fatalf("failed to click '选择文件…': %v", err)
	}
	if doneCalls != 2 {
		t.Fatalf("expected onDone called twice, got %d", doneCalls)
	}
	select {
	case <-pickerCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected pickFilesDialog to be called")
	}
}

// 场景 7：recentDownloads 跳过未完成下载的临时文件（.crdownload/.download/.part）。
func TestRecentDownloads_IncompleteDownloadSkip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create downloads dir: %v", err)
	}

	for _, name := range []string{"video.mp4.crdownload", "archive.zip.download", "blob.bin.part", "BLOCKED.PART"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "done.txt"), []byte("done"), 0644); err != nil {
		t.Fatalf("failed to write done.txt: %v", err)
	}

	entries, err := recentDownloads(dir, 15)
	if err != nil {
		t.Fatalf("recentDownloads failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "done.txt" {
		t.Fatalf("expected done.txt, got %s", entries[0].Name)
	}
}
