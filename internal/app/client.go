package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/attention"
	"mcode/internal/automation"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/kanban"
	"mcode/internal/localmem"
	"mcode/internal/mcp"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/procattr"
	"mcode/internal/projctx"
	"mcode/internal/providers/acp"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/cliupdate"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/copilot"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/magpie"
	"mcode/internal/providers/multiaccount"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/textgen"
	mcodeui "mcode/internal/ui"
)

// Client 实现 mcodeui.Client 接口，基于进程内的 remote.MemoryBackend 进行适配。
type Client struct {
	updateChecker  *UpdateChecker
	backend        *remote.MemoryBackend
	git            *git.Runner
	oauth          *mcp.OAuthManager
	agy            *accounts.Manager
	agyLoginMu     sync.Mutex
	agyLogin       *accounts.LoginSession
	contextWriteMu sync.Mutex

	// memoryMu 保护记忆面板的懒加载资源：仅在无后端 store 时自行打开的第二个 store 连接，
	// 以及 Nowledge Mem 客户端，两者都只建一次。
	memoryMu    sync.Mutex
	memoryStore *store.Store
	memoryNmem  *nmem.Client

	// agySessionForThread 把界面会话 ID 解析成 agy 的 provider 会话 ID（路由绑定的键），
	// 由装配层注入；未注入时额度弹窗退回「新会话目标账号」。
	agySessionForThread func(ctx context.Context, threadID string) string

	// quotaTracker 是「会话 → 实际应答账号」的进程内映射，懒创建；监听只对本机
	// Magpie 端点启动，测试里可替换 tracker 的 readTrace。
	quotaTrackerOnce sync.Once
	quotaTracker     *sessionAccountTracker

	kanban             *kanban.Service
	automation         *automation.Service
	swarm              *swarm.Manager
	swarmPipeline      *swarm.Pipeline
	remote             *RemoteService
	kanbanMu           sync.Mutex
	nextWatchID        int
	kanbanWatchers     map[int]func()
	automationWatchers map[int]func()
	swarmWatchers      map[int]func(string)
	// configWatchers 是配置变更订阅者（设置页刷新、读配置缓存失效）。
	// 与上面的 watchers 共用 kanbanMu 与 nextWatchID。
	configWatchers map[int]func()
	// swarmNotifyTimers 是 Swarm 变更通知的合并窗口定时器，键为主会话 ID。
	swarmNotifyTimers map[string]*time.Timer
	// swarmBeeProgress 缓存已结束 bee 的进度数据，避免每次拉取状态都重新查一遍每个 bee
	// 的会话、用量与完整历史（开销随累计 bee 总数线性增长）。
	swarmBeeProgress map[string]swarmBeeProgress

	// forwardedQuestions 记录已转交主线程回答的子线程提问，键为 beeThreadID+"/"+requestID。
	forwardedMu        sync.Mutex
	forwardedQuestions map[string]bool
	// questionFirstSeen 记录提问首次出现的时间，键同 forwardedQuestions。
	questionFirstSeen map[string]time.Time
	// questionAutoForwardFailed 记录自动转交失败的提问，键同 forwardedQuestions，避免同一提问反复重试。
	questionAutoForwardFailed map[string]bool
	autoForwardMu             sync.Mutex
	autoForwardCancel         context.CancelFunc

	providersMu sync.Mutex
	dataDir     string
	secrets     config.SecretStore
	httpClient  *http.Client
	// mcp 是运行时重建 Provider harness 时复用的 MCP 注入配置。
	mcp providerMCP

	// gitMu 保护 git 与下面两个字段：git 会被多处并发取用，不加锁会各建一份执行器，
	// 把进程内的 git 并发上限从 4 放大到 8。gitCfgMod/gitCfgSize 记录上次回推子模块开关时
	// 配置文件的修改时间与大小，用来判断设置页是否改过（改过才重读配置）。
	gitMu      sync.Mutex
	gitCfgMod  time.Time
	gitCfgSize int64

	attention *attention.Service

	// foregroundMu 保护 foregroundThread：界面每帧上报当前前台会话，总管读取它判断该会话是否正被人看着。
	foregroundMu     sync.Mutex
	foregroundThread string
}

var _ mcodeui.Client = (*Client)(nil)

// 文件树写操作是可选能力，单独断言，避免扩大 mcodeui.Client 接口影响其他实现。
var _ mcodeui.FileOpsClient = (*Client)(nil)

// 包级函数变量，便于测试替换。
var (
	readProviderCredits     = credits.Read
	readProviderPlanWindows = credits.ReadPlanWindows
	// readCopilotAccount 读取 Copilot CLI 当前登录的账户，测试中替换为桩，避免启动真实 CLI。
	readCopilotAccount = copilot.ReadAccountFromCLI
	// readCodexQuotaLimits 与路由读取函数可替换，避免额度测试启动真实 Codex。
	readCodexQuotaLimits = func(c *Client, ctx context.Context, threadID string) (codex.RateLimits, error) {
		return c.codexQuotaLimits(ctx, threadID)
	}
	codexMagpieRoutesForQuota = codexMagpieRoutes
	readCodexThreadModel      = func(ctx context.Context, backend *remote.MemoryBackend, threadID string) (string, error) {
		if backend == nil {
			return "", nil
		}
		thread, err := backend.GetThread(ctx, threadID)
		if err != nil {
			return "", err
		}
		return derefString(thread.Model), nil
	}
)

// NewClient 创建并返回一个新的 Client 实例。
func NewClient(b *remote.MemoryBackend) *Client {
	return &Client{
		backend:           b,
		kanbanWatchers:    make(map[int]func()),
		swarmWatchers:     make(map[int]func(string)),
		swarmNotifyTimers: make(map[string]*time.Timer),
		swarmBeeProgress:  make(map[string]swarmBeeProgress),
	}
}

// swarmNotifyDelay 是 Swarm 变更通知的合并窗口：同一主会话在窗口内的多次变更只向监听者
// 推送一次，推的仍是窗口结束时的最新状态。子代理多时状态变更很密集，逐次推送会让界面
// 每次变更都拉一次全量状态，拖慢会话消息流。
const swarmNotifyDelay = 150 * time.Millisecond

// swarmBeeProgress 是已结束 bee 的进度快照，字段含义与响应里的对应字段一致。
type swarmBeeProgress struct {
	provider  string
	model     string
	activity  string
	tokens    int
	toolCalls int
}

// SetMCPOAuth 设置 MCP OAuth 管理器。
func (c *Client) SetMCPOAuth(m *mcp.OAuthManager) {
	c.oauth = m
}

// SetGit 设置 Git 执行器，并把「新建工作树时初始化子模块」开关同步给 git 包。
func (c *Client) SetGit(r *git.Runner) {
	c.gitMu.Lock()
	c.git = r
	c.gitMu.Unlock()
	c.syncGitSubmodulesConfig(true)
}

func (c *Client) effectiveDataDir() string {
	if c.dataDir != "" {
		return c.dataDir
	}
	if c.remote != nil && c.remote.dataDir != "" {
		return c.remote.dataDir
	}
	return ""
}

func (c *Client) loadConfig() (config.Config, bool) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return config.Config{}, false
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return config.Config{}, false
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg, true
}

// syncGitSubmodulesConfig 把配置里的「新建工作树时初始化子模块」推给 git 包的进程级全局。
// 设置页直接写 config.json，不通知 app，所以这里按配置文件的时间戳判断是否变过：
// 没变就直接返回（取执行器是热路径，不能每次都解析一遍配置），变了才重读并回推，
// 避免改完开关必须重启进程才生效。force 为 true 时无条件回推（装配阶段）。
func (c *Client) syncGitSubmodulesConfig(force bool) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return
	}
	var mod time.Time
	var size int64
	if st, err := os.Stat(config.ConfigPath(dir)); err == nil {
		mod, size = st.ModTime(), st.Size()
	}
	c.gitMu.Lock()
	unchanged := !force && mod.Equal(c.gitCfgMod) && size == c.gitCfgSize
	c.gitCfgMod, c.gitCfgSize = mod, size
	c.gitMu.Unlock()
	if unchanged {
		return
	}
	if cfg, ok := c.loadConfig(); ok {
		git.SetInitSubmodules(cfg.SourceControl.InitSubmodulesEnabled())
		git.SetCloneIgnored(cfg.SourceControl.CloneIgnoredOnWorktreeEnabled(), cfg.SourceControl.CloneIgnoredPatternList())
	}
}

// gitRunner 返回 Git 执行器：装配处注入的实例优先，没有再懒建一个，两者都只建一次。
func (c *Client) gitRunner() *git.Runner {
	c.gitMu.Lock()
	if c.git == nil {
		c.git = git.NewRunner("", 4)
	}
	r := c.git
	c.gitMu.Unlock()
	c.syncGitSubmodulesConfig(false)
	return r
}

// newUUID 使用 crypto/rand 生成标准 UUID v4 字符串。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x-%04x-4000-8000-%012x", time.Now().UnixNano(), 0, 0)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}

// Projects 调用 ListProjects 获取已登记的项目列表。
func (c *Client) Projects(ctx context.Context) ([]mcodeui.Project, error) {
	res, err := c.backend.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	return ProjectsSummaryToUI(res.Projects), nil
}

// AddProject 调用 AddProjectPath 添加指定本地目录的项目。
func (c *Client) AddProject(ctx context.Context, path string) (mcodeui.Project, error) {
	p, err := c.backend.AddProjectPath(ctx, path)
	if err != nil {
		return mcodeui.Project{}, err
	}
	return ProjectSummaryToUI(p), nil
}

// Threads 调用 ListThreads 并按 projectID 过滤，通过循环分页取完全部会话。
func (c *Client) Threads(ctx context.Context, projectID string) ([]mcodeui.Thread, error) {
	var all []wire.ThreadSummary
	var cursor *string
	limit := 100
	for {
		params := wire.ThreadListParams{
			Cursor: cursor,
			Limit:  &limit,
		}
		if projectID != "" {
			params.ProjectID = &projectID
		}
		res, err := c.backend.ListThreads(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, t := range res.Threads {
			if projectID == "" || t.ProjectID == projectID {
				all = append(all, t)
			}
		}
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return ThreadsSummaryToUI(all), nil
}

// ArchivedThreads 调用 ListThreads 并查询所有已归档的会话列表，通过循环分页取完全部并按更新时间倒序返回。
func (c *Client) ArchivedThreads(ctx context.Context) ([]mcodeui.Thread, error) {
	if c.backend == nil {
		return nil, nil
	}
	var all []wire.ThreadSummary
	var cursor *string
	limit := 100
	incArchived := true
	for {
		params := wire.ThreadListParams{
			Cursor:          cursor,
			Limit:           &limit,
			IncludeArchived: &incArchived,
		}
		res, err := c.backend.ListThreads(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, t := range res.Threads {
			if t.IsArchived {
				all = append(all, t)
			}
		}
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return ThreadsSummaryToUI(all), nil
}

// History 调用 ThreadHistory，循环分页取完全部历史消息，并按时间正序返回。
func (c *Client) History(ctx context.Context, threadID string) ([]mcodeui.Message, error) {
	var pages [][]wire.ThreadMessage
	var cursor *string
	limit := 100
	for {
		res, err := c.backend.ThreadHistory(ctx, wire.HistoryParams{
			ThreadID: threadID,
			Cursor:   cursor,
			Limit:    &limit,
		})
		if err != nil {
			return nil, err
		}
		pages = append(pages, res.Messages)
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	var total int
	for _, p := range pages {
		total += len(p)
	}
	all := make([]mcodeui.Message, 0, total)
	// ThreadHistory 按时间反向翻页，pages[0] 为最新页，倒序遍历还原为时间正序
	for i := len(pages) - 1; i >= 0; i-- {
		for _, m := range pages[i] {
			all = append(all, ThreadMessageToUI(m))
		}
	}
	return all, nil
}

// Providers 调用 ListProviders 获取可用的 Agent 提供方列表。
func (c *Client) Providers(ctx context.Context) ([]mcodeui.ProviderInfo, error) {
	res, err := c.backend.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	return ProvidersWireToUI(res.Providers), nil
}

// RefreshProviders 强制刷新供应商模型目录，并返回包含成功结果的最新快照。
func (c *Client) RefreshProviders(ctx context.Context) ([]mcodeui.ProviderInfo, error) {
	refreshErr := c.backend.RefreshProviderModels(ctx)
	providers, err := c.Providers(ctx)
	if err != nil {
		return nil, err
	}
	return providers, refreshErr
}

// CreateThreadWithOptions 调用 CreateThread 创建新线程，actionID 使用 crypto/rand 生成 UUID v4。
// 仅对非空字段设置 wire.ComposerOptions 的 Provider、Model、Effort、SwarmBeesProvider、SwarmBeesModel、SwarmBeesEffort 指针，
// Fast 为 true 时设置 FastMode；编排开关总是显式下发，避免被服务端的记忆补齐覆盖。
func (c *Client) CreateThreadWithOptions(ctx context.Context, projectID string, opts mcodeui.NewThreadOptions) (string, error) {
	params := wire.ThreadCreateParams{
		ProjectID: projectID,
		ActionID:  newUUID(),
	}
	if opts.Provider != "" || opts.Model != "" || opts.Effort != "" || opts.Fast || opts.BeesProvider != "" || opts.BeesModel != "" || opts.BeesEffort != "" || opts.PairID != "" {
		cOpts := &wire.ComposerOptions{}
		if opts.Provider != "" {
			p := opts.Provider
			cOpts.Provider = &p
		}
		if opts.Model != "" {
			m := opts.Model
			cOpts.Model = &m
		}
		if opts.Effort != "" {
			e := opts.Effort
			cOpts.Effort = &e
		}
		if opts.Fast {
			f := true
			cOpts.FastMode = &f
		}
		if opts.BeesProvider != "" {
			hp := opts.BeesProvider
			cOpts.SwarmBeesProvider = &hp
		}
		if opts.BeesModel != "" {
			hm := opts.BeesModel
			cOpts.SwarmBeesModel = &hm
		}
		if opts.BeesEffort != "" {
			he := opts.BeesEffort
			cOpts.SwarmBeesEffort = &he
		}
		// 选中的配对随创建参数下发；空串表示不传，由服务端按模型推断。
		if opts.PairID != "" {
			pid := opts.PairID
			cOpts.SwarmPairID = &pid
		}
		params.Options = cOpts
	}

	// 桌面端的编排开关总是显式下发：null 会被服务端当成“没传”而用记忆补齐覆盖。
	if params.Options == nil {
		params.Options = &wire.ComposerOptions{}
	}
	swarmEnabled := opts.SwarmEnabled
	params.Options.SwarmEnabled = &swarmEnabled

	// 默认工作区模式 (DefaultWorkspaceMode): local 或 worktree
	if cfg, ok := c.loadConfig(); ok {
		mode := cfg.SourceControl.DefaultWorkspaceMode
		if mode != "" {
			if params.Options == nil {
				params.Options = &wire.ComposerOptions{}
			}
			if params.Options.WorkspaceMode == nil {
				params.Options.WorkspaceMode = &mode
			}
		}
	}

	if opts.Provisional {
		provisional := true
		params.Provisional = &provisional
	}

	res, err := c.backend.CreateThread(ctx, params)
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// CreateThread 调用 CreateThreadWithOptions 创建新线程，provider 为空时表示使用默认值。
func (c *Client) CreateThread(ctx context.Context, projectID, provider string) (string, error) {
	return c.CreateThreadWithOptions(ctx, projectID, mcodeui.NewThreadOptions{Provider: provider})
}

// Send 调用 SendMessage 发送消息，actionID 使用 crypto/rand 生成 UUID v4。
// delegated 为 true 表示消息被交给子线程处理，本会话不会开始新一轮。
func (c *Client) Send(ctx context.Context, threadID, text string) (delegated bool, err error) {
	res, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     text,
		ActionID: newUUID(),
	})
	if err != nil {
		return false, err
	}
	return res.DelegatedThreadID != nil, nil
}

// SendWithAttachments 调用 SendMessage 向指定会话发送附带附件的消息。
// delegated 为 true 表示消息被交给子线程处理，本会话不会开始新一轮。
func (c *Client) SendWithAttachments(ctx context.Context, threadID, text string, attachmentIDs []string) (delegated bool, err error) {
	res, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        text,
		ActionID:    newUUID(),
		Attachments: attachmentIDs,
	})
	if err != nil {
		return false, err
	}
	return res.DelegatedThreadID != nil, nil
}

// UploadAttachment 调用 AttachmentsUpload 上传单张图片附件，返回附件元数据。
func (c *Client) UploadAttachment(ctx context.Context, name, mimeType string, data []byte) (mcodeui.Attachment, error) {
	var mimePtr *string
	if mimeType != "" {
		m := mimeType
		mimePtr = &m
	}
	res, err := c.backend.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
		Name:     name,
		MimeType: mimePtr,
		Data:     base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return mcodeui.Attachment{}, err
	}
	return mcodeui.Attachment{
		ID:        res.AttachmentID,
		Name:      res.Name,
		MimeType:  res.MimeType,
		ByteCount: res.ByteCount,
	}, nil
}

// SearchFiles 在指定会话的工作区内模糊搜索文件和目录。
func (c *Client) SearchFiles(ctx context.Context, threadID, query string) ([]mcodeui.FileMatch, error) {
	result, err := c.SearchFileEntries(ctx, threadID, query)
	return result.Entries, err
}

// SearchFileEntries 在工作区内搜索，并返回后端的截断状态。
func (c *Client) SearchFileEntries(ctx context.Context, threadID, query string) (mcodeui.FileSearchResult, error) {
	res, err := c.backend.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    query,
	})
	if err != nil {
		return mcodeui.FileSearchResult{}, err
	}
	matches := make([]mcodeui.FileMatch, len(res.Entries))
	for i, entry := range res.Entries {
		matches[i] = mcodeui.FileMatch{
			Name:  entry.Name,
			Path:  entry.Path,
			IsDir: entry.Kind == wire.RemoteFileEntryKindDirectory,
		}
	}
	return mcodeui.FileSearchResult{Entries: matches, Truncated: res.Truncated}, nil
}

// FilesList 列出指定会话工作区内某个目录的单层条目。
func (c *Client) FilesList(ctx context.Context, threadID, subpath string) (mcodeui.FileListing, error) {
	res, err := c.backend.FilesList(ctx, wire.RemoteFilesListParams{
		ThreadID: threadID,
		Subpath:  subpath,
	})
	if err != nil {
		return mcodeui.FileListing{}, err
	}
	entries := make([]mcodeui.FileEntry, len(res.Entries))
	for i, entry := range res.Entries {
		entries[i] = mcodeui.FileEntry{
			Name:      entry.Name,
			Path:      entry.Path,
			IsDir:     entry.Kind == wire.RemoteFileEntryKindDirectory,
			IsSymlink: entry.Kind == wire.RemoteFileEntryKindSymlink,
			Size:      entry.Size,
			Ignored:   entry.Ignored,
			Protected: entry.Protected,
		}
	}
	return mcodeui.FileListing{
		Path:      res.Path,
		Entries:   entries,
		Truncated: res.Truncated,
	}, nil
}

