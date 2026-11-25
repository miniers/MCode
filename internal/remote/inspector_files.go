package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
	"time"

	"mcode/internal/designpick"
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
	remotePreviewTokenTTL         = 1 * time.Hour
)

// inspectorPreviewCSP 是 HTML 响应的内容安全策略：放行同源与内联脚本以支持可交互设计稿，跨源连接、表单、对象仍全部关闭。
const inspectorPreviewCSP = "default-src 'none'; script-src 'self' 'unsafe-inline'; object-src 'none'; " +
	"base-uri 'none'; frame-ancestors 'none'; form-action 'none'; connect-src 'none'; " +
	"img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'"

// remotePreviewCSP 在桌面端预览策略的基础上允许同源 iframe 嵌入并强制沙箱执行（不授 allow-same-origin），
// 防止设计稿脚本读取主应用存储在同源下的登录凭据。
var remotePreviewCSP = strings.Replace(inspectorPreviewCSP, "frame-ancestors 'none'", "frame-ancestors 'self'", 1) +
	"; sandbox allow-scripts"

// inspectorPreviewSVGCSP 为 SVG 顶层预览保留严格禁脚本策略。
const inspectorPreviewSVGCSP = "default-src 'none'; script-src 'none'; object-src 'none'; " +
	"base-uri 'none'; frame-ancestors 'none'; form-action 'none'; connect-src 'none'; " +
	"img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; media-src 'self'"

// inspectorPreviewGrant 是一个能力 Token 的作用域：绑定会话、工作区根与入口文件。
// html 为真时允许同一 root 内的相对资源；否则只允许入口文件本身。
// remote 为真表示远程端沙箱预览令牌，受 expiresAt 有效期约束且仅可经远程服务访问。
type inspectorPreviewGrant struct {
	threadID  string
	root      string
	entry     string
	html      bool
	remote    bool
	expiresAt time.Time
}

// inspectorPreviewServer 是与 remote 管理服务完全隔离的 loopback 内容服务：独立监听地址、独立处理器。
type inspectorPreviewServer struct {
	backend *MemoryBackend
	addr    string
	http    *http.Server
}

func newInspectorPreviewServer(b *MemoryBackend, addr string) *inspectorPreviewServer {
	return &inspectorPreviewServer{
		backend: b,
		addr:    addr,
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
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), inspectorPreviewShutdownGrace)
		defer cancel()
		if err := srv.http.Shutdown(ctx); err != nil {
			_ = srv.http.Close()
		}
	}
	b.inspectorPreviewMu.Lock()
	b.inspectorPreviewGrants = nil
	b.inspectorPreviewMu.Unlock()
}

func (b *MemoryBackend) previewNow() time.Time {
	if b.inspectorPreviewNow != nil {
		return b.inspectorPreviewNow()
	}
	return time.Now()
}

// resolvePreviewTarget 校验预览请求的会话、路径与安全边界，返回会话工作区根与归一化的相对入口路径。
func (b *MemoryBackend) resolvePreviewTarget(ctx context.Context, threadID, relPath string) (string, string, error) {
	if threadID == "" {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "threadID is required"}
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "path is required"}
	}

	root, err := b.threadWorkspaceRoot(threadID)
	if err != nil {
		return "", "", err
	}
	target, err := resolveWorkspacePath(root, relPath)
	if err != nil {
		return "", "", err
	}
	if isGitRelated(relPath, target) {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "cannot preview .git internals"}
	}
	if isProtected(relPath, target) {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "file is protected and cannot be previewed"}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", &wire.RPCError{Code: wire.ErrCodeFileNotFound, Message: fmt.Sprintf("file not found: %s", relPath)}
	}
	if !info.Mode().IsRegular() {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "not a regular file"}
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", &wire.RPCError{Code: wire.InvalidParams, Message: "path escapes workspace root"}
	}
	return root, filepath.ToSlash(rel), nil
}

