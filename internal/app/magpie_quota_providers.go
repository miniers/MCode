package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"mcode/internal/config"
	"mcode/internal/providers/magpie"
)

// MagpieQuotaProviders 读取已保存的 Magpie 端点卡片上出现的供应商 id（小写、去重、排序），
// 用于设置页勾选菜单栏要展示的额度；端点不存在、不是 Magpie 或没有 key 时返回错误。
func (c *Client) MagpieQuotaProviders(ctx context.Context, endpointID string) ([]string, error) {
	cleanID := strings.TrimSpace(endpointID)
	if cleanID == "" {
		return nil, errors.New("端点 ID 不能为空")
	}

	cfg, ok := c.loadConfig()
	if !ok {
		return nil, errors.New("加载配置失败")
	}

	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	var targetEndpoint *config.CustomEndpointConfig
	for _, ep := range cfg.Providers.Custom {
		if ep.ID == cleanID {
			targetEndpoint = &ep
			break
		}
	}

	if targetEndpoint == nil {
		return nil, fmt.Errorf("端点 %q 不存在", cleanID)
	}
	if !targetEndpoint.Magpie {
		return nil, fmt.Errorf("端点 %q 不是 Magpie 端点", cleanID)
	}

	apiKey := ""
	if secrets != nil {
		if val, err := secrets.Get(config.CustomEndpointSecretAccount(targetEndpoint.ID)); err == nil {
			apiKey = strings.TrimSpace(val)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("端点 %q 未配置 API Key", cleanID)
	}

	baseURL := strings.TrimSpace(targetEndpoint.BaseURL)
	if baseURL == "" {
		baseURL = config.DefaultMagpieBaseURL
	}
	if normalized, err := customEndpointBaseURL(baseURL); err == nil {
		baseURL = normalized
	} else {
		baseURL = strings.TrimRight(baseURL, "/")
	}

	quotaCtx, cancel := context.WithTimeout(ctx, piMagpieFetchTimeout)
	defer cancel()

	windows, err := magpie.ReadQuotas(quotaCtx, baseURL, apiKey)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var providers []string
	for _, w := range windows {
		p := strings.ToLower(strings.TrimSpace(magpie.AccountProvider(w.Account)))
		if p == "" {
			continue
		}
		if !seen[p] {
			seen[p] = true
			providers = append(providers, p)
		}
	}

	sort.Strings(providers)
	return providers, nil
}
