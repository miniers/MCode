package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/tailnet"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
	"mcode/internal/webassets"
)

// ErrRemoteNotRunning 表示远程控制服务尚未处于运行状态。
var ErrRemoteNotRunning = errors.New("远程控制服务未运行")

// RemoteService 负责管理桌面端远程控制服务的生命周期与配对鉴权。
type RemoteService struct {
	lifecycle sync.Mutex
	mu        sync.Mutex
	dataDir   string
	backend   *remote.MemoryBackend
	auth      *remote.PairingAuth
	srv       *remote.Server
	ln        net.Listener
	done      chan struct{}
	state     mcodeui.RemoteServiceState
	lastErr   string

	sleepGuard *remote.SleepGuard

	startedListen     string
	startedExtraHosts []string

	// dispatcher 保留当前 Dispatcher，便于 Swarm 编排服务晚于远程服务启动时补注入。
	dispatcher  *remote.Dispatcher
	swarmSvc    remote.SwarmService
	swarmExtras remote.SwarmRemoteExtras
	// swarmWatch 订阅管理器侧的 Swarm 变更（桌面端的 Client.WatchSwarm），
	// 回调再转发给当前 Dispatcher，补齐桌面端缺失的 threads.swarm.changed 推送。
	swarmWatch     func(onChange func(leadThreadID string)) (stop func())
	swarmWatchStop func()
	// attentionWatch 订阅待处理事件变更（桌面端的 Client.WatchAttention），attentionCount 取当前未处理条数，
	// 回调再经当前 Dispatcher 广播 attention.changed。
	attentionWatch     func(onChange func()) (stop func())
	attentionCount     func(ctx context.Context) (int, error)
	attentionWatchStop func()
	// stewardWatch 订阅总管话题与看板变更（桌面端的 Client.WatchStewardTopics），
	// 回调经 stewardChangeDebouncer 合并后广播 steward.changed。
	stewardWatch     func(onChange func()) (stop func())
	stewardWatchStop func()

	tlsMgr        *remote.TLSManager
	tlsSrv        *remote.Server
	tlsLn         net.Listener
	tlsDone       chan struct{}
	tlsStartedURL string
	tlsErr        string

	// tailscale 是内嵌 tsnet 节点管理器；tailscaleCancel 非空表示启动中或运行中。
	tailscale       tailnetManager
	tailscaleSrv    *remote.Server
	tailscaleLn     net.Listener
	tailscaleCancel context.CancelFunc
	tailscaleErr    string
	// tailscaleFailState 是入口启动或服务失败后停留的状态，非空时 TailscaleStatus 直接返回它。
	tailscaleFailState tailnet.State
	// tailscaleURL 存入口真正开始服务后的 https://<fqdn>，供配对链接无锁读取。
	tailscaleURL atomic.Value
	// tailscaleWho 是最近一次通过 Tailscale 建立的连接身份，仅用于展示与审计。
	tailscaleWho remote.ClientIdentity
	// tailscalePrevDone 是上一个节点的关闭通道，新节点启动前要等它关闭，
	// 避免两个节点共用同一个状态目录。
	tailscalePrevDone <-chan struct{}

	// push 复用远程控制服务生命周期：启动时创建，停止时保留订阅与 VAPID 密钥。
	push *remote.WebPushService
	// pushUnobserve 为当前 StreamHub 推送观察者的注销函数：启动时登记、停止时注销，
	// 避免反复启停后叠加多个观察者、同一事件被重复推送。
	pushUnobserve func()
	// tlsHosts 为当前 TLS 入口允许的 Host 列表，也是 VAPID sub 的域名来源。
	tlsHosts []string

	// lanBase/tlsBase 供 PairingAuth 的 URL 回调无锁读取，避免与 s.mu 重入死锁。
	lanBase atomic.Value
	tlsBase atomic.Value
	// domainBase 是配置了自定义域名时的 http://<domain>:<局域网端口>，供回调在 TLS 入口未运行时选用。
	domainBase atomic.Value
}

// NewRemoteService 创建并返回 RemoteService 实例，初始状态为 stopped。
func NewRemoteService(dataDir string, backend *remote.MemoryBackend) *RemoteService {
	return &RemoteService{
		dataDir: dataDir,
		backend: backend,
		state:   mcodeui.RemoteStateStopped,
	}
}

