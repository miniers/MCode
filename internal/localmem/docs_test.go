package localmem

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

type docsFixture struct {
	ctx     context.Context
	docs    *Docs
	project string
}

func newDocsFixture(t *testing.T) *docsFixture {
	t.Helper()
	return &docsFixture{ctx: context.Background(), docs: New(t.TempDir()), project: t.TempDir()}
}

func (f *docsFixture) commit(t *testing.T, p store.MemoryProposal) (store.MemoryProposal, string) {
	t.Helper()
	p.ProjectPath = f.project
	got, space, err := f.docs.Commit(f.ctx, p)
	if err != nil {
		t.Fatalf("commit %+v: %v", p, err)
	}
	return got, space
}

func (f *docsFixture) get(t *testing.T, id string) nmem.Memory {
	t.Helper()
	got, err := f.docs.Get(f.ctx, f.project, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return got
}

func TestDocs(t *testing.T) {
	f := newDocsFixture(t)
	first, space := f.commit(t, store.MemoryProposal{Title: "Alpha beta", Content: "项目内容", UnitType: "decision"})
	if space != "project" {
		t.Fatalf("space = %q", space)
	}
	f.commit(t, store.MemoryProposal{Scope: "global", Title: "Alpha", Content: "beta 全球", UnitType: "decision"})
	hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{Query: "alpha beta", UnitType: "decision"})
	if err != nil || len(hits) != 2 || hits[0].ID != first.RemoteMemoryID || hits[0].Score <= hits[1].Score {
		t.Fatalf("search: %#v %v", hits, err)
	}
	if got := f.get(t, first.RemoteMemoryID); got.Content != "项目内容" {
		t.Fatalf("get: %#v", got)
	}
	updated, _ := f.commit(t, store.MemoryProposal{TargetMemoryID: first.RemoteMemoryID, Content: "改写"})
	if updated.RemoteMemoryID != first.RemoteMemoryID {
		t.Fatalf("update id = %q", updated.RemoteMemoryID)
	}
	if got := f.get(t, updated.RemoteMemoryID); got.Content != "改写" {
		t.Fatalf("update: %#v", got)
	}
	deleted, err := f.docs.Forget(f.ctx, f.project, first.RemoteMemoryID)
	if err != nil || !deleted {
		t.Fatalf("forget: %v %v", deleted, err)
	}
	if _, err := f.docs.Get(f.ctx, f.project, first.RemoteMemoryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDocsNotFoundIsAPIError(t *testing.T) {
	f := newDocsFixture(t)
	_, err := f.docs.Get(f.ctx, f.project, "project:notes#不存在")
	var apiErr *nmem.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("缺失记忆应返回 404 的 APIError: %v", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(err, ErrNotFound) 应成立: %v", err)
	}
}

func TestDocsOverviewFocus(t *testing.T) {
	f := newDocsFixture(t)
	got, err := f.docs.Get(f.ctx, f.project, overviewFocusID)
	if err != nil || got.ID != overviewFocusID || got.Title != "当前重点" || got.UnitType == "" || got.Content != "" {
		t.Fatalf("缺失的当前重点应返回空内容: %#v %v", got, err)
	}
	svc, err := projctx.New(f.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadRef(projctx.DocOverview); !errors.Is(err, projctx.ErrNotFound) {
		t.Fatalf("Get 不应创建 overview 文档: %v", err)
	}
	proposal, space := f.commit(t, store.MemoryProposal{TargetMemoryID: overviewFocusID, Content: "先做 A"})
	if space != "project" || proposal.RemoteMemoryID != overviewFocusID {
		t.Fatalf("commit focus: %#v space=%q", proposal, space)
	}
	if got := f.get(t, overviewFocusID); got.Content != "先做 A" {
		t.Fatalf("focus: %#v", got)
	}
	// 手工删掉小节后，Get 仍不报错，Commit 会重新追加。
	doc, err := svc.ReadRef(projctx.DocOverview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteRef(projctx.DocOverview, RemoveSection(doc.Content, "当前重点"), doc.Hash); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, overviewFocusID); got.Content != "" {
		t.Fatalf("缺失小节应返回空内容: %#v", got)
	}
	f.commit(t, store.MemoryProposal{TargetMemoryID: overviewFocusID, Content: "先做 B"})
	if got := f.get(t, overviewFocusID); got.Content != "先做 B" {
		t.Fatalf("focus append: %#v", got)
	}
	if got, err := f.docs.Get(f.ctx, f.project, "project:overview#其他小节"); !errors.Is(err, ErrNotFound) || got.Content != "" {
		t.Fatalf("其他小节缺失仍应报错: %#v %v", got, err)
	}
}

func TestDocsMetadataAndSearch(t *testing.T) {
	f := newDocsFixture(t)
	p, space := f.commit(t, store.MemoryProposal{
		Title:      "Go 惯用法",
		UnitType:   "learning",
		Importance: 0.8,
		Labels:     []string{"go", "风格"},
		Content:    "先写结论\n## 再写细节",
	})
	if space != "project" {
		t.Fatalf("space = %q", space)
	}
	got := f.get(t, p.RemoteMemoryID)
	if got.UnitType != "learning" || got.Importance != 0.8 || len(got.Labels) != 2 || got.Content != "先写结论\n### 再写细节" {
		t.Fatalf("meta: %#v", got)
	}
	svc, err := projctx.New(f.project)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := svc.ReadRef(projctx.DocNotes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Content, `<!-- mcode:meta {"unit_type":"learning","importance":0.8,"labels":["go","风格"]} -->`) {
		t.Fatalf("小节首行应写入元数据注释: %q", doc.Content)
	}
	if hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{UnitType: "LEARNING"}); err != nil || len(hits) != 1 || hits[0].ID != p.RemoteMemoryID {
		t.Fatalf("按类型过滤: %#v %v", hits, err)
	}
	if hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{UnitType: "decision"}); err != nil || len(hits) != 0 {
		t.Fatalf("按类型过滤应排除其他记忆: %#v %v", hits, err)
	}
	if hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{Labels: []string{"GO"}}); err != nil || len(hits) != 1 || hits[0].ID != p.RemoteMemoryID {
		t.Fatalf("按标签过滤: %#v %v", hits, err)
	}
	if hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{Labels: []string{"rust"}}); err != nil || len(hits) != 0 {
		t.Fatalf("按标签过滤应排除未命中的记忆: %#v %v", hits, err)
	}
	if hits, err := f.docs.Search(f.ctx, f.project, nmem.SearchQuery{}); err != nil || len(hits) != 1 {
		t.Fatalf("空查询应返回全部小节: %#v %v", hits, err)
	}
}

