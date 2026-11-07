package app

import (
	"context"
	"testing"
)

// TestClient_ReadMessageMedia_NilBackend 校验后端缺失时返回明确错误而不是 panic。
func TestClient_ReadMessageMedia_NilBackend(t *testing.T) {
	c := &Client{}
	if _, _, err := c.ReadMessageMedia(context.Background(), "th", "msg", "med", ""); err == nil {
		t.Fatal("expected error when backend is nil")
	}
}
