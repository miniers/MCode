package remote

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/pi"
	"mcode/internal/remote/wire"
)

func TestCLICatalogPanicClearsFetching(t *testing.T) {
	var catalog cliCatalog
	bad := func(context.Context) ([]wire.ProviderModel, error) { panic("fetch") }
	catalog.fetching = true
	catalog.refresh(bad, 0)
	if catalog.fetching { t.Fatal("后台拉取状态未清除") }
	if err := catalog.reload(context.Background(), bad); err == nil || catalog.fetching { t.Fatal("同步刷新未清除状态") }
	if err := catalog.reload(context.Background(), func(context.Context) ([]wire.ProviderModel, error) {
		return []wire.ProviderModel{{ID: "ok"}}, nil
	}); err != nil { t.Fatal(err) }
}

func TestCLICatalogReloadKeepsLatestAndPreservesOnFailure(t *testing.T) {
	var catalog cliCatalog
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fetch := func(context.Context) ([]wire.ProviderModel, error) {
		close(entered)
		<-release
		return []wire.ProviderModel{{ID: "old"}}, nil
	}
	catalog.fetching = true
	go func() {
		catalog.refresh(fetch, 0)
		close(done)
	}()
	<-entered
	if err := catalog.reload(context.Background(), func(context.Context) ([]wire.ProviderModel, error) {
		return []wire.ProviderModel{{ID: "new"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if got := catalog.get(fetch); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("旧请求覆盖了新目录: %+v", got)
	}
	if err := catalog.reload(context.Background(), func(context.Context) ([]wire.ProviderModel, error) {
		return nil, errors.New("offline")
	}); err == nil {
		t.Fatal("刷新失败应返回错误")
	}
	if got := catalog.get(fetch); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("刷新失败不应清空旧目录: %+v", got)
	}
}

func TestPiModelRowsCarryEffortsAndDefaultEffort(t *testing.T) {
	models := []pi.Model{
		{ID: "magpie-jp/auto/deepseek-v4.1-flash", Provider: "magpie-jp", Name: "DeepSeek V4.1 Flash (auto)", ContextWindow: 200_000, ThinkingLevels: []string{"low", "high", "max"}, DefaultThinking: "low", IsDefault: true},
		{ID: "magpie-jp/auto/mimo-v2.6-flash", Provider: "magpie-jp", Name: "MiMo V2.6 Flash (auto)"},
	}
	rows := withDefaultRow(piModelRows(models))
	if len(rows) != 2 {
		t.Fatalf("行数 = %d，期望 2（已有默认行，不应再补占位行）: %+v", len(rows), rows)
	}
	if len(rows[0].Efforts) != 3 || rows[0].Efforts[0] != "low" || rows[0].DefaultEffort == nil || *rows[0].DefaultEffort != "low" {
		t.Fatalf("档位没有传下去: %+v", rows[0])
	}
	if rows[0].Name != "DeepSeek V4.1 Flash (auto)" {
		t.Fatalf("显示名应取目录里的精简名: %q", rows[0].Name)
	}
	if len(rows[1].Efforts) != 0 || rows[1].DefaultEffort != nil {
		t.Fatalf("没有档位的模型不应带档位: %+v", rows[1])
	}
	// 上下文窗口要跟着下发，否则界面的上下文圆环对 pi 模型无数据。
	if rows[0].ContextLimit != 200_000 {
		t.Fatalf("上下文窗口没有传下去: %+v", rows[0])
	}
	if rows[1].ContextLimit != 0 {
		t.Fatalf("未知上下文窗口应保持 0: %+v", rows[1])
	}
}

// 作废（注入变化）后在途拉取的结果必须丢掉：否则作废前发出的请求会把旧目录写回缓存，
// 用户又要等一整个新鲜期才看到新模型。
func TestCLICatalogInvalidateDropsInFlightResult(t *testing.T) {
	var catalog cliCatalog
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	fetch := func(context.Context) ([]wire.ProviderModel, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
			return []wire.ProviderModel{{ID: "stale"}}, nil
		}
		return []wire.ProviderModel{{ID: "fresh"}}, nil
	}

	catalog.get(fetch)
	<-entered
	catalog.invalidate(fetch)
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		catalog.mu.Lock()
		models := append([]wire.ProviderModel(nil), catalog.models...)
		catalog.mu.Unlock()
		if len(models) > 0 && models[0].ID == "fresh" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("作废后在途结果仍写进了缓存")
}
