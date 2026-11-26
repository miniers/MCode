package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/openartifact"
	"mcode/internal/openartifact/noderuntime"
	"mcode/internal/providers/loginenv"
	"mcode/internal/remote/wire"
)

const openArtifactCallTimeout = 30 * time.Second

type openArtifactState struct {
	mu             sync.Mutex
	bridge         *openartifact.Bridge
	startCancel    context.CancelFunc
	starting       bool
	closed         bool
	gen            uint64
	err            error
	skill          string
	toolNames      []string
	removeListener func()
}

type openArtifactHost struct {
	app *App
}

func (h openArtifactHost) DeliverWake(threadID, text string) error {
	if h.app == nil {
		return errors.New("openartifact: 应用实例不可用")
	}
	return h.app.deliverOpenArtifactWake(context.Background(), threadID, text)
}

func (h openArtifactHost) OpenURL(threadID, targetURL string) bool {
	if h.app == nil {
		return false
	}
	return h.app.openOpenArtifactURL(threadID, targetURL)
}

func (a *App) openArtifactRuntimesDir() string {
	if strings.TrimSpace(a.dataDir) == "" {
		return ""
	}
	return filepath.Join(a.dataDir, "runtimes")
}

// OpenArtifactNodeStatus 探测并返回可用于 open-artifact 的 Node.js 运行时状态。
func (a *App) OpenArtifactNodeStatus(ctx context.Context) noderuntime.Status {
	if ctx == nil {
		ctx = context.Background()
	}
	return noderuntime.Detect(ctx, a.openArtifactRuntimesDir())
}

// DownloadOpenArtifactNode 下载并校验托管的 Node.js LTS 运行时到数据目录。
func (a *App) DownloadOpenArtifactNode(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimesDir := a.openArtifactRuntimesDir()
	if runtimesDir == "" {
		return noderuntime.Status{}, errors.New("openartifact: 数据目录未配置")
	}
	return noderuntime.Download(ctx, runtimesDir, progress)
}

// SetOpenArtifactEnabled 保存 open-artifact 启用配置，并立即启动或关闭桥接。
func (a *App) SetOpenArtifactEnabled(enabled bool) error {
	if strings.TrimSpace(a.dataDir) == "" {
		return errors.New("openartifact: 数据目录未配置")
	}
	if err := config.Update(a.dataDir, func(cfg *config.Config) error {
		cfg.OpenArtifact.Enabled = enabled
		return nil
	}); err != nil {
		return err
	}
	if a.client != nil {
		a.client.notifyConfigChanged()
	}
	if enabled {
		a.startOpenArtifact()
	} else {
		a.stopOpenArtifact()
	}
	return nil
}

// OpenArtifactState 返回 open-artifact 桥接当前是否就绪以及最近一次启动错误。
func (a *App) OpenArtifactState() (ready bool, err error) {
	a.oa.mu.Lock()
	bridge := a.oa.bridge
	lastErr := a.oa.err
	a.oa.mu.Unlock()
	if bridge != nil && bridge.Ready() {
		return true, lastErr
	}
	return false, lastErr
}

// OpenArtifactHomeURL 返回当前已连接 open-artifact daemon 的主页地址。
func (a *App) OpenArtifactHomeURL(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.oa.mu.Lock()
	bridge := a.oa.bridge
	a.oa.mu.Unlock()
	if bridge == nil || !bridge.Ready() {
		return "", openartifact.ErrNotReady
	}
	return bridge.HomeURL(ctx)
}

// OpenArtifactSkill 返回当前缓存的 open-artifact 入口 skill 正文；未就绪时返回空串。
func (a *App) OpenArtifactSkill() string {
	a.oa.mu.Lock()
	defer a.oa.mu.Unlock()
	if a.oa.closed {
		return ""
	}
	return a.oa.skill
}

// OpenArtifactTypes 解析并返回当前已安装的 open-artifact 交付物类型列表；未就绪时返回空切片。
func (a *App) OpenArtifactTypes() []openartifact.ArtifactType {
	if a == nil {
		return nil
	}
	return openartifact.ParseInstalledTypes(a.OpenArtifactSkill())
}