// FilesRead 读取指定会话工作区内的文件分块。
func (c *Client) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (mcodeui.FileContent, error) {
	params := wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     path,
	}
	if offset != 0 {
		o := offset
		params.Offset = &o
	}
	if length > 0 {
		l := int(length)
		params.Length = &l
	}
	res, err := c.backend.FilesRead(ctx, params)
	if err != nil {
		return mcodeui.FileContent{}, err
	}
	content := mcodeui.FileContent{
		Path:      res.Path,
		Kind:      res.Kind,
		TotalSize: res.TotalBytes,
		HasMore:   res.Truncated,
	}
	if res.Text != nil {
		content.Text = *res.Text
	}
	if res.MimeType != nil {
		content.MIME = *res.MimeType
	}
	if res.NextOffset != nil {
		content.NextOffset = *res.NextOffset
	}
	if res.Base64 != nil {
		if data, decErr := base64.StdEncoding.DecodeString(*res.Base64); decErr == nil {
			content.Data = data
		}
	}
	return content, nil
}

// FilesCreate 在指定会话工作区内新建空文件或目录，返回相对工作区的路径。
func (c *Client) FilesCreate(ctx context.Context, threadID, path string, dir bool) (string, error) {
	res, err := c.backend.FilesCreate(ctx, wire.FilesCreateParams{
		ThreadID: threadID,
		Path:     path,
		Dir:      dir,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// FilesRename 重命名或移动工作区内的条目，返回新的相对路径。
func (c *Client) FilesRename(ctx context.Context, threadID, path, newPath string) (string, error) {
	res, err := c.backend.FilesRename(ctx, wire.FilesRenameParams{
		ThreadID: threadID,
		Path:     path,
		NewPath:  newPath,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// FilesTrash 把工作区内的条目移到系统废纸篓。
func (c *Client) FilesTrash(ctx context.Context, threadID, path string) error {
	_, err := c.backend.FilesTrash(ctx, wire.FilesTrashParams{
		ThreadID: threadID,
		Path:     path,
	})
	return err
}

// FilesDuplicate 在同一目录下复制普通文件，返回副本的相对路径。
func (c *Client) FilesDuplicate(ctx context.Context, threadID, path string) (string, error) {
	res, err := c.backend.FilesDuplicate(ctx, wire.FilesDuplicateParams{
		ThreadID: threadID,
		Path:     path,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// Commands 获取指定会话可用的斜杠命令列表（包含内置命令与技能）。
func (c *Client) Commands(ctx context.Context, threadID string) ([]mcodeui.SlashCommand, error) {
	var threadIDPtr *string
	if threadID != "" {
		t := threadID
		threadIDPtr = &t
	}
	res, err := c.backend.ThreadCommands(ctx, wire.ThreadsCommandsParams{
		ThreadID: threadIDPtr,
	})
	if err != nil {
		return nil, err
	}
	cmds := make([]mcodeui.SlashCommand, len(res.Commands))
	for i, cmd := range res.Commands {
		cmds[i] = mcodeui.SlashCommand{
			Name:      cmd.Name,
			Detail:    cmd.Detail,
			Kind:      cmd.Kind,
			IsBuiltIn: cmd.IsBuiltIn,
		}
	}
	return cmds, nil
}

// Interrupt 调用 InterruptCurrent 中断指定会话当前轮次回复，没有在运行时什么也不做。
// alreadyStopped 为 true 表示后端确认该会话本就没有在运行的轮次。
func (c *Client) Interrupt(ctx context.Context, threadID string) (alreadyStopped bool, err error) {
	res, err := c.backend.InterruptCurrent(ctx, threadID)
	if err != nil {
		return false, err
	}
	return res.State == wire.InterruptStateAlreadyStopped, nil
}

// KillRunningCommand 终止指定会话里正在运行的那条命令的子进程树，不中断本轮对话；
// messageID 用于定位该次工具调用的开始时间，为空时退回本轮开始时间。返回被杀掉的进程数。
func (c *Client) KillRunningCommand(ctx context.Context, threadID, messageID string) (killed int, err error) {
	if c.backend == nil {
		return 0, errors.New("backend 不可用")
	}
	return c.backend.KillRunningCommand(ctx, threadID, messageID)
}

// RunningCommandActivity 返回按工具消息 ID 归集的命令进程快照。
func (c *Client) RunningCommandActivity(ctx context.Context, threadID string) (map[string][]mcodeui.CommandProc, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	activity, err := c.backend.RunningCommandActivity(ctx, threadID)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]mcodeui.CommandProc, len(activity))
	for id, processes := range activity {
		result[id] = make([]mcodeui.CommandProc, len(processes))
		for i, p := range processes {
			result[id][i] = mcodeui.CommandProc{PID: p.PID, PPID: p.PPID, StartTime: p.StartTime, CPUTime: p.CPUTime, RSS: p.RSS, Command: p.Command}
		}
	}
	return result, nil
}

// BackgroundTaskLog 获取指定后台任务的日志。
func (c *Client) BackgroundTaskLog(ctx context.Context, threadID, messageID string) (string, bool, error) {
	if c.backend == nil {
		return "", false, errors.New("backend 不可用")
	}
	return c.backend.BackgroundTaskLog(ctx, threadID, messageID)
}

// StopBackgroundTask 停止指定后台任务。
func (c *Client) StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error) {
	if c.backend == nil {
		return false, errors.New("backend 不可用")
	}
	return c.backend.StopBackgroundTask(ctx, threadID, messageID)
}

// SplitSession 分割会话：在时间线末尾写一条分割线，从下一条消息起发给模型的上下文不再带
// 分割线以上的历史。已处于待分割状态时幂等成功。
func (c *Client) SplitSession(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	err := c.backend.RequestSplit(threadID)
	switch {
	case errors.Is(err, remote.ErrSplitRunning):
		return errors.New("这一轮还在进行，等它结束再分割")
	case errors.Is(err, remote.ErrSplitEmpty):
		return errors.New("会话还没有内容，不需要分割")
	}
	return err
}

// Approvals 调用 ListRequests 查询待处理的交互与审批请求。
func (c *Client) Approvals(ctx context.Context, threadID string) ([]mcodeui.Approval, error) {
	res, err := c.backend.ListRequests(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return RequestsWireToUI(res.Requests), nil
}

// Respond 调用 RespondRequest 答复审批请求，TurnID 为空时传 nil。
func (c *Client) Respond(ctx context.Context, a mcodeui.Approval, optionID string) error {
	var turnID *string
	if a.TurnID != "" {
		t := a.TurnID
		turnID = &t
	}
	var optID *string
	if optionID != "" {
		o := optionID
		optID = &o
	}
	_, err := c.backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  a.ThreadID,
		TurnID:    turnID,
		RunID:     a.RunID,
		RequestID: a.RequestID,
		OptionID:  optID,
	})
	return err
}

// RespondAnswers 答复提问或计划类请求：answers 键为题目 ID，值为所选或填写的内容；skip 为 true 表示跳过。
func (c *Client) RespondAnswers(ctx context.Context, a mcodeui.Approval, optionID string, answers map[string][]string, skip bool) error {
	var turnID *string
	if a.TurnID != "" {
		t := a.TurnID
		turnID = &t
	}
	var optID *string
	if optionID != "" {
		o := optionID
		optID = &o
	}
	params := wire.RequestsRespondParams{
		ThreadID:  a.ThreadID,
		TurnID:    turnID,
		RunID:     a.RunID,
		RequestID: a.RequestID,
		OptionID:  optID,
	}
	if len(answers) > 0 {
		raw, err := json.Marshal(answers)
		if err != nil {
			return err
		}
		params.Answers = raw
	}
	if skip {
		s := true
		params.Skip = &s
	}
	_, err := c.backend.RespondRequest(ctx, params)
	return err
}

// localConn 实现 remote.Conn 接口，供界面层进程内订阅。
type localConn struct {
	mu          sync.Mutex
	closed      bool
	threadID    string
	onUpdate    func(mcodeui.ThreadUpdate)
	onApprovals func([]mcodeui.Approval)
	// eventMu 串行化快照与事件回调；关闭连接不等待回调，避免与界面锁互相等待。
	eventMu         sync.Mutex
	runID           string
	lastSeq         int
	snapshotApplied bool
	pendingEvents   []wire.ThreadEvent
}

func (c *localConn) Info() remote.ClientInfo {
	return remote.ClientInfo{
		Authenticated: true,
		DeviceID:      "local-ui",
		Label:         "local-ui",
		Entry:         remote.EntryLAN,
	}
}

func (c *localConn) MarkAuthenticated(deviceID, label string) {}

func (c *localConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *localConn) Notify(method string, params any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	threadID := c.threadID
	onApprovals := c.onApprovals
	c.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		return nil
	}

	switch method {
	case "threads.event":
		var ev wire.ThreadEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil
		}
		if ev.ThreadID != threadID {
			return nil
		}
		c.eventMu.Lock()
		defer c.eventMu.Unlock()
		if !c.snapshotApplied {
			c.pendingEvents = append(c.pendingEvents, ev)
		} else {
			c.applyEventLocked(ev)
		}

	case "requests.changed":
		var reqChanged wire.RequestsChangedParams
		if err := json.Unmarshal(raw, &reqChanged); err != nil {
			return nil
		}
		if reqChanged.ThreadID != threadID {
			return nil
		}
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nil
		}
		if onApprovals != nil {
			onApprovals(RequestsWireToUI(reqChanged.Requests))
		}
	}

	return nil
}

// applyEventLocked 的水位比较与回调共用 eventMu，避免旧帧在新帧之后覆盖界面。
func (c *localConn) applyEventLocked(ev wire.ThreadEvent) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed || (ev.RunID == c.runID && ev.Sequence <= c.lastSeq) {
		return
	}
	c.runID, c.lastSeq = ev.RunID, ev.Sequence
	if c.onUpdate != nil {
		c.onUpdate(ThreadUpdateFromWire(ev))
	}
}

func (c *localConn) applySnapshot(snap *wire.ThreadStreamSnapshot) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	if snap != nil {
		c.runID, c.lastSeq = snap.RunID, snap.Sequence
		if c.onUpdate != nil {
			c.onUpdate(mcodeui.ThreadUpdate{
				ThreadID: c.threadID,
				Thread:   ThreadSummaryToUI(snap.Thread),
				Messages: MessagesWireToUI(snap.History.Messages),
			})
		}
	}
	// 轮次保持首次出现的顺序，同一轮次内按序号回放；快照所属轮次最先处理。
	runOrder := map[string]int{c.runID: 0}
	for _, ev := range c.pendingEvents {
		if _, ok := runOrder[ev.RunID]; !ok {
			runOrder[ev.RunID] = len(runOrder)
		}
	}
	sort.SliceStable(c.pendingEvents, func(i, j int) bool {
		a, b := c.pendingEvents[i], c.pendingEvents[j]
		if a.RunID != b.RunID {
			return runOrder[a.RunID] < runOrder[b.RunID]
		}
		return a.Sequence < b.Sequence
	})
	for _, ev := range c.pendingEvents {
		c.applyEventLocked(ev)
	}
	c.pendingEvents = nil
	c.snapshotApplied = true
}

// Watch 订阅指定会话的事件与审批，返回可重复调用的 stop 函数。
func (c *Client) Watch(threadID string, onUpdate func(mcodeui.ThreadUpdate), onApprovals func([]mcodeui.Approval)) (stop func()) {
	conn := &localConn{
		threadID:    threadID,
		onUpdate:    onUpdate,
		onApprovals: onApprovals,
	}

	ctx := context.Background()
	res, err := c.backend.Subscribe(ctx, conn, threadID)
	if err != nil {
		_ = conn.Close()
		log.Printf("Watch: failed to subscribe thread %s: %v", threadID, err)
		return func() {}
	}

	// 订阅快照是后端对会话状态的权威回答（含 IsRunning）。会话列表里的运行标记只是
	// 某次加载时的快照；若一轮的结束事件在未订阅该会话时发出，界面会一直显示进行中，
	// 打开会话也不会自愈。这里立刻把快照交给界面，使打开会话即以真实状态为准。
	conn.applySnapshot(res.Snapshot)

	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = conn.Close()
			_ = c.backend.Unsubscribe(context.Background(), conn, threadID)
		})
	}
	return stop
}

// Usage 调用 backend.ThreadUsage 获取指定会话的 token 用量统计，并从已持久化的消息
// 推导请求数与工具调用等活动统计（不改动存储格式，历史数据也能算）。
func (c *Client) Usage(ctx context.Context, threadID string) (mcodeui.ThreadUsage, error) {
	turns, total, err := c.backend.ThreadUsage(ctx, threadID)
	if err != nil {
		return mcodeui.ThreadUsage{}, err
	}
	u := ThreadUsageHarnessToUI(turns, total)
	if msgs, hErr := c.History(ctx, threadID); hErr == nil {
		u.Requests, u.ToolCalls, u.TopTools = ThreadActivityFromMessages(msgs)
	}
	return u, nil
}

// SetThreadRuntimeMode 调用 backend.UpdateThread 设置指定会话的运行模式。
func (c *Client) SetThreadRuntimeMode(threadID, mode string) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    threadID,
		RuntimeMode: &mode,
	})
	return err
}

// SetThreadAgent 调用 backend.UpdateThread 设置指定会话的 Agent 与模型。
// provider 为空时表示不修改供应商。
func (c *Client) SetThreadAgent(threadID, provider, model string) error {
	ctx := context.Background()
	params := wire.ThreadUpdateParams{
		ThreadID: threadID,
	}
	if provider != "" {
		params.Provider = &provider
	}
	if model != "" || provider == "" {
		params.Model = &model
	}
	_, err := c.backend.UpdateThread(ctx, params)
	return err
}

// RenameThread 调用 backend.UpdateThread 更新会话标题。
func (c *Client) RenameThread(threadID, title string) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Title:    &title,
	})
	return err
}

// SetThreadPinned 调用 backend.UpdateThread 设置会话置顶状态。
func (c *Client) SetThreadPinned(threadID string, pinned bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Pinned:   &pinned,
	})
	return err
}

// SetThreadArchived 调用 backend.UpdateThread 设置会话归档状态。
func (c *Client) SetThreadArchived(threadID string, archived bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	})
	return err
}

// SetThreadDormant 调用 backend.UpdateThread 设置会话休眠状态。
func (c *Client) SetThreadDormant(threadID string, dormant bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Dormant:  &dormant,
	})
	return err
}

// ArchiveThreadWithUndo 归档指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
func (c *Client) ArchiveThreadWithUndo(threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	ctx := context.Background()
	archived := true
	_, token, err := c.backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	})
	return token, err
}

// MarkDormantWithUndo 休眠指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
func (c *Client) MarkDormantWithUndo(threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	ctx := context.Background()
	dormant := true
	_, token, err := c.backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Dormant:  &dormant,
	})
	return token, err
}

// RetainThread 将临时会话转为正式会话。
func (c *Client) RetainThread(threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.RetainThread(context.Background(), threadID)
}

// ReleaseProvisionalThread 释放临时会话，调度延迟自动清理。
func (c *Client) ReleaseProvisionalThread(threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	c.backend.ScheduleProvisionalCleanup(threadID)
	return nil
}

// DeleteThread 调用 backend.DeleteThread 删除指定会话。
// 若会话运行在 mcode 管理的 worktree 中且 AutoCleanupWorktreesOnDeleteEnabled 开启，
// 删除会话后会自动执行 git worktree remove --force 并删除对应的任务分支。
func (c *Client) DeleteThread(threadID string) error {
	ctx := context.Background()

	var (
		wtPath    string
		wtBranch  string
		repoRoot  string
		isMcodeWT bool
	)
	if c.backend != nil {
		if workDir, ok := c.backend.ThreadWorkDir(threadID); ok && workDir != "" {
			if st, err := c.backend.GitState(ctx, threadID); err == nil {
				repoRoot = st.WorkDir
				for _, wt := range st.Worktrees {
					if filepath.Clean(wt.Path) == filepath.Clean(workDir) && !wt.Current {
						wtPath = wt.Path
						wtBranch = wt.Branch
						isMcodeWT = true
						break
					}
				}
			}
			if !isMcodeWT {
				dataDir := c.effectiveDataDir()
				if dataDir != "" {
					managedDir := filepath.Join(dataDir, "worktrees")
					swarmDir := filepath.Join(dataDir, "swarm-worktrees")
					// 旧版本建在 hydra-worktrees 下，过渡期两个目录都认。
					legacySwarmDir := filepath.Join(dataDir, "hydra-worktrees")
					cleanWD := filepath.Clean(workDir)
					if strings.HasPrefix(cleanWD, filepath.Clean(managedDir)) || strings.HasPrefix(cleanWD, filepath.Clean(swarmDir)) || strings.HasPrefix(cleanWD, filepath.Clean(legacySwarmDir)) {
						wtPath = workDir
						isMcodeWT = true
					}
				}
			}
		}
	}

	cleanupEnabled := true
	if cfg, ok := c.loadConfig(); ok {
		cleanupEnabled = cfg.SourceControl.AutoCleanupWorktreesOnDeleteEnabled()
	}

	if err := c.backend.DeleteThread(ctx, threadID); err != nil {
		return err
	}

	if isMcodeWT && cleanupEnabled && wtPath != "" && c.gitRunner() != nil {
		if repoRoot == "" {
			repoRoot = wtPath
		}
		_ = c.gitRunner().WorktreeRemove(ctx, repoRoot, wtPath, true)
		if wtBranch != "" {
			_ = c.gitRunner().BranchDelete(ctx, repoRoot, wtBranch, true)
		}
		_ = os.RemoveAll(wtPath)
	}

	return nil
}

// RemoveProject 调用 backend.RemoveProject 从侧边栏移除指定项目。
func (c *Client) RemoveProject(projectID string) error {
	ctx := context.Background()
	return c.backend.RemoveProject(ctx, projectID)
}

// Diff 调用 backend.GetDiff 获取指定会话或轮次的代码差异。
// turnID 为空表示整个会话。
func (c *Client) Diff(ctx context.Context, threadID, turnID string) (mcodeui.ThreadDiff, error) {
	var tID *string
	if turnID != "" {
		t := turnID
		tID = &t
	}
	res, err := c.backend.GetDiff(ctx, wire.ThreadsGetDiffParams{
		ThreadID: threadID,
		TurnID:   tID,
	})
	if err != nil {
		return mcodeui.ThreadDiff{}, err
	}

	var failureReason string
	if res.FailureReason != nil {
		failureReason = *res.FailureReason
	}
	filesChanged := res.FilesChanged
	if filesChanged == nil {
		filesChanged = []string{}
	}
	turns := make([]mcodeui.DiffTurn, len(res.Turns))
	for i, t := range res.Turns {
		turns[i] = mcodeui.DiffTurn{
			TurnID:     t.TurnID,
			Index:      t.Index,
			Status:     t.Status,
			HasChanges: t.HasChanges,
		}
	}
	return mcodeui.ThreadDiff{
		Diff:          res.Diff,
		Truncated:     res.Truncated,
		FilesChanged:  filesChanged,
		Turns:         turns,
		FailureReason: failureReason,
	}, nil
}

// MCPSignIn 执行指定 MCP 服务的 OAuth 授权登录流程。
// 重新授权也调用此方法，在开始前会先执行 SignOut。
func (c *Client) MCPSignIn(ctx context.Context, serverID, serverURL string) error {
	return c.MCPSignInWithAuth(ctx, serverID, serverURL, nil)
}

// MCPSignInWithAuth 按目录条目声明的登录方式执行登录；auth 为 nil 时与 MCPSignIn 完全一致。
// 与 MCPSignIn 一样，开始前先执行 SignOut。
func (c *Client) MCPSignInWithAuth(ctx context.Context, serverID, serverURL string, auth *agentconf.MCPCatalogAuth) error {
	if c.oauth == nil {
		return errors.New("mcp oauth is not configured")
	}
	_ = c.oauth.SignOut(serverID)
	return c.oauth.SignInWithHint(ctx, serverID, serverURL, hintFromCatalogAuth(auth))
}

