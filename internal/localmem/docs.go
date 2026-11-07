package localmem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

// ErrNotFound 表示指定记忆不存在。用 nmem 的 404 错误表示，
// MCP 层的 isNmemNotFound 因此无需感知本地后端。
var ErrNotFound error = &nmem.APIError{Status: http.StatusNotFound, Body: "localmem: memory not found"}

// overviewFocusID 是「当前重点」这条项目概览记忆的固定 id：
// 新项目还没有该小节时 Get 返回空内容、Commit 自动创建文档或追加小节。
const overviewFocusID = "project:overview#当前重点"

// placeholderPrefix 标出模板里的示例行。
const placeholderPrefix = "- （例）"

// metaPrefix 是小节首行元数据注释的前缀：文档里没有记忆字段，类型、重要度与标签
// 以一行 HTML 注释存在正文最前面，读回时再填到 nmem.Memory 上。
const metaPrefix = "<!-- mcode:meta "

// sectionMeta 是小节元数据注释的内容。
type sectionMeta struct {
	UnitType   string   `json:"unit_type,omitempty"`
	Importance float64  `json:"importance,omitempty"`
	Labels     []string `json:"labels,omitempty"`
}

// Docs 提供基于项目文档的记忆服务。
type Docs struct{ globalHome string }

// New 创建文档记忆服务；空 home 使用当前用户目录。
func New(globalHome string) *Docs {
	if globalHome == "" {
		globalHome, _ = os.UserHomeDir()
	}
	return &Docs{globalHome: globalHome}
}

func docForType(unit string) string {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "decision":
		return projctx.DocDecisions
	case "rule", "preference":
		return projctx.DocInstructions
	default:
		return projctx.DocNotes
	}
}

func docUnit(doc string) string {
	switch doc {
	case projctx.DocDecisions:
		return "decision"
	case projctx.DocInstructions:
		return "rule"
	default:
		return "fact"
	}
}

func (d *Docs) service(scope, projectPath string) (*projctx.Service, error) {
	if scope == "global" {
		return projctx.New(d.globalHome)
	}
	return projctx.New(projectPath)
}

func parseID(id string) (scope, doc, heading string, err error) {
	scope, rest, ok := strings.Cut(id, ":")
	if !ok || (scope != "project" && scope != "global") {
		return "", "", "", ErrNotFound
	}
	doc, heading, ok = strings.Cut(rest, "#")
	info, valid := projctx.LookupStandard(doc)
	if !ok || !valid || heading == "" {
		return "", "", "", ErrNotFound
	}
	return scope, info.Name, heading, nil
}

// encodeMeta 返回需要写入小节首行的元数据注释；与文档默认类型一致且没有
// 重要度与标签时返回空串，让文档保持干净。
func encodeMeta(doc, unitType string, importance float64, labels []string) string {
	unit := strings.ToLower(strings.TrimSpace(unitType))
	if unit == "" {
		unit = docUnit(doc)
	}
	if unit == docUnit(doc) && importance <= 0 && len(labels) == 0 {
		return ""
	}
	raw, err := json.Marshal(sectionMeta{UnitType: unit, Importance: importance, Labels: labels})
	if err != nil {
		return ""
	}
	return metaPrefix + string(raw) + " -->"
}

// sectionBody 把元数据注释与小节正文拼成落盘内容。
func sectionBody(doc, unitType string, importance float64, labels []string, body string) string {
	meta := encodeMeta(doc, unitType, importance, labels)
	body = strings.TrimSpace(body)
	switch {
	case meta == "":
		return body
	case body == "":
		return meta
	default:
		return meta + "\n" + body
	}
}

// parseSectionBody 拆出小节正文首行的元数据注释；没有该行或格式不对时按普通正文处理。
func parseSectionBody(body string) (string, sectionMeta) {
	trimmed := strings.TrimSpace(body)
	line, rest, _ := strings.Cut(trimmed, "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, strings.TrimSpace(metaPrefix)) || !strings.HasSuffix(line, "-->") {
		return trimmed, sectionMeta{}
	}
	raw := strings.TrimSpace(strings.TrimPrefix(line, strings.TrimSpace(metaPrefix)))
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "-->"))
	var meta sectionMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return trimmed, sectionMeta{}
	}
	meta.UnitType = strings.ToLower(strings.TrimSpace(meta.UnitType))
	return strings.TrimSpace(rest), meta
}

func memory(scope, doc string, s Section) nmem.Memory {
	body, meta := parseSectionBody(s.Body)
	unit := meta.UnitType
	if unit == "" {
		unit = docUnit(doc)
	}
	return nmem.Memory{
		ID:         scope + ":" + doc + "#" + s.Heading,
		Title:      s.Heading,
		Content:    body,
		UnitType:   unit,
		Importance: meta.Importance,
		Labels:     meta.Labels,
		Space:      scope,
	}
}

