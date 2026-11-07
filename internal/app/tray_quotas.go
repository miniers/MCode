package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"mcode/internal/providers/codex"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// TrayQuotaGroup 是菜单栏托盘面板展示的一个账号/提供方额度分组。
type TrayQuotaGroup struct {
	Provider string
	Title    string
	Windows  []mcodeui.ProviderQuotaWindow
	Err      string
}

// trayQuotaTimeout 是每个提供方额度读取的单独超时时长。
const trayQuotaTimeout = 15 * time.Second

// quotaSkipErrorNeedles 是判定 Provider 额度应整组跳过的错误关键字（小写匹配）。
// 覆盖未安装、未登录、缺少凭据、不支持等情况。
var quotaSkipErrorNeedles = []string{
	// 未实现 / 不支持
	"not implemented",
	"unsupported",
	"not supported",
	"does not support",
	"不支持",
	"未实现",

	// 未安装 / 可执行文件不存在
	"not installed",
	"未安装",
	"executable file not found",
	"no such file or directory",
	"command not found",
	"未找到可执行文件",

	// 未登录 / 需要授权
	"401",
	"unauthorized",
	"unauthenticated",
	"needs-auth",
	"needs auth",
	"authentication",
	"not logged in",
	"not login",
	"login required",
	"please log in",
	"未登录",
	"未授权",

	// 缺少凭据 / 凭据文件不存在
	"credential",
	"no credentials",
	"credentials not found",
	"missing key",
	"缺少凭据",
	"缺少凭证",
	"未找到凭据",
	"未找到登录凭据",
	"token expired",
	"token 过期",
	"invalid token",
	"invalid_grant",
}

// isQuotaSkipError 判断错误是否属于整组跳过的类型（未实现、不支持、未安装、未登录、缺少凭据等）。
func isQuotaSkipError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcodeui.ErrNotImplemented) ||
		errors.Is(err, credits.ErrUnsupported) ||
		errors.Is(err, credits.ErrMissingKey) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range quotaSkipErrorNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// filterAndMapMagpieQuotaWindows 过滤不在 allowedProviders 集合中的卡片、大小写不敏感匹配、按 Account+ID 去重，并映射为 ProviderQuotaWindow。
func filterAndMapMagpieQuotaWindows(windows []magpie.QuotaWindow, allowedProviders []string) []mcodeui.ProviderQuotaWindow {
	if len(windows) == 0 || len(allowedProviders) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(allowedProviders))
	for _, p := range allowedProviders {
		clean := strings.ToLower(strings.TrimSpace(p))
		if clean != "" {
			allowed[clean] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(windows))
	out := make([]mcodeui.ProviderQuotaWindow, 0, len(windows))
	for _, w := range windows {
		prov := strings.ToLower(strings.TrimSpace(magpie.AccountProvider(w.Account)))
		if !allowed[prov] {
			continue
		}
		key := w.Account + "\x00" + w.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, mcodeui.ProviderQuotaWindow{
			ID:           w.ID,
			Period:       w.Period,
			Label:        w.Label,
			UsedPercent:  w.UsedPercent,
			ResetsAt:     w.ResetsAt,
			Account:      w.Account,
			Source:       codex.QuotaSourceMagpie,
			Plan:         w.Plan,
			ReadAt:       w.ReadAt,
			Provider:     w.Provider,
			User:         w.User,
			Kind:         w.Kind,
			Alias:        w.Alias,
			Last:         w.Last,
			LastServedAt: w.LastServedAt,
			// Active 固定为 false：托盘是全局视图，没有会话上下文，
			// 哪张卡最近应答过由 Last 表达，不能当作「在用」。
			Active:    false,
			Display:   w.Display,
			Text:      w.Text,
			Unlimited: w.Unlimited,
			Balance:   w.Balance,
			Unit:      w.Unit,
		})
	}
	return out
}

// resolveEndpointQuotaProviders 解析端点配置的所选供应商列表：
// nil 表示旧配置未设置，按只选 codex 处理；
// 空切片表示一个都不展示；
// 否则返回去空白、转小写、去重后的供应商 ID 列表。
func resolveEndpointQuotaProviders(providers []string) []string {
	if providers == nil {
		return []string{"codex"}
	}
	if len(providers) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(providers))
	res := make([]string, 0, len(providers))
	for _, p := range providers {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !seen[p] {
			seen[p] = true
			res = append(res, p)
		}
	}
	return res
}

// isDirectProviderHidden 检查指定直连供应商 ID 是否在隐藏列表中。
func isDirectProviderHidden(providerID string, hidden []string) bool {
	id := strings.ToLower(strings.TrimSpace(providerID))
	if id == "" {
		return false
	}
	for _, h := range hidden {
		if strings.ToLower(strings.TrimSpace(h)) == id {
			return true
		}
	}
	return false
}

// filterHiddenDirectProviders 过滤掉在 hidden 列表中的直连候选供应商。
func filterHiddenDirectProviders(candidates []string, hidden []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	res := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !isDirectProviderHidden(c, hidden) {
			res = append(res, c)
		}
	}
	return res
}