// hintFromCatalogAuth 把目录条目的登录声明转换成 OAuth 流程提示，nil 返回零值。
// Scopes 复制一份，避免提示与目录条目共享底层数组。
func hintFromCatalogAuth(auth *agentconf.MCPCatalogAuth) mcp.AuthHint {
	if auth == nil {
		return mcp.AuthHint{}
	}
	return mcp.AuthHint{
		Mode:     auth.Mode,
		ClientID: auth.ClientID,
		Scopes:   append([]string(nil), auth.Scopes...),
	}
}

// MCPAuthURL 返回指定服务进行中登录的授权链接，没有进行中的登录时返回空串。
// 浏览器没能自动打开时，界面可以用它把链接展示给用户手动打开。
func (c *Client) MCPAuthURL(serverID string) string {
	if c.oauth == nil {
		return ""
	}
	return c.oauth.PendingAuthURL(serverID)
}

// MCPDeviceCode 返回设备流进行中的验证码与验证地址，没有设备流会话时 ok 为 false。
// 只读已生成的会话状态，界面逐帧调用也不会触发网络请求。
func (c *Client) MCPDeviceCode(serverID string) (userCode, verificationURI string, ok bool) {
	if c.oauth == nil {
		return "", "", false
	}
	prompt, ok := c.oauth.PendingDeviceCode(serverID)
	if !ok {
		return "", "", false
	}
	return prompt.UserCode, prompt.VerificationURI, true
}

// MCPCancelSignIn 取消指定服务进行中的登录，没有进行中的登录时什么也不做。
func (c *Client) MCPCancelSignIn(serverID string) {
	if c.oauth == nil {
		return
	}
	c.oauth.CancelSignIn(serverID)
}

// MCPSignOut 清除指定 MCP 服务的授权凭据及客户端注册信息。
func (c *Client) MCPSignOut(serverID string) error {
	if c.oauth == nil {
		return errors.New("mcp oauth is not configured")
	}
	return c.oauth.SignOut(serverID)
}

// MCPIsSignedIn 查询指定 MCP 服务当前是否已登录授权。
func (c *Client) MCPIsSignedIn(serverID string) bool {
	if c.oauth == nil {
		return false
	}
	return c.oauth.IsSignedIn(serverID)
}

// SetAgyAccounts 设置 agy 多账号管理器。
func (c *Client) SetAgyAccounts(m *accounts.Manager) {
	c.agy = m
}

// SetAgySessionLookup 注入「界面会话 ID → agy 会话 ID」解析函数：
// 额度弹窗靠它把「在用」标在当前会话实际绑定的账号上。
func (c *Client) SetAgySessionLookup(fn func(ctx context.Context, threadID string) string) {
	c.agySessionForThread = fn
}

// sessionTracker 返回进程内共享的会话账号跟踪器，首次调用时创建。
func (c *Client) sessionTracker() *sessionAccountTracker {
	c.quotaTrackerOnce.Do(func() {
		c.quotaTracker = newSessionAccountTracker(context.Background(), magpie.ReadTrace)
	})
	return c.quotaTracker
}

// AgyAccounts 返回已配置的全部 agy 账号列表。
func (c *Client) AgyAccounts() ([]accounts.Account, error) {
	if c.agy == nil {
		return nil, errors.New("agy accounts are not configured")
	}
	return c.agy.List(), nil
}

// AgyLoginStart 开启新的 OAuth 登录会话，如有旧会话先取消，并返回授权 URL。
func (c *Client) AgyLoginStart(ctx context.Context) (string, error) {
	if c.agy == nil {
		return "", errors.New("agy accounts are not configured")
	}
	c.agyLoginMu.Lock()
	defer c.agyLoginMu.Unlock()

	if c.agyLogin != nil {
		c.agyLogin.Cancel()
		c.agyLogin = nil
	}

	sess, err := c.agy.LoginStart(ctx)
	if err != nil {
		return "", err
	}
	c.agyLogin = sess
	return sess.URL(), nil
}

// AgyLoginSubmit 提交手动复制的回调 URL 或授权码。
func (c *Client) AgyLoginSubmit(input string) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	c.agyLoginMu.Lock()
	sess := c.agyLogin
	c.agyLoginMu.Unlock()

	if sess == nil {
		return errors.New("no active agy login session")
	}
	return sess.Submit(input)
}

// AgyLoginWait 等待当前登录会话完成并返回新增/更新的账号实体。
func (c *Client) AgyLoginWait(ctx context.Context) (accounts.Account, error) {
	if c.agy == nil {
		return accounts.Account{}, errors.New("agy accounts are not configured")
	}
	c.agyLoginMu.Lock()
	sess := c.agyLogin
	c.agyLoginMu.Unlock()

	if sess == nil {
		return accounts.Account{}, errors.New("no active agy login session")
	}
	return sess.Wait(ctx)
}

// AgyLoginCancel 取消当前登录会话。
func (c *Client) AgyLoginCancel() {
	c.agyLoginMu.Lock()
	defer c.agyLoginMu.Unlock()

	if c.agyLogin != nil {
		c.agyLogin.Cancel()
		c.agyLogin = nil
	}
}

// AgyTerminalLogin 预建隔离账号目录并返回启动子进程所需的环境变量与参数。
func (c *Client) AgyTerminalLogin() (id string, env []string, args []string, err error) {
	if c.agy == nil {
		return "", nil, nil, errors.New("agy accounts are not configured")
	}
	return c.agy.TerminalLogin()
}

// AgyAdoptTerminalLogin 采纳终端登录生成的凭证并合并落盘。
func (c *Client) AgyAdoptTerminalLogin(ctx context.Context, id string) (accounts.Account, error) {
	if c.agy == nil {
		return accounts.Account{}, errors.New("agy accounts are not configured")
	}
	return c.agy.AdoptTerminalLogin(ctx, id)
}

// AgyRemove 删除指定的隔离账号。
func (c *Client) AgyRemove(id string) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.Remove(id)
}

// AgyBoundConversations 查询绑定在该账号上的活跃会话 ID 列表。
func (c *Client) AgyBoundConversations(id string) []string {
	if c.agy == nil {
		return nil
	}
	return c.agy.BoundConversations(id)
}

// AgySetDefault 将指定账号设为默认账号。
func (c *Client) AgySetDefault(id string) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.SetDefault(id)
}

// AgyUpdate 更新指定账号的路由参数与配置。
func (c *Client) AgyUpdate(id string, p accounts.UpdatePatch) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.Update(id, p)
}

// AgyRouting 获取全局路由调度配置。
func (c *Client) AgyRouting() (multiaccount.Config, error) {
	if c.agy == nil {
		return multiaccount.Config{}, errors.New("agy accounts are not configured")
	}
	return c.agy.Routing(), nil
}

// AgySetRouting 更新全局路由调度配置。
func (c *Client) AgySetRouting(cfg multiaccount.Config) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.SetRouting(cfg)
}

// AgyRefreshQuota 刷新指定账号或所有有效账号的额度。
func (c *Client) AgyRefreshQuota(ctx context.Context, id string) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.RefreshQuota(ctx, id)
}

// AgyWarmup 对指定账号（或全部有效账号）执行 Gemini 预热。
func (c *Client) AgyWarmup(ctx context.Context, id string) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.Warmup(ctx, id)
}

// AgyAutoWarmup 获取 AGY 自动预热开关状态。
func (c *Client) AgyAutoWarmup() (bool, error) {
	if c.agy == nil {
		return true, errors.New("agy accounts are not configured")
	}
	return c.agy.AutoWarmup(), nil
}

// AgySetAutoWarmup 设置 AGY 自动预热开关状态。
func (c *Client) AgySetAutoWarmup(enabled bool) error {
	if c.agy == nil {
		return errors.New("agy accounts are not configured")
	}
	return c.agy.SetAutoWarmup(enabled)
}

// AgyDefaultAccountID 获取当前默认生效账号 ID，未配置 agy 时返回空串。
func (c *Client) AgyDefaultAccountID() string {
	if c.agy == nil {
		return ""
	}
	return c.agy.DefaultAccountID()
}

// SetKanban 设置看板核心服务。
func (c *Client) SetKanban(k *kanban.Service) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	c.kanban = k
}

// SetAutomation 设置自动化核心服务。
func (c *Client) SetAutomation(a *automation.Service) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	c.automation = a
}

// SetRemoteService 设置远程控制服务。
func (c *Client) SetRemoteService(r *RemoteService) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	c.remote = r
}

// SetSwarmManager 设置 Swarm 多协作者管理器。
func (c *Client) SetSwarmManager(m *swarm.Manager) {
	c.kanbanMu.Lock()
	c.swarm = m
	if m == nil {
		// 管理器已拆除：清掉排队中的合并通知，避免退出后再触发一次全量拉取。
		for id, timer := range c.swarmNotifyTimers {
			timer.Stop()
			delete(c.swarmNotifyTimers, id)
		}
	}
	c.kanbanMu.Unlock()
	if m != nil {
		m.SetOnChange(c.notifySwarmChanged)
		c.startQuestionAutoForward()
	} else {
		c.stopQuestionAutoForward()
	}
}

// startQuestionAutoForward 启动提问自动转交后台循环，重复调用不会叠加。
func (c *Client) startQuestionAutoForward() {
	c.autoForwardMu.Lock()
	defer c.autoForwardMu.Unlock()
	if c.autoForwardCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.autoForwardCancel = cancel
	go c.runQuestionAutoForward(ctx)
}

// stopQuestionAutoForward 停止提问自动转交后台循环。
func (c *Client) stopQuestionAutoForward() {
	c.autoForwardMu.Lock()
	cancel := c.autoForwardCancel
	c.autoForwardCancel = nil
	c.autoForwardMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SetSwarmPipeline 设置 Swarm 自动合并流水线。
func (c *Client) SetSwarmPipeline(p *swarm.Pipeline) {
	c.kanbanMu.Lock()
	c.swarmPipeline = p
	c.kanbanMu.Unlock()
}

// notifySwarmChanged 在指定主会话的 Swarm 状态变更时通知所有监听者，
// 并重新发布该主会话摘要：Swarm 状态变更不走会话事件，只有重新发布才能让
// SwarmBusy 的变化被桌面通知与远程推送的提醒检测看到。
// 同一主会话在 swarmNotifyDelay 窗口内的多次变更合并成一次通知，保证最后一次一定送达。
func (c *Client) notifySwarmChanged(leadThreadID string) {
	if leadThreadID == "" {
		return
	}
	c.kanbanMu.Lock()
	if c.swarmNotifyTimers == nil {
		c.swarmNotifyTimers = make(map[string]*time.Timer)
	}
	if _, pending := c.swarmNotifyTimers[leadThreadID]; pending {
		// 窗口内已排队：到期时读的是最新状态，这里只需丢弃本次重复通知。
		c.kanbanMu.Unlock()
		return
	}
	c.swarmNotifyTimers[leadThreadID] = time.AfterFunc(swarmNotifyDelay, func() {
		c.flushSwarmChanged(leadThreadID)
	})
	c.kanbanMu.Unlock()
}

// flushSwarmChanged 关闭指定主会话的合并窗口并推送一次最新状态。
func (c *Client) flushSwarmChanged(leadThreadID string) {
	c.kanbanMu.Lock()
	delete(c.swarmNotifyTimers, leadThreadID)
	c.kanbanMu.Unlock()
	c.deliverSwarmChanged(leadThreadID)
}

// deliverSwarmChanged 立即把变更送达监听者（摘要重发 + 全部 watcher 回调）。
func (c *Client) deliverSwarmChanged(leadThreadID string) {
	if leadThreadID != "" && c.backend != nil {
		c.backend.RepublishThreadSummary(leadThreadID)
	}
	c.kanbanMu.Lock()
	watchers := make([]func(string), 0, len(c.swarmWatchers))
	for _, fn := range c.swarmWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()

	for _, fn := range watchers {
		fn(leadThreadID)
	}
}

// notifyKanbanChanged 在看板变更时向所有注册的监听者发送通知。
func (c *Client) notifyKanbanChanged() {
	c.kanbanMu.Lock()
	var watchers []func()
	for _, fn := range c.kanbanWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()

	for _, fn := range watchers {
		fn()
	}
}

func wrapConflictErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		return fmt.Errorf("%w: %v", mcodeui.ErrRevisionConflict, err)
	}
	return err
}

