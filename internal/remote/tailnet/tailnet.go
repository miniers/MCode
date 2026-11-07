// Package tailnet 封装内嵌的 Tailscale 节点（tsnet）：启停、状态机与 WhoIs。
// 这里只做节点生命周期与身份查询，不含 HTTP 逻辑，也不引用 mcode 的其他内部包，
// 避免与 internal/remote 形成循环依赖。
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"tailscale.com/tsnet"
)

// State 是 Tailscale 入口的五态。
type State string

const (
	// StateDisabled 表示未启用，或已停止。
	StateDisabled State = "disabled"
	// StateLoginRequired 表示节点在等用户到浏览器完成登录，AuthURL 非空时可直接打开。
	StateLoginRequired State = "login_required"
	// StateNeedsHTTPS 表示 tailnet 没开 MagicDNS 或 HTTPS Certificates，ListenTLS 拿不到证书。
	StateNeedsHTTPS State = "needs_https"
	// StateRunning 表示节点已加入 tailnet，FQDN 可用。
	StateRunning State = "running"
	// StateFailed 表示启动或监听失败，节点不可用，需要重试。
	StateFailed State = "failed"
)

// DefaultHostname 是 Options.Hostname 为空时使用的默认主机名。
const DefaultHostname = "mcode"

// loginPollInterval 是等待登录期间轮询 LocalAPI 取 AuthURL 的间隔。
const loginPollInterval = 2 * time.Second

// runningPollInterval 是节点上线后轮询 LocalAPI 的间隔，用来发现改名与密钥过期。
// 声明成变量是为了让测试缩短等待。
var runningPollInterval = 10 * time.Second

// 以下两个取值是 ipn.State 的字符串形式（tailscale.com/ipn），Status.BackendState 直接用它们。
const (
	backendStateNeedsLogin = "NeedsLogin"
	backendStateRunning    = "Running"
)

// alreadyClosed 是一个已关闭的通道，供从未启动过的 Manager 作为 Done 的返回值。
var alreadyClosed = func() <-chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}()

// closeServer 关闭 tsnet.Server；抽成变量是为了让测试验证 finish 只关一次。
var closeServer = func(server *tsnet.Server) error {
	return server.Close()
}

// redactedAuthURL 是日志里替换登录链接的占位符：链接本身就等价于登录凭据。
const redactedAuthURL = "[redacted-auth-url]"

// authURLPattern 匹配 tsnet 打进日志的登录链接（https://主机/a/令牌），日志里必须脱敏。
var authURLPattern = regexp.MustCompile(`https://[^\s/]+/a/[^\s]+`)

// Status 是 Tailscale 入口的当前状态快照，零值等价于 StateDisabled。
type Status struct {
	State   State
	AuthURL string // 仅 login_required 有值
	FQDN    string // 仅 running 有值，已去掉末尾的点
	Error   string // 失败原因，供 UI 直接展示
}

// Identity 是通过 WhoIs 解析出的对端身份，只用于展示与审计，不参与鉴权。
type Identity struct {
	LoginName    string // UserProfile.LoginName
	NodeName     string // Node.Name，FQDN 带尾点
	ComputedName string // Node.ComputedName，MagicDNS 短名
}

// Options 是 Manager 的创建参数。
type Options struct {
	Dir      string // 存节点密钥与 tailscaled.state 的状态目录；为空时不允许启动
	Hostname string // 默认 "mcode"
	Logf     func(format string, args ...any)
}

// Manager 管理一个内嵌的 tsnet 节点。可以反复 Start / Stop，每次 Start 都新建 tsnet.Server，
// 状态由互斥锁保护，可并发读取。
type Manager struct {
	opts Options

	mu        sync.Mutex
	status    Status
	server    *tsnet.Server
	cancel    context.CancelFunc
	done      chan struct{} // 后台协程退出后关闭
	closed    chan struct{} // tsnet.Server 彻底关闭后关闭，供下一次启动等待
	closeOnce *sync.Once    // 保护 tsnet.Server.Close，只调一次
	started   bool
}

// New 创建 Manager，此时不连接 tailnet，状态为 StateDisabled。
func New(opts Options) *Manager {
	if opts.Hostname == "" {
		opts.Hostname = DefaultHostname
	}
	return &Manager{opts: opts, status: Status{State: StateDisabled}}
}

// Start 用 0700 权限准备好状态目录，创建 tsnet.Server 后立即返回；
// 登录进度由后台协程推进，调用方轮询 Status 观察。
// ctx 约束后台协程的生命周期，Stop 会取消它。
func (m *Manager) Start(ctx context.Context) error {
	if m.opts.Dir == "" {
		return errors.New("tailnet: 未配置状态目录")
	}
	if err := prepareDir(m.opts.Dir); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("tailnet: 已经在运行，先调用 Stop")
	}
	runCtx, cancel := context.WithCancel(ctx)
	server := &tsnet.Server{
		Dir:      m.opts.Dir,
		Hostname: m.opts.Hostname,
		Logf:     m.opts.Logf,
		// tsnet 默认经 UserLogf 把登录链接打到 log.Printf，这里先脱敏再输出。
		UserLogf: redactLogf(m.opts.Logf),
	}
	m.server = server
	m.cancel = cancel
	done := make(chan struct{})
	closed := make(chan struct{})
	m.done = done
	m.closed = closed
	m.closeOnce = new(sync.Once)
	m.started = true
	// 启动到 Up 返回之间都算在等登录/连接，拿到授权地址后再补 AuthURL。
	m.status = Status{State: StateLoginRequired}
	m.mu.Unlock()

	go m.run(runCtx, server, done)
	return nil
}

