package redact

import "regexp"

// 凭证脱敏规则，覆盖常见厂商 token 前缀。
// 这是一层尽力而为的保护，无法识别的形态仍会原样通过。
var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// URL userinfo（https://user:pass@host）。
	{regexp.MustCompile(`(?i)(\bhttps?://)[^\s/@"'<>]+@`), "${1}"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`), "Bearer ***"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`), "sk-***"},
	{regexp.MustCompile(`\b(gh[pousr]_)[A-Za-z0-9]{20,}`), "${1}***"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "github_pat_***"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), "AIza***"},
	{regexp.MustCompile(`\b(xox[abp]-)[A-Za-z0-9-]{10,}`), "${1}***"},
	// 字段赋值（含 JSON 引号形式、query 参数）：只掩掉值。
	{regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(?:token|password|passwd|secret|api[_-]?key|apikey|credentials?|private[_-]?key)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;"'&]+)`), "${1}***"},
}

// Secrets 掩码文本中可识别的凭证（Bearer、各类 API key、token 赋值等），其余文本保持不变。
func Secrets(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
