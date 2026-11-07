package remote

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// cspScriptHash 是测试侧独立计算的 CSP 表达式，不复用被测实现。
func cspScriptHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestInlineScriptHashes(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "no script",
			html: `<html><body>hello</body></html>`,
		},
		{
			name: "single inline script",
			html: `<html><head><script>console.log('a')</script></head></html>`,
			want: []string{cspScriptHash([]byte(`console.log('a')`))},
		},
		{
			name: "external scripts ignored",
			html: `<html><head><script src="./remote-boot.js"></script><script type="module" src="./src/main.tsx"></script></head></html>`,
		},
		{
			name: "multiple inline scripts",
			html: `<script>first()</script><script src="/a.js"></script><script type="module">
  second();
</script>`,
			want: []string{cspScriptHash([]byte("first()")), cspScriptHash([]byte("\n  second();\n"))},
		},
		{
			name: "raw bytes are not trimmed",
			html: "<script>\n  x();\n</script>",
			want: []string{cspScriptHash([]byte("\n  x();\n"))},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inlineScriptHashes([]byte(tc.html))
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d hashes, got %d: %v", len(tc.want), len(got), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("hash[%d] = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestInlineScriptHashes_WebIndexHTML(t *testing.T) {
	html, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}

	hashes := inlineScriptHashes(html)
	if len(hashes) == 0 {
		t.Fatal("expected at least one inline script hash from web/index.html")
	}

	// 独立定位首个脚本标签，避免直接复用被测正则。
	text := string(html)
	openIdx := strings.Index(text, "<script")
	if openIdx < 0 {
		t.Fatal("no script tag found in web/index.html")
	}
	tagEnd := strings.Index(text[openIdx:], ">") + openIdx
	if tagEnd <= openIdx {
		t.Fatal("unterminated script tag in web/index.html")
	}
	if strings.Contains(text[openIdx:tagEnd], "src") {
		t.Fatalf("first script tag is not inline: %q", text[openIdx:tagEnd])
	}
	closeIdx := strings.Index(text[tagEnd+1:], "</script>")
	if closeIdx < 0 {
		t.Fatal("no closing script tag in web/index.html")
	}
	content := html[tagEnd+1 : tagEnd+1+closeIdx]

	want := cspScriptHash(content)
	if hashes[0] != want {
		t.Fatalf("first inline script hash = %s, want %s", hashes[0], want)
	}
}

func TestServeStaticAsset_CSPIncludesInlineScriptHashes(t *testing.T) {
	inline := "\n  (function () { document.documentElement.classList.add('dark'); })();\n"
	assets := fstest.MapFS{
		"remote-index.html": {
			Data: []byte(`<html><head><script>` + inline + `</script><script src="./remote-boot.js"></script></head></html>`),
		},
	}
	server := NewServer(ServerOptions{
		Assets:       assets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	csp := resp.Header.Get("Content-Security-Policy")
	want := cspScriptHash([]byte(inline))
	if !strings.Contains(csp, "script-src 'self' "+want) {
		t.Fatalf("CSP missing inline script hash %s: %s", want, csp)
	}
	if strings.Count(csp, "'sha256-") != 1 {
		t.Fatalf("expected exactly one sha256 source (external script must be ignored): %s", csp)
	}
	for _, directive := range []string{
		"default-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"connect-src 'self'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"frame-ancestors 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP missing directive %q: %s", directive, csp)
		}
	}

	// 第二次请求走缓存，结果必须一致。
	resp2, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("second GET / failed: %v", err)
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, resp2.Body)
	if got := resp2.Header.Get("Content-Security-Policy"); got != csp {
		t.Fatalf("cached CSP mismatch: %s != %s", got, csp)
	}
}
