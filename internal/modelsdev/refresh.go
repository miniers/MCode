package modelsdev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/fsutil"
)

const (
	// APIURL 是 models.dev 的完整目录地址。
	APIURL = "https://models.dev/api.json"
	// cacheName 是数据目录下的缓存文件名，内容为裁剪后的目录。
	cacheName = "models_dev_cache.json"
	// etagName 保存上次响应的 ETag；再次请求带上它，未变化时服务端返回 304 且无响应体。
	etagName = "models_dev_cache.etag"
	// refreshInterval 内的缓存视为新鲜，启动时不再联网。
	refreshInterval = 24 * time.Hour
	// maxBody 限制下载体积，防止异常响应占满内存。
	maxBody = 64 << 20
)

// LoadCache 读取数据目录下的缓存并叠加到目录上；缓存缺失或损坏时保持原数据。
func (c *Catalog) LoadCache(dataDir string) {
	data, err := os.ReadFile(filepath.Join(dataDir, cacheName))
	if err != nil {
		return
	}
	c.load(data)
}

// Refresh 从 url 拉取目录，成功后更新内存并把裁剪结果写入缓存；任何失败都保持现有数据。
func (c *Catalog) Refresh(ctx context.Context, client *http.Client, url, dataDir string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mcode")
	cachePath := filepath.Join(dataDir, cacheName)
	// 仅当缓存文件仍在时才带 ETag，避免 304 后没有数据可用。
	if dataDir != "" {
		if _, err := os.Stat(cachePath); err == nil {
			if etag, err := os.ReadFile(filepath.Join(dataDir, etagName)); err == nil && len(etag) > 0 {
				req.Header.Set("If-None-Match", string(etag))
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		// 数据未变：刷新缓存时间戳即可，下次启动在间隔内不再联网。
		now := time.Now()
		return os.Chtimes(cachePath, now, now)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("models.dev: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	parsed, err := parse(data)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return fmt.Errorf("models.dev: payload has no supported providers")
	}
	trimmed, err := marshalTrimmed(parsed)
	if err != nil {
		return err
	}
	if !c.load(trimmed) {
		return fmt.Errorf("models.dev: payload not usable")
	}
	if dataDir == "" {
		return nil
	}
	if err := writeFileAtomic(cachePath, trimmed); err != nil {
		return err
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		return writeFileAtomic(filepath.Join(dataDir, etagName), []byte(etag))
	}
	return nil
}

func marshalTrimmed(parsed map[string]map[string]rawModel) ([]byte, error) {
	out := map[string]map[string]map[string]rawModel{}
	for p, models := range parsed {
		out[p] = map[string]map[string]rawModel{"models": models}
	}
	return json.Marshal(out)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// cacheFresh 报告缓存是否在刷新间隔内。
func cacheFresh(dataDir string) bool {
	st, err := os.Stat(filepath.Join(dataDir, cacheName))
	return err == nil && time.Since(st.ModTime()) < refreshInterval
}

// Default 是进程级目录，供 internal/remote 的 ProviderModels 等无状态入口使用。
var Default = New()

// Start 加载缓存，并在缓存过期或缺失时于后台刷新；失败只影响本次刷新，不影响已加载的数据。
func Start(dataDir string) {
	if dataDir == "" {
		return
	}
	Default.LoadCache(dataDir)
	if cacheFresh(dataDir) {
		return
	}
	go func() {
		_ = Default.Refresh(context.Background(), http.DefaultClient, APIURL, dataDir)
	}()
}
