package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcode/internal/agent/llm"
)

func runFetch(t *testing.T, ctx context.Context, args map[string]any) Result {
	t.Helper()
	raw, e := json.Marshal(args)
	if e != nil {
		t.Fatal(e)
	}
	r, e := WebFetchTool().Run(ctx, Env{}, raw)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestWebFetchHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != llm.UserAgent {
			t.Errorf("User-Agent = %q, want %q", got, llm.UserAgent)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><html><head><title>t</title><style>.x{color:red}</style></head><body>
<h1>Title</h1><h2>Sub</h2>
<p>Hello <b>world</b>.</p>
<script>alert(1)</script>
<ul><li>one</li><li>two <a href="/next?q=1">Next</a></li></ul>
<pre>line one
line two</pre>
</body></html>`))
	}))
	defer srv.Close()

	r := runFetch(t, context.Background(), map[string]any{"url": srv.URL + "/dir/page"})
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Text)
	}
	for _, want := range []string{
		"URL: " + srv.URL + "/dir/page",
		"Status: 200",
		"# Title",
		"## Sub",
		"Hello world.",
		"- one",
		"- two Next (" + srv.URL + "/next?q=1)",
		"line one\nline two",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in:\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "alert(1)") || strings.Contains(r.Text, "color:red") {
		t.Errorf("script or style content leaked:\n%s", r.Text)
	}
}

func TestWebFetchRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("final body"))
	}))
	defer srv.Close()

	r := runFetch(t, context.Background(), map[string]any{"url": srv.URL + "/old"})
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Text)
	}
	if !strings.HasPrefix(r.Text, "URL: "+srv.URL+"/new\nStatus: 200\n\n") {
		t.Fatalf("unexpected header:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "final body") {
		t.Fatalf("missing body:\n%s", r.Text)
	}
}

func TestWebFetch404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("missing page"))
	}))
	defer srv.Close()

	r := runFetch(t, context.Background(), map[string]any{"url": srv.URL})
	if !r.IsError {
		t.Fatalf("expected error result, got: %s", r.Text)
	}
	if !strings.Contains(r.Text, "Status: 404") || !strings.Contains(r.Text, "missing page") {
		t.Fatalf("unexpected text:\n%s", r.Text)
	}
}

func TestWebFetchRejectsImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG"))
	}))
	defer srv.Close()

	r := runFetch(t, context.Background(), map[string]any{"url": srv.URL})
	if !r.IsError || r.Text != "unsupported content type: image/png" {
		t.Fatalf("unexpected result: %#v", r)
	}
}

func TestWebFetchMaxChars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	r := runFetch(t, context.Background(), map[string]any{"url": srv.URL, "maxChars": 4})
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Text)
	}
	if !strings.HasSuffix(r.Text, "\n\n0123\n[truncated: 6 more chars]") {
		t.Fatalf("unexpected truncation:\n%s", r.Text)
	}
}

func TestWebFetchRejectsFileScheme(t *testing.T) {
	r := runFetch(t, context.Background(), map[string]any{"url": "file:///etc/passwd"})
	if !r.IsError || !strings.Contains(r.Text, "unsupported url scheme") {
		t.Fatalf("unexpected result: %#v", r)
	}
}

func TestWebFetchContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { done <- runFetch(t, ctx, map[string]any{"url": srv.URL}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case r := <-done:
		if !r.IsError {
			t.Fatalf("expected error result, got: %s", r.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not return after context cancellation")
	}
}
