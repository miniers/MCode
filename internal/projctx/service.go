package projctx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DirName 为项目上下文根目录相对路径。
	DirName = ".mcode/context"
	// DocsDir 为文档子目录名称。
	DocsDir = "docs"
	// InboxDir 为收集箱子目录名称。
	InboxDir = "inbox"
)

var (
	// ErrInvalidName 表示文档或文件名称不合法。
	ErrInvalidName = errors.New("projctx: invalid name")
	// ErrRedirected 表示上下文目录被符号链接重定向至外部。
	ErrRedirected = errors.New("projctx: redirected context directory")
	// ErrNotFound 表示文档或条目不存在。
	ErrNotFound = errors.New("projctx: not found")
	// ErrEmptyMemo 表示备忘录内容为空。
	ErrEmptyMemo = errors.New("projctx: empty memo")
	// ErrConflict 表示并发写冲突。
	ErrConflict = errors.New("projctx: conflict")
)

// ConflictError 记录 CAS 期望哈希与实际哈希不一致的冲突详情。
type ConflictError struct {
	Name     string
	Expected string
	Actual   string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("projctx: conflict for %q: expected hash %q, got %q", e.Name, e.Expected, e.Actual)
}

// Is 实现 errors.Is 接口，使 errors.Is(err, ErrConflict) 成立。
func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

// Doc 描述一个上下文文档的信息与内容。
type Doc struct {
	Name    string
	Content string
	Hash    string
	ModTime time.Time
}

// HashContent 计算字符串内容的 sha256 小写十六进制值。
func HashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Service 提供项目上下文文档服务。
type Service struct {
	mu   sync.Mutex
	root string
	now  func() time.Time
}

// resolveMainCheckout 尝试将 Git worktree 路径规整为主仓库根目录。
// 如果 <abs>/.git 是普通文件且首行以 gitdir: 开头，读出 gitdir（相对路径就相对 abs 解析）；
// 再读 <gitdir>/commondir（相对路径就相对 gitdir 解析），得到 common；
// 如果 filepath.Base(common) == ".git" 且 filepath.Dir(common) 是存在的目录，就把 root 设为该目录。
// 其他情况（bare 仓库、读取失败、普通仓库）保持 abs。只读文件，不启动 git 子进程。
func resolveMainCheckout(abs string) string {
	gitPath := filepath.Join(abs, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil || !fi.Mode().IsRegular() {
		return abs
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return abs
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return abs
	}
	firstLine := strings.TrimSpace(lines[0])
	const gitdirPrefix = "gitdir:"
	if !strings.HasPrefix(firstLine, gitdirPrefix) {
		return abs
	}

	gitdirRaw := strings.TrimSpace(strings.TrimPrefix(firstLine, gitdirPrefix))
	if gitdirRaw == "" {
		return abs
	}

	var gitdir string
	if filepath.IsAbs(gitdirRaw) {
		gitdir = filepath.Clean(gitdirRaw)
	} else {
		gitdir = filepath.Clean(filepath.Join(abs, gitdirRaw))
	}

	commondirFile := filepath.Join(gitdir, "commondir")
	cdata, err := os.ReadFile(commondirFile)
	if err != nil {
		return abs
	}

	commonRaw := strings.TrimSpace(string(cdata))
	if commonRaw == "" {
		return abs
	}

	var common string
	if filepath.IsAbs(commonRaw) {
		common = filepath.Clean(commonRaw)
	} else {
		common = filepath.Clean(filepath.Join(gitdir, commonRaw))
	}

	if filepath.Base(common) == ".git" {
		mainDir := filepath.Dir(common)
		if st, err := os.Stat(mainDir); err == nil && st.IsDir() {
			return mainDir
		}
	}

	return abs
}

// ensureGitExclude 尽力确保 <root>/.git/info/exclude 中包含指定的排除行。
// 仅当 <root>/.git 是目录时处理，不存在就追加，已存在就跳过，出错只忽略。
func ensureGitExclude(root, entry string) {
	gitDir := filepath.Join(root, ".git")
	st, err := os.Stat(gitDir)
	if err != nil || !st.IsDir() {
		return
	}

	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return
	}

	excludePath := filepath.Join(infoDir, "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == entry {
				return
			}
		}
	}

	var toAppend string
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		toAppend = "\n" + entry + "\n"
	} else {
		toAppend = entry + "\n"
	}

	f, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(toAppend)
}

