package mcp

import "testing"

func TestOAuthOpenBrowserRejectsNonHTTPScheme(t *testing.T) {
	t.Parallel()

	var called []string
	mgr := NewOAuthManager(OAuthOptions{
		OpenBrowser: func(rawURL string) error {
			called = append(called, rawURL)
			return nil
		},
	})

	rejected := []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"smb://host/share",
		"https:///nohost",
	}
	for _, rawURL := range rejected {
		if err := mgr.openBrowser(rawURL); err == nil {
			t.Errorf("openBrowser(%q) expected error, got nil", rawURL)
		}
	}
	if len(called) != 0 {
		t.Fatalf("expected OpenBrowser not to be called for rejected URLs, got %v", called)
	}

	allowed := []string{
		"https://auth.example.com/authorize?x=1",
		"http://127.0.0.1:8080/cb",
	}
	for _, rawURL := range allowed {
		if err := mgr.openBrowser(rawURL); err != nil {
			t.Errorf("openBrowser(%q) unexpected error: %v", rawURL, err)
		}
	}
	if len(called) != len(allowed) {
		t.Fatalf("expected %d OpenBrowser calls, got %d (%v)", len(allowed), len(called), called)
	}
	for i, want := range allowed {
		if called[i] != want {
			t.Errorf("called[%d] = %q, want %q", i, called[i], want)
		}
	}
}
