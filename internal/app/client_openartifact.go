package app

import (
	"context"
	"sync"

	"mcode/internal/openartifact"
	"mcode/internal/openartifact/noderuntime"
	mcodeui "mcode/internal/ui"
)

// openArtifactProvider 提供 open-artifact 运行时管理与状态查询能力，由 App 实现。
type openArtifactProvider interface {
	OpenArtifactNodeStatus(ctx context.Context) noderuntime.Status
	DownloadOpenArtifactNode(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error)
	SetOpenArtifactEnabled(enabled bool) error
	OpenArtifactState() (ready bool, err error)
	OpenArtifactHomeURL(ctx context.Context) (string, error)
	OpenArtifactTypes() []openartifact.ArtifactType
}

var _ mcodeui.OpenArtifactClient = (*Client)(nil)

var (
	clientOpenArtifactMu sync.RWMutex
	clientOpenArtifacts  = make(map[*Client]openArtifactProvider)
)

// SetOpenArtifact 注入提供 open-artifact 能力的实例；传入 nil 可清除注入。
func (c *Client) SetOpenArtifact(provider openArtifactProvider) {
	if c == nil {
		return
	}
	clientOpenArtifactMu.Lock()
	if provider == nil {
		delete(clientOpenArtifacts, c)
	} else {
		clientOpenArtifacts[c] = provider
	}
	clientOpenArtifactMu.Unlock()
}

func (c *Client) openArtifactService() openArtifactProvider {
	if c == nil {
		return nil
	}
	clientOpenArtifactMu.RLock()
	provider := clientOpenArtifacts[c]
	clientOpenArtifactMu.RUnlock()
	return provider
}

// OpenArtifactNodeStatus 探测并返回可用于 open-artifact 的 Node.js 运行时状态。
func (c *Client) OpenArtifactNodeStatus(ctx context.Context) noderuntime.Status {
	provider := c.openArtifactService()
	if provider == nil {
		return noderuntime.Status{}
	}
	return provider.OpenArtifactNodeStatus(ctx)
}

// DownloadOpenArtifactNode 下载并校验托管的 Node.js 运行时。
func (c *Client) DownloadOpenArtifactNode(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error) {
	provider := c.openArtifactService()
	if provider == nil {
		return noderuntime.Status{}, openartifact.ErrNotReady
	}
	return provider.DownloadOpenArtifactNode(ctx, progress)
}

// SetOpenArtifactEnabled 保存 open-artifact 启用配置并启动或关闭桥接。
func (c *Client) SetOpenArtifactEnabled(enabled bool) error {
	provider := c.openArtifactService()
	if provider == nil {
		return openartifact.ErrNotReady
	}
	return provider.SetOpenArtifactEnabled(enabled)
}

// OpenArtifactState 返回 open-artifact 桥接当前是否就绪及最近一次错误。
func (c *Client) OpenArtifactState() (ready bool, err error) {
	provider := c.openArtifactService()
	if provider == nil {
		return false, openartifact.ErrNotReady
	}
	return provider.OpenArtifactState()
}

// OpenArtifactHomeURL 返回已连接 open-artifact daemon 的主页地址。
func (c *Client) OpenArtifactHomeURL(ctx context.Context) (string, error) {
	provider := c.openArtifactService()
	if provider == nil {
		return "", openartifact.ErrNotReady
	}
	return provider.OpenArtifactHomeURL(ctx)
}

// OpenArtifactTypes 返回当前已安装的 open-artifact 交付物类型列表。
func (c *Client) OpenArtifactTypes() []openartifact.ArtifactType {
	provider := c.openArtifactService()
	if provider == nil {
		return nil
	}
	return provider.OpenArtifactTypes()
}
