package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/designpick"
	"mcode/internal/remote/wire"
)

func newRemotePreviewTestServer(t *testing.T, b *MemoryBackend) *httptest.Server {
	t.Helper()
	srv := NewServer(ServerOptions{
		Entry:          EntryLAN,
		AllowedHosts:   []string{"127.0.0.1", "localhost"},
		PreviewHandler: b.RemotePreviewHandler(),
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func TestRemotePreview_ServesWithSandboxCSP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	htmlBody := `<!DOCTYPE html><html><head><link rel="stylesheet" href="style.css"></head><body><script>console.log("ok")</script></body></html>`
	cssBody := `body{color:blue}`
	writeInspectorPreviewTestFile(t, root, "sub/index.html", htmlBody)
	writeInspectorPreviewTestFile(t, root, "sub/style.css", cssBody)
	writeInspectorPreviewTestFile(t, root, ".git/config", "[core]")
	outside := writeInspectorPreviewTestFile(t, base, "outside.txt", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	threadID := "11111111-1111-4111-8111-111111111111"
	b := newInspectorPreviewTestBackend(t, map[string]string{threadID: root})
	ts := newRemotePreviewTestServer(t, b)

	// 通过 threads.preview.open RPC 签发同源相对路径，不触发本地回环服务启动。
	d := NewDispatcher(&fakeAuth{}, b)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	rawResp := d.Handle(context.Background(), conn, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.preview.open","params":{"threadID":%q,"path":"sub/index.html"},"id":1}`,
		threadID,
	)))
	var rpcResp wire.Response
	if err := json.Unmarshal(rawResp, &rpcResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("threads.preview.open failed: %+v", rpcResp.Error)
	}
	resBytes, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var openRes wire.ThreadsPreviewOpenResult
	if err := json.Unmarshal(resBytes, &openRes); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(openRes.URL, inspectorPreviewURLPrefix) || !strings.HasSuffix(openRes.URL, "/sub/index.html") {
		t.Fatalf("unexpected remote preview url: %q", openRes.URL)
	}

	b.inspectorPreviewMu.Lock()
	loopbackStarted := b.inspectorPreview != nil
	b.inspectorPreviewMu.Unlock()
	if loopbackStarted {
		t.Fatal("RemotePreviewPath 不应懒启动本地回环服务")
	}

	resp, body := fetchInspectorPreview(t, ts.URL+openRes.URL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTML 状态=%d，期望 200", resp.StatusCode)
	}
	if string(body) != htmlBody {
		t.Fatalf("HTML 内容=%q，期望 %q", string(body), htmlBody)
	}
	if xfo := resp.Header.Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("远程预览不应设置 X-Frame-Options，实际=%q", xfo)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if csp != remotePreviewCSP {
		t.Fatalf("CSP=%q，期望 %q", csp, remotePreviewCSP)
	}
	if !strings.Contains(csp, "sandbox allow-scripts") {
		t.Fatalf("CSP 缺少 sandbox allow-scripts: %q", csp)
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("CSP 不应包含 allow-same-origin: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") || strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP frame-ancestors 不符: %q", csp)
	}

	// 同目录相对资源可以取到
	prefix := strings.TrimSuffix(openRes.URL, "index.html")
	cssResp, cssGot := fetchInspectorPreview(t, ts.URL+prefix+"style.css", nil)
	if cssResp.StatusCode != http.StatusOK || string(cssGot) != cssBody {
		t.Fatalf("同目录资源读取失败: status=%d body=%q", cssResp.StatusCode, string(cssGot))
	}

	// 目录外的路径、符号链接逃逸与 .git 内部必须被拒绝
	rootPrefix := strings.TrimSuffix(openRes.URL, "sub/index.html")
	for _, rel := range []string{
		"../outside.txt",
		"%2e%2e/outside.txt",
		"%2e%2e%2Foutside.txt",
		"escape.txt",
		".git/config",
		"%2fetc/passwd",
	} {
		badResp, _ := fetchInspectorPreview(t, ts.URL+rootPrefix+rel, nil)
		if badResp.StatusCode != http.StatusNotFound && badResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("越界路径 %q 状态=%d，期望 404/400", rel, badResp.StatusCode)
		}
	}

	// 未知后端或非法参数应被拦截
	noPreviewDisp := NewDispatcher(&fakeAuth{}, &fakeBackend{})
	errRaw := noPreviewDisp.Handle(context.Background(), conn, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.preview.open","params":{"threadID":%q,"path":"sub/index.html"},"id":2}`,
		threadID,
	)))
	var errResp wire.Response
	if err := json.Unmarshal(errRaw, &errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Error == nil || errResp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected, got %+v", errResp.Error)
	}

	for _, badParams := range []string{
		`{"threadID":"not-a-uuid","path":"sub/index.html"}`,
		fmt.Sprintf(`{"threadID":%q,"path":"   "}`, threadID),
		fmt.Sprintf(`{"threadID":%q,"path":"sub/index.html","extra":1}`, threadID),
	} {
		raw := d.Handle(context.Background(), conn, []byte(`{"jsonrpc":"2.0","method":"threads.preview.open","params":`+badParams+`,"id":3}`))
		var r wire.Response
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.Error == nil || r.Error.Code != wire.InvalidParams {
			t.Fatalf("params %s: expected InvalidParams, got %+v", badParams, r.Error)
		}
	}
}

