package mcodeui

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// reviewCardHeadHeight 是改动卡片吸顶文件头的高度。
const reviewCardHeadHeight float32 = 32

// reviewRowKey 标识卡片列表的一行：每个文件占文件头与正文两行。
type reviewRowKey struct {
	path string
	body bool
}

// reviewStatusCode 把审阅状态（后端字母或旧接口单词）映射为 Git 状态字母。
func reviewStatusCode(status string) byte {
	switch status {
	case "A", "added":
		return 'A'
	case "D", "deleted":
		return 'D'
	case "R", "renamed":
		return 'R'
	case "C", "copied":
		return 'C'
	case "?", "untracked":
		return '?'
	case "U", "unmerged", "conflicted":
		return 'U'
	}
	return 'M'
}

// reviewCardOpen 返回卡片是否展开：默认折叠，只看用户显式开合过的值。
func reviewCardOpen(dp *diffPanelState, path string) bool {
	return dp.cardOpen[path]
}

// 调用方持有 diffPanelMu。
func setReviewCardOpenLocked(dp *diffPanelState, path string, open bool) {
	if dp.cardOpen == nil {
		dp.cardOpen = make(map[string]bool)
	}
	dp.cardOpen[path] = open
}

// reviewCardNote 是卡片正文里一行灰字说明（二进制、过大、读取失败等），可带重试。
func reviewCardNote(c *ui.Context, key, text string, retry func()) {
	ui.Row(c.Key(key)).FillWidth().Padding(Space3, Space4).Gap(Space2).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).Grow(1).Shrink(1).MinWidth(0)
		if retry != nil {
			Button(c, ButtonOpts{Key: key + "-retry", Text: "重试", Variant: ButtonGhost, Size: ControlSM, OnClick: retry})
		}
	})
}

// reviewCardLoading 是卡片正文读取中的一行：小号加载指示加灰字，与 reviewCardNote 同一行高。
func reviewCardLoading(c *ui.Context, key string) {
	ui.Row(c.Key(key)).FillWidth().Padding(Space3, Space4).Gap(Space2).AlignItems(ui.Center).Children(func() {
		LoadingIndicator(c, key+"-spinner", "", IconSM)
		ui.Text(c, "加载中").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	})
}

type reviewCardsController struct {
	renderRow func(relRow int)
}

