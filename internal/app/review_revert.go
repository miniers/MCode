package app

import (
	"context"
	"errors"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// ReviewRevertHunk 将审阅块撤销请求转发给后端。
func (c *Client) ReviewRevertHunk(ctx context.Context, p mcodeui.ReviewRevertHunkParams) (mcodeui.ReviewRevertHunkResult, error) {
	if c.backend == nil {
		return mcodeui.ReviewRevertHunkResult{}, errors.New("review backend is not available")
	}
	res, err := c.backend.ReviewRevertHunk(ctx, wire.ThreadsReviewRevertHunkParams{ThreadID: p.ThreadID, Scope: wire.ReviewScope(p.Scope), TurnID: p.TurnID, Path: p.Path, BaseRevision: p.BaseRevision, HeadRevision: p.HeadRevision, StartLine: p.StartLine, LineCount: p.LineCount, HunkHash: p.HunkHash, ExpectedFileHash: p.ExpectedFileHash})
	if err != nil {
		return mcodeui.ReviewRevertHunkResult{}, err
	}
	return mcodeui.ReviewRevertHunkResult{FileHash: res.FileHash}, nil
}
