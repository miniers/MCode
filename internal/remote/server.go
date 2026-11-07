package remote

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// 长连接存活探测：每 PingInterval 发一次 ping，PongTimeout 内收不到 pong 记一次错过，
// 连续 maxMissedPongs 次即断开。半开连接（对端断电、切网）在没有待发数据时不会被写失败发现，
// 只能靠这层探测回收连接槽。
const (
	defaultPingInterval = 30 * time.Second
	defaultPongTimeout  = 10 * time.Second
	maxMissedPongs      = 2
	// maxUnauthConnections 是未认证连接的独立上限。没配对的一方不该能占满 MaxConnections 的全部槽位。
	maxUnauthConnections = 4
)

// MessageHandler 处理 WebSocket 接收到的文本帧原始数据，返回需发回的响应数据（若有）。
type MessageHandler interface {
	Handle(ctx context.Context, c Conn, raw []byte) []byte
}

type connRemover interface {
	RemoveConn(c Conn)
}

// ServerOptions 提供 Server 的配置项。
type ServerOptions struct {
	Handler       MessageHandler
	Backend       Backend
	Hub           *StreamHub
	Assets        fs.FS
	ListenAddr    string
	Entry         Entry
	AllowedHosts  []string
	Now           func() time.Time
	UnauthTimeout time.Duration
	// PingInterval / PongTimeout 控制长连接的存活探测，留空时默认 30 秒 / 10 秒。
	PingInterval   time.Duration
	PongTimeout    time.Duration
	SendQueueSize  int
	MaxConnections int
	// AdminRoutes 是桌面端发起的本机管理动作（如发送测试推送）的处理器，路径均为 /admin/remote/*；
	// 只接受源自 localhost 或 127.0.0.1 的请求，远程设备无法调用。
	AdminRoutes []http.HandlerFunc
	// AnswerHandler 处理通知内联作答的一次性请求；仅安全入口放行，未配置时该路径 404。
	AnswerHandler http.Handler
	// WhoIs 在连接建立时解析传输层身份（如 tsnet 的 WhoIs），仅用于展示与审计，不参与鉴权。
	// 调用带 2 秒超时；返回错误时连接照常建立，TailnetUser / TailnetNode 留空。
	WhoIs func(ctx context.Context, remoteAddr string) (ClientIdentity, error)
}

// Server 实现 HTTP 静态资源托管与 WebSocket 远程控制升级端点。
type Server struct {
	opts           ServerOptions
	handler        MessageHandler
	now            func() time.Time
	unauthTimeout  time.Duration
	pingInterval   time.Duration
	pongTimeout    time.Duration
	sendQueueSize  int
	maxConnections int

	mu      sync.Mutex
	server  *http.Server
	assetMu sync.RWMutex
	assets  map[string]*staticAsset

	// cspMu 保护 cspCache：内联脚本哈希只在首次返回 HTML 时算一次，之后按文件名复用。
	cspMu    sync.RWMutex
	cspCache map[string]string

	connMu        sync.RWMutex
	conns         map[*wsConn]struct{}
	reservedConns int

	// hub 用于判断某设备的浏览器是否在线（已认证且订阅了会话）。
	hub *StreamHub
}

type staticAsset struct {
	data        []byte
	gzipped     []byte
	etag        string
	contentType string
}

type wsConn struct {
	server      *Server
	ws          *websocket.Conn
	sendCh      chan []byte
	closed      chan struct{}
	closeOnce   sync.Once
	cancel      context.CancelFunc
	unauthTimer *time.Timer
	// handling 标记读循环正同步执行一个 RPC：此期间 pong 得不到处理，ping 超时不能算对端失联。
	handling atomic.Bool

	mu   sync.RWMutex
	info ClientInfo
}

// isAuthenticated 返回连接是否已完成认证（含设备绑定）。
func (c *wsConn) isAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info.Authenticated
}

func (c *wsConn) Info() ClientInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
}