func TestRemotePreview_Expired(t *testing.T) {
	root := t.TempDir()
	writeInspectorPreviewTestFile(t, root, "index.html", "<html>ok</html>")

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	b.inspectorPreviewNow = func() time.Time { return now }

	previewPath, err := b.RemotePreviewPath(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	token, _, ok := inspectorPreviewRequestPath(previewPath)
	if !ok {
		t.Fatalf("invalid preview path %q", previewPath)
	}

	ts := newRemotePreviewTestServer(t, b)
	now = now.Add(remotePreviewTokenTTL - time.Minute)
	expectInspectorPreviewStatus(t, ts.URL+previewPath, http.StatusOK)

	now = now.Add(2 * time.Minute)
	expectInspectorPreviewStatus(t, ts.URL+previewPath, http.StatusNotFound)

	b.inspectorPreviewMu.Lock()
	_, exists := b.inspectorPreviewGrants[token]
	b.inspectorPreviewMu.Unlock()
	if exists {
		t.Fatal("过期令牌应在查询时顺手删除")
	}
}

func TestRemotePreview_TokenIsolation(t *testing.T) {
	root := t.TempDir()
	writeInspectorPreviewTestFile(t, root, "index.html", "<html>ok</html>")

	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	ts := newRemotePreviewTestServer(t, b)

	remotePath, err := b.RemotePreviewPath(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	desktopURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}

	remoteToken, _, ok := inspectorPreviewRequestPath(remotePath)
	if !ok {
		t.Fatalf("invalid remote path %q", remotePath)
	}
	desktopToken, _, ok := inspectorPreviewRequestPath(strings.TrimPrefix(desktopURL, "http://"+b.inspectorPreview.addr))
	if !ok {
		t.Fatalf("invalid desktop url %q", desktopURL)
	}

	// 各自在自己的入口有效
	expectInspectorPreviewStatus(t, ts.URL+remotePath, http.StatusOK)
	expectInspectorPreviewStatus(t, desktopURL, http.StatusOK)

	// 远程令牌在回环服务上无效；桌面令牌在远程服务上无效
	expectInspectorPreviewStatus(t, "http://"+b.inspectorPreview.addr+inspectorPreviewURLPrefix+remoteToken+"/index.html", http.StatusNotFound)
	expectInspectorPreviewStatus(t, ts.URL+inspectorPreviewURLPrefix+desktopToken+"/index.html", http.StatusNotFound)

	// RevokeInspectorPreviews 会一并撤销远程和桌面令牌
	b.RevokeInspectorPreviews("thread-a")
	expectInspectorPreviewStatus(t, ts.URL+remotePath, http.StatusNotFound)
	expectInspectorPreviewStatus(t, desktopURL, http.StatusNotFound)
}

func TestRemotePreviewAndFinalizeDesign_SchemaAndCapabilities(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	for _, m := range []string{"threads.preview.open", "threads.swarm.finalizeDesign"} {
		foundReq := false
		for name, raw := range doc.Defs {
			var d struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &d) == nil && d.Properties.Method.Const == m {
				foundReq = true
				if strings.Count(string(data), `"$ref": "#/$defs/`+name+`"`) != 2 {
					t.Fatalf("%s request $ref count != 2", m)
				}
				respName := strings.TrimSuffix(name, "Request") + "Response"
				if strings.Count(string(data), `"$ref": "#/$defs/`+respName+`"`) != 2 {
					t.Fatalf("%s response (%s) $ref count != 2", m, respName)
				}
				break
			}
		}
		if !foundReq {
			t.Fatalf("schema missing %s", m)
		}

		registered := false
		for _, impl := range ImplementedMethods() {
			if impl == m {
				registered = true
				break
			}
		}
		if !registered {
			t.Fatalf("ImplementedMethods missing %s", m)
		}
	}

	var beeSchema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(doc.Defs["RemoteSwarmBee"], &beeSchema); err != nil {
		t.Fatal(err)
	}
	if beeSchema.Properties["awaitingFinalize"].Type != "boolean" {
		t.Fatalf("RemoteSwarmBee.awaitingFinalize type = %q, want boolean", beeSchema.Properties["awaitingFinalize"].Type)
	}
	if beeSchema.Properties["finalizeRequested"].Type != "boolean" {
		t.Fatalf("RemoteSwarmBee.finalizeRequested type = %q, want boolean", beeSchema.Properties["finalizeRequested"].Type)
	}
}

