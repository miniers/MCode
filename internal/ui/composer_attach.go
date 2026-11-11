package mcodeui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	maxAttachmentBytes      = 20 * 1024 * 1024 // 单文件上限 20MB
	maxTotalAttachmentBytes = 50 * 1024 * 1024 // 附件总大小上限 50MB
)

const (
	// composerAttachThumbSize 是图片附件方形缩略图的边长。
	composerAttachThumbSize float32 = 48
	// composerAttachOverlayIconSize 是缩略图上重试角标的图标边长。
	composerAttachOverlayIconSize float32 = 10
	// composerAttachInlineIconSize 是附件胶囊里重试图标的边长。
	composerAttachInlineIconSize float32 = 11
	// composerAttachErrorBorder 是附件出错时卡片的描边宽度，比发丝线更醒目。
	composerAttachErrorBorder float32 = 2
)

// 图片上的遮罩必须与主题无关：压在照片上的永远是黑色遮罩，换成 ThemeOverlay 在深色主题下会变白。
var (
	composerAttachScrim = ui.Color{}.Alpha(0.55) // 缩略图上 × 按钮与重试按钮的底
	composerAttachDim   = ui.Color{}.Alpha(0.2)  // 上传中整卡压暗 20%
)

// attachmentThumbDecodeLimit 限制参与解码的图片字节数，避免大图拖慢输入框。
const attachmentThumbDecodeLimit = 16 * 1024 * 1024

// decodeAttachmentImage 解码受支持的图片数据；GIF 取首帧，不支持的格式返回 nil。
func decodeAttachmentImage(mime string, data []byte) image.Image {
	if !isAllowedImageMime(mime) || len(data) == 0 || len(data) > attachmentThumbDecodeLimit {
		return nil
	}
	var img image.Image
	var err error
	switch mime {
	case "image/gif":
		var g *gif.GIF
		g, err = gif.DecodeAll(bytes.NewReader(data))
		if err == nil && len(g.Image) > 0 {
			img = g.Image[0]
		}
	default:
		// PNG/JPEG 直接解码；WebP/TIFF 由注册的 x/image 解码器处理。
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil
	}
	return img
}

// attachmentThumbnail 为图片附件生成用于 48×48 胶囊预览的位图；非图片或解码失败返回 nil。
func attachmentThumbnail(mime string, data []byte) *ui.Bitmap {
	img := decodeAttachmentImage(mime, data)
	if img == nil {
		return nil
	}
	return ui.NewBitmap(img)
}

// attachmentPreviewData 返回点击放大时要保留的原始图片数据；非图片附件返回 nil，避免长期占用内存。
func attachmentPreviewData(mime string, data []byte) []byte {
	if !isAllowedImageMime(mime) {
		return nil
	}
	return data
}

type composerAttachment struct {
	// InspectorRef 让引用去重，并在切换会话时保留尚未完成的上传。
	InspectorRef *inspectorFileRef
	LocalID      string
	Name         string
	MimeType     string
	Size         int
	ID           string // 后端上传成功后回填
	Uploading    bool
	Err          string
	// Progress 是上传进度（0~1）。上传走单次 RPC，客户端拿不到中间进度，因此生产环境通常为 0：
	// 0 表示进度未知，卡片显示不定进度；大于 0 时显示百分比。
	Progress float32
	// RetryData 保存尚未上传成功的原始字节，仅用于失败后重试上传；成功后清空以释放内存。
	RetryData []byte
	// Thumb 是图片附件的缩略图位图，用于在胶囊左侧显示 48×48 预览；非图片或解码失败时为 nil。
	Thumb *ui.Bitmap
	// Data 保存图片附件的原始数据，点击缩略图时解码成全尺寸图片；非图片附件为 nil。
	Data []byte
}

func (a composerAttachment) uploadName() string {
	// 后端按名称保留扩展名，图片后缀会让 provider 把引用文本误判成图片。
	if a.InspectorRef != nil && filepath.Ext(a.Name) != ".txt" {
		return a.Name + ".txt"
	}
	return a.Name
}

var localAttachmentSeq uint64

func nextLocalAttachmentID() string {
	seq := atomic.AddUint64(&localAttachmentSeq, 1)
	return fmt.Sprintf("loc-att-%d", seq)
}

// attachmentNotice 记录附件超限或拒绝提示信息与有效期。
type attachmentNotice struct {
	message   string
	expiresAt time.Time
}

var (
	attachmentNoticeMu sync.Mutex
	attachmentNotices  = make(map[*chatState]*attachmentNotice)
)

// setAttachmentNotice 设置当前会话的附件超限提示，有效期约 4 秒。
func (s *chatState) setAttachmentNotice(msg string) {
	if s == nil || msg == "" {
		return
	}
	attachmentNoticeMu.Lock()
	attachmentNotices[s] = &attachmentNotice{
		message:   msg,
		expiresAt: time.Now().Add(4 * time.Second),
	}
	attachmentNoticeMu.Unlock()
	s.invalidate()

	time.AfterFunc(4*time.Second, func() {
		attachmentNoticeMu.Lock()
		if n, ok := attachmentNotices[s]; ok && time.Now().After(n.expiresAt) {
			delete(attachmentNotices, s)
		}
		attachmentNoticeMu.Unlock()
		s.invalidate()
	})
}

// getAttachmentNotice 获取当前有效的附件超限提示（过期则自动清理并返回空）。
func (s *chatState) getAttachmentNotice() string {
	if s == nil {
		return ""
	}
	attachmentNoticeMu.Lock()
	defer attachmentNoticeMu.Unlock()
	n, ok := attachmentNotices[s]
	if !ok {
		return ""
	}
	if time.Now().After(n.expiresAt) {
		delete(attachmentNotices, s)
		return ""
	}
	return n.message
}

// attachmentNotice 便捷导出方法供同包测试或组件访问。
func (s *chatState) attachmentNotice() string {
	return s.getAttachmentNotice()
}