// SetSwarmWiring 注入 Swarm 编排服务与远程扩展能力，供 Dispatcher 暴露 Swarm 相关 RPC；
// 远程服务已在运行时立即应用到当前 Dispatcher。
func (s *RemoteService) SetSwarmWiring(svc remote.SwarmService, extras remote.SwarmRemoteExtras) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmSvc = svc
	s.swarmExtras = extras
	if s.dispatcher == nil {
		return
	}
	if svc != nil {
		s.dispatcher.SetSwarm(svc)
	}
	if extras != nil {
		s.dispatcher.SetSwarmExtras(extras)
	}
}

// SetSwarmWatcher 注入 Swarm 变更订阅函数，供 Dispatcher 向 web 端推送 threads.swarm.changed；
// 远程服务已在运行时立即订阅。桌面端由 Client.SetSwarmManager 独占管理器的回调，
// 因此这里额外订阅 Client.WatchSwarm，而不是再抢一次 SetOnChange。
func (s *RemoteService) SetSwarmWatcher(watch func(onChange func(leadThreadID string)) (stop func())) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmWatch = watch
	if s.dispatcher != nil {
		s.startSwarmWatchLocked()
	}
}

// startSwarmWatchLocked 起一次 Swarm 变更订阅，回调按当前 Dispatcher 转发；调用方需持有 s.mu。
// 订阅在服务启动时建立、停止时释放，Dispatcher 重建后回调自然落到新实例上。
func (s *RemoteService) startSwarmWatchLocked() {
	s.stopSwarmWatchLocked()
	if s.swarmWatch == nil {
		return
	}
	s.swarmWatchStop = s.swarmWatch(func(leadThreadID string) {
		if leadThreadID == "" {
			return
		}
		s.mu.Lock()
		dispatcher := s.dispatcher
		s.mu.Unlock()
		if dispatcher != nil {
			dispatcher.NotifySwarmChanged(leadThreadID)
		}
	})
}

// stopSwarmWatchLocked 释放当前的 Swarm 变更订阅；调用方需持有 s.mu。
func (s *RemoteService) stopSwarmWatchLocked() {
	stop := s.swarmWatchStop
	s.swarmWatchStop = nil
	if stop != nil {
		stop()
	}
}

// SetAttentionWatcher 注入待处理事件订阅函数与未处理条数查询，供 Dispatcher 向 web 端推送 attention.changed；
// 远程服务已在运行时立即订阅。
func (s *RemoteService) SetAttentionWatcher(watch func(onChange func()) (stop func()), count func(ctx context.Context) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attentionWatch = watch
	s.attentionCount = count
	if s.dispatcher != nil {
		s.startAttentionWatchLocked()
	}
}

// startAttentionWatchLocked 起一次待处理事件订阅，回调取最新未处理条数后按当前 Dispatcher 广播；调用方需持有 s.mu。
func (s *RemoteService) startAttentionWatchLocked() {
	s.stopAttentionWatchLocked()
	if s.attentionWatch == nil || s.attentionCount == nil {
		return
	}
	count := s.attentionCount
	s.attentionWatchStop = s.attentionWatch(func() {
		s.mu.Lock()
		dispatcher := s.dispatcher
		s.mu.Unlock()
		if dispatcher == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		n, err := count(ctx)
		if err != nil {
			log.Printf("remote: 统计未处理事件失败: %v", err)
			return
		}
		dispatcher.NotifyAttentionChanged(n)
	})
}

// stopAttentionWatchLocked 释放当前的待处理事件订阅；调用方需持有 s.mu。
func (s *RemoteService) stopAttentionWatchLocked() {
	stop := s.attentionWatchStop
	s.attentionWatchStop = nil
	if stop != nil {
		stop()
	}
}

// SetStewardWatcher 注入总管话题变更订阅函数，供 Dispatcher 向 web 端推送 steward.changed；
// 远程服务已在运行时立即订阅。订阅回调不带项目，因此推送的 projectID 为空，表示「全部项目」。
func (s *RemoteService) SetStewardWatcher(watch func(onChange func()) (stop func())) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stewardWatch = watch
	if s.dispatcher != nil {
		s.startStewardWatchLocked()
	}
}

