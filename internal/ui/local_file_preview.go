package mcodeui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 悬停卡片的尺寸与读取上限。
const (
	// localPreviewThumbWidth 是缩略图的最大宽度，高度按原比例。
	localPreviewThumbWidth float32 = 240
	// localPreviewMaxWidth 是卡片宽度上限，避免长行把卡片撑开。
	localPreviewMaxWidth float32 = 520
	// localPreviewMaxBytes 是文本预览允许读取的最大字节数，超过后不读内容。
	localPreviewMaxBytes int64 = 256 << 10
	// localPreviewSniffBytes 是判断二进制内容时查看的前若干字节。
	localPreviewSniffBytes = 4 << 10
	// localPreviewLineCount 是文本预览显示的行数。
	localPreviewLineCount = 8
	// localPreviewLineRunes 是单行显示的字符数上限，超出部分截断不换行。
	localPreviewLineRunes = 72
)

// localPreviewKind 是悬停卡片的内容形式。
type localPreviewKind int

const (
	localPreviewNone localPreviewKind = iota
	localPreviewImage
	localPreviewText
)

// localFilePreviewBinaryExts 是不做内容预览的二进制格式：压缩包、文档、音视频、字体与可执行文件等。
var localFilePreviewBinaryExts = map[string]bool{
	".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".iso": true, ".img": true, ".dmg": true, ".pkg": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".pages": true, ".numbers": true, ".key": true,
	".mp3": true, ".wav": true, ".flac": true, ".aac": true, ".m4a": true, ".ogg": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".ico": true, ".icns": true,
	".heic": true, ".tiff": true, ".psd": true, ".sketch": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true,
	".class": true, ".jar": true, ".war": true, ".bin": true, ".dat": true,
	".db": true, ".sqlite": true, ".app": true,
}

// localFilePreviewKind 只按扩展名判断预览形式，不访问文件系统：
// 图片按后缀，已知的二进制格式不预览，其余按文本尝试。
func localFilePreviewKind(path string) localPreviewKind {
	ext := strings.ToLower(filepath.Ext(path))
	if imagePreviewExts[ext] {
		return localPreviewImage
	}
	if localFilePreviewBinaryExts[ext] {
		return localPreviewNone
	}
	return localPreviewText
}

// localFilePreviewPath 从链接地址里挑出预览用的路径与形式：图片优先，其次文本。
func localFilePreviewPath(dest string) (string, localPreviewKind) {
	text := ""
	for _, p := range localFileLinkCandidates(dest) {
		switch localFilePreviewKind(p) {
		case localPreviewImage:
			return p, localPreviewImage
		case localPreviewText:
			if text == "" {
				text = p
			}
		}
	}
	if text == "" {
		return "", localPreviewNone
	}
	return text, localPreviewText
}

// localFileLinkPreview 返回本地文件链接的悬停卡片填充函数：图片显示缩略图，文本显示前几行，
// 其他地址与文件类型返回 nil，交给默认的地址提示。这里只做字符串解析，不访问文件系统，
// 读盘都留到卡片真正显示时的填充函数里。
func localFileLinkPreview(c *ui.Context, dest string) func(tip ui.Element) {
	path, kind := localFilePreviewPath(dest)
	first, last := localFileLinkLineRange(dest)
	switch kind {
	case localPreviewImage:
		return func(tip ui.Element) {
			card := localPreviewCard(c, tip)
			localPreviewPathText(c, card, dest)
			if resolved, ok := localFileLinkPath(dest); ok {
				path = resolved
			}
			src, err := loadLocalImage(path)
			if err != nil {
				return
			}
			card.Children(func() {
				ui.Image(c, src).
					MaxWidth(localPreviewThumbWidth).
					Radius(RadiusXS)
			})
		}
	case localPreviewText:
		return func(tip ui.Element) {
			card := localPreviewCard(c, tip)
			localPreviewPathText(c, card, dest)
			if resolved, ok := localFileLinkPath(dest); ok {
				path = resolved
			}
			start := 1
			if first > 0 {
				start = max(1, first-2)
			}
			lines := localPreviewLineWindow(localTextPreviewLines(path), start)
			if len(lines) == 0 {
				return
			}
			card.Children(func() {
				ui.Column(c).Gap(Space0).Children(func() {
					for i, line := range lines {
						row := ui.Row(c).FillWidth()
						if first > 0 && start+i >= first && start+i <= last {
							row.Background(ThemeAccentSubtle(c))
						}
						row.Children(func() {
							ui.Text(c, line).
								Font("monospace").
								FontSize(codeFontSize()).
								NoWrap().
								TextColor(ThemePrimaryText(c))
						})
					}
				})
			})
		}
	}
	return nil
}