// InspectorPreviewURL 为会话工作区里的一个文件签发短生命周期的 loopback URL。
// 每次调用都重新解析会话根、工作区路径与保护规则，Token 绑定 (threadID, root, 入口相对路径)。
func (b *MemoryBackend) InspectorPreviewURL(ctx context.Context, threadID, relPath string) (string, error) {
	root, entry, err := b.resolvePreviewTarget(ctx, threadID, relPath)
	if err != nil {
		return "", err
	}

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
	if !b.addPreviewGrant(token, grant) {
		return "", errors.New("本地预览服务已关闭")
	}
	return "http://" + srv.addr + inspectorPreviewURLPrefix + token + "/" + escapeInspectorPreviewPath(entry), nil
}

// RemotePreviewPath 为远程端沙箱预览签发有效期 1 小时的能力令牌，返回同源相对路径 /p/<token>/<entry>。
// 不会触发本地回环内容服务的懒启动。
func (b *MemoryBackend) RemotePreviewPath(ctx context.Context, threadID, relPath string) (string, error) {
	root, entry, err := b.resolvePreviewTarget(ctx, threadID, relPath)
	if err != nil {
		return "", err
	}
	token, err := newInspectorPreviewToken()
	if err != nil {
		return "", fmt.Errorf("生成预览令牌失败: %w", err)
	}
	grant := inspectorPreviewGrant{
		threadID:  threadID,
		root:      root,
		entry:     entry,
		html:      inspectorPreviewHTMLPath(entry),
		remote:    true,
		expiresAt: b.previewNow().Add(remotePreviewTokenTTL),
	}
	if !b.addPreviewGrant(token, grant) {
		return "", errors.New("本地预览服务已关闭")
	}
	return inspectorPreviewURLPrefix + token + "/" + escapeInspectorPreviewPath(entry), nil
}

// RemotePreviewHandler 返回挂载在远程服务 /p/ 路由下的沙箱预览处理器。
func (b *MemoryBackend) RemotePreviewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b == nil {
			inspectorPreviewError(w, http.StatusNotFound, "not found")
			return
		}
		b.servePreview(w, r, true)
	})
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
	if srv != nil && u.Host == srv.addr {
		delete(b.inspectorPreviewGrants, token)
	}
	b.inspectorPreviewMu.Unlock()
}

// RevokeInspectorPreviews 撤销某个会话的全部预览 Token（含桌面端与远程端）。
func (b *MemoryBackend) RevokeInspectorPreviews(threadID string) {
	if threadID == "" {
		return
	}
	b.inspectorPreviewMu.Lock()
	defer b.inspectorPreviewMu.Unlock()
	for token, grant := range b.inspectorPreviewGrants {
		if grant.threadID == threadID {
			delete(b.inspectorPreviewGrants, token)
		}
	}
}