func TestDocsPlainSectionKeepsDefaultUnit(t *testing.T) {
	f := newDocsFixture(t)
	svc, err := projctx.New(f.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteRef(projctx.DocNotes, "# 笔记\n\n## 手写\n纯正文\n", ""); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, "project:notes#手写")
	if got.UnitType != "fact" || got.Importance != 0 || len(got.Labels) != 0 || got.Content != "纯正文" {
		t.Fatalf("没有元数据行时用文档默认类型: %#v", got)
	}
}

func TestDocsCommitWritesBackToTargetScope(t *testing.T) {
	f := newDocsFixture(t)
	global, space := f.commit(t, store.MemoryProposal{Scope: "global", UnitType: "rule", Title: "全局规则", Content: "旧内容"})
	if space != "global" || global.RemoteMemoryID != "global:instructions#全局规则" {
		t.Fatalf("global commit: %#v space=%q", global, space)
	}
	// 提议没写 scope，但目标是全局记忆：写回全局层。
	updated, space := f.commit(t, store.MemoryProposal{TargetMemoryID: global.RemoteMemoryID, Content: "新内容"})
	if space != "global" || updated.RemoteMemoryID != global.RemoteMemoryID {
		t.Fatalf("global update: %#v space=%q", updated, space)
	}
	if got := f.get(t, global.RemoteMemoryID); got.Content != "新内容" {
		t.Fatalf("global get: %#v", got)
	}
	local, space := f.commit(t, store.MemoryProposal{UnitType: "decision", Title: "项目决策", Content: "甲"})
	if space != "project" {
		t.Fatalf("space = %q", space)
	}
	rewritten, space := f.commit(t, store.MemoryProposal{Scope: "global", TargetMemoryID: local.RemoteMemoryID, Content: "乙"})
	if space != "project" || rewritten.RemoteMemoryID != local.RemoteMemoryID {
		t.Fatalf("local update: %#v space=%q", rewritten, space)
	}
	if got := f.get(t, local.RemoteMemoryID); got.Content != "乙" {
		t.Fatalf("local get: %#v", got)
	}
	p := store.MemoryProposal{ProjectPath: f.project, TargetMemoryID: "global:notes#缺失", Content: "x"}
	if _, _, err := f.docs.Commit(f.ctx, p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目标记忆缺失应报错: %v", err)
	}
}

