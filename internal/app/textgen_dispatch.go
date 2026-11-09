package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	"mcode/internal/providers/pi"
	"mcode/internal/textgen"
)

// textgenBin 返回 CLI 型 provider 使用的可执行文件路径，未知 provider 返回空串。
// 配置了 Bin 时直接用它；未配置时按 provider 的默认命令名在登录 shell 的 PATH
// 与常见安装目录中解析（GUI 进程自己的 PATH 通常找不到这些 CLI）；
// 都找不到时返回空串，由 textgen 侧按裸命令名尝试并在 exec 处报 not found。
func textgenBin(cfg config.Config, provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		if bin := cfg.Providers.Claude.Bin; bin != "" {
			return bin
		}
		return findProviderBinary("claude")
	case "codex":
		if bin := cfg.Providers.Codex.Bin; bin != "" {
			return bin
		}
		return findProviderBinary("codex")
	case "agy":
		return cfg.Providers.Agy.Bin
	case "pi":
		if bin := cfg.Providers.Pi.Bin; bin != "" {
			return bin
		}
		return findProviderBinary("pi")
	}
	return ""
}

// findProviderBinary 按默认命令名在登录 shell 的 PATH 与常见安装目录中解析可执行文件，
// 找不到时返回空串。
func findProviderBinary(name string) string {
	if path, ok := providerBinaryFinder([]string{name}, providerFallbackDirs(name)); ok {
		return path
	}
	return ""
}

// textgenModelOverride 返回调用方指定的模型名；为空或 "default" 时表示未指定。
func textgenModelOverride(model string) string {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "default") {
		return ""
	}
	return model
}

// resolveTextgenSource 区分未知来源与已停用的 API 来源。
func resolveTextgenSource(cfg config.Config, secrets config.SecretStore, provider string) (apisource.Source, bool, error) {
	for _, source := range apisource.Resolve(cfg, secrets) {
		if strings.EqualFold(source.Kind, provider) {
			return source, true, nil
		}
	}
	for _, kind := range apisource.Kinds(cfg) {
		if strings.EqualFold(kind, provider) {
			return apisource.Source{}, true, fmt.Errorf("来源 %s 未启用或缺少 API Key", provider)
		}
	}
	return apisource.Source{}, false, nil
}

// textgenModelSelection 返回一次性文本生成（Git 提交信息、会话标题）共用的模型选择。
// 设置页指定的「提交信息 / 会话标题生成模型」优先；未指定时回退通用 provider，
// model 留空表示用该 provider 的默认模型。
func textgenModelSelection(cfg config.Config) (provider, model, effort string) {
	provider = cfg.Models.Provider
	if sel := cfg.SourceControl.CommitMessageModel; strings.TrimSpace(sel.Provider) != "" {
		provider = strings.TrimSpace(sel.Provider)
		model = strings.TrimSpace(sel.Model)
		effort = strings.TrimSpace(sel.Effort)
	}
	return provider, model, effort
}

// textgenPiMagpieInjection 是 pi 一次性文本生成取 Magpie 注入信息的来源，测试可替换。
var textgenPiMagpieInjection = piMagpieInjection

// piTextgenOptions 组装 pi 一次性文本生成的选项：同时拿到 Magpie 桥路径与端点清单时，
// 用 -e 加载桥扩展并通过环境变量下发清单（apiKey 只随清单进环境变量，不进命令行参数）。
func piTextgenOptions(cfg config.Config, model, effort string) textgen.Options {
	opts := textgen.Options{
		Provider:   "pi",
		Model:      model,
		Effort:     effort,
		BinaryPath: textgenBin(cfg, "pi"),
	}
	bridge, env := textgenPiMagpieInjection()
	bridge = strings.TrimSpace(bridge)
	env = strings.TrimSpace(env)
	if bridge == "" || env == "" {
		return opts
	}
	opts.Extensions = []string{bridge}
	opts.ExtraEnv = []string{
		pi.MagpieEnvVar + "=" + env,
		pi.MagpieBridgeEnvVar + "=" + bridge,
	}
	return opts
}

// cliTextgenOptions 为 Claude 和 Codex 按当前 Magpie 模式组装调用选项。
func cliTextgenOptions(cfg config.Config, secrets config.SecretStore, provider, model, effort string) (textgen.Options, error) {
	opts := textgen.Options{Provider: provider, Model: model, Effort: effort, BinaryPath: textgenBin(cfg, provider)}
	routing := ResolveAgentRouting(cfg, secrets, provider)
	if routing.Mode == config.MagpieModeGlobal {
		if strings.TrimSpace(model) == "" {
			opts.Model = routing.Default.ModelID
		}
		return opts, nil
	}
	if routing.Mode != config.MagpieModeInject {
		return opts, nil
	}
	ref := strings.TrimSpace(model)
	if ref == "" || strings.EqualFold(ref, "default") {
		if routing.Default.ModelID == "" {
			return opts, nil
		}
		ref = routing.Default.Ref()
	}
	if selected, target, ok := routing.Lookup(ref); ok {
		opts.Model = selected.ModelID
		if provider == "claude" {
			opts.ExtraEnv = claude.MagpieEnv(target.BaseURL, target.APIKey)
		} else {
			route := codex.MagpieRoute{ProviderID: target.ProviderID, BaseURL: target.BaseURL, APIKey: target.APIKey}
			opts.ExtraArgs = codex.MagpieArgs(route)
			opts.ExtraEnv = codex.MagpieEnv(route)
		}
		return opts, nil
	}
	if prefix, _, ok := strings.Cut(ref, "/"); ok {
		taken := make(map[string]bool)
		for _, ep := range cfg.Providers.Custom {
			if !ep.Magpie {
				continue
			}
			id := magpie.ProviderID(ep.Name, taken)
			if id == prefix {
				return opts, fmt.Errorf("%s 的 Magpie 模型 %q 未启用或不可用", provider, ref)
			}
			if ep.Enabled {
				taken[id] = true
			}
		}
		if strings.HasPrefix(prefix, "magpie-") {
			return opts, fmt.Errorf("%s 的 Magpie 模型 %q 未启用或不可用", provider, ref)
		}
	}
	return opts, nil
}

