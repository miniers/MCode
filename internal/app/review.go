package app

import (
	"context"
	"errors"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// ReviewGet 通过后端按作用域取得审阅快照。
func (c *Client) ReviewGet(ctx context.Context, threadID, scope, turnID, path string) (mcodeui.ReviewSnapshot, error) {
	if c.backend == nil {
		return mcodeui.ReviewSnapshot{}, errors.New("review backend is not available")
	}
	res, err := c.backend.ReviewGet(ctx, wire.ThreadsReviewGetParams{ThreadID: threadID, Scope: wire.ReviewScope(scope), TurnID: turnID, Path: path})
	if err != nil {
		return mcodeui.ReviewSnapshot{}, err
	}
	return reviewSnapshotFromWire(res), nil
}

func reviewSnapshotFromWire(res wire.ThreadsReviewGetResult) mcodeui.ReviewSnapshot {
	result := mcodeui.ReviewSnapshot{Scope: string(res.Scope), TurnID: res.TurnID, BaseRevision: res.BaseRevision, HeadRevision: res.HeadRevision, Running: res.Running, Unavailable: res.Unavailable, Files: make([]mcodeui.ReviewFile, len(res.Files))}
	for i, f := range res.Files {
		result.Files[i] = mcodeui.ReviewFile{Path: f.Path, OrigPath: f.OrigPath, Status: f.Status, Language: f.Language, OldSize: f.OldSize, NewSize: f.NewSize, OldHash: f.OldHash, NewHash: f.NewHash, Binary: f.Binary, TooLarge: f.TooLarge, Missing: f.Missing}
	}
	if f := res.File; f != nil {
		result.File = &mcodeui.ReviewFileContent{Path: f.Path, OldText: f.OldText, NewText: f.NewText, OldHash: f.OldHash, NewHash: f.NewHash, Binary: f.Binary, TooLarge: f.TooLarge, Missing: f.Missing}
	}
	return result
}
