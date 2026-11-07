package remote

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// claude 模型目录为空（CLI 不可用）时 ListProviders 仍下发 available=true，
// 同时把 ModelCatalogNotice 的原因填进 UnavailableReason；不适用原因文案的
// openai 兼容供应商不受影响。
func TestListProviders_CatalogNoticeAsUnavailableReason(t *testing.T) {
	// 隔离环境，避免真跑 CLI 或读到本机缓存里的目录。
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "claude"})
	backend.RegisterHarness(&mockHarness{kind: "openai"})

	res, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}

	var claude, openai *wire.Provider
	for i := range res.Providers {
		switch res.Providers[i].ID {
		case "claude":
			claude = &res.Providers[i]
		case "openai":
			openai = &res.Providers[i]
		}
	}
	if claude == nil {
		t.Fatalf("缺少 claude 项: %+v", res.Providers)
	}
	if !claude.Available {
		t.Error("目录为空不代表不可用，claude 应保持 Available=true")
	}
	if claude.Auth != "signedIn" {
		t.Errorf("目录原因不应改动 auth，got %q", claude.Auth)
	}
	if len(claude.Models) != 0 {
		// 进程内的模型缓存可能已被其它用例的探测填满，此时没有空目录可验证。
		t.Skipf("claude 目录已在本进程内被探测到，跳过空目录原因校验: %+v", claude.Models)
	}
	if claude.UnavailableReason == nil || strings.TrimSpace(*claude.UnavailableReason) == "" {
		t.Error("空目录应带有原因文案")
	}

	if openai == nil {
		t.Fatalf("缺少 openai 项: %+v", res.Providers)
	}
	if len(openai.Models) == 0 {
		t.Fatal("openai 应保留起始目录")
	}
	if openai.UnavailableReason != nil {
		t.Errorf("openai 不适用目录原因文案，got %q", *openai.UnavailableReason)
	}
}
