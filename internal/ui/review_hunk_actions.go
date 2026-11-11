package mcodeui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type reviewHunkActionState struct {
	mu            sync.Mutex
	open, pending bool
	err           string
}

var reviewHunkStates sync.Map

func reviewHunkState(key string) *reviewHunkActionState {
	v, _ := reviewHunkStates.LoadOrStore(key, &reviewHunkActionState{})
	return v.(*reviewHunkActionState)
}

func reviewContentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func configureReviewHunkActions(c *ui.Context, s *chatState, dp *diffPanelState, tid string, snapshot ReviewSnapshot, file ReviewFile, content ReviewFileContent, view *reviewDiffView) {
	client, ok := s.client.(reviewHunkReverter)
	if !ok || snapshot.Running || file.Binary || file.OrigPath != "" || file.Status == "A" || file.Status == "D" || file.Status == "R" || content.Binary || content.TooLarge || content.Missing {
		view.state.RenderHunkActions = nil
		return
	}
	view.state.RenderHunkActions = func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
		start, count := a.Hunk.NewStart+1, a.Hunk.NewEnd-a.Hunk.NewStart
		hash, err := reviewHunkHash(content.NewText, start, count)
		if err != nil {
			return
		}
		state := reviewHunkState(fmt.Sprintf("%p-%d", view, a.Index))
		state.mu.Lock()
		open, pending, actionErr := state.open, state.pending, state.err
		state.mu.Unlock()
		btn := ui.ButtonBase(c.Key(fmt.Sprintf("review-revert-hunk-%d", a.Index))).Label("撤销此块").Padding(Space1, Space2).Radius(RadiusXS).Disabled(pending)
		btn.Children(func() { ui.Icon(c, iconRotateCcw).Size(IconSM, IconSM) })
		if btn.Clicked() && !pending {
			state.mu.Lock()
			state.open, state.err = true, ""
			state.mu.Unlock()
			c.Invalidate()
		}
		RenderConfirmDialog(c, ConfirmDialog{Key: fmt.Sprintf("review-revert-confirm-%p-%d", view, a.Index), Open: open, Title: "撤销此块？", ConfirmLabel: "撤销", CancelLabel: "取消", ConfirmDisabled: pending,
			OnCancel: func() { state.mu.Lock(); state.open = false; state.mu.Unlock() },
			OnConfirm: func() {
				state.mu.Lock()
				state.open, state.pending, state.err = false, true, ""
				state.mu.Unlock()
				s.invalidate()
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					_, callErr := client.ReviewRevertHunk(ctx, ReviewRevertHunkParams{ThreadID: tid, Scope: snapshot.Scope, TurnID: snapshot.TurnID, Path: file.Path, BaseRevision: snapshot.BaseRevision, HeadRevision: snapshot.HeadRevision, StartLine: start, LineCount: count, HunkHash: hash, ExpectedFileHash: reviewContentHash(content.NewText)})
					state.mu.Lock()
					if callErr != nil {
						state.err = callErr.Error()
					} else {
						state.err = ""
					}
					state.pending = false
					state.mu.Unlock()
					if callErr == nil {
						fileTreeInvalidate(s, tid, nil)
						s.loadDiff(tid, snapshot.TurnID)
					}
					s.invalidate()
				}()
			}, Body: "仅撤销当前改动块。"})
		if actionErr != "" {
			ui.Text(c, actionErr).FontSize(TypeCaption).TextColor(ThemeDanger(c))
		}
	}
}

func reviewHunkHash(text string, start, count int) (string, error) {
	return git.RevertHunkHash(text, start, count)
}
