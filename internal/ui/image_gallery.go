package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 大图浮层的画廊：按消息顺序收集当前会话里的图片，供左右翻页使用。
// 条目分两类：用户消息里的图片附件（靠消息/媒体 ID 定位）与助手正文 markdown 里的本地图片
// （靠原始地址定位）；输入框附件既不在消息里也没有地址，定位不到时不显示翻页。

// previewGalleryKind 区分画廊条目的来源。
type previewGalleryKind int

const (
	// previewGalleryAttachment 表示用户消息里的图片附件。
	previewGalleryAttachment previewGalleryKind = iota
	// previewGalleryMarkdown 表示助手正文 markdown 里的本地图片。
	previewGalleryMarkdown
)

// previewGalleryItem 是画廊里的一张图片。附件条目只有消息/媒体标识，
// 正文条目记录原始地址、替代文字与解析出的本地路径。
type previewGalleryItem struct {
	Kind      previewGalleryKind
	ThreadID  string
	MessageID string
	MediaID   string
	Name      string
	MimeType  string
	URL       string
	Alt       string
	LocalPath string
}

// previewGalleryKey 返回条目在画廊里的唯一标识：附件按消息与媒体，正文图片按地址。
func previewGalleryKey(item previewGalleryItem) string {
	if item.Kind == previewGalleryAttachment {
		return "media\x00" + item.MessageID + "\x00" + item.MediaID
	}
	return "url\x00" + item.URL
}

// previewGalleryItemMatches 判断画廊条目是否就是浮层当前打开的那张图片。
func previewGalleryItemMatches(item previewGalleryItem, pv *imagePreviewState) bool {
	if pv == nil {
		return false
	}
	if pv.full != nil {
		return item.Kind == previewGalleryAttachment &&
			item.MessageID == pv.full.messageID &&
			item.MediaID == pv.full.mediaID
	}
	if item.Kind != previewGalleryMarkdown || item.URL == "" {
		return false
	}
	return strings.TrimSpace(item.URL) == strings.TrimSpace(pv.url)
}

// previewGalleryIndex 返回当前图片在画廊里的下标，定位不到时返回 -1。
func previewGalleryIndex(items []previewGalleryItem, pv *imagePreviewState) int {
	for i := range items {
		if previewGalleryItemMatches(items[i], pv) {
			return i
		}
	}
	return -1
}

// collectPreviewGallery 按消息顺序收集会话里的图片：用户消息的图片附件，
// 以及助手正文 markdown 里能被 localImagePath 解析的本地图片。
// 远程图片与打不开的地址一律跳过，避免翻页翻到加载不出来的条目。
func collectPreviewGallery(messages []Message) []previewGalleryItem {
	var items []previewGalleryItem
	seen := map[string]bool{}
	for _, msg := range messages {
		for _, item := range collectAttachmentGalleryItems(msg) {
			key := previewGalleryKey(item)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, item)
		}
		if msg.Role != "assistant" {
			continue
		}
		for _, item := range collectMarkdownGalleryItems(msg) {
			key := previewGalleryKey(item)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, item)
		}
	}
	return items
}

// collectAttachmentGalleryItems 收集一条用户消息里的图片附件；非图片附件直接排除。
func collectAttachmentGalleryItems(msg Message) []previewGalleryItem {
	if msg.Role != "user" || len(msg.Media) == 0 {
		return nil
	}
	var items []previewGalleryItem
	for _, media := range msg.Media {
		if !isImageMedia(media) {
			continue
		}
		items = append(items, previewGalleryItem{
			Kind:      previewGalleryAttachment,
			MessageID: msg.ID,
			MediaID:   media.ID,
			Name:      media.Name,
			MimeType:  media.MimeType,
		})
	}
	return items
}

// collectMarkdownGalleryItems 收集一条消息正文里的本地图片。
func collectMarkdownGalleryItems(msg Message) []previewGalleryItem {
	refs := markdownImageRefs(msg.Text)
	if len(refs) == 0 {
		return nil
	}
	items := make([]previewGalleryItem, 0, len(refs))
	for _, ref := range refs {
		path, err := localImagePath(ref.URL)
		if err != nil || path == "" {
			continue
		}
		items = append(items, previewGalleryItem{
			Kind:      previewGalleryMarkdown,
			MessageID: msg.ID,
			URL:       ref.URL,
			Alt:       ref.Alt,
			LocalPath: path,
		})
	}
	return items
}

// previewMarkdownImage 是正文 markdown 里的一张图片：原始地址与替代文字。
type previewMarkdownImage struct {
	URL string
	Alt string
}