func (a *App) startOpenArtifact() {
	a.oa.mu.Lock()
	if a.oa.closed {
		a.oa.mu.Unlock()
		return
	}
	if a.oa.starting || (a.oa.bridge != nil && a.oa.bridge.Ready()) {
		a.oa.mu.Unlock()
		return
	}
	if a.oa.startCancel != nil {
		a.oa.startCancel()
	}
	a.oa.gen++
	gen := a.oa.gen
	ctx, cancel := context.WithCancel(context.Background())
	a.oa.startCancel = cancel
	a.oa.starting = true
	a.oa.err = nil
	runtimesDir := a.openArtifactRuntimesDir()
	a.oa.mu.Unlock()

	go a.runOpenArtifactStart(ctx, cancel, gen, runtimesDir)
}

func (a *App) runOpenArtifactStart(ctx context.Context, cancel context.CancelFunc, gen uint64, runtimesDir string) {
	defer cancel()

	st := noderuntime.Detect(ctx, runtimesDir)
	if ctx.Err() != nil {
		a.finishOpenArtifactStartErr(gen, nil, nil)
		return
	}
	if !st.OK {
		err := fmt.Errorf("openartifact: 未找到可用的 Node.js（需要 >= %s）", noderuntime.MinVersion)
		log.Printf("%v", err)
		a.finishOpenArtifactStartErr(gen, nil, err)
		return
	}

	bridge := openartifact.New(openartifact.Options{
		NodeBinDir: noderuntime.BinDir(st),
		Env:        loginenv.Environ(),
		Host:       openArtifactHost{app: a},
		Logf:       log.Printf,
	})

	a.oa.mu.Lock()
	if a.oa.gen != gen || a.oa.closed || ctx.Err() != nil {
		a.oa.starting = false
		a.oa.mu.Unlock()
		return
	}
	a.oa.bridge = bridge
	a.oa.mu.Unlock()

	if err := bridge.Start(ctx); err != nil {
		log.Printf("openartifact: 启动桥接失败: %v", err)
		_ = bridge.Close()
		a.finishOpenArtifactStartErr(gen, bridge, err)
		return
	}

	listCtx, listCancel := context.WithTimeout(ctx, openArtifactCallTimeout)
	tools, err := bridge.ListTools(listCtx)
	listCancel()
	if err != nil {
		log.Printf("openartifact: 获取工具列表失败: %v", err)
		_ = bridge.Close()
		a.finishOpenArtifactStartErr(gen, bridge, err)
		return
	}

	skillCtx, skillCancel := context.WithTimeout(ctx, openArtifactCallTimeout)
	skillText, skillErr := bridge.Skill(skillCtx)
	skillCancel()
	if skillErr != nil {
		log.Printf("openartifact: 获取 skill 失败: %v", skillErr)
	}

	a.applyOpenArtifactStartResult(gen, ctx.Err(), bridge, tools, skillText, skillErr)
}

func (a *App) finishOpenArtifactStartErr(gen uint64, bridge *openartifact.Bridge, err error) {
	a.oa.mu.Lock()
	defer a.oa.mu.Unlock()
	if a.oa.gen != gen {
		return
	}
	if bridge != nil && a.oa.bridge == bridge {
		a.oa.bridge = nil
	}
	if !a.oa.closed && err != nil {
		a.oa.err = err
	}
	a.oa.starting = false
}

func (a *App) applyOpenArtifactStartResult(gen uint64, ctxErr error, bridge *openartifact.Bridge, tools []openartifact.ToolDefinition, skillText string, skillErr error) {
	a.oa.mu.Lock()
	if a.oa.gen != gen || a.oa.closed || ctxErr != nil {
		if bridge != nil && a.oa.bridge == bridge {
			a.oa.bridge = nil
		}
		a.oa.starting = false
		a.oa.mu.Unlock()
		if bridge != nil {
			_ = bridge.Close()
		}
		return
	}

	if bridge != nil {
		a.oa.bridge = bridge
	}
	a.oa.toolNames = a.registerOpenArtifactToolsLocked(bridge, tools)
	if skillErr == nil {
		a.oa.skill = skillText
	} else {
		a.oa.err = skillErr
	}
	a.oa.starting = false
	a.oa.mu.Unlock()
}