// Done 返回本节点彻底关闭后才会关闭的通道：下一次启动要等它，避免两个节点共用状态目录。
// 从未 Start 过，或已经收尾复位时，返回一个已关闭的通道。
func (m *Manager) Done() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed != nil {
		return m.closed
	}
	return alreadyClosed
}

// Stop 取消后台协程、等它退出后关闭 tsnet.Server，幂等；从没 Start 过时直接返回 nil，
// 绝不调用 Close（tsnet 规定 Close 不能早于 Start，也不能与 Start 并发）。
// 关闭完成后状态回到 StateDisabled。
func (m *Manager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	server := m.server
	cancel := m.cancel
	done := m.done
	closed := m.closed
	closeOnce := m.closeOnce
	started := m.started
	m.mu.Unlock()

	if !started || server == nil {
		return nil
	}
	// 后台协程里的 Up 会触发 tsnet.Start，只有它返回之后 Close 才安全。
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			// 超时也不能放任不管：tsnet 规定 Close 不能与 Start 并发，所以等后台
			// 协程真正退出后再关闭服务器，避免既不关服务器又永久卡在 started。
			go func() {
				<-done
				_ = m.finish(server, closeOnce, closed)
			}()
			return ctx.Err()
		}
	}
	return m.finish(server, closeOnce, closed)
}

// finish 关闭 tsnet.Server 并把 Manager 复位到未启用态，只能在后台协程退出后调用。
// 只在 m.server 仍是当初那个 server 时清字段，避免覆盖之后重新 Start 的状态。
func (m *Manager) finish(server *tsnet.Server, closeOnce *sync.Once, closed chan struct{}) error {
	var closeErr error
	if closeOnce != nil {
		// Stop 超时后可能再次调用 finish，关闭动作与通道都只做一次。
		closeOnce.Do(func() {
			closeErr = closeServer(server)
			if closed != nil {
				close(closed)
			}
		})
	}
	m.mu.Lock()
	if m.server == server {
		m.server = nil
		m.cancel = nil
		m.done = nil
		m.closed = nil
		m.closeOnce = nil
		m.started = false
		// 无论 Close 是否报错都回到未启用态，避免 UI 停在运转中的假象。
		m.status = Status{State: StateDisabled}
	}
	m.mu.Unlock()
	return closeErr
}

// Listener 在 tailnet 的 443 端口上监听，返回的 listener 已完成 TLS 握手。
// 失败时把原因写进状态：证书 / MagicDNS 相关错误落 StateNeedsHTTPS，其余落 StateFailed。
func (m *Manager) Listener() (net.Listener, error) {
	server := m.currentServer()
	if server == nil {
		return nil, errors.New("tailnet: 尚未启动")
	}
	ln, err := server.ListenTLS("tcp", ":443")
	if err != nil {
		if needsHTTPS(err) {
			m.setStatus(Status{State: StateNeedsHTTPS, Error: err.Error()})
		} else {
			m.setStatus(Status{State: StateFailed, Error: err.Error()})
		}
		return nil, err
	}
	return ln, nil
}

// Status 返回当前状态快照，可并发调用。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// WhoIs 解析对端身份，只用于展示与审计；未启动时返回错误。
func (m *Manager) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	server := m.currentServer()
	if server == nil {
		return Identity{}, errors.New("tailnet: 尚未启动")
	}
	client, err := server.LocalClient()
	if err != nil {
		return Identity{}, fmt.Errorf("tailnet: 获取本地客户端失败: %w", err)
	}
	resp, err := client.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, fmt.Errorf("tailnet: 查询对端身份失败: %w", err)
	}
	var id Identity
	if resp != nil {
		if resp.UserProfile != nil {
			id.LoginName = resp.UserProfile.LoginName
		}
		if resp.Node != nil {
			id.NodeName = resp.Node.Name
			id.ComputedName = resp.Node.ComputedName
		}
	}
	return id, nil
}

