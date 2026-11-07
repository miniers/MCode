package agentconf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mcode/internal/config"
)

// BackupMCPSecretRefs discovers credential references without reading secrets.
func BackupMCPSecretRefs(dataDir string, projectPaths []string) ([]config.SecretRef, error) {
	paths := []string{filepath.Join(dataDir, "mcp.json")}
	for _, project := range projectPaths {
		info, err := os.Stat(project)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat project %s: %w", project, err)
		}
		if !info.IsDir() {
			continue
		}
		paths = append(paths, filepath.Join(project, ".mcode", "mcp.json"))
	}
	seen := make(map[config.SecretRef]bool)
	for _, path := range paths {
		cfg, err := loadCustomMCPConfig(path)
		if err != nil {
			return nil, fmt.Errorf("load MCP configuration %s: %w", path, err)
		}
		for name, server := range cfg.MCPServers {
			for _, key := range server.SecretEnvKeys {
				seen[config.SecretRef{Service: "mcode.mcp", Account: "mcp." + name + "." + key}] = true
			}
			for _, key := range server.SecretHeaderKeys {
				seen[config.SecretRef{Service: "mcode.mcp", Account: "mcp." + name + ".header." + key}] = true
			}
			id := (MCPServer{Name: name, URL: server.URL}).AuthID()
			for _, suffix := range []string{"token", "client", "cimd"} {
				seen[config.SecretRef{Service: "mcode.mcp-oauth", Account: "mcp-oauth:" + id + ":" + suffix}] = true
			}
		}
	}
	refs := make([]config.SecretRef, 0, len(seen))
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
