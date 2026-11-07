package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadAccounts(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	if len(store.List()) != 0 || store.ActiveID != "" {
		t.Fatalf("空 store 期望无账号，得到 %+v", store)
	}
	if _, ok := store.Active(); ok {
		t.Fatal("空 store 不应有激活账号")
	}

	a1 := Account{ID: "a1", Label: "一号", Home: filepath.Join(dir, "h1"), Email: "a@b.c"}
	a2 := Account{ID: "a2", Label: "二号", Home: filepath.Join(dir, "h2")}
	if err := store.Add(a1); err != nil {
		t.Fatalf("Add(a1): %v", err)
	}
	if err := store.Add(a2); err != nil {
		t.Fatalf("Add(a2): %v", err)
	}
	if err := store.Add(Account{ID: ""}); err == nil {
		t.Fatal("空 ID 应当报错")
	}
	if err := store.Add(Account{ID: "a1"}); err == nil {
		t.Fatal("重复 ID 应当报错")
	}
	if err := store.Add(Account{ID: "a3", Home: a1.Home}); err == nil {
		t.Fatal("重复 Home 应当报错")
	}

	// List 返回副本：外部修改不影响 store。
	list := store.List()
	list[0].Label = "改过"
	if store.List()[0].Label == "改过" {
		t.Fatal("List 应返回副本")
	}

	if err := store.SetActive("a2"); err != nil {
		t.Fatalf("SetActive(a2): %v", err)
	}
	got, ok := store.Active()
	if !ok || got.ID != "a2" {
		t.Fatalf("Active 期望 a2，得到 %+v ok=%v", got, ok)
	}
	if err := store.SetActive("nope"); err == nil {
		t.Fatal("激活不存在的账号应当报错")
	}

	if err := store.Remove("a2"); err != nil {
		t.Fatalf("Remove(a2): %v", err)
	}
	if store.ActiveID != "" {
		t.Fatalf("删除激活账号后应清空 ActiveID，得到 %q", store.ActiveID)
	}
	if _, ok := store.Active(); ok {
		t.Fatal("删除激活账号后不应再返回账号")
	}
	if err := store.Remove("nope"); err == nil {
		t.Fatal("删除不存在的账号应当报错")
	}

	// 保存后重新加载应当往返一致。
	path := filepath.Join(dir, "nested", "accounts.json")
	store.path = path
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件应当已重命名，stat err=%v", err)
	}
	loaded, err := LoadAccounts(path)
	if err != nil {
		t.Fatalf("重新 LoadAccounts: %v", err)
	}
	got4 := loaded.List()
	if len(got4) != 1 || got4[0].ID != "a1" || got4[0].Email != "a@b.c" {
		t.Fatalf("往返结果不符: %+v", got4)
	}
}

func TestLoadAccountsMissingFile(t *testing.T) {
	store, err := LoadAccounts(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("文件缺失不应报错: %v", err)
	}
	if store == nil || len(store.List()) != 0 {
		t.Fatalf("期望空 store，得到 %+v", store)
	}
}

func TestReadAccountEmail(t *testing.T) {
	home := t.TempDir()
	if got := ReadAccountEmail(home); got != "" {
		t.Fatalf("auth.json 缺失应返回空串，得到 %q", got)
	}
	if got := ReadAccountEmail(""); got != "" {
		t.Fatalf("空 home 应返回空串，得到 %q", got)
	}

	if got := emailFromIDToken("not-a-jwt"); got != "" {
		t.Fatalf("非法 token 应返回空串，得到 %q", got)
	}

	// 手工拼出的未签名 JWT：header.payload.sig。
	payload, err := json.Marshal(map[string]string{"email": "a@b.c"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	auth := map[string]any{"tokens": map[string]string{"id_token": token}}
	blob, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), blob, 0o600); err != nil {
		t.Fatalf("写 auth.json: %v", err)
	}
	if got := ReadAccountEmail(home); got != "a@b.c" {
		t.Fatalf("期望 a@b.c，得到 %q", got)
	}

	// 带 padding 的 payload 走 URLEncoding 回退分支。
	padded := base64.URLEncoding.EncodeToString(payload)
	if got := emailFromIDToken("h." + padded + ".s"); got != "a@b.c" {
		t.Fatalf("padding 回退分支期望 a@b.c，得到 %q", got)
	}
}

func TestAccountEnv(t *testing.T) {
	if env := AccountEnv(Account{ID: "a1"}); env != nil {
		t.Fatalf("Home 为空应返回 nil，得到 %v", env)
	}
	env := AccountEnv(Account{ID: "a1", Home: "/tmp/codex-a1"})
	if len(env) != 1 || env[0] != "CODEX_HOME=/tmp/codex-a1" {
		t.Fatalf("环境变量不符: %v", env)
	}
}

func TestReadAccountPlan(t *testing.T) {
	home := t.TempDir()
	if got := ReadAccountPlan(home); got != "" {
		t.Fatalf("auth.json 缺失应返回空串，得到 %q", got)
	}
	if got := ReadAccountPlan(""); got != "" {
		t.Fatalf("空 home 应返回空串，得到 %q", got)
	}

	// 手工拼出的未签名 JWT：claims 同时带邮箱与 https://api.openai.com/auth.chatgpt_plan_type。
	payload, err := json.Marshal(map[string]any{
		"email": "a@b.c",
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_plan_type": "self_serve_business_prolite",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	auth := map[string]any{"tokens": map[string]string{"id_token": token}}
	blob, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), blob, 0o600); err != nil {
		t.Fatalf("写 auth.json: %v", err)
	}
	if got := ReadAccountPlan(home); got != "Business Premium" {
		t.Fatalf("期望 Business Premium，得到 %q", got)
	}
	if got := ReadAccountEmail(home); got != "a@b.c" {
		t.Fatalf("邮箱期望 a@b.c，得到 %q", got)
	}

	// 未知计划标识按 FormatCodexPlanName 的兜底规则首字母大写。
	other, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "plus"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	otherToken := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(other) + ".sig"
	otherAuth, err := json.Marshal(map[string]any{"tokens": map[string]string{"id_token": otherToken}})
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), otherAuth, 0o600); err != nil {
		t.Fatalf("写 auth.json: %v", err)
	}
	if got := ReadAccountPlan(home); got != "Plus" {
		t.Fatalf("期望 Plus，得到 %q", got)
	}
}
