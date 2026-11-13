package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/providers/copilot"
	"mcode/internal/testenv"
	mcodeui "mcode/internal/ui"
)

// writeCodexAuth 在 home 下写入 auth.json fixture：ID Token 带邮箱与 chatgpt_plan_type。
// 测试不读取真实登录，也不访问网络。
func writeCodexAuth(t *testing.T, home, email, planType string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}
	payload, err := json.Marshal(map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_plan_type": planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	blob, err := json.Marshal(map[string]any{"tokens": map[string]string{"id_token": token}})
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), blob, 0o600); err != nil {
		t.Fatalf("写 auth.json: %v", err)
	}
}

func TestProviderAccountCodex(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CODEX_HOME", "")
	writeCodexAuth(t, filepath.Join(home, ".codex"), "a@b.c", "pro")

	info, err := (&Client{}).ProviderAccount(context.Background(), "codex")
	if err != nil {
		t.Fatalf("ProviderAccount(codex): %v", err)
	}
	if info.Provider != "codex" || info.Email != "a@b.c" || info.Plan != "Pro 20x" {
		t.Fatalf("ProviderAccount(codex) = %+v，期望 a@b.c / Pro 20x", info)
	}

	// CODEX_HOME 优先于 ~/.codex（多账号切换时由它指向激活账号的 home）。
	other := t.TempDir()
	writeCodexAuth(t, other, "other@example.com", "plus")
	t.Setenv("CODEX_HOME", other)
	info, err = (&Client{}).ProviderAccount(context.Background(), "codex")
	if err != nil {
		t.Fatalf("ProviderAccount(codex, CODEX_HOME): %v", err)
	}
	if info.Email != "other@example.com" || info.Plan != "Plus" {
		t.Fatalf("CODEX_HOME 未生效: %+v", info)
	}
}

func TestProviderAccountCodexNotLoggedIn(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("CODEX_HOME", "")
	_, err := (&Client{}).ProviderAccount(context.Background(), "codex")
	if err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("没有 auth.json 时期望非 ErrNotImplemented 的错误，得到 %v", err)
	}
}

func TestProviderAccountClaude(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(
		`{"oauthAccount":{"emailAddress":"a@b.c","organizationType":"claude_max"}}`), 0o600); err != nil {
		t.Fatalf("写 .claude.json: %v", err)
	}

	info, err := (&Client{}).ProviderAccount(context.Background(), "claude")
	if err != nil {
		t.Fatalf("ProviderAccount(claude): %v", err)
	}
	if info.Provider != "claude" || info.Email != "a@b.c" || info.Plan != "Max" {
		t.Fatalf("ProviderAccount(claude) = %+v，期望 a@b.c / Max", info)
	}

	// 文件缺失（未登录）时期望普通错误。
	if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatalf("remove .claude.json: %v", err)
	}
	if _, err := (&Client{}).ProviderAccount(context.Background(), "claude"); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("缺少 .claude.json 时期望非 ErrNotImplemented 的错误，得到 %v", err)
	}
}

func TestProviderAccountCopilot(t *testing.T) {
	testenv.TempHome(t)
	stubProviderBinaryFinder(t)
	binDir := t.TempDir()
	writeFakeProvider(t, binDir, "copilot", "1.0.0")
	t.Setenv("PATH", binDir)

	orig := readCopilotAccount
	t.Cleanup(func() { readCopilotAccount = orig })

	readCopilotAccount = func(context.Context, string) (copilot.Account, error) {
		return copilot.Account{Login: "octocat", Plan: "Copilot Individual"}, nil
	}
	info, err := (&Client{}).ProviderAccount(context.Background(), "copilot")
	if err != nil {
		t.Fatalf("ProviderAccount(copilot): %v", err)
	}
	if info.Provider != "copilot" || info.Email != "octocat" || info.Plan != "Copilot Individual" {
		t.Fatalf("ProviderAccount(copilot) = %+v，期望 octocat / Copilot Individual", info)
	}

	// 已登录 GitHub 但没有 Copilot 权限时在套餐后追加标记。
	readCopilotAccount = func(context.Context, string) (copilot.Account, error) {
		return copilot.Account{Login: "octocat", Plan: "Copilot Business", NoCopilotAccess: true}, nil
	}
	info, err = (&Client{}).ProviderAccount(context.Background(), "copilot")
	if err != nil {
		t.Fatalf("ProviderAccount(copilot, no access): %v", err)
	}
	if info.Plan != "Copilot Business · no Copilot access" {
		t.Fatalf("Plan = %q，期望带 no Copilot access 标记", info.Plan)
	}

	// 未登录（CLI 报错）时错误透传，而不是 ErrNotImplemented。
	readCopilotAccount = func(context.Context, string) (copilot.Account, error) {
		return copilot.Account{}, errors.New("copilot: 尚未登录 GitHub Copilot")
	}
	if _, err := (&Client{}).ProviderAccount(context.Background(), "copilot"); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("未登录时期望非 ErrNotImplemented 的错误，得到 %v", err)
	}
}

// 不提供账户信息的提供方返回包级 errProviderAccountUnsupported，而不是 ErrNotImplemented。
func TestProviderAccountUnsupportedProvider(t *testing.T) {
	testenv.TempHome(t)
	for _, provider := range []string{"droid", "cline", "pi", "omp", "commandcode", "openai"} {
		_, err := (&Client{}).ProviderAccount(context.Background(), provider)
		if err == nil || !errors.Is(err, errProviderAccountUnsupported) {
			t.Errorf("ProviderAccount(%s) = %v，期望 errProviderAccountUnsupported", provider, err)
		}
		if errors.Is(err, mcodeui.ErrNotImplemented) {
			t.Errorf("ProviderAccount(%s) 不应返回 ErrNotImplemented", provider)
		}
	}
}