func (c *wsConn) MarkAuthenticated(deviceID, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label

	if c.unauthTimer != nil {
		c.unauthTimer.Stop()
	}
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func (c *wsConn) Notify(method string, params any) error {
	notif := rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	raw, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	return c.sendRaw(raw)
}

func (c *wsConn) sendRaw(msg []byte) error {
	select {
	case <-c.closed:
		return errors.New("remote: conn closed")
	default:
	}

	select {
	case c.sendCh <- msg:
		return nil
	default:
		// 对端已不读消息，不等待关闭握手
		_ = c.ws.CloseNow()
		_ = c.Close()
		return errors.New("remote: send queue full")
	}
}

func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.unauthTimer != nil {
			c.unauthTimer.Stop()
		}
		close(c.closed)
		if c.cancel != nil {
			c.cancel()
		}
		err = c.ws.Close(websocket.StatusNormalClosure, "closed")
		c.server.removeConn(c)
	})
	return err
}

func (c *wsConn) readPump(ctx context.Context) {
	defer func() {
		_ = c.Close()
	}()

	for {
		typ, p, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			_ = c.ws.Close(websocket.StatusUnsupportedData, "binary frames not supported")
			return
		}

		if c.server.handler != nil {
			c.handling.Store(true)
			resp := c.server.handler.Handle(ctx, c, p)
			c.handling.Store(false)
			if len(resp) > 0 {
				if err := c.sendRaw(resp); err != nil {
					return
				}
			}
		}
	}
}

func (c *wsConn) writePump(ctx context.Context) {
	ticker := time.NewTicker(c.server.pingInterval)
	defer ticker.Stop()
	missedPongs := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-ticker.C:
			// 读循环正在执行耗时 RPC 时无法处理 pong，这一轮不计错过。
			if c.handling.Load() {
				missedPongs = 0
				continue
			}
			pingCtx, cancel := context.WithTimeout(ctx, c.server.pongTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err == nil {
				missedPongs = 0
				continue
			}
			if ctx.Err() != nil {
				return
			}
			missedPongs++
			if missedPongs >= maxMissedPongs {
				// 连续两轮没有 pong：对端已失联，主动断开把连接槽还给其他设备。
				_ = c.ws.Close(websocket.StatusPolicyViolation, "ping timeout")
				_ = c.Close()
				return
			}
		case msg := <-c.sendCh:
			if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}

// NewServer 构造远程控制服务实例。
func NewServer(opts ServerOptions) *Server {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	unauthTimeout := opts.UnauthTimeout
	if unauthTimeout <= 0 {
		unauthTimeout = 30 * time.Second
	}

	sendQueueSize := opts.SendQueueSize
	if sendQueueSize <= 0 {
		sendQueueSize = 64
	}

	pingInterval := opts.PingInterval
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}
	pongTimeout := opts.PongTimeout
	if pongTimeout <= 0 {
		pongTimeout = defaultPongTimeout
	}

	maxConns := opts.MaxConnections
	if maxConns <= 0 {
		maxConns = 16
	}

	srv := &Server{
		opts:           opts,
		handler:        opts.Handler,
		now:            now,
		unauthTimeout:  unauthTimeout,
		pingInterval:   pingInterval,
		pongTimeout:    pongTimeout,
		sendQueueSize:  sendQueueSize,
		maxConnections: maxConns,
		assets:         make(map[string]*staticAsset),
		cspCache:       make(map[string]string),
		conns:          make(map[*wsConn]struct{}),
	}
	if opts.Hub != nil {
		srv.hub = opts.Hub
	} else if source, ok := opts.Backend.(interface{ Hub() *StreamHub }); ok {
		srv.hub = source.Hub()
	}
	return srv
}

// reserveConn 预留一个连接槽位。未认证连接单独限流（maxUnauthConnections 条），
// 避免无凭据的一方占满槽位把已配对客户端挡在门外；已认证连接的配额仍是 MaxConnections。
func (s *Server) reserveConn() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	authed, unauth := 0, s.reservedConns
	for c := range s.conns {
		if c.isAuthenticated() {
			authed++
		} else {
			unauth++
		}
	}

	unauthLimit := maxUnauthConnections
	if unauthLimit > s.maxConnections {
		unauthLimit = s.maxConnections
	}
	if unauth >= unauthLimit || authed >= s.maxConnections {
		return false
	}
	s.reservedConns++
	return true
}

func (s *Server) releaseReservation() {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.reservedConns > 0 {
		s.reservedConns--
	}
}

func (s *Server) addConn(c *wsConn) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.reservedConns > 0 {
		s.reservedConns--
	}
	s.conns[c] = struct{}{}
	return true
}

