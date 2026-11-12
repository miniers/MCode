package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type gitDiffClient interface {
	GitIndexDiff(context.Context, string, string, bool) ([]git.FileDiff, error)
	GitStageFiles(context.Context, string, []string) error
	GitUnstageFiles(context.Context, string, []string) error
	GitStageLines(context.Context, string, string, []git.LineSel) error
	GitUnstageLines(context.Context, string, string, []git.LineSel) error
	GitDiscardLines(context.Context, string, string, []git.LineSel) (*git.Snapshot, error)
	GitResolveFileSide(context.Context, string, string, bool) error
	GitMarkResolved(context.Context, string, []string) error
	GitWriteResolved(context.Context, string, string, []byte) error
}

const gitDiffMaxBytes = 2 << 20
const gitDiffSplitWidth float32 = 560

type gitDiffKey struct {
	threadID, root, path string
	staged, conflict     bool
}

type gitDiffResult struct {
	file     git.FileDiff
	conflict *gitConflictState
	err      error
	large    bool
}

type gitDiffViewState struct {
	mu                                   sync.Mutex
	key                                  gitDiffKey
	seq, refreshSeq                      uint64
	loadedDuringRefresh                  bool
	loading, showLoading, dirty, pending bool
	cancel                               context.CancelFunc
	result                               *gitDiffResult
	clearSelection                       bool
	// editor 与浮层状态只由 UI 线程读写；后台只发布不可变的加载结果。
	applied         *gitDiffResult
	appliedKey      gitDiffKey
	view            *editor.DiffState
	split, menuOpen bool
	conflictScroll  ui.ScrollState
}

var gitDiffStates sync.Map // *gitPaneState → *gitDiffViewState

// shortcut: 面板销毁时不清理，按会话数量级可忽略；若会话量很大再接到 dispose
func gitDiffStateFor(ps *gitPaneState) *gitDiffViewState {
	v, _ := gitDiffStates.LoadOrStore(ps, &gitDiffViewState{})
	return v.(*gitDiffViewState)
}

func appendGitLineSelection(out []git.LineSel, old bool, no int) []git.LineSel {
	if no < 1 {
		return out
	}
	if n := len(out); n > 0 && out[n-1].Old == old && out[n-1].End+1 == no {
		out[n-1].End = no
		return out
	}
	return append(out, git.LineSel{Old: old, Start: no, End: no})
}

func hunkLineSelections(h git.Hunk) []git.LineSel {
	var out []git.LineSel
	for _, l := range h.Lines {
		if l.Kind == '-' {
			out = appendGitLineSelection(out, true, l.OldNo)
		}
		if l.Kind == '+' {
			out = appendGitLineSelection(out, false, l.NewNo)
		}
	}
	return out
}

// SetPatch 的选择回调返回片段 Buffer 的零基坐标，必须先映射回文件真实行号。
func diffRangeSelections(f git.FileDiff, r editor.DiffLineRange) []git.LineSel {
	var out []git.LineSel
	if r.Start < 0 || r.End < r.Start {
		return out
	}
	index := 0
	old := r.Side == editor.DiffOld
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind != ' ' && l.Kind != '-' && l.Kind != '+' {
				continue
			}
			if old && l.Kind == '+' || !old && l.Kind == '-' {
				continue
			}
			if index >= r.Start && index <= r.End {
				if old && l.Kind == '-' {
					out = appendGitLineSelection(out, true, l.OldNo)
				}
				if !old && l.Kind == '+' {
					out = appendGitLineSelection(out, false, l.NewNo)
				}
			}
			index++
		}
	}
	return out
}

func diffHunkSelections(f git.FileDiff, h editor.DiffHunk) []git.LineSel {
	old, next := 0, 0
	for _, patchHunk := range f.Hunks {
		oldEnd, newEnd := old, next
		for _, l := range patchHunk.Lines {
			if l.Kind == ' ' || l.Kind == '-' {
				oldEnd++
			}
			if l.Kind == ' ' || l.Kind == '+' {
				newEnd++
			}
		}
		if h.OldStart < oldEnd && h.OldEnd > old || h.NewStart < newEnd && h.NewEnd > next {
			return hunkLineSelections(patchHunk)
		}
		old, next = oldEnd, newEnd
	}
	return nil
}

