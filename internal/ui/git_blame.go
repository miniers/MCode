package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// gitBlameClient 是逐行追溯所需的最小后端能力。
type gitBlameClient interface {
	GitBlame(ctx context.Context, threadID, path, rev string) ([]git.BlameLine, error)
}

type gitBlameState struct {
	mu               sync.Mutex
	path             string
	lines            []git.BlameLine
	blocks           []git.BlameBlock
	blockByRow       []int
	list             ui.ListState
	loading          bool
	err              error
	seq              uint64
	hoverBlock       int
	hoverRow         int
	earliest, latest time.Time
	pending          bool
}

// shortcut: 面板销毁时不清理，按会话数量级可忽略；若会话量很大再接到 dispose。
var gitBlameStates sync.Map // *gitPaneState → *gitBlameState

func gitBlameStateFor(ps *gitPaneState) *gitBlameState {
	if ps == nil {
		return nil
	}
	st, _ := gitBlameStates.LoadOrStore(ps, &gitBlameState{hoverBlock: -1, hoverRow: -1})
	return st.(*gitBlameState)
}

// gitBlameTimeAlpha 把提交时间映射为新旧色条透明度。
func gitBlameTimeAlpha(earliest, latest, at time.Time) float32 {
	if !earliest.Before(latest) {
		return 1
	}
	ratio := float32(at.Sub(earliest)) / float32(latest.Sub(earliest))
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	return 0.15 + ratio*0.85
}

