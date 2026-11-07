package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"

	"mcode/internal/remote/wire"
)

// 协议常量定义
const (
	maxAttachmentBytes           = 2936012                 // ≈ 2.8 MiB
	maxLocalAttachmentBytes      = 20 * 1024 * 1024        // 本地桌面进程单文件上传上限 20 MiB
	maxTotalAttachmentBytes      = 8 * 1024 * 1024         // 未引用附件总大小上限 8 MiB
	maxLocalTotalAttachmentBytes = 50 * 1024 * 1024        // 本地桌面进程未引用附件总大小上限 50 MiB
	attachmentLifetime           = 1 * time.Hour           // 附件未被引用时的存活时间 (1小时)
	maxFullBytes                 = int64(8 * 1024 * 1024)  // full 模式原图大小上限 8 MiB
	maxChunkBytes                = 256 * 1024              // 单块读取上限 256 KiB
	maxThumbSourceBytes          = int64(64 * 1024 * 1024) // thumb 允许解码的原图源文件上限 64 MiB
	thumbMaxEdge                 = 480                     // 缩略图长边最大像素值
	thumbJPEGQuality             = 80                      // 缩略图 JPEG 压缩质量
)

type localUploadContextKey struct{}

// WithLocalUpload 返回带有本地进程上传标记的 context。
func WithLocalUpload(ctx context.Context) context.Context {
	return context.WithValue(ctx, localUploadContextKey{}, true)
}

func isLocalUpload(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if v, ok := ctx.Value(localUploadContextKey{}).(bool); ok && v {
		return true
	}
	if v, ok := ctx.Value("local_upload").(bool); ok && v {
		return true
	}
	return false
}

// MediaBackend 定义媒体与附件操作的后端接口契约。
type MediaBackend interface {
	AttachmentsUpload(ctx context.Context, params wire.AttachmentsUploadParams) (wire.AttachmentsUploadResult, error)
	ThreadsMediaRead(ctx context.Context, params wire.ThreadsMediaReadParams) (wire.ThreadsMediaReadResult, error)
}

// mediaBackend 对 d.backend 进行类型断言，获取 MediaBackend 接口。
func (d *Dispatcher) mediaBackend() (MediaBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	mb, ok := d.backend.(MediaBackend)
	return mb, ok
}

type deviceIDContextKey struct{}

// WithDeviceID 返回附加 deviceID 的 context。
func WithDeviceID(ctx context.Context, deviceID string) context.Context {
	return context.WithValue(ctx, deviceIDContextKey{}, deviceID)
}

// DeviceIDFromContext 从 context 中尝试提取 deviceID。
func DeviceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(deviceIDContextKey{}).(string); ok && v != "" {
		return v
	}
	if v, ok := ctx.Value("deviceID").(string); ok && v != "" {
		return v
	}
	if v, ok := ctx.Value("device_id").(string); ok && v != "" {
		return v
	}
	return ""
}

// currentDeviceID 从 context 或 Dispatcher 关联的已认证连接中解析设备 ID。
func (d *Dispatcher) currentDeviceID(ctx context.Context) string {
	if id := DeviceIDFromContext(ctx); id != "" {
		return id
	}
	if d == nil {
		return ""
	}
	d.authedMu.RLock()
	defer d.authedMu.RUnlock()
	var candidate string
	for c := range d.authedConns {
		id := c.Info().DeviceID
		if id == "" {
			continue
		}
		if candidate == "" {
			candidate = id
		} else if candidate != id {
			// 存在多个不同设备的活跃连接，无法在缺少上下文时明确归属
			return ""
		}
	}
	return candidate
}

