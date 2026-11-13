package accounts

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"strings"
	"testing"
)

// TestRefreshQuotaAllQuietWithoutSystemLogin 验证宿主没登录 agy 时，批量刷新仍返回错误，
// 但不为系统账号写「read token file」日志（日志里每 5 分钟一条的真实报告）。
func TestRefreshQuotaAllQuietWithoutSystemLogin(t *testing.T) {
	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	err = mgr.RefreshQuota(context.Background(), "")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RefreshQuota err = %v，期望包含 fs.ErrNotExist", err)
	}
	if strings.Contains(buf.String(), "refresh quota for "+SystemAccountID) {
		t.Fatalf("不应为未登录的系统账号写日志，实际日志：%s", buf.String())
	}
}
