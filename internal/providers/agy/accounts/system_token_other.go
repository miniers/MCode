//go:build !darwin && !windows

package accounts

import (
	"context"
	"errors"
)

// readSystemCredentialBytes 在这些平台没有可读的系统凭据存储。
func readSystemCredentialBytes(context.Context) ([]byte, error) {
	return nil, errors.New("system keychain unavailable on this platform")
}

// systemCredentialHasEntry 在这些平台没有可查的系统凭据存储。
func systemCredentialHasEntry(context.Context) bool {
	return false
}