// HasSubscribedConnection 返回该设备是否有一条已认证且正订阅至少一个会话的连接。
// 这样的浏览器在线，能自己刷新和提示，离线推送会跳过它，避免手机上重复两条。
func (s *Server) HasSubscribedConnection(deviceID string) bool {
	if deviceID == "" {
		return false
	}
	if s.hub == nil {
		return false
	}
	s.connMu.RLock()
	defer s.connMu.RUnlock()

	for c := range s.conns {
		c.mu.RLock()
		authed := c.info.Authenticated && c.info.DeviceID == deviceID
		c.mu.RUnlock()
		if authed && s.hub.IsSubscribed(c) {
			return true
		}
	}
	return false
}

func (s *Server) removeConn(c *wsConn) {
	s.connMu.Lock()
	delete(s.conns, c)
	s.connMu.Unlock()

	if s.opts.Hub != nil {
		s.opts.Hub.RemoveConn(c)
	}
	if s.opts.Backend != nil {
		s.opts.Backend.RemoveConn(c)
	}
	if cr, ok := s.handler.(connRemover); ok {
		cr.RemoveConn(c)
	}
}

// CloseDevice 强制关闭指定设备关联的所有已认证活跃连接。
func (s *Server) CloseDevice(deviceID string) {
	s.connMu.RLock()
	var targets []*wsConn
	for c := range s.conns {
		c.mu.RLock()
		if c.info.Authenticated && c.info.DeviceID == deviceID {
			targets = append(targets, c)
		}
		c.mu.RUnlock()
	}
	s.connMu.RUnlock()

	for _, c := range targets {
		_ = c.ws.Close(websocket.StatusNormalClosure, "device revoked")
		_ = c.Close()
	}
}