// handleAttachmentsUpload 处理 attachments.upload JSON-RPC 请求。
func (d *Dispatcher) handleAttachmentsUpload(
	ctx context.Context,
	params json.RawMessage,
	replySuccess func(any) *wire.Response,
	replyErr func(int, string) *wire.Response,
	replyWithBackendErr func(error) *wire.Response,
) *wire.Response {
	var p wire.AttachmentsUploadParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if p.Name == "" || len(p.Name) > 255 {
		return replyErr(wire.InvalidParams, "Invalid params: 'name' must be between 1 and 255 characters")
	}
	if p.Data == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'data' cannot be empty")
	}

	backend, ok := d.mediaBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "media backend unavailable")
	}

	// 关联 dispatcher 到附件存储，并将已认证设备 ID 注入 context
	if devID := d.currentDeviceID(ctx); devID != "" && DeviceIDFromContext(ctx) == "" {
		ctx = WithDeviceID(ctx, devID)
	}
	if mb, ok := backend.(*MemoryBackend); ok && mb.attachments != nil {
		mb.attachments.setDispatcher(d)
	}

	res, err := backend.AttachmentsUpload(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

// handleThreadsMediaRead 处理 threads.media.read JSON-RPC 请求。
func (d *Dispatcher) handleThreadsMediaRead(
	ctx context.Context,
	params json.RawMessage,
	replySuccess func(any) *wire.Response,
	replyErr func(int, string) *wire.Response,
	replyWithBackendErr func(error) *wire.Response,
) *wire.Response {
	var p wire.ThreadsMediaReadParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.MessageID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'messageID' must be a valid UUID")
	}
	if p.MediaID == "" || len(p.MediaID) > 128 {
		return replyErr(wire.InvalidParams, "Invalid params: 'mediaID' must be between 1 and 128 characters")
	}
	if p.Variant != wire.MediaVariantThumb && p.Variant != wire.MediaVariantFull {
		return replyErr(wire.InvalidParams, "Invalid params: 'variant' must be 'thumb' or 'full'")
	}
	if p.Offset != nil && *p.Offset < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'offset' must be non-negative")
	}
	if p.Length != nil && (*p.Length <= 0 || *p.Length > 512*1024) {
		return replyErr(wire.InvalidParams, "Invalid params: 'length' must be between 1 and 524288")
	}

	backend, ok := d.mediaBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "media backend unavailable")
	}
	res, err := backend.ThreadsMediaRead(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

// attachmentRecord 记录未被消息引用的单个暂存附件元数据。
type attachmentRecord struct {
	id        string
	deviceID  string // 上传该附件的设备 ID，用于设备间隔离
	name      string
	mimeType  string
	byteCount int
	createdAt time.Time
	path      string
	queueRefs int
}

// attachmentStore 管理未被消息引用的暂存附件生命周期与磁盘存储。
type attachmentStore struct {
	mu         sync.Mutex
	now        func() time.Time
	items      map[string]*attachmentRecord
	tempDir    string
	dispatcher *Dispatcher
}

func (s *attachmentStore) setDispatcher(d *Dispatcher) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatcher = d
}

func (s *attachmentStore) resolveDeviceID(ctx context.Context) string {
	if id := DeviceIDFromContext(ctx); id != "" {
		return id
	}
	if s == nil {
		return ""
	}
	s.mu.Lock()
	d := s.dispatcher
	s.mu.Unlock()
	if d != nil {
		return d.currentDeviceID(ctx)
	}
	return ""
}

func (s *attachmentStore) resolveCallerDeviceID(callerDeviceIDs ...string) string {
	if len(callerDeviceIDs) > 0 && callerDeviceIDs[0] != "" {
		return callerDeviceIDs[0]
	}
	return s.resolveDeviceID(nil)
}

// SetAttachmentsDispatcher 设置关联的 Dispatcher，用于在无法从 context 取得设备 ID 时读取已认证连接信息。
func (b *MemoryBackend) SetAttachmentsDispatcher(d *Dispatcher) {
	if b.attachments != nil {
		b.attachments.setDispatcher(d)
	}
}

func newAttachmentStore() *attachmentStore {
	return &attachmentStore{
		now:   time.Now,
		items: make(map[string]*attachmentRecord),
	}
}

func (s *attachmentStore) setNow(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn == nil {
		s.now = time.Now
	} else {
		s.now = fn
	}
}

func (s *attachmentStore) currentNow() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// cleanupExpired 清理超过 1 小时仍未被引用的附件文件及索引。
func (s *attachmentStore) cleanupExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.now != nil {
		now = s.now()
	}

	var expired []string
	for id, item := range s.items {
		if item.queueRefs > 0 {
			continue
		}
		if now.Sub(item.createdAt) >= attachmentLifetime {
			expired = append(expired, id)
		}
	}

	for _, id := range expired {
		item := s.items[id]
		_ = os.Remove(item.path)
		delete(s.items, id)
	}
	return len(expired)
}

func (s *attachmentStore) ensureTempDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tempDir == "" {
		d, err := os.MkdirTemp("", "mcode-attachments-*")
		if err == nil {
			s.tempDir = d
		}
	}
	return s.tempDir
}

