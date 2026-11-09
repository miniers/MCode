package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// customEndpointTestTimeout 是连通性测试的超时时长，包级变量便于单元测试中缩短。
var customEndpointTestTimeout = 10 * time.Second

func customEndpointBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("BaseURL 必须为不含账号密码、查询参数或片段的 http 或 https 地址")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// normalizePersonaWebURL 校验并规范化 magpie web 管理地址：必须是带主机的 http(s) 地址，
// 且不带账号密码、查询参数或片段；空串表示不配置（由调用方处理）。
func normalizePersonaWebURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("web 管理地址必须是 http(s)://主机:端口")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// newCustomEndpointHarness 为自定义端点构造由内置引擎驱动的 Harness 实例。
// 自定义端点不读 OPENAI_API_KEY 作为兜底密钥：否则内置 OpenAI 的官方密钥会被发给第三方端点。
func newCustomEndpointHarness(ep config.CustomEndpointConfig, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	// dataDir 不可读时仍要能解析到这个端点，所以兜底配置里带上它本身。
	fallback := config.Config{}
	fallback.Providers.Custom = []config.CustomEndpointConfig{ep}
	return newSourceEngineHarness(harness.Kind(config.CustomEndpointProviderKind(ep)), fallback, secrets, dataDir, mcpInjection)
}

// SetProviderStore 设置自定义端点所需的配置文件目录与凭据存储。
func (c *Client) SetProviderStore(dataDir string, secrets config.SecretStore) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	c.dataDir = dataDir
	c.secrets = secrets
}

// SetProviderMCP 设置运行时重建 Provider harness 时使用的内置与外部 MCP 注入配置。
func (c *Client) SetProviderMCP(mcp providerMCP) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	c.mcp = mcp
}

// CustomEndpoints 读取当前配置中的全部自定义端点，并附加密钥是否存在标记。
func (c *Client) CustomEndpoints(ctx context.Context) ([]mcodeui.CustomEndpoint, error) {
	c.providersMu.Lock()
	dataDir := c.dataDir
	secrets := c.secrets
	c.providersMu.Unlock()

	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	res := make([]mcodeui.CustomEndpoint, 0, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		hasKey := false
		hasWebKey := false
		if secrets != nil {
			if val, err := secrets.Get(config.CustomEndpointSecretAccount(ep.ID)); err == nil && strings.TrimSpace(val) != "" {
				hasKey = true
			}
			// web 管理密钥只有 Magpie 端点用，别的端点不白读一次密钥库。
			if ep.Magpie {
				if val, err := secrets.Get(config.CustomEndpointWebKeySecretAccount(ep.ID)); err == nil && strings.TrimSpace(val) != "" {
					hasWebKey = true
				}
			}
		}
		var quotaProviders []string
		if ep.QuotaProviders != nil {
			quotaProviders = make([]string, len(ep.QuotaProviders))
			copy(quotaProviders, ep.QuotaProviders)
		}
		res = append(res, mcodeui.CustomEndpoint{
			ID:             ep.ID,
			Name:           ep.Name,
			BaseURL:        ep.BaseURL,
			Model:          ep.Model,
			Enabled:        ep.Enabled,
			HasAPIKey:      hasKey,
			Magpie:         ep.Magpie,
			QuotaProviders: quotaProviders,
			PersonaWebURL:  ep.PersonaWebURL,
			PersonasJSON:   ep.PersonasJSON,
			PersonaRules:   ep.PersonaRules,
			HasWebKey:      hasWebKey,
		})
	}
	return res, nil
}

