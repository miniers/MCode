// Package modelcache 保存 CLI 返回的模型目录，并支持手动刷新。
package modelcache

import (
	"context"
	"errors"
	"sync"
	"time"
)

const retryDelay = 10 * time.Minute

// ErrNotInstalled 表示探测时找不到对应的 CLI 可执行文件。
var ErrNotInstalled = errors.New("CLI 未安装")

// Snapshot 是一次探测状态：Models 是上次成功的目录（从未成功为 nil），
// Err 是最近一次失败原因（成功后清空），Probing 表示后台探测正在跑。
type Snapshot[T any] struct {
	Models  []T
	Err     error
	Probing bool
}

type Cache[T any] struct {
	mu       sync.Mutex
	models   []T
	lastErr  error
	failedAt time.Time
	fetching bool
	gen      uint64
}

// Peek 立即返回缓存与最近一次探测状态；首次读取或失败重试时在后台拉取。
func (c *Cache[T]) Peek(fetch func(context.Context) ([]T, error)) Snapshot[T] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) == 0 && !c.fetching && (c.failedAt.IsZero() || time.Since(c.failedAt) >= retryDelay) {
		c.fetching = true
		go c.fetch(context.Background(), c.gen, fetch)
	}
	return Snapshot[T]{
		Models:  append([]T(nil), c.models...),
		Err:     c.lastErr,
		Probing: c.fetching,
	}
}

// Get 立即返回缓存；首次读取或失败重试时在后台拉取。
func (c *Cache[T]) Get(fetch func(context.Context) ([]T, error)) []T {
	return c.Peek(fetch).Models
}

// Refresh 强制重新拉取；失败时保留已有目录。
func (c *Cache[T]) Refresh(ctx context.Context, fetch func(context.Context) ([]T, error)) error {
	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.fetching = true
	c.mu.Unlock()
	return c.fetch(ctx, gen, fetch)
}

func (c *Cache[T]) fetch(ctx context.Context, gen uint64, fetch func(context.Context) ([]T, error)) error {
	models, err := fetch(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && len(models) == 0 {
		err = errors.New("模型目录为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return errors.New("刷新期间模型目录已变更")
	}
	c.fetching = false
	c.lastErr = err
	if err != nil {
		c.failedAt = time.Now()
	} else {
		c.models, c.failedAt = append([]T(nil), models...), time.Time{}
	}
	return err
}