// magpieProviderDisplayName 返回 Magpie 供应商展示名：
// claude→Claude、codex→Codex、cline→Cline、zai→Z.ai、droid→Droid、gemini→Gemini，其余首字母大写。
func magpieProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "cline":
		return "Cline"
	case "zai":
		return "Z.ai"
	case "droid":
		return "Droid"
	case "gemini":
		return "Gemini"
	default:
		p := strings.TrimSpace(provider)
		if p == "" {
			return ""
		}
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}
}

// trayQuotaCLICandidates 获取设置页同样会查询的 CLI 类 harness ID 列表。
// 仅包含 ProviderQuota 支持的（zai、cline、droid），且符合已启用或已安装的规则。
func (c *Client) trayQuotaCLICandidates(ctx context.Context) []mcodeui.ProviderSetting {
	settingsList, err := c.ProviderSettings(ctx)
	if err != nil {
		return nil
	}
	supported := map[string]bool{
		"zai":   true,
		"cline": true,
		"droid": true,
	}
	var candidates []mcodeui.ProviderSetting
	for _, s := range settingsList {
		if !supported[s.ID] {
			continue
		}
		// 检查是否已启用或已安装
		isInstalled := false
		if strings.TrimSpace(s.Bin) != "" {
			isInstalled = true
		} else if _, err := c.providerExecutable(s.ID); err == nil {
			isInstalled = true
		}
		hasKey := false
		if s.ID == "zai" {
			if c.creditsOptions(s.ID).APIKey != "" {
				hasKey = true
			}
		}
		if s.Enabled || isInstalled || hasKey {
			candidates = append(candidates, s)
		}
	}
	return candidates
}