// run 是后台协程：一边轮询登录地址，一边等 Up 返回，成功即置为运行中，
// 之后继续轮询直到被取消，这样改名与密钥过期都能反映到状态里。
// done 由 Start 创建，退出时关闭，Stop 据此等后台协程收尾。
func (m *Manager) run(ctx context.Context, server *tsnet.Server, done chan struct{}) {
	defer close(done)

	// Up 在等用户登录时不会返回，所以要另开协程从 LocalAPI 读 AuthURL。
	loginDone := make(chan struct{})
	var loginWG sync.WaitGroup
	loginWG.Add(1)
	go func() {
		defer loginWG.Done()
		m.watchLogin(ctx, server, loginDone)
	}()

	st, err := server.Up(ctx)
	close(loginDone)
	loginWG.Wait()

	if err != nil {
		if ctx.Err() != nil {
			return // 被 Stop 取消，状态由 Stop 收尾
		}
		// 整体替换状态，避免残留上一次的 AuthURL / login_required。
		m.setStatus(Status{State: StateFailed, Error: fmt.Sprintf("tailnet: 连接失败: %v", err)})
		return
	}
	var fqdn string
	if st != nil && st.Self != nil {
		fqdn = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	m.setStatus(Status{State: StateRunning, FQDN: fqdn})

	// 上线后不能退出：节点可能改名，也可能因为密钥过期被登出，都要同步到状态。
	m.watchRunning(ctx, server)
}

// watchRunning 定时读 LocalAPI 同步状态，ctx 取消（Stop）时退出。
func (m *Manager) watchRunning(ctx context.Context, server *tsnet.Server) {
	ticker := time.NewTicker(runningPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.syncRunning(ctx, server)
	}
}

// syncRunning 读一次 LocalAPI 状态并在必要时更新；读失败时跳过本轮，等下个间隔。
func (m *Manager) syncRunning(ctx context.Context, server *tsnet.Server) {
	client, err := server.LocalClient()
	if err != nil {
		return
	}
	st, err := client.Status(ctx)
	if err != nil || st == nil {
		return
	}
	dnsName := ""
	if st.Self != nil {
		dnsName = st.Self.DNSName
	}
	if next, ok := runningStatusUpdate(st.BackendState, st.AuthURL, dnsName); ok {
		m.setStatus(next)
	}
}

// runningStatusUpdate 把 LocalAPI 的 BackendState / AuthURL / DNSName 映射成对外状态。
// 需要重新登录时带上授权地址；正常运行时用去掉尾点的 FQDN；其他状态（如 Starting）
// 在网络切换时只是短暂出现，保持原状态不动，避免界面来回跳。
func runningStatusUpdate(backendState, authURL, dnsName string) (Status, bool) {
	switch backendState {
	case backendStateNeedsLogin:
		return Status{State: StateLoginRequired, AuthURL: authURL}, true
	case backendStateRunning:
		fqdn := strings.TrimSuffix(dnsName, ".")
		if fqdn == "" {
			// 没有域名就无法拼出 https 地址与 AllowedHosts，留着旧状态更安全。
			return Status{}, false
		}
		return Status{State: StateRunning, FQDN: fqdn}, true
	default:
		return Status{}, false
	}
}

// watchLogin 在等待登录期间反复读 LocalAPI，把授权地址写进状态。
func (m *Manager) watchLogin(ctx context.Context, server *tsnet.Server, stop <-chan struct{}) {
	ticker := time.NewTicker(loginPollInterval)
	defer ticker.Stop()
	for {
		if url := authURL(ctx, server); url != "" {
			m.setStatus(Status{State: StateLoginRequired, AuthURL: url})
		}
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// authURL 读一次 LocalAPI 状态里的授权地址，读不到时返回空串。
func authURL(ctx context.Context, server *tsnet.Server) string {
	client, err := server.LocalClient()
	if err != nil {
		return ""
	}
	st, err := client.Status(ctx)
	if err != nil || st == nil {
		return ""
	}
	return st.AuthURL
}

// needsHTTPS 判断 ListenTLS 的错误是否指向 tailnet 没开 MagicDNS / HTTPS Certificates。
// tsnet 的两条真实文案分别是 "you must enable MagicDNS in the DNS page of the admin
// panel to proceed" 与 "you must enable HTTPS in the admin panel to proceed"，
// 只按这两句匹配，避免超时、连接失败里出现的 https 字样被误判。
func needsHTTPS(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "enable https") ||
		strings.Contains(text, "enable magicdns")
}

// currentServer 返回正在运行的 tsnet.Server，未启动时返回 nil。
func (m *Manager) currentServer() *tsnet.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil
	}
	return m.server
}

// setStatus 整体替换状态，所有写入都经过这里由互斥锁保护。
func (m *Manager) setStatus(st Status) {
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
}

// prepareDir 创建状态目录并收紧到 0700：目录里存着节点私钥，已存在的目录也要改权限。
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tailnet: 创建状态目录失败: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("tailnet: 收紧状态目录权限失败: %w", err)
	}
	return nil
}

// redactLogf 包装调用方给的日志函数，先把登录链接脱敏再交给它；未配置时退回 log.Printf。
func redactLogf(logf func(format string, args ...any)) func(format string, args ...any) {
	if logf == nil {
		logf = log.Printf
	}
	return func(format string, args ...any) {
		logf("%s", redactAuthURL(fmt.Sprintf(format, args...)))
	}
}

// redactAuthURL 把文本里的登录链接替换成占位符，避免授权令牌随日志落盘。
func redactAuthURL(text string) string {
	return authURLPattern.ReplaceAllString(text, redactedAuthURL)
}
