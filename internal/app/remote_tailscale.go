package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/tailnet"
	mcodeui "mcode/internal/ui"
	"mcode/internal/webassets"
)

const (
	// tailscaleDirName 是内嵌 tsnet 节点状态目录的名字，放在 dataDir 下。
	tailscaleDirName = "tailnet"
	// tailscalePollInterval 是等待节点登录/上线时轮询状态的间隔。
	tailscalePollInterval = 500 * time.Millisecond
	// tailscaleStopTimeout 限制停止时等待 Manager.Stop 的最长时间。
	tailscaleStopTimeout = 5 * time.Second
)

var (
	// tailscalePrevWaitTimeout 限制新节点等旧节点退出的最长时间；测试里会调小。
	tailscalePrevWaitTimeout = 15 * time.Second
	// tailscaleWatchInterval 是运行中检查节点域名与登录态的间隔；测试里会调小。
	tailscaleWatchInterval = 5 * time.Second
)

// tailnetManager 是 internal/remote/tailnet.Manager 在本包内的抽象，便于测试替换实现。
type tailnetManager interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// Done 返回节点彻底关闭后才关闭的通道：新节点启动前要等它。
	Done() <-chan struct{}
	Listener() (net.Listener, error)
	Status() tailnet.Status
	WhoIs(ctx context.Context, remoteAddr string) (tailnet.Identity, error)
}

// newTailnetManager 创建 tailnet 管理器；测试可替换成 fake。
var newTailnetManager = func(opts tailnet.Options) tailnetManager {
	return tailnet.New(opts)
}

// startTailscaleLocked 在已持有 s.mu 时启动 Tailscale 入口：只做启动准备，实际登录、
// 监听与服务都放到后台协程，避免 Start 被 tailnet 阻塞。
func (s *RemoteService) startTailscaleLocked(cfg config.RemoteConfig, dispatcher *remote.Dispatcher) {
	if dispatcher == nil || s.tailscaleCancel != nil {
		return // 没有 dispatcher 或已在启动/运行
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.tailscaleCancel = cancel
	s.tailscaleErr = ""
	s.tailscaleFailState = ""
	s.tailscaleURL.Store("")
	opts := tailnet.Options{
		Dir:      filepath.Join(s.dataDir, tailscaleDirName),
		Hostname: cfg.TailscaleHost(),
	}
	// 在 lifecycle 锁内取值：后台协程只读传下去的参数、不再读包级变量，
	// 测试里改小间隔后再还原时就不会与还在跑的协程竞争（-race 下才暴露）。
	prevWait := tailscalePrevWaitTimeout
	watchEvery := tailscaleWatchInterval
	// 上一个节点可能还在收尾：把它的关闭通道交下去，新节点等它退出后再建。
	go s.runTailscale(ctx, opts, dispatcher, s.tailscalePrevDone, prevWait, watchEvery)
}

// runTailscale 是 Tailscale 入口的后台协程：建管理器、等节点上线、在 tailnet 443 监听并提供服务。
// 任何失败都经 failTailscale 收尾并落成失败状态，不影响 LAN 与 TLS 入口。
// prev 是上一个节点的关闭通道，非 nil 时要等它关闭后才建新节点。
// prevWait 是等待旧节点退出的上限，watchEvery 是运行中监视节点的间隔，都由启动方在锁内读好传入。
func (s *RemoteService) runTailscale(ctx context.Context, opts tailnet.Options, dispatcher *remote.Dispatcher, prev <-chan struct{}, prevWait, watchEvery time.Duration) {
	if prev != nil {
		// tsnet 的两个节点不能同时打开同一个状态目录，否则节点密钥会互相踩踏。
		select {
		case <-prev:
		case <-ctx.Done():
			return
		case <-time.After(prevWait):
			s.failTailscale(ctx, tailnet.StateFailed, "上一个 Tailscale 节点仍未退出，请稍后重试")
			return
		}
	}
	mgr := newTailnetManager(opts)
	if err := mgr.Start(ctx); err != nil {
		s.failTailscale(ctx, tailnet.StateFailed, err.Error())
		return
	}

	// 登记管理器，Stop / ResetTailscale 才能找到它；启动过程中被取消时在这里自行收尾。
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		_ = stopTailnetManager(context.Background(), mgr)
		return
	}
	s.tailscale = mgr
	s.mu.Unlock()

	st, ok := s.waitTailscaleRunning(ctx, mgr)
	if !ok {
		// ctx 已取消时 failTailscale 自动不做事，收尾交给 Stop。
		s.failTailscale(ctx, st.State, st.Error)
		return
	}
	if st.FQDN == "" {
		// 没有 FQDN 就无法拼出 https 地址与 AllowedHosts，入口无法服务。
		s.failTailscale(ctx, tailnet.StateFailed, "tailnet 状态缺少 FQDN")
		return
	}
	ln, err := mgr.Listener()
	if err != nil {
		// tailnet 没开 MagicDNS / HTTPS 时保留管理器上报的状态，界面才能给出具体指引。
		if cur := mgr.Status(); cur.State == tailnet.StateNeedsHTTPS {
			s.failTailscale(ctx, tailnet.StateNeedsHTTPS, cur.Error)
		} else {
			s.failTailscale(ctx, tailnet.StateFailed, err.Error())
		}
		return
	}

	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		_ = ln.Close()
		_ = stopTailnetManager(context.Background(), mgr)
		return
	}
	srv := remote.NewServer(remote.ServerOptions{
		Handler:       dispatcher,
		Assets:        webassets.FS(),
		ListenAddr:    ":443",
		Entry:         remote.EntryTailscale,
		AllowedHosts:  []string{st.FQDN},
		AdminRoutes:   s.adminRoutesLocked(),
		AnswerHandler: s.answerHandlerLocked(remote.EntryTailscale),
		WhoIs: func(whoCtx context.Context, remoteAddr string) (remote.ClientIdentity, error) {
			return s.identifyTailnetPeer(mgr, whoCtx, remoteAddr)
		},
	})
	s.tailscaleSrv = srv
	s.tailscaleLn = ln
	s.tailscaleErr = ""
	// 入口真正开始服务后才写 URL，配对主链接据此从 TLS/LAN 切到 Tailscale。
	s.tailscaleURL.Store("https://" + st.FQDN)
	s.mu.Unlock()

	// 入口服务期间继续盯着节点：改名要换入口，掉线要让配对链接回落。
	go s.watchTailscale(ctx, mgr, st.FQDN, watchEvery)

	// listener 由 tsnet 完成 TLS 握手，这里只设置 http.Server 需要的 NextProtos。
	serveErr := srv.ServeTLSListener(ln, &tls.Config{NextProtos: []string{"http/1.1"}})
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		// 入口已挂，配对主链接必须回落到 LAN / TLS，不能再指向死掉的 https 地址。
		s.failTailscale(ctx, tailnet.StateFailed, serveErr.Error())
	}
}