// SaveCustomEndpoint 保存或更新自定义端点；持久化配置与凭据并同步更新运行时 Provider 注册。
// webKey 为空串表示保留已保存的 web 管理密钥，非空表示覆盖；ep.PersonaWebURL 为空串表示
// 不再使用 web 人设源，同时删掉已保存的 web 管理密钥。
func (c *Client) SaveCustomEndpoint(ctx context.Context, ep mcodeui.CustomEndpoint, apiKey, webKey string) (mcodeui.CustomEndpoint, error) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	if ep.Magpie && strings.TrimSpace(ep.BaseURL) == "" {
		ep.BaseURL = config.DefaultMagpieBaseURL
	}
	rawBaseURL, err := customEndpointBaseURL(ep.BaseURL)
	if err != nil {
		return mcodeui.CustomEndpoint{}, err
	}
	u, _ := url.Parse(rawBaseURL)

	name := strings.TrimSpace(ep.Name)
	if name == "" {
		if ep.Magpie {
			name = "Magpie"
		} else {
			name = u.Host
		}
	}

	id := strings.TrimSpace(ep.ID)
	if id == "" {
		id = strings.ReplaceAll(newUUID(), "-", "")[:8]
	}

	model := strings.TrimSpace(ep.Model)

	// 人设来源只有 Magpie 端点用得上，非 Magpie 端点顺手清掉，免得配置里残留失效地址。
	personaWebURL := ""
	personasJSON := ""
	personaRules := false
	if ep.Magpie {
		webURL, err := normalizePersonaWebURL(ep.PersonaWebURL)
		if err != nil {
			return mcodeui.CustomEndpoint{}, err
		}
		personaWebURL = webURL
		personasJSON = strings.TrimSpace(ep.PersonasJSON)
		if personasJSON != "" {
			// 手动列表每轮运行前都要能直接解析，现在不拦住以后只会静默失效。
			if _, err := magpie.ParsePersonasJSON(personasJSON); err != nil {
				return mcodeui.CustomEndpoint{}, fmt.Errorf("人设列表解析失败：%w", err)
			}
		}
		personaRules = ep.PersonaRules
	}

	var quotaProviders []string
	if ep.Magpie && ep.QuotaProviders != nil {
		quotaProviders = make([]string, len(ep.QuotaProviders))
		copy(quotaProviders, ep.QuotaProviders)
	}

	epCfg := config.CustomEndpointConfig{
		ID:             id,
		Name:           name,
		BaseURL:        rawBaseURL,
		Model:          model,
		Enabled:        ep.Enabled,
		Magpie:         ep.Magpie,
		QuotaProviders: quotaProviders,
		PersonaWebURL:  personaWebURL,
		PersonasJSON:   personasJSON,
		PersonaRules:   personaRules,
	}

	trimmedKey := strings.TrimSpace(apiKey)
	account := config.CustomEndpointSecretAccount(id)
	var oldKey string
	if trimmedKey != "" {
		if c.secrets == nil {
			return mcodeui.CustomEndpoint{}, errors.New("凭据存储不可用，无法保存 API Key")
		}
		oldKey, err = c.secrets.Get(account)
		if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return mcodeui.CustomEndpoint{}, fmt.Errorf("read api key from secrets: %w", err)
		}
		if err := c.secrets.Set(account, trimmedKey); err != nil {
			return mcodeui.CustomEndpoint{}, fmt.Errorf("save api key to secrets: %w",
				errors.Join(err, restoreEndpointKey(c.secrets, account, oldKey)))
		}
	}

	// web 管理密钥跟 web 地址绑定：地址清空就删密钥，给了新密钥就覆盖，否则保留原样。
	webAccount := config.CustomEndpointWebKeySecretAccount(id)
	trimmedWebKey := strings.TrimSpace(webKey)
	// web 密钥这一段失败时配置不会写，但上面可能已经写过 API Key：一并退回旧值，别留半套凭据。
	withAPIKeyRestore := func(err error) error {
		if trimmedKey == "" {
			return err
		}
		return errors.Join(err, restoreEndpointKey(c.secrets, account, oldKey))
	}
	var oldWebKey string
	webKeyTouched := false
	if c.secrets != nil {
		if personaWebURL == "" {
			oldWebKey, err = c.secrets.Get(webAccount)
			if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
				return mcodeui.CustomEndpoint{}, fmt.Errorf("read web key from secrets: %w", withAPIKeyRestore(err))
			}
			webKeyTouched = oldWebKey != ""
			if err := c.secrets.Delete(webAccount); err != nil && !errors.Is(err, config.ErrSecretNotFound) {
				return mcodeui.CustomEndpoint{}, fmt.Errorf("delete web key from secrets: %w",
					withAPIKeyRestore(errors.Join(err, restoreEndpointKey(c.secrets, webAccount, oldWebKey))))
			}
		} else if trimmedWebKey != "" {
			oldWebKey, err = c.secrets.Get(webAccount)
			if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
				return mcodeui.CustomEndpoint{}, fmt.Errorf("read web key from secrets: %w", withAPIKeyRestore(err))
			}
			if err := c.secrets.Set(webAccount, trimmedWebKey); err != nil {
				return mcodeui.CustomEndpoint{}, fmt.Errorf("save web key to secrets: %w",
					withAPIKeyRestore(errors.Join(err, restoreEndpointKey(c.secrets, webAccount, oldWebKey))))
			}
			webKeyTouched = true
		}
	}

	// 查找与替换在写锁内就地完成：拿回调外的快照整条覆盖会把别的入口（或另一个进程）
	// 刚写的端点改动抹掉；替换时保留由 pi 注入路径单独写入的 PiModels，改个名字不该清空模型勾选。
	if err := config.Update(c.dataDir, func(latest *config.Config) error {
		for i, item := range latest.Providers.Custom {
			if item.ID == id {
				epCfg.PiModels = item.PiModels
				latest.Providers.Custom[i] = epCfg
				return nil
			}
		}
		latest.Providers.Custom = append(latest.Providers.Custom, epCfg)
		return nil
	}); err != nil {
		if trimmedKey != "" {
			err = errors.Join(err, restoreEndpointKey(c.secrets, account, oldKey))
		}
		if webKeyTouched && c.secrets != nil {
			err = errors.Join(err, restoreEndpointKey(c.secrets, webAccount, oldWebKey))
		}
		return mcodeui.CustomEndpoint{}, fmt.Errorf("save config: %w", err)
	}

	// 保存成功后把人设缓存清掉（web 地址可能刚被改过），下一轮运行才用新配置。
	magpie.InvalidatePersonaCache(magpie.PersonaCacheKey(id, personaWebURL))

	if ep.Enabled {
		if c.backend != nil {
			c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))
		}
	} else {
		remote.SetAPICatalog(config.CustomEndpointProviderKind(epCfg), nil)
		if c.backend != nil {
			c.backend.UnregisterHarness(config.CustomEndpointProviderKind(epCfg))
		}
	}

	hasKey := false
	if trimmedKey != "" {
		hasKey = true
	} else if c.secrets != nil {
		if val, err := c.secrets.Get(account); err == nil && strings.TrimSpace(val) != "" {
			hasKey = true
		}
	}

	hasWebKey := false
	if personaWebURL != "" {
		if trimmedWebKey != "" {
			hasWebKey = true
		} else if c.secrets != nil {
			if val, err := c.secrets.Get(webAccount); err == nil && strings.TrimSpace(val) != "" {
				hasWebKey = true
			}
		}
	}

	var retQuotaProviders []string
	if quotaProviders != nil {
		retQuotaProviders = make([]string, len(quotaProviders))
		copy(retQuotaProviders, quotaProviders)
	}

	return mcodeui.CustomEndpoint{
		ID:             id,
		Name:           name,
		BaseURL:        rawBaseURL,
		Model:          model,
		Enabled:        ep.Enabled,
		HasAPIKey:      hasKey,
		Magpie:         ep.Magpie,
		QuotaProviders: retQuotaProviders,
		PersonaWebURL:  personaWebURL,
		PersonasJSON:   personasJSON,
		PersonaRules:   personaRules,
		HasWebKey:      hasWebKey,
	}, nil
}

