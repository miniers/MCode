package mcodeui

import (
	"slices"
	"strings"

	"mcode/internal/config"
)

// trayQuotaShown 判断直连供应商 id 是否在菜单栏额度面板中展示。
// 取值为 claude、agy、zai、cline、droid，不在 TrayQuotaHidden 中即为展示。
func trayQuotaShown(cfg config.Config, id string) bool {
	return !slices.Contains(cfg.Startup.TrayQuotaHidden, id)
}

// setTrayQuotaShown 更新直连供应商在菜单栏额度面板中的展示状态并落盘。
// 照搬 internal/ui/settings_page_general.go 约 271 行的写入方式，走 st.save()，
// 最终通过 config.Update 在 latest 上做增删，不整份覆盖。
func setTrayQuotaShown(st *SettingsState, id string, shown bool) {
	if st == nil {
		return
	}
	// 读改写同在一组锁内：读 st.cfg 也要和并发写入对齐，顺序固定 mu → cfgMu。
	// save 在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	current := trayQuotaShown(st.cfg, id)
	if current == shown {
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return
	}
	var hidden []string
	if shown {
		for _, h := range st.cfg.Startup.TrayQuotaHidden {
			if h != id {
				hidden = append(hidden, h)
			}
		}
	} else {
		hidden = append(hidden, st.cfg.Startup.TrayQuotaHidden...)
		if !slices.Contains(hidden, id) {
			hidden = append(hidden, id)
		}
	}
	st.cfg.Startup.TrayQuotaHidden = hidden
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
}

// trayQuotaProviderLabel 返回供应商展示名。
// 复用 settings_page_providers_agents.go 中的 providerDisplayName。
func trayQuotaProviderLabel(id string) string {
	lower := strings.ToLower(strings.TrimSpace(id))
	switch lower {
	case "zai", "z.ai":
		return "ZAI"
	default:
		name := providerDisplayName(id)
		if name != id {
			return name
		}
		if len(id) > 0 {
			return strings.ToUpper(id[:1]) + id[1:]
		}
		return id
	}
}

// resolveMagpieQuotaSelection 解析 Magpie 端点已保存的 QuotaProviders 切片为集合。
// saved 为 nil 时（旧配置未设置）默认只勾选 codex；
// saved 为空切片或已有列表时返回对应的勾选集合。
func resolveMagpieQuotaSelection(saved []string) map[string]bool {
	res := make(map[string]bool)
	if saved == nil {
		res["codex"] = true
		return res
	}
	for _, id := range saved {
		trimmed := strings.ToLower(strings.TrimSpace(id))
		if trimmed != "" {
			res[trimmed] = true
		}
	}
	return res
}
