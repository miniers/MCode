package accounts

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestInvalidOAuthCallbackDoesNotFinishLogin(t *testing.T) {
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: t.TempDir(), Endpoints: endpoints,
		Opener: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := m.LoginStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Cancel()
	if err := sess.Submit(sess.redirectURI + "?code=missing-state"); err == nil {
		t.Fatal("callback URL without state accepted")
	}
	for i := 0; i < 3; i++ {
		res, err := http.Get(sess.redirectURI + "?error=access_denied&state=wrong")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatal("invalid callback accepted")
		}
	}
	select {
	case <-sess.resChan:
		t.Fatal("invalid callback terminated a valid login")
	default:
	}
	if err := sess.Submit("valid-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&emailSeq) == 0 {
		t.Fatal("valid login never completed")
	}
}

func TestLoginMatchingSystemEmailNeverWritesSystemToken(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser", true: "terminal"}[terminal], func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(Options{DataDir: t.TempDir(), HostHome: home})
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			m.index.Accounts[0].Email = "same@example.com"
			m.mu.Unlock()
			path := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
			if err := atomicWriteJSON(path, OAuthTokenFile{Token: OAuthTokenData{AccessToken: "system-original"}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			token := OAuthTokenFile{Token: OAuthTokenData{AccessToken: "new-login"}}
			var acc Account
			if terminal {
				id, _, _, err := m.TerminalLogin()
				if err != nil {
					t.Fatal(err)
				}
				acc, err = m.adoptTerminalLoginLocked(id, userInfoResponse{Email: "same@example.com"}, token)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				acc, err = m.mergeAndSaveAccount(token, "same@example.com", "User")
				if err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) || acc.ID == SystemAccountID {
				t.Fatal("login matching system email modified system credentials")
			}
		})
	}
}
