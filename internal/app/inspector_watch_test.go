package app

import (
	"context"
	"testing"
)

// 没有后端时返回错误，不 panic。
func TestInspectorWatchBackendUnavailable(t *testing.T) {
	client := &Client{}
	generation, err := client.WorkspaceGeneration(context.Background(), "thread")
	if err == nil {
		t.Fatalf("backend 为 nil 时应返回错误")
	}
	if generation != 0 {
		t.Fatalf("失败时代次应为 0，得到 %d", generation)
	}
}
