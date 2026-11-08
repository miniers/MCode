package mcode

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mcode/internal/agent/session"
	"mcode/internal/harness"
)

// TestNewDefaults 验证空 Options 下外壳的默认行为与改造前一致。
func TestNewDefaults(t *testing.T) {
	h := New(Options{})
	if got := h.Kind(); got != harness.Kind("mcode") {
		t.Fatalf("Kind() = %q, want mcode", got)
	}
	if got := h.DisplayName(); got != "mcode" {
		t.Fatalf("DisplayName() = %q, want mcode", got)
	}
	if got := h.ModelIDs(); got != nil {
		t.Fatalf("ModelIDs() = %v, want nil", got)
	}
	if err := h.RefreshModels(context.Background()); err != nil {
		t.Fatalf("RefreshModels() = %v, want nil", err)
	}
	if available, reason := h.Available(); !available || reason != "" {
		t.Fatalf("Available() = %v, %q, want true and empty reason", available, reason)
	}
	if !h.ConfirmsApprovalDelivery() {
		t.Fatal("ConfirmsApprovalDelivery() = false, want true")
	}
}

// TestOptionsOverrides 验证配置项原样生效。
func TestOptionsOverrides(t *testing.T) {
	models := []string{"a", "b"}
	var refreshed int
	h := New(Options{
		Kind:        harness.Kind("mygo"),
		DisplayName: "MyGo",
		ModelIDs:    func() []string { return models },
		RefreshModels: func(context.Context) error {
			refreshed++
			return nil
		},
		Available: func() (bool, string) { return false, "未登录" },
	})
	if got := h.Kind(); got != harness.Kind("mygo") {
		t.Fatalf("Kind() = %q, want mygo", got)
	}
	if got := h.DisplayName(); got != "MyGo" {
		t.Fatalf("DisplayName() = %q, want MyGo", got)
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, models) {
		t.Fatalf("ModelIDs() = %v, want %v", got, models)
	}
	if err := h.RefreshModels(context.Background()); err != nil || refreshed != 1 {
		t.Fatalf("RefreshModels() = %v, refreshed %d", err, refreshed)
	}
	if available, reason := h.Available(); available || reason != "未登录" {
		t.Fatalf("Available() = %v, %q", available, reason)
	}
}

// TestRefreshModelsPropagatesError 验证刷新回调的错误会原样返回。
func TestRefreshModelsPropagatesError(t *testing.T) {
	want := errors.New("刷新失败")
	h := New(Options{RefreshModels: func(context.Context) error { return want }})
	if err := h.RefreshModels(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RefreshModels() = %v, want %v", err, want)
	}
}

// TestSessionExists 验证会话文件缺失与存在时的判断。
func TestSessionExists(t *testing.T) {
	dir := t.TempDir()
	h := New(Options{StoreDir: dir})
	if h.SessionExists("missing") {
		t.Fatal("SessionExists(missing) = true, want false")
	}
	store := session.NewStore(dir)
	if err := store.Append("session-1", map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	if !h.SessionExists("session-1") {
		t.Fatal("SessionExists(session-1) = false, want true")
	}
}

// TestSessionExistsWithoutStore 验证未配置会话目录时不触发交接。
func TestSessionExistsWithoutStore(t *testing.T) {
	h := New(Options{})
	if !h.SessionExists("missing") {
		t.Fatal("SessionExists(missing) = false, want true when no store is configured")
	}
}
