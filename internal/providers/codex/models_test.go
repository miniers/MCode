package codex

import (
	"context"
	"errors"
	"testing"

	"mcode/internal/providers/modelcache"
)

// 找不到 CLI 时错误必须能被 errors.Is 认成 ErrNotInstalled，界面据此给出“未安装”文案。
func TestFetchModelsNotInstalled(t *testing.T) {
	_, err := FetchModels(context.Background(), "/nonexistent/mcode-codex-test")
	if !errors.Is(err, modelcache.ErrNotInstalled) {
		t.Fatalf("error = %v, want ErrNotInstalled", err)
	}
}

func withCatalogSnapshot(t *testing.T, snap modelcache.Snapshot[Model]) {
	t.Helper()
	old := catalogSnapshot
	catalogSnapshot = func() modelcache.Snapshot[Model] { return snap }
	t.Cleanup(func() { catalogSnapshot = old })
}

func withLoginPresent(t *testing.T, present bool) {
	t.Helper()
	old := loginPresent
	loginPresent = func() bool { return present }
	t.Cleanup(func() { loginPresent = old })
}

func TestCatalogNotice(t *testing.T) {
	notInstalled := errors.Join(modelcache.ErrNotInstalled, errors.New("lookpath failed"))
	cases := []struct {
		name  string
		snap  modelcache.Snapshot[Model]
		login bool
		want  string
	}{
		{"目录可用", modelcache.Snapshot[Model]{Models: []Model{{ID: "gpt-5.5", DisplayName: "GPT-5.5"}}}, false, ""},
		{"探测中", modelcache.Snapshot[Model]{Probing: true}, true, "正在探测 Codex 模型…"},
		{"未安装", modelcache.Snapshot[Model]{Err: notInstalled}, false, "未安装 Codex CLI"},
		{"未登录", modelcache.Snapshot[Model]{Err: errors.New("app-server failed")}, false, "未登录 Codex"},
		{"其他失败", modelcache.Snapshot[Model]{Err: errors.New("app-server failed")}, true, "探测 Codex 模型失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCatalogSnapshot(t, tc.snap)
			withLoginPresent(t, tc.login)
			if got := CatalogNotice(); got != tc.want {
				t.Errorf("CatalogNotice() = %q, want %q", got, tc.want)
			}
		})
	}
}
