package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendLoad(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	id := NewID()
	if store.Exists(id) { t.Fatal("会话不应已存在") }
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) { t.Fatalf("err=%v", err) }
	if err := store.Append(id, "first", 2); err != nil { t.Fatal(err) }
	if err := store.Append(id, true); err != nil { t.Fatal(err) }
	entries, err := store.Load(id)
	if err != nil || len(entries) != 3 { t.Fatalf("entries=%q err=%v", entries, err) }
	for i, want := range []string{`"first"`, `2`, `true`} {
		var got any
		var expected any
		if json.Unmarshal(entries[i], &got) != nil || json.Unmarshal([]byte(want), &expected) != nil || got != expected { t.Fatalf("entry %d = %s", i, entries[i]) }
	}
	if !store.Exists(id) { t.Fatal("会话应已存在") }
}

func TestLoadPartialLastLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.jsonl"), []byte("{\"ok\":true}\n{\"partial\":"), 0600); err != nil { t.Fatal(err) }
	entries, err := NewStore(dir).Load("test")
	if err != nil || len(entries) != 1 || !json.Valid(entries[0]) { t.Fatalf("entries=%q err=%v", entries, err) }
}

func TestInvalidID(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"", "../escape", "a/b", "a.b", "空"} {
		if err := store.Append(id, 1); err == nil { t.Errorf("Append(%q) succeeded", id) }
		if _, err := store.Load(id); err == nil { t.Errorf("Load(%q) succeeded", id) }
		if store.Exists(id) { t.Errorf("Exists(%q) succeeded", id) }
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
			if err := store.Append("test", fmt.Sprintf("item-%d", i)); err != nil { t.Error(err) }
		}(i)
	}
	wg.Wait()
	entries, err := store.Load("test")
	if err != nil || len(entries) != count { t.Fatalf("count=%d err=%v", len(entries), err) }
	seen := make(map[string]bool)
	for _, entry := range entries {
		var value string
		if err := json.Unmarshal(entry, &value); err != nil || seen[value] { t.Fatalf("entry=%s err=%v", entry, err) }
		seen[value] = true
	}
}
