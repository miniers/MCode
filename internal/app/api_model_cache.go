package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/readhttp"
)

const apiModelRequestTimeout = 5 * time.Second

type apiModelEntry struct {
	ids     []string
	expires time.Time
	flight  *apiModelFlight
}

type apiModelFlight struct {
	done chan struct{}
	err  error
}

type apiSourceModelCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*apiModelEntry
	now     func() time.Time
	fetch   func(context.Context, *http.Client, apisource.Source, string) ([]string, error)
}

func newAPISourceModelCache() *apiSourceModelCache {
	return &apiSourceModelCache{entries: make(map[[32]byte]*apiModelEntry), now: time.Now, fetch: apisource.ListModels}
}

var sourceModelCache = newAPISourceModelCache()
var apiModelHTTPClient = readhttp.NewClient(nil)

func apiModelKey(source apisource.Source, key string) [32]byte {
	b, _ := json.Marshal([]any{source.Kind, source.BaseURL, key, source.Headers})
	return sha256.Sum256(b)
}

func (c *apiSourceModelCache) models(source apisource.Source, key string) []string {
	k := apiModelKey(source, key)
	c.mu.Lock()
	e := c.entries[k]
	if e == nil {
		e = &apiModelEntry{}
		c.entries[k] = e
	}
	ids := append([]string(nil), e.ids...)
	if e.flight == nil && !c.now().Before(e.expires) {
		e.flight = &apiModelFlight{done: make(chan struct{})}
		go c.fetchEntry(context.Background(), e, e.flight, source, key)
	}
	c.mu.Unlock()
	return ids
}

func (c *apiSourceModelCache) fetchEntry(ctx context.Context, e *apiModelEntry, flight *apiModelFlight, source apisource.Source, key string) {
	ctx, cancel := context.WithTimeout(ctx, apiModelRequestTimeout)
	defer cancel()
	ids, err := c.fetch(ctx, apiModelHTTPClient, source, key)
	c.mu.Lock()
	if err == nil {
		e.ids = append([]string(nil), ids...)
		e.expires = c.now().Add(5 * time.Minute)
	} else {
		e.expires = c.now().Add(30 * time.Second)
	}
	flight.err = err
	e.flight = nil
	close(flight.done)
	c.mu.Unlock()
}

func (c *apiSourceModelCache) refresh(ctx context.Context, source apisource.Source, key string) error {
	ctx, cancel := context.WithTimeout(ctx, apiModelRequestTimeout)
	defer cancel()
	k := apiModelKey(source, key)
	c.mu.Lock()
	e := c.entries[k]
	if e == nil {
		e = &apiModelEntry{}
		c.entries[k] = e
	}
	if e.flight == nil {
		e.flight = &apiModelFlight{done: make(chan struct{})}
		go c.fetchEntry(ctx, e, e.flight, source, key)
	}
	flight := e.flight
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-flight.done:
	}
	return flight.err
}

// sourceAvailability 在后台探测凭据，避免列表读取等待钥匙串。
type sourceAvailability struct {
	mu        sync.Mutex
	checked   time.Time
	running   bool
	available bool
	reason    string
}

func (a *sourceAvailability) get(kind harness.Kind, cfg config.Config, secrets config.SecretStore) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running && time.Since(a.checked) >= time.Minute {
		a.running = true
		go func() {
			current := mcodeCurrentConfig(cfg, secrets)
			store := mcodeCurrentSecrets(secrets)
			source, ok := findAPISource(apisource.Resolve(current, store), kind)
			usable := ok && beeSourceUsable(source, store)
			reason := ""
			if !ok {
				reason = fmt.Sprintf("来源 %s 未启用或已删除", kind)
			} else if !usable {
				reason = "未配置 API Key"
			}
			a.mu.Lock()
			a.available = usable
			a.reason = reason
			a.checked = time.Now()
			a.running = false
			a.mu.Unlock()
		}()
	}
	if a.checked.IsZero() {
		return false, "正在检查 API Key"
	}
	return a.available, a.reason
}
