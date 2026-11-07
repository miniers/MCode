package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type fakeInstallableUpdate struct{}

func (fakeInstallableUpdate) Install(_ context.Context, progress func(int64, int64)) error {
	progress(1, 4)
	progress(1, 0)
	progress(5, 4)
	return nil
}

func TestNativeUpdateCheckAndInstall(t *testing.T) {
	oldEnabled, oldCheck, oldRelaunch := nativeUpdatesEnabled, nativeUpdateCheck, relaunchAfterUpdate
	t.Cleanup(func() { nativeUpdatesEnabled, nativeUpdateCheck, relaunchAfterUpdate = oldEnabled, oldCheck, oldRelaunch })
	nativeUpdatesEnabled = func() bool { return true }
	nativeUpdateCheck = func(context.Context) (string, installableUpdate, error) {
		return "2.0.0", fakeInstallableUpdate{}, nil
	}
	relaunched := false
	relaunchAfterUpdate = func() { relaunched = true }
	u := &UpdateChecker{}
	if err := u.Install(context.Background(), nil); err == nil {
		t.Fatal("没有待安装更新时应报错")
	}
	info, err := u.Check(context.Background())
	if err != nil || !info.Available || !info.Installable || info.Latest != "2.0.0" || info.URL != "https://github.com/miniers/MCode/releases/tag/v2.0.0" || u.Latest() != info {
		t.Fatalf("原生更新结果错误: %+v, %v", info, err)
	}
	var values []float64
	if err := u.Install(context.Background(), func(v float64) { values = append(values, v) }); err != nil {
		t.Fatal(err)
	}
	if !relaunched || len(values) != 3 || values[0] != 0.25 || values[1] != 0 || values[2] != 1 {
		t.Fatalf("安装进度或重启错误: %v, %v", values, relaunched)
	}
	nativeUpdateCheck = func(context.Context) (string, installableUpdate, error) { return "", nil, nil }
	if info, err = u.Check(context.Background()); err != nil || info.Available || info.Installable {
		t.Fatalf("最新版本检查错误: %+v, %v", info, err)
	}
	if err := u.Install(context.Background(), nil); err == nil {
		t.Fatal("没有新版本时不应保留待安装更新")
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0", "1.0.0-nightly", 1},
		{"1.0.0-beta", "1.0.0-nightly", -1},
		{"1.1", "1.0.9", 1},
	}
	for _, c := range cases {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdateCheckerEnabled(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://example.com/r","prerelease":false}`))
	}))
	defer srv.Close()

	enabled := false
	var checked atomic.Int32
	u := &UpdateChecker{
		Client:         srv.Client(),
		FeedURL:        srv.URL,
		CurrentVersion: "1.1.0",
		Enabled:        func() bool { return enabled },
		OnChecked:      func() { checked.Add(1) },
	}

	u.checkIfEnabled(context.Background())
	if requests.Load() != 0 || checked.Load() != 0 {
		t.Fatalf("disabled checker: requests=%d checked=%d", requests.Load(), checked.Load())
	}

	enabled = true
	u.checkIfEnabled(context.Background())
	if requests.Load() != 1 || checked.Load() != 1 {
		t.Fatalf("enabled checker: requests=%d checked=%d", requests.Load(), checked.Load())
	}
}

func TestUpdateCheckerCheck(t *testing.T) {
	oldEnabled := nativeUpdatesEnabled
	nativeUpdatesEnabled = func() bool { return false }
	t.Cleanup(func() { nativeUpdatesEnabled = oldEnabled })
	var body atomic.Value
	body.Store(`{"tag_name":"v1.2.0","html_url":"https://example.com/r","prerelease":false}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()

	u := &UpdateChecker{Client: srv.Client(), FeedURL: srv.URL, CurrentVersion: "1.1.0"}
	info, err := u.Check(context.Background())
	if err != nil || !info.Available || info.Latest != "1.2.0" || info.URL != "https://example.com/r" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if u.Latest() != info {
		t.Fatalf("Latest()=%+v", u.Latest())
	}

	u.CurrentVersion = "1.2.0"
	if info, _ := u.Check(context.Background()); info.Available {
		t.Fatalf("same version should not be available: %+v", info)
	}

	body.Store(`{"tag_name":"v9.0.0","html_url":"x","prerelease":true}`)
	if info, _ := u.Check(context.Background()); info.Available {
		t.Fatalf("prerelease must be skipped: %+v", info)
	}

	empty := &UpdateChecker{}
	if _, err := empty.Check(context.Background()); err == nil {
		t.Fatal("empty feed should error")
	}
}