// sweepOrphanFiles 清理暂存目录中超过 1 小时且未被索引的孤儿文件
// （例如宿主重启后遗留在磁盘上的暂存附件）；已索引或新写入的文件不会被删除。
func (s *attachmentStore) sweepOrphanFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	s.mu.Lock()
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	indexed := make(map[string]bool, len(s.items))
	for _, item := range s.items {
		indexed[item.path] = true
	}
	s.mu.Unlock()

	cutoff := now.Add(-attachmentLifetime)
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			// 支持清理设备子目录中的孤儿文件
			subEntries, err := os.ReadDir(fullPath)
			if err == nil {
				for _, sub := range subEntries {
					if sub.IsDir() {
						continue
					}
					subPath := filepath.Join(fullPath, sub.Name())
					if indexed[subPath] {
						continue
					}
					if info, err := sub.Info(); err == nil && info.ModTime().Before(cutoff) {
						_ = os.Remove(subPath)
					}
				}
			}
			continue
		}
		if indexed[fullPath] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(fullPath)
	}
}

func (s *attachmentStore) get(id string) (*attachmentRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *attachmentStore) getLocked(id string) (*attachmentRecord, bool) {
	item, ok := s.items[id]
	if !ok {
		return nil, false
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if item.queueRefs == 0 && now.Sub(item.createdAt) >= attachmentLifetime {
		_ = os.Remove(item.path)
		delete(s.items, id)
		return nil, false
	}
	return item, true
}

func (s *attachmentStore) pinAttachments(ids []string, callerDeviceIDs ...string) ([]wire.Attachment, error) {
	if len(ids) == 0 {
		return []wire.Attachment{}, nil
	}
	if s == nil {
		return nil, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "attachment store unavailable"}
	}

	callerDeviceID := s.resolveCallerDeviceID(callerDeviceIDs...)

	s.mu.Lock()
	defer s.mu.Unlock()
	atts := make([]wire.Attachment, 0, len(ids))
	for _, id := range ids {
		rec, ok := s.getLocked(id)
		if !ok {
			return nil, &wire.RPCError{
				Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("attachment %s not found or expired", id),
			}
		}
		// 设备隔离：若能确定调用方设备且记录关联了设备，只允许引用本设备目录下的附件
		if callerDeviceID != "" && rec.deviceID != "" && rec.deviceID != callerDeviceID {
			return nil, &wire.RPCError{
				Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("attachment %s not found or expired", id),
			}
		}
		if callerDeviceID != "" && rec.deviceID != "" {
			expectedSub := filepath.Join("attachments", callerDeviceID)
			if !strings.Contains(filepath.ToSlash(rec.path), filepath.ToSlash(expectedSub)) {
				return nil, &wire.RPCError{
					Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("attachment %s not found or expired", id),
				}
			}
		}
		// 若未能从 context 或 dispatcher 取得设备 ID（如脱机单元测试或无认证调用），退回到原行为并允许引用已索引附件

		byteCount := rec.byteCount
		atts = append(atts, wire.Attachment{
			ID: rec.id, Name: rec.name, MimeType: rec.mimeType, ByteCount: &byteCount,
		})
	}
	for _, id := range ids {
		s.items[id].queueRefs++
	}
	return atts, nil
}

func (s *attachmentStore) unpin(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.items[id]; ok && item.queueRefs > 0 {
		item.queueRefs--
	}
}

func (s *attachmentStore) register(rec *attachmentRecord) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[rec.id]; ok {
		existing.queueRefs += rec.queueRefs
	} else {
		s.items[rec.id] = rec
	}
}

// thumbCache 提供按 mediaID 缓存生成缩略图的内存 LRU/FIFO 缓存。
type thumbCache struct {
	mu    sync.Mutex
	items map[string][]byte
	keys  []string
	limit int
}

func newThumbCache(limit int) *thumbCache {
	if limit <= 0 {
		limit = 256
	}
	return &thumbCache{
		items: make(map[string][]byte),
		keys:  make([]string, 0, limit),
		limit: limit,
	}
}

func (c *thumbCache) get(mediaID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	val, ok := c.items[mediaID]
	return val, ok
}

func (c *thumbCache) put(mediaID string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[mediaID]; exists {
		c.items[mediaID] = data
		return
	}
	if len(c.keys) >= c.limit {
		evict := c.keys[0]
		c.keys = c.keys[1:]
		delete(c.items, evict)
	}
	c.keys = append(c.keys, mediaID)
	c.items[mediaID] = data
}

// attachmentMimeExt 为常见 MIME 类型固定落盘扩展名，避免 mime.ExtensionsByType
// 返回 .jpe 之类不常用的别名（其顺序依赖运行环境的 mime 表）。
var attachmentMimeExt = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
}