// markdownImageRefs 按出现顺序取出 markdown 正文里的图片地址。
// 遍历方式与 extractLocalFileLinks 一致：只看承载正文的块，逐个跳到下一个内联节点。
func markdownImageRefs(text string) []previewMarkdownImage {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	doc := parser.New(parser.Default)
	doc.Set(text)
	var refs []previewMarkdownImage
	for i := 1; i < doc.Len(); i++ {
		switch doc.Block(i).Kind {
		case parser.Paragraph, parser.Heading, parser.TableCell:
		default:
			continue
		}
		in := doc.Inlines(i)
		for k := 0; k < len(in); k = in[k].Next {
			if in[k].Kind != parser.Image || in[k].Pending {
				continue
			}
			dest := strings.TrimSpace(in[k].Dest)
			if dest == "" {
				continue
			}
			alt := ""
			if in[k].Next > k+1 {
				alt = strings.TrimSpace(parser.PlainText(in[k+1 : in[k].Next]))
			}
			refs = append(refs, previewMarkdownImage{URL: dest, Alt: alt})
		}
	}
	return refs
}

// previewGalleryCache 缓存最近一次解析出的画廊列表。消息快照的会话、版本号、
// 消息数量与最后一条消息 ID 都没变时直接复用，避免每帧解析全部 markdown。
type previewGalleryCache struct {
	mu       sync.Mutex
	items    []previewGalleryItem
	threadID string
	rev      uint64
	length   int
	lastID   string
}

// previewGalleryItemsCache 是画廊列表的进程级缓存。
var previewGalleryItemsCache previewGalleryCache

// previewGalleryFor 返回当前会话的画廊列表，从 chatState 里取消息快照。
func previewGalleryFor(s *chatState) []previewGalleryItem {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	snapshot := s.messages
	threadID := s.selectedThreadID
	rev := s.messagesRev
	s.mu.Unlock()
	return previewGalleryFromSnapshot(snapshot, threadID, rev)
}

// previewGalleryFromSnapshot 按消息快照现算画廊列表，消息数量与最后一条消息 ID
// 都没变时直接复用上次的结果（流式增量消息只改最后一条）。
func previewGalleryFromSnapshot(messages []Message, threadID string, rev uint64) []previewGalleryItem {
	lastID := ""
	if n := len(messages); n > 0 {
		lastID = messages[n-1].ID
	}
	cache := &previewGalleryItemsCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.items != nil && cache.threadID == threadID && cache.rev == rev &&
		cache.length == len(messages) && cache.lastID == lastID {
		return cache.items
	}
	items := collectPreviewGallery(messages)
	for i := range items {
		items[i].ThreadID = threadID
	}
	cache.items = items
	cache.threadID = threadID
	cache.rev = rev
	cache.length = len(messages)
	cache.lastID = lastID
	return items
}

// previewGalleryState 把画廊条目转成浮层状态：附件先显示缩略图，
// 原图由 full 引用在加载完成后替换；正文图片按地址加载本地文件。
// 翻页时缩放与平移使用零值，由 previewClampZoom 归一为 1x。
func previewGalleryState(c *ui.Context, s *chatState, item previewGalleryItem) *imagePreviewState {
	if s == nil {
		return nil
	}
	if item.Kind == previewGalleryMarkdown {
		return &imagePreviewState{
			src: cachedPreviewImage(c, item.URL),
			alt: item.Alt,
			url: item.URL,
		}
	}
	ref := &messageMediaRef{
		threadID:  item.ThreadID,
		messageID: item.MessageID,
		mediaID:   item.MediaID,
	}
	// 原图还没加载好时先用缩略图顶上，和附件入口的行为一致。
	var src ui.ImageSource
	if full := cachedMessageMedia(c, s, ref.threadID, ref.messageID, ref.mediaID, "full"); full != nil {
		src = full
	} else {
		src = cachedMessageMedia(c, s, ref.threadID, ref.messageID, ref.mediaID, "thumb")
	}
	return &imagePreviewState{src: src, alt: item.Name, full: ref}
}

// previewLocalPath 返回图片对应的本地文件路径：只有正文里的本地图片有，
// 消息附件与输入框附件都没有。
func previewLocalPath(pv *imagePreviewState) string {
	if pv == nil || pv.full != nil {
		return ""
	}
	path, err := localImagePath(strings.TrimSpace(pv.url))
	if err != nil {
		return ""
	}
	return path
}

