// Package modelcache 保存 CLI 返回的模型目录，并支持手动刷新。
package modelcache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mcode/internal/fsutil"
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
	File      string
	mu        sync.Mutex
	models    []T
	lastErr   error
	failedAt  time.Time
	fetching  bool
	gen       uint64
	loaded    bool
	attempted bool
}

// DefaultFile 返回模型目录的默认缓存路径。
func DefaultFile(name string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mcode", "models", name+".json")
}

// Peek 立即返回缓存与最近一次探测状态；首次读取或失败重试时在后台拉取。
func (c *Cache[T]) Peek(fetch func(context.Context) ([]T, error)) Snapshot[T] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		c.loaded = true
		if c.File != "" && len(c.models) == 0 {
			if data, err := os.ReadFile(c.File); err == nil {
				var models []T
				if json.Unmarshal(data, &models) == nil {
					c.models = models
				}
			}
		}
	}
	if (len(c.models) == 0 || (c.File != "" && !c.attempted)) && !c.fetching && (c.failedAt.IsZero() || time.Since(c.failedAt) >= retryDelay) {
		c.fetching = true
		c.attempted = true
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
	c.attempted = true
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
		if c.File != "" {
			data, writeErr := json.Marshal(models)
			if writeErr == nil {
				writeErr = os.MkdirAll(filepath.Dir(c.File), 0o700)
			}
			if writeErr == nil {
				writeErr = fsutil.WriteFileAtomic(c.File, data, 0o600)
			}
			if writeErr != nil {
				log.Printf("modelcache: 写入缓存失败: %v", writeErr)
			}
		}
	}
	return err
}
