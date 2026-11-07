package localmem

import (
	"context"
	"errors"

	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

type Threads struct{ Store *store.Store }

// projectPaths 返回与 projectPath 属于同一主 checkout 的全部项目路径（含 projectPath 本身，已去重），
// 使主 checkout 与各 worktree 下记录的线程互相可检索。
func (t Threads) projectPaths(ctx context.Context, projectPath string) ([]string, error) {
	projects, err := t.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	main := projctx.MainCheckout(projectPath)
	paths := []string{projectPath}
	seen := map[string]bool{projectPath: true}
	for _, p := range projects {
		if projctx.MainCheckout(p.Path) != main || seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		paths = append(paths, p.Path)
	}
	return paths, nil
}

func (t Threads) Search(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
	paths, err := t.projectPaths(ctx, projectPath)
	if err != nil {
		return nil, err
	}
	hits, err := t.Store.SearchThreadMessages(ctx, paths, query, limit)
	if err != nil {
		return nil, err
	}
	var out []nmem.ThreadHit
	for _, hit := range hits {
		h := nmem.ThreadHit{ThreadID: hit.ThreadID, Title: hit.Title, Source: "mcode", MessageCount: hit.MessageCount, LastActivity: hit.LastActivity}
		for _, match := range hit.Matches {
			h.Matches = append(h.Matches, nmem.ThreadMatch{Index: match.Offset, Role: match.Role, Snippet: match.Snippet})
		}
		out = append(out, h)
	}
	return out, nil
}

func (t Threads) Fetch(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error) {
	paths, err := t.projectPaths(ctx, projectPath)
	if err != nil {
		return nmem.ThreadPage{}, err
	}
	title, total, msgs, err := t.Store.ThreadMessagesPage(ctx, paths, threadID, offset, limit)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nmem.ThreadPage{}, ErrNotFound
		}
		return nmem.ThreadPage{}, err
	}
	if offset < 0 {
		offset = 0
	}
	page := nmem.ThreadPage{ThreadID: threadID, Title: title, Total: total, Offset: offset}
	for _, msg := range msgs {
		page.Messages = append(page.Messages, nmem.Message{Role: msg.Role, Content: msg.Text, Timestamp: msg.CreatedAt})
	}
	return page, nil
}
