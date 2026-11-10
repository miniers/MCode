package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/acp"
	mcodeui "mcode/internal/ui"
)

// droidAPIKeyAccount 是 keyring 中 Droid API Key 的账号名。
const droidAPIKeyAccount = "droid.apiKey"

// providerSettingSpec 描述设置页中单个 Provider 的静态元信息。
type providerSettingSpec struct {
	id   string
	name string
	kind string // "cli" 或 "api"
}

// providerSettingSpecs 是设置页 Provider 列表的固定顺序。
// ACP Agent（droid/cline/cursor/opencode/grok/devin）与 CLI 家族按 Kind=cli，
// Anthropic 与 OpenAI 兼容预设按 Kind=api。
var providerSettingSpecs = []providerSettingSpec{
	{id: "copilot", name: "GitHub Copilot", kind: "cli"},
	{id: "pi", name: "Pi", kind: "cli"},
	{id: "omp", name: "Omp", kind: "cli"},
	{id: "commandcode", name: "Command Code", kind: "cli"},
	{id: "droid", name: "Droid", kind: "cli"},
	{id: "cline", name: "Cline", kind: "cli"},
	{id: "cursor", name: "Cursor", kind: "cli"},
	{id: "opencode", name: "OpenCode", kind: "cli"},
	{id: "grok", name: "Grok", kind: "cli"},
	{id: "devin", name: "Devin", kind: "cli"},
	{id: "anthropic", name: "Anthropic", kind: "api"},
	{id: "openai", name: "OpenAI", kind: "api"},
	{id: "deepseek", name: "DeepSeek", kind: "api"},
	{id: "zai", name: "Z.ai", kind: "api"},
	{id: "meta", name: "Meta", kind: "api"},
}

func providerSettingSpecByID(id string) (providerSettingSpec, bool) {
	for _, spec := range providerSettingSpecs {
		if spec.id == id {
			return spec, true
		}
	}
	return providerSettingSpec{}, false
}

// providerSettingIsACP 判断该 Provider 是否由 ACP 循环注册（见 acp.Agents）。
func providerSettingIsACP(id string) bool {
	for _, a := range acp.Agents() {
		if string(a) == id {
			return true
		}
	}
	return false
}

// providerSettingSecretAccount 返回 Provider 对应的 keyring 账号名；不支持 API Key 的返回空串。
func providerSettingSecretAccount(id string) string {
	if id == "droid" {
		return droidAPIKeyAccount
	}
	if id == "anthropic" {
		return "anthropic.apiKey"
	}
	if id == "openai" {
		return "openai.apiKey"
	}
	if preset, ok := apisource.PresetByID(id); ok {
		return preset.APIKeyAccount
	}
	return ""
}

// optionalTruePtr 在值为 true 时返回 nil，省略该项即视为开启。
func optionalTruePtr(enabled bool) *bool {
	if enabled {
		return nil
	}
	off := false
	return &off
}

// hasProviderSecret 判断 keyring 中是否已保存该账号的非空密钥。
func hasProviderSecret(secrets config.SecretStore, account string) bool {
	if secrets == nil || account == "" {
		return false
	}
	val, err := secrets.Get(account)
	return err == nil && strings.TrimSpace(val) != ""
}

