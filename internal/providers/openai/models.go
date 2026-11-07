package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultModelCacheTTL 是 ModelCache 的默认缓存时长。
const defaultModelCacheTTL = 10 * time.Minute

// modelsURL 按与 chatCompletionsURL 相同的规则拼接 /models 路径：
// baseURL 以 /chat/completions 结尾时回到其上一级再拼，否则直接追加。
func modelsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	return base + "/models"
}

// ListModels 请求 {baseURL}/models 并返回排序去重后的模型 ID 列表。
func ListModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("openai: create models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: request models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("openai: read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			return nil, fmt.Errorf("openai: models HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai: models HTTP %d: %s", resp.StatusCode, msg)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openai: parse models response: %w", err)
	}

	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}

// ModelCache 是并发安全的模型列表内存缓存，按 key（通常为 baseURL）缓存。
type ModelCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]modelCacheEntry
}

type modelCacheEntry struct {
	models  []string
	expires time.Time
}

// NewModelCache 创建缓存，ttl <= 0 时使用默认的 10 分钟。
func NewModelCache(ttl time.Duration) *ModelCache {
	return newModelCacheWithClock(ttl, time.Now)
}

// newModelCacheWithClock 以指定时间源创建缓存，供测试注入。
func newModelCacheWithClock(ttl time.Duration, now func() time.Time) *ModelCache {
	if ttl <= 0 {
		ttl = defaultModelCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &ModelCache{
		ttl:     ttl,
		now:     now,
		entries: make(map[string]modelCacheEntry),
	}
}

// Get 返回 key 对应的模型列表：命中未过期缓存时直接返回副本，否则调用 fetch
// 拉取并写入缓存。fetch 为 nil 或返回错误时透传错误，且不污染缓存。
func (c *ModelCache) Get(ctx context.Context, key string, fetch func(context.Context) ([]string, error)) ([]string, error) {
	if fetch == nil {
		return nil, errors.New("openai: model cache fetch function is nil")
	}
	if c == nil {
		return fetch(ctx)
	}

	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && c.now().Before(entry.expires) {
		models := append([]string(nil), entry.models...)
		c.mu.Unlock()
		return models, nil
	}
	c.mu.Unlock()

	models, err := fetch(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[key] = modelCacheEntry{
		models:  append([]string(nil), models...),
		expires: c.now().Add(c.ttl),
	}
	c.mu.Unlock()

	return append([]string(nil), models...), nil
}