// DeleteCustomEndpoint 删除指定 ID 的自定义端点、清除其密钥并注销运行时 Provider。
func (c *Client) DeleteCustomEndpoint(ctx context.Context, id string) error {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return errors.New("端点 ID 不能为空")
	}

	cfg, err := config.Load(c.dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	idx := -1
	var removed config.CustomEndpointConfig
	for i, item := range cfg.Providers.Custom {
		if item.ID == cleanID {
			idx = i
			removed = item
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("自定义端点 %q 不存在", cleanID)
	}

	account := config.CustomEndpointSecretAccount(cleanID)
	var oldKey string
	if c.secrets != nil {
		oldKey, err = c.secrets.Get(account)
		if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return fmt.Errorf("read api key from secrets: %w", err)
		}
		if err := c.secrets.Delete(account); err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return fmt.Errorf("delete api key from secrets: %w",
				errors.Join(err, restoreEndpointKey(c.secrets, account, oldKey)))
		}
	}

	// web 管理密钥也要一并删，否则下次复用同一个端点 ID 会莫名带上旧密钥。
	webAccount := config.CustomEndpointWebKeySecretAccount(cleanID)
	var oldWebKey string
	if c.secrets != nil {
		oldWebKey, err = c.secrets.Get(webAccount)
		if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return fmt.Errorf("read web key from secrets: %w", err)
		}
		if err := c.secrets.Delete(webAccount); err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return fmt.Errorf("delete web key from secrets: %w",
				errors.Join(err, restoreEndpointKey(c.secrets, webAccount, oldWebKey)))
		}
	}

	// 删除同样在写锁内按 ID 就地做，不用回调外的快照整条覆盖（会抹掉别处的并发改动）。
	if err := config.Update(c.dataDir, func(latest *config.Config) error {
		for i, item := range latest.Providers.Custom {
			if item.ID != cleanID {
				continue
			}
			removed = item
			latest.Providers.Custom = append(latest.Providers.Custom[:i], latest.Providers.Custom[i+1:]...)
			return nil
		}
		return nil // 已在别处删除，按成功处理
	}); err != nil {
		if c.secrets != nil {
			err = errors.Join(err, restoreEndpointKey(c.secrets, account, oldKey))
			err = errors.Join(err, restoreEndpointKey(c.secrets, webAccount, oldWebKey))
		}
		return fmt.Errorf("save config: %w", err)
	}

	if removed.ID != "" {
		kind := config.CustomEndpointProviderKind(removed)
		remote.SetAPICatalog(kind, nil)
		if c.backend != nil {
			c.backend.UnregisterHarness(kind)
		}
	}
	magpie.InvalidatePersonaCache(magpie.PersonaCacheKey(cleanID, removed.PersonaWebURL))

	return nil
}

