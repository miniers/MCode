package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

func TestInspectorPreviewNilBackend(t *testing.T) {
	client := &Client{}
	if _, err := client.InspectorPreviewURL(context.Background(), "thread", "index.html"); err == nil {
		t.Fatal("nil backend 未返回错误")
	}
	client.RevokeInspectorPreviews("thread")
}

func TestInspectorPreviewDelegatesToBackend(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.AddProject(wire.ProjectSummary{ID: "project-a", Path: root})
	backend.AddThread(wire.ThreadSummary{ID: "thread-a", ProjectID: "project-a"}, nil)
	client := NewClient(backend)

	previewURL, err := client.InspectorPreviewURL(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "<html>ok</html>" {
		t.Fatalf("预览响应 %d %q", resp.StatusCode, body)
	}

	client.RevokeInspectorPreviews("thread-a")
	resp, err = httpClient.Get(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("撤销后状态=%d，期望 404", resp.StatusCode)
	}
}
