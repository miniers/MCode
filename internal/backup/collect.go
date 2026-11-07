package backup

import (
	"sort"

	"mcode/internal/agentconf"
	"mcode/internal/config"
)

// DiscoverSecretRefs returns the configured credential references in stable order.
func DiscoverSecretRefs(dataDir string, cfg config.Config, projectPaths []string) ([]config.SecretRef, error) {
	seen := make(map[config.SecretRef]bool)
	for _, ref := range config.LegacySecretRefs(nil) {
		seen[ref] = true
	}
	add := func(account string) {
		seen[config.SecretRef{Service: "mcode", Account: account}] = true
	}
	for _, id := range []string{"openai", "droid", "deepseek", "zai", "meta", "magpie1", "magpie2", "magpie3", "magpie4"} {
		add(id + ".apiKey")
	}
	for id := range cfg.Providers.ACP {
		add(id + ".apiKey")
	}
	for id := range cfg.Providers.Presets {
		add(id + ".apiKey")
	}
	for _, endpoint := range cfg.Providers.Custom {
		add(config.CustomEndpointSecretAccount(endpoint.ID))
		add(config.CustomEndpointWebKeySecretAccount(endpoint.ID))
		for _, id := range endpoint.QuotaProviders {
			add(id + ".apiKey")
		}
	}
	refs, err := agentconf.BackupMCPSecretRefs(dataDir, projectPaths)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		seen[ref] = true
	}
	refs = make([]config.SecretRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Service != refs[j].Service {
			return refs[i].Service < refs[j].Service
		}
		return refs[i].Account < refs[j].Account
	})
	return refs, nil
}
