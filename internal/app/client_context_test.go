package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/projctx"
	mcodeui "mcode/internal/ui"
)

func TestClientContextDocuments(t *testing.T) {
	client := NewClient(nil)
	path := t.TempDir()
	doc, err := client.CreateContextDoc(path, "design.md")
	if err != nil || doc.Name != "docs/design.md" || doc.Hash != projctx.HashContent("# Design\n\n") {
		t.Fatalf("创建文档：%+v, %v", doc, err)
	}
	doc, err = client.WriteContextDoc(path, doc.Name, "original", doc.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateContextDoc(path, doc.Name); !errors.Is(err, os.ErrExist) {
		t.Fatalf("重复创建必须失败：%v", err)
	}
	docs, err := client.ContextDocs(path)
	if err != nil || len(docs) != 1 || docs[0].Name != "docs/design.md" || docs[0].Content != "original" {
		t.Fatalf("列表：%+v, %v", docs, err)
	}
	if _, err := client.CreateContextDoc(path, "../escape.md"); err == nil {
		t.Fatal("允许了非法名称")
	}
	if _, err := client.ContextDocs(""); err == nil {
		t.Fatal("允许了空项目路径")
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"first", "second"} {
		wg.Go(func() {
			_, err := client.WriteContextDoc(path, doc.Name, content, doc.Hash)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, mcodeui.ErrContextConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("并发 CAS：success=%d conflict=%d", successes, conflicts)
	}
	if _, err := client.WriteContextDoc(path, doc.Name, "overwrite", ""); err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadContextDoc(path, doc.Name)
	if err != nil || got.Content != "overwrite" {
		t.Fatalf("覆盖结果：%+v, %v", got, err)
	}
}

func TestClientContextStandardDocuments(t *testing.T) {
	client := NewClient(nil)
	path := t.TempDir()
	if docs, err := client.ContextDocs(path); err != nil || len(docs) != 0 {
		t.Fatalf("缺失的标准文档不应返回：%+v, %v", docs, err)
	}
	if _, err := client.ReadContextDoc(path, "instructions.md"); !errors.Is(err, projctx.ErrNotFound) {
		t.Fatalf("缺失的标准文档应返回 ErrNotFound：%v", err)
	}
	doc, err := client.CreateContextDoc(path, "instructions")
	if err != nil || doc.Name != "instructions.md" {
		t.Fatalf("创建标准文档：%+v, %v", doc, err)
	}
	doc, err = client.WriteContextDoc(path, "docs/notes.md", "custom", "")
	if err != nil || doc.Name != "docs/notes.md" {
		t.Fatalf("写入自定义文档：%+v, %v", doc, err)
	}
	if _, err := client.ReadContextDoc(path, "notes"); !errors.Is(err, projctx.ErrNotFound) {
		t.Fatalf("docs/notes.md 不应落到根部 notes.md：%v", err)
	}
	docs, err := client.ContextDocs(path)
	if err != nil || len(docs) != 2 || docs[0].Name != "instructions.md" || docs[1].Name != "docs/notes.md" {
		t.Fatalf("列表：%+v, %v", docs, err)
	}
}

func TestClientContextWatcher(t *testing.T) {
	client := NewClient(nil)
	path := t.TempDir()
	changed := make(chan struct{}, 1)
	stop, err := client.WatchContextDocs(path, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	docPath := filepath.Join(path, projctx.DirName, projctx.DocsDir, "notes.md")
	if err := os.WriteFile(docPath, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("未收到项目文档外部修改事件")
	}
	stop()
	stop()
}