// startStewardWatchLocked 起一次总管话题订阅，回调经去抖后按当前 Dispatcher 广播；调用方需持有 s.mu。
func (s *RemoteService) startStewardWatchLocked() {
	s.stopStewardWatchLocked()
	if s.stewardWatch == nil {
		return
	}
	// 话题与看板变化经常成串到达，去抖器把一串变化合并成一条通知。
	debouncer := newStewardChangeDebouncer(stewardChangedDebounce, func() {
		s.mu.Lock()
		dispatcher := s.dispatcher
		s.mu.Unlock()
		if dispatcher != nil {
			dispatcher.NotifyStewardChanged("")
		}
	})
	stop := s.stewardWatch(debouncer.trigger)
	s.stewardWatchStop = func() {
		if stop != nil {
			stop()
		}
		debouncer.stop()
	}
}

// stopStewardWatchLocked 释放当前的总管话题订阅；调用方需持有 s.mu。
func (s *RemoteService) stopStewardWatchLocked() {
	stop := s.stewardWatchStop
	s.stewardWatchStop = nil
	if stop != nil {
		stop()
	}
}

// SendOwnerPush 向已订阅的离线设备发一条给用户的网页推送，供总管 notify_owner 使用。
// 推送服务未启用或没有任何订阅时返回 false；否则异步发送并返回 true。
func (s *RemoteService) SendOwnerPush(title, body, threadID string) bool {
	s.mu.Lock()
	push := s.push
	s.mu.Unlock()
	if push == nil || push.SubscriptionCount() == 0 {
		return false
	}
	go push.Send(context.Background(), remote.PushPayload{
		Title:    title,
		Body:     body,
		ThreadID: threadID,
	})
	return true
}

// answerHandlerLocked 构造推送通知内联作答处理器；鉴权或后端缺失时不注入，路径自然 404。
// 调用方需持有 s.mu。
func (s *RemoteService) answerHandlerLocked(entry remote.Entry) http.Handler {
	if s.auth == nil || s.backend == nil {
		return nil
	}
	return remote.NewAnswerEndpoint(remote.AnswerEndpointOptions{
		Auth:    s.auth,
		Backend: s.backend,
		Entry:   entry,
	})
}

