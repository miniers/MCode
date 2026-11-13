package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/attention"
	"mcode/internal/automation"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/kanban"
	"mcode/internal/mcp"
	"mcode/internal/nmem"
	"mcode/internal/procattr"
	"mcode/internal/projctx"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/claude"
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
	readCodexThreadModel      = readThreadModel
)

func readThreadModel(ctx context.Context, backend *remote.MemoryBackend, threadID string) (string, error) {
	if backend == nil || strings.TrimSpace(threadID) == "" {
		return "", nil
	}
	thread, err := backend.GetThread(ctx, threadID)
	if err != nil {
		return "", err
	}
	return derefString(thread.Model), nil
}

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

func wrapConflictErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		return fmt.Errorf("%w: %v", mcodeui.ErrRevisionConflict, err)
	}
	return err
}

const goalOwnerSteward = "steward"

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

// applySwarmThinking 在 bee 长时间无进展时把活动文案替换为「思考中 N 分钟…」。
func applySwarmThinking(src []swarm.Bee, bees []mcodeui.SwarmBee) {
	for i := range bees {
		if i < len(src) && src[i].ThinkingMinutes > 0 {
			bees[i].Activity = fmt.Sprintf("思考中 %d 分钟…", src[i].ThinkingMinutes)
		}
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

// memoryTimeoutCtx 返回带 10 秒超时的调用上下文，用于记忆提议的本地读写与远端请求。
func memoryTimeoutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), memoryRequestTimeout)
}

// memoryRequestTimeout 是记忆相关的单次本地或远端请求超时。
const memoryRequestTimeout = 10 * time.Second

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

// nowledgeThreadIDPrefix 是导入 Nowledge Threads 时给 mcode 会话 ID 加的前缀。
// 对应 internal/remote/nmem_sync.go 的同名常量，两边必须保持一致。
const nowledgeThreadIDPrefix = "mcode-"

// memoryCallCtx 在调用方给定的 ctx 上叠一个记忆请求超时；ctx 为空时从 Background 起算。
func memoryCallCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, memoryRequestTimeout)
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

// errProviderAccountUnsupported 表示该提供方不提供账户信息。
var errProviderAccountUnsupported = errors.New("该提供方不提供账户信息")

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