// emptyMemory 构造一条只有 id、标题与类型的空记忆，用于「当前重点」这类占位记忆。
func emptyMemory(scope, doc, heading string) nmem.Memory {
	return nmem.Memory{
		ID:       scope + ":" + doc + "#" + heading,
		Title:    heading,
		UnitType: docUnit(doc),
		Space:    scope,
	}
}

// hasSection 报告文档里是否已有同名小节。
func hasSection(text, heading string) bool {
	for _, s := range ListSections(text) {
		if s.Heading == heading {
			return true
		}
	}
	return false
}

// matchAnyLabel 报告 labels 里是否有 want 中的任一标签（忽略大小写与首尾空白）。
func matchAnyLabel(labels, want []string) bool {
	for _, label := range labels {
		for _, target := range want {
			if strings.EqualFold(strings.TrimSpace(label), strings.TrimSpace(target)) {
				return true
			}
		}
	}
	return false
}

// Search 在项目和全局标准文档中按关键词检索小节。
func (d *Docs) Search(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
	words := strings.Fields(strings.ToLower(q.Query))
	wantUnit := strings.ToLower(strings.TrimSpace(q.UnitType))
	var result []nmem.Memory
	for _, scope := range []string{"project", "global"} {
		if scope == "project" && strings.TrimSpace(projectPath) == "" {
			continue
		}
		// 没有全局 home 时只查项目层，不把配置缺失变成检索失败。
		if scope == "global" && strings.TrimSpace(d.globalHome) == "" {
			continue
		}
		svc, err := d.service(scope, projectPath)
		if err != nil {
			return nil, err
		}
		for _, info := range projctx.StandardDocs() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			doc, err := svc.ReadRef(info.Name)
			if errors.Is(err, projctx.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, s := range ListSections(doc.Content) {
				m := memory(scope, info.Name, s)
				if wantUnit != "" && strings.ToLower(strings.TrimSpace(m.UnitType)) != wantUnit {
					continue
				}
				// nmem 的 filter_labels 语义由服务端决定，本地按「命中任一标签」处理。
				if len(q.Labels) > 0 && !matchAnyLabel(m.Labels, q.Labels) {
					continue
				}
				title, body := strings.ToLower(m.Title), strings.ToLower(m.Content)
				matched, titleHits := 0, 0
				for _, word := range words {
					if strings.Contains(title, word) {
						matched++
						titleHits++
					} else if strings.Contains(body, word) {
						matched++
					}
				}
				if len(words) > 0 && matched == 0 {
					continue
				}
				if len(words) > 0 {
					m.Score = float64(matched)/float64(len(words)) + 0.25*float64(titleHits)/float64(len(words))
					if matched == len(words) {
						m.Score += 1
					}
				}
				result = append(result, m)
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Get 读取指定的小节；「当前重点」缺文档或小节时返回空内容而不是错误。
func (d *Docs) Get(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
	if err := ctx.Err(); err != nil {
		return nmem.Memory{}, err
	}
	scope, name, heading, err := parseID(id)
	if err != nil {
		return nmem.Memory{}, err
	}
	focus := scope+":"+name+"#"+heading == overviewFocusID
	svc, err := d.service(scope, projectPath)
	if err != nil {
		return nmem.Memory{}, err
	}
	doc, err := svc.ReadRef(name)
	if errors.Is(err, projctx.ErrNotFound) {
		if focus {
			return emptyMemory(scope, name, heading), nil
		}
		return nmem.Memory{}, ErrNotFound
	}
	if err != nil {
		return nmem.Memory{}, err
	}
	for _, s := range ListSections(doc.Content) {
		if s.Heading == heading {
			return memory(scope, name, s), nil
		}
	}
	if focus {
		return emptyMemory(scope, name, heading), nil
	}
	return nmem.Memory{}, ErrNotFound
}

// Forget 删除指定的小节，并在写入冲突时重试一次。
func (d *Docs) Forget(ctx context.Context, projectPath, id string) (bool, error) {
	scope, name, heading, err := parseID(id)
	if err != nil {
		return false, err
	}
	svc, err := d.service(scope, projectPath)
	if err != nil {
		return false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		doc, err := svc.ReadRef(name)
		if errors.Is(err, projctx.ErrNotFound) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, err
		}
		next := RemoveSection(doc.Content, heading)
		if next == doc.Content {
			return false, ErrNotFound
		}
		_, err = svc.WriteRef(name, next, doc.Hash)
		if errors.Is(err, projctx.ErrConflict) && attempt == 0 {
			continue
		}
		return err == nil, err
	}
	return false, projctx.ErrConflict
}

// Commit 将已接受的提议写入标准文档。
func (d *Docs) Commit(ctx context.Context, p store.MemoryProposal) (store.MemoryProposal, string, error) {
	scope := "project"
	if p.Scope == "global" {
		scope = "global"
	}
	name, heading := docForType(p.UnitType), strings.TrimSpace(p.Title)
	target := p.TargetMemoryID != ""
	if target {
		targetScope, targetDoc, targetHeading, err := parseID(p.TargetMemoryID)
		if err != nil {
			return p, "", err
		}
		// 改写写回记忆原来所在的层，忽略提议里可能不一致的 scope。
		scope, name, heading = targetScope, targetDoc, targetHeading
	}
	if heading == "" || strings.ContainsAny(heading, "\r\n") {
		return p, "", errors.New("localmem: invalid heading")
	}
	focus := target && scope+":"+name+"#"+heading == overviewFocusID
	svc, err := d.service(scope, p.ProjectPath)
	if err != nil {
		return p, "", err
	}
	body := sectionBody(name, p.UnitType, p.Importance, p.Labels, p.Content)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return p, "", err
		}
		doc, err := svc.ReadRef(name)
		if errors.Is(err, projctx.ErrNotFound) && (!target || focus) {
			doc, err = svc.CreateDoc(name)
			if errors.Is(err, os.ErrExist) {
				doc, err = svc.ReadRef(name)
			}
		}
		if errors.Is(err, projctx.ErrNotFound) {
			return p, "", ErrNotFound
		}
		if err != nil {
			return p, "", err
		}
		content, remoteHeading := doc.Content, heading
		switch {
		case target && !focus && !hasSection(doc.Content, heading):
			return p, "", ErrNotFound
		case target && !focus:
			if want := strings.TrimSpace(p.Title); want != "" && want != heading {
				if hasSection(doc.Content, want) {
					return p, "", fmt.Errorf("localmem: heading %q already exists", want)
				}
				content = ReplaceSection(content, heading, want, body)
				remoteHeading = want
			} else {
				content = UpsertSection(content, heading, body)
			}
		default:
			content = UpsertSection(content, heading, body)
		}
		_, err = svc.WriteRef(name, content, doc.Hash)
		if errors.Is(err, projctx.ErrConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			return p, "", err
		}
		p.RemoteMemoryID = scope + ":" + name + "#" + remoteHeading
		return p, scope, nil
	}
	return p, "", projctx.ErrConflict
}

// GlobalGuidance 将全局 instructions 包装为上下文块；只剩模板说明时返回空串。
func (d *Docs) GlobalGuidance(maxRunes int) string {
	svc, err := d.service("global", "")
	if err != nil {
		return ""
	}
	doc, err := svc.ReadRef(projctx.DocInstructions)
	if err != nil {
		return ""
	}
	text := guidanceBody(doc.Content)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "</global_context>", "&lt;/global_context&gt;")
	if maxRunes > 0 && len([]rune(text)) > maxRunes {
		return "<global_context>\n" + truncateGuidance(text, maxRunes) + "\n（已截断）\n</global_context>"
	}
	return "<global_context>\n" + text + "\n</global_context>"
}

