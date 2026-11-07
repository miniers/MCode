package projctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 标准文档名：四篇固定文档，位于上下文目录根部。
const (
	DocInstructions = "instructions"
	DocOverview     = "overview"
	DocDecisions    = "decisions"
	DocNotes        = "notes"
)

// 文档图标 id，对应 internal/ui/icons.go 中已有的图标。
const (
	IconShield = "shield"
	IconMap    = "map"
	IconSeal   = "seal-check"
	IconNote   = "note"
	IconFile   = "file"
)

// StandardDoc 描述一篇标准文档的静态元信息。
type StandardDoc struct {
	Name     string
	File     string
	Title    string
	Subtitle string
	Icon     string
	Template string
}

var standardDocs = []StandardDoc{
	{
		Name:     DocInstructions,
		File:     DocInstructions + ".md",
		Title:    "Instructions",
		Subtitle: "项目规则、边界与架构约束",
		Icon:     IconShield,
		Template: `# 项目规则

> 写给在本项目中工作的所有 Agent。每条规则尽量具体、可检查，过时的及时删掉。

## 必须遵守
- （例）提交前保证构建与测试通过。
- （例）不执行改写历史、强制推送等破坏性 git 操作。

## 禁止事项
- （例）未经确认不新增第三方依赖。

## 代码约定
- 命名、目录划分、错误处理与注释风格写在这里。
`,
	},
	{
		Name:     DocOverview,
		File:     DocOverview + ".md",
		Title:    "Overview",
		Subtitle: "系统架构、模块与当前重点",
		Icon:     IconMap,
		Template: `# 项目概览

## 这个项目做什么
一两句话说明用途与主要使用者。

## 目录与模块
- （例）cmd/：程序入口
- （例）internal/：业务实现

## 当前重点
- 正在推进的事项与近期目标。
`,
	},
	{
		Name:     DocDecisions,
		File:     DocDecisions + ".md",
		Title:    "Decisions",
		Subtitle: "架构决策记录（ADR）",
		Icon:     IconSeal,
		Template: `# 决策记录

> 每条决策一个小节，新的写在最上面；被推翻的决策保留并标注「已废弃」。

## 0001 标题
- 日期：
- 背景：为什么需要做这个决定
- 决定：选了什么
- 影响：带来的好处、代价与后续要注意的地方
`,
	},
	{
		Name:     DocNotes,
		File:     DocNotes + ".md",
		Title:    "Notes",
		Subtitle: "工作草稿、设计笔记与零散想法",
		Icon:     IconNote,
		Template: `# 笔记

临时记录、排查过程和还没定论的想法，整理成熟后再移到规则或决策里。
`,
	},
}

// StandardDocs 返回四篇标准文档的元信息副本（始终全部列出）。
func StandardDocs() []StandardDoc {
	return append([]StandardDoc(nil), standardDocs...)
}

// LookupStandard 按名称（大小写不敏感，可带 .md 后缀）查找标准文档。
func LookupStandard(name string) (StandardDoc, bool) {
	stem := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".md"))
	for _, doc := range standardDocs {
		if doc.Name == stem {
			return doc, true
		}
	}
	return StandardDoc{}, false
}

// TemplateFor 返回名称对应文档的初始模板：标准文档用固定模板，自定义文档只带一级标题。
func TemplateFor(raw string) (string, bool) {
	ref, err := ParseRef(raw)
	if err != nil {
		return "", false
	}
	if ref.Standard {
		return ref.Info.Template, true
	}
	return "# " + TitleOf(ref.Name) + "\n\n", true
}

// RefID 返回标准文档的引用 id（如 "instructions"）。
func (d StandardDoc) RefID() string {
	return d.Name
}

// RelativePath 返回标准文档相对项目根目录的路径。
func (d StandardDoc) RelativePath() string {
	return DirName + "/" + d.File
}

// DocRef 引用项目文档中的一篇文档：根部标准文档，或 docs/ 下的自定义文档。
type DocRef struct {
	Name     string // 规整后的文件名（总是 .md 结尾）
	Standard bool
	Info     StandardDoc
}

// ErrDocExists 表示同名文档已存在。
var ErrDocExists = errors.New("projctx: document already exists")

type docExistsError struct{ Name string }

func (e *docExistsError) Error() string {
	return fmt.Sprintf("projctx: document %q already exists", e.Name)
}

