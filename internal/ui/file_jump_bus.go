package mcodeui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcode/internal/fsutil"
)

// fileJumpTarget 指定文件/行号跳转的首选落点。
type fileJumpTarget int

const (
	fileJumpBuiltIn fileJumpTarget = iota
	fileJumpExternal
)

// fileJumpRequest 描述一次跨面板的文件与行号定位请求。
type fileJumpRequest struct {
	Path     string
	WorkDir  string
	ThreadID string
	Line     int
	EndLine  int
	Col      int
	Target   fileJumpTarget
}

var (
	fileJumpOpenLocalFn         = openLocalFile
	fileJumpOpenDefaultEditorFn = openInDefaultEditor
	consoleSourceStatFn         = os.Stat
)

const consoleSourceCacheLimit = 512

type consoleSourceCacheKey struct {
	workDir string
	source  string
}

type consoleSourceCacheEntry struct {
	absPath      string
	embeddedLine int
	ok           bool
}

var (
	consoleSourceCacheMu    sync.Mutex
	consoleSourceCache      = make(map[consoleSourceCacheKey]consoleSourceCacheEntry)
	consoleSourceCacheOrder []consoleSourceCacheKey
)

// resetConsoleSourceCache 清空控制台源码定位缓存，供测试隔离使用。
func resetConsoleSourceCache() {
	consoleSourceCacheMu.Lock()
	consoleSourceCache = make(map[consoleSourceCacheKey]consoleSourceCacheEntry)
	consoleSourceCacheOrder = nil
	consoleSourceCacheMu.Unlock()
}

// resolveFileJumpPaths 解析目标的绝对路径与工作区相对路径；路径穿越工作区外时 rel 返回空串。
func resolveFileJumpPaths(workDir, rawPath string) (absPath, rel string) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", ""
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	cleanDir := strings.TrimSpace(workDir)
	if cleanDir != "" {
		cleanDir = filepath.Clean(cleanDir)
	}
	if filepath.IsAbs(clean) {
		absPath = clean
		if cleanDir != "" {
			rel = filepath.ToSlash(turnFilePreviewPath(cleanDir, turnEditedFile{path: absPath, abs: absPath}))
		}
		return absPath, rel
	}
	if cleanDir != "" {
		joined := filepath.Clean(filepath.Join(cleanDir, clean))
		rel = filepath.ToSlash(turnFilePreviewPath(cleanDir, turnEditedFile{path: joined, abs: joined}))
		return joined, rel
	}
	if clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", filepath.ToSlash(clean)
	}
	return "", ""
}

func (s *chatState) openBuiltInFileAt(threadID, rel string, line, endLine int) bool {
	if s == nil || threadID == "" || rel == "" {
		return false
	}
	if !isFileManagerOpen(s) {
		toggleFileManager(s)
	}
	if line > 0 {
		s.openFileManagerFileExternalAt(threadID, rel, line, endLine)
	} else {
		s.openFileManagerFileExternal(threadID, rel)
	}
	return true
}

func openExternalFallbackPath(absPath string) bool {
	if absPath == "" {
		return false
	}
	fileJumpOpenLocalFn(absPath)
	return true
}

// openFileAtLocation 是工作台统一的文件与行号跳转入口。
func (s *chatState) openFileAtLocation(req fileJumpRequest) bool {
	if s == nil || strings.TrimSpace(req.Path) == "" {
		return false
	}
	tid := strings.TrimSpace(req.ThreadID)
	workDir := strings.TrimSpace(req.WorkDir)
	selTID, selDir := getSelectedThreadWorkDir(s)
	if tid == "" {
		tid = selTID
	}
	if workDir == "" {
		if tid != "" && tid != selTID {
			workDir = turnFilesWorkDir(s, tid)
		}
		if workDir == "" {
			workDir = selDir
		}
	}

	line, endLine, col := req.Line, req.EndLine, req.Col
	if line > 0 {
		if endLine < line {
			endLine = line
		}
	} else {
		line, endLine = 0, 0
	}

	absPath, rel := resolveFileJumpPaths(workDir, req.Path)
	if absPath != "" {
		if info, err := os.Stat(absPath); err == nil && info.IsDir() {
			openItemInFinder(absPath)
			return true
		}
	}

	switch req.Target {
	case fileJumpBuiltIn:
		if tid != "" && rel != "" {
			return s.openBuiltInFileAt(tid, rel, line, endLine)
		}
		if absPath == "" {
			return false
		}
		if line > 0 && openPathAtLine(s, absPath, line, col) {
			return true
		}
		fileJumpOpenLocalFn(absPath)
		return true

	case fileJumpExternal:
		if line > 0 {
			if absPath != "" && openPathAtLine(s, absPath, line, col) {
				return true
			}
			if tid != "" && rel != "" {
				return s.openBuiltInFileAt(tid, rel, line, endLine)
			}
			return openExternalFallbackPath(absPath)
		}
		if absPath != "" {
			fileJumpOpenDefaultEditorFn(s, absPath)
			return true
		}
		if tid != "" && rel != "" {
			return s.openBuiltInFileAt(tid, rel, 0, 0)
		}
		return false
	}
	return false
}