type rejectedAttachment struct {
	name   string
	reason string
}

// formatRejectionNotice 合并并格式化多个被丢弃文件的提示信息。
func formatRejectionNotice(rejected []rejectedAttachment) string {
	if len(rejected) == 0 {
		return ""
	}
	if len(rejected) == 1 {
		return fmt.Sprintf("已忽略 %s：%s", rejected[0].name, rejected[0].reason)
	}

	var reasons []string
	groups := make(map[string][]string)
	for _, r := range rejected {
		if _, ok := groups[r.reason]; !ok {
			reasons = append(reasons, r.reason)
		}
		groups[r.reason] = append(groups[r.reason], r.name)
	}

	var parts []string
	for _, reason := range reasons {
		names := groups[reason]
		parts = append(parts, fmt.Sprintf("%s：%s", strings.Join(names, "、"), reason))
	}
	return "已忽略 " + strings.Join(parts, "；")
}

// isAllowedImageMime 判断 MIME 类型是否为允许上传的图片类型。
func isAllowedImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/tiff":
		return true
	default:
		return false
	}
}

// detectImageMime 通过扩展名与数据前缀检测图片 MIME 类型。
func detectImageMime(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".tiff", ".tif":
		return "image/tiff"
	}
	if len(data) > 0 {
		ct := http.DetectContentType(data)
		if isAllowedImageMime(ct) {
			return ct
		}
	}
	return ""
}

// commonCodeAndTextExts 定义受支持的代码源文件与纯文本扩展名映射。
var commonCodeAndTextExts = map[string]string{
	".go":       "text/plain",
	".swift":    "text/plain",
	".ts":       "text/plain",
	".tsx":      "text/plain",
	".js":       "text/plain",
	".jsx":      "text/plain",
	".py":       "text/plain",
	".rs":       "text/plain",
	".java":     "text/plain",
	".kt":       "text/plain",
	".c":        "text/plain",
	".h":        "text/plain",
	".cpp":      "text/plain",
	".cc":       "text/plain",
	".cxx":      "text/plain",
	".hpp":      "text/plain",
	".m":        "text/plain",
	".mm":       "text/plain",
	".rb":       "text/plain",
	".sh":       "text/plain",
	".bash":     "text/plain",
	".zsh":      "text/plain",
	".json":     "application/json",
	".yaml":     "text/yaml",
	".yml":      "text/yaml",
	".toml":     "text/plain",
	".xml":      "application/xml",
	".html":     "text/html",
	".htm":      "text/html",
	".css":      "text/css",
	".scss":     "text/css",
	".sass":     "text/css",
	".less":     "text/css",
	".sql":      "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
}

// detectAttachmentMime 检测文件名与数据对应的 MIME 类型。
// 图片、PDF、Markdown 与常见代码/文本按内容识别；其余任意文件（docx、xlsx、pptx、zip、
// 无扩展名的二进制等）也不再拒绝，按扩展名或内容嗅探给一个 MIME 兜底接受，
// 由后端落盘后把路径交给 agent 自行读取。只有内容为空时由调用方另行拦截。
func detectAttachmentMime(filename string, data []byte) (string, bool) {
	// 1. 优先检测图片格式（支持 PNG, JPEG, GIF, WEBP, TIFF）
	imgMime := detectImageMime(filename, data)
	if imgMime != "" {
		return imgMime, true
	}

	ext := strings.ToLower(filepath.Ext(filename))

	// 2. 检测 PDF 格式
	if ext == ".pdf" || bytes.HasPrefix(data, []byte("%PDF")) {
		return "application/pdf", true
	}

	// 3. 检测 Markdown 格式
	if ext == ".md" || ext == ".markdown" {
		if len(data) > 0 && !utf8.Valid(data) {
			// 扩展名是文本但内容是二进制：按二进制兜底接受，避免把二进制误当文本，也不再拒绝。
			return fallbackAttachmentMime(ext, data, false), true
		}
		return "text/markdown", true
	}

	// 4. 常见代码源文件与纯文本
	if defaultMime, exists := commonCodeAndTextExts[ext]; exists {
		if len(data) > 0 && !utf8.Valid(data) {
			return fallbackAttachmentMime(ext, data, false), true
		}
		m := mime.TypeByExtension(ext)
		if m != "" {
			m = strings.TrimSpace(strings.Split(m, ";")[0])
		}
		if m == "" {
			m = defaultMime
		}
		return m, true
	}

	// 5. 其余任意二进制文件（docx/xlsx/pptx/zip/无扩展名等）一律兜底接受
	return fallbackAttachmentMime(ext, data, true), true
}

// fallbackAttachmentMime 为无法可视化预览的附件挑选 MIME：优先按扩展名查系统表，
// 查不到时按内容嗅探，最后退回 application/octet-stream。
// guessByExt 为 false 时跳过扩展名查询——内容与文本类扩展名不符，按扩展名会得到错误的文本 MIME。
// 此时调用方已确认内容不是合法 UTF-8 文本，嗅探结果若仍是 text/*（http.DetectContentType
// 会把 0xFF 0xFE 当成 UTF-16LE BOM 等），说明嗅探不可信，直接按二进制兜底。
func fallbackAttachmentMime(ext string, data []byte, guessByExt bool) string {
	if guessByExt {
		if m := mime.TypeByExtension(ext); m != "" {
			return strings.TrimSpace(strings.Split(m, ";")[0])
		}
	}
	if m := http.DetectContentType(data); m != "" {
		m = strings.TrimSpace(strings.Split(m, ";")[0])
		// 未按扩展名认定、且内容已知非 UTF-8 文本时，不接受嗅探出的文本类型。
		if !guessByExt && strings.HasPrefix(m, "text/") {
			return "application/octet-stream"
		}
		return m
	}
	return "application/octet-stream"
}