// prepareReviewCards 准备审阅文件卡片列表的按行渲染与快捷键/滚动状态；可在独立列表或统一时间轴列表中按偏移量调用。
func prepareReviewCards(c *ui.Context, s *chatState, dp *diffPanelState, host ui.Element, tid string, snapshot ReviewSnapshot, g *reviewGit, rh reviewHost, list *ui.ListState, rowOffset int, railWidth float32) reviewCardsController {
	files := snapshot.Files
	base := reviewKey(s, tid, snapshot, "")
	scope := reviewScope(snapshot.Scope)
	diffPanelMu.Lock()
	selected, scrolled, split, width := dp.selectedPath, dp.scrolledPath, dp.preferSplit, dp.reviewWidth
	readKeys := dp.readKeys
	reads := make([]bool, len(files))
	opens := make([]bool, len(files))
	touched := make([]bool, len(files))
	for i, file := range files {
		key := base
		key.path = file.Path
		reads[i] = slices.Contains(readKeys, reviewReadKey(key, file))
		opens[i] = reviewCardOpen(dp, file.Path)
		touched[i] = scope == "all" && dp.sessionFiles[file.Path]
	}
	// 跟随、时间线或恢复的选中文件：滚到它的文件头并展开。
	if selected != scrolled {
		dp.scrolledPath = selected
		for i, file := range files {
			if file.Path == selected {
				setReviewCardOpenLocked(dp, file.Path, true)
				opens[i] = true
				list.ScrollTo(rowOffset+2*i, ui.Start)
				break
			}
		}
	}
	// 上一帧可见行加前后预建的余量；正文还在加载时每张卡片很矮，一屏能放下很多张。
	first, last := list.Visible()
	visFirst := max(first, rowOffset)
	visLast := min(last, rowOffset+2*len(files)-1)
	if visLast < visFirst {
		dp.cacheLimit = 4
	} else {
		dp.cacheLimit = (visLast-visFirst)/2 + 4
	}
	diffPanelMu.Unlock()

	current := func() int {
		first, _ := list.Visible()
		for i, file := range files {
			if file.Path == selected {
				return i
			}
		}
		return min(max((first-rowOffset)/2, 0), len(files)-1)
	}
	focus := func(i int) {
		i = min(max(i, 0), len(files)-1)
		selectReviewFile(s, dp, tid, files[i].Path, rh)
		diffPanelMu.Lock()
		dp.scrolledPath = files[i].Path
		diffPanelMu.Unlock()
		list.ScrollTo(rowOffset+2*i, ui.Start)
	}
	toggleCard := func(i int) {
		diffPanelMu.Lock()
		setReviewCardOpenLocked(dp, files[i].Path, !opens[i])
		dp.scrolledPath = files[i].Path
		diffPanelMu.Unlock()
		selectReviewFile(s, dp, tid, files[i].Path, rh)
	}
	toggleRead := func(i int) {
		key := base
		key.path = files[i].Path
		toggleReviewRead(dp, reviewReadKey(key, files[i]))
		diffPanelMu.Lock()
		// 标记已查看后收起并把文件头顶到最上，下一个文件紧接着出现；取消已查看不自动展开。
		if !reads[i] {
			setReviewCardOpenLocked(dp, files[i].Path, false)
		}
		diffPanelMu.Unlock()
		rememberReviewPreference(s, dp, tid)
		if !reads[i] {
			list.ScrollTo(rowOffset+2*i, ui.Start)
		}
		s.invalidate()
	}
	for _, key := range []ui.Key{ui.KeyJ, ui.KeyBracketRight} {
		if host.Shortcut(0, key) {
			focus(current() + 1)
		}
	}
	for _, key := range []ui.Key{ui.KeyK, ui.KeyBracketLeft} {
		if host.Shortcut(0, key) {
			focus(current() - 1)
		}
	}
	if host.Shortcut(0, ui.KeyV) {
		toggleRead(current())
	}
	commitSelected := list.Selected != nil && *list.Selected >= rowOffset+2*len(files) && list.Focused(c)
	if !commitSelected && host.Shortcut(0, ui.KeyEnter) {
		toggleCard(current())
	}
	if g != nil && host.Shortcut(0, ui.KeySpace) {
		g.toggleStage(s, files[current()].Path)
	}

	return reviewCardsController{
		renderRow: func(relRow int) {
			i := relRow / 2
			file := files[i]
			if relRow%2 == 0 {
				renderReviewCardHead(c, s, dp, tid, file, opens[i], reads[i], touched[i], file.Path == selected, railWidth, g, rh, func() { toggleCard(i) }, func() { toggleRead(i) })
				return
			}
			renderBody := func() {
				motionUnfoldBody(c, "review-card-body-"+file.Path, opens[i], func() {
					renderReviewFile(c, s, dp, tid, snapshot, file, split, max(0, width-railWidth))
				})
			}
			if railWidth > 0 {
				ui.Row(c.Key("git-all-file-body-row-"+file.Path)).FillWidth().AlignItems(ui.Stretch).Background(ThemeWindowBg(c)).Children(func() {
					renderGitTimelineRailLine(c, railWidth)
					ui.Column(c.Key("git-all-file-body-content-"+file.Path)).Grow(1).MinWidth(0).Children(renderBody)
				})
				return
			}
			renderBody()
		},
	}
}

