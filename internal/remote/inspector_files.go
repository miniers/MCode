package remote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

// 本地内容服务的固定参数：只监听回环地址，单文件上限 50 MiB，
// 读写超时防止被挂起的连接长期占用，Token 用 32 字节随机数。
const (
	inspectorPreviewListenAddr    = "127.0.0.1:0"
	inspectorPreviewURLPrefix     = "/p/"
	inspectorPreviewTokenBytes    = 32
	inspectorPreviewMaxFileBytes  = 50 << 20
	inspectorPreviewShutdownGrace = 2 * time.Second
)

// inspectorPreviewCSP 是 HTML 响应的内容安全策略：默认什么都不允许，
// 只放行同源的样式、图片与字体；脚本、对象、表单与跨源连接全部关闭。
const inspectorPreviewCSP = "default-src 'none'; script-src 'none'; object-src 'none'; " +
	"base-uri 'none'; frame-ancestors 'none'; form-action 'none'; connect-src 'none'; " +
	"img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; media-src 'self'"

// inspectorPreviewGrant 是一个能力 Token 的作用域：绑定会话、工作区根与入口文件。
// html 为真时允许同一 root 内的相对资源；否则只允许入口文件本身。
type inspectorPreviewGrant struct {
	threadID string
	root     string
	entry    string
	html     bool
}

// inspectorPreviewServer 是与 remote 管理服务完全隔离的 loopback 内容服务：独立监听地址、独立处理器。
type inspectorPreviewServer struct {
	backend *MemoryBackend
	addr    string
	http    *http.Server

	mu     sync.Mutex
	grants map[string]inspectorPreviewGrant
	closed bool
}

func newInspectorPreviewServer(b *MemoryBackend, addr string) *inspectorPreviewServer {
	return &inspectorPreviewServer{
		backend: b,
		addr:    addr,
		grants:  make(map[string]inspectorPreviewGrant),
	}
}

// inspectorPreviewServer 返回（必要时懒启动）本地内容服务。所有启动与关闭都经 backend 锁串行化。
func (b *MemoryBackend) inspectorPreviewServer() (*inspectorPreviewServer, error) {
	b.inspectorPreviewMu.Lock()
	defer b.inspectorPreviewMu.Unlock()
	if b.inspectorPreviewClosed {
		return nil, errors.New("本地预览服务已关闭")
	}
	if b.inspectorPreview != nil {
		return b.inspectorPreview, nil
	}

	listener, err := net.Listen("tcp", inspectorPreviewListenAddr)
	if err != nil {
		return nil, fmt.Errorf("启动本地预览服务失败: %w", err)
	}
	srv := newInspectorPreviewServer(b, listener.Addr().String())
	srv.http = &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		// 默认的错误日志可能带上完整请求地址（含 Token），这里直接丢弃。
		ErrorLog: log.New(io.Discard, "", 0),
	}
	b.inspectorPreview = srv
	go func() {
		if serveErr := srv.http.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// 只记录错误本身，不输出监听地址或请求路径。
			log.Printf("本地预览服务退出: %v", serveErr)
		}
	}()
	return srv, nil
}

// closeInspectorPreviewServer 关闭监听与所有在飞请求，并清空 Token。
func (b *MemoryBackend) closeInspectorPreviewServer() {
	b.inspectorPreviewMu.Lock()
	b.inspectorPreviewClosed = true
	srv := b.inspectorPreview
	b.inspectorPreview = nil
	b.inspectorPreviewMu.Unlock()
	if srv == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), inspectorPreviewShutdownGrace)
	defer cancel()
	if err := srv.http.Shutdown(ctx); err != nil {
		_ = srv.http.Close()
	}
	srv.mu.Lock()
	srv.closed = true
	srv.grants = nil
	srv.mu.Unlock()
}

// InspectorPreviewURL 为会话工作区里的一个文件签发短生命周期的 loopback URL。
// 每次调用都重新解析会话根、工作区路径与保护规则，Token 绑定 (threadID, root, 入口相对路径)。
func (b *MemoryBackend) InspectorPreviewURL(ctx context.Context, threadID, relPath string) (string, error) {
	if threadID == "" {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "threadID is required"}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "path is required"}
	}

	root, err := b.threadWorkspaceRoot(threadID)
	if err != nil {
		return "", err
	}
	target, err := resolveWorkspacePath(root, relPath)
	if err != nil {
		return "", err
	}
	if isGitRelated(relPath, target) {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "cannot preview .git internals"}
	}
	if isProtected(relPath, target) {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "file is protected and cannot be previewed"}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", &wire.RPCError{Code: wire.ErrCodeFileNotFound, Message: fmt.Sprintf("file not found: %s", relPath)}
	}
	if !info.Mode().IsRegular() {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "not a regular file"}
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &wire.RPCError{Code: wire.InvalidParams, Message: "path escapes workspace root"}
	}
	entry := filepath.ToSlash(rel)

	srv, err := b.inspectorPreviewServer()
	if err != nil {
		return "", err
	}
	token, err := newInspectorPreviewToken()
	if err != nil {
		return "", fmt.Errorf("生成预览令牌失败: %w", err)
	}
	grant := inspectorPreviewGrant{
		threadID: threadID,
		root:     root,
		entry:    entry,
		html:     inspectorPreviewHTMLPath(entry),
	}
	if !srv.addGrant(token, grant) {
		return "", errors.New("本地预览服务已关闭")
	}
	return "http://" + srv.addr + inspectorPreviewURLPrefix + token + "/" + escapeInspectorPreviewPath(entry), nil
}

