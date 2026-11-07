package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// accountFixture 读取 testdata 下的录制样本，测试不访问网络也不启动 CLI。
func accountFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture %s 失败: %v", name, err)
	}
	return data
}

func TestParseAccount_SignedIn(t *testing.T) {
	acc, err := ParseAccount(accountFixture(t, "account_signed_in.json"))
	if err != nil {
		t.Fatalf("ParseAccount 失败: %v", err)
	}
	if acc.Login != "octocat" || acc.Plan != "Copilot Individual" || acc.NoCopilotAccess {
		t.Errorf("解析结果不符: %+v", acc)
	}
}

// 已登录 GitHub 但没有 Copilot 权限时置 NoCopilotAccess（对应 access_type_sku == "no_access"）。
func TestParseAccount_NoCopilotAccess(t *testing.T) {
	acc, err := ParseAccount(accountFixture(t, "account_no_access.json"))
	if err != nil {
		t.Fatalf("ParseAccount 失败: %v", err)
	}
	if acc.Login != "octocat" || acc.Plan != "Copilot Business" || !acc.NoCopilotAccess {
		t.Errorf("解析结果不符: %+v", acc)
	}
}

// authInfo 缺失或为 null 表示未登录，应返回错误。
func TestParseAccount_SignedOut(t *testing.T) {
	if _, err := ParseAccount(accountFixture(t, "account_signed_out.json")); err == nil {
		t.Fatal("authInfo 为 null 应视为未登录并报错")
	}
	if _, err := ParseAccount(json.RawMessage(`{}`)); err == nil {
		t.Fatal("缺少 authInfo 应视为未登录并报错")
	}
}

func TestParseAccount_Malformed(t *testing.T) {
	if _, err := ParseAccount(accountFixture(t, "account_malformed.json")); err == nil {
		t.Fatal("格式错误的响应应返回错误")
	}
}

func TestReadAccount_CallsAuthMethod(t *testing.T) {
	fixture := accountFixture(t, "account_signed_in.json")
	var gotMethod string
	var gotParams any
	call := func(_ context.Context, method string, params any) (json.RawMessage, error) {
		gotMethod = method
		gotParams = params
		return fixture, nil
	}
	acc, err := ReadAccount(context.Background(), call)
	if err != nil {
		t.Fatalf("ReadAccount 失败: %v", err)
	}
	if gotMethod != "account.getCurrentAuth" {
		t.Errorf("RPC 方法 = %q，期望 %q", gotMethod, "account.getCurrentAuth")
	}
	if params, ok := gotParams.(map[string]any); !ok || len(params) != 0 {
		t.Errorf("RPC 参数 = %#v，期望空表", gotParams)
	}
	if acc.Login != "octocat" || acc.Plan != "Copilot Individual" {
		t.Errorf("解析结果不符: %+v", acc)
	}
}

func TestReadAccount_PropagatesError(t *testing.T) {
	sentinel := errors.New("rpc 失败")
	call := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, sentinel
	}
	if _, err := ReadAccount(context.Background(), call); !errors.Is(err, sentinel) {
		t.Fatalf("错误未透传: %v", err)
	}
}
