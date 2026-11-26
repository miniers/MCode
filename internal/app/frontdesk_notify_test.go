package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/config"
)

func disableStewardForTest(t *testing.T, dataDir string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写入关闭总管的配置失败: %v", err)
	}
}

func TestWebhookPayloadFormats(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want map[string]any
	}{
		{
			name: "feishu",
			url:  "https://open.feishu.cn/open-apis/bot/v2/hook/secret",
			want: map[string]any{
				"msg_type": "text",
				"content":  map[string]any{"text": "待审批\n请确认操作"},
			},
		},
		{
			name: "lark",
			url:  "https://open.larksuite.com/open-apis/bot/v2/hook/secret",
			want: map[string]any{
				"msg_type": "text",
				"content":  map[string]any{"text": "待审批\n请确认操作"},
			},
		},
		{
			name: "dingtalk",
			url:  "https://oapi.dingtalk.com/robot/send?access_token=secret",
			want: map[string]any{
				"msgtype": "text",
				"text":    map[string]any{"content": "待审批\n请确认操作"},
			},
		},
		{
			name: "generic",
			url:  "https://example.com/webhook/notify",
			want: map[string]any{
				"title": "待审批",
				"body":  "请确认操作",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := webhookPayload(tc.url, "待审批", "请确认操作")
			if err != nil {
				t.Fatalf("webhookPayload(%q) unexpected error: %v", tc.url, err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal payload %s: %v", raw, err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("payload = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestWebhookPayloadInvalidScheme(t *testing.T) {
	for _, rawURL := range []string{
		"",
		"ftp://open.feishu.cn/open-apis/bot/v2/hook/abc",
		"javascript:alert(1)",
		"://bad-url",
		"https:///missing-host",
	} {
		if _, err := webhookPayload(rawURL, "t", "b"); err == nil {
			t.Errorf("webhookPayload(%q) expected error, got nil", rawURL)
		}
	}
}

func TestWebhookPost(t *testing.T) {
	var (
		gotMethod      string
		gotContentType string
		gotBody        []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := postWebhook(context.Background(), srv.Client(), srv.URL+"/secret-token", "任务停滞", "请查看"); err != nil {
		t.Fatalf("postWebhook failed: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var parsed struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("unmarshal body %q: %v", gotBody, err)
	}
	if parsed.Title != "任务停滞" || parsed.Body != "请查看" {
		t.Errorf("body = %+v, want title=任务停滞 body=请查看", parsed)
	}
}

func TestWebhookPostErrorDoesNotLeakURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	closedURL := srv.URL + "/super-secret-hook-key"
	srv.Close()

	err := postWebhook(context.Background(), srv.Client(), closedURL, "t", "b")
	if err == nil {
		t.Fatal("expected error for closed server")
	}
	if strings.Contains(err.Error(), "super-secret-hook-key") {
		t.Fatalf("error leaked secret URL path: %v", err)
	}
}
