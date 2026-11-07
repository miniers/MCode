//go:build darwin

package keyring

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"mcode/internal/procattr"
)

// SecurityBin 是 macOS 下 security 命令行工具路径，包级变量方便测试替换。
var SecurityBin = "/usr/bin/security"

type darwinSecurityBackend struct{}

func (b *darwinSecurityBackend) Get(service, account string) (string, error) {
	if err := validateNames(service, account); err != nil {
		return "", err
	}
	cmd := procattr.HideWindow(exec.Command(SecurityBin, "find-generic-password", "-s", service, "-a", account, "-w"))
	out, err := cmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return "", ErrNotFound
		}
		if strings.Contains(outStr, "could not be found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("security find-generic-password: %w (%s)", err, strings.TrimSpace(outStr))
	}

	trimmed := strings.TrimSuffix(outStr, "\n")
	return DecodeValue(trimmed)
}

func (b *darwinSecurityBackend) Set(service, account, value string) error {
	if err := validateNames(service, account); err != nil {
		return err
	}
	encoded := EncodeValue(value)
	line := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", QuoteArg(service), QuoteArg(account), QuoteArg(encoded))

	cmd := procattr.HideWindow(exec.Command(SecurityBin, "-i"))
	cmd.Stdin = strings.NewReader(line)
	err := cmd.Run()
	if err != nil {
		// interactive security 的诊断可能回显输入，不能把凭据带入错误或日志。
		return fmt.Errorf("security add-generic-password: %w", err)
	}
	return nil
}

func (b *darwinSecurityBackend) Delete(service, account string) error {
	if err := validateNames(service, account); err != nil {
		return err
	}
	cmd := procattr.HideWindow(exec.Command(SecurityBin, "delete-generic-password", "-s", service, "-a", account))
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return nil
		}
		outStr := string(out)
		if strings.Contains(outStr, "could not be found") {
			return nil
		}
		return fmt.Errorf("security delete-generic-password: %w (%s)", err, strings.TrimSpace(outStr))
	}
	return nil
}

func validateNames(service, account string) error {
	if service == "" || account == "" || strings.ContainsAny(service+account, "\x00\r\n") {
		return errors.New("keyring: service and account must be nonempty and contain no NUL or line breaks")
	}
	return nil
}

func newPlatformDefaultBackend() Backend {
	return &darwinSecurityBackend{}
}