// localPreviewCard 给 TooltipBase 交来的卡片加上统一的底色、描边与内边距。
func localPreviewCard(c *ui.Context, tip ui.Element) ui.Element {
	card := tip.MaxWidth(localPreviewMaxWidth).
		Padding(Space2).
		Gap(Space2).
		Radius(RadiusSM).
		Background(ThemeSurfacePopover(c)).
		Border(Hairline, ThemeBorder(c))
	return applyElevation(c, card, ElevPopover)
}

// localPreviewPathText 渲染悬停卡片中的完整路径。
func localPreviewPathText(c *ui.Context, card ui.Element, path string) {
	card.Children(func() {
		ui.Text(c, path).
			MaxWidth(localPreviewMaxWidth - 2*Space2).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c))
	})
}

// localTextPreviewEntry 是按「修改时间、大小」失效的文本预览缓存。
type localTextPreviewEntry struct {
	modTime time.Time
	size    int64
	lines   []string
}

var (
	localTextPreviewMu    sync.Mutex
	localTextPreviewCache = map[string]localTextPreviewEntry{}
)

// localTextPreviewLines 读文件的全部预览行；目录、过大、读不了或含 NUL 的文件返回 nil。
// 结果按路径、修改时间与大小缓存在进程内，重复悬停不再读盘。
func localTextPreviewLines(path string) []string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	localTextPreviewMu.Lock()
	entry, cached := localTextPreviewCache[path]
	localTextPreviewMu.Unlock()
	if cached && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		return entry.lines
	}
	lines := readTextPreview(path, info.Size())
	localTextPreviewMu.Lock()
	localTextPreviewCache[path] = localTextPreviewEntry{modTime: info.ModTime(), size: info.Size(), lines: lines}
	localTextPreviewMu.Unlock()
	return lines
}

// readTextPreview 读取文件并取出预览行，超过字节上限的文件不读。
func readTextPreview(path string, size int64) []string {
	if size > localPreviewMaxBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return localPreviewTextLines(data)
}

// localPreviewTextLines 从文件内容里取出全部行：含 NUL 的二进制内容返回 nil，长行截断不换行。
func localPreviewTextLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	sniff := data
	if len(sniff) > localPreviewSniffBytes {
		sniff = sniff[:localPreviewSniffBytes]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return nil
	}
	lines := make([]string, 0, localPreviewLineCount)
	for _, line := range strings.Split(string(data), "\n") {
		lines = append(lines, localPreviewTruncateLine(line))
	}
	return lines
}

// localPreviewLineWindow 按从 1 开始的行号截取悬停卡片窗口。
func localPreviewLineWindow(lines []string, start int) []string {
	start = max(1, start) - 1
	if start >= len(lines) {
		return nil
	}
	return lines[start:min(start+localPreviewLineCount, len(lines))]
}

// localPreviewTruncateLine 去掉行尾回车，并把过长的行截断到固定字符数，末尾补省略号。
func localPreviewTruncateLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if utf8.RuneCountInString(line) <= localPreviewLineRunes {
		return line
	}
	return string([]rune(line)[:localPreviewLineRunes]) + "…"
}