// ConnectedDeviceIDs 返回当前已认证连接关联的设备 ID，按字典序去重排列。
func (s *Server) ConnectedDeviceIDs() []string {
	s.connMu.RLock()
	defer s.connMu.RUnlock()

	seen := make(map[string]struct{})
	for c := range s.conns {
		c.mu.RLock()
		authed := c.info.Authenticated
		deviceID := c.info.DeviceID
		c.mu.RUnlock()
		if !authed || deviceID == "" {
			continue
		}
		seen[deviceID] = struct{}{}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ConnectedCount 返回当前已认证的活跃连接数。
func (s *Server) ConnectedCount() int {
	s.connMu.RLock()
	defer s.connMu.RUnlock()

	count := 0
	for c := range s.conns {
		c.mu.RLock()
		authed := c.info.Authenticated
		c.mu.RUnlock()
		if authed {
			count++
		}
	}
	return count
}

// newHTTPServer 构造带基础超时的 http.Server。
// 不设 WriteTimeout：远程入口的 WebSocket 是需要长期推送的全双工长连接，写超时按整条连接计时
// 会把它掐断；慢速或半开连接由 ReadHeaderTimeout 与 wsConn 的 ping/pong 探测兜底。
func (s *Server) newHTTPServer(tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           s,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Serve 启动底层 HTTP 监听服务。
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.server = s.newHTTPServer(nil)
	s.mu.Unlock()
	return s.server.Serve(ln)
}

// ServeTLS 启动底层 HTTPS 监听服务。
func (s *Server) ServeTLS(ln net.Listener, cfg *tls.Config) error {
	s.mu.Lock()
	s.server = s.newHTTPServer(cfg)
	s.mu.Unlock()
	tlsLn := tls.NewListener(ln, cfg)
	return s.server.Serve(tlsLn)
}

// whoIsTimeout 限制连接建立时解析传输层身份的最长时间，避免拖慢握手。
const whoIsTimeout = 2 * time.Second

// ServeTLSListener 在自身负责 TLS 握手的 listener 上启动 HTTPS 服务。
// tsnet 的 ListenTLS 返回的 listener 已经带好 TLS 配置，这里不能再包
// tls.NewListener，否则会二次握手而失败。cfg 只用于 http.Server 的服务端
// 字段（如 NextProtos）；证书协商由传入的 listener 完成，因此可以不带证书。
func (s *Server) ServeTLSListener(ln net.Listener, cfg *tls.Config) error {
	s.mu.Lock()
	s.server = s.newHTTPServer(cfg)
	s.mu.Unlock()
	return s.server.Serve(ln)
}

// Shutdown 优雅关闭 HTTP 服务及全部 WebSocket 活跃连接。
func (s *Server) Shutdown(ctx context.Context) error {
	s.connMu.Lock()
	targets := make([]*wsConn, 0, len(s.conns))
	for c := range s.conns {
		targets = append(targets, c)
	}
	s.connMu.Unlock()

	for _, c := range targets {
		_ = c.ws.Close(websocket.StatusNormalClosure, "server shutdown")
		_ = c.Close()
	}

	s.mu.Lock()
	srv := s.server
	s.mu.Unlock()

	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// adminPushTestPath 是独立 server 在本机触发一次测试推送的管理端点。
const adminPushTestPath = "/admin/remote/push-test"

// answerPath 是远程设备的一次性作答端点：Service Worker 点通知按钮后直接 POST，不经过 WebSocket。
const answerPath = "/api/remote/answer"

var staticRouteMap = map[string]string{
	"/":                            "remote-index.html",
	"/index.html":                  "remote-index.html",
	"/remote-boot.js":              "remote-boot.js",
	"/remote-app.js":               "remote-app.js",
	"/remote-app.css":              "remote-app.css",
	"/remote-manifest.webmanifest": "remote-manifest.webmanifest",
	"/remote-sw.js":                "remote-sw.js",
	"/remote-icon.svg":             "remote-icon.svg",
	"/apple-touch-icon-180.png":    "apple-touch-icon-180.png",
	"/icon-192.png":                "icon-192.png",
	"/icon-512.png":                "icon-512.png",
	"/icon-maskable-512.png":       "icon-maskable-512.png",
}

// assetChunkPathPattern 限定懒加载资源路径：/assets/ 下扁平文件名，只允许字母数字点下划线与连字符，
// 扩展名只允许打包产物实际用到的 js、css、字体、svg、png。子目录、查询串、.map、.html 一律不匹配。
var assetChunkPathPattern = regexp.MustCompile(`^/assets/[A-Za-z0-9._-]+\.(?:js|css|woff2|woff|ttf|svg|png)$`)

// immutableAssetCacheControl 是内容哈希文件名的长缓存头：文件名变即内容变，可以永久缓存。
const immutableAssetCacheControl = "public, max-age=31536000, immutable"

// resolveAssetChunkPath 把合法的 /assets/ 请求路径映射为嵌入资源内的相对文件名。
// 入参已在 ServeHTTP 的前置校验里排除 ".." 与反斜杠，这里再用 path.Clean 兜一层，
// 保证清理后仍以 /assets/ 开头且与原路径一致（拒绝 /assets/./x.js 之类的变体）。
func resolveAssetChunkPath(rawPath string) (string, bool) {
	if !strings.HasPrefix(rawPath, "/assets/") {
		return "", false
	}
	if !assetChunkPathPattern.MatchString(rawPath) {
		return "", false
	}
	cleaned := path.Clean(rawPath)
	if cleaned != rawPath || !strings.HasPrefix(cleaned, "/assets/") {
		return "", false
	}
	return "assets/" + strings.TrimPrefix(cleaned, "/assets/"), true
}

// ServeHTTP 实现 http.Handler 接口，处理静态文件与 WebSocket 升级。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 绝对拒绝任何携带反向代理特征标头的请求
	if hasForwardedHeaders(r) {
		http.Error(w, "Forbidden: reverse proxy headers rejected", http.StatusForbidden)
		return
	}

	// 校验 Host 与 Origin
	if !s.validateHostAndOrigin(r) {
		http.Error(w, "Forbidden: invalid host or origin", http.StatusForbidden)
		return
	}

	// 路径穿越与非法分隔符校验
	if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "\\") || strings.Contains(r.RequestURI, "//") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 本机管理动作：只允许回环地址访问
	if r.URL.Path == adminPushTestPath {
		if ip := extractClientIP(r.RemoteAddr); ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if len(s.opts.AdminRoutes) == 0 {
			http.NotFound(w, r)
			return
		}
		s.opts.AdminRoutes[0](w, r)
		return
	}

	// WebSocket 升级端点
	if r.URL.Path == "/ws" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleWebSocketUpgrade(w, r)
		return
	}

	// 通知内联作答：必须在静态 GET/HEAD 检查之前分发，否则 POST 会被改写成 405。
	if r.URL.Path == answerPath {
		s.serveAnswer(w, r)
		return
	}

	// 静态文件仅允许 GET 与 HEAD
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	targetFilename, ok := staticRouteMap[r.URL.Path]
	if !ok {
		// 打包产物的按需加载 chunk 与字体：固定文件名白名单之外唯一放行的路径族。
		assetFilename, assetOK := resolveAssetChunkPath(r.URL.Path)
		if !assetOK {
			http.NotFound(w, r)
			return
		}
		s.serveAssetChunk(w, r, assetFilename)
		return
	}

	s.serveStaticAsset(w, r, targetFilename)
}