// TrayQuotaGroups 汇总所有账号的额度，供菜单栏托盘面板展示。
// 各 Provider 并发读取（每项单独设 15 秒超时），并按固定顺序输出：
// 直连组（claude、agy、zai/cline/droid），后接按供应商排序的通用 Magpie 组。
func (c *Client) TrayQuotaGroups(ctx context.Context) []TrayQuotaGroup {
	cfg, ok := c.loadConfig()
	if !ok {
		return nil
	}

	type directTask struct {
		providerID string
		read       func(taskCtx context.Context) (TrayQuotaGroup, bool)
	}

	var directTasks []directTask

	// 1. 直连组：claude、agy、zai/cline/droid。id 在 cfg.Startup.TrayQuotaHidden 里的整组不读不输出，连请求都不发。
	if !isDirectProviderHidden("claude", cfg.Startup.TrayQuotaHidden) {
		directTasks = append(directTasks, directTask{
			providerID: "claude",
			read: func(taskCtx context.Context) (TrayQuotaGroup, bool) {
				windows, err := c.ProviderQuota(taskCtx, "claude")
				if err != nil {
					if isQuotaSkipError(err) {
						return TrayQuotaGroup{}, false
					}
					return TrayQuotaGroup{Provider: "claude", Title: "Claude", Err: err.Error()}, true
				}
				if len(windows) == 0 {
					return TrayQuotaGroup{}, false
				}
				return TrayQuotaGroup{Provider: "claude", Title: "Claude", Windows: windows}, true
			},
		})
	}

	if !isDirectProviderHidden("agy", cfg.Startup.TrayQuotaHidden) {
		directTasks = append(directTasks, directTask{
			providerID: "agy",
			read: func(taskCtx context.Context) (TrayQuotaGroup, bool) {
				windows, err := c.ProviderQuota(taskCtx, "agy")
				if err != nil {
					if isQuotaSkipError(err) {
						return TrayQuotaGroup{}, false
					}
					return TrayQuotaGroup{Provider: "agy", Title: "Antigravity", Err: err.Error()}, true
				}
				if len(windows) == 0 {
					return TrayQuotaGroup{}, false
				}
				return TrayQuotaGroup{Provider: "agy", Title: "Antigravity", Windows: windows}, true
			},
		})
	}

	cliCandidates := c.trayQuotaCLICandidates(ctx)
	for _, candidate := range cliCandidates {
		cand := candidate
		if isDirectProviderHidden(cand.ID, cfg.Startup.TrayQuotaHidden) {
			continue
		}
		directTasks = append(directTasks, directTask{
			providerID: cand.ID,
			read: func(taskCtx context.Context) (TrayQuotaGroup, bool) {
				windows, err := c.ProviderQuota(taskCtx, cand.ID)
				if err != nil {
					if isQuotaSkipError(err) {
						return TrayQuotaGroup{}, false
					}
					return TrayQuotaGroup{Provider: cand.ID, Title: cand.Name, Err: err.Error()}, true
				}
				if len(windows) == 0 {
					return TrayQuotaGroup{}, false
				}
				return TrayQuotaGroup{Provider: cand.ID, Title: cand.Name, Windows: windows}, true
			},
		})
	}

	// 2. Magpie 端点准备：遍历每个有 key 的 Magpie 端点，空切片跳过不发请求
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	type magpieTargetJob struct {
		target   piMagpieTarget
		selected []string
	}
	var magpieJobs []magpieTargetJob
	for _, candidate := range piMagpieTargets(cfg, secrets) {
		if candidate.apiKey == "" {
			continue
		}
		selected := resolveEndpointQuotaProviders(candidate.endpoint.QuotaProviders)
		if len(selected) == 0 {
			continue
		}
		magpieJobs = append(magpieJobs, magpieTargetJob{
			target:   candidate,
			selected: selected,
		})
	}

	// 并发执行直连组读取与 Magpie 端点读取
	directResults := make([]struct {
		group TrayQuotaGroup
		ok    bool
	}, len(directTasks))

	type magpieFetchResult struct {
		target   piMagpieTarget
		selected []string
		windows  []magpie.QuotaWindow
		err      error
	}
	magpieResults := make([]magpieFetchResult, len(magpieJobs))

	var wg sync.WaitGroup
	wg.Add(len(directTasks) + len(magpieJobs))

	for i, t := range directTasks {
		i, t := i, t
		go func() {
			defer wg.Done()
			taskCtx, cancel := context.WithTimeout(ctx, trayQuotaTimeout)
			defer cancel()
			g, ok := t.read(taskCtx)
			directResults[i] = struct {
				group TrayQuotaGroup
				ok    bool
			}{group: g, ok: ok}
		}()
	}

	for i, job := range magpieJobs {
		i, job := i, job
		go func() {
			defer wg.Done()
			quotaCtx, cancel := context.WithTimeout(ctx, piMagpieFetchTimeout)
			defer cancel()
			windows, err := magpie.ReadQuotas(quotaCtx, job.target.baseURL, job.target.apiKey)
			magpieResults[i] = magpieFetchResult{
				target:   job.target,
				selected: job.selected,
				windows:  windows,
				err:      err,
			}
		}()
	}

	wg.Wait()

	// 收集输出的直连组
	var out []TrayQuotaGroup
	directOutputted := make(map[string]bool)
	for _, r := range directResults {
		if r.ok {
			out = append(out, r.group)
			directOutputted[strings.ToLower(r.group.Provider)] = true
		}
	}

	// 3. 汇总 Magpie 组：跨端点按供应商分组合并，组内按 Account+ID 去重
	allRequestedProviders := make(map[string]bool)
	var orderedRequestedProviders []string
	for _, job := range magpieJobs {
		for _, p := range job.selected {
			if !allRequestedProviders[p] {
				allRequestedProviders[p] = true
				orderedRequestedProviders = append(orderedRequestedProviders, p)
			}
		}
	}

	var magpieGroups []TrayQuotaGroup
	for _, p := range orderedRequestedProviders {
		var relevantResults []magpieFetchResult
		hasSuccess := false
		for _, res := range magpieResults {
			hasP := false
			for _, sp := range res.selected {
				if sp == p {
					hasP = true
					break
				}
			}
			if !hasP {
				continue
			}
			relevantResults = append(relevantResults, res)
			if res.err == nil {
				hasSuccess = true
			}
		}

		if hasSuccess {
			// 某个端点失败、其他端点成功时，只输出成功的数据
			var successWindows []magpie.QuotaWindow
			for _, res := range relevantResults {
				if res.err == nil {
					successWindows = append(successWindows, res.windows...)
				}
			}
			mapped := filterAndMapMagpieQuotaWindows(successWindows, []string{p})
			if len(mapped) > 0 {
				title := magpieProviderDisplayName(p)
				if directOutputted[p] {
					title += " · Magpie"
				}
				magpieGroups = append(magpieGroups, TrayQuotaGroup{
					Provider: "magpie:" + p,
					Title:    title,
					Windows:  mapped,
				})
			}
		} else {
			// 某个供应商在所有端点都读不到时，输出一个带首个错误文本的组；isQuotaSkipError 判为跳过的错误照旧跳过
			var firstErr error
			for _, res := range relevantResults {
				if res.err != nil && !isQuotaSkipError(res.err) {
					firstErr = res.err
					break
				}
			}
			if firstErr != nil {
				title := magpieProviderDisplayName(p)
				if directOutputted[p] {
					title += " · Magpie"
				}
				magpieGroups = append(magpieGroups, TrayQuotaGroup{
					Provider: "magpie:" + p,
					Title:    title,
					Err:      firstErr.Error(),
				})
			}
		}
	}

	// 排序 Magpie 组：按供应商 id 排序，codex 放第一个
	sort.Slice(magpieGroups, func(i, j int) bool {
		idI := strings.TrimPrefix(magpieGroups[i].Provider, "magpie:")
		idJ := strings.TrimPrefix(magpieGroups[j].Provider, "magpie:")
		if idI == "codex" {
			return true
		}
		if idJ == "codex" {
			return false
		}
		return idI < idJ
	})

	out = append(out, magpieGroups...)
	return out
}