// uploadAttachmentTask 异步向后端上传附件并更新附件状态。
func (s *chatState) uploadAttachmentTask(locID, attName, attMime string, attData []byte) {
	ctx := context.WithValue(context.Background(), "local_upload", true)
	res, err := s.client.UploadAttachment(ctx, attName, attMime, attData)
	s.mu.Lock()
	defer s.mu.Unlock()
	update := func(atts []composerAttachment) {
		for i := range atts {
			if atts[i].LocalID != locID {
				continue
			}
			atts[i].Uploading = false
			if err != nil {
				atts[i].Err = err.Error()
			} else {
				atts[i].Err = ""
				atts[i].ID = res.ID
				atts[i].RetryData = nil
			}
			break
		}
	}
	update(s.attachments)
	update(s.editStashAttachments)
	for _, draft := range s.drafts {
		update(draft.Attachments)
	}
	if err != nil {
		s.toastMessage = "附件上传失败: " + err.Error()
	}
	s.invalidate()
}

// addAttachment 校验并向输入框添加一个附件，通过后异步触发上传。
func (s *chatState) addAttachment(name, mime string, data []byte) bool {
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	return s.addAttachmentToThread(threadID, name, mime, data, nil)
}

// addAttachmentToThread 复用附件限额与上传；引用明确指定文本类型，不能按被引用路径的后缀解码。
func (s *chatState) addAttachmentToThread(threadID, name, mime string, data []byte, ref *inspectorFileRef) bool {
	if len(data) == 0 {
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：内容为空", name))
		return false
	}

	detectedMime, ok := mime, true
	if ref == nil {
		detectedMime, ok = detectAttachmentMime(name, data)
	}
	if !ok || detectedMime == "" {
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：不支持的文件类型", name))
		return false
	}
	mime = detectedMime

	// TIFF 转为 PNG 格式
	if ref == nil && (mime == "image/tiff" || strings.ToLower(filepath.Ext(name)) == ".tiff" || strings.ToLower(filepath.Ext(name)) == ".tif") {
		img, err := tiff.Decode(bytes.NewReader(data))
		if err == nil {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err == nil {
				data = buf.Bytes()
				mime = "image/png"
				ext := filepath.Ext(name)
				name = strings.TrimSuffix(name, ext) + ".png"
			}
		}
	}

	if len(data) > maxAttachmentBytes {
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：超过单个 20MB 上限", name))
		return false
	}

	limit := s.attachmentLimit()
	s.mu.Lock()
	atts := s.attachments
	if threadID != s.selectedThreadID {
		atts = s.drafts[threadID].Attachments
	}
	if ref != nil {
		for _, att := range atts {
			if att.InspectorRef != nil && *att.InspectorRef == *ref {
				s.mu.Unlock()
				return false
			}
		}
	}
	if len(atts) >= limit {
		s.mu.Unlock()
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：超过最多 %d 个附件上限", name, limit))
		return false
	}

	var totalSize int
	for _, a := range atts {
		totalSize += a.Size
	}
	if totalSize+len(data) > maxTotalAttachmentBytes {
		s.mu.Unlock()
		s.setAttachmentNotice(fmt.Sprintf("已忽略 %s：超过总计 50MB 上限", name))
		return false
	}

	localID := nextLocalAttachmentID()
	att := composerAttachment{
		InspectorRef: ref,
		LocalID:      localID,
		Name:         name,
		MimeType:     mime,
		Size:         len(data),
		Uploading:    true,
		Thumb:        attachmentThumbnail(mime, data),
		Data:         attachmentPreviewData(mime, data),
		RetryData:    data,
	}
	atts = append(atts, att)
	if threadID == s.selectedThreadID {
		s.attachments = atts
	} else {
		if s.drafts == nil {
			s.drafts = make(map[string]composerDraft)
		}
		draft := s.drafts[threadID]
		draft.Attachments = atts
		s.drafts[threadID] = draft
	}
	s.mu.Unlock()
	attachmentTokenCache.Store(localID, estimateAttachmentTokens(mime, data, len(data)))
	s.invalidate()

	go s.uploadAttachmentTask(localID, att.uploadName(), mime, data)
	return true
}

// removeAttachment 根据 localID 移除附件。
func (s *chatState) removeAttachment(localID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.attachments {
		if a.LocalID == localID {
			s.attachments = append(s.attachments[:i], s.attachments[i+1:]...)
			attachmentTokenCache.Delete(localID)
			break
		}
	}
	s.invalidate()
}

// retryAttachment 清除失败状态并复用 uploadAttachmentTask 重新上传同一附件。
// 原始字节优先取 RetryData（失败时保留），图片附件退回 Data；都没有数据时返回 false。
func (s *chatState) retryAttachment(localID string) bool {
	s.mu.Lock()
	var name, mime string
	var data []byte
	found := false
	for i := range s.attachments {
		if s.attachments[i].LocalID != localID {
			continue
		}
		a := &s.attachments[i]
		if a.Err == "" || a.Uploading {
			break
		}
		data = a.RetryData
		if data == nil {
			data = a.Data
		}
		if len(data) == 0 {
			break
		}
		a.Err = ""
		a.Uploading = true
		a.Progress = 0
		name, mime = a.uploadName(), a.MimeType
		found = true
		break
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	s.invalidate()
	go s.uploadAttachmentTask(localID, name, mime, data)
	return true
}

// attachmentScanLimit 限制一次拖拽/粘贴里目录递归枚举的文件数。拖入大目录时先遍历整棵树再逐个拒绝
// 会长时间占住 UI 线程，因此枚举到这个数量就停，并在提示里说明结果被截断。
const attachmentScanLimit = 500

// attachmentScan 累积一次目录递归枚举的结果。
type attachmentScan struct {
	files      []string
	unreadable []string
	truncated  bool
}

// collectFilesFromPath 递归遍历指定路径，跳过隐藏文件和目录，返回普通文件列表与不可读项；
// 枚举满 attachmentScanLimit 个文件即停止，并通过 truncated 告知调用方结果被截断。
func collectFilesFromPath(p string) (files []string, unreadable []string, truncated bool) {
	var scan attachmentScan
	scan.walk(p)
	return scan.files, scan.unreadable, scan.truncated
}

// walk 递归收集单个路径，隐藏文件与隐藏目录一律跳过。
func (sc *attachmentScan) walk(p string) {
	info, err := os.Lstat(p)
	if err != nil {
		sc.unreadable = append(sc.unreadable, filepath.Base(p))
		return
	}
	// 跳过隐藏文件或隐藏目录
	if strings.HasPrefix(filepath.Base(p), ".") {
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			sc.unreadable = append(sc.unreadable, filepath.Base(p))
			return
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			// 还有待访问的条目却已到上限，说明结果被截断
			if len(sc.files) >= attachmentScanLimit {
				sc.truncated = true
				return
			}
			sc.walk(filepath.Join(p, entry.Name()))
		}
		return
	}
	if !info.Mode().IsRegular() {
		sc.unreadable = append(sc.unreadable, filepath.Base(p))
		return
	}
	sc.files = append(sc.files, p)
}

