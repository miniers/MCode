package mcodeui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitConflictState struct {
	text             string
	regions          []git.ConflictRegion
	choices          map[int]git.ConflictChoice
	err              error
	binary, tooLarge bool
}

func gitConflictPath(root, path string) (string, error) {
	if !filepath.IsLocal(path) || path == ".git" || strings.HasPrefix(filepath.ToSlash(path), ".git/") {
		return "", fmt.Errorf("冲突文件路径无效")
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, full)
	if err != nil || !filepath.IsLocal(rel) || rel == ".git" || strings.HasPrefix(filepath.ToSlash(rel), ".git/") {
		return "", fmt.Errorf("冲突文件超出工作区")
	}
	return full, nil
}

func readGitConflict(root, path string) *gitConflictState {
	cs := &gitConflictState{choices: map[int]git.ConflictChoice{}}
	full, err := gitConflictPath(root, path)
	if err != nil {
		cs.err = err
		return cs
	}
	info, err := os.Stat(full)
	if err != nil {
		cs.err = err
		return cs
	}
	if info.Size() > gitDiffMaxBytes {
		cs.tooLarge = true
		return cs
	}
	data, err := os.ReadFile(full)
	if err != nil {
		cs.err = err
		return cs
	}
	cs.binary = bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
	if !cs.binary {
		cs.text = string(data)
		cs.regions = git.ParseConflicts(cs.text)
	}
	return cs
}

func gitConflictReady(cs *gitConflictState) bool {
	if cs == nil || cs.err != nil || cs.binary || cs.tooLarge {
		return false
	}
	for i := range cs.regions {
		switch cs.choices[i] {
		case git.ConflictOurs, git.ConflictTheirs, git.ConflictBoth:
		default:
			return false
		}
	}
	return true
}

func gitConflictLabels(ps *gitPaneState) (string, string) {
	ps.mu.Lock()
	ours, theirs := ps.op.OursLabel, ps.op.TheirsLabel
	ps.mu.Unlock()
	if ours == "" {
		ours = "当前"
	}
	if theirs == "" {
		theirs = "传入"
	}
	return ours, theirs
}

func renderGitConflict(c *ui.Context, s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, result *gitDiffResult, disabled bool, width float32, onBack func()) {
	cs := (*gitConflictState)(nil)
	if result != nil {
		cs = result.conflict
	}
	ours, theirs := gitConflictLabels(ps)
	ui.Row(c.Key("git-conflict-toolbar")).FillWidth().Height(diffHeaderHeight).Gap(Space2).AlignItems(ui.Center).Padding(0, Space3).Children(func() {
		if onBack != nil {
			IconButton(c, IconButtonProps{Key: "git-conflict-back", Icon: IconArrowLeft(), Label: "返回", Variant: ButtonGhost, Size: ControlSM, OnClick: onBack})
		}
		gitDiffPath(c, key.path, width)
		ready := gitConflictReady(cs)
		busy := gitPaneAgentBusy(s, key.threadID)
		tip := ""
		if !ready {
			n := 0
			if cs != nil {
				n = len(cs.regions) - len(cs.choices)
			}
			tip = fmt.Sprintf("还有 %d 处未选择", n)
			if cs != nil && (cs.err != nil || cs.binary || cs.tooLarge) {
				tip = "请使用整个文件选边"
			}
		}
		if busy {
			tip = "Agent 正在修改此工作区"
		}
		Button(c, ButtonOpts{Text: "保存并标记已解决", Size: ControlSM, Disabled: disabled || !ready || busy, Tooltip: tip, OnClick: func() { saveGitConflict(s, ps, st, client, key, cs) }})
		menuState := cs
		if menuState == nil {
			menuState = &gitConflictState{}
		}
		renderGitDiffMenu(c, s, ps, st, client, key, menuState, disabled)
	})
	if cs == nil {
		return
	}
	if cs.err != nil {
		renderGitPaneEmpty(c, "无法读取冲突文件", IconFile())
		return
	}
	if cs.binary {
		renderGitPaneEmpty(c, "二进制文件", IconFile())
		return
	}
	if cs.tooLarge {
		renderGitPaneEmpty(c, "文件过大", IconFile())
		return
	}
	// 冲突需要在首行之前插入工具条；DiffAnnotation 只支持行后插入，因此使用等宽文本列表。
	ui.Scroll(c.Key("git-conflict-content")).Grow(1).FillWidth().MinHeight(0).TrackScroll(&st.conflictScroll).Children(func() {
		ui.Column(c).FillWidth().Children(func() {
			pos := 0
			lines := strings.SplitAfter(cs.text, "\n")
			for i, region := range cs.regions {
				renderGitConflictText(c, lines[pos:region.Start])
				ui.Row(c.Key(fmt.Sprintf("git-conflict-region-%d", i))).Height(gitGroupHeaderHeight).Gap(Space1).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("第 %d/%d 处", i+1, len(cs.regions))).FontSize(uiFont(TypeFootnote))
					for _, choice := range []struct {
						value git.ConflictChoice
						label string
					}{{git.ConflictOurs, "保留 " + ours}, {git.ConflictTheirs, "保留 " + theirs}, {git.ConflictBoth, "都保留"}} {
						b := Button(c, ButtonOpts{Key: fmt.Sprintf("choice-%d-%s", i, choice.value), Text: choice.label, Label: fmt.Sprintf("第 %d 处 %s", i+1, choice.label), Size: ControlSM, Variant: ButtonGhost, Disabled: disabled, OnClick: func() { cs.choices[i] = choice.value; s.invalidate() }}).Selected(cs.choices[i] == choice.value)
						if cs.choices[i] == choice.value {
							b.Background(ThemeSelected(c))
						}
					}
				})
				renderGitConflictText(c, lines[region.Start:region.End+1])
				pos = region.End + 1
			}
			renderGitConflictText(c, lines[pos:])
		})
	})
}

