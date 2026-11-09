package apisource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-Test") != "extra" {
			t.Errorf("请求头 = %v", r.Header)
		}
		fmt.Fprint(w, `{"data":[{"id":"b"},{"id":" a "},{"id":"a"},{"id":" "}]}`)
	}))
	defer server.Close()
	for _, suffix := range []string{"/v1/", "/v1/chat/completions/"} {
		t.Run(suffix, func(t *testing.T) {
			src := Source{BaseURL: server.URL + suffix, Headers: map[string]string{"X-Test": "extra"}}
			models, err := ListModels(context.Background(), nil, src, " test-key ")
			if err != nil || !reflect.DeepEqual(models, []string{"a", "b"}) {
				t.Fatalf("模型 = %v，错误 = %v", models, err)
			}
		})
	}
}

func TestListModelsResponses(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "空列表", body: `{"data":[]}`},
		{name: "非成功状态", status: http.StatusUnauthorized, body: "secret response", wantErr: "401"},
		{name: "坏 JSON", body: `{`, wantErr: "parse"},
		{name: "超限", body: `{"data":[]}` + strings.Repeat(" ", maxModelsResponseBytes), wantErr: "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			models, err := ListModels(context.Background(), server.Client(), Source{BaseURL: server.URL}, "")
			if tc.wantErr == "" {
				if err != nil || models == nil || len(models) != 0 {
					t.Fatalf("模型 = %v，错误 = %v", models, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "secret response") {
				t.Fatalf("错误 = %v", err)
			}
		})
	}
}

func TestListModelsCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ListModels(ctx, server.Client(), Source{BaseURL: server.URL}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v，期望 context.Canceled", err)
	}
}