// addAttachmentFiles 读取外部文件并批量添加为附件（支持目录递归展开并跳过隐藏文件）。
// 本函数只在 UI 线程做受上限约束的目录枚举；读盘与解码（含 TIFF 转码、缩略图生成）
// 交给 readAttachmentFiles 在后台协程完成，避免拖拽/粘贴时同步读文件卡住界面。
func (s *chatState) addAttachmentFiles(paths []string) {
	limit := s.attachmentLimit()
	var allFiles []string
	var rejected []rejectedAttachment

	for _, p := range paths {
		files, unreadable, truncated := collectFilesFromPath(p)
		for _, u := range unreadable {
			rejected = append(rejected, rejectedAttachment{name: u, reason: "无法读取文件"})
		}
		if truncated {
			rejected = append(rejected, rejectedAttachment{
				name:   "目录 " + filepath.Base(p),
				reason: fmt.Sprintf("文件过多，只处理前 %d 个", attachmentScanLimit),
			})
		}
		allFiles = append(allFiles, files...)
	}

	s.mu.Lock()
	currentCount := len(s.attachments)
	s.mu.Unlock()

	room := limit - currentCount
	if room < 0 {
		room = 0
	}

	// 超出可用数量上限的部分直接标记丢弃；只报剩余数量，避免拖入大目录时拼出上千条文件名。
	if len(allFiles) > room {
		rejected = append(rejected, rejectedAttachment{
			name:   fmt.Sprintf("另有 %d 个文件", len(allFiles)-room),
			reason: fmt.Sprintf("超过最多 %d 个附件上限", limit),
		})
		allFiles = allFiles[:room]
	}

	if len(allFiles) == 0 && len(rejected) == 0 {
		return
	}
	go s.readAttachmentFiles(allFiles, rejected)
}

// readAttachmentFiles 在后台读取并解码候选文件，逐个加入附件；结束后提示被拒绝的文件。
func (s *chatState) readAttachmentFiles(paths []string, rejected []rejectedAttachment) {
	limit := s.attachmentLimit()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			rejected = append(rejected, rejectedAttachment{name: filepath.Base(p), reason: "无法读取文件"})
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			rejected = append(rejected, rejectedAttachment{name: filepath.Base(p), reason: "无法读取文件"})
			continue
		}
		// 避免阻塞 UI 或耗尽内存，仅读取单文件上限加 1 字节
		data, err := io.ReadAll(io.LimitReader(f, int64(maxAttachmentBytes+1)))
		f.Close()
		if err != nil {
			rejected = append(rejected, rejectedAttachment{name: filepath.Base(p), reason: "无法读取文件"})
			continue
		}

		name := filepath.Base(p)
		if len(data) > maxAttachmentBytes {
			rejected = append(rejected, rejectedAttachment{name: name, reason: "超过单个 20MB 上限"})
			continue
		}

		mime, ok := detectAttachmentMime(name, data)
		if !ok || mime == "" {
			rejected = append(rejected, rejectedAttachment{name: name, reason: "不支持的文件类型"})
			continue
		}

		// TIFF 转换为 PNG
		if mime == "image/tiff" || strings.ToLower(filepath.Ext(name)) == ".tiff" || strings.ToLower(filepath.Ext(name)) == ".tif" {
			img, err := tiff.Decode(bytes.NewReader(data))
			if err == nil {
				var buf bytes.Buffer
				if err := png.Encode(&buf, img); err == nil {
					data = buf.Bytes()
					mime = "image/png"
					ext := filepath.Ext(name)
					name = strings.TrimSuffix(name, ext) + ".png"
				}
			}
		}

		s.mu.Lock()
		if len(s.attachments) >= limit {
			s.mu.Unlock()
			rejected = append(rejected, rejectedAttachment{name: name, reason: fmt.Sprintf("超过最多 %d 个附件上限", limit)})
			continue
		}

		var totalSize int
		for _, a := range s.attachments {
			totalSize += a.Size
		}
		if totalSize+len(data) > maxTotalAttachmentBytes {
			s.mu.Unlock()
			rejected = append(rejected, rejectedAttachment{name: name, reason: "超过总计 50MB 上限"})
			continue
		}

		localID := nextLocalAttachmentID()
		att := composerAttachment{
			LocalID:   localID,
			Name:      name,
			MimeType:  mime,
			Size:      len(data),
			Uploading: true,
			Thumb:     attachmentThumbnail(mime, data),
			Data:      attachmentPreviewData(mime, data),
			RetryData: data,
		}
		s.attachments = append(s.attachments, att)
		s.mu.Unlock()
		attachmentTokenCache.Store(localID, estimateAttachmentTokens(mime, data, len(data)))
		s.invalidate()

		go s.uploadAttachmentTask(localID, name, mime, data)
	}

	if len(rejected) > 0 {
		s.setAttachmentNotice(formatRejectionNotice(rejected))
	}
}

