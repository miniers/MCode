package copilot

// 本文件读账户登录：调用 Copilot CLI 的
// account.getCurrentAuth，取出 CLI 背后的 GitHub 登录名（来自 `copilot login` 或 `gh auth`）
// 与 Copilot 订阅类型（authInfo.copilotUser.copilot_plan）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// AccountMethod 是 Copilot CLI 上报当前登录所用的 RPC 方法名。
const AccountMethod = "account.getCurrentAuth"

// Account 是一次 Copilot 账户读取的结果。
type Account struct {
	// Login 是 CLI 背后的 GitHub 登录名；响应未给出时为空串。
	Login string
	// Plan 是订阅类型文案，形如 "Copilot Individual"；响应未给出时为空串。
	Plan string
	// NoCopilotAccess 表示已登录 GitHub 但没有 Copilot 权限
	// （authInfo.copilotUser.access_type_sku == "no_access"）。
	NoCopilotAccess bool
}

// ReadAccount 以空参数调用 account.getCurrentAuth，
// 再把响应交给 ParseAccount 解析。
func ReadAccount(ctx context.Context, call Caller) (Account, error) {
	raw, err := call(ctx, AccountMethod, map[string]any{})
	if err != nil {
		return Account{}, err
	}
	return ParseAccount(raw)
}

// ParseAccount 按 authStatus 的逻辑解析 account.getCurrentAuth 响应：authInfo 缺失或为 null
// 表示未登录，返回错误；登录名为空不算错误，交由调用方按未登录展示。
func ParseAccount(raw json.RawMessage) (Account, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Account{}, fmt.Errorf("copilot: 解析 account.getCurrentAuth 响应: %w", err)
	}
	auth := planObject(root["authInfo"])
	if auth == nil {
		return Account{}, errors.New("copilot: 尚未登录 GitHub Copilot")
	}
	user := planObject(auth["copilotUser"])
	return Account{
		Login:           planText(auth["login"]),
		Plan:            planLabel(planText(user["copilot_plan"])),
		NoCopilotAccess: planText(user["access_type_sku"]) == "no_access",
	}, nil
}

// ReadAccountFromCLI 启动一次临时连接并读取当前登录账户，连接方式与 AuthStatus 一致；
// 连接失败或 CLI 未登录都返回错误。调用方负责取消 ctx。
func ReadAccountFromCLI(ctx context.Context, binary string) (Account, error) {
	conn, cmd, err := connect(ctx, binary, nil)
	if err != nil {
		return Account{}, err
	}
	defer closeConnection(conn, cmd)
	call := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		var raw json.RawMessage
		if err := conn.Call(ctx, method, params, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return ReadAccount(ctx, call)
}