// renderReviewCards 把改动渲染为连续滚动的文件卡片：文件头吸顶，正文展开时才读取全文。
func renderReviewCards(c *ui.Context, s *chatState, dp *diffPanelState, host ui.Element, tid string, snapshot ReviewSnapshot, g *reviewGit, rh reviewHost) {
	files := snapshot.Files
	ctrl := prepareReviewCards(c, s, dp, host, tid, snapshot, g, rh, &dp.rowsList, 0, 0)
	dp.rowsList.Key = func(i int) any { return reviewRowKey{files[i/2].Path, i%2 == 1} }
	dp.rowsList.Header = func(i int) bool { return i%2 == 0 }
	ui.List(c.Key("review-files"), &dp.rowsList, 2*len(files), ctrl.renderRow).Grow(1).MinHeight(0).FillWidth()
}

// renderReviewCardHead 渲染吸顶文件头：暂存勾选、开合箭头、状态、本线程圆点、文件名与目录、增删行数、悬停操作与已查看。
func renderReviewCardHead(c *ui.Context, s *chatState, dp *diffPanelState, tid string, file ReviewFile, open, read, threadTouched, selected bool, railWidth float32, g *reviewGit, rh reviewHost, onToggle, onRead func()) {
	// 吸顶时下方正文会滚过，底色必须不透明。
	bg := ThemeWindowBg(c)
	if rh.dialog {
		bg = ThemeSurface(c)
	}
	head := ui.Row(c.Key("review-card-head-"+file.Path)).Label(file.Path).FillWidth().Height(reviewCardHeadHeight).AlignItems(ui.Center).Focusable().OnClick(onToggle)
	hovered := head.Hovered()
	switch {
	case selected:
		bg = bg.Mix(ThemeAccent(c), 0.08)
	case hovered:
		bg = bg.Mix(ThemePrimaryText(c), 0.04)
	}
	head.Background(bg)
	menu := func(m *ui.Menu) { reviewFileMenu(m, c, s, dp, tid, file, g, rh) }
	head.ContextMenu(menu)
	renderContent := func() {
		if g != nil {
			g.renderStageBox(c, s, file.Path)
		}
		target := float32(0)
		if open {
			target = 1
		}
		if reduceMotion(c) {
			head.Jump("open", target)
		}
		turn := head.AnimateTo("open", target, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
		label := ui.Row(c.Key("review-file-"+file.Path)).Grow(1).MinWidth(0).Height(reviewCardHeadHeight).Padding(0, Space1).Gap(Space2).Justify(ui.Start).AlignItems(ui.Center)
		label.Children(func() {
			ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c)).Rotate(90 * turn).Shrink(0)
			letter, color := gitStatusPresentation(c, reviewStatusCode(file.Status))
			ui.Text(c, letter).Font("monospace").FontSize(uiFont(TypeCaption)).FontWeight(600).TextColor(color).Width(Space3).Shrink(0)
			if threadTouched {
				ui.Box(c.Key("review-thread-dot-"+file.Path)).Label("本线程改动:"+file.Path).Size(Space2, Space2).Radius(RadiusPill).Background(ThemeAccent(c)).Shrink(0)
			}
			name := path.Base(file.Path)
			nameColor := ThemePrimaryText(c)
			if read {
				nameColor = ThemeTertiaryText(c)
			}
			ui.Text(c, name).FontSize(uiFont(TypeFootnote)).TextColor(nameColor).SingleLine().Shrink(1).MinWidth(0)
			if dir := path.Dir(file.Path); dir != "." {
				ui.Text(c, dir).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine().TruncateStart().Shrink(1).MinWidth(0)
			}
		})
		if file.Additions > 0 {
			ui.Text(c, "+"+strconv.Itoa(file.Additions)).Font("monospace").FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c)).Shrink(0)
		}
		if file.Deletions > 0 {
			ui.Text(c, "−"+strconv.Itoa(file.Deletions)).Font("monospace").FontSize(uiFont(TypeCaption)).TextColor(ThemeDanger(c)).Shrink(0)
		}
		if g != nil {
			g.renderFileAction(c, s, dp, file.Path, hovered)
		}
		more := IconButton(c, IconButtonProps{Key: "review-file-more-" + file.Path, Icon: IconMoreHorizontal(), Label: "更多操作", Variant: ButtonGhost, Size: ControlXS}).Menu(menu)
		if !hovered && !more.Focused() {
			more.Opacity(0)
		}
		renderReviewReadButton(c, file.Path, read, hovered, onRead)
	}
	if railWidth > 0 {
		head.Gap(0).Children(func() {
			renderGitTimelineRailLine(c, railWidth)
			ui.Row(c.Key("review-card-head-content-"+file.Path)).Grow(1).MinWidth(0).Height(reviewCardHeadHeight).Padding(0, Space2, 0, Space1).Gap(Space1).AlignItems(ui.Center).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(renderContent)
		})
		return
	}
	head.Padding(0, Space2, 0, Space1).Gap(Space1).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(renderContent)
}