// isSafeAttachmentExt 判断扩展名是否安全：点号开头，后接 1-10 个 ASCII 小写字母或数字。
// 借此挡掉路径穿越与奇怪字符。
func isSafeAttachmentExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 11 || ext[0] != '.' {
		return false
	}
	for i := 1; i < len(ext); i++ {
		c := ext[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// attachmentFileExt 推导附件落盘文件的扩展名（含前导点）。
// 优先取原始文件名的扩展名（仅保留安全形式）；文件名没有合法扩展名时按 MIME 类型兜底；
// 仍取不到时返回空串，调用方保持原先无扩展名的落盘方式。
func attachmentFileExt(name, mimeType string) string {
	if ext := strings.ToLower(filepath.Ext(filepath.Base(strings.TrimSpace(name)))); isSafeAttachmentExt(ext) {
		return ext
	}
	mt := strings.ToLower(strings.TrimSpace(mimeType))
	if mt == "" {
		return ""
	}
	if ext, ok := attachmentMimeExt[mt]; ok {
		return ext
	}
	exts, err := mime.ExtensionsByType(mt)
	if err != nil {
		return ""
	}
	for _, candidate := range exts {
		if ext := strings.ToLower(candidate); isSafeAttachmentExt(ext) {
			return ext
		}
	}
	return ""
}

// resolveMediaFilePath 返回会话媒体目录下 mediaID 对应的实际文件路径。
// 新落盘文件带扩展名，历史文件只有裸 ID，因此依次尝试：按记录原始名/MIME 推导的带扩展名路径、
// 裸 ID 路径，最后扫描目录中形如 "<mediaID>.<ext>" 的文件；都不存在时返回裸 ID 路径，
// 由调用方按“文件不存在”处理。
func resolveMediaFilePath(dir, mediaID, name, mimeType string) string {
	legacyPath := filepath.Join(dir, mediaID)
	// mediaID 必须是单段文件名，防止拼接出越出媒体目录的路径
	if mediaID == "" || filepath.Base(mediaID) != mediaID {
		return legacyPath
	}
	if ext := attachmentFileExt(name, mimeType); ext != "" {
		p := filepath.Join(dir, mediaID+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if fi, err := os.Stat(legacyPath); err == nil && !fi.IsDir() {
		return legacyPath
	}
	if entries, err := os.ReadDir(dir); err == nil {
		prefix := mediaID + "."
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if isSafeAttachmentExt(strings.TrimPrefix(entry.Name(), mediaID)) {
				return filepath.Join(dir, entry.Name())
			}
		}
	}
	return legacyPath
}

// detectImageMimeType 识别图片的 MIME 类型，只识别并返回支持的类型：png, jpeg, gif, webp。
func detectImageMimeType(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	detected := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(detected, "image/png"):
		return "image/png"
	case strings.HasPrefix(detected, "image/jpeg"):
		return "image/jpeg"
	case strings.HasPrefix(detected, "image/gif"):
		return "image/gif"
	case strings.HasPrefix(detected, "image/webp"):
		return "image/webp"
	default:
		return detected
	}
}

// detectAttachmentMime 为非图片附件推断 MIME 类型，与桌面端 detectAttachmentMime 的分类保持一致：
// PDF 按扩展名或内容签名识别，其余先按扩展名查系统 MIME 表、再按内容嗅探，最后兜底 octet-stream。
// 声明类型不参与推断——远程客户端可能只给文件名，内容才是权威依据。
func detectAttachmentMime(name string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(strings.TrimSpace(name))))
	if ext == ".pdf" || bytes.HasPrefix(data, []byte("%PDF")) {
		return "application/pdf"
	}
	if m := mime.TypeByExtension(ext); m != "" {
		return strings.TrimSpace(strings.Split(m, ";")[0])
	}
	if m := http.DetectContentType(data); m != "" {
		return strings.TrimSpace(strings.Split(m, ";")[0])
	}
	return "application/octet-stream"
}