// KanbanBoard 获取看板数据，projectID 为空返回全部。
func (c *Client) KanbanBoard(ctx context.Context, projectID string) (mcodeui.KanbanBoard, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanBoard{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	board, err := k.Board(ctx)
	if err != nil {
		return mcodeui.KanbanBoard{}, wrapConflictErr(err)
	}
	return KanbanBoardToUI(board, projectID), nil
}

// CreateTask 在指定项目下创建新任务。
func (c *Client) CreateTask(ctx context.Context, projectID, title, notes string) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	var notesPtr *string
	if notes != "" {
		notesPtr = &notes
	}
	task, err := k.CreateTask(ctx, kanban.CreateTaskInput{
		ProjectID: projectID,
		Title:     title,
		Notes:     notesPtr,
	})
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// MoveTask 移动任务到指定状态。
func (c *Client) MoveTask(ctx context.Context, taskID, status string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.MoveTask(ctx, taskID, kanban.TaskStatus(status), revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// DeleteTask 按修订号删除任务。
func (c *Client) DeleteTask(ctx context.Context, taskID string, revision int) error {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return wrapConflictErr(errors.New("看板服务不可用"))
	}
	if err := k.DeleteTask(ctx, taskID, revision); err != nil {
		return wrapConflictErr(err)
	}
	return nil
}

// NudgeTask 给执行中的任务会话追加一条催促提醒。
func (c *Client) NudgeTask(ctx context.Context, taskID string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.RescueTask(ctx, taskID, kanban.RescueActionNudge, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// StartTask 开始执行指定任务。
func (c *Client) StartTask(ctx context.Context, taskID string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.StartTask(ctx, taskID, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// ReviewTask 评审任务（decision: accept/reject/requestReview）。
func (c *Client) ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.ReviewTask(ctx, taskID, kanban.ReviewDecision(decision), feedback, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	c.recordRejectPreference(ctx, task, decision, feedback)
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

const goalOwnerSteward = "steward"

// recordRejectPreference 在用户亲手打回总管创建的目标下的任务时，把打回理由记成一条
// 待用户确认的记忆提议，让总管下次少犯同样的错；失败只记日志，不影响评审结果。
func (c *Client) recordRejectPreference(ctx context.Context, task store.Task, decision, feedback string) {
	if decision != string(kanban.ReviewReject) {
		return
	}
	reason := strings.TrimSpace(feedback)
	if reason == "" || task.GoalID == nil || strings.TrimSpace(*task.GoalID) == "" {
		return
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		log.Printf("记录打回偏好失败，无法打开记忆库: %v", err)
		return
	}
	goal, err := st.GetGoal(ctx, *task.GoalID)
	if err != nil || goal.Owner != goalOwnerSteward {
		return
	}
	projectPath := ""
	if task.ProjectID != nil && c.backend != nil {
		if res, listErr := c.backend.ListProjects(ctx); listErr == nil {
			for _, p := range res.Projects {
				if p.ID == *task.ProjectID {
					projectPath = p.Path
					break
				}
			}
		}
	}
	if projectPath == "" {
		return
	}
	if _, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projectPath,
		Title:       "用户打回任务时的要求",
		Content:     fmt.Sprintf("用户打回任务时提出的要求：%s（任务：%s）", reason, task.Title),
		UnitType:    "preference",
		Importance:  0.6,
		Source:      store.MemoryProposalSourceAgent,
		Scope:       store.MemoryProposalScopeProject,
		Status:      store.MemoryProposalStatusPending,
	}); err != nil {
		log.Printf("记录打回偏好失败: %v", err)
	}
}

// CreateGoal 在指定项目下创建新目标。
func (c *Client) CreateGoal(ctx context.Context, projectID, prompt string) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.CreateGoal(ctx, projectID, prompt)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// StartGoal 开始执行目标。
func (c *Client) StartGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.StartGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// PauseGoal 暂停目标执行。
func (c *Client) PauseGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.PauseGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// CancelGoal 取消目标。
func (c *Client) CancelGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.CancelGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// ReplanGoal 重新规划目标。
func (c *Client) ReplanGoal(ctx context.Context, goalID, feedback string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.ReplanGoal(ctx, goalID, feedback, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// WatchKanban 监听看板变更，任一变更时回调，可能来自任意 goroutine。
func (c *Client) WatchKanban(onChange func()) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.kanbanWatchers == nil {
		c.kanbanWatchers = make(map[int]func())
	}
	c.kanbanWatchers[id] = onChange
	c.kanbanMu.Unlock()

	return func() {
		c.kanbanMu.Lock()
		delete(c.kanbanWatchers, id)
		c.kanbanMu.Unlock()
	}
}

func (c *Client) notifyAutomationChanged() {
	c.kanbanMu.Lock()
	watchers := make([]func(), 0, len(c.automationWatchers))
	for _, fn := range c.automationWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

func (c *Client) WatchAutomations(onChange func()) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.automationWatchers == nil {
		c.automationWatchers = make(map[int]func())
	}
	c.automationWatchers[id] = onChange
	c.kanbanMu.Unlock()
	return func() {
		c.kanbanMu.Lock()
		delete(c.automationWatchers, id)
		c.kanbanMu.Unlock()
	}
}

// notifyConfigChanged 通知所有配置订阅者：配置文件已被改写（远程设置、档位写入等）。
func (c *Client) notifyConfigChanged() {
	c.kanbanMu.Lock()
	watchers := make([]func(), 0, len(c.configWatchers))
	for _, fn := range c.configWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

// WatchConfig 订阅配置变更，返回取消订阅的函数。
// 回调在通知方 goroutine 上同步执行，不要在回调里长时间阻塞。
func (c *Client) WatchConfig(onChange func()) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.configWatchers == nil {
		c.configWatchers = make(map[int]func())
	}
	c.configWatchers[id] = onChange
	c.kanbanMu.Unlock()
	return func() {
		c.kanbanMu.Lock()
		delete(c.configWatchers, id)
		c.kanbanMu.Unlock()
	}
}

// Automations 获取指定项目的自动化列表。
func (c *Client) Automations(ctx context.Context, projectID string) ([]mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return nil, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	defs, err := a.List(ctx, projectID)
	if err != nil {
		return nil, wrapConflictErr(err)
	}
	res := make([]mcodeui.Automation, len(defs))
	for i, d := range defs {
		res[i] = AutomationToUI(d)
	}
	return res, nil
}

// CreateAutomation 创建自动化任务。
func (c *Client) CreateAutomation(ctx context.Context, in mcodeui.AutomationInput) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	spec := AutomationInputToSpec(in, automation.Spec{})
	def, err := a.Create(ctx, automation.CreateInput{
		ProjectID: in.ProjectID,
		Name:      in.Name,
		Prompt:    in.Prompt,
		Spec:      spec,
		Enabled:   in.Enabled,
	})
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// UpdateAutomation 更新自动化任务。
func (c *Client) UpdateAutomation(ctx context.Context, id string, in mcodeui.AutomationInput, revision int) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	existing, err := a.Get(ctx, id)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	spec := AutomationInputToSpec(in, existing.Spec)
	update := automation.UpdateInput{
		Name:   &in.Name,
		Prompt: &in.Prompt,
		Spec:   &spec,
	}
	if in.Enabled != existing.Enabled {
		update.Enabled = &in.Enabled
	}
	def, err := a.Update(ctx, id, update, revision)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// SetAutomationEnabled 只改变启用状态，显式启用时重置失败状态。
func (c *Client) SetAutomationEnabled(ctx context.Context, id string, enabled bool, revision int) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	def, err := a.Update(ctx, id, automation.UpdateInput{Enabled: &enabled}, revision)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// DeleteAutomation 删除自动化任务。
func (c *Client) DeleteAutomation(ctx context.Context, id string) error {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return wrapConflictErr(errors.New("自动化服务不可用"))
	}
	return wrapConflictErr(a.Delete(ctx, id))
}

// RunAutomation 立即触发运行一次自动化任务。
func (c *Client) RunAutomation(ctx context.Context, id string) (mcodeui.AutomationRun, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.AutomationRun{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	run, err := a.RunNow(ctx, id)
	if err != nil {
		if errors.Is(err, automation.ErrSchedulerPassive) {
			return mcodeui.AutomationRun{}, fmt.Errorf("%w: %v", mcodeui.ErrAutomationSchedulerPassive, err)
		}
		return mcodeui.AutomationRun{}, wrapConflictErr(err)
	}
	return AutomationRunToUI(run), nil
}

// AutomationSchedulerStatus 返回当前实例的定时调度锁状态，供页面提示使用。
func (c *Client) AutomationSchedulerStatus(ctx context.Context) (mcodeui.AutomationSchedulerStatus, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.AutomationSchedulerStatus{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	st := a.SchedulerLockState()
	return mcodeui.AutomationSchedulerStatus{
		State: mcodeui.AutomationSchedulerState(st.State),
		Error: st.Error,
	}, nil
}

// AutomationRuns 获取指定自动化任务的历史运行记录。
func (c *Client) AutomationRuns(ctx context.Context, id string, limit int) ([]mcodeui.AutomationRun, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return nil, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	runs, err := a.Runs(ctx, id, limit)
	if err != nil {
		return nil, wrapConflictErr(err)
	}
	res := make([]mcodeui.AutomationRun, len(runs))
	for i, r := range runs {
		res[i] = AutomationRunToUI(r)
	}
	return res, nil
}

// Swarm 获取指定会话或其所属主会话的 Swarm 状态。
func (c *Client) Swarm(ctx context.Context, threadID string) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{
			Available:           false,
			PlansOnly:           c.globalLeadPlansOnly(),
			PlansOnlyOverridden: false,
			Bees:                []mcodeui.SwarmBee{},
		}, nil
	}
	if lead, ok := m.BeeOwner(threadID); ok {
		st := m.State(lead)
		return c.swarmState(ctx, st, lead, true), nil
	}
	st := m.State(threadID)
	return c.swarmState(ctx, st, threadID, false), nil
}

func (c *Client) globalLeadPlansOnly() bool {
	dir := c.dataDir
	if dir == "" && c.remote != nil {
		dir = c.remote.dataDir
	}
	if dir != "" {
		if cfg, err := config.Load(dir); err == nil {
			config.ApplyEnv(&cfg, os.LookupEnv)
			return cfg.Orchestration.LeadPlansOnly
		}
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Orchestration.LeadPlansOnly
}

func (c *Client) swarmStateToUI(st swarm.State, leadThreadID string, isBee, available bool) mcodeui.SwarmState {
	res := swarmStateToUI(st, leadThreadID, isBee, available)
	res.Queued = st.Queued
	res.Interrupted = st.Interrupted
	if st.PlansOnly != nil {
		res.PlansOnly = *st.PlansOnly
		res.PlansOnlyOverridden = true
	} else {
		res.PlansOnly = c.globalLeadPlansOnly()
		res.PlansOnlyOverridden = false
	}
	applySwarmThinking(st.Bees, res.Bees)
	return res
}

// applySwarmThinking 在 bee 长时间无进展时把活动文案替换为「思考中 N 分钟…」。
func applySwarmThinking(src []swarm.Bee, bees []mcodeui.SwarmBee) {
	for i := range bees {
		if i < len(src) && src[i].ThinkingMinutes > 0 {
			bees[i].Activity = fmt.Sprintf("思考中 %d 分钟…", src[i].ThinkingMinutes)
		}
	}
}

func (c *Client) swarmState(ctx context.Context, st swarm.State, leadID string, isBee bool) mcodeui.SwarmState {
	result := c.swarmStateToUI(st, leadID, isBee, true)
	if c.backend == nil {
		return result
	}
	for i := range result.Bees {
		h := &result.Bees[i]
		running := h.Status == string(swarm.StatusRunning)
		// 已结束 bee 的进度数据不会再变：命中缓存直接复用，跳过逐 bee 的会话/用量查询。
		// 这些查询随「累计 bee 总数」线性增长，每次都走单连接的 SQLite，是子代理多时
		// 状态推送变慢、会话消息写入被拖住的主因之一。
		if !running {
			if cached, ok := c.cachedSwarmBeeProgress(h.ThreadID); ok {
				h.Provider, h.Model, h.Activity = cached.provider, cached.model, cached.activity
				h.Tokens, h.ToolCalls = cached.tokens, cached.toolCalls
				continue
			}
		}
		// 查询失败时保留已有字段值，并且不把降级结果写进缓存。
		progress := swarmBeeProgress{provider: h.Provider, model: h.Model, activity: h.Activity, tokens: h.Tokens, toolCalls: h.ToolCalls}
		cacheable := true
		if thread, err := c.backend.GetThread(ctx, h.ThreadID); err == nil {
			progress.provider = thread.Provider
			if thread.Model != nil {
				progress.model = *thread.Model
			}
			if thread.LastActivityText != nil {
				progress.activity = *thread.LastActivityText
			}
		} else {
			cacheable = false
		}
		// 用量与工具计数共用一次历史遍历：Usage 内部还会再拉一遍历史，逐 bee 调用会让
		// 查询次数翻倍。
		if _, total, err := c.backend.ThreadUsage(ctx, h.ThreadID); err == nil {
			progress.tokens = int(total.InputTokens + total.OutputTokens)
		} else {
			cacheable = false
		}
		if messages, err := c.History(ctx, h.ThreadID); err == nil {
			progress.toolCalls = 0
			for _, msg := range messages {
				if msg.Kind == "tool" || msg.Role == "tool" {
					progress.toolCalls++
				}
				if running && msg.Role == "assistant" && msg.Kind != "reasoning" && msg.Text != "" {
					progress.activity = msg.Text
				}
			}
		} else {
			cacheable = false
		}
		h.Provider, h.Model, h.Activity = progress.provider, progress.model, progress.activity
		h.Tokens, h.ToolCalls = progress.tokens, progress.toolCalls
		if running {
			h.PendingQuestion = c.pendingBeeQuestion(ctx, h.ThreadID)
			continue
		}
		if cacheable {
			c.storeSwarmBeeProgress(h.ThreadID, progress)
		}
	}
	// 实时活动文本会覆盖「思考中」提示，最后按无进展时长补回。
	applySwarmThinking(st.Bees, result.Bees)
	return result
}

// cachedSwarmBeeProgress 读取已结束 bee 的进度缓存；未命中时返回 false，由调用方重新查询。
func (c *Client) cachedSwarmBeeProgress(threadID string) (swarmBeeProgress, bool) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	progress, ok := c.swarmBeeProgress[threadID]
	return progress, ok
}

// storeSwarmBeeProgress 缓存已结束 bee 的进度数据，供后续状态拉取复用。
func (c *Client) storeSwarmBeeProgress(threadID string, progress swarmBeeProgress) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	if c.swarmBeeProgress == nil {
		c.swarmBeeProgress = make(map[string]swarmBeeProgress)
	}
	// 缓存只需覆盖当前活跃的 bee，超过上限时整体丢弃重建，避免长跑进程里无限增长。
	if len(c.swarmBeeProgress) > 512 {
		c.swarmBeeProgress = make(map[string]swarmBeeProgress)
	}
	c.swarmBeeProgress[threadID] = progress
}

// SetSwarmEnabled 开启或关闭指定主会话的 Swarm 协作模式。
func (c *Client) SetSwarmEnabled(ctx context.Context, threadID string, enabled bool) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{}, errors.New("编排服务不可用")
	}

	leadID := threadID
	isBee := false
	if lead, ok := m.BeeOwner(threadID); ok {
		leadID = lead
		isBee = true
	}

	st := m.SetEnabled(leadID, enabled)
	return c.swarmStateToUI(st, leadID, isBee, true), nil
}

// SetSwarmPlansOnly 设置指定主会话的 Swarm 只规划开关覆盖。
func (c *Client) SetSwarmPlansOnly(ctx context.Context, threadID string, plansOnly bool) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{}, errors.New("编排服务不可用")
	}

	leadID := threadID
	isBee := false
	if lead, ok := m.BeeOwner(threadID); ok {
		leadID = lead
		isBee = true
	}

	v := plansOnly
	st := m.SetLeadPlansOnly(leadID, &v)
	return c.swarmStateToUI(st, leadID, isBee, true), nil
}

// StopSwarmBee 停止指定的子会话。
func (c *Client) StopSwarmBee(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.StopBee(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// StopAllSwarmBees 停止指定主会话下的所有子会话。
func (c *Client) StopAllSwarmBees(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.StopAll(ctx, leadThreadID); err != nil {
		return err
	}
	return nil
}

// ClearFinishedSwarmBees 清理指定主会话下已结束的子会话。
func (c *Client) ClearFinishedSwarmBees(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.ClearFinished(ctx, leadThreadID); err != nil {
		return err
	}
	return nil
}

// pendingBeeQuestion 返回子线程当前待回答的提问，没有则返回 nil。
func (c *Client) pendingBeeQuestion(ctx context.Context, beeThreadID string) *mcodeui.SwarmBeeQuestion {
	approvals, err := c.Approvals(ctx, beeThreadID)
	if err != nil {
		return nil
	}
	for _, a := range approvals {
		if a.Kind != mcodeui.ApprovalKindQuestion {
			continue
		}
		q := &mcodeui.SwarmBeeQuestion{
			RequestID: a.RequestID,
			Questions: a.Questions,
			Forwarded: c.questionForwarded(beeThreadID, a.RequestID),
		}
		if delay := c.autoForwardQuestionDelay(); delay > 0 && !q.Forwarded {
			q.AutoForwardAt = c.noteQuestionSeen(beeThreadID+"/"+a.RequestID, time.Now()).Add(delay)
		}
		return q
	}
	return nil
}

// noteQuestionSeen 记录提问首次出现的时间并返回该时间，重复调用返回首次值。
func (c *Client) noteQuestionSeen(key string, now time.Time) time.Time {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.questionFirstSeen == nil {
		c.questionFirstSeen = make(map[string]time.Time)
	}
	if first, ok := c.questionFirstSeen[key]; ok {
		return first
	}
	c.questionFirstSeen[key] = now
	return now
}

// markQuestionAutoForwardFailed 记录一次自动转交失败，避免同一提问反复重试。
func (c *Client) markQuestionAutoForwardFailed(key string) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.questionAutoForwardFailed == nil {
		c.questionAutoForwardFailed = make(map[string]bool)
	}
	c.questionAutoForwardFailed[key] = true
}

// questionAutoForwardFailedBefore 报告该提问的自动转交是否已经失败过。
func (c *Client) questionAutoForwardFailedBefore(key string) bool {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	return c.questionAutoForwardFailed[key]
}

// dropStaleQuestionTimers 清理已不再待答的提问对应的计时与失败标记。
func (c *Client) dropStaleQuestionTimers(active map[string]bool) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	for key := range c.questionFirstSeen {
		if !active[key] {
			delete(c.questionFirstSeen, key)
		}
	}
	for key := range c.questionAutoForwardFailed {
		if !active[key] {
			delete(c.questionAutoForwardFailed, key)
		}
	}
}

// autoForwardQuestionDelay 返回配置中子线程提问自动转交主线程的等待时长，0 表示关闭。
func (c *Client) autoForwardQuestionDelay() time.Duration {
	dir := c.dataDir
	if dir == "" && c.remote != nil {
		dir = c.remote.dataDir
	}
	if dir != "" {
		if cfg, err := config.Load(dir); err == nil {
			config.ApplyEnv(&cfg, os.LookupEnv)
			return cfg.Orchestration.AutoForwardQuestionDelay()
		}
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Orchestration.AutoForwardQuestionDelay()
}

// autoForwardQuestionsOnce 检查一轮待答提问，把超过配置等待时长的提问自动转交主线程回答。
func (c *Client) autoForwardQuestionsOnce(ctx context.Context, now time.Time) {
	delay := c.autoForwardQuestionDelay()
	if delay <= 0 {
		return
	}
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return
	}
	active := make(map[string]bool)
	for _, leadID := range m.LeadIDs() {
		for _, h := range m.State(leadID).Bees {
			// 不按 bee 状态过滤：待答提问只存在于仍在进行的轮次，以后端请求列表为准。
			if h.Merged || h.MergeDiscarded {
				continue
			}
			q := c.pendingBeeQuestion(ctx, h.ThreadID)
			if q == nil || q.Forwarded {
				continue
			}
			key := h.ThreadID + "/" + q.RequestID
			active[key] = true
			if c.questionAutoForwardFailedBefore(key) {
				continue
			}
			if now.Sub(c.noteQuestionSeen(key, now)) < delay {
				continue
			}
			if err := c.forwardBeeQuestion(ctx, leadID, h.ThreadID, q.RequestID, delay); err != nil {
				log.Printf("自动转交子代理提问失败（%s）: %v", key, err)
				c.markQuestionAutoForwardFailed(key)
			}
		}
	}
	c.dropStaleQuestionTimers(active)
}

// runQuestionAutoForward 每 5 秒检查一次待答提问，超时无人回答时自动转交主线程。
func (c *Client) runQuestionAutoForward(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.autoForwardQuestionsOnce(ctx, now)
		}
	}
}

// questionForwarded 报告子线程提问是否已转交主线程回答。
func (c *Client) questionForwarded(beeThreadID, requestID string) bool {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	return c.forwardedQuestions[beeThreadID+"/"+requestID]
}

// markQuestionForwarded 记录子线程提问已转交主线程回答。
func (c *Client) markQuestionForwarded(beeThreadID, requestID string) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.forwardedQuestions == nil {
		c.forwardedQuestions = make(map[string]bool)
	}
	c.forwardedQuestions[beeThreadID+"/"+requestID] = true
}

// ForwardBeeQuestion 把子线程的提问转给主线程回答：主线程在回复里写答案块，系统解析后自动提交。
func (c *Client) ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error {
	return c.forwardBeeQuestion(ctx, leadThreadID, beeThreadID, requestID, 0)
}

// forwardBeeQuestion 实现提问转交；autoWaited 大于 0 表示超时自动转交，消息里附带等待时长说明。
func (c *Client) forwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string, autoWaited time.Duration) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if owner, ok := m.BeeOwner(beeThreadID); !ok || owner != leadThreadID {
		return errors.New("子线程不属于该会话")
	}

	approvals, err := c.Approvals(ctx, beeThreadID)
	if err != nil {
		return err
	}
	var target *mcodeui.Approval
	for i := range approvals {
		if approvals[i].RequestID == requestID && approvals[i].Kind == mcodeui.ApprovalKindQuestion {
			target = &approvals[i]
			break
		}
	}
	if target == nil {
		return errors.New("提问已结束")
	}

	questions := make([]swarm.ForwardQuestion, 0, len(target.Questions))
	for _, q := range target.Questions {
		choices := make([]string, 0, len(q.Choices))
		for _, ch := range q.Choices {
			choices = append(choices, ch.Label)
		}
		questions = append(questions, swarm.ForwardQuestion{
			ID:             q.ID,
			Header:         q.Header,
			Prompt:         q.Prompt,
			Choices:        choices,
			AllowsMultiple: q.AllowsMultiple,
			AllowsOther:    q.AllowsOther,
		})
	}

	name := ""
	for _, h := range m.State(leadThreadID).Bees {
		if h.ThreadID == beeThreadID {
			name = h.Name
			break
		}
	}
	var msg string
	if autoWaited > 0 {
		msg = swarm.BuildAutoForwardQuestionMessage(name, beeThreadID, requestID, autoWaited, questions)
	} else {
		msg = swarm.BuildForwardQuestionMessage(name, beeThreadID, requestID, questions)
	}
	if err := c.backend.SendSwarmReport(ctx, leadThreadID, msg); err != nil {
		return err
	}
	c.markQuestionForwarded(beeThreadID, requestID)
	c.notifySwarmChanged(leadThreadID)
	return nil
}

// SwarmMergeDiff 获取 bee 待合并改动（Diff 为 unified diff 文本，FilesChanged 为改动文件）。
func (c *Client) SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (mcodeui.ThreadDiff, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.ThreadDiff{}, errors.New("编排服务不可用")
	}
	patch, err := m.MergeDiff(ctx, leadThreadID, beeThreadID)
	if err != nil {
		return mcodeui.ThreadDiff{}, err
	}
	var filesChanged []string
	st := m.State(leadThreadID)
	for _, h := range st.Bees {
		if h.ThreadID == beeThreadID {
			filesChanged = make([]string, len(h.ChangedFiles))
			copy(filesChanged, h.ChangedFiles)
			break
		}
	}
	return mcodeui.ThreadDiff{
		Diff:         string(patch),
		FilesChanged: filesChanged,
	}, nil
}