func restoreEndpointKey(secrets config.SecretStore, account, key string) error {
	if key == "" {
		return secrets.Delete(account)
	}
	return secrets.Set(account, key)
}

// legacyMagpiePresets 返回当前配置中仍存在的旧版 Magpie 预设。
func legacyMagpiePresets(presets map[string]config.PresetProviderConfig) []apisource.Preset {
	var res []apisource.Preset
	for slot := 1; ; slot++ {
		preset, ok := apisource.Magpie(slot)
		if !ok {
			break
		}
		if _, ok := presets[preset.ID]; ok {
			res = append(res, preset)
		}
	}
	return res
}

// migrateLegacyMagpieEndpoints 把旧版 Magpie 预设迁移为 Magpie 自定义端点：
// 启用状态、BaseURL 或 Model 任一有值的预设生成同 ID 的端点，密钥搬到
// providers.custom.<ID>.apiKey，随后删除旧预设。cfg 会被就地更新并写回配置文件，
// 重复执行不会产生重复条目。
func migrateLegacyMagpieEndpoints(dataDir string, cfg *config.Config, secrets config.SecretStore) error {
	legacy := legacyMagpiePresets(cfg.Providers.Presets)
	if len(legacy) == 0 {
		return nil
	}

	if secrets != nil {
		for _, preset := range legacy {
			if err := migrateLegacyMagpieSecret(secrets, preset); err != nil {
				return err
			}
		}
	}

	existing := make(map[string]struct{}, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		existing[ep.ID] = struct{}{}
	}
	for _, preset := range legacy {
		pc := cfg.Providers.Presets[preset.ID]
		if pc.Enabled || strings.TrimSpace(pc.BaseURL) != "" || strings.TrimSpace(pc.Model) != "" {
			if _, ok := existing[preset.ID]; !ok {
				cfg.Providers.Custom = append(cfg.Providers.Custom, config.CustomEndpointConfig{
					ID:      preset.ID,
					Name:    preset.DisplayName,
					BaseURL: preset.ResolveBaseURL(pc.BaseURL, pc.CodingPlan),
					Model:   strings.TrimSpace(pc.Model),
					Enabled: pc.Enabled,
					Magpie:  true,
				})
				existing[preset.ID] = struct{}{}
			}
		}
		delete(cfg.Providers.Presets, preset.ID)
	}

	return config.Update(dataDir, func(latest *config.Config) error {
		latest.Providers.Custom = cfg.Providers.Custom
		latest.Providers.Presets = cfg.Providers.Presets
		return nil
	})
}

