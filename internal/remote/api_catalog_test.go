package remote

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func TestAPICatalogRegistrationAndCopies(t *testing.T) {
	const kind = "custom-catalog-test"
	t.Cleanup(func() { SetAPICatalog(kind, nil) })
	detail := "详情"
	rows := []wire.ProviderModel{{ID: "model", Name: "Model · 网关", Detail: &detail, Efforts: []string{"high"}, Cost: &wire.ModelCost{Input: 1}}}
	SetAPICatalog(kind, func() []wire.ProviderModel { return rows })
	got := ProviderModels(kind)
	got[0].Efforts[0] = "low"
	*got[0].Detail = "修改"
	got[0].Cost.Input = 2
	if rows[0].Name != "Model · 网关" || rows[0].Efforts[0] != "high" || *rows[0].Detail != "详情" || rows[0].Cost.Input != 1 {
		t.Fatalf("目录缓存被修改: %+v", rows)
	}
	SetAPICatalog(kind, func() []wire.ProviderModel { SetAPICatalog(kind, nil); return nil })
	if _, ok := apiCatalog(kind); !ok {
		t.Fatal("已注册的空目录应可区分")
	}
	if _, ok := apiCatalog(kind); ok {
		t.Fatal("回调内删除注册失败")
	}
}

func TestAPICatalogConcurrentRegistration(t *testing.T) {
	const kind = "custom-concurrent-test"
	t.Cleanup(func() { SetAPICatalog(kind, nil) })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				SetAPICatalog(kind, func() []wire.ProviderModel { return []wire.ProviderModel{{ID: "a"}} })
				apiCatalog(kind)
				SetAPICatalog(kind, nil)
			}
		}()
	}
	wg.Wait()
}

func TestAPICatalogMatchesProviderList(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "deepseek", "zai", "meta", "custom-test", "magpie", "magpie2", "mcode"} {
		t.Run(kind, func(t *testing.T) {
			SetAPICatalog(kind, func() []wire.ProviderModel {
				return []wire.ProviderModel{{ID: "bare-model", Name: "模型", IsDefault: true, ContextLimit: 123}}
			})
			t.Cleanup(func() { SetAPICatalog(kind, nil) })
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			backend := NewMemoryBackend(hub)
			defer backend.Close()
			backend.RegisterHarness(&customMetaHarness{kind: harness.Kind(kind), modelIDs: []string{"other"}})
			result, err := backend.ListProviders(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, provider := range result.Providers {
				if provider.ID == kind {
					if !reflect.DeepEqual(provider.Models, ProviderModels(kind)) {
						t.Fatalf("目录不一致: %+v", provider.Models)
					}
					return
				}
			}
			t.Fatalf("缺少来源 %s", kind)
		})
	}
}

func TestAPICatalogColdCache(t *testing.T) {
	const kind = "custom-cold-test"
	SetAPICatalog(kind, func() []wire.ProviderModel { return nil })
	t.Cleanup(func() { SetAPICatalog(kind, nil) })
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&customMetaHarness{kind: kind, modelIDs: []string{"stale"}})
	result, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range result.Providers {
		if provider.ID == kind {
			if len(provider.Models) != 0 {
				t.Fatalf("冷目录不应回退: %+v", provider.Models)
			}
			return
		}
	}
	t.Fatal("缺少端点")
}

func TestOfflineAPICatalogs(t *testing.T) {
	for _, kind := range []string{"anthropic", "deepseek", "zai", "meta", "openai"} {
		models := ProviderModels(kind)
		if len(models) == 0 || models[0].ID == "default" {
			t.Errorf("%s 缺少离线模型: %+v", kind, models)
		}
		if kind == "anthropic" && len(models) != 3 {
			t.Errorf("Anthropic 模型数量错误: %+v", models)
		}
	}
}