// New 创建项目上下文服务实例。
func New(projectPath string) (*Service, error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, fmt.Errorf("projctx: empty project path")
	}
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("projctx: abs project path: %w", err)
	}
	root := resolveMainCheckout(abs)
	return &Service{
		root: root,
		now:  time.Now,
	}, nil
}

// ContextDir 返回项目上下文根目录绝对路径。
func (s *Service) ContextDir() string {
	return filepath.Join(s.root, DirName)
}

// DocsPath 返回文档目录绝对路径。
func (s *Service) DocsPath() string {
	return filepath.Join(s.ContextDir(), DocsDir)
}

// InboxPath 返回收集箱目录绝对路径。
func (s *Service) InboxPath() string {
	return filepath.Join(s.ContextDir(), InboxDir)
}

func (s *Service) isRedirected() (bool, error) {
	mcodeDir := filepath.Join(s.root, ".mcode")
	if fi, err := os.Lstat(mcodeDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("projctx: stat .mcode: %w", err)
	}

	ctxDir := s.ContextDir()
	if fi, err := os.Lstat(ctxDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("projctx: stat context dir: %w", err)
	}

	return false, nil
}

// Ensure 创建 docs 和 inbox 目录骨架。若发现符号链接重定向则返回 ErrRedirected。
func (s *Service) Ensure() error {
	redir, err := s.isRedirected()
	if err != nil {
		return err
	}
	if redir {
		return ErrRedirected
	}

	if err := os.MkdirAll(s.DocsPath(), 0o755); err != nil {
		return fmt.Errorf("projctx: ensure docs: %w", err)
	}
	if err := os.MkdirAll(s.InboxPath(), 0o755); err != nil {
		return fmt.Errorf("projctx: ensure inbox: %w", err)
	}
	ensureGitExclude(s.root, "/.mcode/context/")
	return nil
}

func validName(name string) error {
	if name == "" {
		return fmt.Errorf("projctx: empty name: %w", ErrInvalidName)
	}
	if len(name) > 255 {
		return fmt.Errorf("projctx: name %q exceeds 255 characters: %w", name, ErrInvalidName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("projctx: invalid name %q: %w", name, ErrInvalidName)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("projctx: name %q starts with dot: %w", name, ErrInvalidName)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("projctx: name %q contains path separators: %w", name, ErrInvalidName)
	}
	if !strings.HasSuffix(name, ".md") {
		return fmt.Errorf("projctx: name %q does not end with .md: %w", name, ErrInvalidName)
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("projctx: name %q contains control characters: %w", name, ErrInvalidName)
		}
	}
	return nil
}

func (s *Service) resolve(dir, name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}

	redir, err := s.isRedirected()
	if err != nil {
		return "", err
	}
	if redir {
		return "", ErrRedirected
	}

	realCtx, err := filepath.EvalSymlinks(s.ContextDir())
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("projctx: context dir not found: %w", ErrNotFound)
		}
		return "", fmt.Errorf("projctx: eval context dir: %w", err)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("projctx: target dir not found: %w", ErrNotFound)
		}
		return "", fmt.Errorf("projctx: eval target dir: %w", err)
	}

	rel, err := filepath.Rel(realCtx, realDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("projctx: target dir %q outside context dir: %w", realDir, ErrInvalidName)
	}

	target := filepath.Join(dir, name)
	fi, err := os.Lstat(target)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			realTarget, err := filepath.EvalSymlinks(target)
			if err != nil {
				return "", fmt.Errorf("projctx: dangling symlink %q: %w", name, ErrInvalidName)
			}
			relTarget, err := filepath.Rel(realDir, realTarget)
			if err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) || filepath.IsAbs(relTarget) {
				return "", fmt.Errorf("projctx: symlink %q points outside target dir: %w", name, ErrInvalidName)
			}
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("projctx: stat %s: %w", target, err)
	}

	return target, nil
}

// List 列出 docs 目录下的 .md 普通文件，跳过符号链接和子目录，按 Name 升序排序。
func (s *Service) List() ([]Doc, error) {
	redir, err := s.isRedirected()
	if err != nil {
		return nil, err
	}
	if redir {
		return nil, ErrRedirected
	}

	// docs 目录本身是符号链接时不列举：其条目位于项目之外。
	if fi, err := os.Lstat(s.DocsPath()); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return []Doc{}, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("projctx: stat docs dir: %w", err)
	}

	entries, err := os.ReadDir(s.DocsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Doc{}, nil
		}
		return nil, fmt.Errorf("projctx: list docs: %w", err)
	}

	var docs []Doc
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if err := validName(name); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		filePath := filepath.Join(s.DocsPath(), name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		content := string(data)
		docs = append(docs, Doc{
			Name:    name,
			Content: content,
			Hash:    HashContent(content),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Name < docs[j].Name
	})
	if docs == nil {
		docs = []Doc{}
	}
	return docs, nil
}