// cliTextgenAvailable 判断一次性文本生成用的 CLI 是否可用：
// PATH 中能找到，或用户显式配置了存在的 bin 文件。配置的 bin 不存在时按未配置处理。
func cliTextgenAvailable(cfg config.Config, provider string) bool {
	if textgen.AvailableProvider(provider) {
		return true
	}
	var bin string
	switch provider {
	case "claude":
		bin = strings.TrimSpace(cfg.Providers.Claude.Bin)
	case "codex":
		bin = strings.TrimSpace(cfg.Providers.Codex.Bin)
	default:
		return false
	}
	if bin == "" {
		return false
	}
	_, err := os.Stat(bin)
	return err == nil
}

// generateText 按层次选择一次性文本生成后端：
// 本地 CLI / memory → OpenAI 兼容端点 → 回退 claude、codex、mcode。
// effort 为推理强度，为空时交给各后端默认值。
func generateText(ctx context.Context, cfg config.Config, secrets config.SecretStore, provider, model, effort, prompt string) (string, error) {
	raw := strings.TrimSpace(provider)
	lower := strings.ToLower(raw)
	if lower == "" {
		lower = "claude"
	}
	mcodeEnabled := cfg.Providers.Mcode.Enabled

	// pi 额外需要 Magpie 桥注入，单独走一项组装选项的分支。
	if lower == "pi" {
		return textgen.GenerateWithOptions(ctx, piTextgenOptions(cfg, model, effort), prompt)
	}

	// 第一层：本地 CLI 与 memory 直接走 textgen.Generate。
	if lower == "claude" || lower == "codex" {
		// CLI 没装时不传 claude 或 codex 的模型名，直接交给内置 mcode 的默认模型。
		if mcodeEnabled && !cliTextgenAvailable(cfg, lower) {
			return mcodeGenerateText(ctx, cfg, secrets, "", effort, prompt)
		}
		opts, err := cliTextgenOptions(cfg, secrets, lower, model, effort)
		if err != nil {
			return "", err
		}
		return textgen.GenerateWithOptions(ctx, opts, prompt)
	}
	if lower == "memory" {
		return textgen.GenerateWithOptions(ctx, textgen.Options{Provider: lower, Model: model, Effort: effort}, prompt)
	}

	// 内置 mcode provider 直接用内部模型客户端，不走 CLI、memory 或 OpenAI 兼容端点。
	if lower == config.ProviderMcode {
		return mcodeGenerateText(ctx, cfg, secrets, model, effort, prompt)
	}

	// 第二层：API 来源使用对应协议的无工具流式请求。
	if source, ok, err := resolveTextgenSource(cfg, secrets, raw); ok {
		if err != nil {
			return "", err
		}
		key := source.APIKey(secrets)
		optionalKey := false
		for _, ep := range cfg.Providers.Custom {
			if !ep.Magpie && strings.EqualFold(config.CustomEndpointProviderKind(ep), source.Kind) {
				optionalKey = true
				break
			}
		}
		if key == "" && !optionalKey {
			return "", fmt.Errorf("来源 %s 缺少 API Key", raw)
		}
		modelID := textgenModelOverride(model)
		if modelID == "" {
			if source.Magpie {
				_, modelID, err = resolveSourceClient(harness.Kind(source.Kind), cfg, secrets, "", "")
				if err != nil {
					return "", err
				}
			} else {
				modelID = sourceEngineDefaultModel(source, harness.Kind(source.Kind), secrets)
			}
		}
		if modelID == "" {
			return "", fmt.Errorf("来源 %s 未配置默认模型", raw)
		}
		client := apisource.NewClient(source, key, modelID, apisource.ClientOptions{Effort: engineReasoningEffort(source.BaseURL)})
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return streamOnce(requestCtx, client, modelID, effort, prompt)
	}

	// 第三层：回退到自动引擎顺序，先 claude、codex，最后内置 mcode。
	out, errClaude := textgen.GenerateWithOptions(ctx, textgen.Options{Provider: "claude", Effort: effort, BinaryPath: cfg.Providers.Claude.Bin}, prompt)
	if errClaude == nil {
		return out, nil
	}
	out, errCodex := textgen.GenerateWithOptions(ctx, textgen.Options{Provider: "codex", Effort: effort, BinaryPath: cfg.Providers.Codex.Bin}, prompt)
	if errCodex == nil {
		return out, nil
	}
	var errMcode error
	if mcodeEnabled {
		out, errMcode = mcodeGenerateText(ctx, cfg, secrets, "", effort, prompt)
		if errMcode == nil {
			return out, nil
		}
	}
	if errMcode != nil {
		return "", fmt.Errorf("%s 不支持一次性文本生成，回退 claude / codex / mcode 也失败：%v；%v；%v", raw, errClaude, errCodex, errMcode)
	}
	return "", fmt.Errorf("%s 不支持一次性文本生成，回退 claude / codex 也失败：%v；%v", raw, errClaude, errCodex)
}

// textgenSecrets 返回一次性文本生成使用的凭据存储，未注入时新建默认存储。
func (c *Client) textgenSecrets() config.SecretStore {
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	if secrets == nil {
		return config.NewSecretStore("mcode")
	}
	return secrets
}