// serveAnswer 把作答请求交给注入的处理器：LAN 入口恒拒绝，未接线时视为路径不存在。
func (s *Server) serveAnswer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Entry.Secure() {
		http.Error(w, "Forbidden: answers require the HTTPS entry", http.StatusForbidden)
		return
	}
	if s.opts.AnswerHandler == nil {
		http.NotFound(w, r)
		return
	}
	s.opts.AnswerHandler.ServeHTTP(w, r)
}

func (s *Server) handleWebSocketUpgrade(w http.ResponseWriter, r *http.Request) {
	if !s.reserveConn() {
		http.Error(w, "Service Unavailable: connection limit reached", http.StatusServiceUnavailable)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.releaseReservation()
		return
	}

	ws.SetReadLimit(4 * 1024 * 1024)

	ctx, cancel := context.WithCancel(r.Context())
	conn := &wsConn{
		server: s,
		ws:     ws,
		sendCh: make(chan []byte, s.sendQueueSize),
		closed: make(chan struct{}),
		cancel: cancel,
		info: ClientInfo{
			UserAgent: r.UserAgent(),
			Address:   extractClientIP(r.RemoteAddr),
			Entry:     s.opts.Entry,
		},
	}
	// 传输层身份只用于展示与审计：失败或超时都不影响连接建立，字段留空即可。
	if s.opts.WhoIs != nil {
		whoCtx, whoCancel := context.WithTimeout(ctx, whoIsTimeout)
		identity, whoErr := s.opts.WhoIs(whoCtx, r.RemoteAddr)
		whoCancel()
		if whoErr == nil {
			conn.info.TailnetUser = identity.LoginName
			conn.info.TailnetNode = identity.NodeName
		}
	}

	s.addConn(conn)

	conn.unauthTimer = time.AfterFunc(s.unauthTimeout, func() {
		conn.mu.RLock()
		authed := conn.info.Authenticated
		conn.mu.RUnlock()
		if !authed {
			_ = conn.ws.Close(websocket.StatusPolicyViolation, "unauthenticated timeout")
			_ = conn.Close()
		}
	})

	go conn.writePump(ctx)
	conn.readPump(ctx)
}

// serveAssetChunk 提供 /assets/ 下带内容哈希的打包产物，使用不可变长缓存。
// 文件名带哈希，文件不存在就只能说明部署已换版本（旧 chunk 已被清理），按 404 返回，
// 交给 remote-boot.js 走刷新恢复路径。
func (s *Server) serveAssetChunk(w http.ResponseWriter, r *http.Request, filename string) {
	s.serveStaticAssetWithCache(w, r, filename, immutableAssetCacheControl, http.StatusNotFound)
}

// serveStaticAsset 提供固定文件名的应用壳资源，文件名不含哈希，必须每次回源校验。
func (s *Server) serveStaticAsset(w http.ResponseWriter, r *http.Request, filename string) {
	s.serveStaticAssetWithCache(w, r, filename, "no-cache", http.StatusServiceUnavailable)
}