func gitFilePatch(f git.FileDiff) editor.FilePatch {
	p := editor.FilePatch{OldName: f.OldPath, NewName: f.NewPath, Binary: f.Binary}
	for _, h := range f.Hunks {
		section := ""
		if i := strings.Index(h.Header, " @@"); i >= 0 {
			section = strings.TrimSpace(h.Header[i+3:])
		}
		ph := editor.PatchHunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Section: section}
		for _, l := range h.Lines {
			ph.Lines = append(ph.Lines, string(l.Kind)+l.Text)
		}
		p.Hunks = append(p.Hunks, ph)
	}
	return p
}

func gitDiffTooLarge(f git.FileDiff) bool {
	if f.IsDeferred || len(f.DeferredPatch) > gitDiffMaxBytes {
		return true
	}
	n := 0
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			n += len(l.Text) + 1
			if n > gitDiffMaxBytes {
				return true
			}
		}
	}
	return false
}

func gitDiffContext(ps *gitPaneState) (context.Context, context.CancelFunc) {
	ps.mu.Lock()
	parent := ps.lifeCtx
	ps.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 30*time.Second)
}

func gitDiffIdentity(ps *gitPaneState, tid, path string, staged bool) (gitDiffKey, uint64, bool, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	key := gitDiffKey{threadID: tid, root: ps.state.WorkDir, path: path, staged: staged}
	for _, e := range ps.entries {
		if e.Path == path {
			key.conflict = gitEntryConflicted(e)
			break
		}
	}
	return key, ps.refreshSeq, ps.refreshing, ps.disposed
}