func isAcceptedImageMime(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// AttachmentsUpload 实现 attachments.upload 后端逻辑。
func (b *MemoryBackend) AttachmentsUpload(ctx context.Context, params wire.AttachmentsUploadParams) (wire.AttachmentsUploadResult, error) {
	if b.attachments != nil {
		b.attachments.cleanupExpired()
	}

	name := strings.TrimSpace(params.Name)
	if name == "" || len(name) > 255 {
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "附件名称无效",
		}
	}
	safeName := filepath.Base(name)
	if safeName == "." || safeName == "/" || safeName == "" {
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "附件名称无效",
		}
	}

	data, err := base64.StdEncoding.DecodeString(params.Data)
	if err != nil || len(data) == 0 {
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "上传内容为空或 Base64 解码失败",
		}
	}

	// 图片继续走原有的校验与缩略图路径；其余任意文件（PDF、docx、zip、代码、无扩展名二进制等）
	// 与桌面端一致地接受，落盘后把路径交给 agent 自行读取。
	detectedImageMime := detectImageMimeType(data)
	isImage := isAcceptedImageMime(detectedImageMime)

	maxSingle := maxAttachmentBytes
	maxTotal := maxTotalAttachmentBytes
	if isLocalUpload(ctx) {
		maxSingle = maxLocalAttachmentBytes
		maxTotal = maxLocalTotalAttachmentBytes
	}

	if len(data) > maxSingle {
		if isLocalUpload(ctx) {
			return wire.AttachmentsUploadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "单个文件超过 20 MiB",
			}
		}
		if !isImage {
			return wire.AttachmentsUploadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "单个文件超过 2.8 MiB",
			}
		}
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "单张图片超过 2.8 MiB",
		}
	}

	var resolvedMime string
	if isLocalUpload(ctx) {
		if params.MimeType != nil && strings.TrimSpace(*params.MimeType) != "" {
			resolvedMime = strings.TrimSpace(*params.MimeType)
		} else {
			detectedMime := detectImageMimeType(data)
			if detectedMime != "" {
				resolvedMime = detectedMime
			} else {
				resolvedMime = "application/octet-stream"
			}
		}
	} else if isImage {
		resolvedMime = detectedImageMime
		if params.MimeType != nil && strings.TrimSpace(*params.MimeType) != "" {
			decl := strings.TrimSpace(*params.MimeType)
			if !isAcceptedImageMime(decl) {
				return wire.AttachmentsUploadResult{}, &wire.RPCError{
					Code:    wire.InvalidParams,
					Message: "声明的图片类型与实际内容不一致",
				}
			}
			if decl != detectedImageMime {
				return wire.AttachmentsUploadResult{}, &wire.RPCError{
					Code:    wire.InvalidParams,
					Message: "声明的图片类型与实际内容不一致",
				}
			}
			resolvedMime = decl
		}
	} else {
		resolvedMime = detectAttachmentMime(safeName, data)
	}

	b.attachments.mu.Lock()
	var currentTotal int
	for _, item := range b.attachments.items {
		currentTotal += item.byteCount
	}
	if currentTotal+len(data) > maxTotal {
		b.attachments.mu.Unlock()
		if isLocalUpload(ctx) {
			return wire.AttachmentsUploadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "文件总大小超过 50 MiB",
			}
		}
		if !isImage {
			return wire.AttachmentsUploadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "文件总大小超过 8 MiB",
			}
		}
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "图片总大小超过 8 MiB",
		}
	}
	b.attachments.mu.Unlock()

	b.mu.Lock()
	baseDir := b.dataDir
	b.mu.Unlock()
	if baseDir == "" && b.attachments != nil {
		baseDir = b.attachments.ensureTempDir()
	}

	// 按设备 ID 分子目录存储暂存附件；若无法取得设备 ID（如脱机测试），退回到原根目录 attachments/
	var deviceID string
	if b.attachments != nil {
		deviceID = b.attachments.resolveDeviceID(ctx)
	} else {
		deviceID = DeviceIDFromContext(ctx)
	}

	var attachmentsDir string
	if deviceID != "" {
		attachmentsDir = filepath.Join(baseDir, "attachments", deviceID)
	} else {
		// 未能从 context 或 dispatcher 取得设备 ID 时（如脱机单元测试或无认证连接），退回到原 attachments/ 目录
		attachmentsDir = filepath.Join(baseDir, "attachments")
	}

	if err := os.MkdirAll(attachmentsDir, 0700); err != nil {
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to create attachments directory: %v", err),
		}
	}

	// 顺带清理宿主重启前遗留、已超过 1 小时未被引用的暂存附件
	b.attachments.sweepOrphanFiles(attachmentsDir)

	attachmentID := newUUID()
	// 落盘文件名带上扩展名，agent 的文件读取工具需要靠扩展名识别 PDF/docx 等格式
	targetPath := filepath.Join(attachmentsDir, attachmentID+attachmentFileExt(safeName, resolvedMime))
	if err := os.WriteFile(targetPath, data, 0600); err != nil {
		return wire.AttachmentsUploadResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to write attachment file: %v", err),
		}
	}

	now := b.attachments.currentNow()
	b.attachments.mu.Lock()
	b.attachments.items[attachmentID] = &attachmentRecord{
		id:        attachmentID,
		deviceID:  deviceID,
		name:      safeName,
		mimeType:  resolvedMime,
		byteCount: len(data),
		createdAt: now,
		path:      targetPath,
	}
	b.attachments.mu.Unlock()

	return wire.AttachmentsUploadResult{
		AttachmentID: attachmentID,
		Name:         safeName,
		MimeType:     resolvedMime,
		ByteCount:    len(data),
		ExpiresAt:    now.Add(attachmentLifetime).UTC().Format(time.RFC3339Nano),
	}, nil
}