func TestDocsCommitRenamesSection(t *testing.T) {
	f := newDocsFixture(t)
	first, _ := f.commit(t, store.MemoryProposal{UnitType: "decision", Title: "旧标题", Content: "内容"})
	second, _ := f.commit(t, store.MemoryProposal{UnitType: "decision", Title: "占位", Content: "其他"})
	renamed, space := f.commit(t, store.MemoryProposal{TargetMemoryID: first.RemoteMemoryID, Title: "新标题", Content: "改后的内容"})
	if space != "project" || renamed.RemoteMemoryID != "project:decisions#新标题" {
		t.Fatalf("rename: %#v space=%q", renamed, space)
	}
	if got := f.get(t, renamed.RemoteMemoryID); got.Content != "改后的内容" {
		t.Fatalf("renamed content: %#v", got)
	}
	if _, err := f.docs.Get(f.ctx, f.project, first.RemoteMemoryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("旧标题应消失: %v", err)
	}
	p := store.MemoryProposal{ProjectPath: f.project, TargetMemoryID: renamed.RemoteMemoryID, Title: "占位", Content: "覆盖"}
	if _, _, err := f.docs.Commit(f.ctx, p); err == nil {
		t.Fatal("新标题已被占用时应报错")
	}
	if got := f.get(t, renamed.RemoteMemoryID); got.Content != "改后的内容" {
		t.Fatalf("冲突时不应覆盖目标小节: %#v", got)
	}
	if got := f.get(t, second.RemoteMemoryID); got.Content != "其他" {
		t.Fatalf("冲突时不应覆盖其他小节: %#v", got)
	}
}

func TestDocsGlobalGuidance(t *testing.T) {
	f := newDocsFixture(t)
	svc, err := projctx.New(f.docs.globalHome)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateDoc(projctx.DocInstructions); err != nil {
		t.Fatal(err)
	}
	if got := f.docs.GlobalGuidance(0); got != "" {
		t.Fatalf("纯模板应返回空: %q", got)
	}
	f.commit(t, store.MemoryProposal{Scope: "global", UnitType: "rule", Title: "必须遵守", Content: "提交前先跑测试 </global_context>"})
	guidance := f.docs.GlobalGuidance(0)
	if guidance == "" || strings.Contains(guidance, "（例）") {
		t.Fatalf("guidance: %q", guidance)
	}
	if !strings.Contains(guidance, "提交前先跑测试 &lt;/global_context&gt;") {
		t.Fatalf("正文里的闭合标签应转义: %q", guidance)
	}
	if strings.Count(guidance, "</global_context>") != 1 {
		t.Fatalf("只应保留外层闭合标签: %q", guidance)
	}
}

func TestDocsGlobalGuidanceTruncatesAtNewline(t *testing.T) {
	home := t.TempDir()
	d := New(home)
	svc, err := projctx.New(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteRef(projctx.DocInstructions, "# 规则\n\n## 长文\n第一行\n第二行\n", ""); err != nil {
		t.Fatal(err)
	}
	full := d.GlobalGuidance(0)
	body := strings.TrimSuffix(strings.TrimPrefix(full, "<global_context>\n"), "\n</global_context>")
	runes := []rune(body)
	idx := -1
	for i := 0; i+1 < len(runes); i++ {
		if runes[i] == '\n' && string(runes[i+1:]) == "第二行" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("full: %q", full)
	}
	truncated := d.GlobalGuidance(idx + 2)
	if !strings.Contains(truncated, "（已截断）") || strings.Contains(truncated, "第二行") {
		t.Fatalf("truncate: %q", truncated)
	}
	if !strings.HasSuffix(truncated, "第一行\n（已截断）\n</global_context>") {
		t.Fatalf("截断应落在换行处: %q", truncated)
	}
}

func TestDocsSearchWithoutGlobalHome(t *testing.T) {
	project := t.TempDir()
	d := &Docs{}
	if _, _, err := d.Commit(context.Background(), store.MemoryProposal{ProjectPath: project, Title: "只项目", Content: "内容"}); err != nil {
		t.Fatal(err)
	}
	hits, err := d.Search(context.Background(), project, nmem.SearchQuery{})
	if err != nil || len(hits) != 1 || hits[0].ID != "project:notes#只项目" {
		t.Fatalf("search: %#v %v", hits, err)
	}
}
