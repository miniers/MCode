package mcodeui

import (
	"mcode/internal/apisource"
	"mcode/internal/config"
)

type modelPickerCatalog struct {
	Providers         []ProviderInfo
	Pins              []ModelPin
	ProviderEnabled   map[string]bool
	MagpieFilter      modelPickerMagpieFilter
	QuotaUsed         func(string) (float64, bool)
	OnRefreshCatalog  func()
	CatalogRefreshing bool
}

func frontdeskModelPickerCatalog(cat modelPickerCatalog, cfg config.Config) modelPickerCatalog {
	allowed := make(map[string]bool)
	for _, kind := range apisource.Kinds(cfg) {
		allowed[kind] = true
	}
	providers := make([]ProviderInfo, 0, len(cat.Providers))
	for _, provider := range cat.Providers {
		if allowed[provider.ID] {
			providers = append(providers, provider)
		}
	}
	cat.Providers = providers
	// 前台只能直接使用 API 来源，Magpie 端点是主要来源，不受普通选择器的隐藏开关影响。
	cat.MagpieFilter = modelPickerMagpieFilter{}
	return cat
}

func (cat modelPickerCatalog) apply(p ModelPickerProps) ModelPickerProps {
	p.Providers = cat.Providers
	p.Pins = cat.Pins
	p.ProviderEnabled = cat.ProviderEnabled
	p.MagpieFilter = cat.MagpieFilter
	p.QuotaUsed = cat.QuotaUsed
	p.OnRefreshCatalog = cat.OnRefreshCatalog
	p.CatalogRefreshing = cat.CatalogRefreshing
	return p
}

func modelPickerCatalogFor(s *chatState) modelPickerCatalog {
	var cat modelPickerCatalog
	if s == nil {
		return cat
	}
	s.mu.Lock()
	cat.Providers = append([]ProviderInfo(nil), s.providers...)
	cat.CatalogRefreshing = s.catalogRefreshing
	s.mu.Unlock()
	if st := getChatSettings(s); st != nil {
		st.cfgMu.Lock()
		cat.Pins = append([]ModelPin(nil), pinnedModelPins(s)...)
		cat.ProviderEnabled = providerEnabledState(s)
		cat.MagpieFilter = newModelPickerMagpieFilter(st.cfg)
		st.cfgMu.Unlock()
	}
	cat.QuotaUsed = func(id string) (float64, bool) { return getProviderQuotaUsed(s, id) }
	cat.OnRefreshCatalog = s.refreshProviders
	return cat
}

func settingsModelPickerCatalog(st *SettingsState) modelPickerCatalog {
	var cat modelPickerCatalog
	if st != nil {
		st.mu.Lock()
		chat := st.chat
		st.mu.Unlock()
		if chat != nil {
			cat = modelPickerCatalogFor(chat)
		}
	}
	if len(cat.Providers) == 0 {
		cat.Providers = []ProviderInfo{
			{ID: config.ProviderClaude, Name: "Claude"},
			{ID: config.ProviderCodex, Name: "Codex"},
			{ID: config.ProviderAgy, Name: "agy"},
		}
	}
	return cat
}