// buildProviderSetting 由配置与凭据存储组装单个 Provider 的设置项。
func buildProviderSetting(cfg config.Config, secrets config.SecretStore, spec providerSettingSpec) mcodeui.ProviderSetting {
	s := mcodeui.ProviderSetting{
		ID:   spec.id,
		Name: spec.name,
		Kind: spec.kind,
	}

	switch spec.id {
	case "copilot":
		s.Enabled = cfg.Providers.Copilot.Enabled
		s.Bin = cfg.Providers.Copilot.Bin
	case "anthropic":
		// Anthropic 来源同时看配置开关与 Key：没有 Key 时仍显示为关闭。
		hasKey := hasProviderSecret(secrets, providerSettingSecretAccount(spec.id)) || hasProviderSecret(secrets, mcodeAnthropicAPIKeyAccount) || apisource.AnthropicAPIKey(nil) != ""
		s.HasAPIKey = hasKey
		s.Enabled = cfg.Providers.Anthropic.IsEnabled() && hasKey
	case "openai":
		s.Enabled = cfg.Providers.OpenAI.Enabled
		s.BaseURL = cfg.Providers.OpenAI.BaseURL
		s.Model = cfg.Providers.OpenAI.Model
		s.HasAPIKey = hasProviderSecret(secrets, providerSettingSecretAccount(spec.id))
	case "pi":
		s.Enabled = cfg.Providers.Pi.Enabled
		s.Bin = cfg.Providers.Pi.Bin
		s.PiCacheLong = cfg.Providers.Pi.PiCacheLong
		s.PiVisionModel = cfg.Providers.Pi.PiVisionModel
	case "omp":
		s.Enabled = cfg.Providers.Omp.Enabled
		s.Bin = cfg.Providers.Omp.Bin
	case "commandcode":
		s.Enabled = cfg.Providers.CommandCode.Enabled
		s.Bin = cfg.Providers.CommandCode.Bin
	case "droid", "cline", "cursor", "opencode", "grok", "devin":
		pc, ok := cfg.Providers.ACP[spec.id]
		// ACP map 中缺省时视为启用，与 registerExtraProviders 的注册逻辑一致。
		s.Enabled = !ok || pc.Enabled
		s.Bin = pc.Bin
		if spec.id == "droid" {
			s.Auth = pc.Auth
			s.HasAPIKey = hasProviderSecret(secrets, droidAPIKeyAccount)
		}
	default:
		preset, ok := apisource.PresetByID(spec.id)
		if !ok {
			return s
		}
		pc := cfg.Providers.Presets[spec.id]
		s.Enabled = pc.Enabled
		s.BaseURL = pc.BaseURL
		s.Model = pc.Model
		s.DefaultBaseURL = preset.DefaultBaseURL
		s.DefaultModel = preset.DefaultModel
		s.HasAPIKey = hasProviderSecret(secrets, preset.APIKeyAccount)
	}
	return s
}

// applyProviderSetting 把设置页提交的单个 Provider 配置写回配置结构体。
func applyProviderSetting(cfg *config.Config, id string, s mcodeui.ProviderSetting) {
	switch id {
	case "copilot":
		cfg.Providers.Copilot.Enabled = s.Enabled
		cfg.Providers.Copilot.Bin = strings.TrimSpace(s.Bin)
	case "anthropic":
		cfg.Providers.Anthropic.Enabled = optionalTruePtr(s.Enabled)
	case "openai":
		cfg.Providers.OpenAI.Enabled = s.Enabled
		cfg.Providers.OpenAI.BaseURL = strings.TrimSpace(s.BaseURL)
		cfg.Providers.OpenAI.Model = strings.TrimSpace(s.Model)
	case "pi":
		cfg.Providers.Pi.Enabled = s.Enabled
		cfg.Providers.Pi.Bin = strings.TrimSpace(s.Bin)
		cfg.Providers.Pi.PiCacheLong = s.PiCacheLong
		cfg.Providers.Pi.PiVisionModel = strings.TrimSpace(s.PiVisionModel)
	case "omp":
		cfg.Providers.Omp.Enabled = s.Enabled
		cfg.Providers.Omp.Bin = strings.TrimSpace(s.Bin)
	case "commandcode":
		cfg.Providers.CommandCode.Enabled = s.Enabled
		cfg.Providers.CommandCode.Bin = strings.TrimSpace(s.Bin)
	case "droid", "cline", "cursor", "opencode", "grok", "devin":
		pc := cfg.Providers.ACP[id]
		pc.Enabled = s.Enabled
		pc.Bin = strings.TrimSpace(s.Bin)
		if id == "droid" {
			pc.Auth = strings.TrimSpace(s.Auth)
		}
		if cfg.Providers.ACP == nil {
			cfg.Providers.ACP = make(map[string]config.ProviderConfig)
		}
		cfg.Providers.ACP[id] = pc
	default:
		pc := cfg.Providers.Presets[id]
		pc.Enabled = s.Enabled
		pc.BaseURL = strings.TrimSpace(s.BaseURL)
		pc.Model = strings.TrimSpace(s.Model)
		if cfg.Providers.Presets == nil {
			cfg.Providers.Presets = make(map[string]config.PresetProviderConfig)
		}
		cfg.Providers.Presets[id] = pc
	}
}

