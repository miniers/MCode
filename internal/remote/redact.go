package remote

import "mcode/internal/redact"

// RedactSecrets 掩码文本中可识别的凭证（Bearer、各类 API key、token 赋值等），其余文本保持不变。
func RedactSecrets(s string) string {
	return redact.Secrets(s)
}