// clipboardReaderFunc 定义剪贴板读取函数签名。
type clipboardReaderFunc func() (hasImage bool, name string, mime string, data []byte, filePaths []string)

// defaultClipboardReader 是生产环境下默认的剪贴板读取实现。
func defaultClipboardReader() (bool, string, string, []byte, []string) {
	// 优先检查剪贴板中的文件列表。
	// ReadFiles 读取的是合并后的摘要数据，遇到系统剪贴板切换或格式协商失败时会整体返回错误；
	// 此时仍应继续检查图片格式，不能把一张图片的粘贴当成纯文本。
	if files, err := mygo.Clipboard.ReadFiles(); err == nil && len(files) > 0 {
		return true, "", "", nil, files
	}

	formats := mygo.Clipboard.Formats()

	for _, candidate := range []struct{ mime, ext string }{
		{"image/png", ".png"}, {"image/jpeg", ".jpg"},
		{"image/gif", ".gif"}, {"image/webp", ".webp"},
	} {
		for _, f := range formats {
			if string(f) == candidate.mime {
				data, err := mygo.Clipboard.ReadFormat(f)
				if err == nil && len(data) > 0 {
					return true, "pasted-image" + candidate.ext, candidate.mime, data, nil
				}
			}
		}
	}
	for _, f := range formats {
		if strings.HasPrefix(string(f), "image/") {
			if data := mygo.Clipboard.ReadImage(); len(data) > 0 {
				return true, "pasted-image.png", "image/png", data, nil
			}
			break
		}
	}

	// 纯文本不拦截，交给 TextArea 正常粘贴
	return false, "", "", nil, nil
}

// clipboardReader 是可被测试替换的包级剪贴板读取函数。
var clipboardReader clipboardReaderFunc = defaultClipboardReader
var clipboardFilesReader = mygo.Clipboard.ReadFiles

// tryPasteImage 尝试从剪贴板读取文件路径或图片数据并添加为附件。
func (s *chatState) tryPasteImage() bool {
	hasAttach, name, mime, data, filePaths := clipboardReader()
	if !hasAttach {
		return false
	}
	if len(filePaths) > 0 {
		s.addAttachmentFiles(filePaths)
		return true
	}
	if len(data) > 0 {
		s.addAttachment(name, mime, data)
		return true
	}
	return false
}

// isPastedFilePath 判断粘贴回调拿到的文本是否就是剪贴板里文件本身的路径。
// 从访达复制文件时系统会同时提供文件 URL 与内含文本，其中的文本不应插进草稿。
func isPastedFilePath(text string, filePaths []string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(filePaths) == 0 {
		return false
	}
	for _, p := range filePaths {
		if p == "" {
			continue
		}
		if trimmed == p || trimmed == filepath.Base(p) {
			return true
		}
	}
	return false
}

// handlePaste 处理输入框的粘贴：剪贴板含图片或文件时作为附件加入并吞掉这次粘贴；
// 纯文本里算长文本的不直接插入（会把输入框撑爆），收成附件行里的卡片并吞掉粘贴；
// 短文本仍交给输入框正常插入并返回 false。
func (s *chatState) handlePaste(text string) bool {
	if strings.TrimSpace(text) != "" {
		if filePaths, err := clipboardFilesReader(); err == nil && isPastedFilePath(text, filePaths) {
			s.addAttachmentFiles(filePaths)
			return true
		}
		return s.pasteLongText(text)
	}
	return s.tryPasteImage()
}

// truncateAttachmentName 截断过长的文件名用于在胶囊中紧凑展示。
func truncateAttachmentName(name string, maxLen int) string {
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	ext := filepath.Ext(name)
	extRunes := []rune(ext)
	if len(extRunes) >= maxLen-3 {
		return string(runes[:maxLen-3]) + "..."
	}
	baseLen := maxLen - 3 - len(extRunes)
	return string(runes[:baseLen]) + "..." + ext
}

// formatAttachmentSize 格式化附件文件字节大小展示。
func formatAttachmentSize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	if size < 1024*1024 {
		kb := float64(size) / 1024.0
		return fmt.Sprintf("%.1fKB", kb)
	}
	mb := float64(size) / (1024.0 * 1024.0)
	return fmt.Sprintf("%.1fMB", mb)
}

// attachmentTokenCache 按附件 localID 缓存 token 估算值，避免每帧渲染重复解码图片。
var attachmentTokenCache sync.Map // map[string]int

// estimateAttachmentTokens 粗略估算单个附件占用的 token 数。
// 图片按 Claude 公式 宽×高/750 计算并封顶 1600，长边超过 1568 时先等比缩放再计算；
// 文本/代码按字符数估算（CJK 每字约 1 token，其余每 4 字约 1 token，向上取整）；
// PDF 按每 3KB 约 1000 token 的粗略经验值估算（真实值随排版与内嵌图片波动很大），至少 1000；
// docx/xlsx/pptx/zip 等二进制文件不会解码进上下文（由 agent 按落盘路径自行读取），估算为 0，
// 避免按字节数被误报为超过上下文。
func estimateAttachmentTokens(mime string, data []byte, size int) int {
	if size <= 0 {
		size = len(data)
	}
	switch {
	case isAllowedImageMime(mime):
		img := decodeAttachmentImage(mime, data)
		if img == nil {
			// 解码失败时按图片上限计，宁可高估也不漏报。
			return 1600
		}
		bounds := img.Bounds()
		w, h := bounds.Dx(), bounds.Dy()
		if w <= 0 || h <= 0 {
			return 1600
		}
		long := w
		if h > long {
			long = h
		}
		if long > 1568 {
			scale := float64(1568) / float64(long)
			w = int(float64(w)*scale + 0.5)
			h = int(float64(h)*scale + 0.5)
			if w < 1 {
				w = 1
			}
			if h < 1 {
				h = 1
			}
		}
		tokens := (w*h + 749) / 750
		if tokens > 1600 {
			tokens = 1600
		}
		if tokens < 1 {
			tokens = 1
		}
		return tokens
	case mime == "application/pdf":
		tokens := size / 3000 * 1000
		if tokens < 1000 {
			tokens = 1000
		}
		return tokens
	case isTextLikeMime(mime):
		return estimateTextTokens(data, size)
	default:
		// 其他二进制文件不进入上下文，不计 token。
		return 0
	}
}