// Read 读取指定的文档内容。若文件不存在则返回 ErrNotFound。
func (s *Service) Read(name string) (Doc, error) {
	path, err := s.resolve(s.DocsPath(), name)
	if err != nil {
		return Doc{}, err
	}

	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, fmt.Errorf("projctx: %q: %w", name, ErrNotFound)
		}
		return Doc{}, fmt.Errorf("projctx: stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return Doc{}, fmt.Errorf("projctx: %q is directory: %w", name, ErrNotFound)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, fmt.Errorf("projctx: %q: %w", name, ErrNotFound)
		}
		return Doc{}, fmt.Errorf("projctx: read %s: %w", path, err)
	}

	content := string(data)
	return Doc{
		Name:    name,
		Content: content,
		Hash:    HashContent(content),
		ModTime: fi.ModTime(),
	}, nil
}

// Create 创建空文档；独占创建避免覆盖其他进程刚创建的文件。
func (s *Service) Create(name string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Ensure(); err != nil {
		return Doc{}, err
	}
	path, err := s.resolve(s.DocsPath(), name)
	if err != nil {
		return Doc{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Doc{}, fmt.Errorf("projctx: create %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return Doc{}, fmt.Errorf("projctx: close %s: %w", name, err)
	}
	return s.Read(name)
}

// ReadInbox 读取收集箱中的单条备忘录。若条目不存在则返回 ErrNotFound。
func (s *Service) ReadInbox(name string) (InboxItem, error) {
	path, err := s.resolve(s.InboxPath(), name)
	if err != nil {
		return InboxItem{}, err
	}

	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return InboxItem{}, fmt.Errorf("projctx: inbox item %q: %w", name, ErrNotFound)
		}
		return InboxItem{}, fmt.Errorf("projctx: stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return InboxItem{}, fmt.Errorf("projctx: inbox item %q is directory: %w", name, ErrNotFound)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return InboxItem{}, fmt.Errorf("projctx: inbox item %q: %w", name, ErrNotFound)
		}
		return InboxItem{}, fmt.Errorf("projctx: read %s: %w", path, err)
	}

	return InboxItem{
		Name:    name,
		Content: string(data),
		ModTime: fi.ModTime(),
	}, nil
}

// Write 以 CAS 语义原子写入文档。
func (s *Service) Write(name, content, expectedHash string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.Ensure(); err != nil {
		return Doc{}, err
	}

	path, err := s.resolve(s.DocsPath(), name)
	if err != nil {
		return Doc{}, err
	}

	data, err := os.ReadFile(path)
	if err == nil {
		existingContent := string(data)
		if existingContent == content {
			stat, _ := os.Stat(path)
			var modTime time.Time
			if stat != nil {
				modTime = stat.ModTime()
			} else {
				modTime = time.Now()
			}
			return Doc{
				Name:    name,
				Content: content,
				Hash:    HashContent(content),
				ModTime: modTime,
			}, nil
		}

		diskHash := HashContent(existingContent)
		if expectedHash != "" && expectedHash != diskHash {
			return Doc{}, &ConflictError{
				Name:     name,
				Expected: expectedHash,
				Actual:   diskHash,
			}
		}
	} else if os.IsNotExist(err) {
		if expectedHash != "" {
			return Doc{}, &ConflictError{
				Name:     name,
				Expected: expectedHash,
				Actual:   "",
			}
		}
	} else {
		return Doc{}, fmt.Errorf("projctx: read %s: %w", path, err)
	}

	dir := s.DocsPath()
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return Doc{}, fmt.Errorf("projctx: create temp in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()
		return Doc{}, fmt.Errorf("projctx: write temp %s: %w", tmpPath, err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return Doc{}, fmt.Errorf("projctx: sync temp %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return Doc{}, fmt.Errorf("projctx: close temp %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return Doc{}, fmt.Errorf("projctx: rename temp to %s: %w", path, err)
	}
	tmpPath = ""

	stat, _ := os.Stat(path)
	var modTime time.Time
	if stat != nil {
		modTime = stat.ModTime()
	} else {
		modTime = time.Now()
	}

	return Doc{
		Name:    name,
		Content: content,
		Hash:    HashContent(content),
		ModTime: modTime,
	}, nil
}