// previewSaveName 返回另存为的默认文件名：附件名、本地文件名、地址末段依次兜底，
// 没有扩展名时按 MIME 补一个。
func previewSaveName(pv *imagePreviewState, mimeType string) string {
	if pv == nil {
		return "image" + previewImageExtForMime(mimeType)
	}
	name := ""
	if pv.full != nil {
		name = filepath.Base(strings.TrimSpace(pv.alt))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = filepath.Base(previewLocalPath(pv))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = previewURLBaseName(pv.url)
	}
	name = sanitizePreviewFileName(name)
	if name == "" {
		name = "image"
	}
	if filepath.Ext(name) == "" {
		name += previewImageExtForMime(mimeType)
	}
	return name
}

// previewURLBaseName 取地址里最后一段路径作为文件名，去掉查询串与锚点。
func previewURLBaseName(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if i := strings.IndexAny(value, "?#"); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimRight(value, "/")
	if i := strings.LastIndex(value, "/"); i >= 0 {
		value = value[i+1:]
	}
	return value
}

// sanitizePreviewFileName 去掉文件名里的路径分隔符与控制字符，避免用 alt 文本生成非法文件名。
func sanitizePreviewFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"\n", " ",
		"\r", " ",
		"\x00", "",
	).Replace(name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}

// previewImageExtForMime 返回图片 MIME 对应的扩展名，未知时按 PNG 处理。
func previewImageExtForMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/tiff":
		return ".tiff"
	default:
		return ".png"
	}
}

// previewImageData 取出图片的原始字节：本地图片直接读盘，消息附件按 full 变体读原图。
func previewImageData(s *chatState, pv *imagePreviewState) (string, []byte, error) {
	if path := previewLocalPath(pv); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		return detectImageMime(filepath.Base(path), data), data, nil
	}
	if pv == nil || pv.full == nil {
		return "", nil, errors.New("没有可读取的原图")
	}
	reader, ok := s.client.(messageMediaReader)
	if !ok {
		return "", nil, errors.New("当前客户端不支持读取附件原图")
	}
	mimeType, data, err := reader.ReadMessageMedia(context.Background(), pv.full.threadID, pv.full.messageID, pv.full.mediaID, "full")
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		return "", nil, errors.New("原图内容为空")
	}
	return mimeType, data, nil
}

// previewSaveDialog 唤起系统保存对话框，测试时可替换以避免弹出真实窗口。
var previewSaveDialog func(defaultPath string) (string, error) = defaultPreviewSaveDialog

// defaultPreviewSaveDialog 打开系统原生保存框，返回用户选择的路径；取消时路径为空。
func defaultPreviewSaveDialog(defaultPath string) (string, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:             "另存图片",
		DefaultPath:       defaultPath,
		ButtonLabel:       "保存",
		CreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// savePreviewImage 把当前图片另存到用户选择的路径：本地图片复制文件，
// 消息附件写入原图字节；取消或失败时给出提示。
func savePreviewImage(s *chatState, pv *imagePreviewState) {
	if s == nil || pv == nil {
		return
	}
	mimeType, data, err := previewImageData(s, pv)
	if err != nil {
		s.setToast("读取原图失败：" + shortMediaError(err))
		return
	}
	defaultPath := previewSaveName(pv, mimeType)
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		defaultPath = filepath.Join(home, "Downloads", defaultPath)
	}
	target, err := previewSaveDialog(defaultPath)
	if err != nil {
		s.setToast("打开保存对话框失败：" + shortMediaError(err))
		return
	}
	if strings.TrimSpace(target) == "" {
		return
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		s.setToast("保存失败：" + shortMediaError(err))
		return
	}
	s.setToast("已保存到 " + filepath.Base(target))
}

// sendPreviewToComposer 把当前图片加入输入框附件：本地图片交给 addAttachmentFiles
// 走读盘与解码，消息附件先取原图字节再交给 addAttachment。
func sendPreviewToComposer(s *chatState, pv *imagePreviewState) {
	if s == nil || pv == nil {
		return
	}
	if path := previewLocalPath(pv); path != "" {
		s.addAttachmentFiles([]string{path})
		return
	}
	ref := pv.full
	if ref == nil {
		return
	}
	reader, ok := s.client.(messageMediaReader)
	if !ok {
		s.setToast("当前客户端不支持读取附件原图")
		return
	}
	alt := strings.TrimSpace(pv.alt)
	// 读原图与解码可能很慢，放到后台，完成后由 addAttachment 自己加锁写入附件列表。
	go func() {
		mimeType, data, err := reader.ReadMessageMedia(context.Background(), ref.threadID, ref.messageID, ref.mediaID, "full")
		if err != nil || len(data) == 0 {
			s.setToast("读取原图失败，无法加入输入框附件")
			return
		}
		name := sanitizePreviewFileName(alt)
		if name == "" {
			name = "image" + previewImageExtForMime(mimeType)
		}
		s.addAttachment(name, mimeType, data)
	}()
}
