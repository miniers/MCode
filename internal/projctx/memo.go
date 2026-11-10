package projctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"mcode/internal/fsutil"
)

// InboxItem 描述收集箱中的一条备忘录。
type InboxItem struct {
	Name    string
	Content string
	ModTime time.Time
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func firstHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if heading != "" {
				return heading
			}
		}
	}
	return ""
}

func slugify(text string) string {
	lower := strings.ToLower(text)
	var b strings.Builder
	pendingDash := false
	for _, r := range lower {
		// 保留 Unicode 字母、标记与数字，标题为中文等非拉丁文时仍可读出文件名。
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			pendingDash = false
		} else {
			pendingDash = true
		}
	}

	res := strings.Trim(b.String(), "-")
	if runes := []rune(res); len(runes) > 48 {
		res = strings.Trim(string(runes[:48]), "-")
	}
	if res == "" {
		return "memo"
	}
	return res
}

// DraftMemo 将新的备忘录起草并写入 inbox 收集箱。
func (s *Service) DraftMemo(content, title string) (InboxItem, error) {
	body := strings.TrimSpace(content)
	if body == "" {
		return InboxItem{}, ErrEmptyMemo
	}

	cleanTitle := strings.TrimSpace(title)
	slugSource := cleanTitle
	if slugSource == "" {
		slugSource = firstHeading(body)
	}
	if slugSource == "" {
		slugSource = "memo"
	}
	slug := slugify(slugSource)

	now := s.currentTime()
	stamp := now.Format("20060102-150405")
	name := fmt.Sprintf("%s-%s.md", stamp, slug)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.Ensure(); err != nil {
		return InboxItem{}, err
	}

	attempt := 2
	for {
		p := filepath.Join(s.InboxPath(), name)
		_, err := os.Stat(p)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return InboxItem{}, fmt.Errorf("projctx: stat %s: %w", p, err)
		}
		name = fmt.Sprintf("%s-%s-%d.md", stamp, slug, attempt)
		attempt++
	}

	targetPath, err := s.resolve(s.InboxPath(), name)
	if err != nil {
		return InboxItem{}, err
	}

	stored := body
	if cleanTitle != "" && !strings.HasPrefix(stored, "#") {
		stored = "# " + cleanTitle + "\n\n" + stored
	}
	stored += "\n"

	if err := fsutil.WriteFileAtomic(targetPath, []byte(stored), 0o600); err != nil {
		return InboxItem{}, fmt.Errorf("projctx: write %s: %w", targetPath, err)
	}

	stat, _ := os.Stat(targetPath)
	var modTime time.Time
	if stat != nil {
		modTime = stat.ModTime()
	} else {
		modTime = now
	}

	return InboxItem{
		Name:    name,
		Content: stored,
		ModTime: modTime,
	}, nil
}

// Title 返回条目的展示标题：去掉 .md 与时间戳前缀，连字符/下划线转空格。
func (it InboxItem) Title() string {
	return TitleOf(it.Name)
}

// ListInbox 列出收集箱中的所有备忘录，按 Name 倒序（最新在前）排列。

func (s *Service) ListInbox() ([]InboxItem, error) {
	redir, err := s.isRedirected()
	if err != nil {
		return nil, err
	}
	if redir {
		return nil, ErrRedirected
	}

	// inbox 目录本身是符号链接时不列举：其条目位于项目之外。
	if fi, err := os.Lstat(s.InboxPath()); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return []InboxItem{}, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("projctx: stat inbox dir: %w", err)
	}

	entries, err := os.ReadDir(s.InboxPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []InboxItem{}, nil
		}
		return nil, fmt.Errorf("projctx: list inbox: %w", err)
	}

	var items []InboxItem
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
		filePath := filepath.Join(s.InboxPath(), name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		items = append(items, InboxItem{
			Name:    name,
			Content: string(data),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Name > items[j].Name
	})
	if items == nil {
		items = []InboxItem{}
	}
	return items, nil
}

// FileInbox 将收集箱条目归入目标文档（标准文档写在上下文根部，自定义文档写在 docs/）。
// 目标文档尚不存在时以空内容为基线。
func (s *Service) FileInbox(inboxName, docName string) (Doc, error) {
	inboxPath, err := s.resolve(s.InboxPath(), inboxName)
	if err != nil {
		return Doc{}, err
	}

	ref, err := ParseRef(docName)
	if err != nil {
		return Doc{}, err
	}

	data, err := os.ReadFile(inboxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, fmt.Errorf("projctx: inbox item %q: %w", inboxName, ErrNotFound)
		}
		return Doc{}, fmt.Errorf("projctx: read inbox %s: %w", inboxPath, err)
	}

	memoText := strings.TrimSpace(string(data))
	if memoText == "" {
		return Doc{}, ErrEmptyMemo
	}

	targetDoc, err := s.ReadRefPath(ref)
	var currentContent string
	var diskHash string
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return Doc{}, err
		}
		currentContent = ""
		diskHash = ""
	} else {
		currentContent = targetDoc.Content
		diskHash = targetDoc.Hash
	}

	head := currentContent
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	separator := ""
	if strings.TrimSpace(currentContent) != "" {
		separator = "\n---\n\n"
	}
	merged := head + separator + memoText + "\n"

	doc, err := s.WriteRef(docName, merged, diskHash)
	if err != nil {
		return Doc{}, err
	}

	_ = os.Remove(inboxPath)
	return doc, nil
}

// DiscardInbox 丢弃并删除收集箱中的指定备忘录。
func (s *Service) DiscardInbox(name string) error {
	inboxPath, err := s.resolve(s.InboxPath(), name)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(inboxPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("projctx: inbox item %q: %w", name, ErrNotFound)
		}
		return fmt.Errorf("projctx: stat %s: %w", inboxPath, err)
	}

	if err := os.Remove(inboxPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("projctx: inbox item %q: %w", name, ErrNotFound)
		}
		return fmt.Errorf("projctx: remove %s: %w", inboxPath, err)
	}
	return nil
}