// Start 启动远程控制服务。如果服务已在运行则直接返回 nil。
func (s *RemoteService) Start(listen string, extraHosts []string) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == mcodeui.RemoteStateRunning {
		return nil
	}

	if listen == "" {
		listen = config.DefaultRemoteListen
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		s.state = mcodeui.RemoteStateError
		s.lastErr = err.Error()
		return err
	}

	cfg := s.loadRemoteConfig()
	s.lanBase.Store(lanBaseURL(ln.Addr()))
	s.domainBase.Store(domainLANBaseURL(cfg.TLSDomain, ln.Addr()))

	if s.auth == nil {
		if _, err := s.ensureAuthLocked(); err != nil {
			_ = ln.Close()
			s.lanBase.Store("")
			s.domainBase.Store("")
			s.state = mcodeui.RemoteStateError
			s.lastErr = err.Error()
			return err
		}
	} else if _, err := s.auth.NewPairingCode(); err != nil {
		_ = ln.Close()
		s.lanBase.Store("")
		s.domainBase.Store("")
		s.state = mcodeui.RemoteStateError
		s.lastErr = err.Error()
		return err
	}

	dispatcher := remote.NewDispatcher(s.auth, s.backend)
	s.dispatcher = dispatcher
	if s.swarmSvc != nil {
		dispatcher.SetSwarm(s.swarmSvc)
	}
	if s.swarmExtras != nil {
		dispatcher.SetSwarmExtras(s.swarmExtras)
	}
	s.startSwarmWatchLocked()
	s.startAttentionWatchLocked()
	s.startStewardWatchLocked()
	if s.backend != nil {
		s.backend.SetAttachmentsDispatcher(dispatcher)
	}

	listenHost, _, isUnspecified, _ := parseHostPort(ln.Addr())

	hosts := []string{"localhost", "127.0.0.1"}
	if !isUnspecified {
		hosts = append(hosts, listenHost)
	}
	hosts = append(hosts, remote.LANIPv4s()...)
	if len(extraHosts) > 0 {
		hosts = append(hosts, extraHosts...)
	}

	srv := remote.NewServer(remote.ServerOptions{
		Handler:       dispatcher,
		Assets:        webassets.FS(),
		ListenAddr:    ln.Addr().String(),
		Entry:         remote.EntryLAN,
		AllowedHosts:  hosts,
		AdminRoutes:   s.adminRoutesLocked(),
		AnswerHandler: s.answerHandlerLocked(remote.EntryLAN),
	})

	// TLS 先启动，推送的 VAPID sub 才能拿到 TLS 域名。
	s.startTLSLocked(cfg, hosts, dispatcher)
	s.startPushLocked(dispatcher, cfg, ln)

	s.srv = srv
	s.ln = ln
	done := make(chan struct{})
	s.done = done
	s.startedListen = listen
	s.startedExtraHosts = append([]string(nil), extraHosts...)
	s.state = mcodeui.RemoteStateRunning
	s.lastErr = ""
	// LAN 与 TLS 都就绪后再启 Tailscale 入口；它只做启动准备，不阻塞 Start。
	if cfg.TailscaleEnabled() {
		s.startTailscaleLocked(cfg, dispatcher)
	}

	backend := s.backend
	busy := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if backend == nil {
			return false
		}
		res, err := backend.ListThreads(ctx, wire.ThreadListParams{})
		if err != nil {
			return false
		}
		for _, th := range res.Threads {
			if th.IsRunning {
				return true
			}
		}
		return false
	}
	guard := remote.NewSleepGuard(busy, 20*time.Second)
	guard.Start()
	s.sleepGuard = guard

	go func() {
		defer close(done)
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			if s.srv == srv {
				s.state = mcodeui.RemoteStateError
				s.lastErr = err.Error()
			}
			s.mu.Unlock()
		}
	}()

	return nil
}