// ApproveSwarmMerge 批准并应用 bee 的改动。
func (c *Client) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if _, err := m.ApproveMerge(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// DiscardSwarmMerge 放弃 bee 的改动。
func (c *Client) DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.DiscardMerge(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// WatchSwarm 监听 Swarm 状态变更，回调接收发生变更的主会话 ID。
func (c *Client) WatchSwarm(onChange func(leadThreadID string)) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.swarmWatchers == nil {
		c.swarmWatchers = make(map[int]func(string))
	}
	c.swarmWatchers[id] = onChange
	c.kanbanMu.Unlock()

	return func() {
		c.kanbanMu.Lock()
		delete(c.swarmWatchers, id)
		c.kanbanMu.Unlock()
	}
}

// Scripts 列出会话的项目级脚本与工作区 package.json 中的脚本：项目脚本在前，package 脚本在后。
func (c *Client) Scripts(ctx context.Context, threadID string) ([]mcodeui.Script, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	// 项目脚本的命令只存在本机存储里，scripts.list 不回传，所以直接读后端。
	projectScripts, err := c.backend.ProjectScripts(threadID)
	if err != nil {
		return nil, err
	}
	res, err := c.backend.ScriptsList(ctx, wire.ScriptsListParams{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	scripts := make([]mcodeui.Script, 0, len(projectScripts)+len(res.PackageScripts))
	for _, s := range projectScripts {
		scripts = append(scripts, mcodeui.Script{
			Name:                s.Name,
			Command:             s.Command,
			Kind:                "project",
			ID:                  s.ID,
			Symbol:              s.Symbol,
			RunOnWorktreeCreate: s.RunOnWorktreeCreate,
		})
	}
	for _, s := range res.PackageScripts {
		scripts = append(scripts, mcodeui.Script{
			Name:     s.Name,
			Command:  s.Command,
			Runner:   res.Runner,
			Favorite: s.IsFavorite,
			Kind:     "package",
		})
	}
	return scripts, nil
}

// ProjectScripts 读取会话所属项目的项目级脚本。
func (c *Client) ProjectScripts(ctx context.Context, threadID string) ([]mcodeui.ProjectScript, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	stored, err := c.backend.ProjectScripts(threadID)
	if err != nil {
		return nil, err
	}
	scripts := make([]mcodeui.ProjectScript, len(stored))
	for i, s := range stored {
		scripts[i] = mcodeui.ProjectScript{
			ID:                  s.ID,
			Name:                s.Name,
			Command:             s.Command,
			Symbol:              s.Symbol,
			RunOnWorktreeCreate: s.RunOnWorktreeCreate,
		}
	}
	return scripts, nil
}

// SetProjectScripts 覆盖保存会话所属项目的项目级脚本。
func (c *Client) SetProjectScripts(ctx context.Context, threadID string, scripts []mcodeui.ProjectScript) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	stored := make([]remote.ProjectScript, len(scripts))
	for i, s := range scripts {
		stored[i] = remote.ProjectScript{
			ID:                  s.ID,
			Name:                s.Name,
			Command:             s.Command,
			Symbol:              s.Symbol,
			RunOnWorktreeCreate: s.RunOnWorktreeCreate,
		}
	}
	return c.backend.SetProjectScripts(threadID, stored)
}

func (c *Client) ThreadWorkDir(threadID string) (string, bool) {
	if c.backend == nil {
		return "", false
	}
	return c.backend.ThreadWorkDir(threadID)
}

// GitState 调用 backend.GitState 读取会话所在仓库的分支、worktree 与改动状态。
func (c *Client) GitState(ctx context.Context, threadID string) (mcodeui.GitState, error) {
	if c.backend == nil {
		return mcodeui.GitState{}, errors.New("backend 不可用")
	}
	st, err := c.backend.GitState(ctx, threadID)
	if err != nil {
		return mcodeui.GitState{}, err
	}
	workDir := filepath.Clean(st.WorkDir)
	worktrees := make([]mcodeui.GitWorktree, len(st.Worktrees))
	for i, wt := range st.Worktrees {
		worktrees[i] = mcodeui.GitWorktree{
			Path:    wt.Path,
			Branch:  strings.TrimPrefix(wt.Branch, "refs/heads/"),
			Current: wt.Current || filepath.Clean(wt.Path) == workDir,
		}
	}
	var remoteURL string
	if rURL, err := c.gitRunner().RemoteURL(ctx, st.WorkDir); err == nil {
		remoteURL = rURL
	}
	return mcodeui.GitState{
		WorkDir:   st.WorkDir,
		Branch:    st.Branch,
		Detached:  st.Detached,
		Head:      st.Head,
		Branches:  st.Branches,
		Worktrees: worktrees,
		Changes:   st.Changes,
		RemoteURL: remoteURL,

		ChangedFiles:   st.ChangedFiles,
		Ahead:          st.Ahead,
		Behind:         st.Behind,
		HasUpstream:    st.HasUpstream,
		HasStash:       st.HasStash,
		CanFastForward: st.CanFastForward,
	}, nil
}

// GitCheckout 调用 backend.GitCheckout 切换会话所在仓库的分支。
func (c *Client) GitCheckout(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCheckout(ctx, threadID, branch)
}

// GitSwitchWorktree 调用 backend.GitSwitchWorktree 把会话工作目录切换到指定 worktree。
func (c *Client) GitSwitchWorktree(ctx context.Context, threadID, path string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitSwitchWorktree(ctx, threadID, path)
}

// GitCommit 调用 backend.GitCommit 暂存全部改动并提交。
func (c *Client) GitCommit(ctx context.Context, threadID, message string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCommit(ctx, threadID, message)
}

// GitChangedFiles 调用 backend.GitChangedFiles 列出会话工作目录的改动文件。
func (c *Client) GitChangedFiles(ctx context.Context, threadID string) ([]mcodeui.GitChangedFile, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	files, err := c.backend.GitChangedFiles(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]mcodeui.GitChangedFile, len(files))
	for i, file := range files {
		out[i] = mcodeui.GitChangedFile{Path: file.Path, OrigPath: file.OrigPath, Status: file.Status}
	}
	return out, nil
}

// GitCommitSelected 调用 backend.GitCommitSelected 只提交勾选的路径。
func (c *Client) GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCommitSelected(ctx, threadID, paths, message)
}

// GitPush 调用 backend.GitPush 推送当前分支。
func (c *Client) GitPush(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitPush(ctx, threadID)
}

// GitCreateBranch 在会话工作目录创建并切换到新分支。
func (c *Client) GitCreateBranch(ctx context.Context, threadID, name string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	return c.gitRunner().CreateBranch(ctx, workDir, name)
}

// GitFastForwardInto 把会话所在仓库的当前分支快进到 branch。
func (c *Client) GitFastForwardInto(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitFastForwardInto(ctx, threadID, branch)
}

// GitMergeFrom 把 branch 合并进会话所在仓库的当前分支。
func (c *Client) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitMergeFrom(ctx, threadID, branch)
}

// GitFetch 调用 backend.GitFetch 抓取远程分支。
func (c *Client) GitFetch(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitFetch(ctx, threadID)
}

// GitPull 调用 backend.GitPull 快进拉取当前分支。
func (c *Client) GitPull(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitPull(ctx, threadID)
}

// GitStash 调用 backend.GitStash 贮藏当前工作区改动。
func (c *Client) GitStash(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitStash(ctx, threadID)
}

// GitStashPop 调用 backend.GitStashPop 恢复最近一条贮藏。
func (c *Client) GitStashPop(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitStashPop(ctx, threadID)
}

// GenerateCommitMessage 为指定会话的工作区未提交改动生成提交信息。
func (c *Client) GenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}

	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		return "", errors.New("未启用")
	}

	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}

	diffStat := runGitDiffForCommit(ctx, workDir, "--stat")
	diffPatch := runGitDiffForCommit(ctx, workDir)

	if strings.TrimSpace(diffStat) == "" && strings.TrimSpace(diffPatch) == "" {
		return "", errors.New("没有检测到未提交的代码改动")
	}

	// 与会话标题自动生成共用同一套模型选择。
	provider, model, effort := textgenModelSelection(cfg)

	prompt := textgen.CommitMessagePrompt(diffStat, diffPatch, cfg.SourceControl.CommitInstructions)
	ctx = withThreadTextgenNotice(ctx, c.backend, threadID)
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", err
	}

	cleaned := textgen.CleanCommitMessage(out)
	if cleaned == "" {
		return "", errors.New("生成提交信息为空")
	}
	return cleaned, nil
}

// GeneratePRDescription 根据分支提交或未提交的改动生成 PR 描述。
func (c *Client) GeneratePRDescription(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		return "", errors.New("未启用")
	}
	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	commits, stat, patch, err := gitPRChanges(ctx, workDir)
	if err != nil {
		return "", err
	}
	provider, model, effort := textgenModelSelection(cfg)
	prompt := textgen.PRDescriptionPrompt(commits, stat, patch, cfg.SourceControl.CommitInstructions)
	ctx = withThreadTextgenNotice(ctx, c.backend, threadID)
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", err
	}
	cleaned := textgen.CleanCommitMessage(out)
	if cleaned == "" {
		return "", errors.New("生成 PR 描述为空")
	}
	return cleaned, nil
}

func gitPRChanges(ctx context.Context, workDir string) (string, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", args...))
		cmd.Dir = workDir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	head, _ := run("rev-parse", "HEAD")
	for _, base := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		baseHead, err := run("rev-parse", "--verify", base+"^{commit}")
		if err != nil || baseHead == head {
			continue
		}
		mergeBase, err := run("merge-base", "HEAD", base)
		if err != nil {
			continue
		}
		commits, err := run("log", "--format=%s%n%b", mergeBase+"..HEAD")
		if err != nil {
			continue
		}
		if commits != "" {
			stat, err := run("diff", "--stat", mergeBase+"...HEAD")
			if err != nil {
				return "", "", "", err
			}
			patch, err := run("diff", "--no-color", mergeBase+"...HEAD")
			return commits, stat, patch, err
		}
		break
	}
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	stat, patch := runGitDiffForCommit(ctx, workDir, "--stat"), runGitDiffForCommit(ctx, workDir)
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	if stat == "" && patch == "" {
		return "", "", "", errors.New("当前分支没有可描述的改动")
	}
	return "", stat, patch, nil
}

func runGitDiffForCommit(ctx context.Context, workDir string, extraArgs ...string) string {
	args := append([]string{"diff", "HEAD", "--no-color"}, extraArgs...)
	cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", args...))
	cmd.Dir = workDir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		return strings.TrimSpace(out.String())
	}

	var sb strings.Builder
	cachedArgs := append([]string{"diff", "--cached", "--no-color"}, extraArgs...)
	cachedCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", cachedArgs...))
	cachedCmd.Dir = workDir
	var cachedOut bytes.Buffer
	cachedCmd.Stdout = &cachedOut
	if err := cachedCmd.Run(); err == nil && cachedOut.Len() > 0 {
		sb.WriteString(cachedOut.String())
	}

	unstagedArgs := append([]string{"diff", "--no-color"}, extraArgs...)
	unstagedCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", unstagedArgs...))
	unstagedCmd.Dir = workDir
	var unstagedOut bytes.Buffer
	unstagedCmd.Stdout = &unstagedOut
	if err := unstagedCmd.Run(); err == nil && unstagedOut.Len() > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(unstagedOut.String())
	}

	return strings.TrimSpace(sb.String())
}

// BranchThread 调用 backend.BranchThread 从指定助手答复分支出新会话。
func (c *Client) BranchThread(ctx context.Context, threadID, messageID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	res, err := c.backend.BranchThread(ctx, wire.ThreadsBranchParams{ThreadID: threadID, MessageID: messageID})
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// ForwardThread 调用 backend.ForwardThread 将已有会话的历史上下文转发为新会话。
func (c *Client) ForwardThread(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	res, err := c.backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: threadID,
		ActionID: newUUID(),
	})
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// ThreadForwardedContext 调用 backend.ThreadForwardedContext 读取转发会话带入的历史上下文。
func (c *Client) ThreadForwardedContext(ctx context.Context, threadID string) (mcodeui.ForwardedContext, bool, error) {
	if c.backend == nil {
		return mcodeui.ForwardedContext{}, false, errors.New("backend 不可用")
	}
	res, ok := c.backend.ThreadForwardedContext(threadID)
	return mcodeui.ForwardedContext{
		SourceTitle: res.SourceTitle,
		Content:     res.Content,
		Truncated:   res.Truncated,
	}, ok, nil
}

// SetThreadComposerPrefs 调用 backend.SetThreadComposerPrefs 保存会话的 composer 偏好。
func (c *Client) SetThreadComposerPrefs(threadID string, prefs mcodeui.ComposerPrefs) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadComposerPrefs(context.Background(), threadID, remote.ThreadComposerPrefs{
		InteractionMode: prefs.InteractionMode,
		Effort:          prefs.Effort,
		BeesProvider:    prefs.BeesProvider,
		BeesModel:       prefs.BeesModel,
		BeesEffort:      prefs.BeesEffort,
		Persona:         prefs.Persona,
	})
	return err
}

// SetThreadFastMode 调用 backend.SetThreadFastMode 保存会话的快速模式与速率档位。
func (c *Client) SetThreadFastMode(threadID string, fastMode bool, speedTier string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadFastMode(context.Background(), threadID, fastMode, speedTier)
	return err
}

// SetThreadSwarmMode 调用 backend.SetThreadSwarmMode 保存会话级 Swarm 工作模式。
func (c *Client) SetThreadSwarmMode(threadID, mode string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadSwarmMode(context.Background(), threadID, mode)
	return err
}

// SetThreadBeeTiers 调用 backend.SetThreadBeeTiers 保存会话的 Swarm Bee 档位覆盖配置。
func (c *Client) SetThreadBeeTiers(threadID, beeTiers string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.SetThreadBeeTiers(context.Background(), threadID, beeTiers)
}

// SetThreadSwarmPairID 调用 backend.SetThreadSwarmPairID 记录会话选中的 Swarm 配对。
func (c *Client) SetThreadSwarmPairID(threadID, pairID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.SetThreadSwarmPairID(context.Background(), threadID, pairID)
}

// FollowUpsWireToUI 将 wire 协议层的 FollowUp 切片转换为 UI 层的 QueuedFollowUp 切片。
func FollowUpsWireToUI(fus []wire.FollowUp) []mcodeui.QueuedFollowUp {
	if fus == nil {
		return nil
	}
	items := make([]mcodeui.QueuedFollowUp, len(fus))
	for i, fu := range fus {
		items[i] = mcodeui.QueuedFollowUp{
			ID:              fu.ID,
			Text:            fu.Text,
			CreatedAt:       fu.CreatedAt,
			AttachmentCount: len(fu.Attachments),
			System:          fu.System,
			Intent:          fu.Intent,
		}
	}
	return items
}

// Queue 调用 backend.QueueList 列出指定会话排队等待发送的消息列表。
func (c *Client) Queue(ctx context.Context, threadID string) ([]mcodeui.QueuedFollowUp, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	snap, err := c.backend.QueueList(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return FollowUpsWireToUI(snap.FollowUps), nil
}

// Enqueue 调用 backend.SendMessage 在会话运行中把消息加入排队队列。
func (c *Client) Enqueue(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        text,
		Attachments: attachmentIDs,
		ActionID:    newUUID(),
		WhenRunning: "queue",
	})
	return err
}

// RemoveQueued 调用 backend.QueueRemove 从指定会话的排队队列中移除跟进消息。
func (c *Client) RemoveQueued(ctx context.Context, threadID, followUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		ActionID:   newUUID(),
	})
	return err
}

// SendQueuedNow 调用 backend.QueueSendNow 立即从队列发送指定跟进项。
func (c *Client) SendQueuedNow(ctx context.Context, threadID, followUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		ActionID:   newUUID(),
	})
	return err
}

// MoveQueued 调用 backend.QueueMove 把排队项（连同所在 bundle）移到 beforeFollowUpID 之前，空表示移到队尾。
func (c *Client) MoveQueued(ctx context.Context, threadID, followUpID, beforeFollowUpID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueMove(ctx, wire.ThreadsQueueMoveParams{
		ThreadID:         threadID,
		FollowUpID:       followUpID,
		BeforeFollowUpID: beforeFollowUpID,
		ActionID:         newUUID(),
	})
	return err
}

// SetThreadMCPDisabled 调用 backend.SetThreadMCPDisabled 保存会话级外部 MCP 禁用名单。
func (c *Client) SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadMCPDisabled(ctx, threadID, keys)
	return err
}

// UpdateQueued 调用 backend.QueueUpdate 更新指定会话排队队列中跟进项的文本内容。
func (c *Client) UpdateQueued(ctx context.Context, threadID, followUpID, text string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID:   threadID,
		FollowUpID: followUpID,
		Text:       &text,
		ActionID:   newUUID(),
	})
	return err
}

func (c *Client) QueuedAttachments(ctx context.Context, threadID, followUpID string) ([]mcodeui.Attachment, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	queue, err := c.backend.QueueList(ctx, threadID)
	if err != nil {
		return nil, err
	}
	for _, item := range queue.FollowUps {
		if item.ID != followUpID {
			continue
		}
		var attachments []mcodeui.Attachment
		for _, att := range item.Attachments {
			size := 0
			if att.ByteCount != nil {
				size = *att.ByteCount
			}
			attachments = append(attachments, mcodeui.Attachment{
				ID: att.ID, Name: att.Name, MimeType: att.MimeType, ByteCount: size,
			})
		}
		return attachments, nil
	}
	return nil, errors.New("排队消息已派发或删除")
}

func (c *Client) UpdateQueuedWithAttachments(ctx context.Context, threadID, followUpID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	// 非 nil 空切片表示清空附件；nil 在后端表示保留原附件。
	ids := append([]string{}, attachmentIDs...)
	_, err := c.backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: followUpID, Text: &text, Attachments: ids, ActionID: newUUID(),
	})
	return err
}

// InterruptAndSend 调用 backend.QueueInterruptAndSend 中断当前轮次并在停止后优先发送或排队新消息。
func (c *Client) InterruptAndSend(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID:    threadID,
		Text:        &text,
		Attachments: attachmentIDs,
		ActionID:    newUUID(),
	})
	return err
}

// WatchQueue 调用 backend.AddQueueChangedListener 监听队列变动并转发给 onChange。
func (c *Client) WatchQueue(onChange func(threadID string, items []mcodeui.QueuedFollowUp)) (stop func()) {
	if c.backend == nil {
		return func() {}
	}
	// 后端在锁外通知，并发提交可能倒序到达，不能让旧快照覆盖新队列。
	var mu sync.Mutex
	revisions := make(map[string]int)
	return c.backend.AddQueueChangedListener(func(snap wire.ThreadsQueueSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		if snap.Revision <= revisions[snap.ThreadID] {
			return
		}
		revisions[snap.ThreadID] = snap.Revision
		if onChange != nil {
			onChange(snap.ThreadID, FollowUpsWireToUI(snap.FollowUps))
		}
	})
}

// WatchThreadSummaries 调用 backend.Hub() 注册全局事件观察者，把任一会话的运行态切换转发给 onChange。
// 会话列表加载得到的运行标记只是某次快照：没有订阅 Watch 的会话（切走后结束、在 web 端被停止等）
// 收不到结束事件，侧栏左侧的动态图标会一直转下去，这里补一个全局的运行态来源。
func (c *Client) WatchThreadSummaries(onChange func(update mcodeui.Thread)) (stop func()) {
	if c.backend == nil || onChange == nil {
		return func() {}
	}
	hub := c.backend.Hub()
	if hub == nil {
		return func() {}
	}
	return hub.AddObserver(func(_ string, method string, params any) {
		if method != "thread.summary" {
			return
		}
		sum, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		onChange(ThreadSummaryToUI(sum))
	})
}

// contextDocName 把文档名规整为项目文档引用：标准文档为根部文件名（instructions.md），自定义文档为 docs/x.md。
func contextDocName(name string) (string, error) {
	ref, err := projctx.ParseRef(name)
	if err != nil {
		return "", err
	}
	if ref.Standard {
		return ref.Name, nil
	}
	return projctx.DocsDir + "/" + ref.Name, nil
}

func contextDocToUI(name string, d projctx.Doc) mcodeui.ContextDoc {
	return mcodeui.ContextDoc{
		Name:    name,
		Content: d.Content,
		Hash:    d.Hash,
		ModTime: d.ModTime,
	}
}

// ContextDocs 获取指定项目文档的文档列表。
func (c *Client) ContextDocs(projectPath string) ([]mcodeui.ContextDoc, error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, err
	}
	manifest, err := svc.Manifest()
	if err != nil {
		return nil, err
	}
	res := make([]mcodeui.ContextDoc, 0, len(manifest.Docs))
	for _, d := range manifest.Docs {
		if !d.Exists {
			continue
		}
		name := d.File
		if !d.Standard {
			name = projctx.DocsDir + "/" + d.File
		}
		doc, err := svc.ReadRef(name)
		if errors.Is(err, projctx.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res = append(res, contextDocToUI(name, doc))
	}
	return res, nil
}

// ReadContextDoc 读取指定项目文档中的单篇文档。
func (c *Client) ReadContextDoc(projectPath, name string) (mcodeui.ContextDoc, error) {
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.ContextDoc{}, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.ReadRef(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}

// WriteContextDoc 写入或更新项目文档中的单篇文档。
func (c *Client) WriteContextDoc(projectPath, name, content, expectedHash string) (mcodeui.ContextDoc, error) {
	// 不同窗口共享 Client，但每次调用创建的 Service 不共享互斥锁。
	c.contextWriteMu.Lock()
	defer c.contextWriteMu.Unlock()
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.ContextDoc{}, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.WriteRef(name, content, expectedHash)
	if err != nil {
		if errors.Is(err, projctx.ErrConflict) {
			return mcodeui.ContextDoc{}, fmt.Errorf("%w: %v", mcodeui.ErrContextConflict, err)
		}
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}

// WatchContextDocs 监听项目文档文档变更。
func (c *Client) WatchContextDocs(projectPath string, onChange func()) (stop func(), err error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, err
	}
	watcher, err := svc.Watch(onChange)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = watcher.Close()
		})
	}
	return stop, nil
}

