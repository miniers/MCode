// 此文件包含 Client 的供应商账号与额度方法。
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"mcode/internal/providers/acp"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/cliupdate"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// ProviderVersion 获取指定提供方的当前安装版本与最新可用版本信息。
func (c *Client) ProviderVersion(ctx context.Context, provider string) (mcodeui.ProviderVersionInfo, error) {
	executable, err := c.providerExecutable(provider)
	if err != nil {
		return mcodeui.ProviderVersionInfo{}, err
	}
	info, err := cliupdate.Installed(ctx, provider, executable)
	return mcodeui.ProviderVersionInfo{
		Provider:        info.Provider,
		Installed:       info.Installed,
		Latest:          info.Latest,
		UpdateAvailable: info.UpdateAvailable,
		ExecutablePath:  info.ExecutablePath,
		Channel:         info.Channel,
		UpdateCommand:   info.UpdateCommand,
	}, err
}

// CheckProviderUpdate 检查指定提供方的版本更新。
func (c *Client) CheckProviderUpdate(ctx context.Context, provider string) (mcodeui.ProviderVersionInfo, error) {
	executable, err := c.providerExecutable(provider)
	if err != nil {
		return mcodeui.ProviderVersionInfo{}, err
	}
	info, err := cliupdate.Check(ctx, provider, executable)
	if errors.Is(err, cliupdate.ErrUnsupported) {
		// 没有更新来源的 provider（droid 等）不算失败，只回落到已安装版本；
		// 已安装版本仍带渠道与升级命令，供设置页动态生成更新入口。
		info, err = cliupdate.Installed(ctx, provider, executable)
	}
	return mcodeui.ProviderVersionInfo{
		Provider:        info.Provider,
		Installed:       info.Installed,
		Latest:          info.Latest,
		UpdateAvailable: info.UpdateAvailable,
		ExecutablePath:  info.ExecutablePath,
		Channel:         info.Channel,
		UpdateCommand:   info.UpdateCommand,
	}, err
}

// providerExecutable 解析 provider CLI 的可执行文件路径，顺序为：
//  1. 配置里的用户路径优先（claude/codex/agy/copilot/pi/omp/commandcode 各自的 providers 字段，
//     ACP Agent（droid/cline/cursor/opencode/grok/devin）用 providers.acp 表）；
//  2. 登录 shell 的 PATH（GUI 进程的 PATH 找不到这些 CLI）；
//  3. provider 的常见安装目录（providerFallbackDirs）。
func (c *Client) providerExecutable(provider string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(provider))
	cfg, hasConfig := c.loadConfig()

	configured := ""
	var names []string
	switch key {
	case "claude":
		if hasConfig {
			configured = cfg.Providers.Claude.Bin
		}
		names = []string{"claude"}
	case "codex":
		if hasConfig {
			configured = cfg.Providers.Codex.Bin
		}
		names = []string{"codex"}
	case "agy", "antigravity":
		if hasConfig {
			configured = cfg.Providers.Agy.Bin
		}
		names = []string{"agy"}
	case "droid":
		if hasConfig {
			configured = cfg.Providers.ACP["droid"].Bin
		}
		names = []string{"droid"}
	case "copilot":
		if hasConfig {
			configured = cfg.Providers.Copilot.Bin
		}
		names = []string{"copilot"}
	case "pi":
		if hasConfig {
			configured = cfg.Providers.Pi.Bin
		}
		names = []string{"pi"}
	case "omp":
		if hasConfig {
			configured = cfg.Providers.Omp.Bin
		}
		names = []string{"omp"}
	case "commandcode":
		if hasConfig {
			configured = cfg.Providers.CommandCode.Bin
		}
		// `cmd` 是文档里的命令名，command-code/commandcode 是包自带的别名。
		names = []string{"command-code", "commandcode", "cmd"}
	case "cline":
		if hasConfig {
			configured = cfg.Providers.ACP["cline"].Bin
		}
		names = []string{"cline"}
	case "cursor", "opencode", "grok", "devin":
		if hasConfig {
			configured = cfg.Providers.ACP[key].Bin
		}
		names = []string{acp.DefaultBinary(acp.Agent(key))}
	}
	if configured != "" {
		return configured, nil
	}
	if len(names) == 0 {
		return "", fmt.Errorf("provider %s 没有已知的可执行文件", provider)
	}
	if path, ok := providerBinaryFinder(names, providerFallbackDirs(key)); ok {
		return path, nil
	}
	return "", fmt.Errorf("在登录 shell 的 PATH 与常见安装目录中都找不到 %s 的可执行文件（%s）: %w",
		provider, strings.Join(names, "/"), exec.ErrNotFound)
}