// Stop 停止远程控制服务。
func (s *RemoteService) Stop(ctx context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	// Tailscale 入口先停：它占用配对主链接，必须早于 LAN / TLS 收尾；配对 URL 已在此清空。
	tailscaleTeardown := s.detachTailscaleLocked()
	s.lanBase.Store("")
	s.tlsBase.Store("")
	s.domainBase.Store("")
	srv := s.srv
	ln, done := s.ln, s.done
	guard := s.sleepGuard
	tlsMgr := s.tlsMgr
	tlsSrv := s.tlsSrv
	tlsLn, tlsDone := s.tlsLn, s.tlsDone
	s.srv = nil
	s.ln = nil
	s.done = nil
	s.sleepGuard = nil
	s.stopSwarmWatchLocked()
	s.stopAttentionWatchLocked()
	s.stopStewardWatchLocked()
	s.tlsMgr = nil
	s.tlsSrv = nil
	s.tlsLn = nil
	s.tlsDone = nil
	s.tlsStartedURL = ""
	s.tlsErr = ""
	s.state = mcodeui.RemoteStateStopped
	s.lastErr = ""
	pushUnobserve := s.pushUnobserve
	s.pushUnobserve = nil
	backend := s.backend
	s.mu.Unlock()

	if pushUnobserve != nil {
		pushUnobserve()
	}

	// tsnet 收尾可能吃掉调用方仅有的 ctx 预算（见 app.go 里 Stop 的 2 秒超时），
	// 所以与 LAN / TLS 收尾并行，return 前再等它结束。
	tailscaleDone := make(chan struct{})
	go func() {
		defer close(tailscaleDone)
		_ = tailscaleTeardown(ctx)
	}()
	defer func() { <-tailscaleDone }()

	if guard != nil {
		guard.Stop()
	}
	if backend != nil {
		backend.SetAttachmentsDispatcher(nil)
	}

	// Serve 可能尚未进入 HTTP 循环，先关闭监听并等待其退出，避免 Shutdown 漏关端口。
	if ln != nil {
		_ = ln.Close()
	}
	if tlsLn != nil {
		_ = tlsLn.Close()
	}
	if done != nil {
		<-done
	}
	if tlsDone != nil {
		<-tlsDone
	}

	var firstErr error
	if srv != nil {
		firstErr = srv.Shutdown(ctx)
	}
	if tlsSrv != nil {
		if err := tlsSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if tlsMgr != nil {
		tlsMgr.Close()
	}

	// 连接已全部断开，把节流缓存里的最新活跃记录落盘，避免重启后设备丢失最近一次活跃时间与地址。
	s.flushDevices()

	return firstErr
}

// flushDevices 将设备存储中因写盘节流而滞留在内存的活跃记录（最新活跃时间、设备名与地址）
// 立即同步到磁盘。调用方需自行保证不持有 s.mu，避免与 s.now 回调内的加锁交叉。
func (s *RemoteService) flushDevices() {
	s.mu.Lock()
	auth := s.auth
	s.mu.Unlock()
	if auth == nil {
		return
	}
	store := auth.Store()
	if store == nil {
		return
	}
	if err := store.Flush(); err != nil {
		log.Printf("remote: flush device store: %v", err)
	}
}

// Status 获取当前远程控制服务的运行状态与网络监听信息。
func (s *RemoteService) Status() mcodeui.RemoteStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := mcodeui.RemoteStatus{
		State:             s.state,
		Error:             s.lastErr,
		StartedListen:     s.startedListen,
		StartedExtraHosts: append([]string(nil), s.startedExtraHosts...),
	}
	if s.srv != nil {
		st.ConnectedClients += s.srv.ConnectedCount()
	}
	if s.tlsSrv != nil {
		st.ConnectedClients += s.tlsSrv.ConnectedCount()
	}
	// Tailscale 入口是另一个 server，手机经它连上来时也要算进在线设备数。
	if s.tailscaleSrv != nil {
		st.ConnectedClients += s.tailscaleSrv.ConnectedCount()
	}
	if s.push != nil {
		st.PushSubscriptions = s.push.SubscriptionCount()
	}
	if s.ln == nil {
		return st
	}

	st.ListenAddr = s.ln.Addr().String()
	host, port, isUnspecified, isLoopback := parseHostPort(s.ln.Addr())
	st.Port = port

	if isUnspecified {
		st.LANURLs = remote.LANBaseURLs(port)
	} else if isLoopback {
		st.LANURLs = nil
	} else {
		st.LANURLs = []string{"http://" + net.JoinHostPort(host, strconv.Itoa(port))}
	}

	return st
}

// Pairing 获取当前配对信息；refresh 为 true 时换发新码，否则返回当前有效码。
func (s *RemoteService) Pairing(refresh bool) (remote.PairingInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != mcodeui.RemoteStateRunning || s.auth == nil {
		return remote.PairingInfo{}, ErrRemoteNotRunning
	}
	auth := s.auth

	if refresh {
		return auth.NewPairingCode()
	}
	return auth.CurrentPairingURLs(), nil
}