// CreateContextDoc 创建空白项目文档文档，不覆盖已有文件。
func (c *Client) CreateContextDoc(projectPath, name string) (mcodeui.ContextDoc, error) {
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.CreateDoc(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}

// memoryTimeoutCtx 返回带 10 秒超时的调用上下文，用于记忆提议的本地读写与远端请求。
func memoryTimeoutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), memoryRequestTimeout)
}

// memoryRequestTimeout 是记忆相关的单次本地或远端请求超时。
const memoryRequestTimeout = 10 * time.Second

// memoryStoreConn 返回记忆提议用的 store 连接。
// 优先复用已挂载到 MemoryBackend 的持久化 store：与主连接是同一个实例，迁移也只在主连接上跑过，
// 正常运行时不会再出现第二条 sqlite 连接。只有后端未挂载 store（测试或无后端）时才回退到
// 按数据目录单独打开一条连接，此时两个连接指向同一个 sqlite 文件，WAL + busy_timeout(5000)
// 下并发读写是安全的。
func (c *Client) memoryStoreConn() (*store.Store, error) {
	if c.backend != nil {
		if s := c.backend.Store(); s != nil {
			return s, nil
		}
	}
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.memoryStore != nil {
		return c.memoryStore, nil
	}
	dir := c.effectiveDataDir()
	if dir == "" {
		return nil, errors.New("无法定位 mcode 数据目录，记忆提议暂不可用")
	}
	s, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		return nil, fmt.Errorf("打开记忆提议数据库失败: %w", err)
	}
	c.memoryStore = s
	return s, nil
}

// nmemClient 返回懒加载的 Nowledge Mem 客户端；配置损坏时返回明确的中文错误。
func (c *Client) nmemClient() (*nmem.Client, error) {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.memoryNmem != nil {
		return c.memoryNmem, nil
	}
	cl, err := nmem.Default()
	if err != nil {
		return nil, fmt.Errorf("未配置 Nowledge Mem：%w", err)
	}
	c.memoryNmem = cl
	return cl, nil
}

// memorySpace 解析项目对应的 Nowledge 空间：配置覆盖优先，其次按目录同名匹配（memspace 的
// NMEM_SPACE），都没有时落到 default 空间。
func (c *Client) memorySpace(projectPath string) string {
	if cfg, ok := c.loadConfig(); ok {
		if space := strings.TrimSpace(cfg.Memory.SpaceFor(projectPath)); space != "" {
			return space
		}
	}
	for _, kv := range memspace.Env(projectPath) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "NMEM_SPACE" {
			if space := strings.TrimSpace(v); space != "" {
				return space
			}
		}
	}
	return nmem.DefaultSpace
}

// memoryWriteError 把远端写入失败转成中文提示；未配置密钥时给出明确指引。
func memoryWriteError(err error) error {
	if errors.Is(err, nmem.ErrNotConfigured) {
		return errors.New("未配置 Nowledge Mem：请设置 NMEM_API_KEY（或 ~/.nowledge-mem/config.json 的 apiKey）后重试")
	}
	return fmt.Errorf("写入 Nowledge Mem 失败：%w", err)
}

// memoryReadError 把远端读取失败转成中文提示；未配置密钥时给出明确指引。
func memoryReadError(err error) error {
	if errors.Is(err, nmem.ErrNotConfigured) {
		return errors.New("未配置 Nowledge Mem：请设置 NMEM_API_KEY（或 ~/.nowledge-mem/config.json 的 apiKey）后重试")
	}
	return fmt.Errorf("读取 Nowledge Mem 失败：%w", err)
}

// memoryProposalToUI 把 store 版本的记忆提议转成 UI 版本。
func memoryProposalToUI(p store.MemoryProposal) mcodeui.MemoryProposal {
	return mcodeui.MemoryProposal{
		ID:             p.ID,
		ProjectPath:    p.ProjectPath,
		ThreadID:       p.ThreadID,
		Title:          p.Title,
		Content:        p.Content,
		UnitType:       p.UnitType,
		Importance:     p.Importance,
		Labels:         append([]string(nil), p.Labels...),
		Source:         p.Source,
		Status:         p.Status,
		RemoteMemoryID: p.RemoteMemoryID,
		Error:          p.Error,
		Scope:          p.Scope,
		TargetMemoryID: p.TargetMemoryID,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// MemoryProposals 列出指定项目的记忆提议（按创建时间倒序）；status 为空表示不过滤。
func (c *Client) MemoryProposals(projectPath, status string) ([]mcodeui.MemoryProposal, error) {
	st, err := c.memoryStoreConn()
	if err != nil {
		return nil, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	items, err := st.ListMemoryProposals(ctx, projectPath, status, 0)
	if err != nil {
		return nil, err
	}
	out := make([]mcodeui.MemoryProposal, 0, len(items))
	for _, p := range items {
		out = append(out, memoryProposalToUI(p))
	}
	return out, nil
}

// CountPendingMemoryProposals 统计指定项目待审核的记忆提议条数。
func (c *Client) CountPendingMemoryProposals(projectPath string) (int, error) {
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	return st.CountPendingMemoryProposals(ctx, projectPath)
}

// UpdateMemoryProposal 保存用户编辑的标题、内容、类型、重要度与写入范围，其余字段（状态、来源、远端 ID、错误）保持不变。
func (c *Client) UpdateMemoryProposal(p mcodeui.MemoryProposal) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("记忆标题不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	cur, err := st.GetMemoryProposal(readCtx, p.ID)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	cur.Title = strings.TrimSpace(p.Title)
	cur.Content = p.Content
	if unitType := strings.TrimSpace(p.UnitType); unitType != "" {
		cur.UnitType = unitType
	}
	cur.Importance = p.Importance
	if p.Scope == store.MemoryProposalScopeProject || p.Scope == store.MemoryProposalScopeGlobal {
		cur.Scope = p.Scope
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, cur); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// SetMemoryProposalTarget 把待审提议设为改写某条已有记忆（合并目标）；targetMemoryID 为空表示取消合并、恢复为新增。
func (c *Client) SetMemoryProposalTarget(id, targetMemoryID string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	cur, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	if cur.Status != store.MemoryProposalStatusPending {
		return errors.New("只能修改待审核的记忆提议")
	}
	cur.TargetMemoryID = strings.TrimSpace(targetMemoryID)
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, cur); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// CondenseMemory 用模型精简一条记忆，返回精简后的标题与正文。
// mergeWith 非空时是要合并进来的已有记忆全文（标题+正文），结果合并两者信息去重后精简。
// projectPath 暂只做保留，不参与请求。
func (c *Client) CondenseMemory(projectPath, title, content, mergeWith string) (string, string, error) {
	_ = projectPath
	if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" {
		return "", "", errors.New("记忆标题和正文不能都为空")
	}

	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	provider, model, effort := textgenModelSelection(cfg)
	prompt := textgen.MemoryCondensePrompt(title, content, mergeWith)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", "", err
	}

	newTitle, newContent := textgen.ParseMemoryCondense(out)
	if strings.TrimSpace(newContent) == "" {
		return "", "", errors.New("AI 精简结果为空")
	}
	if strings.TrimSpace(newTitle) == "" {
		newTitle = strings.TrimSpace(title)
	}
	return newTitle, newContent, nil
}

// AcceptMemoryProposal 接受一条待审提议并写入 Nowledge Mem（scope 为 global 时写全局空间，改写提议则更新原记忆）。
// 写入成功时状态置为 accepted 并记下远端 ID；失败时把错误回写到提议上、保持 pending，并把错误返回给界面。
func (c *Client) MemoryBackend() string { return getMemoryBackend() }

func (c *Client) AcceptMemoryProposal(id string) (mcodeui.MemoryProposal, error) {
	if strings.TrimSpace(id) == "" {
		return mcodeui.MemoryProposal{}, errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	p, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return mcodeui.MemoryProposal{}, errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	if p.Status != store.MemoryProposalStatusPending {
		return memoryProposalToUI(p), fmt.Errorf("只有待审核的记忆提议才能写入，当前状态为 %s", p.Status)
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		mem := &localMemoryTools{docs: localmem.New(""), store: st}
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		saved, _, err := mem.commit(ctx, id)
		return memoryProposalToUI(saved), err
	}
	cl, err := c.nmemClient()
	if err != nil {
		return memoryProposalToUI(p), err
	}
	saved, _, err := commitMemoryProposal(context.Background(), st, cl, proposalSpace(p, c.memorySpace(p.ProjectPath)), p)
	return memoryProposalToUI(saved), err
}

// RejectMemoryProposal 驳回一条待审提议：只改本地状态，不碰远端。
func (c *Client) RejectMemoryProposal(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	p, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	if p.Status != store.MemoryProposalStatusPending {
		return fmt.Errorf("只有待审核的记忆提议才能驳回，当前状态为 %s", p.Status)
	}
	p.Status = store.MemoryProposalStatusRejected
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// CreateMemoryProposal 用户手动新建一条记忆提议：来源记为 user，状态为 pending，等待本人确认后才写入。
func (c *Client) CreateMemoryProposal(projectPath, threadID, title, content string) (mcodeui.MemoryProposal, error) {
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.MemoryProposal{}, errors.New("projectPath 不能为空")
	}
	if strings.TrimSpace(title) == "" {
		return mcodeui.MemoryProposal{}, errors.New("记忆标题不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	p, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projectPath,
		ThreadID:    threadID,
		Title:       strings.TrimSpace(title),
		Content:     content,
		Importance:  0.5,
		Source:      store.MemoryProposalSourceUser,
		Status:      store.MemoryProposalStatusPending,
	})
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	return memoryProposalToUI(p), nil
}

// nowledgeThreadIDPrefix 是导入 Nowledge Threads 时给 mcode 会话 ID 加的前缀。
// 对应 internal/remote/nmem_sync.go 的同名常量，两边必须保持一致。
const nowledgeThreadIDPrefix = "mcode-"

// DistillThreadMemories 从 Nowledge 已同步的会话提炼候选记忆，作为 distill 来源的待审提议放进收件箱，返回新增条数。
func (c *Client) DistillThreadMemories(projectPath, threadID string) (int, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return 0, errors.New("本地记忆后端不支持从会话提炼记忆")
	}
	if strings.TrimSpace(projectPath) == "" {
		return 0, errors.New("projectPath 不能为空")
	}
	if strings.TrimSpace(threadID) == "" {
		return 0, errors.New("会话 ID 不能为空")
	}
	cl, err := c.nmemClient()
	if err != nil {
		return 0, err
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0, err
	}
	space := c.memorySpace(projectPath)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	items, err := cl.DistillPreview(ctx, space, nowledgeThreadIDPrefix+threadID)
	if err != nil {
		if errors.Is(err, nmem.ErrUnsupported) {
			return 0, errors.New("Nowledge 没有找到可提炼的会话：这个会话可能还没同步到 Nowledge（每轮结束后自动同步），或服务端不支持提炼预览")
		}
		return 0, memoryReadError(err)
	}

	existing, err := st.ListMemoryProposals(ctx, projectPath, store.MemoryProposalStatusPending, 0)
	if err != nil {
		return 0, err
	}
	existingTitles := make(map[string]struct{})
	for _, p := range existing {
		if p.ThreadID == threadID {
			existingTitles[p.Title] = struct{}{}
		}
	}

	var added int
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			clean := strings.Join(strings.Fields(item.Content), " ")
			runes := []rune(clean)
			if len(runes) > 40 {
				title = string(runes[:40])
			} else {
				title = string(runes)
			}
		}
		if title == "" {
			continue
		}
		if _, ok := existingTitles[title]; ok {
			continue
		}
		unitType := strings.TrimSpace(item.UnitType)
		if unitType == "" {
			unitType = "fact"
		}
		importance := item.Importance
		if importance <= 0 {
			importance = 0.5
		} else if importance > 1 {
			importance = 1
		}

		if _, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
			ProjectPath: projectPath,
			ThreadID:    threadID,
			Title:       title,
			Content:     item.Content,
			UnitType:    unitType,
			Importance:  importance,
			Labels:      item.Labels,
			Source:      store.MemoryProposalSourceDistill,
			Scope:       store.MemoryProposalScopeProject,
			Status:      store.MemoryProposalStatusPending,
		}); err != nil {
			return added, err
		}
		existingTitles[title] = struct{}{}
		added++
	}
	return added, nil
}

// SearchMemories 在项目对应的空间里语义检索记忆。
func (c *Client) SearchMemories(projectPath, query string) ([]mcodeui.MemoryItem, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("搜索内容不能为空")
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		mems, err := localmem.New("").Search(ctx, projectPath, nmem.SearchQuery{Query: query, Limit: 20})
		if err != nil {
			return nil, err
		}
		out := make([]mcodeui.MemoryItem, 0, len(mems))
		for _, m := range mems {
			out = append(out, mcodeui.MemoryItem{ID: m.ID, Title: m.Title, Content: m.Content, UnitType: m.UnitType, Importance: m.Importance, Labels: append([]string(nil), m.Labels...), CreatedAt: m.CreatedAt})
		}
		return out, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	mems, err := searchWithGlobal(ctx, cl, c.memorySpace(projectPath), nmem.SearchQuery{Query: query, Limit: 20})
	if err != nil {
		return nil, memoryReadError(err)
	}
	out := make([]mcodeui.MemoryItem, 0, len(mems))
	for _, m := range mems {
		// 已标记废弃的记忆不再出现在面板搜索结果里。
		if nmem.IsDeprecated(m) {
			continue
		}
		out = append(out, mcodeui.MemoryItem{
			ID:         m.ID,
			Title:      m.Title,
			Content:    m.Content,
			UnitType:   m.UnitType,
			Importance: m.Importance,
			Labels:     append([]string(nil), m.Labels...),
			CreatedAt:  m.CreatedAt,
		})
	}
	return out, nil
}

// ForgetMemory 忘掉一条记忆：服务端支持删除时直接删除，不支持删除时返回 deleted=false，
// 表示已改为给标题打上“已废弃”前缀。成功后一并清掉本地待审里指向这条记忆的提议。
func (c *Client) ForgetMemory(projectPath, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("记忆 ID 不能为空")
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		deleted, err := localmem.New("").Forget(ctx, projectPath, id)
		if err != nil {
			return false, err
		}
		c.removePendingProposalsForMemory(ctx, projectPath, id)
		return deleted, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return false, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	space := c.memorySpace(projectPath)
	deleted, err := cl.ForgetMemory(ctx, space, id)
	if isNmemStatus(err, http.StatusNotFound) && !strings.EqualFold(space, nmem.GlobalSpace) {
		deleted, err = cl.ForgetMemory(ctx, nmem.GlobalSpace, id)
	}
	if err != nil {
		return false, memoryWriteError(err)
	}
	c.removePendingProposalsForMemory(ctx, projectPath, id)
	return deleted, nil
}

// removePendingProposalsForMemory 删除本地待审提议里指向某条远端记忆的条目。
// 失败不影响忘掉记忆本身，所以不返回错误。
func (c *Client) removePendingProposalsForMemory(ctx context.Context, projectPath, memoryID string) int {
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memoryRequestTimeout)
	defer cancel()
	proposals, err := st.ListMemoryProposals(cleanupCtx, projectPath, store.MemoryProposalStatusPending, 0)
	if err != nil {
		return 0
	}
	removed := 0
	for _, p := range proposals {
		if p.TargetMemoryID != memoryID && p.RemoteMemoryID != memoryID {
			continue
		}
		if err := st.DeleteMemoryProposal(cleanupCtx, p.ID); err == nil {
			removed++
		}
	}
	return removed
}

// ExportMemories 把项目空间的全部记忆导出成 Markdown（跳过已废弃的记忆）。
// 开头写导出时间与条数；本地文档后端一次读完（不设条数上限），Nowledge Mem 后端
// 每页 100 条分页读取，最多读 5000 条。
func (c *Client) ExportMemories(projectPath string) (string, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		// 本地文档按小节直接在磁盘上读，不带上限（Search 只对正数 Limit 截断）。
		mems, err := localmem.New("").Search(ctx, projectPath, nmem.SearchQuery{Limit: math.MaxInt32})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n", memoryExportTitle)
		fmt.Fprintf(&b, "空间：本地文档 · 导出时间：%s · 共 %d 条\n",
			time.Now().Format("2006-01-02 15:04"), len(mems))
		for _, scope := range []string{"project", "global"} {
			if scope == "project" {
				b.WriteString("\n## 项目记忆\n\n")
			} else {
				b.WriteString("\n## 全局记忆\n\n")
			}
			for _, m := range mems {
				if m.Space == scope {
					fmt.Fprintf(&b, "### %s\n\n%s\n\n", m.Title, m.Content)
				}
			}
		}
		return b.String(), nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return "", err
	}
	const (
		pageSize    = 100
		maxMemories = 5000
	)
	space := c.memorySpace(projectPath)
	all := make([]nmem.Memory, 0, pageSize)
	for offset := 0; offset < maxMemories; offset += pageSize {
		ctx, cancel := memoryTimeoutCtx()
		page, err := cl.ListMemories(ctx, space, pageSize, offset)
		cancel()
		if err != nil {
			return "", memoryReadError(err)
		}
		for _, m := range page {
			if nmem.IsDeprecated(m) {
				continue
			}
			all = append(all, m)
		}
		if len(page) < pageSize {
			break
		}
	}
	return renderMemoryExport(space, time.Now(), all), nil
}

// WorkingMemory 读取项目对应空间的今日 Working Memory 正文。
func (c *Client) WorkingMemory(projectPath string) (string, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return "", nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	text, err := cl.WorkingMemory(ctx, c.memorySpace(projectPath))
	if err != nil {
		return "", memoryReadError(err)
	}
	return text, nil
}

// MemorySpaces 列出 Nowledge Mem 中所有记忆空间，供设置页的项目记忆空间下拉框使用。
func (c *Client) MemorySpaces(ctx context.Context) ([]mcodeui.MemorySpace, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return []mcodeui.MemorySpace{}, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := memoryCallCtx(ctx)
	defer cancel()
	spaces, err := cl.Spaces(callCtx)
	if err != nil {
		return nil, memoryReadError(err)
	}
	out := make([]mcodeui.MemorySpace, 0, len(spaces))
	for _, s := range spaces {
		out = append(out, mcodeui.MemorySpace{ID: s.ID, Name: s.Name})
	}
	return out, nil
}

// ResolveMemorySpace 解析项目路径对应的记忆空间：override 非空时按它匹配，
// 否则按仓库目录同名匹配（与子进程注入 NMEM_SPACE 的规则一致）。
func (c *Client) ResolveMemorySpace(ctx context.Context, projectPath, override string) (mcodeui.MemorySpaceResolution, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return mcodeui.MemorySpaceResolution{}, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return mcodeui.MemorySpaceResolution{}, err
	}
	callCtx, cancel := memoryCallCtx(ctx)
	defer cancel()
	res := memspace.Describe(callCtx, cl, projectPath, override)
	return mcodeui.MemorySpaceResolution{
		Space:        res.Space,
		FromOverride: res.FromOverride,
		Hit:          res.Hit,
	}, nil
}