func renderGitBlameView(c *ui.Context, s *chatState, threadID string, ps *gitPaneState, path string, onOpenCommit func(sha string)) {
	st := gitBlameStateFor(ps)
	if st == nil || s == nil {
		return
	}
	st.mu.Lock()
	changed := st.path != path
	var seq uint64
	if changed {
		st.path, st.lines, st.blocks, st.blockByRow, st.err = path, nil, nil, nil, nil
		st.loading, st.pending = false, true
		st.seq++
		seq = st.seq
		st.list = ui.ListState{}
	}
	loading, err, lines, blocks := st.loading, st.err, st.lines, st.blocks
	st.mu.Unlock()
	if changed {
		go loadGitBlame(s, ps, st, threadID, path, seq)
	}
	if err != nil {
		renderGitPaneEmpty(c, err.Error(), IconGitBranch())
		return
	}
	if len(lines) > 20000 {
		renderGitPaneEmpty(c, "文件过大", IconGitBranch())
		return
	}
	for _, line := range lines {
		if strings.ContainsRune(line.Text, '\x00') {
			renderGitPaneEmpty(c, "二进制文件", IconGitBranch())
			return
		}
	}
	if loading {
		LoadingIndicator(c, nil, "", IconSM)
		return
	}
	if len(lines) == 0 {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.path != path || st.seq != seq && changed {
		return
	}
	st.list.Key = func(i int) any { return lines[i].Line }
	ui.List(c.Key("git-blame-lines-"+path), &st.list, len(lines), func(i int) {
		renderGitBlameLine(c, st, lines, blocks, i, onOpenCommit)
	}).FillWidth().FillHeight().MinHeight(0)
}

func loadGitBlame(s *chatState, ps *gitPaneState, st *gitBlameState, threadID, path string, seq uint64) {
	client, ok := s.client.(gitBlameClient)
	if !ok {
		err := fmt.Errorf("Git 客户端不支持逐行追溯")
		st.mu.Lock()
		if st.seq == seq {
			st.err, st.loading, st.pending = err, false, false
		}
		st.mu.Unlock()
		ps.fail(err)
		s.invalidate()
		return
	}
	timer := time.AfterFunc(150*time.Millisecond, func() {
		st.mu.Lock()
		if st.seq == seq && st.pending {
			st.loading = true
		}
		st.mu.Unlock()
		s.invalidate()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lines, err := client.GitBlame(ctx, threadID, path, "")
	timer.Stop()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.seq != seq {
		return
	}
	if err != nil {
		st.err, st.loading, st.pending = err, false, false
		st.mu.Unlock()
		ps.fail(err)
		st.mu.Lock()
		s.invalidate()
		return
	}
	st.lines, st.blocks, st.blockByRow = lines, git.BlameBlocks(lines), make([]int, len(lines))
	st.earliest, st.latest = time.Time{}, time.Time{}
	bi := 0
	for i, line := range lines {
		for bi+1 < len(st.blocks) && line.Line > st.blocks[bi].End {
			bi++
		}
		st.blockByRow[i] = bi
		if !line.AuthorTime.IsZero() {
			if st.earliest.IsZero() || line.AuthorTime.Before(st.earliest) {
				st.earliest = line.AuthorTime
			}
			if st.latest.IsZero() || line.AuthorTime.After(st.latest) {
				st.latest = line.AuthorTime
			}
		}
	}
	st.err, st.loading, st.pending = nil, false, false
	s.invalidate()
}

func renderGitBlameLine(c *ui.Context, st *gitBlameState, lines []git.BlameLine, blocks []git.BlameBlock, i int, onOpenCommit func(string)) {
	line := lines[i]
	blockIndex := st.blockByRow[i]
	first := line.Line == blocks[blockIndex].Start
	uncommitted := strings.Trim(line.SHA, "0") == ""
	row := ui.Row(c.Key(fmt.Sprintf("git-blame-line-%d", i))).FillWidth().Height(Space6).AlignItems(ui.Center)
	row.Children(func() {
		ui.Row(c).Width(Space0).Height(Space6).Background(ThemeAccent(c).Alpha(gitBlameTimeAlpha(st.earliest, st.latest, line.AuthorTime)))
		info := ui.ButtonBase(c.Key(fmt.Sprintf("git-blame-info-%d", i))).Label(fmt.Sprintf("%s %d", line.SHA, line.Line)).Width(168).Height(Space6).Padding(0).Transition(motionFeedback(c))
		if info.Hovered() {
			if st.hoverBlock != blockIndex || st.hoverRow != i {
				st.hoverBlock, st.hoverRow = blockIndex, i
				c.Invalidate()
			}
		} else if st.hoverRow == i {
			st.hoverBlock, st.hoverRow = -1, -1
			c.Invalidate()
		}
		if st.hoverBlock == blockIndex {
			info.Background(ThemeHover(c))
		}
		if first && i > 0 {
			info.BorderColor(ThemeBorder(c)).BorderWidth(Hairline, 0, 0, 0)
		}
		if !uncommitted {
			info.Tooltip(fmt.Sprintf("%s · %s · %s\n%s", line.Author, line.AuthorTime.Format("2006-01-02 15:04"), shortBlameSHA(line.SHA), line.Summary))
			if onOpenCommit != nil {
				info.OnClick(func() { onOpenCommit(line.SHA) })
			}
		}
		info.Children(func() {
			if !first {
				return
			}
			if uncommitted {
				ui.Text(c, "未提交").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
				return
			}
			ui.Text(c, blameInitials(line.Author)).Width(Space5).Height(Space5).Radius(RadiusPill).Background(ThemeAccent(c).Alpha(0.2)).FontSize(uiFont(TypeCaption)).TextColor(ThemeAccent(c))
			ui.Text(c, formatRelativeTime(line.AuthorTime)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine().Shrink(0)
			ui.Text(c, line.Summary).FontSize(uiFont(TypeCaption)).TextColor(ThemePrimaryText(c)).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		})
		ui.Text(c, fmt.Sprintf("%d", line.Line)).Width(Space8 + Space4).Font("monospace").FontSize(uiFont(TypeCode)).TextColor(ThemeTertiaryText(c)).TextAlign(ui.End).SingleLine()
		ui.Text(c, line.Text).Font("monospace").FontSize(uiFont(TypeCode)).TextColor(ThemePrimaryText(c)).SingleLine().Shrink(1)
	})
}

func blameInitials(author string) string {
	parts := strings.Fields(author)
	if len(parts) == 0 {
		return "?"
	}
	if len(parts) == 1 {
		r := []rune(parts[0])
		return string(r[0])
	}
	return string([]rune(parts[0])[0]) + string([]rune(parts[len(parts)-1])[0])
}

func shortBlameSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func init() { gitPaneBlameView = renderGitBlameView }
