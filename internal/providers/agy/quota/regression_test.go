package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMissingQuotaDoesNotMeanExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{"models":{"gemini-missing":{},"gemini-empty":{"quotaInfo":{}}}}`))
		case "/v1internal:retrieveUserQuotaSummary":
			_, _ = w.Write([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"5h"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	client := Client{HTTPClient: srv.Client(), Hosts: []string{srv.URL}}
	res, err := client.Fetch(context.Background(), "mock", "")
	if err != nil || len(res.Groups) != 0 {
		t.Fatalf("missing quota was fabricated as exhausted: %+v, %v", res, err)
	}
}

func TestGenericForbiddenDoesNotDisableAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "PERMISSION_DENIED", http.StatusForbidden)
	}))
	defer srv.Close()
	client := Client{HTTPClient: srv.Client(), Hosts: []string{srv.URL}}
	_, err := client.Fetch(context.Background(), "mock", "")
	if err == nil || errors.Is(err, ErrForbidden) {
		t.Fatalf("generic project permission error treated as account ban: %v", err)
	}
}

func TestWeeklyActivationMinimalRequestAndNoNetworkRetry(t *testing.T) {
	genCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"test-proj"}`))
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{"models":{"gemini-dynamic-model":{"displayName":"Gemini Dynamic Model"}}}`))
		case "/v1internal:retrieveUserQuotaSummary":
			_, _ = w.Write([]byte(`{}`))
		case "/v1internal:generateContent":
			genCalls++
			var payload struct {
				Model   string `json:"model"`
				Project string `json:"project"`
				Request struct {
					GenerationConfig struct {
						MaxOutputTokens int `json:"maxOutputTokens"`
					} `json:"generationConfig"`
				} `json:"request"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.Model != "gemini-dynamic-model" || payload.Request.GenerationConfig.MaxOutputTokens != 1 {
				t.Errorf("incorrect activation request: model=%s, maxOutputTokens=%d", payload.Model, payload.Request.GenerationConfig.MaxOutputTokens)
			}
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := Client{HTTPClient: srv.Client(), Hosts: []string{srv.URL}}
	if err := client.ActivateWeekly(context.Background(), "mock", "project"); err == nil {
		t.Fatal("expected activation error")
	}
	if genCalls != 1 {
		t.Fatalf("potentially billable request was retried %d times", genCalls)
	}
}
