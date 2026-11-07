package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
