package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type timelineRefLoadState int

const (
	timelineRefLoading timelineRefLoadState = iota
	timelineRefReady
)

// parsedTimelineInspectorRef 保存从引用附件正文里解析出的工作区相对路径与行范围。
type parsedTimelineInspectorRef struct {
	path  string
	first int
	last  int
	ok    bool
	isDir bool
}

type timelineRefCacheEntry struct {
	mu    sync.Mutex
	state timelineRefLoadState
	ref   parsedTimelineInspectorRef
}

// timelineAttachmentRefCache 按「会话/消息/媒体」缓存引用文本的解析结果，避免渲染每帧重复请求。
var timelineAttachmentRefCache sync.Map // key -> *timelineRefCacheEntry

func timelineAttachmentRefKey(threadID, messageID, mediaID string) string {
	return threadID + "\x00" + messageID + "\x00" + mediaID
}

func isPotentialInspectorRefMedia(med MessageMedia) bool {
	mimeType := strings.ToLower(strings.TrimSpace(med.MimeType))
	return strings.HasPrefix(mimeType, "text/plain") && strings.HasSuffix(strings.ToLower(strings.TrimSpace(med.Name)), ".txt")
}

func timelineMediaReader(s *chatState) (messageMediaReader, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	reader, ok := client.(messageMediaReader)
	return reader, ok
}

// prefetchTimelineAttachmentRef 在后台读取并解析疑似工作区引用的文本媒体，每个键只发起一次请求。
func prefetchTimelineAttachmentRef(s *chatState, threadID, messageID string, med MessageMedia) {
	if s == nil || threadID == "" || messageID == "" || med.ID == "" || !isPotentialInspectorRefMedia(med) {
		return
	}
	reader, ok := timelineMediaReader(s)
	if !ok {
		return
	}
	key := timelineAttachmentRefKey(threadID, messageID, med.ID)
	v, loaded := timelineAttachmentRefCache.LoadOrStore(key, &timelineRefCacheEntry{})
	if loaded {
		return
	}
	e := v.(*timelineRefCacheEntry)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), previewMediaReadTimeout)
		defer cancel()
		_, data, err := reader.ReadMessageMedia(ctx, threadID, messageID, med.ID, "full")
		var parsed parsedTimelineInspectorRef
		if err == nil {
			parsed.path, parsed.first, parsed.last, parsed.ok = parseInspectorRefContent(data)
			if parsed.ok {
				if abs := fileManagerWorkspacePath(s, threadID, parsed.path); abs != "" {
					if info, statErr := os.Stat(abs); statErr == nil {
						parsed.isDir = info.IsDir()
					}
				}
			}
		}
		e.mu.Lock()
		e.state = timelineRefReady
		e.ref = parsed
		e.mu.Unlock()
		s.invalidate()
	}()
}

// cachedTimelineAttachmentRef 只读查询引用解析缓存，不触发网络或文件 IO。
func cachedTimelineAttachmentRef(threadID, messageID, mediaID string) *parsedTimelineInspectorRef {
	if threadID == "" || messageID == "" || mediaID == "" {
		return nil
	}
	v, ok := timelineAttachmentRefCache.Load(timelineAttachmentRefKey(threadID, messageID, mediaID))
	if !ok {
		return nil
	}
	e := v.(*timelineRefCacheEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != timelineRefReady || !e.ref.ok {
		return nil
	}
	ref := e.ref
	return &ref
}

// timelineFileCapsuleDisplayName 返回时间线文件胶囊的展示文案与 hover 完整名称。
func timelineFileCapsuleDisplayName(med MessageMedia, ref *parsedTimelineInspectorRef) string {
	if ref != nil && ref.ok {
		if ref.first > 0 {
			return fmt.Sprintf("%s:%d-%d", ref.path, ref.first, ref.last)
		}
		return ref.path
	}
	if med.Name != "" {
		return med.Name
	}
	return "附件"
}

func openTimelineInspectorRef(s *chatState, threadID string, ref parsedTimelineInspectorRef) {
	if s == nil || !ref.ok {
		return
	}
	inspectorSelect(s, sidePaneFileID, false)
	abs := fileManagerWorkspacePath(s, threadID, ref.path)
	if abs != "" {
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			s.loadFileManagerDir(threadID, ref.path)
			return
		}
	}
	s.openFileManagerFileAt(threadID, ref.path, true, ref.first, ref.last, true)
}

