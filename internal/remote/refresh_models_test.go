package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/providers/acp"
	"mcode/internal/providers/omp"
	"mcode/internal/remote/wire"
)

type refreshTestHarness struct {
	customMetaHarness
	available bool
	err       error
	calls     atomic.Int32
}

type panicRefreshHarness struct {
	customMetaHarness
	calls atomic.Int32
}

func (h *panicRefreshHarness) RefreshModels(context.Context) error {
	if h.calls.Add(1) == 1 {
		panic("refresh")
	}
	return nil
}

func TestRefreshProviderModelsRecoversPanic(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	h := &panicRefreshHarness{customMetaHarness: customMetaHarness{kind: "custom-panic"}}
	backend.RegisterHarness(h)
	if err := backend.RefreshProviderModels(context.Background()); err == nil {
		t.Fatal("应报告刷新失败")
	}
	if err := backend.RefreshProviderModels(context.Background()); err != nil || h.calls.Load() != 2 {
		t.Fatalf("再次刷新: %v", err)
	}
}

func TestRefreshAPICatalogAndMcodeAlias(t *testing.T) {
	const kind = "custom-magpie-refresh"
	h := &refreshTestHarness{customMetaHarness: customMetaHarness{kind: kind}, available: true}
	alias := &refreshTestHarness{customMetaHarness: customMetaHarness{kind: "mcode"}, available: true}
	SetAPICatalog(kind, func() []wire.ProviderModel {
		var rows []wire.ProviderModel
		for _, id := range h.modelIDs {
			rows = append(rows, wire.ProviderModel{ID: id, Name: id})
		}
		return rows
	})
	t.Cleanup(func() { SetAPICatalog(kind, nil) })
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(h)
	backend.RegisterHarness(alias)
	if len(ProviderModels(kind)) != 0 {
		t.Fatal("初始目录应为空")
	}
	if err := backend.RefreshProviderModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.calls.Load() != 1 || alias.calls.Load() != 0 {
		t.Fatal("刷新次数错误")
	}
	if rows := ProviderModels(kind); len(rows) != 1 || rows[0].ID != "new-model" {
		t.Fatalf("刷新后目录错误: %+v", rows)
	}
}

func TestRefreshUsesConfiguredCLIBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("测试用 CLI 为 shell 脚本")
	}
	binary := filepath.Join(t.TempDir(), "custom-omp")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '{\"models\":[{\"id\":\"new\",\"provider\":\"test\"}]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := omp.New(omp.Options{Binary: binary})
	models, err := fetchOmpModelsWithBinary(context.Background(), h.CatalogBinary())
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.ID == "test/new" {
			return
		}
	}
	t.Fatalf("未从配置的 CLI 读取目录: %+v", models)
}

func (h *refreshTestHarness) Available() (bool, string) { return h.available, "unavailable" }
func (h *refreshTestHarness) RefreshModels(context.Context) error {
	h.calls.Add(1)
	if h.err == nil {
		h.modelIDs = []string{"new-model"}
	}
	return h.err
}

func TestRefreshProviderModelsPartialFailure(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	good := &refreshTestHarness{customMetaHarness: customMetaHarness{kind: "custom-good"}, available: true}
	bad := &refreshTestHarness{customMetaHarness: customMetaHarness{kind: "custom-bad", modelIDs: []string{"old-model"}}, available: true, err: errors.New("offline")}
	unavailable := &refreshTestHarness{customMetaHarness: customMetaHarness{kind: "custom-unavailable"}}
	backend.RegisterHarness(good)
	backend.RegisterHarness(bad)
	backend.RegisterHarness(unavailable)
	err := backend.RefreshProviderModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "custom-bad") {
		t.Fatalf("应提示失败的供应商: %v", err)
	}
	if good.calls.Load() != 1 || bad.calls.Load() != 1 || unavailable.calls.Load() != 0 {
		t.Fatalf("可用供应商应各刷新一次: good=%d bad=%d unavailable=%d", good.calls.Load(), bad.calls.Load(), unavailable.calls.Load())
	}
	result, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range result.Providers {
		switch p.ID {
		case "custom-good":
			if len(p.Models) != 1 || p.Models[0].ID != "new-model" {
				t.Fatalf("成功的供应商未更新: %+v", p.Models)
			}
		case "custom-bad":
			if len(p.Models) != 1 || p.Models[0].ID != "old-model" {
				t.Fatalf("失败的供应商应保留旧目录: %+v", p.Models)
			}
		}
	}
}

func TestListProvidersOpenAIIncludesEndpointModels(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&customMetaHarness{kind: "openai", modelIDs: []string{"configured", "server-model"}})
	result, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range result.Providers {
		if p.ID == "openai" {
			for _, m := range p.Models {
				if m.ID == "server-model" {
					return
				}
			}
		}
	}
	t.Fatal("OpenAI 端点返回的模型没有出现在目录里")
}

type refreshACPTestHarness struct{ customMetaHarness }

func (h *refreshACPTestHarness) ProbeModels(context.Context) ([]acp.ProbedModel, error) {
	return []acp.ProbedModel{{ID: "agent-model", Name: "Agent Model", Current: true}}, nil
}

func TestRefreshProviderModelsProbesACPCatalog(t *testing.T) {
	catalog := otherACPCatalogs["cursor"]
	catalog.mu.Lock()
	previousModels, previousFetched, previousFailed, previousGen, previousFetchGen, previousFetching := catalog.models, catalog.fetchedAt, catalog.failedAt, catalog.gen, catalog.fetchGen, catalog.fetching
	catalog.mu.Unlock()
	t.Cleanup(func() {
		catalog.mu.Lock()
		catalog.models, catalog.fetchedAt, catalog.failedAt, catalog.gen, catalog.fetchGen, catalog.fetching = previousModels, previousFetched, previousFailed, previousGen, previousFetchGen, previousFetching
		catalog.mu.Unlock()
	})
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&refreshACPTestHarness{customMetaHarness: customMetaHarness{kind: "cursor"}})
	if err := backend.RefreshProviderModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range result.Providers {
		if p.ID == "cursor" && len(p.Models) == 1 && p.Models[0].ID == "agent-model" && p.Models[0].IsDefault {
			return
		}
	}
	t.Fatal("ACP 探测到的模型没有出现在供应商目录里")
}
