package harness

// IsCLICatalogProvider 判断模型目录是否由 CLI 动态列出。
func IsCLICatalogProvider(provider string) bool {
	switch provider {
	case "commandcode", "cline", "pi", "omp", "copilot", "droid", "cursor", "opencode", "grok", "devin":
		return true
	}
	return false
}