// renderReviewReadButton 是文件头末尾的「已查看」：未查看时只在悬停出现，已查看时常驻对勾。
func renderReviewReadButton(c *ui.Context, filePath string, read, hovered bool, onClick func()) {
	label := "标记为已查看"
	if read {
		label = "标记为未查看"
	}
	button := ui.ButtonBase(c.Key("review-read-"+filePath)).Label(label).Tooltip(label).Size(ControlXS, ControlXS).Radius(RadiusSM).Justify(ui.Center).AlignItems(ui.Center)
	scale := motionPressScale(c, button, "button-press-scale", button.Pressed())
	button.Scale(scale, scale)
	target := float32(0)
	if read {
		target = 1
	}
	if reduceMotion(c) {
		button.Jump("read", target)
	}
	amount := button.AnimateTo("read", target, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
	if !read && !hovered && !button.Focused() {
		button.Opacity(0)
	}
	if button.Clicked() {
		onClick()
	}
	button.Children(func() {
		icon := IconEye()
		if read {
			icon = IconCheck()
		}
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c).Mix(ThemeAccent(c), amount))
	})
}

// reviewFileMenu 是文件头「⋯」与右键菜单：「未提交」范围复用 Git 文件菜单，其余范围只给通用项。
func reviewFileMenu(m *ui.Menu, c *ui.Context, s *chatState, dp *diffPanelState, tid string, file ReviewFile, g *reviewGit, rh reviewHost) {
	if g != nil {
		entry, ok := g.entries[file.Path]
		if !ok {
			entry = git.StatusEntry{Path: file.Path}
		}
		gitFileMenu(m, c, s, tid, g.ps, entry, func() {
			diffPanelMu.Lock()
			dp.gitDetail = file.Path
			diffPanelMu.Unlock()
			s.invalidate()
		})
		return
	}
	deleted := reviewStatusCode(file.Status) == 'D'
	if !rh.dialog {
		if m.Item("在编辑器打开").Disabled(deleted).Chosen() {
			s.openFileManagerFileExternal(tid, file.Path)
		}
		if m.Item("在文件面板中显示").Disabled(deleted).Chosen() {
			inspectorSelect(s, sidePaneFileID, false)
			dir := path.Dir(file.Path)
			if dir == "." {
				dir = ""
			}
			s.fileTreeRevealDir(tid, dir)
			s.fileTreeSelect(file.Path, 0)
		}
	}
	if m.Item("复制路径").Chosen() {
		c.WriteClipboard(file.Path)
	}
}

// reviewGit 是「未提交」范围下改动面板借用的 Git 状态：暂存、撤销、冲突与提交框。
type reviewGit struct {
	ps                 *gitPaneState
	st                 *gitChangesState
	client             gitChangesClient
	entries            map[string]git.StatusEntry
	busy, pendingShown bool
	pending            string
}