func newInspectorPreviewToken() (string, error) {
	buf := make([]byte, inspectorPreviewTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (b *MemoryBackend) addPreviewGrant(token string, grant inspectorPreviewGrant) bool {
	b.inspectorPreviewMu.Lock()
	defer b.inspectorPreviewMu.Unlock()
	if b.inspectorPreviewClosed {
		return false
	}
	if b.inspectorPreviewGrants == nil {
		b.inspectorPreviewGrants = make(map[string]inspectorPreviewGrant)
	}
	b.inspectorPreviewGrants[token] = grant
	return true
}

func (b *MemoryBackend) lookupPreviewGrant(token string, wantRemote bool) (inspectorPreviewGrant, bool) {
	b.inspectorPreviewMu.Lock()
	defer b.inspectorPreviewMu.Unlock()
	if b.inspectorPreviewClosed {
		return inspectorPreviewGrant{}, false
	}
	grant, ok := b.inspectorPreviewGrants[token]
	if !ok {
		return inspectorPreviewGrant{}, false
	}
	if grant.remote != wantRemote {
		return inspectorPreviewGrant{}, false
	}
	if !grant.expiresAt.IsZero() && !b.previewNow().Before(grant.expiresAt) {
		delete(b.inspectorPreviewGrants, token)
		return inspectorPreviewGrant{}, false
	}
	return grant, true
}

// ServeHTTP 处理回环内容请求：校验方法与回环 Host 后转交共用的 servePreview 逻辑。
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
	srv.backend.servePreview(w, r, false)
}

// servePreview 统一处理桌面回环预览与远程沙箱预览的令牌校验、路径清理、安全拦截与响应头写入。
func (b *MemoryBackend) servePreview(w http.ResponseWriter, r *http.Request, remoteMode bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		inspectorPreviewError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token, rawRel, ok := inspectorPreviewRequestPath(r.URL.Path)
	if !ok {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	grant, ok := b.lookupPreviewGrant(token, remoteMode)
	if !ok {
		inspectorPreviewError(w, http.StatusNotFound, "not found")
		return
	}
	root, err := b.threadWorkspaceRoot(grant.threadID)
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

	isHTML := inspectorPreviewHTMLPath(target)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", inspectorPreviewContentType(target))
	// SVG 作为顶层文档打开时也可能执行脚本；虽然 UI 不在 WebView 里渲染 SVG，
	// 这里仍为 SVG 保留严格禁脚本策略，避免被 HTML 里的同源链接带进可执行上下文。
	if isHTML {
		if remoteMode {
			w.Header().Set("Content-Security-Policy", remotePreviewCSP)
		} else {
			w.Header().Set("Content-Security-Policy", inspectorPreviewCSP)
		}
	} else if strings.EqualFold(filepath.Ext(target), ".svg") {
		w.Header().Set("Content-Security-Policy", inspectorPreviewSVGCSP)
	}

	var content io.ReadSeeker = f
	if remoteMode && isHTML {
		data, readErr := io.ReadAll(io.LimitReader(f, inspectorPreviewMaxFileBytes+1))
		if readErr != nil {
			inspectorPreviewError(w, http.StatusInternalServerError, "read failed")
			return
		}
		if int64(len(data)) > inspectorPreviewMaxFileBytes {
			inspectorPreviewError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		}
		if bytes.Contains(data, []byte("data-mc-choice")) {
			data = injectDesignPickScript(data)
		}
		content = bytes.NewReader(data)
	}
	// 不传修改时间：no-store 下不做 304，Range 仍由 ServeContent 处理。
	http.ServeContent(w, r, filepath.Base(target), time.Time{}, content)
}

func findHeadInsertOffset(html []byte) int {
	const prefix = "<head"
	for i := 0; i+len(prefix) < len(html); i++ {
		if html[i] != '<' || !strings.EqualFold(string(html[i:i+len(prefix)]), prefix) {
			continue
		}
		next := html[i+len(prefix)]
		if next == '>' {
			return i + len(prefix) + 1
		}
		if next == ' ' || next == '\t' || next == '\r' || next == '\n' {
			if end := bytes.IndexByte(html[i+len(prefix):], '>'); end >= 0 {
				return i + len(prefix) + end + 1
			}
		}
	}
	return 0
}

func injectDesignPickScript(html []byte) []byte {
	tag := "<script>" + designpick.Script + "</script>"
	offset := findHeadInsertOffset(html)
	out := make([]byte, 0, len(html)+len(tag))
	out = append(out, html[:offset]...)
	out = append(out, tag...)
	out = append(out, html[offset:]...)
	return out
}

// RemotePreviewProvider 为 Dispatcher 提供签发远程沙箱预览路径的可选后端接口。
type RemotePreviewProvider interface {
	RemotePreviewPath(ctx context.Context, threadID, relPath string) (string, error)
}

// handleThreadsPreviewOpen 处理 threads.preview.open JSON-RPC 请求。
func (d *Dispatcher) handleThreadsPreviewOpen(
	ctx context.Context,
	params json.RawMessage,
	replySuccess func(any) *wire.Response,
	replyErr func(int, string) *wire.Response,
	replyWithBackendErr func(error) *wire.Response,
) *wire.Response {
	var p wire.ThreadsPreviewOpenParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Path) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'path' is required")
	}

	provider, ok := d.backend.(RemotePreviewProvider)
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "preview backend unavailable")
	}
	previewPath, err := provider.RemotePreviewPath(ctx, p.ThreadID, p.Path)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(wire.ThreadsPreviewOpenResult{URL: previewPath})
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
