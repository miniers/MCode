package remote

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

const inspectorPreviewTestTimeout = 5 * time.Second

func TestInspectorPreviewRevokeSingleToken(t *testing.T) {
	root := t.TempDir()
	writeInspectorPreviewTestFile(t, root, "index.html", "ok")
	b := newInspectorPreviewTestBackend(t, map[string]string{"t1": root, "t2": root})
	var urls []string
	for _, threadID := range []string{"t1", "t1", "t2"} {
		u, err := b.InspectorPreviewURL(context.Background(), threadID, "index.html")
		if err != nil {
			t.Fatal(err)
		}
		urls = append(urls, u)
	}
	for _, malformed := range []string{"", "://", "https://example.test/p/token/a", strings.Replace(urls[0], "127.0.0.1", "localhost", 1)} {
		b.RevokeInspectorPreviewURL(malformed)
	}
	expectInspectorPreviewStatus(t, urls[0], http.StatusOK)
	b.RevokeInspectorPreviewURL(urls[0])
	b.RevokeInspectorPreviewURL(urls[0])
	expectInspectorPreviewStatus(t, urls[0], http.StatusNotFound)
	expectInspectorPreviewStatus(t, urls[1], http.StatusOK)
	expectInspectorPreviewStatus(t, urls[2], http.StatusOK)
	b.RevokeInspectorPreviews("t1")
	expectInspectorPreviewStatus(t, urls[1], http.StatusNotFound)
	expectInspectorPreviewStatus(t, urls[2], http.StatusOK)
}

func newInspectorPreviewTestBackend(t *testing.T, roots map[string]string) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	for threadID, root := range roots {
		b.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "project-a"}, nil)
		b.mu.Lock()
		b.threads[threadID].workDir = root
		b.mu.Unlock()
	}
	return b
}

func writeInspectorPreviewTestFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

func fetchInspectorPreview(t *testing.T, target string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{Timeout: inspectorPreviewTestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func expectInspectorPreviewStatus(t *testing.T, target string, want int) {
	t.Helper()
	resp, _ := fetchInspectorPreview(t, target, nil)
	if resp.StatusCode != want {
		t.Fatalf("请求 %s 状态=%d，期望 %d", target, resp.StatusCode, want)
	}
}

func TestInspectorPreviewRejectsTraversalSymlinkProtected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInspectorPreviewTestFile(t, root, "docs/index.html", "<html>ok</html>")
	writeInspectorPreviewTestFile(t, root, ".git/config", "[core]")
	writeInspectorPreviewTestFile(t, root, ".env", "SECRET=1")
	outside := writeInspectorPreviewTestFile(t, base, "outside.txt", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	previewURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "docs/index.html")
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(previewURL, "index.html")

	cases := []struct {
		name string
		url  string
	}{
		{"parent", prefix + "../outside.txt"},
		{"encoded parent", prefix + "%2e%2e/outside.txt"},
		{"encoded slash parent", prefix + "%2e%2e%2Foutside.txt"},
		{"symlink out of root", prefix + "escape.txt"},
		{"git", prefix + ".git/config"},
		{"protected", prefix + ".env"},
		{"absolute", prefix + "%2fetc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectInspectorPreviewStatus(t, tc.url, http.StatusNotFound)
		})
	}

	if _, err := b.InspectorPreviewURL(context.Background(), "thread-a", "../outside.txt"); err == nil {
		t.Fatal("为越界路径签发了 Token")
	}
	if _, err := b.InspectorPreviewURL(context.Background(), "thread-a", "escape.txt"); err == nil {
		t.Fatal("为跳出工作区的符号链接签发了 Token")
	}
	if _, err := b.InspectorPreviewURL(context.Background(), "thread-a", ".git/config"); err == nil {
		t.Fatal("为 .git 内容签发了 Token")
	}
	if _, err := b.InspectorPreviewURL(context.Background(), "thread-a", ".env"); err == nil {
		t.Fatal("为受保护文件签发了 Token")
	}
}