// ThreadsMediaRead 实现 threads.media.read 后端分块读取与缩略图生成逻辑。
func (b *MemoryBackend) ThreadsMediaRead(ctx context.Context, params wire.ThreadsMediaReadParams) (wire.ThreadsMediaReadResult, error) {
	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}
	// C-ST-07：要按消息 ID 查找媒体引用，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		b.mu.Unlock()
		return wire.ThreadsMediaReadResult{}, err
	}
	b.touchThreadLocked(td)

	var targetMsg *wire.ThreadMessage
	for i := range td.messages {
		if td.messages[i].ID == params.MessageID {
			targetMsg = &td.messages[i]
			break
		}
	}
	if targetMsg == nil {
		b.mu.Unlock()
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("message %s not found in thread %s", params.MessageID, params.ThreadID),
		}
	}

	var targetRef *wire.MediaRef
	for i := range targetMsg.Media {
		if targetMsg.Media[i].ID == params.MediaID {
			targetRef = &targetMsg.Media[i]
			break
		}
	}
	if targetRef == nil {
		b.mu.Unlock()
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("media %s not found in message %s", params.MediaID, params.MessageID),
		}
	}

	baseDir := b.dataDir
	b.mu.Unlock()
	if baseDir == "" && b.attachments != nil {
		baseDir = b.attachments.ensureTempDir()
	}

	// 磁盘上的媒体文件可能带扩展名（新数据）或只有裸 ID（历史数据），统一由 resolveMediaFilePath 兼容
	filePath := resolveMediaFilePath(filepath.Join(baseDir, "media", params.ThreadID), params.MediaID, targetRef.Name, targetRef.MimeType)
	fi, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: "图片文件不存在或已被清理",
			}
		}
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to stat media file: %v", err),
		}
	}

	switch params.Variant {
	case wire.MediaVariantFull:
		if fi.Size() > maxFullBytes {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.ErrCodeMediaTooLarge,
				Message: "原图超过 8 MiB，请在 Mac 上查看原图",
				Data:    map[string]any{"reason": "tooLarge"},
			}
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to read media file: %v", err),
			}
		}
		return chunkMediaBytes(data, targetRef.MimeType, params.Offset, params.Length)

	case wire.MediaVariantThumb:
		if b.thumbCache != nil {
			if cached, ok := b.thumbCache.get(params.MediaID); ok {
				return chunkMediaBytes(cached, "image/jpeg", params.Offset, params.Length)
			}
		}

		if fi.Size() > maxThumbSourceBytes {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.ErrCodeMediaTooLarge,
				Message: "图片过大，无法生成缩略图",
				Data:    map[string]any{"reason": "tooLarge"},
			}
		}

		srcBytes, err := os.ReadFile(filePath)
		if err != nil {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to read media file for thumbnail: %v", err),
			}
		}

		srcImg, _, err := image.Decode(bytes.NewReader(srcBytes))
		if err != nil {
			// 无法解码的图片按“图片不可用”返回，客户端据此展示占位提示
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "图片不可用",
				Data:    map[string]any{"reason": "unavailable"},
			}
		}

		bounds := srcImg.Bounds()
		w := bounds.Dx()
		h := bounds.Dy()
		maxEdge := w
		if h > maxEdge {
			maxEdge = h
		}

		var targetImg image.Image
		if maxEdge <= thumbMaxEdge {
			// 原图本来就小于等于 480 时不放大，仅展平白色底色
			targetImg = flattenToWhite(srcImg)
		} else {
			var dstW, dstH int
			if w >= h {
				dstW = thumbMaxEdge
				dstH = int(math.Round(float64(h) * float64(thumbMaxEdge) / float64(w)))
				if dstH < 1 {
					dstH = 1
				}
			} else {
				dstH = thumbMaxEdge
				dstW = int(math.Round(float64(w) * float64(thumbMaxEdge) / float64(h)))
				if dstW < 1 {
					dstW = 1
				}
			}
			targetImg = scaleImageBilinear(srcImg, dstW, dstH)
		}

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, targetImg, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("jpeg encode thumbnail failed: %v", err),
			}
		}

		thumbBytes := buf.Bytes()
		if b.thumbCache != nil {
			b.thumbCache.put(params.MediaID, thumbBytes)
		}

		return chunkMediaBytes(thumbBytes, "image/jpeg", params.Offset, params.Length)

	default:
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid variant %q", params.Variant),
		}
	}
}