// resolveConsoleSourceCandidate 把控制台 source 字符串映射为候选绝对路径与内嵌行号。
func resolveConsoleSourceCandidate(source, cleanWorkDir string) (candidateAbs string, embeddedLine int, ok bool) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || cleanWorkDir == "" {
		return "", 0, false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "file://"):
		filePath, parsed := fsutil.PathFromFileURL(trimmed)
		if !parsed {
			return "", 0, false
		}
		body, parsedLine, _ := splitTerminalLinkPos(filePath)
		if body == "" {
			return "", 0, false
		}
		return filepath.Clean(body), parsedLine, true

	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return "", 0, false
		}
		body, parsedLine, _ := splitTerminalLinkPos(u.Path)
		relURL := strings.TrimPrefix(body, "/")
		if relURL == "" {
			return "", 0, false
		}
		return filepath.Clean(filepath.Join(cleanWorkDir, filepath.FromSlash(relURL))), parsedLine, true

	default:
		if strings.Contains(trimmed, "://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
			return "", 0, false
		}
		body, parsedLine, _ := splitTerminalLinkPos(trimmed)
		if body == "" {
			return "", 0, false
		}
		native := filepath.FromSlash(body)
		if filepath.IsAbs(native) {
			return filepath.Clean(native), parsedLine, true
		}
		return filepath.Clean(filepath.Join(cleanWorkDir, native)), parsedLine, true
	}
}

func resolveConsoleSourceOnDisk(source, cleanWorkDir string) consoleSourceCacheEntry {
	candidateAbs, embeddedLine, ok := resolveConsoleSourceCandidate(source, cleanWorkDir)
	if !ok || candidateAbs == "" {
		return consoleSourceCacheEntry{}
	}
	if turnFilePreviewPath(cleanWorkDir, turnEditedFile{path: candidateAbs, abs: candidateAbs}) == "" {
		return consoleSourceCacheEntry{}
	}
	info, err := consoleSourceStatFn(candidateAbs)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return consoleSourceCacheEntry{}
	}
	// 若路径真实存在符号链接，确认解析后的目标仍落在工作区内。
	if realDir, err1 := filepath.EvalSymlinks(cleanWorkDir); err1 == nil {
		if realFile, err2 := filepath.EvalSymlinks(candidateAbs); err2 == nil {
			if turnFilePreviewPath(realDir, turnEditedFile{path: realFile, abs: realFile}) == "" {
				return consoleSourceCacheEntry{}
			}
		}
	}
	return consoleSourceCacheEntry{
		absPath:      candidateAbs,
		embeddedLine: embeddedLine,
		ok:           true,
	}
}

// parseConsoleSourceLocation 解析浏览器控制台错误源位置：
// 仅当目标路径位于 workDir 内且在磁盘上作为常规文件存在时返回 ok = true。
// 使用有界内存缓存避免渲染热路径每帧重复触发 os.Stat。
func parseConsoleSourceLocation(source string, line int, workDir string) (absPath string, resolvedLine int, ok bool) {
	trimmedDir := strings.TrimSpace(workDir)
	trimmedSource := strings.TrimSpace(source)
	if trimmedDir == "" || trimmedSource == "" {
		return "", 0, false
	}
	cleanWorkDir := filepath.Clean(trimmedDir)
	key := consoleSourceCacheKey{workDir: cleanWorkDir, source: trimmedSource}

	consoleSourceCacheMu.Lock()
	entry, cached := consoleSourceCache[key]
	consoleSourceCacheMu.Unlock()

	if !cached {
		entry = resolveConsoleSourceOnDisk(trimmedSource, cleanWorkDir)
		consoleSourceCacheMu.Lock()
		if _, exists := consoleSourceCache[key]; !exists {
			if len(consoleSourceCache) >= consoleSourceCacheLimit && len(consoleSourceCacheOrder) > 0 {
				oldest := consoleSourceCacheOrder[0]
				consoleSourceCacheOrder = consoleSourceCacheOrder[1:]
				delete(consoleSourceCache, oldest)
			}
			consoleSourceCache[key] = entry
			consoleSourceCacheOrder = append(consoleSourceCacheOrder, key)
		}
		consoleSourceCacheMu.Unlock()
	}

	if !entry.ok {
		return "", 0, false
	}
	resolvedLine = line
	if resolvedLine <= 0 && entry.embeddedLine > 0 {
		resolvedLine = entry.embeddedLine
	}
	if resolvedLine < 0 {
		resolvedLine = 0
	}
	return entry.absPath, resolvedLine, true
}