// ProviderAccount 获取指定提供方的当前登录账户及套餐信息。
// 各来源：codex 读激活 home 的 auth.json ID Token，claude 读 .claude.json 的
// oauthAccount，agy 取默认账号的邮箱与标签，copilot 经 CLI 的 account.getCurrentAuth 读取。
func (c *Client) ProviderAccount(ctx context.Context, provider string) (mcodeui.ProviderAccountInfo, error) {
	key := strings.ToLower(strings.TrimSpace(provider))
	switch key {
	case "codex":
		home := codexAccountHome()
		info := mcodeui.ProviderAccountInfo{
			Provider: provider,
			Email:    codex.ReadAccountEmail(home),
			Plan:     codex.ReadAccountPlan(home),
		}
		if info.Email == "" && info.Plan == "" {
			return mcodeui.ProviderAccountInfo{}, fmt.Errorf("codex: %s 下没有已登录的账号", home)
		}
		return info, nil
	case "claude":
		email, plan, err := claude.ReadAccount("")
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: email, Plan: plan}, nil
	case "agy", "antigravity":
		if c.agy == nil {
			return mcodeui.ProviderAccountInfo{}, errors.New("agy accounts are not configured")
		}
		list := c.agy.List()
		if len(list) == 0 {
			return mcodeui.ProviderAccountInfo{}, errors.New("agy: 没有已登录的账号")
		}
		account := list[0]
		defaultID := c.agy.DefaultAccountID()
		for _, candidate := range list {
			if candidate.ID == defaultID {
				account = candidate
				break
			}
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: account.Email, Plan: account.Label}, nil
	case "copilot":
		bin, err := c.providerExecutable("copilot")
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		account, err := readCopilotAccount(ctx, bin)
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		plan := account.Plan
		if account.NoCopilotAccess {
			plan = strings.TrimSpace(plan + " · no Copilot access")
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: account.Login, Plan: plan}, nil
	default:
		return mcodeui.ProviderAccountInfo{}, fmt.Errorf("ProviderAccount %s: %w", provider, errProviderAccountUnsupported)
	}
}

// codexQuotaLimits 按 Codex 自己的有效配置（与实际会话一致的二进制、工作目录、
// CODEX_HOME）判定额度来源并读取。桌面端与远程端都走这里，来源判定保持一致。
func (c *Client) codexQuotaLimits(ctx context.Context, threadID string) (codex.RateLimits, error) {
	opts := codex.QuotaOptions{CodexHome: codexAccountHome()}
	if bin, err := c.providerExecutable("codex"); err == nil {
		opts.Binary = bin
	}
	if threadID != "" {
		if dir, ok := c.ThreadWorkDir(threadID); ok {
			opts.WorkDir = dir
		}
	}
	return codex.ReadQuotaLimits(ctx, opts)
}