// watchTailscale 在入口服务期间监视节点状态：
//   - 域名变了：旧域名已不再解析，重建入口；
//   - 节点不在运行（掉线、等重新登录）：配对主链接回落到 LAN / TLS；
//   - 回到运行且域名没变：恢复 Tailscale 主链接。
//
// ctx 由启动方创建，停用或重新启用时会被取消，本协程随之退出。
// every 是检查间隔，由启动方在 lifecycle 锁内读好传入，本协程不读包级变量。
func (s *RemoteService) watchTailscale(ctx context.Context, mgr tailnetManager, servedFQDN string, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		st := mgr.Status()
		switch {
		case st.State == tailnet.StateRunning && st.FQDN != "" && st.FQDN != servedFQDN:
			s.restartTailscale(ctx)
			return
		case st.State != tailnet.StateRunning:
			s.mu.Lock()
			// 已经被换掉的旧入口不能动新会话的配对链接。
			if s.tailscale == mgr {
				s.tailscaleURL.Store("")
			}
			s.mu.Unlock()
		case st.FQDN == servedFQDN:
			s.mu.Lock()
			if s.tailscale == mgr {
				if cur, _ := s.tailscaleURL.Load().(string); cur == "" {
					s.tailscaleURL.Store("https://" + servedFQDN)
				}
			}
			s.mu.Unlock()
		}
	}
}

// restartTailscale 在节点域名变化后重建入口。全程持 lifecycle，与 Stop / Reset / 启停串行；
// 旧节点的阻塞收尾放在 s.mu 外执行，期间状态查询与配对不阻塞。
func (s *RemoteService) restartTailscale(ctx context.Context) {
	// 配置文件读取放在锁外，避免持锁做磁盘 IO。
	cfg := s.loadRemoteConfig()

	s.lifecycle.Lock()
	s.mu.Lock()
	if ctx.Err() != nil {
		// 已被 Stop 或停用：收尾由调用方负责，这里不能再起新节点。
		s.mu.Unlock()
		s.lifecycle.Unlock()
		return
	}
	teardown := s.detachTailscaleLocked()
	s.mu.Unlock()
	// detach 已经取消了旧 ctx，之后不能再拿它当条件；收尾用独立 ctx 等旧节点退出。
	_ = teardown(context.Background())

	s.mu.Lock()
	if s.state == mcodeui.RemoteStateRunning && cfg.TailscaleEnabled() {
		s.startTailscaleLocked(cfg, s.dispatcher)
	}
	s.mu.Unlock()
	s.lifecycle.Unlock()
}