func TestRemotePreviewDesignPick_InjectsAfterHead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		headTag  string
		htmlBody string
	}{
		{
			name:     "lowercase head",
			headTag:  "<head>",
			htmlBody: `<!DOCTYPE html><html><head><title>pick</title></head><body><div data-mc-choice="布局"><div data-mc-option="A" data-mc-recommended>A</div><div data-mc-option="B">B</div></div></body></html>`,
		},
		{
			name:     "uppercase HEAD",
			headTag:  "<HEAD>",
			htmlBody: `<!DOCTYPE html><HTML><HEAD><title>pick</title></HEAD><body><div data-mc-choice="布局"><div data-mc-option="A">A</div><div data-mc-option="B">B</div></div></body></HTML>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeInspectorPreviewTestFile(t, root, "pick.html", tc.htmlBody)
			b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
			ts := newRemotePreviewTestServer(t, b)

			previewPath, err := b.RemotePreviewPath(context.Background(), "thread-a", "pick.html")
			if err != nil {
				t.Fatal(err)
			}
			resp, body := fetchInspectorPreview(t, ts.URL+previewPath, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want 200", resp.StatusCode)
			}
			if csp := resp.Header.Get("Content-Security-Policy"); csp != remotePreviewCSP {
				t.Fatalf("CSP=%q, want %q", csp, remotePreviewCSP)
			}
			expectedPrefix := tc.headTag + "<script>" + designpick.Script + "</script>"
			if !strings.Contains(string(body), expectedPrefix) {
				t.Fatalf("expected injected script right after %s", tc.headTag)
			}
		})
	}
}

func TestRemotePreviewDesignPick_NoMarkerNotInjected(t *testing.T) {
	root := t.TempDir()
	htmlBody := `<!DOCTYPE html><html><head><title>plain</title></head><body><div>no marker</div></body></html>`
	writeInspectorPreviewTestFile(t, root, "plain.html", htmlBody)
	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	ts := newRemotePreviewTestServer(t, b)

	previewPath, err := b.RemotePreviewPath(context.Background(), "thread-a", "plain.html")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := fetchInspectorPreview(t, ts.URL+previewPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if string(body) != htmlBody {
		t.Fatalf("body=%q, want %q", string(body), htmlBody)
	}
	if strings.Contains(string(body), designpick.Script) {
		t.Fatal("should not inject picker script without data-mc-choice")
	}
}

func TestRemotePreviewDesignPick_NonRemoteNotInjected(t *testing.T) {
	root := t.TempDir()
	htmlBody := `<!DOCTYPE html><html><head><title>desktop</title></head><body><div data-mc-choice="q"><div data-mc-option="A">A</div><div data-mc-option="B">B</div></div></body></html>`
	writeInspectorPreviewTestFile(t, root, "desktop.html", htmlBody)
	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})

	desktopURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "desktop.html")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := fetchInspectorPreview(t, desktopURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if string(body) != htmlBody {
		t.Fatalf("non-remote body=%q, want %q", string(body), htmlBody)
	}
	if strings.Contains(string(body), designpick.Script) {
		t.Fatal("non-remote preview should not inject picker script")
	}
}

func TestRemotePreviewDesignPick_NoHeadPrepends(t *testing.T) {
	root := t.TempDir()
	htmlBody := `<header>title</header><section data-mc-choice="q"><div data-mc-option="A">A</div><div data-mc-option="B">B</div></section>`
	writeInspectorPreviewTestFile(t, root, "fragment.html", htmlBody)
	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	ts := newRemotePreviewTestServer(t, b)

	previewPath, err := b.RemotePreviewPath(context.Background(), "thread-a", "fragment.html")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := fetchInspectorPreview(t, ts.URL+previewPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	want := "<script>" + designpick.Script + "</script>" + htmlBody
	if string(body) != want {
		t.Fatalf("body prefix mismatch: got len=%d, want len=%d", len(body), len(want))
	}
}