// Unwrap 让 errors.Is(err, os.ErrExist) 成立。
func (e *docExistsError) Unwrap() error { return os.ErrExist }

// Is 让 errors.Is(err, ErrDocExists) 成立。
func (e *docExistsError) Is(target error) bool { return target == ErrDocExists }

// ParseRef 把名称解析为文档引用。裸名（instructions、notes.md 等）先按标准名匹配，命中则指向根部文件，
// 否则规整为 docs/ 下的自定义文档。显式的 "docs/" 前缀（含 .mcode/context/docs/）一律指向自定义文档，
// 不做标准名匹配：docs/notes.md 是 docs/notes.md，而不是根部的 notes.md。名称非法时返回 ErrInvalidName。
func ParseRef(raw string) (DocRef, error) {
	name := strings.TrimSpace(raw)
	explicitDocs := false
	switch {
	case strings.HasPrefix(name, DocsDir+"/"):
		name, explicitDocs = strings.TrimPrefix(name, DocsDir+"/"), true
	case strings.HasPrefix(name, DirName+"/"+DocsDir+"/"):
		name, explicitDocs = strings.TrimPrefix(name, DirName+"/"+DocsDir+"/"), true
	case strings.HasPrefix(name, DirName+"/"):
		name = strings.TrimPrefix(name, DirName+"/")
	}
	if !explicitDocs {
		if doc, ok := LookupStandard(name); ok {
			return DocRef{Name: doc.File, Standard: true, Info: doc}, nil
		}
	}
	normalized, ok := normalizeFileName(name)
	if !ok {
		return DocRef{}, fmt.Errorf("projctx: invalid document name %q: %w", raw, ErrInvalidName)
	}
	return DocRef{Name: normalized}, nil
}

// NormalizeName 校验并规整自定义文档名：补 .md 后缀，拒绝路径分隔符、点开头、过长名称与标准名。
func NormalizeName(raw string) (string, bool) {
	if len([]rune(strings.TrimSpace(raw))) > 120 {
		return "", false
	}
	name, ok := normalizeFileName(raw)
	if !ok {
		return "", false
	}
	if _, ok := LookupStandard(name); ok {
		return "", false
	}
	return name, true
}

// normalizeFileName 补全 .md 后缀并校验文件名，不排除标准名：docs/ 下允许与标准名同名的文件。
func normalizeFileName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	if err := validName(name); err != nil {
		return "", false
	}
	return name, true
}