func (a *App) registerOpenArtifactToolsLocked(bridge *openartifact.Bridge, tools []openartifact.ToolDefinition) []string {
	if a.mcp == nil {
		return nil
	}
	var call mcp.OpenArtifactCallFunc
	if bridge != nil {
		call = func(ctx context.Context, threadID, cwd, name string, args json.RawMessage) (json.RawMessage, error) {
			return bridge.CallTool(ctx, threadID, cwd, name, args)
		}
	}
	names := make([]string, 0, len(tools))
	for _, def := range tools {
		if strings.TrimSpace(def.Name) == "" {
			continue
		}
		a.mcp.RegisterTool(mcp.OpenArtifactToolDefinition(def, call))
		names = append(names, def.Name)
	}
	return names
}

func (a *App) stopOpenArtifact() {
	a.stopOpenArtifactInternal(false)
}

func (a *App) closeOpenArtifact() {
	a.stopOpenArtifactInternal(true)
}

func (a *App) stopOpenArtifactInternal(closing bool) {
	a.oa.mu.Lock()
	if closing {
		a.oa.closed = true
	}
	a.oa.gen++
	cancel := a.oa.startCancel
	a.oa.startCancel = nil
	a.oa.starting = false
	bridge := a.oa.bridge
	a.oa.bridge = nil
	a.oa.err = nil
	a.oa.skill = ""
	toolNames := a.oa.toolNames
	a.oa.toolNames = nil
	var removeListener func()
	if closing {
		removeListener = a.oa.removeListener
		a.oa.removeListener = nil
	}
	a.oa.mu.Unlock()

	if removeListener != nil {
		removeListener()
	}
	if cancel != nil {
		cancel()
	}
	if a.mcp != nil {
		for _, name := range toolNames {
			a.mcp.UnregisterTool(name)
		}
	}
	if bridge != nil {
		_ = bridge.Close()
	}
}

func (a *App) deliverOpenArtifactWake(ctx context.Context, threadID, text string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.openArtifactDeliverWake != nil {
		return a.openArtifactDeliverWake(ctx, threadID, text)
	}
	if a.backend == nil {
		return errors.New("openartifact: 后端不可用")
	}
	return a.backend.SendHostEvent(ctx, threadID, text)
}

func (a *App) openOpenArtifactURL(threadID, targetURL string) bool {
	fn := a.OpenPreviewNewTab
	if fn == nil {
		return false
	}
	if err := fn(threadID, targetURL); err != nil {
		log.Printf("openartifact: 打开预览标签页失败 (%s): %v", threadID, err)
		return false
	}
	return true
}

func (a *App) handleOpenArtifactThreadsChanged(tc wire.ThreadsChanged) {
	seen := make(map[string]bool)
	for _, th := range tc.Threads {
		if th.IsArchived && th.ID != "" && !seen[th.ID] {
			seen[th.ID] = true
			a.detachOpenArtifactThread(th.ID)
		}
	}
	for _, id := range tc.RemovedThreadIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			a.detachOpenArtifactThread(id)
		}
	}
}

func (a *App) detachOpenArtifactThread(threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	a.oa.mu.Lock()
	bridge := a.oa.bridge
	closed := a.oa.closed
	a.oa.mu.Unlock()
	if closed || bridge == nil || !bridge.Ready() {
		return
	}
	go func(b *openartifact.Bridge, tid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.Detach(ctx, tid); err != nil && !errors.Is(err, openartifact.ErrNotReady) {
			log.Printf("openartifact: detach 会话失败 (%s): %v", tid, err)
		}
	}(bridge, threadID)
}