// waitTailscaleRunning 轮询管理器状态，直到节点上线、需要用户处理（证书 / 失败）或启动被取消。
func (s *RemoteService) waitTailscaleRunning(ctx context.Context, mgr tailnetManager) (tailnet.Status, bool) {
	ticker := time.NewTicker(tailscalePollInterval)
	defer ticker.Stop()
	for {
		st := mgr.Status()
		switch st.State {
		case tailnet.StateRunning:
			return st, true
		case tailnet.StateNeedsHTTPS, tailnet.StateFailed:
			// 需要用户处理或已经失败：原因已写进管理器状态，交回调用方落成失败状态。
			return st, false
		}
		select {
		case <-ctx.Done():
			return st, false
		case <-ticker.C:
		}
	}
}

// identifyTailnetPeer 把 tsnet 的 WhoIs 身份映射成 remote.ClientIdentity，并记下最近一次连接身份。
func (s *RemoteService) identifyTailnetPeer(mgr tailnetManager, ctx context.Context, remoteAddr string) (remote.ClientIdentity, error) {
	id, err := mgr.WhoIs(ctx, remoteAddr)
	if err != nil {
		// WhoIs 失败会让请求拿不到身份，日志里留下失败原因，方便排查手机端的连接。
		log.Printf("remote tailscale: WhoIs %s 失败: %v", remoteAddr, err)
		return remote.ClientIdentity{}, err
	}
	node := id.ComputedName
	if node == "" {
		node = id.NodeName
	}
	identity := remote.ClientIdentity{LoginName: id.LoginName, NodeName: node}
	s.mu.Lock()
	// 旧连接可能在入口已经换掉之后才回来，不能让它覆盖新会话的身份。
	if s.tailscale == mgr {
		s.tailscaleWho = identity
	}
	s.mu.Unlock()
	return identity, nil
}

// failTailscale 把入口记为失败：先摘下会话，再在持 lifecycle 锁时完成阻塞收尾。
// 持 lifecycle 是为了不让用户重试时新节点与正在关闭的旧节点同时占用状态目录。
func (s *RemoteService) failTailscale(ctx context.Context, state tailnet.State, text string) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	if ctx != nil && ctx.Err() != nil {
		// 已被 Stop 取消：收尾由 Stop 负责，这里不再覆盖状态。
		s.mu.Unlock()
		return
	}
	teardown := s.detachTailscaleLocked()
	s.tailscaleErr = text
	s.tailscaleFailState = state
	s.mu.Unlock()
	_ = teardown(context.Background())
}

// detachTailscaleLocked 在已持有 s.mu 时摘下 Tailscale 入口的全部状态并取消后台协程，幂等。
// 返回的函数负责在不持 s.mu 时做阻塞收尾，永不为 nil，可直接调用；
// 分开的原因：持 s.mu 调 Shutdown 时，正在 WhoIs 的 handler 要拿 s.mu 写身份，
// Shutdown 只能等到超时，期间 Status / Pairing 全部冻结。
func (s *RemoteService) detachTailscaleLocked() func(ctx context.Context) error {
	mgr := s.tailscale
	srv := s.tailscaleSrv
	ln := s.tailscaleLn
	cancel := s.tailscaleCancel
	s.tailscale = nil
	s.tailscaleSrv = nil
	s.tailscaleLn = nil
	s.tailscaleCancel = nil
	s.tailscaleWho = remote.ClientIdentity{}
	s.tailscaleErr = ""
	s.tailscaleFailState = ""
	s.tailscaleURL.Store("")
	// 记下关闭通道：下一次启动要等这个节点彻底退出，否则两个节点会共用一个状态目录。
	if mgr != nil {
		s.tailscalePrevDone = mgr.Done()
	}

	if cancel != nil {
		cancel()
	}
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		if srv != nil {
			// 给 Shutdown 一个上限：等待不能无界，避免卡住整条 Stop / Reset 流程。
			shutdownCtx := ctx
			if _, ok := shutdownCtx.Deadline(); !ok {
				var cancelShutdown context.CancelFunc
				shutdownCtx, cancelShutdown = context.WithTimeout(shutdownCtx, tailscaleStopTimeout)
				defer cancelShutdown()
			}
			_ = srv.Shutdown(shutdownCtx)
		}
		if ln != nil {
			_ = ln.Close()
		}
		if mgr != nil {
			return stopTailnetManager(ctx, mgr)
		}
		return nil
	}
}

