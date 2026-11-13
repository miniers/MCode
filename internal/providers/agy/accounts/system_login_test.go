package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/testenv"
)

// SystemTokenPath 指向宿主 home 下的 antigravity-oauth-token（文件存储模式）。
func TestSystemTokenPath(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if got := SystemTokenPath(home); got != want {
		t.Fatalf("SystemTokenPath = %q, want %q", got, want)
	}
	if got := SystemTokenPath(""); got != "" {
		t.Fatalf("空 home 应返回空串，got %q", got)
	}
}

// 凭证文件里 access_token 或 refresh_token 非空即视为已登录；缺失、损坏或字段为空时不算。
func TestTokenFileHasCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "antigravity-oauth-token")

	if tokenFileHasCredentials(path) {
		t.Fatal("文件缺失时不应算已登录")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tokenFileHasCredentials(path) {
		t.Fatal("内容不是 JSON 时不应算已登录")
	}

	writeToken := func(token OAuthTokenFile) {
		t.Helper()
		data, err := json.Marshal(token)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeToken(OAuthTokenFile{})
	if tokenFileHasCredentials(path) {
		t.Fatal("token 字段为空时不应算已登录")
	}

	writeToken(OAuthTokenFile{Token: OAuthTokenData{RefreshToken: "refresh"}})
	if !tokenFileHasCredentials(path) {
		t.Fatal("有 refresh_token 应算已登录")
	}

	writeToken(OAuthTokenFile{Token: OAuthTokenData{AccessToken: "access"}})
	if !tokenFileHasCredentials(path) {
		t.Fatal("有 access_token 应算已登录")
	}

	if tokenFileHasCredentials("") {
		t.Fatal("空路径不应算已登录")
	}
}

// 宿主 home 下存在凭证文件时 SystemLoginAvailable 为 true（不看账号管理器是否接线）。
// 反例（没有文件、也没有钥匙串条目）依赖运行测试的机器，这里不覆盖。
func TestSystemLoginAvailableReadsHostToken(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	path := SystemTokenPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(OAuthTokenFile{Token: OAuthTokenData{AccessToken: "access", RefreshToken: "refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if !SystemLoginAvailable(context.Background()) {
		t.Fatal("宿主凭证文件存在时应算已登录")
	}
}

// decodeSystemToken 解析系统凭据里的凭证：纯 JSON 与 go-keyring 的 base64 前缀都要能读。
func TestDecodeSystemToken(t *testing.T) {
	raw, err := json.Marshal(OAuthTokenFile{Token: OAuthTokenData{AccessToken: "access", RefreshToken: "refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	prefixed := []byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString(raw))
	for name, data := range map[string][]byte{"纯 JSON": raw, "go-keyring base64 前缀": prefixed} {
		tok, err := decodeSystemToken(data)
		if err != nil {
			t.Fatalf("%s: 解析出错: %v", name, err)
		}
		if tok.Token.AccessToken != "access" || tok.Token.RefreshToken != "refresh" {
			t.Fatalf("%s: 解析结果不对: %+v", name, tok)
		}
	}
	if _, err := decodeSystemToken([]byte("go-keyring-base64:not base64!")); err == nil {
		t.Fatal("前缀后的内容不是 base64 时应报错")
	}
	if _, err := decodeSystemToken([]byte("{not json")); err == nil {
		t.Fatal("内容不是 JSON 时应报错")
	}
}