func renderGitConflictText(c *ui.Context, lines []string) {
	for _, line := range lines {
		ui.Text(c, strings.TrimSuffix(line, "\n")).Font("monospace").FontSize(uiFont(TypeCode)).MinHeight(ControlXS).SingleLine().Selectable()
	}
}

func saveGitConflict(s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, cs *gitConflictState) {
	if !gitConflictReady(cs) {
		return
	}
	// 在启动后台写入前固定正文与选择，避免改选与 ResolveRegions 并发读 map。
	content := []byte(git.ResolveRegions(cs.text, cs.choices))
	original := cs.text
	runGitDiffWrite(s, ps, st, key, true, func(ctx context.Context) (*git.Snapshot, error) {
		current := readGitConflict(key.root, key.path)
		if current.err != nil {
			return nil, current.err
		}
		if current.binary || current.tooLarge || current.text != original {
			return nil, fmt.Errorf("冲突文件已修改，请重新选择")
		}
		if err := client.GitWriteResolved(ctx, key.threadID, key.path, content); err != nil {
			return nil, err
		}
		return nil, client.GitMarkResolved(ctx, key.threadID, []string{key.path})
	}, "")
}

func renderGitConflictMenu(c *ui.Context, s *chatState, ps *gitPaneState, st *gitDiffViewState, client gitDiffClient, key gitDiffKey, cs *gitConflictState, disabled bool) {
	ours, theirs := gitConflictLabels(ps)
	busy := gitPaneAgentBusy(s, key.threadID)
	tip := ""
	if busy {
		tip = "Agent 正在修改此工作区"
	}
	for _, side := range []struct {
		ours  bool
		label string
	}{{true, ours}, {false, theirs}} {
		Button(c, ButtonOpts{Text: "整个文件用 " + side.label, Variant: ButtonGhost, Disabled: disabled || busy, Tooltip: tip, OnClick: func() {
			st.menuOpen = false
			runGitDiffWrite(s, ps, st, key, true, func(ctx context.Context) (*git.Snapshot, error) {
				if err := client.GitResolveFileSide(ctx, key.threadID, key.path, side.ours); err != nil {
					return nil, err
				}
				return nil, client.GitMarkResolved(ctx, key.threadID, []string{key.path})
			}, "")
		}})
	}
	Button(c, ButtonOpts{Text: "交给 AI 解决", Variant: ButtonGhost, Disabled: disabled || cs.err != nil || cs.binary || cs.tooLarge, OnClick: func() {
		st.menuOpen = false
		s.mu.Lock()
		selected := s.selectedThreadID == key.threadID
		s.mu.Unlock()
		if selected {
			lines := strings.SplitAfter(cs.text, "\n")
			prompt := "请解决文件 " + key.path + " 的 Git 冲突，保留双方意图：\n\n"
			for _, region := range cs.regions {
				prompt += strings.Join(lines[region.Start:region.End+1], "") + "\n"
			}
			// 引用入口会先解析 Markdown；代码围栏保留冲突标记与正文原文。
			fence := threadExportFence(prompt)
			s.quoteIntoDraft(fence + "\n" + prompt + "\n" + fence)
			s.mu.Lock()
			s.composerFocusRequest = true
			s.mu.Unlock()
		}
	}})
}