// ProviderSettings 读取设置页的 Provider 配置列表（桌面专用，不走 RPC）。
// 列表顺序固定为 providerSettingSpecs；API Key 不回传明文，只用 HasAPIKey 表示是否已保存。
func (c *Client) ProviderSettings(ctx context.Context) ([]mcodeui.ProviderSetting, error) {
	c.providersMu.Lock()
	dataDir := c.dataDir
	secrets := c.secrets
	c.providersMu.Unlock()

	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	res := make([]mcodeui.ProviderSetting, 0, len(providerSettingSpecs))
	for _, spec := range providerSettingSpecs {
		res = append(res, buildProviderSetting(cfg, secrets, spec))
	}
	return res, nil
}

// releaseReplacedHarness 注销即将被替换的 harness，先让它放掉空闲驻留的子进程。
// 正在执行的轮次不受影响（只支持 CloseIdle 的 harness 才做这步）；用接口探测，避免 app 层依赖具体 provider。
func (c *Client) releaseReplacedHarness(kind string) {
	if c.backend == nil {
		return
	}
	if old := c.backend.RegisteredHarness(kind); old != nil {
		if closer, ok := old.(interface{ CloseIdle() }); ok {
			closer.CloseIdle()
		}
	}
	c.backend.UnregisterHarness(kind)
}

// SaveProviderSetting 保存单个 Provider 的配置并热更新运行时 harness（桌面专用，不走 RPC）；
// apiKey 为空表示保留已保存的 Key。
func (c *Client) SaveProviderSetting(ctx context.Context, s mcodeui.ProviderSetting, apiKey string) (mcodeui.ProviderSetting, error) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	spec, ok := providerSettingSpecByID(strings.TrimSpace(s.ID))
	if !ok {
		return mcodeui.ProviderSetting{}, fmt.Errorf("未知的 Provider ID %q", s.ID)
	}

	trimmedKey := strings.TrimSpace(apiKey)
	var account, oldKey string
	var oldKeyExists bool
	if trimmedKey != "" {
		account = providerSettingSecretAccount(spec.id)
		if account == "" {
			return mcodeui.ProviderSetting{}, fmt.Errorf("Provider %s 不支持 API Key", spec.id)
		}
		if c.secrets == nil {
			return mcodeui.ProviderSetting{}, errors.New("凭据存储不可用，无法保存 API Key")
		}
		var err error
		oldKey, err = c.secrets.Get(account)
		if err != nil && !errors.Is(err, config.ErrSecretNotFound) {
			return mcodeui.ProviderSetting{}, fmt.Errorf("read api key from secrets: %w", err)
		}
		oldKeyExists = err == nil
		if err := c.secrets.Set(account, trimmedKey); err != nil {
			return mcodeui.ProviderSetting{}, fmt.Errorf("save api key to secrets: %w", err)
		}
	}

	if err := config.Update(c.dataDir, func(latest *config.Config) error {
		applyProviderSetting(latest, spec.id, s)
		return nil
	}); err != nil {
		if trimmedKey != "" {
			var restoreErr error
			if oldKeyExists {
				restoreErr = c.secrets.Set(account, oldKey)
			} else {
				restoreErr = c.secrets.Delete(account)
			}
			err = errors.Join(err, restoreErr)
		}
		return mcodeui.ProviderSetting{}, fmt.Errorf("save config: %w", err)
	}

	cfg, err := config.Load(c.dataDir)
	if err != nil {
		return mcodeui.ProviderSetting{}, fmt.Errorf("load config: %w", err)
	}

	// 热更新运行时注册：先注销旧 harness，启用时按新配置重建并注册。
	// ACP（droid/cline/cursor/opencode/grok/devin）的 harness 由启动时的 ACP 循环构造（含 droid 认证环境变量），
	// 此处只写配置，启用/停用与 Bin、Auth 变更均需重启后生效。
	if c.backend != nil && !providerSettingIsACP(spec.id) {
		c.releaseReplacedHarness(spec.id)
		if spec.id == "anthropic" {
			// 先注销后重建：保存了新 Key 时按最新来源重新注册，没有 Key 时保持注销。
			if _, ok := findAPISource(apisource.Resolve(cfg, c.secrets), anthropicEngineKind); ok {
				c.backend.RegisterHarness(newAnthropicEngineHarness(cfg, c.secrets, c.dataDir, c.mcp))
			}
		} else if h, ok := buildProviderHarness(cfg, c.secrets, spec.id, c.mcp); ok {
			c.backend.RegisterHarness(h)
		}
	}

	return buildProviderSetting(cfg, c.secrets, spec), nil
}