// RevokeInspectorPreviewURL 只撤销本服务签发的单个 Token，不影响同会话的兄弟标签。
func (b *MemoryBackend) RevokeInspectorPreviewURL(previewURL string) {
	u, err := url.Parse(previewURL)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return
	}
	token, _, ok := inspectorPreviewRequestPath(u.Path)
	if !ok {
		return
	}
	b.inspectorPreviewMu.Lock()
	srv := b.inspectorPreview
	b.inspectorPreviewMu.Unlock()
	if srv == nil || u.Host != srv.addr {
		return
	}
	srv.mu.Lock()
	delete(srv.grants, token)
	srv.mu.Unlock()
}

// RevokeInspectorPreviews 撤销某个会话的全部预览 Token；服务未启动时无事发生。
func (b *MemoryBackend) RevokeInspectorPreviews(threadID string) {
	if threadID == "" {
		return
	}
	b.inspectorPreviewMu.Lock()
	srv := b.inspectorPreview
	b.inspectorPreviewMu.Unlock()
	if srv == nil {
		return
	}
	srv.revokeThread(threadID)
}

func newInspectorPreviewToken() (string, error) {
	buf := make([]byte, inspectorPreviewTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (srv *inspectorPreviewServer) addGrant(token string, grant inspectorPreviewGrant) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.closed {
		return false
	}
	if srv.grants == nil {
		srv.grants = make(map[string]inspectorPreviewGrant)
	}
	srv.grants[token] = grant
	return true
}

func (srv *inspectorPreviewServer) grant(token string) (inspectorPreviewGrant, bool) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	grant, ok := srv.grants[token]
	return grant, ok
}

func (srv *inspectorPreviewServer) revokeThread(threadID string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for token, grant := range srv.grants {
		if grant.threadID == threadID {
			delete(srv.grants, token)
		}
	}
}

// ServeHTTP 处理内容请求：校验 Host、方法、Token、会话根与路径，再按扩展名返回文件。
func (srv *inspectorPreviewServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		inspectorPreviewError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Host != srv.addr {
		inspectorPreviewError(w, http.StatusForbidden, "forbidden")
		return
	}
	token, rawRel, ok := inspectorPreviewRequestPath(r.URL.Path)
	if !ok {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	grant, ok := srv.grant(token)
	if !ok {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	root, err := srv.backend.threadWorkspaceRoot(grant.threadID)
	if err != nil || root != grant.root {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	rel, ok := cleanInspectorPreviewPath(rawRel)
	if !ok || (!grant.html && rel != grant.entry) {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	target, err := resolveWorkspacePath(grant.root, rel)
	if err != nil {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	if isGitRelated(rel, target) || isProtected(rel, target) {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	if info.Size() > inspectorPreviewMaxFileBytes {
		inspectorPreviewError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}

	f, err := os.Open(target)
	if err != nil {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", inspectorPreviewContentType(target))
	// SVG 作为顶层文档打开时也可能执行脚本；虽然 UI 不在 WebView 里渲染 SVG，
	// 这里仍给它同样的限制，避免被 HTML 里的同源链接带进可执行上下文。
	if inspectorPreviewHTMLPath(target) || strings.EqualFold(filepath.Ext(target), ".svg") {
		w.Header().Set("Content-Security-Policy", inspectorPreviewCSP)
	}
	// 不传修改时间：no-store 下不做 304，Range 仍由 ServeContent 处理。
	http.ServeContent(w, r, filepath.Base(target), time.Time{}, f)
}

func inspectorPreviewError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, message, status)
}

// inspectorPreviewRequestPath 从 /p/<token>/<相对路径> 中取出 Token 与路径，不负责解码。
func inspectorPreviewRequestPath(p string) (string, string, bool) {
	if !strings.HasPrefix(p, inspectorPreviewURLPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(p, inspectorPreviewURLPrefix)
	idx := strings.IndexByte(rest, '/')
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// cleanInspectorPreviewPath 清理 URL 中已解码的相对路径：拒绝空路径、绝对路径和任何 ".." 段。
func cleanInspectorPreviewPath(rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, '\x00') {
		return "", false
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", false
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func escapeInspectorPreviewPath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func inspectorPreviewHTMLPath(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".html", ".htm", ".xhtml":
		return true
	default:
		return false
	}
}

var inspectorPreviewContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".xhtml": "application/xhtml+xml; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "application/xml; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".pdf":   "application/pdf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".mp3":   "audio/mpeg",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
}

func inspectorPreviewContentType(name string) string {
	if ct, ok := inspectorPreviewContentTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}