// memoryCallCtx 在调用方给定的 ctx 上叠一个记忆请求超时；ctx 为空时从 Background 起算。
func memoryCallCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, memoryRequestTimeout)
}

// RemoteStatus 获取远程控制服务的运行状态（桌面专用，不走 RPC）。
func (c *Client) RemoteStatus(ctx context.Context) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}
	return r.Status(), nil
}

// SetRemoteEnabled 开启或关闭远程控制服务（桌面专用，不走 RPC）。
func (c *Client) SetRemoteEnabled(ctx context.Context, enabled bool) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}

	if enabled {
		cfg, err := config.Load(r.dataDir)
		if err != nil {
			return r.Status(), err
		}
		config.ApplyEnv(&cfg, os.LookupEnv)
		if err := r.Start(cfg.Remote.Listen, cfg.Remote.ExtraHosts); err != nil {
			return r.Status(), err
		}
		return r.Status(), nil
	}

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx); err != nil {
		return r.Status(), err
	}
	return r.Status(), nil
}

// RemotePairing 获取或换发远程控制配对信息（桌面专用，不走 RPC）。
func (c *Client) RemotePairing(ctx context.Context, refresh bool) (mcodeui.RemotePairing, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemotePairing{}, ErrRemoteNotRunning
	}

	// 先取状态再取配对信息：CurrentPairingURLs 会自动换码，不能用于状态判断。
	state := r.PairingState()
	info, err := r.Pairing(refresh)
	if err != nil {
		return mcodeui.RemotePairing{}, err
	}
	if refresh {
		state = r.PairingState()
	}
	return mcodeui.RemotePairing{
		PairingURL:          info.PairingURL,
		LANPairingURL:       info.LANPairingURL,
		TLSPairingURL:       info.TLSPairingURL,
		TailscalePairingURL: info.TailscalePairingURL,
		ShortCode:           info.ShortCode,
		ExpiresAt:           info.ExpiresAt,
		State:               state,
	}, nil
}

// RemoteDevices 获取所有已授权的远程设备列表（桌面专用，不走 RPC）。
func (c *Client) RemoteDevices(ctx context.Context) ([]mcodeui.RemoteDevice, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return nil, ErrRemoteNotRunning
	}
	return r.Devices(), nil
}

// RevokeRemoteDevice 撤销指定的远程设备（桌面专用，不走 RPC）。
func (c *Client) RevokeRemoteDevice(ctx context.Context, deviceID string) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RevokeDevice(deviceID)
}

// RenameRemoteDevice 重命名指定的远程设备（桌面专用，不走 RPC）。
func (c *Client) RenameRemoteDevice(ctx context.Context, deviceID, name string) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.Rename(deviceID, name)
}

// RevokeAllRemoteDevices 撤销全部远程设备（桌面专用，不走 RPC）。
func (c *Client) RevokeAllRemoteDevices(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RevokeAll()
}

// RestartRemote 使用当前配置重启远程控制服务（桌面专用，不走 RPC）。
func (c *Client) RestartRemote(ctx context.Context) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}
	return r.Restart(ctx)
}

// SendRemoteTestPush 通过远程控制服务向所有已登记的 Web Push 订阅发一条测试通知（桌面专用，不走 RPC）。
func (c *Client) SendRemoteTestPush(ctx context.Context) (int, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return 0, ErrRemoteNotRunning
	}
	return r.SendTestPush()
}

// RemoteTLSStatus 获取远程控制 TLS 证书与监听状态（桌面专用，不走 RPC）。
func (c *Client) RemoteTLSStatus(ctx context.Context) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.TLSStatus(), nil
}

// SaveRemoteCloudflareToken 保存或清除 Cloudflare API Token（桌面专用，不走 RPC）。
func (c *Client) SaveRemoteCloudflareToken(token string) error {
	store := config.NewSecretStore("mcode")
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return store.Delete("cloudflare_api_token")
	}
	return store.Set("cloudflare_api_token", trimmed)
}

// HasRemoteCloudflareToken 查询是否已保存 Cloudflare API Token（桌面专用，不走 RPC）。
func (c *Client) HasRemoteCloudflareToken() bool {
	return hasRemoteCloudflareToken()
}

// IssueRemoteTLSCertificate 立即签发远程控制 TLS 证书（桌面专用，不走 RPC）。
func (c *Client) IssueRemoteTLSCertificate(ctx context.Context) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.IssueTLSCertificate(ctx)
}

// ImportRemoteTLSCertificate 导入外部 TLS 证书与私钥（桌面专用，不走 RPC）。
func (c *Client) ImportRemoteTLSCertificate(ctx context.Context, chainPath, keyPath string) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.ImportTLSCertificate(ctx, chainPath, keyPath)
}

// RemoveRemoteTLSCertificate 移除已配置的 TLS 证书（桌面专用，不走 RPC）。
func (c *Client) RemoveRemoteTLSCertificate(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RemoveTLSCertificate(ctx)
}

// RemoteTailscaleStatus 获取内嵌 Tailscale 入口的状态（桌面专用，不走 RPC）。
func (c *Client) RemoteTailscaleStatus(ctx context.Context) (mcodeui.RemoteTailscaleStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTailscaleStatus{}, ErrRemoteNotRunning
	}
	return r.TailscaleStatus(), nil
}

// SetRemoteTailscaleEnabled 在当前运行中的远程服务上启用或停用 Tailscale 入口（桌面专用，不走 RPC）。
func (c *Client) SetRemoteTailscaleEnabled(ctx context.Context, enabled bool) (mcodeui.RemoteTailscaleStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTailscaleStatus{}, ErrRemoteNotRunning
	}
	return r.SetTailscaleEnabled(ctx, enabled)
}

// ResetRemoteTailscale 停止 Tailscale 入口并删除其状态目录（桌面专用，不走 RPC）。
func (c *Client) ResetRemoteTailscale(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.ResetTailscale(ctx)
}

// MoveToWorktree 将指定会话迁移至独立的 Git worktree 分支。
func (c *Client) MoveToWorktree(ctx context.Context, threadID, branch string, transferFiles bool) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.WorktreeMoveExecute(ctx, wire.ThreadsWorktreeExecuteParams{
		ThreadID:      threadID,
		Branch:        branch,
		TransferFiles: transferFiles,
	})
	return err
}

// BulkMarkRead 批量将指定会话标记为已读。
func (c *Client) BulkMarkRead(ctx context.Context, projectID string, threadIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.BulkMarkRead(ctx, projectID, threadIDs)
	return err
}

// BulkArchive 批量归档指定会话。
func (c *Client) BulkArchive(ctx context.Context, projectID string, threadIDs []string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	token, _, err := c.backend.BulkArchive(ctx, projectID, threadIDs)
	return token, err
}

// UndoBulkOperation 根据 token 撤销上一次的批量操作。
func (c *Client) UndoBulkOperation(ctx context.Context, projectID, token string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.UndoBulkOperation(ctx, projectID, token)
}

// RecallTurn 收回指定会话最近一轮消息。
func (c *Client) RecallTurn(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	return c.backend.RecallDraft(ctx, threadID)
}

// PreviewRevert 预览撤销指定会话中某一轮代码变更的效果。
func (c *Client) PreviewRevert(ctx context.Context, threadID, turnID string) (*mcodeui.RevertPreview, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	preview, err := c.backend.PreviewTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}
	files := make([]mcodeui.RevertFile, 0, len(preview.Files))
	for _, f := range preview.Files {
		files = append(files, mcodeui.RevertFile{
			Path:      f.Path,
			IsKept:    f.IsKept,
			Additions: f.Additions,
			Deletions: f.Deletions,
		})
	}
	return &mcodeui.RevertPreview{
		TurnID:    turnID,
		Files:     files,
		Conflicts: preview.Kept(),
	}, nil
}

// ExecuteRevert 执行撤销指定会话中某一轮代码变更。
func (c *Client) ExecuteRevert(ctx context.Context, threadID, turnID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.ExecuteTurnRevert(ctx, threadID, turnID)
	return err
}

// ScanWorktrees 扫描并返回所有 Git worktree 列表。
func (c *Client) ScanWorktrees(ctx context.Context) ([]mcodeui.WorktreeInfo, error) {
	roots, err := c.worktreeRepoRoots(ctx)
	if err != nil {
		return nil, err
	}
	inUse := c.worktreeInUseFunc()
	var out []mcodeui.WorktreeInfo
	for _, root := range roots {
		// ctx 已取消或超时：不再对剩余仓库发起扫描，直接返回已收集的结果。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		items, err := c.gitRunner().ScanWorktrees(ctx, root)
		if err != nil {
			continue
		}
		for _, it := range items {
			out = append(out, mcodeui.WorktreeInfo{
				Path:        it.Path,
				Branch:      it.Branch,
				ProjectPath: it.RepoRoot,
				Merged:      it.Merged,
				Active:      inUse(it.Path),
				AgeDays:     int(it.Age / (24 * time.Hour)),
				SizeBytes:   it.SizeBytes,
				Clean:       it.Clean,
				Orphan:      it.Orphan,
			})
		}
	}
	return out, nil
}

// worktreeRepoRoots 返回全部项目对应的去重 git 仓库根目录。
func (c *Client) worktreeRepoRoots(ctx context.Context) ([]string, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	res, err := c.backend.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var roots []string
	for _, p := range res.Projects {
		root, err := c.gitRunner().RepoRoot(ctx, p.Path)
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots, nil
}

// worktreeInUseFunc 合并会话与 Swarm Bee 正在使用的 worktree 路径，返回判定函数。
func (c *Client) worktreeInUseFunc() func(string) bool {
	return func(path string) bool {
		paths := c.backend.WorktreePathsInUse()
		if c.swarm != nil {
			paths = append(paths, c.swarm.ActiveWorktreePaths()...)
		}
		clean := filepath.Clean(path)
		for _, p := range paths {
			if filepath.Clean(p) == clean {
				return true
			}
			if real, err := filepath.EvalSymlinks(p); err == nil && real == clean {
				return true
			}
		}
		return false
	}
}

// CleanSafeWorktrees 清理已合并且安全的空闲 Git worktree。
func (c *Client) CleanSafeWorktrees(ctx context.Context) (int, error) {
	if c.backend == nil {
		return 0, errors.New("backend 不可用")
	}
	roots, err := c.worktreeRepoRoots(ctx)
	if err != nil {
		return 0, err
	}
	guard := git.CleanupGuard{InUse: c.worktreeInUseFunc()}
	total := 0
	var firstErr error
	for _, root := range roots {
		removed, err := c.gitRunner().CleanSafeWorktrees(ctx, root, guard)
		total += len(removed)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return total, firstErr
}

// TriggerAutoMerge 手动触发对指定 lead 会话下所有已合入 bee 的自动合并。
// 手动路径按设计始终推送，不受 AutoMerge/AutoPush 开关限制。
func (c *Client) TriggerAutoMerge(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	p := c.swarmPipeline
	c.kanbanMu.Unlock()
	if m == nil || p == nil {
		return errors.New("编排自动合并流水线不可用")
	}

	dir, _ := c.ThreadWorkDir(leadThreadID)
	projectID := ""
	if c.backend != nil {
		if thread, err := c.backend.GetThread(ctx, leadThreadID); err == nil {
			projectID = thread.ProjectID
		}
	}
	batches := swarm.BatchesFromState(m.State(leadThreadID), dir, projectID)
	if len(batches) == 0 {
		return errors.New("没有可合并的子代理改动")
	}
	return p.Trigger(ctx, batches)
}

// UpdateModelPreference 更新指定固定模型的偏好配置。
func (c *Client) UpdateModelPreference(ctx context.Context, pin mcodeui.ModelPin, pref mcodeui.ModelPreference) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		latest.Models.SetModelPreference(pin, pref)
		return nil
	})
}

// ReorderModelList 重新排序固定模型列表。
func (c *Client) ReorderModelList(ctx context.Context, pins []mcodeui.ModelPin) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		latest.Models.ReorderModelList(pins)
		return nil
	})
}

// ModelList 获取当前固定模型列表及上限容量。
func (c *Client) ModelList(ctx context.Context) ([]mcodeui.ModelPin, int, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return nil, 0, errors.New("dataDir 未初始化")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, 0, err
	}
	list := make([]mcodeui.ModelPin, len(cfg.Models.Pins))
	copy(list, cfg.Models.Pins)
	return list, cfg.Models.EffectiveModelListLimit(), nil
}

// AddModelToList 向固定模型列表追加模型。
func (c *Client) AddModelToList(ctx context.Context, pin mcodeui.ModelPin) (bool, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return false, errors.New("dataDir 未初始化")
	}
	var added bool
	err := config.Update(dir, func(latest *config.Config) error {
		added = latest.Models.AddModelPin(pin)
		return nil
	})
	if err != nil {
		return false, err
	}
	return added, nil
}

// RemoveModelFromList 从固定模型列表移除模型。
func (c *Client) RemoveModelFromList(ctx context.Context, pin mcodeui.ModelPin) (bool, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return false, errors.New("dataDir 未初始化")
	}
	var removed bool
	err := config.Update(dir, func(latest *config.Config) error {
		removed = latest.Models.RemoveModelPin(pin)
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// ModelPreferences 获取指定固定模型的偏好设置。
func (c *Client) ModelPreferences(ctx context.Context, pin mcodeui.ModelPin) (mcodeui.ModelPreference, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return mcodeui.ModelPreference{}, errors.New("dataDir 未初始化")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return mcodeui.ModelPreference{}, err
	}
	return cfg.Models.ModelPreferenceFor(pin), nil
}

// SwarmPairs 获取所有 Swarm 主导与执行模型配对列表。
func (c *Client) SwarmPairs(ctx context.Context) ([]mcodeui.SwarmPair, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return nil, errors.New("dataDir 未初始化")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	res := make([]mcodeui.SwarmPair, len(cfg.Orchestration.Pairs))
	copy(res, cfg.Orchestration.Pairs)
	return res, nil
}

// AddSwarmPair 添加一组 Swarm 主导与执行模型配对。
func (c *Client) AddSwarmPair(ctx context.Context, p mcodeui.SwarmPair) (mcodeui.SwarmPair, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return mcodeui.SwarmPair{}, errors.New("dataDir 未初始化")
	}
	var added mcodeui.SwarmPair
	err := config.Update(dir, func(latest *config.Config) error {
		added = latest.Orchestration.AddPair(p)
		return nil
	})
	if err != nil {
		return mcodeui.SwarmPair{}, err
	}
	return added, nil
}

// UpdateSwarmPair 更新一组已有的 Swarm 主导与执行模型配对。
func (c *Client) UpdateSwarmPair(ctx context.Context, p mcodeui.SwarmPair) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		if !latest.Orchestration.UpdatePair(p) {
			return fmt.Errorf("swarm pair not found: %s", p.ID)
		}
		return nil
	})
}

// RemoveSwarmPair 根据 ID 移除一组 Swarm 配对。
func (c *Client) RemoveSwarmPair(ctx context.Context, id string) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		if !latest.Orchestration.RemovePair(id) {
			return fmt.Errorf("swarm pair not found: %s", id)
		}
		return nil
	})
}

// ProviderVersion 获取指定提供方的当前安装版本与最新可用版本信息。
func (c *Client) ProviderVersion(ctx context.Context, provider string) (mcodeui.ProviderVersionInfo, error) {
	executable, err := c.providerExecutable(provider)
	if err != nil {
		return mcodeui.ProviderVersionInfo{}, err
	}
	info, err := cliupdate.Installed(ctx, provider, executable)
	return mcodeui.ProviderVersionInfo{
		Provider:        info.Provider,
		Installed:       info.Installed,
		Latest:          info.Latest,
		UpdateAvailable: info.UpdateAvailable,
		ExecutablePath:  info.ExecutablePath,
		Channel:         info.Channel,
		UpdateCommand:   info.UpdateCommand,
	}, err
}

// CheckProviderUpdate 检查指定提供方的版本更新。
func (c *Client) CheckProviderUpdate(ctx context.Context, provider string) (mcodeui.ProviderVersionInfo, error) {
	executable, err := c.providerExecutable(provider)
	if err != nil {
		return mcodeui.ProviderVersionInfo{}, err
	}
	info, err := cliupdate.Check(ctx, provider, executable)
	if errors.Is(err, cliupdate.ErrUnsupported) {
		// 没有更新来源的 provider（droid 等）不算失败，只回落到已安装版本；
		// 已安装版本仍带渠道与升级命令，供设置页动态生成更新入口。
		info, err = cliupdate.Installed(ctx, provider, executable)
	}
	return mcodeui.ProviderVersionInfo{
		Provider:        info.Provider,
		Installed:       info.Installed,
		Latest:          info.Latest,
		UpdateAvailable: info.UpdateAvailable,
		ExecutablePath:  info.ExecutablePath,
		Channel:         info.Channel,
		UpdateCommand:   info.UpdateCommand,
	}, err
}

// providerBinaryFinder 解析 provider CLI 的可执行文件：先在登录 shell 的 PATH 中查找
// （GUI 进程自己的 PATH 通常不含 ~/.local/bin 与各 node 全局 bin），再回退到常见安装目录。
// 测试中可替换为桩，避免依赖运行测试的机器的登录环境。
var providerBinaryFinder = loginenv.FindBinary

// providerFallbackDirs 返回 provider 的常见安装目录。
func providerFallbackDirs(provider string) []string {
	home := loginenv.HomeDirectory()

	// node 全局安装的常见 bin 目录（npm/volta/bun 与 Homebrew）。
	nodeDirs := func() []string {
		var dirs []string
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".npm-global/bin"))
		}
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".local/bin"),
				filepath.Join(home, ".volta/bin"),
				filepath.Join(home, ".bun/bin"),
				filepath.Join(home, ".version-fox/sdks/nodejs/bin"),
				filepath.Join(home, ".version-fox/shims"),
				filepath.Join(home, ".local/share/mise/shims"),
				filepath.Join(home, ".asdf/shims"),
			)
		}
		return dirs
	}

	switch provider {
	case "copilot":
		// gh copilot 把 CLI 下载到 gh 自己的数据目录。
		dir := strings.TrimSpace(os.Getenv("GH_DATA_DIR"))
		if dir == "" {
			if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
				dir = filepath.Join(xdg, "gh")
			} else if home != "" {
				dir = filepath.Join(home, ".local/share/gh")
			}
		}
		if dir == "" {
			return nil
		}
		return []string{filepath.Join(dir, "copilot")}
	case "pi", "omp", "commandcode", "cline":
		return nodeDirs()
	default:
		return nil
	}
}

