package mcode

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/harness"
)

func TestSessionKindAndPreservation(t *testing.T) {
	dir := t.TempDir()
	client := &fakeClient{responses: []*llm.Response{answer(), answer(), answer()}}
	old := harnessFor(client, Options{Kind: "custom-432db46a", StoreDir: dir})
	var id string
	if err := old.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, func(event harness.Event) {
		if event.Type == harness.EventSession {
			id = event.SessionID
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "custom-432db46a_") || !old.SessionExists(id) {
		t.Fatalf("新会话 ID = %q", id)
	}
	newHarness := harnessFor(client, Options{Kind: "custom-432db46a", StoreDir: dir})
	newHarness.PreserveSessionsFrom(old)
	if !newHarness.SessionExists(id) {
		t.Fatal("新实例找不到会话")
	}
	if err := newHarness.Run(context.Background(), harness.Turn{SessionID: id, Prompt: "again", WorkDir: t.TempDir()}, nil); err != nil {
		t.Fatal(err)
	}
	legacy := session.NewID()
	if err := session.NewStore(dir).Append(legacy, map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	if !newHarness.SessionExists(legacy) {
		t.Fatal("旧 UUID 会话不可读")
	}
	if err := newHarness.Run(context.Background(), harness.Turn{SessionID: legacy, Prompt: "again", WorkDir: t.TempDir()}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestForeignSessionKindRejected(t *testing.T) {
	dir := t.TempDir()
	id := "openai_" + session.NewID()
	if err := session.NewStore(dir).Append(id, map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	for _, storeDir := range []string{dir, ""} {
		h := New(Options{Kind: "custom-432db46a", StoreDir: storeDir})
		if h.SessionExists(id) {
			t.Fatal("其他来源的会话不应存在")
		}
		if err := h.Run(context.Background(), harness.Turn{SessionID: id}, nil); err == nil || !strings.Contains(err.Error(), "不属于来源") {
			t.Fatalf("错误来源应在读取会话前拒绝：%v", err)
		}
	}
}