// stopTailnetManager 用固定超时关闭管理器，避免 tsnet 收尾卡住整条停止流程。
func stopTailnetManager(ctx context.Context, mgr tailnetManager) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(ctx, tailscaleStopTimeout)
	defer cancel()
	return mgr.Stop(stopCtx)
}

// TailscaleStatus 获取内嵌 Tailscale 入口的当前状态。
func (s *RemoteService) TailscaleStatus() mcodeui.RemoteTailscaleStatus {
	cfg := s.loadRemoteConfig()
	s.mu.Lock()
	state := s.state
	mgr := s.tailscale
	starting := s.tailscaleCancel != nil
	errText := s.tailscaleErr
	failState := s.tailscaleFailState
	who := s.tailscaleWho
	s.mu.Unlock()

	st := tailnet.Status{}
	if mgr != nil {
		st = mgr.Status()
	}
	startedURL, _ := s.tailscaleURL.Load().(string)

	res := mcodeui.RemoteTailscaleStatus{
		Enabled:       cfg.TailscaleEnabled(),
		Hostname:      cfg.TailscaleHost(),
		AuthURL:       st.AuthURL,
		FQDN:          st.FQDN,
		URL:           startedURL,
		Error:         st.Error,
		LastLoginName: who.LoginName,
		LastNodeName:  who.NodeName,
	}
	if errText != "" {
		res.Error = errText
	}

	switch {
	case !res.Enabled, state != mcodeui.RemoteStateRunning:
		// 开关关闭或远程服务没跑：入口不可用，后者明确告诉界面「服务未运行」。
		res.State = mcodeui.RemoteTailscaleDisabled
		if res.Enabled {
			res.Error = ErrRemoteNotRunning.Error()
		}
	case failState != "":
		// 入口启动或服务失败：直接回放失败状态，不能停在「启动中」。
		res.State = string(failState)
	case st.State == tailnet.StateLoginRequired && st.AuthURL == "":
		// 还在等 AuthURL，对界面就是「启动中」。
		res.State = mcodeui.RemoteTailscaleStarting
	case mgr == nil && starting:
		// 启动协程还在跑但管理器未结好。
		res.State = mcodeui.RemoteTailscaleStarting
	case mgr == nil:
		// 没在启动也没有管理器（如刚重置）：视为未启用。
		res.State = mcodeui.RemoteTailscaleDisabled
	case st.State == "":
		// 管理器已建但还没上报状态，同样属于启动中。
		res.State = mcodeui.RemoteTailscaleStarting
	default:
		res.State = string(st.State)
	}
	return res
}

// SetTailscaleEnabled 在当前运行中的远程服务上启用或停用 Tailscale 入口。
// 配置由界面先行保存，这里只让运行时生效；远程服务未运行时不做事。
func (s *RemoteService) SetTailscaleEnabled(ctx context.Context, enabled bool) (mcodeui.RemoteTailscaleStatus, error) {
	// 配置文件读取放在锁外，避免持锁做磁盘 IO。
	cfg := s.loadRemoteConfig()
	s.lifecycle.Lock()
	s.mu.Lock()
	var teardown func(context.Context) error
	if s.state == mcodeui.RemoteStateRunning {
		if enabled {
			if s.tailscaleCancel == nil {
				s.startTailscaleLocked(cfg, s.dispatcher)
			}
		} else {
			teardown = s.detachTailscaleLocked()
		}
	}
	s.mu.Unlock()
	// 阻塞收尾放在锁外，停用过程中状态查询与配对不阻塞。
	if teardown != nil {
		_ = teardown(ctx)
	}
	s.lifecycle.Unlock()
	return s.TailscaleStatus(), nil
}

// ResetTailscale 停止 Tailscale 入口并删除节点状态目录，状态回到 disabled，不自动重启。
func (s *RemoteService) ResetTailscale(ctx context.Context) error {
	// 全程持 lifecycle：删目录必须与重新启用串行，否则可能删掉新节点正在用的状态。
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	teardown := s.detachTailscaleLocked()
	s.mu.Unlock()

	if err := teardown(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// 节点还没停下来，此时删目录会与 tsnet 收尾冲突，留给用户稍后重试。
			return fmt.Errorf("tailscale 节点尚未停止，请稍后重试: %w", err)
		}
	}
	if s.dataDir == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.dataDir, tailscaleDirName))
}