// guidanceBody 去掉元数据行与模板占位行；剩下的只有标题和模板说明时返回空串。
func guidanceBody(content string) string {
	templates := instructionsTemplateLines()
	kept := make([]string, 0, 16)
	real := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, strings.TrimSpace(metaPrefix)) || strings.HasPrefix(trimmed, placeholderPrefix) {
			continue
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !templates[trimmed] {
			real = true
		}
		kept = append(kept, line)
	}
	if !real {
		return ""
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// instructionsTemplateLines 返回 instructions 模板里除示例行外的非空行集合，
// 用来判断一篇内容是否还只是模板说明、没有真实规则。
func instructionsTemplateLines() map[string]bool {
	lines := map[string]bool{}
	info, ok := projctx.LookupStandard(projctx.DocInstructions)
	if !ok {
		return lines
	}
	for _, line := range strings.Split(info.Template, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, placeholderPrefix) {
			continue
		}
		lines[trimmed] = true
	}
	return lines
}

// truncateGuidance 按 rune 截断到 limit 以内，换行落在预算后半段时退到该换行，避免截出半行。
func truncateGuidance(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return strings.TrimRight(text, "\n")
	}
	head := runes[:limit]
	for i := len(head) - 1; i >= len(head)/2; i-- {
		if head[i] == '\n' {
			return strings.TrimRight(string(head[:i]), "\n")
		}
	}
	return strings.TrimRight(string(head), "\n")
}
