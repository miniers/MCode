package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendLoad(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	id := NewID()
	if store.Exists(id) {
		t.Fatal("会话不应已存在")
	}
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v", err)
	}
	if err := store.Append(id, "first", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(id, true); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Load(id)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
	for i, want := range []string{`"first"`, `2`, `true`} {
		var got any
		var expected any
		if json.Unmarshal(entries[i], &got) != nil || json.Unmarshal([]byte(want), &expected) != nil || got != expected {
			t.Fatalf("entry %d = %s", i, entries[i])
		}
	}
	if !store.Exists(id) {
		t.Fatal("会话应已存在")
	}
}

func TestRewrite(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Append("test", "old"); err != nil {
		t.Fatal(err)
	}
	if err := store.Rewrite("test", "new", 2); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Load("test")
	if err != nil || len(entries) != 2 || strings.TrimSpace(string(entries[0])) != `"new"` || strings.TrimSpace(string(entries[1])) != `2` {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
	if err := store.Rewrite("test", make(chan int)); err == nil {
		t.Fatal("expected marshal error")
	}
	entries, err = store.Load("test")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
}

func TestLoadPartialLastLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.jsonl"), []byte("{\"ok\":true}\n{\"partial\":"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := NewStore(dir).Load("test")
	if err != nil || len(entries) != 1 || !json.Valid(entries[0]) {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
}

func TestAppendAfterPartialLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "partial.jsonl"), []byte(`{"half":`), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	if err := store.Append("partial", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Load("partial")
	if err != nil || len(entries) != 1 || strings.TrimSpace(string(entries[0])) != `{"ok":true}` {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
}

func TestLoadSkipsInvalidLines(t *testing.T) {
	dir := t.TempDir()
	data := "{\"a\":1}\nnot json\n{\"b\":2}\n"
	if err := os.WriteFile(filepath.Join(dir, "test.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := NewStore(dir).Load("test")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
}

func TestLoadAllInvalidLinesFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.jsonl"), []byte("bad\nworse\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load("test"); err == nil {
		t.Fatal("全部记录损坏时应报错")
	}
}

func TestInvalidID(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"", "../escape", "a/b", "a.b", "空"} {
		if err := store.Append(id, 1); err == nil {
			t.Errorf("Append(%q) succeeded", id)
		}
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load(%q) succeeded", id)
		}
		if store.Exists(id) {
			t.Errorf("Exists(%q) succeeded", id)
		}
	}
}

func TestConcurrentAppend(t *testing.T) {
	store := NewStore(t.TempDir())
	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Append("test", fmt.Sprintf("item-%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, err := store.Load("test")
	if err != nil || len(entries) != count {
		t.Fatalf("count=%d err=%v", len(entries), err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		var value string
		if err := json.Unmarshal(entry, &value); err != nil || seen[value] {
			t.Fatalf("entry=%s err=%v", entry, err)
		}
		seen[value] = true
	}
}

func TestCompressIdleSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(dir)

	// 写入可高度压缩的旧会话数据
	payload := strings.Repeat("repeated-session-context-", 40)
	for i := 0; i < 10; i++ {
		if err := store.Append("old-sess", map[string]any{"idx": i, "text": payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append("fresh-sess", map[string]any{"fresh": true, "text": payload}); err != nil {
		t.Fatal(err)
	}
	// 并存场景：.jsonl 与旧 .jsonl.zst 同时存在时，以 .jsonl 为准覆盖压缩
	if err := store.Append("coexist-sess", map[string]any{"latest": true, "text": payload}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coexist-sess.jsonl.zst"), []byte("stale-corrupt"), 0600); err != nil {
		t.Fatal(err)
	}

	// 子目录与符号链接必须被跳过
	subDir := filepath.Join(dir, "nested.jsonl")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outsideFile, []byte("{\"outside\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "link-sess.jsonl")); err != nil {
		t.Fatal(err)
	}

	beforeOld, err := store.Load("old-sess")
	if err != nil {
		t.Fatal(err)
	}
	beforeCoexist, err := store.Load("coexist-sess")
	if err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	for _, p := range []string{
		filepath.Join(dir, "old-sess.jsonl"),
		filepath.Join(dir, "coexist-sess.jsonl"),
		filepath.Join(dir, "link-sess.jsonl"),
		outsideFile,
	} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	files, savedBytes, err := store.CompressIdle(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CompressIdle err=%v", err)
	}
	if files != 2 || savedBytes <= 0 {
		t.Fatalf("files=%d savedBytes=%d, want files=2 savedBytes>0", files, savedBytes)
	}

	// 旧文件已压成 .zst 且原 .jsonl 消失
	for _, id := range []string{"old-sess", "coexist-sess"} {
		if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s.jsonl 应已删除, err=%v", id, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl.zst")); statErr != nil {
			t.Fatalf("%s.jsonl.zst 应存在, err=%v", id, statErr)
		}
		if !store.Exists(id) {
			t.Fatalf("Exists(%q) 应为 true", id)
		}
	}

	// 新文件与符号链接/目录保持不动
	if _, statErr := os.Stat(filepath.Join(dir, "fresh-sess.jsonl")); statErr != nil {
		t.Fatalf("fresh-sess.jsonl 不应被删, err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "fresh-sess.jsonl.zst")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fresh-sess.jsonl.zst 不应存在, err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "link-sess.jsonl.zst")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("符号链接不应被压缩, err=%v", statErr)
	}
	if info, statErr := os.Stat(subDir); statErr != nil || !info.IsDir() {
		t.Fatalf("子目录不应被破坏, err=%v", statErr)
	}

	// Load 读出与压缩前完全一致
	afterOld, err := store.Load("old-sess")
	if err != nil {
		t.Fatalf("Load compressed old-sess err=%v", err)
	}
	if len(afterOld) != len(beforeOld) {
		t.Fatalf("entries len=%d, want %d", len(afterOld), len(beforeOld))
	}
	for i := range beforeOld {
		if !bytes.Equal(afterOld[i], beforeOld[i]) {
			t.Fatalf("entry %d mismatch:\ngot:  %s\nwant: %s", i, afterOld[i], beforeOld[i])
		}
	}

	afterCoexist, err := store.Load("coexist-sess")
	if err != nil {
		t.Fatalf("Load compressed coexist-sess err=%v", err)
	}
	if len(afterCoexist) != len(beforeCoexist) || !bytes.Equal(afterCoexist[0], beforeCoexist[0]) {
		t.Fatalf("coexist-sess 应以 .jsonl 为准: got=%q want=%q", afterCoexist, beforeCoexist)
	}
}

func TestAppendRehydratesCompressedSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(dir)
	const id = "rehydrate"

	if err := store.Append(id, "first", map[string]int{"n": 2}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, id+".jsonl"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	files, _, err := store.CompressIdle(7 * 24 * time.Hour)
	if err != nil || files != 1 {
		t.Fatalf("CompressIdle files=%d err=%v", files, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("压缩后原 .jsonl 应已删除, err=%v", statErr)
	}

	// 对已压缩会话 Append：应还原 .jsonl、删除 .zst 并追加新条目
	if err := store.Append(id, "third"); err != nil {
		t.Fatalf("Append to compressed session err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl.zst")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Append 后 .jsonl.zst 应已删除, err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl")); statErr != nil {
		t.Fatalf("Append 后 .jsonl 应存在, err=%v", statErr)
	}

	entries, err := store.Load(id)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%q err=%v", entries, err)
	}
	for i, want := range []string{`"first"`, `{"n":2}`, `"third"`} {
		if strings.TrimSpace(string(entries[i])) != want {
			t.Fatalf("entry %d = %s, want %s", i, entries[i], want)
		}
	}

	// Rewrite 压缩态会话时同样应清掉残留 .zst
	if err := os.Chtimes(filepath.Join(dir, id+".jsonl"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompressIdle(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Rewrite(id, "rewritten"); err != nil {
		t.Fatalf("Rewrite compressed session err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, id+".jsonl.zst")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Rewrite 后 .jsonl.zst 应已删除, err=%v", statErr)
	}
	entries, err = store.Load(id)
	if err != nil || len(entries) != 1 || strings.TrimSpace(string(entries[0])) != `"rewritten"` {
		t.Fatalf("Rewrite 后 Load entries=%q err=%v", entries, err)
	}
}
