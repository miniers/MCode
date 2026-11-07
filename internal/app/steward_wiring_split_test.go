package app

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/steward"
	"mcode/internal/store"
)

// fakeSplitEvents 是计数用例的事件能力：List 恒为空，UnresolvedCount 返回固定条数。
type fakeSplitEvents struct{ pending int }

func (f fakeSplitEvents) Watch(func(attention.Event)) func() { return func() {} }

func (f fakeSplitEvents) List(context.Context, store.AttentionEventFilter) ([]attention.Event, error) {
	return nil, nil
}

func (f fakeSplitEvents) UnresolvedCount(context.Context) (int, error) { return f.pending, nil }

func (f fakeSplitEvents) Resolve(context.Context, string, string) error { return nil }

// TestStewardSessionRotatorManual 验证手动分割分支：Manual 不看阈值，
// 总管会话给分割版前言，其它会话同意换新但不带前言。
func TestStewardSessionRotatorManual(t *testing.T) {
	ctx := context.Background()
	rt := newRotationStewardRuntime(t, config.StewardConfig{Enabled: true, Name: "总管"})
	rotate := stewardSessionRotator(rt)

	got, preamble := rotate(ctx, remote.SessionRotationInfo{ThreadID: "steward-1", Manual: true})
	if !got {
		t.Fatalf("总管会话手动分割应同意换新")
	}
	if !strings.Contains(preamble, "这是分割后的新会话") {
		t.Fatalf("总管会话手动分割应返回分割版前言, got %q", preamble)
	}

	got, preamble = rotate(ctx, remote.SessionRotationInfo{ThreadID: "lead-1", Manual: true})
	if !got || preamble != "" {
		t.Fatalf("非总管会话手动分割应同意换新但不带前言, 实际 %v %q", got, preamble)
	}

	got, preamble = stewardSessionRotator(nil)(ctx, remote.SessionRotationInfo{ThreadID: "steward-1", Manual: true})
	if !got || preamble != "" {
		t.Fatalf("nil 运行时手动分割应同意换新但不带前言, 实际 %v %q", got, preamble)
	}
}

// TestStewardSplitCounter 验证分割线计数的装配：总管会话返回账本与事件条数，
// 普通会话与 nil 运行时都返回 ok=false，让分割线只写基础文案。
func TestStewardSplitCounter(t *testing.T) {
	ctx := context.Background()
	settings := newFakeStewardSettings()
	if err := settings.SetSetting(ctx, "steward.thread_id", "steward-1"); err != nil {
		t.Fatalf("准备总管会话 ID 失败: %v", err)
	}
	rt := steward.New(steward.Deps{
		Sessions: fakeStewardSessions{},
		Settings: settings,
		Events:   fakeSplitEvents{pending: 2},
		Config:   func() config.StewardConfig { return config.StewardConfig{Enabled: true, Name: "总管"} },
		DataDir:  t.TempDir(),
	})
	t.Cleanup(rt.Stop)
	if id, err := rt.Ensure(ctx); err != nil || id != "steward-1" {
		t.Fatalf("准备总管会话失败: id=%q err=%v", id, err)
	}
	if _, err := rt.AddLedgerEntry(ctx, steward.LedgerInProgress, "写登录流程任务书", ""); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}

	counter := stewardSplitCounter(rt)
	counts, ok := counter(ctx, "steward-1")
	if !ok {
		t.Fatalf("总管会话应返回计数")
	}
	want := remote.SplitCounts{Ledger: 1, Topics: 0, Pending: 2}
	if counts != want {
		t.Fatalf("分割线计数 = %+v, want %+v", counts, want)
	}

	if _, ok := counter(ctx, "lead-1"); ok {
		t.Fatalf("普通会话不应返回计数")
	}
	if _, ok := stewardSplitCounter(nil)(ctx, "steward-1"); ok {
		t.Fatalf("nil 运行时不应返回计数")
	}
}