func (s *Server) serveStaticAssetWithCache(w http.ResponseWriter, r *http.Request, filename string, cacheControl string, missingStatus int) {
	s.assetMu.RLock()
	asset, ok := s.assets[filename]
	s.assetMu.RUnlock()

	if !ok {
		var err error
		asset, err = s.loadAsset(filename)
		if err != nil {
			if missingStatus == http.StatusServiceUnavailable {
				http.Error(w, "Service Unavailable: Remote web asset not packaged yet.", missingStatus)
				return
			}
			http.NotFound(w, r)
			return
		}
		s.assetMu.Lock()
		s.assets[filename] = asset
		s.assetMu.Unlock()
	}

	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", asset.etag)
	w.Header().Set("Vary", "Accept-Encoding")

	if strings.HasSuffix(filename, ".html") {
		// 内联脚本的 sha256 按实际返回的 HTML 实时计算，含构建后 dist 产物，无需手工同步。
		w.Header().Set("Content-Security-Policy", s.contentSecurityPolicy(filename, asset.data))
		w.Header().Set("Referrer-Policy", "no-referrer")
	}
	if strings.HasSuffix(filename, ".js") {
		// service worker 从根路径注册只需同源；显式放开 scope，避免将来挪到子路径时失效。
		w.Header().Set("Service-Worker-Allowed", "/")
	}

	if matchETag(r.Header.Get("If-None-Match"), asset.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := asset.data
	if asset.gzipped != nil && acceptsGzipHeader(r.Header.Get("Accept-Encoding")) {
		body = asset.gzipped
		w.Header().Set("Content-Encoding", "gzip")
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

var (
	// scriptTagPattern 匹配带内容的脚本标签：分组 1 是标签属性，分组 2 是标签之间的原始字节。
	scriptTagPattern = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	// scriptSrcAttributePattern 判断脚本标签是否通过 src 引用外部文件。
	scriptSrcAttributePattern = regexp.MustCompile(`(?i)(?:^|\s)src\s*=`)
)

// inlineScriptHashes 返回 HTML 中所有内联脚本内容的 CSP sha256 表达式（'sha256-…'）。
// 内容按标签之间的原始字节计算，不裁剪空白：浏览器校验的就是这段字节。
func inlineScriptHashes(html []byte) []string {
	matches := scriptTagPattern.FindAllSubmatch(html, -1)
	hashes := make([]string, 0, len(matches))
	for _, m := range matches {
		if scriptSrcAttributePattern.Match(m[1]) {
			continue
		}
		sum := sha256.Sum256(m[2])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

// contentSecurityPolicy 按返回的 HTML 拼出 CSP，并按文件名缓存。嵌入资源在进程生命周期内不变，缓存是安全的。
func (s *Server) contentSecurityPolicy(filename string, html []byte) string {
	s.cspMu.RLock()
	csp, ok := s.cspCache[filename]
	s.cspMu.RUnlock()
	if ok {
		return csp
	}

	scriptSrc := "script-src 'self'"
	for _, hash := range inlineScriptHashes(html) {
		scriptSrc += " " + hash
	}
	csp = "default-src 'self'; " + scriptSrc + "; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; font-src 'self' data:; frame-ancestors 'none'; base-uri 'none';"

	s.cspMu.Lock()
	s.cspCache[filename] = csp
	s.cspMu.Unlock()
	return csp
}

func (s *Server) loadAsset(filename string) (*staticAsset, error) {
	if s.opts.Assets == nil {
		return nil, errors.New("no assets fs")
	}

	f, err := s.opts.Assets.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256(data)
	etag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash[:12]))

	var gzipped []byte
	if len(data) >= 1024 && !strings.HasSuffix(filename, ".png") {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(data); err == nil {
			if err := gz.Close(); err == nil && buf.Len() < len(data) {
				gzipped = buf.Bytes()
			}
		}
	}

	return &staticAsset{
		data:        data,
		gzipped:     gzipped,
		etag:        etag,
		contentType: mimeTypeForFilename(filename),
	}, nil
}

func hasForwardedHeaders(r *http.Request) bool {
	forwardingHeaders := []string{
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Port",
		"X-Forwarded-Proto",
	}
	for _, h := range forwardingHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func (s *Server) validateHostAndOrigin(r *http.Request) bool {
	rawHost := r.Host
	if rawHost == "" {
		rawHost = r.Header.Get("Host")
	}
	if rawHost == "" {
		return false
	}

	reqHost, reqPort := splitHostAndPort(rawHost)

	// 解析服务端监听端口
	_, expectedPort := splitHostAndPort(s.opts.ListenAddr)

	if s.opts.Entry.Secure() {
		// TLS / Tailscale 模式：Host 必须等于配置的域名，端口按缺省 443 归一化后比较。
		// 浏览器访问 443 时 Host 不带端口（reqPort == 0），这里统一按 443 处理。
		normalizedReqPort := reqPort
		if normalizedReqPort == 0 {
			normalizedReqPort = 443
		}
		allowedFound := false
		for _, allowed := range s.opts.AllowedHosts {
			aHost, aPort := splitHostAndPort(allowed)
			if aPort == 0 {
				// 允许项没写端口时按监听端口补全，监听端口也没写则按 443。
				if expectedPort > 0 {
					aPort = expectedPort
				} else {
					aPort = 443
				}
			}
			if strings.EqualFold(reqHost, aHost) && normalizedReqPort == aPort {
				allowedFound = true
				break
			}
		}
		if !allowedFound {
			return false
		}
	} else {
		// LAN 模式：Host 必须为本机活动 IPv4、localhost、127.0.0.1 或 AllowedHosts
		allowedHosts := make(map[string]struct{})
		allowedHosts["localhost"] = struct{}{}
		allowedHosts["127.0.0.1"] = struct{}{}

		for _, ip := range listActiveIPv4Addresses() {
			allowedHosts[ip] = struct{}{}
		}
		for _, h := range s.opts.AllowedHosts {
			hName, _ := splitHostAndPort(h)
			allowedHosts[strings.ToLower(hName)] = struct{}{}
		}

		if _, ok := allowedHosts[strings.ToLower(reqHost)]; !ok {
			return false
		}

		// 本地回环主机名仅限本地对端访问
		if reqHost == "localhost" || reqHost == "127.0.0.1" {
			peerIP := extractClientIP(r.RemoteAddr)
			if peerIP != "127.0.0.1" && peerIP != "::1" && peerIP != "localhost" {
				return false
			}
		}

		// 检查端口匹配
		if expectedPort > 0 && reqPort != 0 && reqPort != expectedPort {
			return false
		}
	}

	// Origin 校验
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 原生客户端不带 Origin，放行通过
	}

	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}

	originHost, originPort := splitHostAndPort(u.Host)
	if s.opts.Entry.Secure() {
		if !strings.EqualFold(u.Scheme, "https") {
			return false
		}
		// 与 Host 分支同一套端口归一化规则：缺省端口一律按 443 处理再比较。
		if originPort == 0 {
			originPort = 443
		}
		normalizedReqPort := reqPort
		if normalizedReqPort == 0 {
			normalizedReqPort = 443
		}
		if !strings.EqualFold(originHost, reqHost) || originPort != normalizedReqPort {
			return false
		}
	} else {
		if !strings.EqualFold(u.Scheme, "http") {
			return false
		}
		if originPort == 0 {
			originPort = 80
		}
		if !strings.EqualFold(originHost, reqHost) || (reqPort != 0 && originPort != reqPort) {
			return false
		}
	}

	return true
}

// splitHostAndPort 拆分主机与端口，并把主机名归一化为小写、去掉末尾根点。
// 浏览器访问 FQDN 时可能带上根点（如 mcode.xxx.ts.net.），不归一化会被 Host 校验误拒。
func splitHostAndPort(hostport string) (string, int) {
	hostport = strings.TrimSpace(hostport)
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		port, _ := strconv.Atoi(p)
		return normalizeHost(h), port
	}
	return normalizeHost(hostport), 0
}

// normalizeHost 小写化主机名并去掉末尾的一个根点。
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// extractClientIP 统一清洗对端地址：去端口、方括号、zone 与 IPv4 映射前缀。
// 所有入口（含 tailscale）都用同一套逻辑，保证配对封禁键等场景下取到同一个值。
func extractClientIP(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = h
	}
	remoteAddr = strings.Trim(remoteAddr, "[]")
	if idx := strings.Index(remoteAddr, "%"); idx >= 0 {
		remoteAddr = remoteAddr[:idx]
	}
	if strings.HasPrefix(remoteAddr, "::ffff:") {
		remoteAddr = remoteAddr[7:]
	}
	return remoteAddr
}

func listActiveIPv4Addresses() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				ips = append(ips, ip.To4().String())
			}
		}
	}
	return ips
}

func mimeTypeForFilename(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(filename, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(filename, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(filename, ".webmanifest"):
		return "application/manifest+json; charset=utf-8"
	case strings.HasSuffix(filename, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(filename, ".png"):
		return "image/png"
	case strings.HasSuffix(filename, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(filename, ".woff"):
		return "font/woff"
	case strings.HasSuffix(filename, ".ttf"):
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}

func matchETag(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if strings.HasPrefix(tag, "W/") {
			tag = tag[2:]
		}
		if tag == "*" || tag == etag {
			return true
		}
	}
	return false
}

func acceptsGzipHeader(header string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		pieces := strings.Split(part, ";")
		if strings.TrimSpace(pieces[0]) == "gzip" {
			hasQ0 := false
			for _, param := range pieces[1:] {
				if strings.TrimSpace(param) == "q=0" {
					hasQ0 = true
					break
				}
			}
			if !hasQ0 {
				return true
			}
		}
	}
	return false
}