// TitleOf 由文件名推导展示标题：去掉扩展名与时间戳前缀，连字符/下划线转空格并首字母大写。
func TitleOf(name string) string {
	stem := name
	if strings.HasSuffix(strings.ToLower(stem), ".md") {
		stem = stem[:len(stem)-3]
	}
	if len(stem) > 16 && stem[8] == '-' && stem[15] == '-' && allDigits(stem[:8]) && allDigits(stem[9:15]) {
		stem = stem[16:]
	}
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(stem))
	if len(words) == 0 {
		return name
	}
	for i, word := range words {
		runes := []rune(word)
		words[i] = strings.ToUpper(string(runes[0])) + string(runes[1:])
	}
	return strings.Join(words, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DocInfo 是 Manifest 中的一篇文档：标准文档即使不存在也会列出，Exists 为 false。
type DocInfo struct {
	Name     string
	File     string
	Title    string
	Subtitle string
	Icon     string
	Standard bool
	Exists   bool
	Hash     string
	Size     int
	ModTime  time.Time
}

// RefID 返回文档引用 id：标准文档为名称，自定义文档为 "docs/<file>"。
func (d DocInfo) RefID() string {
	if d.Standard {
		return d.Name
	}
	return DocsDir + "/" + d.File
}

// RelativePath 返回文档相对项目根目录的路径。
func (d DocInfo) RelativePath() string {
	if d.Standard {
		return DirName + "/" + d.File
	}
	return DirName + "/" + DocsDir + "/" + d.File
}

// Manifest 汇总项目文档的文档：四篇标准文档（无论是否存在）+ 自定义文档。
type Manifest struct {
	Docs []DocInfo
}

// Standard 返回四篇标准文档的信息（顺序固定）。
func (m Manifest) Standard() []DocInfo {
	var out []DocInfo
	for _, d := range m.Docs {
		if d.Standard {
			out = append(out, d)
		}
	}
	return out
}

// Custom 返回 docs/ 下的自定义文档，按标题排序。
func (m Manifest) Custom() []DocInfo {
	var out []DocInfo
	for _, d := range m.Docs {
		if !d.Standard {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// Find 按名称查找文档。
func (m Manifest) Find(name string) (DocInfo, bool) {
	ref, err := ParseRef(name)
	if err != nil {
		return DocInfo{}, false
	}
	for _, d := range m.Docs {
		if d.File == ref.Name && d.Standard == ref.Standard {
			return d, true
		}
	}
	return DocInfo{}, false
}

// docPath 返回文档在磁盘上的绝对路径（标准文档在上下文根部，自定义文档在 docs/）。
func (s *Service) docPath(ref DocRef) (string, error) {
	dir := s.DocsPath()
	if ref.Standard {
		dir = s.ContextDir()
	}
	return s.resolve(dir, ref.Name)
}

func (s *Service) readInfo(ref DocRef) (DocInfo, error) {
	info := DocInfo{Name: ref.Name, File: ref.Name, Standard: ref.Standard, Icon: IconFile}
	if ref.Standard {
		info.Name, info.File = ref.Info.Name, ref.Info.File
		info.Title, info.Subtitle, info.Icon = ref.Info.Title, ref.Info.Subtitle, ref.Info.Icon
	} else {
		info.Name = strings.TrimSuffix(ref.Name, ".md")
		info.Title = TitleOf(ref.Name)
		info.Subtitle = "自定义文档 · docs/"
	}
	path, err := s.docPath(ref)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return info, nil
		}
		return DocInfo{}, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return info, nil
		}
		return DocInfo{}, fmt.Errorf("projctx: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return info, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DocInfo{}, fmt.Errorf("projctx: read %s: %w", path, err)
	}
	info.Exists = true
	info.Hash = HashContent(string(data))
	info.Size = len(data)
	info.ModTime = fi.ModTime()
	return info, nil
}

// ModifyWithRetry 按 mutate 变换读取到的内容并以 CAS 写回；遇到并发冲突时重读后重试，
// 最多 attempts 次，超过后返回最后一次的错误。标准文档尚不存在时以模板作为旧内容。
func (s *Service) ModifyWithRetry(raw, _ string, mutate func(old string) string, attempts int) (Doc, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		old, err := s.ReadOrDefault(raw)
		if err != nil {
			return Doc{}, err
		}
		doc, err := s.WriteRef(raw, mutate(old.Content), old.Hash)
		if err == nil {
			return doc, nil
		}
		if !errors.Is(err, ErrConflict) {
			return Doc{}, err
		}
		lastErr = err
	}
	return Doc{}, lastErr
}

// Manifest 读取上下文目录，列出四篇标准文档（缺失时 Exists=false）与 docs/ 下的自定义文档。
// 不创建任何文件：目录尚未建立时同样返回完整的标准清单。
func (s *Service) Manifest() (Manifest, error) {
	m := Manifest{Docs: make([]DocInfo, 0, len(standardDocs))}
	for _, doc := range standardDocs {
		info, err := s.readInfo(DocRef{Name: doc.File, Standard: true, Info: doc})
		if err != nil {
			return Manifest{}, err
		}
		m.Docs = append(m.Docs, info)
	}
	customs, err := s.List()
	if err != nil {
		return Manifest{}, err
	}
	for _, doc := range customs {
		m.Docs = append(m.Docs, DocInfo{
			Name:     strings.TrimSuffix(doc.Name, ".md"),
			File:     doc.Name,
			Title:    TitleOf(doc.Name),
			Subtitle: "自定义文档 · docs/",
			Icon:     IconFile,
			Exists:   true,
			Hash:     doc.Hash,
			Size:     len(doc.Content),
			ModTime:  doc.ModTime,
		})
	}
	return m, nil
}

// ReadRef 按名称读取文档（"docs/<file>" 前缀自动去掉，标准名映射到根部文件）。
func (s *Service) ReadRef(raw string) (Doc, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return Doc{}, err
	}
	return s.ReadRefPath(ref)
}