// reviewGitFor 只在「全部」范围且客户端支持 Git 写操作时返回；同时让 Git 状态保持刷新与监听。
func reviewGitFor(s *chatState, scope string) *reviewGit {
	if scope != "all" {
		return nil
	}
	client, ok := s.client.(gitChangesClient)
	if _, pane := s.client.(gitPaneClient); !ok || !pane {
		return nil
	}
	ps := getGitPaneState(s)
	if ps == nil || ps.threadID == "" {
		return nil
	}
	ps.setVisible(true)
	ps.mu.Lock()
	root := ps.state.WorkDir
	entries := make(map[string]git.StatusEntry, len(ps.entries))
	for _, e := range ps.entries {
		entries[e.Path] = e
	}
	ps.mu.Unlock()
	if root == "" {
		return nil
	}
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	g := &reviewGit{ps: ps, st: st, client: client, entries: entries, busy: st.busy, pending: st.pending, pendingShown: st.pendingShown}
	st.mu.Unlock()
	return g
}

// reviewGitSyncPaths 在改动路径集合变化（撤销、新建、删除文件）时重取一次「未提交」。
func reviewGitSyncPaths(s *chatState, dp *diffPanelState, g *reviewGit, tid string, loading bool) {
	g.ps.mu.Lock()
	sig := g.ps.pathsSig
	g.ps.mu.Unlock()
	diffPanelMu.Lock()
	seen, changed := dp.gitSigSeen, dp.gitSig != sig
	if !loading {
		dp.gitSig, dp.gitSigSeen = sig, true
	}
	diffPanelMu.Unlock()
	if seen && changed && !loading {
		s.loadDiff(tid, "")
	}
}

// staged 返回文件的暂存状态：全部暂存、部分暂存（索引与工作区都有改动）。
func (g *reviewGit) staged(p string) (all, partial bool) {
	e, ok := g.entries[p]
	if !ok || e.Index == 0 || e.Index == ' ' || e.Index == '?' {
		return false, false
	}
	if e.Worktree != 0 && e.Worktree != ' ' {
		return false, true
	}
	return true, false
}

func (g *reviewGit) toggleStage(s *chatState, p string) {
	e, ok := g.entries[p]
	if !ok || gitEntryConflicted(e) || g.busy {
		return
	}
	all, _ := g.staged(p)
	tid := g.ps.threadID
	gitChangesRun(g.ps, g.st, "stage:"+p, func(ctx context.Context) error {
		if all {
			return g.client.GitUnstageFiles(ctx, tid, []string{p})
		}
		return g.client.GitStageFiles(ctx, tid, []string{p})
	})
}

// renderStageBox 渲染文件头最左侧的暂存勾选框；冲突文件留空位保持对齐。
func (g *reviewGit) renderStageBox(c *ui.Context, s *chatState, p string) {
	e, ok := g.entries[p]
	if !ok || gitEntryConflicted(e) {
		ui.Box(c).Size(ControlXS, ControlXS).Shrink(0)
		return
	}
	all, partial := g.staged(p)
	label := "暂存"
	if all {
		label = "取消暂存"
	}
	box := ui.ButtonBase(c.Key("review-stage-"+p)).Label(label).Tooltip(label).Size(ControlXS, ControlXS).Disabled(g.busy).Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
	if box.Clicked() {
		g.toggleStage(s, p)
	}
	box.Children(func() {
		if g.pending == "stage:"+p && g.pendingShown {
			LoadingIndicator(c, "review-stage-loading", "", IconSM, ThemePrimaryText(c))
			return
		}
		mark := ui.Box(c.Key("mark")).Size(Space4-Space1, Space4-Space1).Radius(RadiusXS).Justify(ui.Center).AlignItems(ui.Center).Transition(motionFeedback(c))
		if all || partial {
			mark.Background(ThemeAccent(c))
		} else {
			mark.Border(1, ThemeTertiaryText(c))
			if box.Hovered() {
				mark.Border(1, ThemeSecondaryText(c))
			}
		}
		mark.Children(func() {
			switch {
			case all:
				ui.Icon(c, IconCheck()).Size(Space3, Space3).TextColor(ThemeOnAccent(c))
			case partial:
				ui.Box(c).Size(Space2, Hairline*2).Radius(RadiusPill).Background(ThemeOnAccent(c))
			}
		})
	})
}