// timelineFileCapsuleMenu 返回用户消息文件胶囊的右键菜单。
func timelineFileCapsuleMenu(c *ui.Context, s *chatState, threadID string, m Message, med MessageMedia) func(*ui.Menu) {
	return func(menu *ui.Menu) {
		if s == nil {
			return
		}
		if ref := cachedTimelineAttachmentRef(threadID, m.ID, med.ID); ref != nil {
			buildTimelineRefCapsuleMenu(c, menu, s, threadID, *ref)
			return
		}
		buildTimelineMediaCapsuleMenu(c, menu, s, threadID, m.ID, med)
	}
}

func buildTimelineRefCapsuleMenu(c *ui.Context, menu *ui.Menu, s *chatState, threadID string, ref parsedTimelineInspectorRef) {
	abs := fileManagerWorkspacePath(s, threadID, ref.path)
	local := fileManagerLocalPath(abs)
	isDir := false
	if local {
		if info, err := os.Stat(abs); err == nil {
			isDir = info.IsDir()
		}
	}

	if isDir {
		if menu.Item("打开").Chosen() {
			inspectorSelect(s, sidePaneFileID, false)
			s.loadFileManagerDir(threadID, ref.path)
		}
	} else {
		if menu.Item("打开").Chosen() {
			inspectorSelect(s, sidePaneFileID, false)
			s.openFileManagerFileAt(threadID, ref.path, true, ref.first, ref.last, true)
		}
		if menu.Item("在右侧分栏打开").Chosen() {
			inspectorSelect(s, sidePaneFileID, false)
			openFileManagerInSplit(s, threadID, ref.path)
		}
		if local && fileManagerGOOS == "darwin" && menu.Item("快速查看").Chosen() {
			_ = fileManagerPreviewItem(abs)
		}
	}

	if local {
		menu.Separator()
		if !isDir {
			if menu.Item("在外部编辑器打开").Chosen() {
				s.openFileAtLocation(fileJumpRequest{
					Path:     abs,
					ThreadID: threadID,
					Line:     ref.first,
					EndLine:  ref.last,
					Target:   fileJumpExternal,
				})
			}
			if menu.Item("用默认应用打开").Chosen() {
				_ = mygo.Shell.OpenPath(abs)
			}
		}
		if menu.Item(revealInFileManagerLabel()).Chosen() {
			openItemInFinder(abs)
		}
	}

	menu.Separator()
	if menu.Item("引用到输入框").Chosen() {
		if st := getFileManagerState(s); st != nil {
			fileManagerMu.Lock()
			if st.threadID != threadID {
				fileManagerResetForThreadLocked(st, threadID)
			}
			fileManagerMu.Unlock()
		}
		s.addInspectorFileRefs([]inspectorFileRef{inspectorFileReference(s, threadID, ref.path, ref.first, ref.last)})
	}
	if menu.Item("复制路径").Disabled(abs == "").Chosen() {
		c.WriteClipboard(abs)
		s.setToast("已复制路径")
	}
	if menu.Item("复制相对路径").Chosen() {
		c.WriteClipboard(ref.path)
		s.setToast("已复制相对路径")
	}
}