// isTextLikeMime 判断 MIME 是否属于文本或代码类附件（与 detectAttachmentMime 的分类保持一致）。
func isTextLikeMime(mime string) bool {
	return strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml"
}

// estimateTextTokens 按字符数估算文本 token：CJK 每字约 1 token，其余每 4 字约 1 token，向上取整。
func estimateTextTokens(data []byte, size int) int {
	if len(data) == 0 {
		// 没有内容时退回按字节数的保守估算。
		return size / 4
	}
	var cjk, other int
	for _, r := range string(data) {
		if isCJKRune(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

// isCJKRune 判断字符是否属于常见 CJK 表意文字或假名、谚文范围。
func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// attachmentTokensFor 返回单个附件的 token 估算值；优先取缓存，未命中时按 MIME 与大小回退估算。
func attachmentTokensFor(att composerAttachment) int {
	if v, ok := attachmentTokenCache.Load(att.LocalID); ok {
		if n, ok := v.(int); ok {
			return n
		}
	}
	return estimateAttachmentTokens(att.MimeType, nil, att.Size)
}

// attachmentTokenNotice 在附件估算 token 超过剩余上下文 80% 时返回提示文案，否则返回空字符串。
// limitTokens <= 0（上下文上限未知）时不提示；剩余上下文最小按 0 计。
func attachmentTokenNotice(attTokens, usedTokens, limitTokens int64) string {
	if limitTokens <= 0 {
		return ""
	}
	remaining := limitTokens - usedTokens
	if remaining < 0 {
		remaining = 0
	}
	if float64(attTokens) <= float64(remaining)*0.8 {
		return ""
	}
	return fmt.Sprintf("附件约 %s tokens，超过剩余上下文（%s）的 80%%，可能被截断或导致上下文溢出",
		formatTokens(attTokens), formatTokens(remaining))
}

type attCategory int

const (
	categoryImage attCategory = iota
	categoryPDF
	categoryMDText
	categoryCode
	// categoryFile 用于 docx/xlsx/pptx/zip 等不可预览的二进制文件：只显示文件名、tag 与大小。
	categoryFile
)

func classifyAttachment(name, mime string) (attCategory, string) {
	ext := strings.ToLower(filepath.Ext(name))
	if isAllowedImageMime(mime) || ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".tiff" || ext == ".tif" {
		return categoryImage, "IMG"
	}
	if mime == "application/pdf" || ext == ".pdf" {
		return categoryPDF, "PDF"
	}
	if ext == ".md" || ext == ".markdown" || ext == ".txt" || mime == "text/markdown" {
		tag := "TXT"
		if ext == ".md" || ext == ".markdown" || mime == "text/markdown" {
			tag = "MD"
		}
		return categoryMDText, tag
	}
	tag := strings.ToUpper(strings.TrimPrefix(ext, "."))
	if tag == "" {
		tag = "FILE"
	}
	if _, isCode := commonCodeAndTextExts[ext]; isCode {
		return categoryCode, tag
	}
	// 非图片、非 PDF、非文本/代码的二进制文件按普通文件渲染，不做内容预览。
	return categoryFile, tag
}

// renderAttachmentRemoveButton 渲染附件卡片上的 × 删除按钮；overlay 为 true 时叠放在图片缩略图右上角，
// 平时透明、卡片悬停或按钮获得键盘焦点时淡入（透明时仍可点击与聚焦）。
func renderAttachmentRemoveButton(c *ui.Context, s *chatState, a composerAttachment, sending, overlay, reveal bool) {
	btn := ui.ButtonBase(c.Key("composer-attach-remove-"+a.LocalID)).
		Label("删除附件 "+a.Name).
		Disabled(sending).
		Size(IconMD, IconMD).
		Radius(RadiusMD).
		OnClick(func() {
			s.removeAttachment(a.LocalID)
		})
	if overlay {
		// 叠在缩略图上时用半透明深色底，保证 × 在任何图片上都可见。
		// 淡入进度挂在按钮自身上：透明不影响命中检测与焦点顺序，键盘与 a11y 照常可用。
		target := float32(0)
		if reveal || btn.Focused() || sending {
			target = 1
		}
		op := btn.AnimateTo("remove-fade", target, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
		btn.Absolute().
			Top(2).
			Right(2).
			Opacity(op).
			Background(composerAttachScrim).
			Children(func() {
				ui.Text(c, "×").FontSize(TypeFootnote).TextColor(ThemeOnAccent(c))
			})
		return
	}
	btn.Background(ui.Transparent).
		Children(func() {
			ui.Text(c, "×").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderAttachmentRetryButton 在失败附件上渲染 16pt 重试按钮：
// overlay 为 true 时叠在图片缩略图左下角（深色底），为 false 时内联在胶囊右侧。
// 点击清掉 Err 并复用首次上传路径重新上传，悬停提示给出失败原因。
func renderAttachmentRetryButton(c *ui.Context, s *chatState, a composerAttachment, overlay bool) {
	btn := ui.ButtonBase(c.Key("composer-attach-retry-"+a.LocalID)).
		Label("重试上传 "+a.Name).
		Tooltip(a.Err).
		Size(IconMD, IconMD).
		Radius(RadiusMD).
		OnClick(func() {
			s.retryAttachment(a.LocalID)
		})
	if overlay {
		btn.Absolute().
			Left(2).
			Bottom(2).
			Background(composerAttachScrim).
			Children(func() {
				ui.Icon(c, IconRefreshCw()).Size(composerAttachOverlayIconSize, composerAttachOverlayIconSize).TextColor(ThemeOnAccent(c))
			})
		return
	}
	btn.Background(ui.Transparent).
		Children(func() {
			ui.Icon(c, IconRefreshCw()).Size(composerAttachInlineIconSize, composerAttachInlineIconSize).TextColor(ThemeDanger(c))
		})
}

// renderAttachmentUploadBar 渲染卡片底部的 16pt 上传进度条：
// 拿得到进度（Progress > 0）时画 accent 进度段与右侧百分比；
// 拿不到时画小号转圈与淡淡的轨道，不显示百分比。
func renderAttachmentUploadBar(c *ui.Context, a composerAttachment) {
	progress := a.Progress
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}

	bar := ui.Row(c.Key("composer-attach-progress-"+a.LocalID)).
		Absolute().
		Left(0).
		Right(0).
		Bottom(0).
		Height(IconMD).
		Padding(0, Space1, 0, Space1).
		Gap(Space0).
		Background(ThemeSurface(c).Alpha(0.8)).
		AlignItems(ui.Center)
	bar.Children(func() {
		if progress <= 0 {
			// 上传是一次 RPC，拿不到中间进度：小号转圈放在进度条里，只表示“上传中”。
			LoadingIndicator(c, nil, "", composerAttachOverlayIconSize)
			ui.Box(c.Key("composer-attach-progress-rail-" + a.LocalID)).
				Grow(1).
				Height(Space0).
				Radius(RadiusPill).
				Background(ThemeAccent(c).Alpha(0.35))
			return
		}

		ProgressBar(c, ProgressBarProps{
			Key: "composer-attach-progress-track-" + a.LocalID, Label: "上传进度", Ratio: progress, Height: Space0,
			Track: ThemeAccent(c).Alpha(0.22), Fill: ThemeAccent(c),
		}).Grow(1)
		ui.Text(c, fmt.Sprintf("%d%%", int(progress*100+0.5))).
			FontSize(TypeCaption).
			FontFeatures("tnum").
			TextColor(ThemePrimaryText(c))
	})
}

// renderImageAttachmentCard 渲染图片附件卡片：只显示铺满卡片的正方形缩略图，
// 文件名（含失败原因）放进 hover 提示，上传中用 20% 压暗与底部进度条、失败用红框与重试入口表示状态。
func renderImageAttachmentCard(c *ui.Context, s *chatState, a composerAttachment, sending bool) {
	tip := a.Name
	if a.Err != "" {
		tip = a.Name + "\n" + a.Err
	}

	card := ui.Row(c.Key("composer-attach-" + a.LocalID)).
		Transition(motionListItem(c)).
		Width(composerAttachThumbSize).
		Height(composerAttachThumbSize).
		Radius(RadiusMD).
		Clip().
		Tooltip(tip).
		AlignItems(ui.Center).
		Justify(ui.Center)
	if a.Err != "" {
		// 上传失败：用危险色描边，避免去掉文件名后失败状态不可见。
		card.Border(composerAttachErrorBorder, ThemeDanger(c))
	}
	card.Children(func() {
		// 缩略图铺满卡片；解码失败时退回回形针图标。
		if a.Thumb != nil {
			// 点击缩略图放大：与消息内图片共用同一个浮层。× 删除按钮是后绘制的兄弟节点，
			// 命中检测自顶向下，点 × 只删除附件不会触发这里的放大。
			previewBtn := ui.ButtonBase(c.Key("composer-attach-preview-" + a.LocalID)).
				Label("放大预览 " + a.Name).
				Padding(0).
				Width(composerAttachThumbSize).
				Height(composerAttachThumbSize).
				Radius(RadiusMD).
				Background(ui.Transparent).
				Cursor(ui.CursorPointer).
				Tooltip("点击放大\n" + tip)
			previewBtn.Children(func() {
				ui.Image(c, a.Thumb).
					Key("composer-attach-thumb-" + a.LocalID).
					Width(composerAttachThumbSize).
					Height(composerAttachThumbSize)
			})
			previewBtn.OnClick(func() {
				openComposerAttachmentPreview(s, a)
			})
		} else {
			ui.Icon(c, IconPaperclip()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		}

		// 上传中：整卡只压暗 20%，缩略图仍可辨认；底部 16pt 进度条显示进度或不定进度。
		if a.Uploading {
			ui.Box(c.Key("composer-attach-uploading-" + a.LocalID)).
				Absolute().
				Left(0).
				Top(0).
				Right(0).
				Bottom(0).
				Background(composerAttachDim)
			renderAttachmentUploadBar(c, a)
		}

		// 上传失败：左下角给重试入口，悬停提示错误原因。
		if a.Err != "" {
			renderAttachmentRetryButton(c, s, a, true)
		}

		// 删除按钮随卡片悬停或获得焦点淡入，平时透明但仍可点击与聚焦。
		reveal := card.Hovered() || card.FocusWithin()
		renderAttachmentRemoveButton(c, s, a, sending, true, reveal)
	})
}

// openComposerAttachmentPreview 打开附件缩略图的放大浮层：
// 用附件原始数据解码一张全尺寸图源，解码失败时退回缩略图。
// 图源在点击时只生成一次并存进浮层状态，避免每帧重复解码。
func openComposerAttachmentPreview(s *chatState, a composerAttachment) {
	var src ui.ImageSource
	if img := decodeAttachmentImage(a.MimeType, a.Data); img != nil {
		src = ui.NewBitmap(img)
	} else if a.Thumb != nil {
		src = a.Thumb
	}
	if src == nil || s == nil {
		return
	}
	s.mu.Lock()
	s.imagePreview = &imagePreviewState{src: src, alt: a.Name}
	s.mu.Unlock()
	s.invalidate()
}

func categoryColor(c *ui.Context, cat attCategory) ui.Color {
	switch cat {
	case categoryPDF:
		return ThemeDanger(c)
	case categoryMDText:
		return ThemeSecondaryText(c)
	case categoryCode:
		return ThemeAccent(c)
	case categoryFile:
		return ThemeSecondaryText(c)
	default:
		return ThemeSecondaryText(c)
	}
}

// renderAttachments 渲染位于输入框上方的提示横幅与附件胶囊行。
func renderAttachments(c *ui.Context, s *chatState) {
	notice := s.attachmentNotice()

	s.mu.Lock()
	hasAtts := len(s.attachments) > 0
	var attachments []composerAttachment
	if hasAtts {
		attachments = append([]composerAttachment(nil), s.attachments...)
	}
	snippets := append([]composerSnippet(nil), s.snippets...)
	previewID := s.snippetPreviewID
	sending := s.sending
	// 与 composer.go 一致的取法：当前选中的会话线程（仅用于读取上下文用量）。
	var currentThread *Thread
	for _, th := range s.threads {
		if th.ID == s.selectedThreadID {
			t := th
			currentThread = &t
			break
		}
	}
	s.mu.Unlock()

	// 附件 token 估算超限提示：随附件实时计算，不写入 setAttachmentNotice 的临时通知。
	var tokenNotice string
	if hasAtts && currentThread != nil {
		var attTokens int64
		for _, att := range attachments {
			attTokens += int64(attachmentTokensFor(att))
		}
		if used, limit, ok := currentThreadContextTokens(s, currentThread); ok {
			tokenNotice = attachmentTokenNotice(attTokens, used, limit)
		}
	}

	if notice == "" && tokenNotice == "" && !hasAtts && len(snippets) == 0 {
		return
	}

	ui.Column(c.Key("composer-attachments-container")).
		Padding(Space0, 0, Space1, 0).
		Gap(Space1).
		Children(func() {
			// 附件 token 估算超限提示（实时计算，非临时通知，与拒绝提示同样式）
			if tokenNotice != "" {
				ui.Row(c.Key("composer-attachment-token-notice")).
					Transition(motionListItem(c)).
					Gap(Space2).
					Padding(Space0, Space3, Space0, Space3).
					Radius(RadiusLG).
					Background(ThemeHover(c)).
					Border(Hairline, ThemeWarning(c)).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeWarning(c))
						ui.Text(c, tokenNotice).FontSize(TypeFootnote).TextColor(ThemeWarning(c))
					})
			}

			// 超限或类型错误提示横幅（居于胶囊行上方，4秒过期后自动消失）
			if notice != "" {
				ui.Row(c.Key("composer-attachment-notice")).
					Transition(motionListItem(c)).
					Gap(Space2).
					Padding(Space0, Space3, Space0, Space3).
					Radius(RadiusLG).
					Background(ThemeHover(c)).
					Border(Hairline, ThemeWarning(c)).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeWarning(c))
						ui.Text(c, notice).FontSize(TypeFootnote).TextColor(ThemeWarning(c))
					})
			}

			// 附件胶囊行：长文本粘贴卡片排在附件前面
			if hasAtts || len(snippets) > 0 {
				ui.Row(c.Key("composer-attachments-row")).
					Gap(Space2).
					Wrap().
					AlignItems(ui.Center).
					Children(func() {
						for _, sn := range snippets {
							renderSnippetChip(c, s, sn, previewID == sn.ID, sending)
						}
						for _, att := range attachments {
							a := att
							cat, tag := classifyAttachment(a.Name, a.MimeType)
							if a.InspectorRef != nil {
								cat, tag = categoryMDText, ""
							}
							catCol := categoryColor(c, cat)

							// 图片附件只显示缩略图正方形卡片；非图片附件保持原有胶囊样式。
							if cat == categoryImage {
								renderImageAttachmentCard(c, s, a, sending)
								continue
							}

							card := ui.Row(c.Key("composer-attach-"+a.LocalID)).
								Transition(motionListItem(c)).
								Gap(Space2).
								Padding(Space0, Space3, Space0, Space3).
								Radius(RadiusLG).
								Background(ThemeHover(c)).
								Border(Hairline, ThemeBorder(c)).
								AlignItems(ui.Center)
							if a.InspectorRef != nil && !reduceMotion(c) {
								scale := float32(1)
								if a.Uploading {
									scale = 0.97
								}
								scale = card.AnimateTo("reference-ready", scale, springSnappy)
								card.Scale(scale, scale)
							}
							card.Children(func() {
								if a.Uploading {
									LoadingIndicator(c, nil, "", IconSM, ThemeSecondaryText(c))
								}

								ui.Icon(c, IconFile()).Size(IconSM, IconSM).TextColor(catCol)
								if tag != "" {
									ui.Text(c, tag).FontSize(TypeCaption).TextColor(catCol)
								}

								displayName := truncateAttachmentName(a.Name, 18)
								txt := ui.Text(c, displayName).FontSize(TypeFootnote)
								if a.Err != "" {
									txt.TextColor(ThemeDanger(c))
								} else {
									txt.TextColor(ThemePrimaryText(c))
								}

								// 显示文件大小
								if a.Size > 0 && a.InspectorRef == nil {
									ui.Text(c, formatAttachmentSize(a.Size)).
										FontSize(TypeCaption).
										TextColor(ThemeSecondaryText(c))
								}

								// 失败时右侧给重试入口，再右侧是 × 删除按钮
								if a.Err != "" {
									renderAttachmentRetryButton(c, s, a, false)
								}

								// 右侧 × 删除按钮
								renderAttachmentRemoveButton(c, s, a, sending, false, true)
							})
						}
					})
			}
		})
}