// migrateLegacyMagpieSecret 把旧 Magpie 预设的密钥搬到自定义端点账号下；目标已有密钥时以目标为准。
func migrateLegacyMagpieSecret(secrets config.SecretStore, preset apisource.Preset) error {
	target := config.CustomEndpointSecretAccount(preset.ID)
	targetKey, err := secrets.Get(target)
	if err == nil && strings.TrimSpace(targetKey) != "" {
		return deleteLegacyMagpieSecret(secrets, preset.APIKeyAccount)
	}
	if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
		return fmt.Errorf("read custom endpoint api key: %w", err)
	}

	key, err := secrets.Get(preset.APIKeyAccount)
	if errors.Is(err, config.ErrSecretNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy magpie api key: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		return nil
	}
	if err := secrets.Set(target, key); err != nil {
		return fmt.Errorf("save custom endpoint api key: %w", err)
	}
	return deleteLegacyMagpieSecret(secrets, preset.APIKeyAccount)
}

func deleteLegacyMagpieSecret(secrets config.SecretStore, account string) error {
	if err := secrets.Delete(account); err != nil && !errors.Is(err, config.ErrSecretNotFound) {
		return fmt.Errorf("delete legacy magpie api key: %w", err)
	}
	return nil
}

// TestCustomEndpoint 校验自定义端点 BaseURL 并尝试请求 /models 测试连通性与检索可用模型列表。
func (c *Client) TestCustomEndpoint(ctx context.Context, ep mcodeui.CustomEndpoint, apiKey string) (mcodeui.CustomEndpointTestResult, error) {
	rawBaseURL, err := customEndpointBaseURL(ep.BaseURL)
	if err != nil {
		return mcodeui.CustomEndpointTestResult{}, err
	}

	key := strings.TrimSpace(apiKey)
	if key == "" && ep.ID != "" {
		c.providersMu.Lock()
		sec := c.secrets
		c.providersMu.Unlock()
		if sec != nil {
			if val, err := sec.Get(config.CustomEndpointSecretAccount(ep.ID)); err == nil {
				key = strings.TrimSpace(val)
			}
		}
	}
	// 自定义端点不回退 OPENAI_API_KEY，未配置密钥时不带 Authorization 探测。

	testCtx, cancel := context.WithTimeout(ctx, customEndpointTestTimeout)
	defer cancel()

	targetURL := strings.TrimRight(rawBaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(testCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return mcodeui.CustomEndpointTestResult{}, fmt.Errorf("create test request: %w", err)
	}

	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)

	if err != nil {
		msg := fmt.Sprintf("网络请求失败：%v", err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(testCtx.Err(), context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
			msg = "请求超时"
		}
		return mcodeui.CustomEndpointTestResult{
			OK:      false,
			Latency: latency,
			Message: msg,
		}, nil
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	latency = time.Since(start)
	if readErr != nil || len(body) > 1<<20 {
		msg := "读取响应失败"
		if errors.Is(testCtx.Err(), context.DeadlineExceeded) {
			msg = "请求超时"
		} else if len(body) > 1<<20 {
			msg = "响应超过 1 MiB"
		}
		return mcodeui.CustomEndpointTestResult{
			StatusCode: resp.StatusCode, Latency: latency, Message: msg,
		}, nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Data *[]struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		var models []string
		if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data == nil {
			return mcodeui.CustomEndpointTestResult{
				StatusCode: resp.StatusCode, Latency: latency, Message: "响应不是有效的 OpenAI 模型列表",
			}, nil
		}
		for _, item := range *parsed.Data {
			if id := strings.TrimSpace(item.ID); id != "" {
				models = append(models, id)
			}
		}
		return mcodeui.CustomEndpointTestResult{
			OK:         true,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("连接成功，可用模型 %d 个", len(models)),
			Models:     models,
			Latency:    latency,
		}, nil
	}

	bodyStr := strings.TrimSpace(string(body))
	if key != "" {
		bodyStr = strings.ReplaceAll(bodyStr, key, "[已隐藏]")
	}
	if len(bodyStr) > 200 {
		bodyStr = bodyStr[:200]
	}
	return mcodeui.CustomEndpointTestResult{
		OK:         false,
		StatusCode: resp.StatusCode,
		Message:    fmt.Sprintf("HTTP %d：%s", resp.StatusCode, bodyStr),
		Latency:    latency,
	}, nil
}