// chunkMediaBytes 通用媒体数据分块逻辑。
func chunkMediaBytes(data []byte, mimeType string, offsetPtr *int64, lengthPtr *int) (wire.ThreadsMediaReadResult, error) {
	totalBytes := int64(len(data))
	var offset int64
	if offsetPtr != nil {
		offset = *offsetPtr
	}
	if offset < 0 || offset > totalBytes {
		return wire.ThreadsMediaReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid offset %d, total bytes %d", offset, totalBytes),
		}
	}

	limit := maxChunkBytes
	if lengthPtr != nil {
		if *lengthPtr <= 0 || *lengthPtr > 512*1024 {
			return wire.ThreadsMediaReadResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "invalid length: must be between 1 and 524288",
			}
		}
		if *lengthPtr < limit {
			limit = *lengthPtr
		}
	}

	end := offset + int64(limit)
	if end > totalBytes {
		end = totalBytes
	}

	chunk := data[offset:end]
	truncated := end < totalBytes
	var nextOffset *int64
	if truncated {
		nextVal := end
		nextOffset = &nextVal
	}

	return wire.ThreadsMediaReadResult{
		MimeType:   mimeType,
		TotalBytes: totalBytes,
		Offset:     offset,
		NextOffset: nextOffset,
		Truncated:  truncated,
		Base64:     base64.StdEncoding.EncodeToString(chunk),
	}, nil
}

// flattenToWhite 将含透明通道的图像在白色底色上展平为 RGBA，防止 JPEG 编码透明通道变黑。
func flattenToWhite(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(bounds.Min.X+x, bounds.Min.Y+y)
			r, g, b, a := c.RGBA()
			if a == 0 {
				dst.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else if a == 65535 {
				dst.SetRGBA(x, y, color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255})
			} else {
				alpha := float64(a) / 65535.0
				r8 := uint8(math.Min(255, (float64(r)/65535.0+(1.0-alpha))*255.0))
				g8 := uint8(math.Min(255, (float64(g)/65535.0+(1.0-alpha))*255.0))
				b8 := uint8(math.Min(255, (float64(b)/65535.0+(1.0-alpha))*255.0))
				dst.SetRGBA(x, y, color.RGBA{R: r8, G: g8, B: b8, A: 255})
			}
		}
	}
	return dst
}

// scaleImageBilinear 纯 Go 高质量双线性缩放并展平至白色背景。
func scaleImageBilinear(src image.Image, dstW, dstH int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	scaleX := float64(srcW) / float64(dstW)
	scaleY := float64(srcH) / float64(dstH)

	for y := 0; y < dstH; y++ {
		sy := (float64(y)+0.5)*scaleY - 0.5 + float64(srcBounds.Min.Y)
		if sy < float64(srcBounds.Min.Y) {
			sy = float64(srcBounds.Min.Y)
		}
		if sy > float64(srcBounds.Max.Y-1) {
			sy = float64(srcBounds.Max.Y - 1)
		}
		iy := int(math.Floor(sy))
		fy := sy - float64(iy)
		iy1 := iy + 1
		if iy1 >= srcBounds.Max.Y {
			iy1 = srcBounds.Max.Y - 1
		}

		w0y := 1.0 - fy
		w1y := fy

		for x := 0; x < dstW; x++ {
			sx := (float64(x)+0.5)*scaleX - 0.5 + float64(srcBounds.Min.X)
			if sx < float64(srcBounds.Min.X) {
				sx = float64(srcBounds.Min.X)
			}
			if sx > float64(srcBounds.Max.X-1) {
				sx = float64(srcBounds.Max.X - 1)
			}
			ix := int(math.Floor(sx))
			fx := sx - float64(ix)
			ix1 := ix + 1
			if ix1 >= srcBounds.Max.X {
				ix1 = srcBounds.Max.X - 1
			}

			w0x := 1.0 - fx
			w1x := fx

			w00 := w0x * w0y
			w10 := w1x * w0y
			w01 := w0x * w1y
			w11 := w1x * w1y

			r00, g00, b00, a00 := src.At(ix, iy).RGBA()
			r10, g10, b10, a10 := src.At(ix1, iy).RGBA()
			r01, g01, b01, a01 := src.At(ix, iy1).RGBA()
			r11, g11, b11, a11 := src.At(ix1, iy1).RGBA()

			rf := float64(r00)*w00 + float64(r10)*w10 + float64(r01)*w01 + float64(r11)*w11
			gf := float64(g00)*w00 + float64(g10)*w10 + float64(g01)*w01 + float64(g11)*w11
			bf := float64(b00)*w00 + float64(b10)*w10 + float64(b01)*w01 + float64(b11)*w11
			af := float64(a00)*w00 + float64(a10)*w10 + float64(a01)*w01 + float64(a11)*w11

			alpha := af / 65535.0
			var r8, g8, b8 uint8
			if alpha <= 0.00001 {
				r8, g8, b8 = 255, 255, 255
			} else {
				r8 = uint8(math.Min(255, math.Max(0, (rf/65535.0+(1.0-alpha))*255.0)))
				g8 = uint8(math.Min(255, math.Max(0, (gf/65535.0+(1.0-alpha))*255.0)))
				b8 = uint8(math.Min(255, math.Max(0, (bf/65535.0+(1.0-alpha))*255.0)))
			}
			dst.SetRGBA(x, y, color.RGBA{R: r8, G: g8, B: b8, A: 255})
		}
	}
	return dst
}

