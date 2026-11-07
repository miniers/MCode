package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/remote/wire"
)

// seedClaudeModelsForTest 用 shell 脚本冒充 claude CLI 的一次性 initialize 握手，
// 把给定 ID 写进 claude 的进程级模型缓存，避免测试依赖本机是否安装或登录 Claude Code。
func seedClaudeModelsForTest(t *testing.T, ids ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("测试用 CLI 为 shell 脚本")
	}
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(claude.ResetCatalogCacheForTest())

	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(`{"value":%s,"displayName":%s}`, strconv.Quote(id), strconv.Quote(id)))
	}
	line := fmt.Sprintf(`{"type":"control_response","response":{"request_id":"handshake","response":{"models":[%s]}}}`,
		strings.Join(rows, ","))
	binary := filepath.Join(t.TempDir(), "claude-fake")
	script := "#!/bin/sh\nIFS= read -r line\nprintf '%s\\n' '" + line + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := claude.RefreshModels(context.Background(), binary); err != nil {
		t.Fatalf("写入 claude 测试目录失败: %v", err)
	}
}

func TestMagpieCatalogReplacesNativeCatalog(t *testing.T) {
	seedClaudeModelsForTest(t, "default", "claude-sonnet-4-6")
	t.Cleanup(codex.ResetCatalogCacheForTest())
	piCatalog.mu.Lock()
	previousModels, previousFetched := piCatalog.models, piCatalog.fetchedAt
	piCatalog.models = []wire.ProviderModel{{ID: "native/model", Name: "Native", IsDefault: true}}
	piCatalog.fetchedAt = time.Now()
	piCatalog.mu.Unlock()
	t.Cleanup(func() {
		piCatalog.mu.Lock()
		piCatalog.models, piCatalog.fetchedAt = previousModels, previousFetched
		piCatalog.mu.Unlock()
	})
	for _, agent := range []string{"claude", "codex", "pi"} {
		t.Run(agent, func(t *testing.T) {
			SetMagpieCatalog(nil)
			baseline := ProviderModels(agent)
			SetMagpieCatalog(func(name string) ([]wire.ProviderModel, bool) {
				if name != agent {
					return nil, false
				}
				return []wire.ProviderModel{{ID: "magpie-jp/auto/a", Name: "A", IsDefault: true}}, true
			})
			got := ProviderModels(agent)
			if len(got) != 1 || got[0].ID != "magpie-jp/auto/a" || !got[0].IsDefault {
				t.Fatalf("%s 生效目录 = %+v", agent, got)
			}
			SetMagpieCatalog(func(string) ([]wire.ProviderModel, bool) { return nil, false })
			if got := ProviderModels(agent); !reflect.DeepEqual(got, baseline) {
				t.Fatalf("%s 原生目录 = %+v, 想要 %+v", agent, got, baseline)
			}
		})
	}
	t.Cleanup(func() { SetMagpieCatalog(nil) })
}

func TestMagpieCatalogPreservesIdentityAndCopiesMetadata(t *testing.T) {
	base := []wire.ProviderModel{{ID: "auto/a", Name: "native", Efforts: []string{"high"}, ContextLimit: 123}}
	rows := []wire.ProviderModel{{ID: "magpie-jp/auto/a", Name: "A", IsDefault: true}}
	got := enrichMagpieRows("codex", rows, base)
	if len(got) != 1 || got[0].ID != rows[0].ID || got[0].Name != "A" || !got[0].IsDefault || len(got[0].Efforts) != 1 || got[0].ContextLimit != 123 {
		t.Fatalf("补全后的目录不符: %+v", got)
	}
}

// 勾选行不带 1m 标记、CLI 目录行带 1m 标记时，claude 仍应认成同一模型并补上档位。
func TestMagpieCatalogMatchesClaudeModelWithContextMarker(t *testing.T) {
	base := []wire.ProviderModel{{
		ID:      "auto/claude-sonnet-5-5[1m]",
		Name:    "native",
		Efforts: []string{"low", "medium", "high", "xhigh", "max"},
	}}
	rows := []wire.ProviderModel{{ID: "auto/claude-sonnet-5-5", Name: "Sonnet 5.5", IsDefault: true}}
	got := enrichMagpieRows("claude", rows, base)
	if len(got) != 1 || got[0].ID != rows[0].ID || got[0].Name != rows[0].Name || !got[0].IsDefault {
		t.Fatalf("claude 勾选行身份被改写: %+v", got)
	}
	if len(got[0].Efforts) == 0 {
		t.Fatalf("claude 勾选行没有推理档位: %+v", got[0])
	}
}

// 目录为空时，带路由段的勾选行用裸名去 models.dev 兜底，拿到档位。
func TestMagpieCatalogFallsBackToBareClaudeModelID(t *testing.T) {
	rows := []wire.ProviderModel{{ID: "magpie_jp/auto/claude-sonnet-5-5", Name: "Sonnet 5.5", IsDefault: true}}
	got := enrichMagpieRows("claude", rows, nil)
	if len(got) != 1 || got[0].ID != rows[0].ID || got[0].Name != rows[0].Name || !got[0].IsDefault {
		t.Fatalf("claude 勾选行身份被改写: %+v", got)
	}
	if len(got[0].Efforts) == 0 {
		t.Fatalf("claude 裸名兜底没有补上推理档位: %+v", got[0])
	}
}

// 勾选行自带网关档位时优先保留，且默认档必须落在档位列表内。
func TestMagpieCatalogKeepsGatewayEfforts(t *testing.T) {
	base := []wire.ProviderModel{{ID: "gpt-5.5", Name: "native", Efforts: []string{"low", "medium", "high"}}}
	rows := []wire.ProviderModel{{ID: "gpt-5.5", Name: "GPT", Efforts: []string{"medium", "high"}, IsDefault: true}}
	got := enrichMagpieRows("codex", rows, base)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Efforts, []string{"medium", "high"}) {
		t.Fatalf("勾选行档位被改写: %+v", got)
	}
	if got[0].DefaultEffort == nil || !containsString(got[0].Efforts, *got[0].DefaultEffort) {
		t.Fatalf("默认档不在勾选行档位内: %+v", got[0])
	}
}

// 目录里没有的模型也要保留勾选行带来的网关档位。
func TestMagpieCatalogKeepsGatewayEffortsWithoutCandidate(t *testing.T) {
	rows := []wire.ProviderModel{{ID: "deepseek-v4", Name: "DeepSeek", Efforts: []string{"low", "high"}, IsDefault: true}}
	got := enrichMagpieRows("codex", rows, nil)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("无候选时勾选行档位丢失: %+v", got)
	}
	if got[0].DefaultEffort == nil || !containsString(got[0].Efforts, *got[0].DefaultEffort) {
		t.Fatalf("默认档不在勾选行档位内: %+v", got[0])
	}
}

// claude 在 models.dev 查不到时不覆盖网关档位。
func TestMagpieCatalogKeepsGatewayEffortsForUnknownClaudeModel(t *testing.T) {
	rows := []wire.ProviderModel{{ID: "auto/some-unknown-model", Name: "Unknown", Efforts: []string{"low", "high"}, IsDefault: true}}
	got := enrichMagpieRows("claude", rows, nil)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("claude 未知模型档位丢失: %+v", got)
	}
	if got[0].DefaultEffort == nil || !containsString(got[0].Efforts, *got[0].DefaultEffort) {
		t.Fatalf("默认档不在勾选行档位内: %+v", got[0])
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