// providerExecutable 解析 provider CLI 的可执行文件路径，顺序为：
//  1. 配置里的用户路径优先（claude/codex/agy/copilot/pi/omp/commandcode 各自的 providers 字段，
//     ACP Agent（droid/cline/cursor/opencode/grok/devin）用 providers.acp 表）；
//  2. 登录 shell 的 PATH（GUI 进程的 PATH 找不到这些 CLI）；
//  3. provider 的常见安装目录（providerFallbackDirs）。
func (c *Client) providerExecutable(provider string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(provider))
	cfg, hasConfig := c.loadConfig()

	configured := ""
	var names []string
	switch key {
	case "claude":
		if hasConfig {
			configured = cfg.Providers.Claude.Bin
		}
		names = []string{"claude"}
	case "codex":
		if hasConfig {
			configured = cfg.Providers.Codex.Bin
		}
		names = []string{"codex"}
	case "agy", "antigravity":
		if hasConfig {
			configured = cfg.Providers.Agy.Bin
		}
		names = []string{"agy"}
	case "droid":
		if hasConfig {
			configured = cfg.Providers.ACP["droid"].Bin
		}
		names = []string{"droid"}
	case "copilot":
		if hasConfig {
			configured = cfg.Providers.Copilot.Bin
		}
		names = []string{"copilot"}
	case "pi":
		if hasConfig {
			configured = cfg.Providers.Pi.Bin
		}
		names = []string{"pi"}
	case "omp":
		if hasConfig {
			configured = cfg.Providers.Omp.Bin
		}
		names = []string{"omp"}
	case "commandcode":
		if hasConfig {
			configured = cfg.Providers.CommandCode.Bin
		}
		// `cmd` 是文档里的命令名，command-code/commandcode 是包自带的别名。
		names = []string{"command-code", "commandcode", "cmd"}
	case "cline":
		if hasConfig {
			configured = cfg.Providers.ACP["cline"].Bin
		}
		names = []string{"cline"}
	case "cursor", "opencode", "grok", "devin":
		if hasConfig {
			configured = cfg.Providers.ACP[key].Bin
		}
		names = []string{acp.DefaultBinary(acp.Agent(key))}
	}
	if configured != "" {
		return configured, nil
	}
	if len(names) == 0 {
		return "", fmt.Errorf("provider %s 没有已知的可执行文件", provider)
	}
	if path, ok := providerBinaryFinder(names, providerFallbackDirs(key)); ok {
		return path, nil
	}
	return "", fmt.Errorf("在登录 shell 的 PATH 与常见安装目录中都找不到 %s 的可执行文件（%s）: %w",
		provider, strings.Join(names, "/"), exec.ErrNotFound)
}

// errProviderAccountUnsupported 表示该提供方不提供账户信息。
var errProviderAccountUnsupported = errors.New("该提供方不提供账户信息")

// ProviderAccount 获取指定提供方的当前登录账户及套餐信息。
// 各来源：codex 读激活 home 的 auth.json ID Token，claude 读 .claude.json 的
// oauthAccount，agy 取默认账号的邮箱与标签，copilot 经 CLI 的 account.getCurrentAuth 读取。
func (c *Client) ProviderAccount(ctx context.Context, provider string) (mcodeui.ProviderAccountInfo, error) {
	key := strings.ToLower(strings.TrimSpace(provider))
	switch key {
	case "codex":
		home := codexAccountHome()
		info := mcodeui.ProviderAccountInfo{
			Provider: provider,
			Email:    codex.ReadAccountEmail(home),
			Plan:     codex.ReadAccountPlan(home),
		}
		if info.Email == "" && info.Plan == "" {
			return mcodeui.ProviderAccountInfo{}, fmt.Errorf("codex: %s 下没有已登录的账号", home)
		}
		return info, nil
	case "claude":
		email, plan, err := claude.ReadAccount("")
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: email, Plan: plan}, nil
	case "agy", "antigravity":
		if c.agy == nil {
			return mcodeui.ProviderAccountInfo{}, errors.New("agy accounts are not configured")
		}
		list := c.agy.List()
		if len(list) == 0 {
			return mcodeui.ProviderAccountInfo{}, errors.New("agy: 没有已登录的账号")
		}
		account := list[0]
		defaultID := c.agy.DefaultAccountID()
		for _, candidate := range list {
			if candidate.ID == defaultID {
				account = candidate
				break
			}
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: account.Email, Plan: account.Label}, nil
	case "copilot":
		bin, err := c.providerExecutable("copilot")
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		account, err := readCopilotAccount(ctx, bin)
		if err != nil {
			return mcodeui.ProviderAccountInfo{}, err
		}
		plan := account.Plan
		if account.NoCopilotAccess {
			plan = strings.TrimSpace(plan + " · no Copilot access")
		}
		return mcodeui.ProviderAccountInfo{Provider: provider, Email: account.Login, Plan: plan}, nil
	default:
		return mcodeui.ProviderAccountInfo{}, fmt.Errorf("ProviderAccount %s: %w", provider, errProviderAccountUnsupported)
	}
}

// codexAccountHome 返回当前生效的 Codex home：优先环境变量 CODEX_HOME（多账号切换时由它指定），
// 未设置时用 ~/.codex；解析用户主目录失败时返回空串，由调用方按未登录处理。
func codexAccountHome() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexQuotaWindows 把直连 Codex 的额度结果转成 UI 窗口。
func codexQuotaWindows(limits codex.RateLimits) []mcodeui.ProviderQuotaWindow {
	var windows []mcodeui.ProviderQuotaWindow
	add := func(account, plan, source string, readAt time.Time, list []codex.RateLimitWindow) int {
		start := len(windows)
		for _, w := range list {
			var resetsAt time.Time
			if w.ResetsAt != nil {
				resetsAt = *w.ResetsAt
			}
			windows = append(windows, mcodeui.ProviderQuotaWindow{
				ID: w.ID, Period: w.Period, Label: w.Title, UsedPercent: w.UsedPercent, ResetsAt: resetsAt,
				Account: account, Plan: plan, Source: source, ReadAt: readAt,
			})
		}
		return start
	}
	add("", limits.PlanType, codex.QuotaSourceOpenAI, limits.ReadAt, limits.Windows)
	return windows
}

// claudeQuotaWindows 把 Claude 的额度窗口映射成 UI 窗口：经 Magpie 网关时带上来源与
// 卡片元数据，直连时保持原来的字段（没有 Source、没有账号级路由信息）；路由命中时
// 只给本会话实际应答的账号标在用。
func claudeQuotaWindows(limits claude.PlanLimits, route sessionRoute, hasRoute bool) []mcodeui.ProviderQuotaWindow {
	windows := make([]mcodeui.ProviderQuotaWindow, 0, len(limits.Windows))
	magpieSource := limits.Source == codex.QuotaSourceMagpie
	for _, w := range limits.Windows {
		var resetsAt time.Time
		if w.ResetsAt != nil {
			resetsAt = *w.ResetsAt
		}
		source := ""
		if magpieSource {
			source = codex.QuotaSourceMagpie
		}
		windows = append(windows, mcodeui.ProviderQuotaWindow{
			ID: w.ID, Period: claudeQuotaPeriod(w.ID), Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: resetsAt, Account: w.Account,
			Source: source, Provider: w.Provider, User: w.User, Kind: w.Kind, Last: w.Last,
			LastServedAt: w.LastServedAt, ReadAt: w.ReadAt,
		})
	}
	markRouteActive(windows, route, hasRoute && magpieSource)
	return windows
}

// codexQuotaLimits 按 Codex 自己的有效配置（与实际会话一致的二进制、工作目录、
// CODEX_HOME）判定额度来源并读取。桌面端与远程端都走这里，来源判定保持一致。
func (c *Client) codexQuotaLimits(ctx context.Context, threadID string) (codex.RateLimits, error) {
	opts := codex.QuotaOptions{CodexHome: codexAccountHome()}
	if bin, err := c.providerExecutable("codex"); err == nil {
		opts.Binary = bin
	}
	if threadID != "" {
		if dir, ok := c.ThreadWorkDir(threadID); ok {
			opts.WorkDir = dir
		}
	}
	return codex.ReadQuotaLimits(ctx, opts)
}

// codexMagpieEndpoint 优先解析 Codex 注入路由；没有注入模型时再按 Codex 自己的
// 有效配置判断直连或 Magpie 来源。
func (c *Client) codexMagpieEndpoint(ctx context.Context, threadID string) (baseURL, apiKey string, ok bool, limits codex.RateLimits, err error) {
	threadID = strings.TrimSpace(threadID)
	if threadID != "" {
		if model, modelErr := readCodexThreadModel(ctx, c.backend, threadID); modelErr == nil {
			for _, route := range codexMagpieRoutesForQuota() {
				providerID := strings.TrimSpace(route.ProviderID)
				prefix := providerID + "/"
				if providerID != "" && strings.HasPrefix(model, prefix) && strings.TrimSpace(strings.TrimPrefix(model, prefix)) != "" {
					return route.BaseURL, route.APIKey, true, codex.RateLimits{}, nil
				}
			}
		}
	}

	limits, err = readCodexQuotaLimits(c, ctx, threadID)
	if err != nil {
		return "", "", false, codex.RateLimits{}, err
	}
	if limits.Source == codex.QuotaSourceMagpie && strings.TrimSpace(limits.GatewayBaseURL) != "" {
		return limits.GatewayBaseURL, limits.GatewayToken, true, limits, nil
	}
	return "", "", false, limits, nil
}

// RefreshProviderQuota 让 Magpie 网关重新向厂商读取指定提供方的额度卡片。
// 经 Magpie 端点的提供方需要这一步：网关平时只自动重读首选账号，手动刷新才能把非首选账号
// 也拉一遍；直连提供方的额度每次都由 ProviderQuota 直接读取，这里按空操作处理。
func (c *Client) RefreshProviderQuota(ctx context.Context, provider string) error {
	provider = strings.TrimSpace(provider)
	// claude 线程指向 Magpie 网关时，额度来自网关缓存的 claude 卡片，需要网关重读。
	if provider == "claude" {
		if _, err := claude.RefreshGatewayQuotas(ctx); err != nil {
			return err
		}
		return nil
	}
	// 内置引擎会话（mcode:<provider id>、magpie、magpie2…）同样经 Magpie 端点取额度。
	if engineQuotaKey(provider) {
		return c.refreshEngineQuota(ctx, provider)
	}
	providerID, ok := strings.CutPrefix(provider, "pi:")
	if !ok || strings.TrimSpace(providerID) == "" {
		return nil
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return nil
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	for _, target := range piMagpieTargets(cfg, secrets) {
		if target.providerID != strings.TrimSpace(providerID) || target.apiKey == "" {
			continue
		}
		// 弹层展示的是端点上的全部卡片（可能含多个账号），这里重读全部卡片。
		return magpie.RefreshQuotas(ctx, target.baseURL, target.apiKey, "")
	}
	return nil
}

// ProviderQuota 获取指定提供方的额度使用窗口列表。
func (c *Client) ProviderQuota(ctx context.Context, provider string) ([]mcodeui.ProviderQuotaWindow, error) {
	// pi 线程的额度来自 Magpie：provider 形如 pi:<pi provider id>，只有能映射到有 key 的
	// Magpie 端点时才读得到额度，其余（线程用的是 pi 自带的 provider）按未实现处理。
	if strings.HasPrefix(provider, "pi:") {
		return c.piMagpieQuota(ctx, strings.TrimPrefix(provider, "pi:"))
	}
	if engineQuotaKey(provider) {
		// 跑在 Magpie 上的内置引擎会话：mcode 的 <端点 provider id>/<模型 ID> 与
		// magpie、magpie2…magpie4 端点都与 pi 走同一份额度读取与「在用」标记。
		return c.engineQuotaWindows(ctx, provider)
	}
	if provider == "codex" {
		threadID := mcodeui.QuotaThread(ctx)
		baseURL, token, magpie, limits, err := c.codexMagpieEndpoint(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if magpie {
			sessionID := ""
			if threadID != "" && c.agySessionForThread != nil {
				sessionID = c.agySessionForThread(ctx, threadID)
			}
			return c.readMagpieQuotaWindows(ctx, baseURL, token, "codex", sessionID)
		}
		windows := codexQuotaWindows(limits)
		return windows, nil
	}
	if provider == "claude" {
		limits, err := claude.ReadPlanLimits(ctx)
		if err != nil {
			return nil, err
		}
		route, hasRoute := sessionRoute{}, false
		// 经 Magpie 网关时，额度来自网关缓存的 claude 卡片，再按 trace 记录标出本会话
		// 实际应答的账号；直连不涉及路由，映射结果保持原样。
		if limits.Source == codex.QuotaSourceMagpie {
			if baseURL, token, ok := claude.GatewayEndpoint(ctx); ok {
				tracker := c.sessionTracker()
				tracker.noteSource(baseURL, token)
				if threadID := mcodeui.QuotaThread(ctx); threadID != "" && c.agySessionForThread != nil {
					sessionID := c.agySessionForThread(ctx, threadID)
					route, hasRoute = tracker.routeFor(ctx, baseURL, token, "claude", sessionID, false)
				}
			}
		}
		return claudeQuotaWindows(limits, route, hasRoute), nil
	}
	if provider == "zai" || provider == "cline" || provider == "droid" {
		windows, err := readProviderPlanWindows(ctx, provider, c.creditsOptions(provider))
		if err != nil {
			if errors.Is(err, credits.ErrUnsupported) {
				return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
			}
			return nil, err
		}
		out := make([]mcodeui.ProviderQuotaWindow, 0, len(windows))
		for _, w := range windows {
			out = append(out, mcodeui.ProviderQuotaWindow{
				ID: w.ID, Period: w.Period, Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt,
			})
		}
		return out, nil
	}
	if provider != "agy" || c.agy == nil {
		return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
	}
	var windows []mcodeui.ProviderQuotaWindow
	// 只输出 Gemini 分组额度，Claude/GPT 分组与逐模型额度只供账户路由使用，不在界面展示
	groups := []string{
		quota.GroupGemini,
		quota.GroupGemini + " 7d",
	}
	view := c.agy.QuotaView()
	list := c.agy.List()

	// 先算每个账号的展示标题与有效启用状态（已合并路由覆盖）。
	titles := make(map[string]string, len(list))
	emails := make(map[string]string, len(list))
	enabled := make(map[string]bool, len(list))
	hasIsolatedEmail := false
	for _, account := range list {
		title := account.Label
		if title == "" {
			title = account.ID
		}
		// 默认以 UUID 作为标题的隔离账号改用邮箱，界面不出现一串随机 ID。
		if account.Email != "" && looksLikeUUID(title) {
			title = account.Email
		}
		titles[account.ID] = title
		emails[account.ID] = account.Email
		enabled[account.ID] = view.On[account.ID]
		if account.ID != accounts.SystemAccountID && account.Email != "" {
			hasIsolatedEmail = true
		}
	}

	// 系统账号登录的就是宿主 Google 账号：与同邮箱隔离账号指向同一个账号时只留一份。
	// 优先保留有效启用的那个，状态相同时保留隔离账号（它有私有 token，额度更准）；
	// 解析不到系统账号邮箱时不去重。
	systemEmail := emails[accounts.SystemAccountID]
	if systemEmail == "" && hasIsolatedEmail {
		systemEmail = c.agy.SystemAccountEmail(ctx)
	}
	merged := make(map[string]string, len(list)) // 被合并掉的账号 ID → 保留的账号 ID
	if systemEmail != "" {
		for _, account := range list {
			if account.ID == accounts.SystemAccountID || !strings.EqualFold(emails[account.ID], systemEmail) {
				continue
			}
			if enabled[account.ID] && !enabled[accounts.SystemAccountID] {
				merged[accounts.SystemAccountID] = account.ID
			} else if enabled[accounts.SystemAccountID] && !enabled[account.ID] {
				merged[account.ID] = accounts.SystemAccountID
			} else {
				// 状态相同：保留隔离账号（有私有 token，额度更准）。
				merged[accounts.SystemAccountID] = account.ID
			}
		}
	}

	// 「在用」优先落在当前会话实际绑定的账号上：view.Target 只说明新会话会路由到哪，
	// 会话一旦绑定过账号，接着聊仍然用那个号，两者可能不一致。
	// 绑定的账号后来被停用（路由覆盖关掉）时不再算在用，退回路由目标，否则界面没有在用账号。
	target := view.Target
	if bound, ok := c.agyBoundAccountForThread(ctx); ok {
		if _, exists := titles[bound]; exists && enabled[bound] {
			target = bound
		}
	}

	for _, account := range list {
		if _, ok := merged[account.ID]; ok {
			continue
		}
		label := titles[account.ID]
		// Active 只给当前在用账号：本会话绑定过账号时就是绑定的那个（新会话则是路由目标）；
		// 停用账号一律不算在用，由 Disabled 交给界面显示「已停止」。
		on := enabled[account.ID]
		active := on && target != "" && (account.ID == target || merged[target] == account.ID)
		for _, group := range groups {
			q, ok := account.Quota[group]
			if !ok {
				continue
			}
			resetAt, _ := time.Parse(time.RFC3339, q.ResetAt)
			windows = append(windows, mcodeui.ProviderQuotaWindow{
				ID: group, Period: agyQuotaGroupPeriod(group),
				Label: label + " · " + group, UsedPercent: q.UsedPercent, ResetsAt: resetAt,
				Account: label, Active: active, Disabled: !on,
			})
		}
	}
	return windows, nil
}

// agyBoundAccountForThread 返回当前会话已绑定的 agy 账号 ID。会话还没跑过一轮（没有会话 ID）、
// 装配层没注解析函数，或该会话尚未绑定账号时返回 false，调用方退回新会话的目标账号。
func (c *Client) agyBoundAccountForThread(ctx context.Context) (string, bool) {
	if c == nil || c.agy == nil || c.agySessionForThread == nil {
		return "", false
	}
	threadID := mcodeui.QuotaThread(ctx)
	if threadID == "" {
		return "", false
	}
	conversationID := c.agySessionForThread(ctx, threadID)
	if conversationID == "" {
		return "", false
	}
	return c.agy.BoundAccountID(conversationID)
}

// looksLikeUUID 判断 s 是否是 8-4-4-4-12 的十六进制 UUID（隔离账号默认以 UUID 当标题）。
func looksLikeUUID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// claudeQuotaPeriod 把 Claude 窗口 ID 映射成周期标记：session/five_hour 是 5 小时窗口，
// weekly_all/seven_day 是周窗口；其余（按模型限定的窗口、extra-usage）不给周期，
// 留给 UI 从标题推断，避免凭猜测贴周期标签。
func claudeQuotaPeriod(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "session", "five_hour", "five-hour", "fivehour":
		return "5h"
	case "weekly_all", "weekly", "seven_day", "seven-day", "sevenday":
		return "7d"
	}
	return ""
}

// agyQuotaGroupPeriod 把 agy 分组名映射成周期标记：带「 7d」后缀的分组是周窗口，其余是 5 小时窗口。
func agyQuotaGroupPeriod(group string) string {
	if strings.HasSuffix(strings.TrimSpace(group), " 7d") {
		return "7d"
	}
	return "5h"
}

// creditsOptions 构造 credits 包读取所需依赖；secrets 为 nil 时只用环境变量与主目录。
func (c *Client) creditsOptions(provider string) credits.Options {
	opts := credits.Options{}
	if c.secrets != nil {
		if key, err := c.secrets.Get(provider + ".apiKey"); err == nil {
			opts.APIKey = key
		}
	}
	env := make(map[string]string)
	for _, kv := range loginenv.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	opts.Env = env
	if home, err := os.UserHomeDir(); err == nil {
		opts.HomeDir = home
	}
	return opts
}

// ProviderCredits 获取按量计费提供方的预付费余额；提供方不支持时返回 ErrNotImplemented。
func (c *Client) ProviderCredits(ctx context.Context, provider string) (mcodeui.ProviderCredits, error) {
	if !credits.ExposesCredits(provider) {
		return mcodeui.ProviderCredits{}, fmt.Errorf("ProviderCredits: %w", mcodeui.ErrNotImplemented)
	}
	got, err := readProviderCredits(ctx, provider, c.creditsOptions(provider))
	if err != nil {
		return mcodeui.ProviderCredits{}, err
	}
	return mcodeui.ProviderCredits{
		Provider:      provider,
		Total:         got.Total,
		Currency:      got.Currency,
		AmountText:    got.AmountText(),
		BreakdownText: got.BreakdownText(),
		Usable:        got.Usable,
		ReadAt:        got.ReadAt,
	}, nil
}
