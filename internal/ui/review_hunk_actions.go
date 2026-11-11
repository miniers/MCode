package mcodeui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
	"weak"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type reviewHunkActionState struct {
	mu            sync.Mutex
	open, pending bool
	err           string
}

const reviewHunkStateLimit = 256

type reviewHunkStateKey struct {
	view  weak.Pointer[reviewDiffView]
	index int
}

var reviewHunkStates = struct {
	sync.Mutex
	entries map[reviewHunkStateKey]*reviewHunkActionState
	order   []reviewHunkStateKey
}{entries: make(map[reviewHunkStateKey]*reviewHunkActionState)}

func reviewHunkState(view *reviewDiffView, index int) *reviewHunkActionState {
	key := reviewHunkStateKey{view: weak.Make(view), index: index}
	reviewHunkStates.Lock()
	defer reviewHunkStates.Unlock()
	state := reviewHunkStates.entries[key]
	if state == nil {
		state = &reviewHunkActionState{}
		reviewHunkStates.entries[key] = state
	}
	for i, old := range reviewHunkStates.order {
		if old == key {
			reviewHunkStates.order = append(reviewHunkStates.order[:i], reviewHunkStates.order[i+1:]...)
			break
		}
	}
	reviewHunkStates.order = append(reviewHunkStates.order, key)
	// 弱引用不保留已淘汰的全文 Buffer；上限同时约束尚未 GC 的按钮状态。
	for len(reviewHunkStates.order) > reviewHunkStateLimit {
		delete(reviewHunkStates.entries, reviewHunkStates.order[0])
		reviewHunkStates.order = reviewHunkStates.order[1:]
	}
	return state
}

func reviewContentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func configureReviewHunkActions(c *ui.Context, s *chatState, dp *diffPanelState, tid string, snapshot ReviewSnapshot, file ReviewFile, content ReviewFileContent, view *reviewDiffView) {
	client, ok := s.client.(reviewHunkReverter)
	if !ok || snapshot.Running || file.Binary || file.OrigPath != "" || file.Status == "A" || file.Status == "D" || file.Status == "R" || content.Binary || content.TooLarge || content.Missing {
		view.state.HunkActions = false
		view.state.RenderHunkActions = nil
		return
	}
	view.state.HunkActions = true
	view.state.RenderHunkActions = func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
		state := reviewHunkState(view, a.Index)
		state.mu.Lock()
		pending := state.pending
		state.mu.Unlock()
		bar.Children(func() {
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("review-revert-hunk-%d", a.Index))).Label("撤销此块").Padding(Space1, Space2).Radius(RadiusXS).Disabled(pending)
			btn.Children(func() { ui.Icon(c, iconRotateCcw).Size(IconSM, IconSM) })
			if btn.Clicked() && !pending {
				state.mu.Lock()
				state.open, state.err = true, ""
				state.mu.Unlock()
				c.Invalidate()
			}
		})
	}
	// 编辑器只为活动块构建按钮；确认框和错误不能随鼠标离开改动块而消失。
	states := make(map[int]*reviewHunkActionState)
	viewKey := weak.Make(view)
	reviewHunkStates.Lock()
	for key, state := range reviewHunkStates.entries {
		if key.view == viewKey {
			states[key.index] = state
		}
	}
	reviewHunkStates.Unlock()
	for index, hunk := range view.state.Hunks() {
		state := states[index]
		if state == nil {
			continue
		}
		state.mu.Lock()
		open, pending, actionErr := state.open, state.pending, state.err
		state.mu.Unlock()
		start, count := hunk.NewStart+1, hunk.NewEnd-hunk.NewStart
		RenderConfirmDialog(c, ConfirmDialog{Key: fmt.Sprintf("review-revert-confirm-%p-%d", view, index), Open: open, Title: "撤销此块？", ConfirmLabel: "撤销", CancelLabel: "取消", ConfirmDisabled: pending,
			OnCancel: func() { state.mu.Lock(); state.open = false; state.mu.Unlock() },
			OnConfirm: func() {
				hash, err := reviewHunkHash(content.NewText, start, count)
				if err != nil {
					state.mu.Lock()
					state.open, state.err = false, err.Error()
					state.mu.Unlock()
					c.Invalidate()
					return
				}
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