func buildTimelineMediaCapsuleMenu(c *ui.Context, menu *ui.Menu, s *chatState, threadID, messageID string, med MessageMedia) {
	_, hasReader := timelineMediaReader(s)
	canRead := hasReader && threadID != "" && messageID != "" && med.ID != ""

	if fileManagerGOOS == "darwin" {
		if menu.Item("快速查看").Disabled(!canRead).Chosen() {
			openTimelineAttachmentTemp(s, threadID, messageID, med, fileManagerPreviewItem)
		}
	}
	if menu.Item("用默认应用打开").Disabled(!canRead).Chosen() {
		openTimelineAttachmentTemp(s, threadID, messageID, med, func(p string) error {
			return mygo.Shell.OpenPath(p)
		})
	}

	menu.Separator()
	if menu.Item("另存为…").Disabled(!canRead).Chosen() {
		saveTimelineAttachment(s, threadID, messageID, med)
	}
	if menu.Item("发送到对话").Disabled(!canRead).Chosen() {
		sendTimelineAttachmentToComposer(s, threadID, messageID, med)
	}

	menu.Separator()
	if menu.Item("复制文件名").Disabled(med.Name == "").Chosen() {
		c.WriteClipboard(med.Name)
		s.setToast("已复制文件名")
	}
}

func readTimelineAttachmentFull(s *chatState, threadID, messageID, mediaID string) (string, []byte, error) {
	reader, ok := timelineMediaReader(s)
	if !ok || threadID == "" || messageID == "" || mediaID == "" {
		return "", nil, errors.New("当前客户端不支持读取附件内容")
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewMediaReadTimeout)
	defer cancel()
	return reader.ReadMessageMedia(ctx, threadID, messageID, mediaID, "full")
}

func timelineAttachmentSafeName(name string) string {
	cleaned := sanitizePreviewFileName(filepath.Base(strings.TrimSpace(name)))
	if cleaned == "" {
		return "attachment"
	}
	return cleaned
}

func openTimelineAttachmentTemp(s *chatState, threadID, messageID string, med MessageMedia, openFn func(string) error) {
	if s == nil {
		return
	}
	safeName := timelineAttachmentSafeName(med.Name)
	safeID := sanitizePreviewFileName(med.ID)
	if safeID == "" {
		safeID = "item"
	}
	go func() {
		_, data, err := readTimelineAttachmentFull(s, threadID, messageID, med.ID)
		if err != nil {
			s.setToast("读取附件失败：" + shortMediaError(err))
			return
		}
		dir := filepath.Join(os.TempDir(), "mcode-attachments", safeID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			s.setToast("创建临时目录失败：" + shortMediaError(err))
			return
		}
		target := filepath.Join(dir, safeName)
		if err := os.WriteFile(target, data, 0o644); err != nil {
			s.setToast("写入临时文件失败：" + shortMediaError(err))
			return
		}
		if err := openFn(target); err != nil {
			s.setToast("打开附件失败：" + shortMediaError(err))
		}
	}()
}

// timelineAttachmentSaveDialog 唤起系统保存对话框，测试时可替换以避免弹出真实窗口。
var timelineAttachmentSaveDialog = func(defaultPath string) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:             "另存为",
		DefaultPath:       defaultPath,
		ButtonLabel:       "保存",
		CreateDirectories: true,
	})
}

func saveTimelineAttachment(s *chatState, threadID, messageID string, med MessageMedia) {
	if s == nil {
		return
	}
	defaultName := timelineAttachmentSafeName(med.Name)
	go func() {
		_, data, err := readTimelineAttachmentFull(s, threadID, messageID, med.ID)
		if err != nil {
			s.setToast("读取附件失败：" + shortMediaError(err))
			return
		}
		defaultPath := defaultName
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			defaultPath = filepath.Join(home, "Downloads", defaultName)
		}
		target, err := timelineAttachmentSaveDialog(defaultPath)
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
	}()
}

func sendTimelineAttachmentToComposer(s *chatState, threadID, messageID string, med MessageMedia) {
	if s == nil {
		return
	}
	go func() {
		mimeType, data, err := readTimelineAttachmentFull(s, threadID, messageID, med.ID)
		if err != nil {
			s.setToast("读取附件失败：" + shortMediaError(err))
			return
		}
		if strings.TrimSpace(mimeType) == "" {
			mimeType = med.MimeType
		}
		name := med.Name
		if name == "" {
			name = "附件"
		}
		s.addAttachment(name, mimeType, data)
	}()
}