func TestInspectorPreviewTokenScopeAndRevocation(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeInspectorPreviewTestFile(t, rootA, "a.pdf", "pdf-a")
	writeInspectorPreviewTestFile(t, rootA, "other.pdf", "pdf-other")
	writeInspectorPreviewTestFile(t, rootB, "b.pdf", "pdf-b")

	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": rootA, "thread-b": rootB})
	urlA, err := b.InspectorPreviewURL(context.Background(), "thread-a", "a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	urlB, err := b.InspectorPreviewURL(context.Background(), "thread-b", "b.pdf")
	if err != nil {
		t.Fatal(err)
	}
	expectInspectorPreviewStatus(t, urlA, http.StatusOK)
	expectInspectorPreviewStatus(t, urlB, http.StatusOK)

	// PDF 的 Token 只允许入口文件本身，不能顺带读同目录的其它文件。
	prefixA := strings.TrimSuffix(urlA, "a.pdf")
	expectInspectorPreviewStatus(t, prefixA+"other.pdf", http.StatusNotFound)

	// Host 不匹配（DNS rebinding 形式）被拒绝。
	req, err := http.NewRequest(http.MethodGet, urlA, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "attacker.example"
	resp, err := (&http.Client{Timeout: inspectorPreviewTestTimeout}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Host 不匹配状态=%d，期望 403", resp.StatusCode)
	}

	// 撤销一个会话不影响另一个；撤销后旧 Token 立刻 404。
	b.RevokeInspectorPreviews("thread-a")
	expectInspectorPreviewStatus(t, urlA, http.StatusNotFound)
	expectInspectorPreviewStatus(t, urlB, http.StatusOK)

	// 工作区身份变化后旧 Token 失效：Token 绑定的是签发时的 root。
	b.mu.Lock()
	b.threads["thread-b"].workDir = rootA
	b.mu.Unlock()
	expectInspectorPreviewStatus(t, urlB, http.StatusNotFound)
}

func TestInspectorPreviewPDFRangeAndLimit(t *testing.T) {
	root := t.TempDir()
	pdf := []byte("%PDF-1.7\npreview range content\n")
	writeInspectorPreviewTestFile(t, root, "doc.pdf", string(pdf))
	bigPath := filepath.Join(root, "big.pdf")
	if err := os.WriteFile(bigPath, []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(bigPath, inspectorPreviewMaxFileBytes+1); err != nil {
		t.Fatal(err)
	}

	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	previewURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}

	resp, body := fetchInspectorPreview(t, previewURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PDF 状态=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("PDF Content-Type=%q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	if string(body) != string(pdf) {
		t.Fatal("PDF 内容不一致")
	}

	resp, body = fetchInspectorPreview(t, previewURL, http.Header{"Range": []string{"bytes=0-9"}})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range 状态=%d，期望 206", resp.StatusCode)
	}
	if len(body) != 10 || string(body) != string(pdf[:10]) {
		t.Fatalf("Range 内容=%q", body)
	}
	if got := resp.Header.Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-9/") {
		t.Fatalf("Content-Range=%q", got)
	}

	bigURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "big.pdf")
	if err != nil {
		t.Fatal(err)
	}
	expectInspectorPreviewStatus(t, bigURL, http.StatusRequestEntityTooLarge)
}

func TestInspectorPreviewHTMLResourcePolicy(t *testing.T) {
	root := t.TempDir()
	writeInspectorPreviewTestFile(t, root, "index.html", `<html><head><link rel="stylesheet" href="style.css"></head><body>hi</body></html>`)
	writeInspectorPreviewTestFile(t, root, "style.css", "body{color:red}")
	writeInspectorPreviewTestFile(t, root, "icon.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	writeInspectorPreviewTestFile(t, root, "sub/page.html", "<html>nested</html>")
	outside := writeInspectorPreviewTestFile(t, t.TempDir(), "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "outside.txt")); err != nil {
		t.Fatal(err)
	}

	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	previewURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(previewURL, "index.html")

	resp, body := fetchInspectorPreview(t, previewURL, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "style.css") {
		t.Fatalf("HTML 状态=%d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"script-src 'none'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP 缺少 %q: %q", directive, csp)
		}
	}

	resp, body = fetchInspectorPreview(t, prefix+"style.css", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "body{color:red}" {
		t.Fatalf("同 root 资源读取失败: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Fatalf("CSS Content-Type=%q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("CSS Cache-Control=%q", got)
	}
	// 子目录里的 HTML 也是同一 root 的资源，可以读取。
	expectInspectorPreviewStatus(t, prefix+"sub/page.html", http.StatusOK)
	// SVG 作为顶层文档可能执行脚本，响应同样带禁脚本的 CSP。
	svgResp, _ := fetchInspectorPreview(t, prefix+"icon.svg", nil)
	if svgResp.StatusCode != http.StatusOK || !strings.Contains(svgResp.Header.Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatalf("SVG CSP=%-q 状态=%d", svgResp.Header.Get("Content-Security-Policy"), svgResp.StatusCode)
	}
	// 目录不列内容。
	dirResp, dirBody := fetchInspectorPreview(t, prefix+"sub/", nil)
	if dirResp.StatusCode != http.StatusNotFound || strings.Contains(string(dirBody), "page.html") {
		t.Fatalf("目录被列出: %d %q", dirResp.StatusCode, dirBody)
	}
	// 跳出 root 的符号链接被拒绝。
	expectInspectorPreviewStatus(t, prefix+"outside.txt", http.StatusNotFound)
	// 绝对路径与编码越界被拒绝。
	expectInspectorPreviewStatus(t, prefix+"%2fetc/passwd", http.StatusNotFound)
	expectInspectorPreviewStatus(t, prefix+"%2e%2e/secret.txt", http.StatusNotFound)

	// 非 GET/HEAD 方法被拒绝。
	req, err := http.NewRequest(http.MethodPost, previewURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = (&http.Client{Timeout: inspectorPreviewTestTimeout}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST 状态=%d，期望 405", resp.StatusCode)
	}

	// 无效 Token 与缺少入口的 URL 都是 404。
	parsed, err := url.Parse(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	expectInspectorPreviewStatus(t, "http://"+parsed.Host+"/p/not-a-token/index.html", http.StatusNotFound)
	expectInspectorPreviewStatus(t, "http://"+parsed.Host+"/p/not-a-token", http.StatusNotFound)
}

func TestInspectorPreviewServerStopsOnBackendClose(t *testing.T) {
	root := t.TempDir()
	writeInspectorPreviewTestFile(t, root, "index.html", "<html>ok</html>")
	b := newInspectorPreviewTestBackend(t, map[string]string{"thread-a": root})
	previewURL, err := b.InspectorPreviewURL(context.Background(), "thread-a", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	expectInspectorPreviewStatus(t, previewURL, http.StatusOK)

	b.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(previewURL); err == nil {
		resp.Body.Close()
		t.Fatal("后端关闭后本地内容服务仍在响应")
	}
}
