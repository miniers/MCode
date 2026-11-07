package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// BeginStatus 表示开始幂等调用的检查状态。
type BeginStatus int

const (
	BeginProceed BeginStatus = iota
	BeginReplay
	BeginConflict
	BeginInFlight
)

type idempEntry struct {
	fingerprint string
	result      *ToolResult
	expiresAt   time.Time
}

// IdempotencyCache 提供内存幂等缓存。
type IdempotencyCache struct {
	mu       sync.Mutex
	entries  map[string]*idempEntry
	order    []string
	capacity int
	ttl      time.Duration
}

// NewIdempotencyCache 创建指定容量与过期时间的幂等缓存实例。
func NewIdempotencyCache(capacity int, ttl time.Duration) *IdempotencyCache {
	if capacity <= 0 {
		capacity = 256
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &IdempotencyCache{
		entries:  make(map[string]*idempEntry),
		order:    make([]string, 0, capacity),
		capacity: capacity,
		ttl:      ttl,
	}
}

// Key 生成缓存键：projectID + "\x1f" + toolName + "\x1f" + requestID。
func (c *IdempotencyCache) Key(projectID, toolName, requestID string) string {
	return projectID + "\x1f" + toolName + "\x1f" + requestID
}

// Fingerprint 计算入参规范化指纹，排除指定的幂等键字段。
func Fingerprint(arguments map[string]any, excluding ...string) string {
	exSet := make(map[string]bool, len(excluding))
	for _, k := range excluding {
		exSet[k] = true
	}
	filtered := make(map[string]any)
	for k, v := range arguments {
		if !exSet[k] {
			filtered[k] = v
		}
	}
	keys := make([]string, 0, len(filtered))
	for k := range filtered {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	canonical := make(map[string]any, len(filtered))
	for _, k := range keys {
		canonical[k] = filtered[k]
	}
	b, _ := json.Marshal(canonical)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Begin 尝试开始幂等执行。
func (c *IdempotencyCache) Begin(key, fingerprint string) (BeginStatus, *ToolResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if entry, ok := c.entries[key]; ok {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
			c.removeFromOrder(key)
		} else {
			if entry.fingerprint != fingerprint {
				return BeginConflict, nil
			}
			if entry.result == nil {
				return BeginInFlight, nil
			}
			return BeginReplay, entry.result
		}
	}

	c.entries[key] = &idempEntry{
		fingerprint: fingerprint,
		result:      nil,
		expiresAt:   now.Add(c.ttl),
	}
	c.order = append(c.order, key)
	c.evictIfNeeded()
	return BeginProceed, nil
}

// Finish 保存执行完成的有效结果。
func (c *IdempotencyCache) Finish(key string, result ToolResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[key]; ok {
		if entry.result == nil {
			entry.result = &result
		}
	}
}

// Abandon 放弃未完成的幂等请求，允许重试。
func (c *IdempotencyCache) Abandon(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[key]; ok && entry.result == nil {
		delete(c.entries, key)
		c.removeFromOrder(key)
	}
}

func (c *IdempotencyCache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *IdempotencyCache) evictIfNeeded() {
	for len(c.order) > c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func extractIdempotencyKey(args map[string]any) string {
	for _, field := range []string{"request_id", "requestId", "idempotency_key"} {
		if val, ok := args[field]; ok {
			if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
