//go:build darwin

package accounts

import (
	"context"
	"errors"
	"os/exec"

	"mcode/internal/procattr"
)

// readSystemCredentialBytes 用 security 命令读出钥匙串条目的密码字节：
// service "gemini"、account "antigravity"，-w 只打印密码本身。
// agy 用 go-keyring 写入，密码就是凭证 JSON（可能带 go-keyring-base64: 前缀），由 decodeSystemToken 解析。
func readSystemCredentialBytes(ctx context.Context) ([]byte, error) {
	data, err := procattr.HideWindow(exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w")).Output()
	if err != nil {
		return nil, errors.New("system agy credentials unavailable")
	}
	return data, nil
}

// systemCredentialHasEntry 只查询钥匙串条目是否存在，不读取其中的凭证内容。
func systemCredentialHasEntry(ctx context.Context) bool {
	cmd := procattr.HideWindow(exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity"))
	return cmd.Run() == nil
}
