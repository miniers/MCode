package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/modelsdev"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

type textgenCandidate struct {
	source apisource.Source
	model  string
	cost   *wire.ModelCost
	order  int
}

type textgenNoticeKey struct{}
type textgenGlobalSelectionKey struct{}

func withTextgenGlobalSelection(ctx context.Context, cfg config.Config) context.Context {
	if strings.TrimSpace(cfg.SourceControl.CommitMessageModel.Provider) == "" {
		return context.WithValue(ctx, textgenGlobalSelectionKey{}, true)
	}
	return ctx
}

func withTextgenFallbackNotice(ctx context.Context, notice func(from, to string)) context.Context {
	return context.WithValue(ctx, textgenNoticeKey{}, notice)
}

func textgenFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	for _, family := range []string{"deepseek", "claude", "gpt"} {
		if strings.HasPrefix(model, family) {
			return family
		}
	}
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		if model == alias || strings.HasPrefix(model, alias+"-") {
			return "claude"
		}
	}
	return ""
}

func sortTextgenCandidates(candidates []textgenCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a.cost == nil) != (b.cost == nil) {
			return a.cost != nil
		}
		if a.cost != nil && b.cost != nil {
			if x, y := a.cost.Input+a.cost.Output, b.cost.Input+b.cost.Output; x != y {
				return x < y
			}
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return a.model < b.model
	})
}

func selectTextgenCandidate(candidates []textgenCandidate, provider, model string, global bool) (textgenCandidate, bool) {
	if textgenModelOverride(model) != "" {
		return textgenCandidate{}, false
	}
	for _, candidate := range candidates {
		if candidate.cost != nil && (global || strings.EqualFold(candidate.source.Kind, provider)) {
			return candidate, true
		}
	}
	return textgenCandidate{}, false
}

func textgenSourceUsable(cfg config.Config, secrets config.SecretStore, source apisource.Source) bool {
	if source.APIKey(secrets) != "" {
		return true
	}
	for _, ep := range cfg.Providers.Custom {
		if ep.Enabled && !ep.Magpie && strings.EqualFold(config.CustomEndpointProviderKind(ep), source.Kind) {
			return true
		}
	}
	return false
}

func collectTextgenCandidates(ctx context.Context, cfg config.Config, secrets config.SecretStore) []textgenCandidate {
	var candidates []textgenCandidate
	for order, source := range apisource.Resolve(cfg, secrets) {
		if ctx.Err() != nil {
			break
		}
		if !textgenSourceUsable(cfg, secrets, source) {
			continue
		}
		var ids []string
		if source.Magpie {
			for _, target := range apisource.MagpieTargets(cfg, secrets) {
				if target.BaseURL == source.BaseURL && target.APIKey == source.APIKey(secrets) {
					if _, hit := magpie.CachedModels(target.BaseURL, target.APIKey); !hit {
						loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
						_, _ = magpie.FetchModels(loadCtx, target.BaseURL, target.APIKey)
						cancel()
					}
					break
				}
			}
		} else if len(source.Models) == 0 {
			ids = sourceModelCache.models(source, source.APIKey(secrets))
			if len(ids) == 0 {
				_ = sourceModelCache.refresh(ctx, source, source.APIKey(secrets))
				ids = sourceModelCache.models(source, source.APIKey(secrets))
			}
		}
		refs := make(map[string]*wire.ModelCost)
		for _, row := range remote.ProviderModels(source.Kind) {
			refs[strings.ToLower(row.ID)] = row.Cost
		}
		for _, row := range apiSourceCatalog(cfg, secrets, source.Kind, ids) {
			id := strings.TrimSpace(row.ID)
			family := textgenFamily(id)
			if id == "" || strings.EqualFold(id, "default") {
				continue
			}
			actual := id
			if i := strings.LastIndex(actual, "/"); i >= 0 {
				actual = actual[i+1:]
			}
			lowerID := strings.ToLower(actual)
			if strings.Contains(lowerID, "embedding") || strings.Contains(lowerID, "embed-") || strings.Contains(lowerID, "image") || strings.Contains(lowerID, "audio") || strings.Contains(lowerID, "moderation") {
				continue
			}
			provider := source.Kind
			if family == "claude" {
				provider = "anthropic"
			} else if family == "gpt" {
				provider = "openai"
			}
			info, known := modelsdev.Default.Lookup(provider, actual)
			if known && len(info.InputModalities) > 0 {
				text := false
				for _, m := range info.InputModalities {
					if m == "text" {
						text = true
					}
				}
				if !text {
					continue
				}
			}
			if !known {
				_, inReference := refs[strings.ToLower(id)]
				knownStatic := false
				for _, staticID := range source.Models {
					if strings.EqualFold(id, staticID) {
						knownStatic = true
						break
					}
				}
				if !inReference && !knownStatic {
					continue
				}
			}
			cost := row.Cost
			if cost == nil {
				cost = refs[strings.ToLower(id)]
			}
			if cost == nil && known && info.Cost != nil {
				cost = &wire.ModelCost{Input: info.Cost.Input, Output: info.Cost.Output}
			}
			candidates = append(candidates, textgenCandidate{source: source, model: id, cost: cost, order: order})
		}
	}
	sortTextgenCandidates(candidates)
	return candidates
}

func executeTextgenFallback(ctx context.Context, primary textgenCandidate, candidates []textgenCandidate, call func(context.Context, textgenCandidate) (string, error)) (string, error) {
	out, err := call(ctx, primary)
	if err == nil || ctx.Err() != nil {
		return out, err
	}
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || (apiErr.Status != 429 && (apiErr.Status < 500 || apiErr.Status > 599)) {
		return out, err
	}
	family := textgenFamily(primary.model)
	if family == "" {
		return out, err
	}
	for _, sameSource := range []bool{false, true} {
		for _, candidate := range candidates {
			same := candidate.source.Kind == primary.source.Kind && candidate.source.BaseURL == primary.source.BaseURL
			if same != sameSource || (same && candidate.model == primary.model) || textgenFamily(candidate.model) != family {
				continue
			}
			if notice, ok := ctx.Value(textgenNoticeKey{}).(func(string, string)); ok && notice != nil {
				notice(primary.source.Kind+" / "+primary.model, candidate.source.Kind+" / "+candidate.model)
			}
			return call(ctx, candidate)
		}
	}
	return out, err
}
