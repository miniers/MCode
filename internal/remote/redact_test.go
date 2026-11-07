package remote

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	secrets := []string{
		"abcdef1234567890XYZ",
		"sk-ant-api03-abcdefghijklmnop",
		"sk-proj-abcdefghijklmnop",
		"sk-abcdefghijklmnopqrstuvwx",
		"ghp_abcdefghijklmnopqrstuvwx1234",
		"gho_abcdefghijklmnopqrstuvwx1234",
		"github_pat_abcdefghijklmnopqrstuv_1234",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"xoxb-1234567890-abcdefghij",
		"hunter2hunter2",
	}
	inputs := []string{
		"Authorization: Bearer abcdef1234567890XYZ failed",
		"key sk-ant-api03-abcdefghijklmnop bad",
		"key sk-proj-abcdefghijklmnop bad",
		"key sk-abcdefghijklmnopqrstuvwx bad",
		"gh ghp_abcdefghijklmnopqrstuvwx1234 x",
		"gh gho_abcdefghijklmnopqrstuvwx1234 x",
		"gh github_pat_abcdefghijklmnopqrstuv_1234 x",
		"g AIzaSyA1234567890abcdefghijklmnopqrstuv x",
		"s xoxb-1234567890-abcdefghij x",
		`{"password": "hunter2hunter2"} and api_key=hunter2hunter2&x=1`,
	}
	for i, in := range inputs {
		out := RedactSecrets(in)
		if strings.Contains(out, secrets[i]) {
			t.Errorf("input %q leaked secret: %q", in, out)
		}
	}
	if got := RedactSecrets("Bearer abcdef1234567890XYZ"); got != "Bearer ***" {
		t.Errorf("bearer = %q", got)
	}
	if got := RedactSecrets("ghp_abcdefghijklmnopqrstuvwx1234"); got != "ghp_***" {
		t.Errorf("ghp = %q", got)
	}
	if got := RedactSecrets("token=abc&y=2"); got != "token=***&y=2" {
		t.Errorf("assign = %q", got)
	}
	if got := RedactSecrets("see https://user:pw@example.com/a"); strings.Contains(got, "pw@") {
		t.Errorf("userinfo leaked: %q", got)
	}
}

func TestRedactSecretsPlainTextUnchanged(t *testing.T) {
	for _, s := range []string{"thread not found", "解析失败: 无效的 threadID", "bearer of bad news", "the task is ready"} {
		if got := RedactSecrets(s); got != s {
			t.Errorf("plain text changed: %q -> %q", s, got)
		}
	}
}
