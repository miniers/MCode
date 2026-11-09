package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

var serverMagpieWarm sync.Map

func warmServerMagpie(baseURL, key string) {
	if key == "" {
		return
	}
	cacheKey := baseURL + "\x00" + key
	if _, loaded := serverMagpieWarm.LoadOrStore(cacheKey, struct{}{}); loaded {
		return
	}
	go func() {
		defer serverMagpieWarm.Delete(cacheKey)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = magpie.FetchModels(ctx, baseURL, key)
	}()
}

// serverAPISources 只读兼容尚未迁移的旧 Magpie 槽位。
func serverAPISources(cfg config.Config, secrets config.SecretStore) []apisource.Source {
	sources := apisource.Resolve(cfg, secrets)
	existing := make(map[string]bool)
	for _, ep := range cfg.Providers.Custom {
		existing[ep.ID] = true
	}
	for slot := 1; ; slot++ {
		preset, ok := apisource.Magpie(slot)
		if !ok {
			break
		}
		pc, ok := cfg.Providers.Presets[preset.ID]
		if !ok || !pc.Enabled || existing[preset.ID] {
			continue
		}
		ep := config.CustomEndpointConfig{ID: preset.ID, Magpie: true}
		sources = append(sources, apisource.Source{
			Kind: config.CustomEndpointProviderKind(ep), Label: preset.DisplayName,
			Protocol: apisource.ProtocolOpenAI, BaseURL: preset.ResolveBaseURL(pc.BaseURL, pc.CodingPlan),
			DefaultModel: strings.TrimSpace(pc.Model), Magpie: true,
			APIKeyAccounts: []string{config.CustomEndpointSecretAccount(preset.ID), preset.APIKeyAccount}, APIKeyEnv: preset.APIKeyEnv,
		})
	}
	return sources
}

func registerAPIEngines(backend *remote.MemoryBackend, cfg config.Config, secrets config.SecretStore, dataDir string) []string {
	current := func() config.Config {
		if loaded, err := config.Load(dataDir); err == nil {
			config.ApplyEnv(&loaded, os.LookupEnv)
			return loaded
		}
		return cfg
	}
	find := func(kind string) (apisource.Source, bool) {
		for _, source := range serverAPISources(current(), secrets) {
			if source.Kind == kind {
				return source, true
			}
		}
		return apisource.Source{}, false
	}
	var kinds []string
	for _, source := range serverAPISources(cfg, secrets) {
		source := source
		kind := source.Kind
		catalog := func() []wire.ProviderModel {
			s, ok := find(kind)
			if !ok {
				return nil
			}
			ids := append([]string(nil), s.Models...)
			if s.Magpie {
				ids = nil
				key := s.APIKey(secrets)
				models, hit := magpie.CachedModels(s.BaseURL, key)
				if !hit {
					warmServerMagpie(s.BaseURL, key)
				}
				for _, model := range models {
					if model.ID == s.DefaultModel {
						ids = append(ids, model.ID)
						break
					}
				}
				for _, model := range models {
					ids = append(ids, model.ID)
				}
			} else if s.DefaultModel != "" {
				ids = append([]string{s.DefaultModel}, ids...)
			}
			seen := map[string]bool{}
			var rows []wire.ProviderModel
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				rows = append(rows, wire.ProviderModel{ID: id, Name: id, IsDefault: len(rows) == 0})
			}
			return rows
		}
		remote.SetAPICatalog(kind, catalog)
		opts := providermcode.Options{
			Kind: harness.Kind(kind), DisplayName: source.Label, StoreDir: filepath.Join(dataDir, "mcode", "sessions"),
			ModelIDs: func() []string {
				var ids []string
				for _, row := range catalog() {
					ids = append(ids, row.ID)
				}
				return ids
			},
			ResolveClient: func(model, affinity string) (llm.Client, string, error) {
				s, ok := find(kind)
				if !ok {
					return nil, "", fmt.Errorf("来源 %s 未启用或已删除", kind)
				}
				model = strings.TrimSpace(model)
				if model == "" || strings.EqualFold(model, "default") {
					model = strings.TrimSpace(s.DefaultModel)
					if s.Magpie {
						models, _ := magpie.CachedModels(s.BaseURL, s.APIKey(secrets))
						if len(models) == 0 {
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							var err error
							models, err = magpie.FetchModels(ctx, s.BaseURL, s.APIKey(secrets))
							cancel()
							if err != nil {
								return nil, "", fmt.Errorf("来源 %s 获取模型目录失败：%w", kind, err)
							}
						}
						found := false
						for _, candidate := range models {
							if candidate.ID == model {
								found = true
								break
							}
						}
						if !found {
							model = ""
							if len(models) > 0 {
								model = models[0].ID
							}
						}
					} else if model == "" && kind == "openai" {
						model = "gpt-4o"
					}
				}
				if s.Protocol == apisource.ProtocolAnthropic {
					model = strings.TrimPrefix(model, "anthropic/")
				}
				if model == "" {
					return nil, "", fmt.Errorf("来源 %s 未配置默认模型", kind)
				}
				key := s.APIKey(secrets)
				headers := s.Headers
				if s.Magpie && affinity != "" {
					headers = map[string]string{"session_id": affinity, "x-client-request-id": affinity, "x-session-affinity": affinity}
				}
				return apisource.NewClient(s, key, model, apisource.ClientOptions{Headers: headers, Effort: serverReasoningEffort(s.BaseURL)}), model, nil
			},
			ContextWindow: func(model string) int {
				s, ok := find(kind)
				if !ok {
					return 0
				}
				if s.Protocol == apisource.ProtocolAnthropic {
					return 200000
				}
				if !s.Magpie {
					return 0
				}
				if cached, hit := magpie.CachedModel(s.BaseURL, s.APIKey(secrets), model); hit {
					return cached.ContextWindow
				}
				return 0
			},
			MaxTokens: func(model string) int {
				s, ok := find(kind)
				if !ok || !s.Magpie {
					return 0
				}
				if cached, hit := magpie.CachedModel(s.BaseURL, s.APIKey(secrets), model); hit {
					return cached.MaxTokens
				}
				return 0
			},
		}
		if source.Magpie {
			opts.RefreshModels = func(ctx context.Context) error {
				s, ok := find(kind)
				if !ok || !s.Magpie {
					return fmt.Errorf("来源 %s 未启用或已删除", kind)
				}
				_, err := magpie.FetchModels(ctx, s.BaseURL, s.APIKey(secrets))
				return err
			}
		}
		backend.RegisterHarness(providermcode.New(opts))
		kinds = append(kinds, kind)
	}
	return kinds
}

func serverReasoningEffort(baseURL string) func(string, string) (string, bool) {
	return func(model, effort string) (string, bool) {
		if effort == "" || strings.EqualFold(model, "deepseek-reasoner") {
			return "", false
		}
		if strings.TrimRight(strings.TrimSpace(baseURL), "/") != "https://api.openai.com/v1" {
			return effort, true
		}
		lower := strings.ToLower(model)
		if !(strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.Contains(lower, "reasoner") || strings.Contains(lower, "r1") || strings.Contains(lower, "thinking") || strings.Contains(lower, "qwq")) {
			return "", false
		}
		switch effort {
		case "low", "medium", "high":
			return effort, true
		case "max", "xhigh", "extra-high":
			return "high", true
		}
		return "", false
	}
}