func (s *RemoteService) ensureAuthLocked() (*remote.PairingAuth, error) {
	if s.auth != nil {
		return s.auth, nil
	}
	if s.dataDir == "" {
		return nil, errors.New("remote: missing dataDir")
	}
	auth, err := remote.NewPairingAuth(remote.PairingAuthOptions{
		DevicesPath: filepath.Join(s.dataDir, "remote-devices.json"),
		LANBaseURL: func() string {
			if v, ok := s.lanBase.Load().(string); ok {
				return v
			}
			return ""
		},
		TLSBaseURL: func() string {
			if v, ok := s.tlsBase.Load().(string); ok && v != "" {
				return v
			}
			// TLS 入口未运行但配置了自定义域名时，配对链接指向域名 + 局域网 HTTP 端口。
			if v, ok := s.domainBase.Load().(string); ok {
				return v
			}
			return ""
		},
		TailscaleBaseURL: func() string {
			// 入口未开始服务时返回空串，主链接自动回落到 TLS / LAN。
			if v, ok := s.tailscaleURL.Load().(string); ok {
				return v
			}
			return ""
		},
		OnRevoke: func(deviceID string) {
			s.mu.Lock()
			servers := []*remote.Server{s.srv, s.tlsSrv, s.tailscaleSrv}
			s.mu.Unlock()
			for _, srv := range servers {
				if srv != nil {
					srv.CloseDevice(deviceID)
				}
			}
			// 撤销的设备不再接收推送：它的订阅随设备一起消失。
			if push := s.push; push != nil {
				push.ForgetDevice(deviceID)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	s.auth = auth
	return auth, nil
}

// Devices 获取当前所有已授权设备记录。
func (s *RemoteService) Devices() []mcodeui.RemoteDevice {
	s.mu.Lock()
	auth, err := s.ensureAuthLocked()
	servers := []*remote.Server{s.srv, s.tlsSrv, s.tailscaleSrv}
	s.mu.Unlock()
	if err != nil || auth == nil {
		return nil
	}

	online := make(map[string]struct{})
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		for _, id := range srv.ConnectedDeviceIDs() {
			online[id] = struct{}{}
		}
	}

	records := auth.Store().AllRecords()
	devices := make([]mcodeui.RemoteDevice, len(records))
	for i, r := range records {
		_, isOnline := online[r.DeviceID]
		devices[i] = mcodeui.RemoteDevice{
			ID:          r.DeviceID,
			Name:        r.Label,
			Platform:    summarizePlatform(r.UserAgent),
			Online:      isOnline,
			LastAddress: r.LastAddress,
			LastEntry:   r.LastEntry,
			UserAgent:   r.UserAgent,
			LastSeenAt:  r.LastSeenAt,
			PairedAt:    r.PairedAt,
		}
	}
	return devices
}

// Rename 更新指定设备的显示名称。
func (s *RemoteService) Rename(deviceID, name string) error {
	s.mu.Lock()
	auth, err := s.ensureAuthLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return auth.RenameDevice(deviceID, name)
}

// RevokeAll 撤销全部已授权设备并断开其活跃连接。
func (s *RemoteService) RevokeAll() error {
	s.mu.Lock()
	auth, err := s.ensureAuthLocked()
	push := s.push
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := auth.RevokeAllDevices(); err != nil {
		return err
	}
	// 没有设备可推送时不留任何订阅。
	if push != nil {
		push.ForgetAllDevices()
	}
	return nil
}

// PairingState 返回当前配对码状态；服务未运行时为 unavailable。
func (s *RemoteService) PairingState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != mcodeui.RemoteStateRunning || s.auth == nil {
		return "unavailable"
	}
	return s.auth.PairingState()
}

// Restart 使用最新配置重启远程控制服务。
func (s *RemoteService) Restart(ctx context.Context) (mcodeui.RemoteStatus, error) {
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		return s.Status(), err
	}

	cfg, err := config.Load(s.dataDir)
	if err != nil {
		return s.Status(), err
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	if err := s.Start(cfg.Remote.Listen, cfg.Remote.ExtraHosts); err != nil {
		return s.Status(), err
	}
	return s.Status(), nil
}

// RevokeDevice 撤销指定设备的授权，若服务在运行则触发断开连接。
func (s *RemoteService) RevokeDevice(deviceID string) error {
	s.mu.Lock()
	auth, err := s.ensureAuthLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = auth.RevokeDevice(deviceID)
	return err
}

// pushHubAddObserver 把推送观察者挂到 StreamHub 上并返回注销函数。
// 包级变量便于单元测试统计存活观察者数量，确认启停不会叠加观察者。
var pushHubAddObserver = func(hub *remote.StreamHub, fn func(threadID, method string, params any)) func() {
	return hub.AddObserver(fn)
}

// startPushLocked 在已持有 s.mu 时创建（或复用）Web Push 服务，并接到 dispatcher 与会话事件上。
// 订阅通过配对鉴权持有的设备存储读写（同一个 remote-devices.json），
// VAPID 私钥位于 <dataDir>/remote-webpush-vapid.pem。
func (s *RemoteService) startPushLocked(dispatcher *remote.Dispatcher, cfg config.RemoteConfig, lanLn net.Listener) {
	// 上一次启动残留的观察者先注销，避免同一事件被多次推送。
	if s.pushUnobserve != nil {
		s.pushUnobserve()
		s.pushUnobserve = nil
	}
	if s.dataDir == "" || s.backend == nil || !cfg.PushEnabled() {
		return
	}
	if s.auth == nil {
		log.Printf("remote push: pairing auth not initialized")
		return
	}

	push, err := remote.NewWebPushService(remote.WebPushOptions{
		KeyPath:      filepath.Join(s.dataDir, "remote-webpush-vapid.pem"),
		Devices:      s.auth.Store(),
		VAPIDSubject: remote.VAPIDSubjectFor(s.tlsPushDomainLocked(), lanLn),
		SkipOnline: func(deviceID string) bool {
			s.mu.Lock()
			srv, tlsSrv, tailscaleSrv := s.srv, s.tlsSrv, s.tailscaleSrv
			s.mu.Unlock()
			for _, server := range []*remote.Server{srv, tlsSrv, tailscaleSrv} {
				if server != nil && server.HasSubscribedConnection(deviceID) {
					return true
				}
			}
			return false
		},
		ThreadWorkDir: func(threadID string) (string, bool) {
			return s.backend.ThreadWorkDir(threadID)
		},
	})
	if err != nil {
		log.Printf("remote push: init webpush service: %v", err)
		return
	}
	s.push = push

	dispatcher.SetPush(push)
	if hub := s.backend.Hub(); hub != nil {
		s.pushUnobserve = pushHubAddObserver(hub, func(threadID, method string, params any) {
			push.HandleNotification(context.Background(), threadID, method, params)
		})
	}
}

// tlsPushDomainLocked 在已持有 s.mu 时返回推送应使用的 TLS 域名：优先取 TLS 入口配置的域名，
// 否则退到当前 TLS 入口实际对外的 https 主机（自定义证书时可能没有域名，只有 IP）。
func (s *RemoteService) tlsPushDomainLocked() string {
	if domain := strings.TrimSpace(s.loadRemoteConfig().TLSDomain); domain != "" {
		return domain
	}
	if s.tlsStartedURL != "" {
		if u, err := url.Parse(s.tlsStartedURL); err == nil {
			return u.Hostname()
		}
	}
	return ""
}

// adminRoutesLocked 在已持有 s.mu 时返回本机管理路由（当前只有测试推送）。
func (s *RemoteService) adminRoutesLocked() []http.HandlerFunc {
	return []http.HandlerFunc{func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		sent, err := s.SendTestPush()
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sent": sent})
	}}
}

// SendTestPush 向所有已登记订阅发送一条测试推送，返回实际发送的端点数，供设置页验证推送链路。
func (s *RemoteService) SendTestPush() (int, error) {
	s.mu.Lock()
	push, running := s.push, s.state == mcodeui.RemoteStateRunning
	s.mu.Unlock()
	if push == nil || !running {
		return 0, ErrRemoteNotRunning
	}
	return push.SendTest(context.Background(), "MCode", "测试推送：手机能收到这条通知，说明推送链路已经打通。")
}

func summarizePlatform(ua string) string {
	if ua == "" {
		return ""
	}
	switch {
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return ua
	}
}

func parseHostPort(addr net.Addr) (host string, port int, isUnspecified, isLoopback bool) {
	if addr == nil {
		return "", 0, false, false
	}
	var portStr string
	var err error
	host, portStr, err = net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	port, _ = strconv.Atoi(portStr)
	ip := net.ParseIP(host)
	isUnspecified = host == "" || host == "0.0.0.0" || host == "::" || (ip != nil && ip.IsUnspecified())
	isLoopback = (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost")
	return
}

func lanBaseURL(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, port, isUnspecified, isLoopback := parseHostPort(addr)
	portStr := strconv.Itoa(port)
	if isUnspecified {
		lanURLs := remote.LANBaseURLs(port)
		if len(lanURLs) > 0 {
			return lanURLs[0]
		}
		return "http://127.0.0.1:" + portStr
	}
	if isLoopback {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort(host, portStr)
}

// domainLANBaseURL 返回配置了自定义域名时的 http://<domain>:<局域网端口>；未配置域名时为空。
func domainLANBaseURL(domain string, addr net.Addr) string {
	domain = strings.TrimSpace(domain)
	if domain == "" || addr == nil {
		return ""
	}
	_, port, _, _ := parseHostPort(addr)
	if port <= 0 {
		return ""
	}
	return "http://" + net.JoinHostPort(domain, strconv.Itoa(port))
}
