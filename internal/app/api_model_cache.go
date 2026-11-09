package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
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
	var ids []string
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("获取模型目录 panic：%v", r)
			log.Print(err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		defer close(flight.done)
		if err == nil {
			e.ids = append([]string(nil), ids...)
			e.expires = c.now().Add(5 * time.Minute)
		} else {
			e.expires = c.now().Add(30 * time.Second)
		}
		flight.err = err
		e.flight = nil
	}()
	ids, err = c.fetch(ctx, apiModelHTTPClient, source, key)
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
var firstAvailabilityWait = 300 * time.Millisecond

type sourceAvailability struct {
	mu        sync.Mutex
	checked   time.Time
	running   bool
	done      chan struct{}
	available bool
	reason    string
	probe     func() (bool, string)
}

func (a *sourceAvailability) start(kind harness.Kind, cfg config.Config, secrets config.SecretStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running && time.Since(a.checked) >= time.Minute {
		a.running = true
		a.done = make(chan struct{})
		done := a.done
		go func() {
			defer func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				if r := recover(); r != nil {
					log.Printf("来源可用性检查 panic：%v", r)
					a.checked = time.Now()
					a.available = false
					a.reason = "来源可用性检查失败"
				}
				a.running = false
				close(done)
			}()
			var usable bool
			reason := ""
			if a.probe != nil {
				usable, reason = a.probe()
			} else {
				current := mcodeCurrentConfig(cfg, secrets)
				store := mcodeCurrentSecrets(secrets)
				source, ok := findAPISource(apisource.Resolve(current, store), kind)
				usable = ok && beeSourceUsable(source, store)
				if !ok {
					reason = fmt.Sprintf("来源 %s 未启用或已删除", kind)
				} else if !usable {
					reason = "未配置 API Key"
				}
			}
			a.mu.Lock()
			a.available = usable
			a.reason = reason
			a.checked = time.Now()
			a.mu.Unlock()
		}()
	}
}

func (a *sourceAvailability) get(kind harness.Kind, cfg config.Config, secrets config.SecretStore) (bool, string) {
	a.start(kind, cfg, secrets)
	a.mu.Lock()
	if a.checked.IsZero() {
		done := a.done
		a.mu.Unlock()
		timer := time.NewTimer(firstAvailabilityWait)
		select {
		case <-done:
		case <-timer.C:
		}
		timer.Stop()
		a.mu.Lock()
	}
	defer a.mu.Unlock()
	if a.checked.IsZero() {
		// 检查超时后先按可用处理，避免启动时整列来源闪成不可用；后台检查完成后由下一次读取纠正。
		return true, ""
	}
	return a.available, a.reason
}