// ReadRefPath 按已解析的引用读取文档，若文件不存在则返回 ErrNotFound。
func (s *Service) ReadRefPath(ref DocRef) (Doc, error) {
	path, err := s.docPath(ref)
	if err != nil {
		return Doc{}, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, fmt.Errorf("projctx: %q: %w", ref.Name, ErrNotFound)
		}
		return Doc{}, fmt.Errorf("projctx: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return Doc{}, fmt.Errorf("projctx: %q is not a regular file: %w", ref.Name, ErrNotFound)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, fmt.Errorf("projctx: %q: %w", ref.Name, ErrNotFound)
		}
		return Doc{}, fmt.Errorf("projctx: read %s: %w", path, err)
	}
	content := string(data)
	return Doc{Name: ref.Name, Content: content, Hash: HashContent(content), ModTime: fi.ModTime()}, nil
}

// ReadOrDefault 读取文档；标准文档尚未创建时返回模板内容（Hash 为空，表示尚未落盘）。
func (s *Service) ReadOrDefault(raw string) (Doc, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return Doc{}, err
	}
	doc, err := s.ReadRefPath(ref)
	if err == nil {
		return doc, nil
	}
	if !errors.Is(err, ErrNotFound) || !ref.Standard {
		return Doc{}, err
	}
	return Doc{Name: ref.Name, Content: ref.Info.Template}, nil
}

// CASUnchanged 是 WriteRef 在磁盘内容已与待写入内容一致时返回的错误，
// 调用方可以按"无需写入"处理。
var CASUnchanged = errors.New("projctx: content unchanged")

// checkCAS 校验磁盘内容与期望哈希；内容已一致时返回 CASUnchanged。
func checkCAS(path, name, content, expectedHash string) error {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		existing := string(data)
		if existing == content {
			return CASUnchanged
		}
		diskHash := HashContent(existing)
		if expectedHash != "" && expectedHash != diskHash {
			return &ConflictError{Name: name, Expected: expectedHash, Actual: diskHash}
		}
	case os.IsNotExist(err):
		if expectedHash != "" {
			return &ConflictError{Name: name, Expected: expectedHash, Actual: ""}
		}
	default:
		return fmt.Errorf("projctx: read %s: %w", path, err)
	}
	return nil
}

// writeMu 串行化进程内所有 CAS 写入：调用方常临时 projctx.New 一个 Service，
// 实例锁 s.mu 无法保护跨实例的"先查后写"，必须再加包级锁。
var writeMu sync.Mutex

// WriteRef 以 CAS 语义写入文档：expectedHash 为加载时的哈希，为空表示新建或强制覆盖。
func (s *Service) WriteRef(raw, content, expectedHash string) (Doc, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return Doc{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := s.Ensure(); err != nil {
		return Doc{}, err
	}
	path, err := s.docPath(ref)
	if err != nil {
		return Doc{}, err
	}
	if err := checkCAS(path, ref.Name, content, expectedHash); err != nil {
		if errors.Is(err, CASUnchanged) {
			return s.ReadRefPath(ref)
		}
		return Doc{}, err
	}
	if err := writeFileAtomic(filepath.Dir(path), path, content); err != nil {
		return Doc{}, err
	}
	return s.ReadRefPath(ref)
}

// writeFileAtomic 以临时文件 + rename 原子写入文件内容。
func writeFileAtomic(dir, path, content string) error {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("projctx: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("projctx: write temp %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("projctx: sync temp %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("projctx: close temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("projctx: rename temp to %s: %w", path, err)
	}
	tmpPath = ""
	return nil
}

// CreateDoc 按内置模板创建文档（标准文档落在上下文根部，自定义文档落在 docs/），
// 已存在时返回 ErrDocExists（同时满足 os.ErrExist），不覆盖已有内容。
func (s *Service) CreateDoc(raw string) (Doc, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return Doc{}, err
	}
	template, ok := TemplateFor(raw)
	if !ok {
		return Doc{}, fmt.Errorf("projctx: invalid document name %q: %w", raw, ErrInvalidName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Ensure(); err != nil {
		return Doc{}, err
	}
	path, err := s.docPath(ref)
	if err != nil {
		return Doc{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Doc{}, &docExistsError{Name: ref.Name}
		}
		return Doc{}, fmt.Errorf("projctx: create %s: %w", ref.Name, err)
	}
	if _, err := file.WriteString(template); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Doc{}, fmt.Errorf("projctx: write %s: %w", ref.Name, err)
	}
	if err := file.Close(); err != nil {
		return Doc{}, fmt.Errorf("projctx: close %s: %w", ref.Name, err)
	}
	return s.ReadRefPath(ref)
}