func loadGitDiff(s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, refreshSeq uint64, refreshing bool) {
	st.mu.Lock()
	changed := st.key != key
	if !changed && (st.pending || st.loading || !st.dirty && st.result != nil && (refreshing || !st.loadedDuringRefresh && st.refreshSeq == refreshSeq)) {
		st.mu.Unlock()
		return
	}
	if st.cancel != nil {
		st.cancel()
	}
	ctx, cancel := gitDiffContext(ps)
	st.cancel = cancel
	st.key, st.refreshSeq, st.seq = key, refreshSeq, st.seq+1
	st.loadedDuringRefresh = refreshing
	st.loading, st.showLoading, st.dirty = true, false, false
	if changed {
		st.result = nil
	}
	seq := st.seq
	st.mu.Unlock()
	go func() {
		defer cancel()
		timer := time.AfterFunc(150*time.Millisecond, func() {
			st.mu.Lock()
			if st.seq == seq && st.loading {
				st.showLoading = true
			}
			st.mu.Unlock()
			s.invalidate()
		})
		defer timer.Stop()
		result := &gitDiffResult{}
		if key.conflict {
			result.conflict = readGitConflict(key.root, key.path)
		} else {
			var files []git.FileDiff
			files, result.err = client.GitIndexDiff(ctx, key.threadID, key.path, key.staged)
			if len(files) > 0 {
				result.file = files[0]
			}
			if info, err := os.Stat(filepath.Join(key.root, filepath.FromSlash(key.path))); err == nil {
				result.large = info.Size() > gitDiffMaxBytes
			}
		}
		if ctx.Err() != nil {
			result.err = ctx.Err()
			if result.conflict != nil {
				result.conflict.err = ctx.Err()
			}
		}
		st.mu.Lock()
		if st.seq == seq {
			st.result, st.loading, st.showLoading = result, false, false
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

func renderGitPaneDiff(c *ui.Context, s *chatState, threadID string, ps *gitPaneState, path string, staged bool) {
	if ps == nil || s == nil || path == "" {
		return
	}
	client, ok := s.client.(gitDiffClient)
	if !ok {
		renderGitPaneEmpty(c, "Git 操作不可用", IconGitBranch())
		return
	}
	key, refreshSeq, refreshing, disposed := gitDiffIdentity(ps, threadID, path, staged)
	if disposed {
		return
	}
	st := gitDiffStateFor(ps)
	loadGitDiff(s, ps, st, client, key, refreshSeq, refreshing)
	st.mu.Lock()
	result, loading, showLoading, pending, clearSelection := st.result, st.loading, st.showLoading, st.pending, st.clearSelection
	st.clearSelection = false
	st.mu.Unlock()
	if clearSelection && st.view != nil {
		st.view.SetSelectedLines(editor.DiffLineRange{}, false)
	}
	if st.applied != result {
		sameKey := st.appliedKey == key
		sameFile := sameKey && st.applied != nil && result != nil && reflect.DeepEqual(st.applied.file, result.file)
		if sameKey && st.applied != nil && result != nil && st.applied.conflict != nil && result.conflict != nil && st.applied.conflict.text == result.conflict.text {
			result.conflict.choices = st.applied.conflict.choices
		}
		if !sameFile {
			st.view = nil
		}
		st.applied, st.appliedKey, st.menuOpen = result, key, false
		if !sameKey {
			st.conflictScroll = ui.ScrollState{}
		}
		if st.view == nil && result != nil && !key.conflict && !result.file.Binary && !result.large && !gitDiffTooLarge(result.file) {
			st.view = &editor.DiffState{Layout: editor.DiffUnified, Inline: editor.DiffInlineWordAlt, Indicators: editor.DiffClassic, Font: ui.Font{Size: uiFont(TypeCode)}}
			st.view.SetPatch(gitFilePatch(result.file))
		}
	}
	container := ui.Column(c.Key("git-pane-diff")).Fill().MinWidth(0).MinHeight(0)
	width := container.Bounds().W
	container.Children(func() {
		if key.conflict {
			renderGitConflict(c, s, ps, st, client, key, result, loading || pending, width)
			if result == nil && showLoading {
				LoadingIndicator(c, "git-conflict-loading", "", IconMD)
			}
			return
		}
		f := git.FileDiff{}
		if result != nil {
			f = result.file
		}
		ui.Row(c.Key("git-diff-toolbar")).FillWidth().Height(Space8+Space3).Gap(Space2).AlignItems(ui.Center).Padding(0, Space3).Children(func() {
			gitDiffPath(c, path, width)
			adds, dels := f.Additions, f.Deletions
			if !f.IsDeferred {
				adds, dels = 0, 0
				for _, h := range f.Hunks {
					for _, l := range h.Lines {
						if l.Kind == '+' {
							adds++
						}
						if l.Kind == '-' {
							dels++
						}
					}
				}
			}
			ui.Text(c, fmt.Sprintf("+%d", adds)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSuccess(c))
			ui.Text(c, fmt.Sprintf("−%d", dels)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
			unified := Button(c, ButtonOpts{Key: "git-diff-unified", Text: "统一", Variant: ButtonGhost, Size: ControlSM, OnClick: func() { st.split = false }}).Selected(!st.split)
			if !st.split {
				unified.Background(ThemeSelected(c))
			}
			tip := ""
			if width < gitDiffSplitWidth {
				tip = "宽度不足 560"
			}
			split := Button(c, ButtonOpts{Key: "git-diff-split", Text: "并排", Variant: ButtonGhost, Size: ControlSM, Disabled: width < gitDiffSplitWidth, Tooltip: tip, OnClick: func() { st.split = true }}).Selected(st.split && width >= gitDiffSplitWidth)
			if st.split && width >= gitDiffSplitWidth {
				split.Background(ThemeSelected(c))
			}
			renderGitDiffMenu(c, s, ps, st, client, key, nil, loading || pending)
		})
		if result == nil {
			if showLoading {
				LoadingIndicator(c, "git-diff-loading", "", IconMD)
			}
			return
		}
		if result.err != nil {
			renderGitPaneEmpty(c, result.err.Error(), IconGitBranch())
			return
		}
		if f.Binary {
			renderGitPaneEmpty(c, "二进制文件", IconFile())
			return
		}
		if result.large || gitDiffTooLarge(f) {
			renderGitPaneEmpty(c, "文件过大", IconFile())
			label := "暂存整个文件"
			if staged {
				label = "取消暂存整个文件"
			}
			Button(c, ButtonOpts{Text: label, Disabled: loading || pending, OnClick: func() {
				runGitDiffWrite(s, ps, st, key, false, func(ctx context.Context) (*git.Snapshot, error) {
					if staged {
						return nil, client.GitUnstageFiles(ctx, threadID, []string{path})
					}
					return nil, client.GitStageFiles(ctx, threadID, []string{path})
				}, "")
			}})
			return
		}
		if len(f.Hunks) == 0 {
			renderGitPaneEmpty(c, "没有改动", IconFile())
			return
		}
		d := st.view
		if st.split && width >= gitDiffSplitWidth {
			d.Layout = editor.DiffSplit
		} else {
			d.Layout = editor.DiffUnified
		}
		d.Font.Size = uiFont(TypeCode)
		d.HunkActions, d.LineSelection = true, true
		d.OnLineSelection = func(editor.DiffLineRange) { s.invalidate() }
		d.RenderHunkActions = func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
			renderGitHunkActions(c, bar, s, ps, st, client, key, f, a.Hunk, loading || pending)
		}
		editor.DiffView(c.Key("git-diff-view"), d).Grow(1).FillWidth().MinHeight(0).Label("文件差异")
		if selection, has := d.SelectedLines(); has {
			container.OnShortcut(0, ui.KeyEscape, func() { d.SetSelectedLines(editor.DiffLineRange{}, false) })
			container.OnShortcut(0, ui.KeySpace, func() {
				if !loading && !pending {
					applyGitSelections(s, ps, st, client, key, diffRangeSelections(f, selection), false, false)
				}
			})
			if c.Shortcut(0, ui.KeyEscape) {
				d.SetSelectedLines(editor.DiffLineRange{}, false)
			} else {
				sels := diffRangeSelections(f, selection)
				if c.Shortcut(0, ui.KeySpace) && !loading && !pending {
					applyGitSelections(s, ps, st, client, key, sels, false, false)
				}
				renderGitSelectionBar(c, s, ps, st, client, key, sels, selection.End-selection.Start+1, loading || pending)
			}
		}
	})
}

func gitDiffPath(c *ui.Context, path string, width float32) {
	// 根据等宽字宽预留工具按钮空间，尾部路径始终可见。
	r := []rune(path)
	limit := max(8, int((width-300)/(uiFont(TypeCode)*0.65)))
	if len(r) > limit {
		path = "…" + string(r[len(r)-limit+1:])
	}
	ui.Text(c, path).Font("monospace").FontSize(uiFont(TypeCode)).SingleLine().Grow(1).MinWidth(0)
}

func renderGitHunkActions(c *ui.Context, bar ui.Element, s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, file git.FileDiff, h editor.DiffHunk, disabled bool) {
	sels := diffHunkSelections(file, h)
	bar.Row().Gap(Space1).Children(func() {
		label := "暂存此块"
		if key.staged {
			label = "取消暂存此块"
		}
		Button(c, ButtonOpts{Text: label, Size: ControlSM, Disabled: disabled, OnClick: func() { applyGitSelections(s, ps, st, client, key, sels, false, true) }})
		if !key.staged {
			busy := gitPaneAgentBusy(s, key.threadID)
			tip := "丢弃此块"
			if busy {
				tip = "Agent 正在修改此工作区"
			}
			Button(c, ButtonOpts{Text: "↺", Label: "丢弃此块", Tooltip: tip, Size: ControlSM, Disabled: disabled || busy, OnClick: func() { applyGitSelections(s, ps, st, client, key, sels, true, true) }})
		}
	})
}

func applyGitSelections(s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, sels []git.LineSel, discard, hunk bool) {
	label := "已丢弃所选行"
	if hunk {
		label = "已丢弃 1 块"
	}
	runGitDiffWrite(s, ps, st, key, discard, func(ctx context.Context) (*git.Snapshot, error) {
		if discard {
			return client.GitDiscardLines(ctx, key.threadID, key.path, sels)
		}
		if key.staged {
			return nil, client.GitUnstageLines(ctx, key.threadID, key.path, sels)
		}
		return nil, client.GitStageLines(ctx, key.threadID, key.path, sels)
	}, label)
}

// 在 UI 线程先占用写入槽，防止连点启动多个并发写；后台不接触编辑器状态。
func runGitDiffWrite(s *chatState, ps *gitPaneState, st *gitDiffViewState, key gitDiffKey, destructive bool, write func(context.Context) (*git.Snapshot, error), undoLabel string) {
	_, _, _, disposed := gitDiffIdentity(ps, key.threadID, key.path, key.staged)
	if disposed || destructive && gitPaneAgentBusy(s, key.threadID) {
		return
	}
	st.mu.Lock()
	if st.pending || st.key != key || st.loading {
		st.mu.Unlock()
		return
	}
	st.pending = true
	st.mu.Unlock()
	s.invalidate()
	go func() {
		ctx, cancel := gitDiffContext(ps)
		defer cancel()
		snap, err := write(ctx)
		if err != nil && !errors.Is(err, git.ErrEmptySelection) {
			ps.fail(err)
		}
		if err == nil && snap != nil {
			ps.showUndo(undoLabel, snap)
		}
		st.mu.Lock()
		st.pending = false
		if st.key == key && key.conflict {
			st.dirty = true
		}
		if st.key == key && (err == nil || errors.Is(err, git.ErrEmptySelection)) {
			st.dirty = true
			st.clearSelection = true
		}
		st.mu.Unlock()
		if err == nil {
			ps.requestRefresh()
		}
		s.invalidate()
	}()
}

func renderGitSelectionBar(c *ui.Context, s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, sels []git.LineSel, selectedCount int, disabled bool) {
	n := 0
	for _, sel := range sels {
		n += sel.End - sel.Start + 1
	}
	ui.Row(c.Key("git-diff-selection")).Radius(RadiusPill).Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)).Padding(Space2, Space3).Gap(Space2).AlignItems(ui.Center).Transition(motionPopover(c, motionRiseOffset)).Children(func() {
		ui.Text(c, fmt.Sprintf("已选 %d 行", selectedCount)).FontSize(uiFont(TypeFootnote))
		label := "暂存"
		if key.staged {
			label = "取消暂存"
		}
		Button(c, ButtonOpts{Text: label, Size: ControlSM, Disabled: disabled || n == 0, OnClick: func() { applyGitSelections(s, ps, st, client, key, sels, false, false) }})
		if !key.staged {
			busy := gitPaneAgentBusy(s, key.threadID)
			tip := ""
			if busy {
				tip = "Agent 正在修改此工作区"
			}
			Button(c, ButtonOpts{Text: "丢弃", Size: ControlSM, Disabled: disabled || busy || n == 0, Tooltip: tip, OnClick: func() { applyGitSelections(s, ps, st, client, key, sels, true, false) }})
		}
		Button(c, ButtonOpts{Text: "esc", Size: ControlSM, Variant: ButtonGhost, OnClick: func() { st.view.SetSelectedLines(editor.DiffLineRange{}, false) }})
	})
}

func renderGitDiffMenu(c *ui.Context, s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, conflict *gitConflictState, disabled bool) {
	trigger := Button(c, ButtonOpts{Text: "⋯", Label: "差异菜单", Variant: ButtonGhost, Size: ControlSM, OnClick: func() { st.menuOpen = true }})
	appPopover(c, trigger, &st.menuOpen, nil, func() {
		popoverCard(c, ui.Column(c)).Padding(Space1).Children(func() {
			Button(c, ButtonOpts{Text: "在编辑器打开", Variant: ButtonGhost, OnClick: func() {
				st.menuOpen = false
				openInDefaultEditor(s, filepath.Join(key.root, filepath.FromSlash(key.path)))
			}})
			if conflict != nil {
				renderGitConflictMenu(c, s, ps, st, client, key, conflict, disabled)
			}
		})
	})
}