// renderFileAction 是文件头的悬停操作：冲突文件常驻「解决冲突」，其余文件悬停出现「撤销文件」。
func (g *reviewGit) renderFileAction(c *ui.Context, s *chatState, dp *diffPanelState, p string, hovered bool) {
	e, ok := g.entries[p]
	if !ok {
		return
	}
	if gitEntryConflicted(e) {
		Button(c, ButtonOpts{Key: "review-resolve-" + p, Text: "解决冲突", Variant: ButtonSecondary, Size: ControlXS, OnClick: func() {
			diffPanelMu.Lock()
			dp.gitDetail = p
			diffPanelMu.Unlock()
			s.invalidate()
		}})
		return
	}
	agentBusy := gitPaneAgentBusy(s, g.ps.threadID)
	tip := "撤销文件改动"
	if agentBusy {
		tip = "Agent 正在修改此工作区"
	}
	key := "discard:" + p
	b := IconButton(c, IconButtonProps{Key: "review-discard-" + p, Icon: iconRotateCcw, Label: "撤销文件改动", Tooltip: tip, Variant: ButtonGhost, Size: ControlXS, Disabled: g.busy || agentBusy, Loading: g.pending == key && g.pendingShown, OnClick: func() {
		gitChangesDiscard(g.ps, g.st, g.client, g.ps.threadID, []string{p})
	}})
	if !hovered && !b.Focused() && !(g.pending == key && g.pendingShown) {
		b.Opacity(0)
	}
}

// openReviewUncommitted 打开 Git Tab 的「全部」范围；path 非空时直接进入该文件的逐行暂存或冲突解决。
func openReviewUncommitted(s *chatState, path string) {
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}
	inspectorSelect(s, sidePaneGitID, false)
	diffPanelMu.Lock()
	dp.gitDetail = path
	diffPanelMu.Unlock()
	s.selectReviewScope(dp, "all")
}

type reviewHeaderMenuItem struct {
	key      string
	label    string
	tooltip  string
	checked  bool
	danger   bool
	disabled bool
	onClick  func()
}

type reviewHeaderTurnOption struct {
	label   string
	checked bool
	onClick func()
}

type reviewHeaderMenuData struct {
	viewItems  []reviewHeaderMenuItem
	turns      []reviewHeaderTurnOption
	stageItems []reviewHeaderMenuItem
	discardAll *reviewHeaderMenuItem
}