// codexMagpieEndpoint 优先解析 Codex 注入路由；没有注入模型时再按 Codex 自己的
// 有效配置判断直连或 Magpie 来源。
func (c *Client) codexMagpieEndpoint(ctx context.Context, threadID string) (baseURL, apiKey string, ok bool, limits codex.RateLimits, err error) {
	threadID = strings.TrimSpace(threadID)
	model := strings.TrimSpace(mcodeui.QuotaModel(ctx))
	if model == "" && threadID != "" {
		if current, modelErr := readCodexThreadModel(ctx, c.backend, threadID); modelErr == nil {
			model = current
		}
	}
	if model != "" {
		for _, route := range codexMagpieRoutesForQuota() {
			providerID := strings.TrimSpace(route.ProviderID)
			prefix := providerID + "/"
			if providerID != "" && strings.HasPrefix(model, prefix) && strings.TrimSpace(strings.TrimPrefix(model, prefix)) != "" {
				return route.BaseURL, route.APIKey, true, codex.RateLimits{}, nil
			}
		}
	}

	limits, err = readCodexQuotaLimits(c, ctx, threadID)
	if err != nil {
		return "", "", false, codex.RateLimits{}, err
	}
	if limits.Source == codex.QuotaSourceMagpie && strings.TrimSpace(limits.GatewayBaseURL) != "" {
		return limits.GatewayBaseURL, limits.GatewayToken, true, limits, nil
	}
	return "", "", false, limits, nil
}