// consumeAttachmentsLocked 必须在持有 b.mu 时调用。
// 全部文件复制成功后才释放暂存引用，避免部分失败或共享附件导致排队项丢图。
func (b *MemoryBackend) consumeAttachmentsLocked(threadID string, attIDs []string, fromQueue bool, callerDeviceIDs ...string) ([]wire.MediaRef, []string, error) {
	if len(attIDs) == 0 {
		return nil, nil, nil
	}

	if b.attachments == nil {
		return nil, nil, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "attachment store unavailable"}
	}
	baseDir := b.dataDir
	if baseDir == "" {
		baseDir = b.attachments.ensureTempDir()
	}

	callerDeviceID := b.attachments.resolveCallerDeviceID(callerDeviceIDs...)

	b.attachments.mu.Lock()
	defer b.attachments.mu.Unlock()

	var records []*attachmentRecord
	counts := make(map[string]int)
	for _, attID := range attIDs {
		rec, ok := b.attachments.getLocked(attID)
		if !ok {
			return nil, nil, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("attachment %s not found or expired", attID),
			}
		}
		// 设备隔离：若能确定调用方设备且记录关联了设备，只允许引用本设备目录下的附件
		if callerDeviceID != "" && rec.deviceID != "" && rec.deviceID != callerDeviceID {
			return nil, nil, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("attachment %s not found or expired", attID),
			}
		}
		if callerDeviceID != "" && rec.deviceID != "" {
			expectedSub := filepath.Join("attachments", callerDeviceID)
			if !strings.Contains(filepath.ToSlash(rec.path), filepath.ToSlash(expectedSub)) {
				return nil, nil, &wire.RPCError{
					Code:    wire.ErrCodeResourceNotFound,
					Message: fmt.Sprintf("attachment %s not found or expired", attID),
				}
			}
		}
		// 若未能从 context 或 dispatcher 取得设备 ID（如脱机单元测试或无认证调用），退回到原行为并允许引用已索引附件

		if counts[attID] == 0 {
			records = append(records, rec)
		}
		counts[attID]++
	}

	mediaDir := filepath.Join(baseDir, "media", threadID)
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		return nil, nil, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to create media directory: %v", err),
		}
	}

	mediaRefs := make([]wire.MediaRef, 0, len(records))
	mediaFilePaths := make([]string, 0, len(records))

	for _, rec := range records {
		attID := rec.id
		dstPath := filepath.Join(mediaDir, attID+attachmentFileExt(rec.name, rec.mimeType))
		if err := copyMediaFile(rec.path, dstPath); err != nil {
			return nil, nil, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to copy attachment %s: %v", attID, err),
			}
		}

		ref := wire.MediaRef{
			ID:        attID,
			Source:    "attachment",
			Name:      rec.name,
			MimeType:  rec.mimeType,
			ByteCount: rec.byteCount,
		}

		// 读取图片宽高
		if f, err := os.Open(dstPath); err == nil {
			if cfg, _, err := image.DecodeConfig(f); err == nil && cfg.Width > 0 && cfg.Height > 0 {
				w := cfg.Width
				h := cfg.Height
				ref.Width = &w
				ref.Height = &h
			}
			_ = f.Close()
		}

		mediaRefs = append(mediaRefs, ref)
		mediaFilePaths = append(mediaFilePaths, dstPath)
	}

	for _, rec := range records {
		if fromQueue {
			rec.queueRefs -= counts[rec.id]
		}
		if rec.queueRefs <= 0 {
			_ = os.Remove(rec.path)
			delete(b.attachments.items, rec.id)
		}
	}
	return mediaRefs, mediaFilePaths, nil
}

func copyMediaFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}