func buildReviewHeaderMenuData(s *chatState, dp *diffPanelState, tid string, data ThreadDiff, turnID, scope string) reviewHeaderMenuData {
	diffPanelMu.Lock()
	split, width := dp.preferSplit, dp.reviewWidth
	files := dp.snapshot.Files
	diffPanelMu.Unlock()

	out := reviewHeaderMenuData{
		viewItems: []reviewHeaderMenuItem{
			{
				key:      "split",
				label:    "并排显示",
				checked:  split,
				disabled: width < reviewWideWidth,
				tooltip:  fmt.Sprintf("面板宽于 %d 时可用", int(reviewWideWidth)),
				onClick: func() {
					diffPanelMu.Lock()
					dp.preferSplit = !split
					diffPanelMu.Unlock()
					s.invalidate()
				},
			},
			{
				key:      "expand-all",
				label:    "全部展开",
				disabled: len(files) == 0,
				onClick: func() {
					diffPanelMu.Lock()
					for _, file := range files {
						setReviewCardOpenLocked(dp, file.Path, true)
					}
					diffPanelMu.Unlock()
					s.invalidate()
				},
			},
			{
				key:      "collapse-all",
				label:    "全部收起",
				disabled: len(files) == 0,
				onClick: func() {
					diffPanelMu.Lock()
					for _, file := range files {
						setReviewCardOpenLocked(dp, file.Path, false)
					}
					diffPanelMu.Unlock()
					s.invalidate()
				},
			},
		},
	}

	if scope == "turn" && len(data.Turns) > 1 {
		out.turns = append(out.turns, reviewHeaderTurnOption{
			label:   "最新一轮",
			checked: turnID == "",
			onClick: func() {
				inspectorFollowPause(s, "")
				s.loadDiff(tid, "")
			},
		})
		for i := len(data.Turns) - 1; i >= 0; i-- {
			t := data.Turns[i]
			targetTurn := t.TurnID
			out.turns = append(out.turns, reviewHeaderTurnOption{
				label:   fmt.Sprintf("第 %d 轮", t.Index+1),
				checked: turnID == targetTurn,
				onClick: func() {
					inspectorFollowPause(s, "")
					s.loadDiff(tid, targetTurn)
				},
			})
		}
	}

	g := reviewGitFor(s, scope)
	if g == nil {
		return out
	}
	g.ps.mu.Lock()
	groups := gitChangesGroups(g.ps.entries)
	g.ps.mu.Unlock()
	out.stageItems = []reviewHeaderMenuItem{
		{
			key:      "stage-all",
			label:    "全部暂存",
			disabled: g.busy || len(groups[2].entries) == 0,
			onClick: func() {
				gitChangesRun(g.ps, g.st, "group:2", func(ctx context.Context) error {
					return g.client.GitStageFiles(ctx, tid, gitChangesPaths(groups[2].entries))
				})
			},
		},
		{
			key:      "unstage-all",
			label:    "全部取消暂存",
			disabled: g.busy || len(groups[1].entries) == 0,
			onClick: func() {
				gitChangesRun(g.ps, g.st, "group:1", func(ctx context.Context) error {
					return g.client.GitUnstageFiles(ctx, tid, gitChangesPaths(groups[1].entries))
				})
			},
		},
	}
	out.discardAll = &reviewHeaderMenuItem{
		key:      "discard-all",
		label:    "全部撤销…",
		danger:   true,
		disabled: g.busy || len(groups[2].entries) == 0 || gitPaneAgentBusy(s, tid),
		onClick: func() {
			g.st.mu.Lock()
			g.st.confirm = true
			g.st.mu.Unlock()
			s.invalidate()
		},
	}
	return out
}

// renderReviewHeaderMenu 是独立差异面板标题栏「⋯」：并排显示、全部展开或收起、轮次与「未提交」的批量操作。
func renderReviewHeaderMenu(m *ui.Menu, s *chatState, dp *diffPanelState, tid string, data ThreadDiff, turnID string) {
	diffPanelMu.Lock()
	scope := reviewScope(dp.scope)
	diffPanelMu.Unlock()
	menuData := buildReviewHeaderMenuData(s, dp, tid, data, turnID, scope)
	for _, it := range menuData.viewItems {
		item := m.Item(it.label).Checked(it.checked).Disabled(it.disabled)
		if it.tooltip != "" {
			item = item.Tooltip(it.tooltip)
		}
		if item.Chosen() {
			it.onClick()
		}
	}
	if len(menuData.turns) > 0 {
		m.Separator()
		m.Submenu("轮次", func(m *ui.Menu) {
			for _, opt := range menuData.turns {
				if m.Item(opt.label).Radio(opt.checked).Chosen() {
					opt.onClick()
				}
			}
		})
	}
	if len(menuData.stageItems) == 0 && menuData.discardAll == nil {
		return
	}
	m.Separator()
	for _, it := range menuData.stageItems {
		if m.Item(it.label).Disabled(it.disabled).Chosen() {
			it.onClick()
		}
	}
	if it := menuData.discardAll; it != nil {
		if m.Item(it.label).Danger().Disabled(it.disabled).Chosen() {
			it.onClick()
		}
	}
}