// RefreshProviderQuota 让 Magpie 网关重新向厂商读取指定提供方的额度卡片。
// 经 Magpie 端点的提供方需要这一步：网关平时只自动重读首选账号，手动刷新才能把非首选账号
// 也拉一遍；直连提供方的额度每次都由 ProviderQuota 直接读取，这里按空操作处理。
func (c *Client) RefreshProviderQuota(ctx context.Context, provider string) error {
	provider = strings.TrimSpace(provider)
	// claude 线程指向 Magpie 网关时，额度来自网关缓存的 claude 卡片，需要网关重读。
	if provider == "claude" {
		if _, err := claude.RefreshGatewayQuotas(ctx); err != nil {
			return err
		}
		return nil
	}
	// 内置引擎会话（mcode:<provider id>、magpie、magpie2…）同样经 Magpie 端点取额度。
	if engineQuotaKey(provider) {
		return c.refreshEngineQuota(ctx, provider)
	}
	providerID, ok := strings.CutPrefix(provider, "pi:")
	if !ok || strings.TrimSpace(providerID) == "" {
		return nil
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return nil
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	for _, target := range piMagpieTargets(cfg, secrets) {
		if target.providerID != strings.TrimSpace(providerID) || target.apiKey == "" {
			continue
		}
		// 弹层展示的是端点上的全部卡片（可能含多个账号），这里重读全部卡片。
		return magpie.RefreshQuotas(ctx, target.baseURL, target.apiKey, "")
	}
	return nil
}

// ProviderQuota 获取指定提供方的额度使用窗口列表。
func (c *Client) ProviderQuota(ctx context.Context, provider string) ([]mcodeui.ProviderQuotaWindow, error) {
	// pi 线程的额度来自 Magpie：provider 形如 pi:<pi provider id>，只有能映射到有 key 的
	// Magpie 端点时才读得到额度，其余（线程用的是 pi 自带的 provider）按未实现处理。
	if strings.HasPrefix(provider, "pi:") {
		return c.piMagpieQuota(ctx, strings.TrimPrefix(provider, "pi:"))
	}
	if engineQuotaKey(provider) {
		// 跑在 Magpie 上的内置引擎会话：mcode 的 <端点 provider id>/<模型 ID> 与
		// magpie、magpie2…magpie4 端点都与 pi 走同一份额度读取与「在用」标记。
		return c.engineQuotaWindows(ctx, provider)
	}
	if provider == "codex" {
		threadID := mcodeui.QuotaThread(ctx)
		baseURL, token, magpie, limits, err := c.codexMagpieEndpoint(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if magpie {
			sessionID := ""
			if threadID != "" && c.agySessionForThread != nil {
				sessionID = c.agySessionForThread(ctx, threadID)
			}
			model := strings.TrimSpace(mcodeui.QuotaModel(ctx))
			if model == "" && threadID != "" {
				if current, modelErr := readCodexThreadModel(ctx, c.backend, threadID); modelErr == nil {
					model = strings.TrimSpace(current)
				}
			}
			for _, route := range codexMagpieRoutesForQuota() {
				if route.ProviderID != "" {
					if gatewayModel, ok := strings.CutPrefix(model, strings.TrimSpace(route.ProviderID)+"/"); ok {
						model = gatewayModel
						break
					}
				}
			}
			if model == "" {
				model = c.magpieQuotaDefaultModel("codex", c.codexQuotaEndpointID(baseURL))
			}
			return c.readMagpieQuotaWindows(ctx, baseURL, token, "codex", sessionID, model)
		}
		windows := codexQuotaWindows(limits)
		return windows, nil
	}
	if provider == "claude" {
		limits, err := claude.ReadPlanLimits(ctx)
		if err != nil {
			return nil, err
		}
		route, hasRoute := sessionRoute{}, false
		// 经 Magpie 网关时，额度来自网关缓存的 claude 卡片，再按 trace 记录标出本会话
		// 实际应答的账号；直连不涉及路由，映射结果保持原样。
		if limits.Source == codex.QuotaSourceMagpie {
			if baseURL, token, ok := claude.GatewayEndpoint(ctx); ok {
				tracker := c.sessionTracker()
				tracker.noteSource(baseURL, token)
				if threadID := mcodeui.QuotaThread(ctx); threadID != "" && c.agySessionForThread != nil {
					sessionID := c.agySessionForThread(ctx, threadID)
					route, hasRoute = tracker.routeFor(ctx, baseURL, token, "claude", sessionID, false)
					if hasRoute && strings.TrimSpace(route.Provider) != "" && !strings.EqualFold(route.Provider, "claude") && !strings.EqualFold(route.Provider, "claude-tui") {
						return c.readMagpieQuotaWindows(ctx, baseURL, token, "claude", sessionID, "")
					}
				}
			}
		}
		return claudeQuotaWindows(limits, route, hasRoute), nil
	}
	if provider == "zai" || provider == "cline" || provider == "droid" {
		windows, err := readProviderPlanWindows(ctx, provider, c.creditsOptions(provider))
		if err != nil {
			if errors.Is(err, credits.ErrUnsupported) {
				return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
			}
			return nil, err
		}
		out := make([]mcodeui.ProviderQuotaWindow, 0, len(windows))
		for _, w := range windows {
			out = append(out, mcodeui.ProviderQuotaWindow{
				ID: w.ID, Period: w.Period, Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt,
			})
		}
		return out, nil
	}
	if provider != "agy" || c.agy == nil {
		return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
	}
	var windows []mcodeui.ProviderQuotaWindow
	// 只输出 Gemini 分组额度，Claude/GPT 分组与逐模型额度只供账户路由使用，不在界面展示
	groups := []string{
		quota.GroupGemini,
		quota.GroupGemini + " 7d",
	}
	view := c.agy.QuotaView()
	list := c.agy.List()

	// 先算每个账号的展示标题与有效启用状态（已合并路由覆盖）。
	titles := make(map[string]string, len(list))
	emails := make(map[string]string, len(list))
	enabled := make(map[string]bool, len(list))
	hasIsolatedEmail := false
	for _, account := range list {
		title := account.Label
		if title == "" {
			title = account.ID
		}
		// 默认以 UUID 作为标题的隔离账号改用邮箱，界面不出现一串随机 ID。
		if account.Email != "" && looksLikeUUID(title) {
			title = account.Email
		}
		titles[account.ID] = title
		emails[account.ID] = account.Email
		enabled[account.ID] = view.On[account.ID]
		if account.ID != accounts.SystemAccountID && account.Email != "" {
			hasIsolatedEmail = true
		}
	}

	// 系统账号登录的就是宿主 Google 账号：与同邮箱隔离账号指向同一个账号时只留一份。
	// 优先保留有效启用的那个，状态相同时保留隔离账号（它有私有 token，额度更准）；
	// 解析不到系统账号邮箱时不去重。
	systemEmail := emails[accounts.SystemAccountID]
	if systemEmail == "" && hasIsolatedEmail {
		systemEmail = c.agy.SystemAccountEmail(ctx)
	}
	merged := make(map[string]string, len(list)) // 被合并掉的账号 ID → 保留的账号 ID
	if systemEmail != "" {
		for _, account := range list {
			if account.ID == accounts.SystemAccountID || !strings.EqualFold(emails[account.ID], systemEmail) {
				continue
			}
			if enabled[account.ID] && !enabled[accounts.SystemAccountID] {
				merged[accounts.SystemAccountID] = account.ID
			} else if enabled[accounts.SystemAccountID] && !enabled[account.ID] {
				merged[account.ID] = accounts.SystemAccountID
			} else {
				// 状态相同：保留隔离账号（有私有 token，额度更准）。
				merged[accounts.SystemAccountID] = account.ID
			}
		}
	}

	// 「在用」优先落在当前会话实际绑定的账号上：view.Target 只说明新会话会路由到哪，
	// 会话一旦绑定过账号，接着聊仍然用那个号，两者可能不一致。
	// 绑定的账号后来被停用（路由覆盖关掉）时不再算在用，退回路由目标，否则界面没有在用账号。
	target := view.Target
	if bound, ok := c.agyBoundAccountForThread(ctx); ok {
		if _, exists := titles[bound]; exists && enabled[bound] {
			target = bound
		}
	}

	for _, account := range list {
		if _, ok := merged[account.ID]; ok {
			continue
		}
		label := titles[account.ID]
		// Active 只给当前在用账号：本会话绑定过账号时就是绑定的那个（新会话则是路由目标）；
		// 停用账号一律不算在用，由 Disabled 交给界面显示「已停止」。
		on := enabled[account.ID]
		active := on && target != "" && (account.ID == target || merged[target] == account.ID)
		for _, group := range groups {
			q, ok := account.Quota[group]
			if !ok {
				continue
			}
			resetAt, _ := time.Parse(time.RFC3339, q.ResetAt)
			windows = append(windows, mcodeui.ProviderQuotaWindow{
				ID: group, Period: agyQuotaGroupPeriod(group),
				Label: label + " · " + group, UsedPercent: q.UsedPercent, ResetsAt: resetAt,
				Account: label, Active: active, Disabled: !on,
			})
		}
	}
	return windows, nil
}

// agyBoundAccountForThread 返回当前会话已绑定的 agy 账号 ID。会话还没跑过一轮（没有会话 ID）、
// 装配层没注解析函数，或该会话尚未绑定账号时返回 false，调用方退回新会话的目标账号。
func (c *Client) agyBoundAccountForThread(ctx context.Context) (string, bool) {
	if c == nil || c.agy == nil || c.agySessionForThread == nil {
		return "", false
	}
	threadID := mcodeui.QuotaThread(ctx)
	if threadID == "" {
		return "", false
	}
	conversationID := c.agySessionForThread(ctx, threadID)
	if conversationID == "" {
		return "", false
	}
	return c.agy.BoundAccountID(conversationID)
}

// creditsOptions 构造 credits 包读取所需依赖；secrets 为 nil 时只用环境变量与主目录。
func (c *Client) creditsOptions(provider string) credits.Options {
	opts := credits.Options{}
	if c.secrets != nil {
		if key, err := c.secrets.Get(provider + ".apiKey"); err == nil {
			opts.APIKey = key
		}
	}
	env := make(map[string]string)
	for _, kv := range loginenv.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	opts.Env = env
	if home, err := os.UserHomeDir(); err == nil {
		opts.HomeDir = home
	}
	return opts
}

// ProviderCredits 获取按量计费提供方的预付费余额；提供方不支持时返回 ErrNotImplemented。
func (c *Client) ProviderCredits(ctx context.Context, provider string) (mcodeui.ProviderCredits, error) {
	if !credits.ExposesCredits(provider) {
		return mcodeui.ProviderCredits{}, fmt.Errorf("ProviderCredits: %w", mcodeui.ErrNotImplemented)
	}
	got, err := readProviderCredits(ctx, provider, c.creditsOptions(provider))
	if err != nil {
		return mcodeui.ProviderCredits{}, err
	}
	return mcodeui.ProviderCredits{
		Provider:      provider,
		Total:         got.Total,
		Currency:      got.Currency,
		AmountText:    got.AmountText(),
		BreakdownText: got.BreakdownText(),
		Usable:        got.Usable,
		ReadAt:        got.ReadAt,
	}, nil
}
