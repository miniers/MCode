package remote

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"mcode/internal/providers/acp"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/cline"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/commandcode"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
)

// RefreshProviderModels 强制更新所有可用供应商支持动态读取的模型目录。
func (b *MemoryBackend) RefreshProviderModels(ctx context.Context) error {
	b.mu.Lock()
	harnesses := make(map[string]interface{}, len(b.harnesses))
	for id, h := range b.harnesses {
		harnesses[id] = h
	}
	b.mu.Unlock()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string
	for id, h := range harnesses {
		if rep, ok := h.(availabilityReporter); ok {
			if available, _ := rep.Available(); !available {
				continue
			}
		}
		binary := ""
		if source, ok := h.(interface{ CatalogBinary() string }); ok {
			binary = source.CatalogBinary()
		}
		var refresh func(context.Context) error
		switch id {
		case "mcode":
			refresh = func(context.Context) error { return nil }
		case "claude":
			refresh = func(ctx context.Context) error { return claude.RefreshModels(ctx, binary) }
		case "codex":
			refresh = func(ctx context.Context) error { return codex.RefreshModels(ctx, binary) }
		case "commandcode":
			refresh = func(ctx context.Context) error { return commandcode.RefreshModels(ctx, binary) }
		case "cline":
			refresh = func(ctx context.Context) error { return cline.RefreshModels(ctx, binary) }
		case "pi":
			refresh = func(ctx context.Context) error {
				return piCatalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {
					return fetchPiModelsWithBinary(ctx, binary)
				})
			}
		case "omp":
			refresh = func(ctx context.Context) error {
				return ompCatalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {
					return fetchOmpModelsWithBinary(ctx, binary)
				})
			}
		case "copilot":
			refresh = func(ctx context.Context) error {
				return copilotCatalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {
					return fetchCopilotModelsWithBinary(ctx, binary)
				})
			}
		case "droid":
			refresh = func(ctx context.Context) error {
				return droidCatalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {
					return fetchDroidModelsWithBinary(ctx, binary)
				})
			}
		case "cursor", "opencode", "grok", "devin":
			if probe, ok := h.(interface {
				ProbeModels(context.Context) ([]acp.ProbedModel, error)
			}); ok {
				catalog := otherACPCatalogs[id]
				refresh = func(ctx context.Context) error {
					return catalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {
						models, err := probe.ProbeModels(ctx)
						if err != nil || len(models) == 0 {
							return nil, err
						}
						rows := make([]wire.ProviderModel, 0, len(models))
						for _, m := range models {
							name := m.Name
							if name == "" {
								name = m.ID
							}
							rows = append(rows, wire.ProviderModel{ID: m.ID, Name: name, Detail: optionalString(m.Description), IsDefault: m.Current})
						}
						startuptrace.Mark("providers: " + id + " models ready")
						return withDefaultRow(rows), nil
					})
				}
			}
		default:
			if r, ok := h.(interface{ RefreshModels(context.Context) error }); ok {
				refresh = r.RefreshModels
			}
		}
		if refresh == nil {
			continue
		}
		name := providerDisplayName(id)
		if named, ok := h.(interface{ DisplayName() string }); ok && named.DisplayName() != "" {
			name = named.DisplayName()
		}
		wg.Add(1)
		go func(name string, refresh func(context.Context) error) {
			defer wg.Done()
			if err := refresh(ctx); err != nil {
				mu.Lock()
				failed = append(failed, name)
				mu.Unlock()
			}
		}(name, refresh)
	}
	wg.Wait()
	if len(failed) != 0 {
		sort.Strings(failed)
		return fmt.Errorf("以下供应商刷新失败：%s", strings.Join(failed, "、"))
	}
	return nil
}
