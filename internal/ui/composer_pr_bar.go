package mcodeui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const (
	// composerPRMergedTitlePrefix 与后端 archiveMergedPRThread 写入的标题前缀保持一致。
	composerPRMergedTitlePrefix = "已合并 · "
	// composerPRPopoverMaxWidth 是合并确认浮层的最大宽度（DIP）。
	composerPRPopoverMaxWidth float32 = 340
	// composerPRStateTimeout 是后台读取 PR 分支状态的超时时间。
	composerPRStateTimeout = 15 * time.Second
)

var composerPRNumberPattern = regexp.MustCompile(`#(\d+)`)

// gitPRFlowClient 是输入框上方 PR 会话条与一键合并流程所需的后端能力，由 app.Client 实现。
type gitPRFlowClient interface {
	GitPRThreadState(ctx context.Context, threadID string) (git.PRThreadState, bool, error)
	GitPRMergeFlow(ctx context.Context, threadID string, number int, method, from string, onStep func(step string)) (failedStep string, err error)
}

var composerPRMergeSteps = []struct {
	key   string
	label string
}{
	{key: "push", label: "推送"},
	{key: "merge", label: "合并 PR"},
	{key: "sync", label: "更新本地 main"},
	{key: "cleanup", label: "清理"},
}

type composerPRThreadEntry struct {
	state      git.PRThreadState
	hasPR      bool
	loadedOnce bool
	loading    bool
	seq        uint64

	lastGitSeq  uint64
	lastGitSnap string
	wasRunning  bool

	mergeMethod string

	merging         bool
	activeStep      string
	completedSteps  map[string]bool
	failedStep      string
	mergeErr        string
	merged          bool
	mergedNumber    int
	mergedProjectID string
}

type composerPRThreadSnapshot struct {
	state           git.PRThreadState
	hasPR           bool
	mergeMethod     string
	merging         bool
	activeStep      string
	completedSteps  map[string]bool
	failedStep      string
	mergeErr        string
	merged          bool
	mergedNumber    int
	mergedProjectID string
}

type composerPRBarState struct {
	threads map[string]*composerPRThreadEntry
}

var (
	composerPRBarMu     sync.Mutex
	composerPRBarStates = make(map[*chatState]*composerPRBarState)
)

func getComposerPRBarStateLocked(s *chatState) *composerPRBarState {
	st, ok := composerPRBarStates[s]
	if !ok {
		st = &composerPRBarState{threads: make(map[string]*composerPRThreadEntry)}
		composerPRBarStates[s] = st
	}
	return st
}

func getComposerPRThreadEntryLocked(st *composerPRBarState, threadID string) *composerPRThreadEntry {
	entry, ok := st.threads[threadID]
	if !ok {
		entry = &composerPRThreadEntry{mergeMethod: "squash"}
		st.threads[threadID] = entry
	}
	return entry
}

func snapshotComposerPRThread(s *chatState, threadID string) composerPRThreadSnapshot {
	if s == nil || threadID == "" {
		return composerPRThreadSnapshot{mergeMethod: "squash"}
	}
	composerPRBarMu.Lock()
	defer composerPRBarMu.Unlock()
	st := getComposerPRBarStateLocked(s)
	entry := st.threads[threadID]
	if entry == nil {
		return composerPRThreadSnapshot{mergeMethod: "squash"}
	}
	var completed map[string]bool
	if len(entry.completedSteps) > 0 {
		completed = make(map[string]bool, len(entry.completedSteps))
		for k, v := range entry.completedSteps {
			completed[k] = v
		}
	}
	return composerPRThreadSnapshot{
		state:           entry.state,
		hasPR:           entry.hasPR,
		mergeMethod:     gitPRCurrentMergeMethod(entry.mergeMethod),
		merging:         entry.merging,
		activeStep:      entry.activeStep,
		completedSteps:  completed,
		failedStep:      entry.failedStep,
		mergeErr:        entry.mergeErr,
		merged:          entry.merged,
		mergedNumber:    entry.mergedNumber,
		mergedProjectID: entry.mergedProjectID,
	}
}

// headerGitObs 返回指定会话当前的 Git 刷新序号与状态指纹，用于感知切会话、回合结束与 git 刷新（不做 IO）。
func headerGitObs(s *chatState, threadID string) (uint64, string) {
	if s == nil || threadID == "" {
		return 0, ""
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	seq := hs.gitSeq[threadID]
	st, ok := hs.git[threadID]
	if !ok {
		return seq, ""
	}
	return seq, fmt.Sprintf("%s|%s|%d|%d|%d|%v|%v",
		st.Branch, st.Head, st.Changes, st.Ahead, st.Behind, st.HasUpstream, st.Detached)
}

func threadSnapshotForPRBar(s *chatState, threadID string) (Thread, bool) {
	if s == nil || threadID == "" {
		return Thread{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th, true
		}
	}
	return Thread{}, false
}

func archivedThreadSnapshotForPRBar(threadID string) (Thread, bool) {
	if threadID == "" {
		return Thread{}, false
	}
	globalArchiveState.mu.Lock()
	defer globalArchiveState.mu.Unlock()
	for _, th := range globalArchiveState.threads {
		if th.ID == threadID {
			return th, true
		}
	}
	return Thread{}, false
}

func parsePRNumberFromTitle(title string) int {
	m := composerPRNumberPattern.FindStringSubmatch(title)
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func startPRThreadStateRefreshLocked(s *chatState, flowClient gitPRFlowClient, entry *composerPRThreadEntry, threadID string) {
	entry.seq++
	seq := entry.seq
	entry.loading = true

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), composerPRStateTimeout)
		defer cancel()
		state, ok, err := flowClient.GitPRThreadState(ctx, threadID)

		composerPRBarMu.Lock()
		if s.closed || entry.seq != seq {
			composerPRBarMu.Unlock()
			return
		}
		entry.loading = false
		entry.loadedOnce = true
		if err == nil {
			entry.hasPR = ok && state.Number > 0
			if entry.hasPR {
				entry.state = state
			} else {
				entry.state = git.PRThreadState{}
			}
		}
		composerPRBarMu.Unlock()
		s.invalidate()
	}()
}

// syncComposerPRThreadState 根据切会话、回合结束与 Git 状态刷新事件按需触发一次异步状态刷新。
func syncComposerPRThreadState(s *chatState, threadID string, isRunning bool) {
	if s == nil || s.client == nil || s.embeddedBee || threadID == "" {
		return
	}
	flowClient, ok := s.client.(gitPRFlowClient)
	if !ok || flowClient == nil {
		return
	}

	curGitSeq, curGitSnap := headerGitObs(s, threadID)

	composerPRBarMu.Lock()
	st := getComposerPRBarStateLocked(s)
	entry := getComposerPRThreadEntryLocked(st, threadID)
	if entry.merged || entry.merging {
		entry.lastGitSeq = curGitSeq
		entry.lastGitSnap = curGitSnap
		entry.wasRunning = isRunning
		composerPRBarMu.Unlock()
		return
	}
	needRefresh := false
	switch {
	case !entry.loadedOnce && !entry.loading:
		needRefresh = true
	case curGitSeq != entry.lastGitSeq || curGitSnap != entry.lastGitSnap:
		needRefresh = true
	case entry.wasRunning && !isRunning:
		needRefresh = true
	}
	entry.lastGitSeq = curGitSeq
	entry.lastGitSnap = curGitSnap
	entry.wasRunning = isRunning
	if needRefresh {
		startPRThreadStateRefreshLocked(s, flowClient, entry, threadID)
	}
	composerPRBarMu.Unlock()
}

// refreshComposerPRThreadState 主动触发一次 PR 分支状态异步刷新（点击主按钮或流程失败后调用）。
func refreshComposerPRThreadState(s *chatState, threadID string) {
	if s == nil || s.client == nil || s.embeddedBee || threadID == "" {
		return
	}
	flowClient, ok := s.client.(gitPRFlowClient)
	if !ok || flowClient == nil {
		return
	}
	composerPRBarMu.Lock()
	st := getComposerPRBarStateLocked(s)
	entry := getComposerPRThreadEntryLocked(st, threadID)
	if !entry.merging && !entry.merged {
		startPRThreadStateRefreshLocked(s, flowClient, entry, threadID)
	}
	composerPRBarMu.Unlock()
}

func startComposerPRMergeFlow(s *chatState, threadID string, number int, method, from string) {
	if s == nil || s.client == nil || threadID == "" || number <= 0 {
		return
	}
	flowClient, ok := s.client.(gitPRFlowClient)
	if !ok || flowClient == nil {
		return
	}
	method = gitPRCurrentMergeMethod(method)
	projectID := ""
	if th, found := threadSnapshotForPRBar(s, threadID); found {
		projectID = th.ProjectID
	}

	composerPRBarMu.Lock()
	st := getComposerPRBarStateLocked(s)
	entry := getComposerPRThreadEntryLocked(st, threadID)
	if entry.merging || entry.merged {
		composerPRBarMu.Unlock()
		return
	}
	entry.merging = true
	entry.mergeMethod = method
	entry.failedStep = ""
	entry.mergeErr = ""
	entry.mergedNumber = number
	if projectID != "" {
		entry.mergedProjectID = projectID
	}
	initialStep := from
	if initialStep == "" {
		initialStep = composerPRMergeSteps[0].key
	}
	entry.activeStep = initialStep
	entry.completedSteps = make(map[string]bool)
	for _, step := range composerPRMergeSteps {
		if step.key == initialStep {
			break
		}
		entry.completedSteps[step.key] = true
	}
	composerPRBarMu.Unlock()
	s.invalidate()

	go func() {
		onStep := func(step string) {
			composerPRBarMu.Lock()
			if s.closed {
				composerPRBarMu.Unlock()
				return
			}
			if entry.completedSteps == nil {
				entry.completedSteps = make(map[string]bool)
			}
			for _, candidate := range composerPRMergeSteps {
				if candidate.key == step {
					break
				}
				entry.completedSteps[candidate.key] = true
			}
			entry.activeStep = step
			composerPRBarMu.Unlock()
			s.invalidate()
		}

		failedStep, err := flowClient.GitPRMergeFlow(context.Background(), threadID, number, method, from, onStep)

		composerPRBarMu.Lock()
		if s.closed {
			composerPRBarMu.Unlock()
			return
		}
		entry.merging = false
		if err != nil {
			entry.failedStep = failedStep
			entry.mergeErr = err.Error()
			composerPRBarMu.Unlock()
			refreshComposerPRThreadState(s, threadID)
			s.invalidate()
			return
		}
		entry.activeStep = ""
		entry.failedStep = ""
		entry.mergeErr = ""
		entry.merged = true
		entry.mergedNumber = number
		if entry.mergedProjectID == "" {
			if th, found := threadSnapshotForPRBar(s, threadID); found {
				entry.mergedProjectID = th.ProjectID
			}
		}
		if entry.completedSteps == nil {
			entry.completedSteps = make(map[string]bool)
		}
		for _, step := range composerPRMergeSteps {
			entry.completedSteps[step.key] = true
		}
		composerPRBarMu.Unlock()
		s.invalidate()
	}()
}

// composerPRMergedSnapshot 从内存态与会话快照判断当前会话是否处于「已合并」只读归档态（不做 IO）。
func composerPRMergedSnapshot(s *chatState, threadID string, currentThread *Thread) (merged bool, number int, projectID string) {
	if s == nil || s.embeddedBee || threadID == "" {
		return false, 0, ""
	}
	snap := snapshotComposerPRThread(s, threadID)
	if snap.merged {
		num := snap.mergedNumber
		if num <= 0 {
			num = snap.state.Number
		}
		projID := snap.mergedProjectID
		if projID == "" && currentThread != nil {
			projID = currentThread.ProjectID
		}
		if num <= 0 && currentThread != nil {
			num = parsePRNumberFromTitle(currentThread.Title)
		}
		return true, num, projID
	}

	if currentThread != nil && strings.HasPrefix(currentThread.Title, composerPRMergedTitlePrefix) {
		num := snap.state.Number
		if num <= 0 {
			num = parsePRNumberFromTitle(currentThread.Title)
		}
		return true, num, currentThread.ProjectID
	}

	if archived, ok := archivedThreadSnapshotForPRBar(threadID); ok && strings.HasPrefix(archived.Title, composerPRMergedTitlePrefix) {
		num := snap.state.Number
		if num <= 0 {
			num = parsePRNumberFromTitle(archived.Title)
		}
		return true, num, archived.ProjectID
	}

	return false, 0, ""
}

// renderComposerPRMergedBar 在会话处于「已合并」只读归档态时替代输入框编辑区渲染状态条与新建会话按钮。
func renderComposerPRMergedBar(c *ui.Context, s *chatState, threadID string, currentThread *Thread) bool {
	merged, number, projectID := composerPRMergedSnapshot(s, threadID, currentThread)
	if !merged {
		return false
	}

	label := "已合并"
	if number > 0 {
		label = fmt.Sprintf("已合并 #%d", number)
	}

	banner := ui.Row(c.Key("composer-pr-merged-bar")).
		Label(label).
		FillWidth().
		Shrink(0).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(Space3).
		Padding(Space3, Space4).
		Radius(RadiusLG).
		Border(Hairline, ThemePlan(c).Alpha(0.30)).
		Background(ThemePlan(c).Alpha(0.12))

	banner.Children(func() {
		ui.Row(c.Key("composer-pr-merged-info")).
			AlignItems(ui.Center).
			Gap(Space2).
			Grow(1).
			MinWidth(0).
			Children(func() {
				ui.Icon(c, IconMerge()).Size(IconMD, IconMD).TextColor(ThemePlan(c))
				ui.Text(c, label).
					FontSize(uiFont(TypeBody)).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
			})

		Button(c, ButtonOpts{
			Key:     "composer-pr-new-thread-btn",
			Text:    "新建会话",
			Icon:    IconPlus(),
			Variant: ButtonSecondary,
			Size:    ControlSM,
			OnClick: func() {
				if projectID != "" {
					s.createThread(projectID)
					return
				}
				s.createNewThread()
			},
		})
	})

	return true
}

func composerPRPrimaryButtonSpec(state git.PRThreadState) (text string, disabled bool, variant ButtonVariant) {
	if state.Dirty {
		return "有未提交的改动", true, ButtonSecondary
	}
	if state.LocalCommits > 0 && !state.CrossRepo {
		return "推送并合并", false, ButtonPrimary
	}
	return "合并", false, ButtonPrimary
}

func composerPRStepLabel(stepKey string) string {
	for _, step := range composerPRMergeSteps {
		if step.key == stepKey {
			return step.label
		}
	}
	return stepKey
}

// renderComposerPRBar 在输入卡片正上方渲染 PR 会话条：显示 PR 状态、合并确认浮层以及四步合并进度与重试。
func renderComposerPRBar(c *ui.Context, s *chatState, threadID string) {
	if s == nil || s.embeddedBee || threadID == "" {
		return
	}
	if _, ok := s.client.(gitPRFlowClient); !ok {
		return
	}

	th, hasThread := threadSnapshotForPRBar(s, threadID)
	var currentThreadPtr *Thread
	if hasThread {
		currentThreadPtr = &th
	}
	if merged, _, _ := composerPRMergedSnapshot(s, threadID, currentThreadPtr); merged {
		return
	}

	isRunning := hasThread && (th.IsRunning || th.SwarmBusy)
	syncComposerPRThreadState(s, threadID, isRunning)

	snap := snapshotComposerPRThread(s, threadID)
	visible := snap.hasPR || snap.merging || snap.mergeErr != ""

	v := &s.composerView
	if v.prBarThread != threadID {
		v.prBarThread = threadID
		v.prBarShown = false
		v.prConfirmOpen = false
	}
	if !visible && !v.prBarShown {
		return
	}
	if snap.merging && v.prConfirmOpen {
		v.prConfirmOpen = false
	}
	if v.prConfirmOpen && c.Shortcut(0, ui.KeyEscape) {
		v.prConfirmOpen = false
	}

	wrap := ui.Column(c.Key("composer-pr-bar")).FillWidth().Shrink(0).Padding(0, Space4)
	if visible && !v.prBarShown {
		v.prBarShown = true
		if !reduceMotion(c) {
			wrap.Jump("composer-pr-bar/show", 0)
		}
	}
	show := float32(1)
	if !visible {
		show = 0
	}
	k := wrap.AnimateTo("composer-pr-bar/show", show, ui.Animation{
		Duration: animateDuration(c, DurBase),
		Ease:     motionEaseStandard,
	})
	if !visible && k == 0 {
		wrap.Hidden(true)
		v.prBarShown = false
		return
	}
	// 与 renderComposerPlanBar 一致：负下边距抵掉列间距并让输入卡片（或下一条）盖住底部重叠区。
	wrap.Margin(0, 0, -(Space3 + planBarOverlap*k), 0)
	wrap.Children(func() {
		strip := ui.Column(c.Key("composer-pr-bar-strip")).
			FillWidth().
			Shrink(0).
			Padding(0, 0, planBarOverlap, 0).
			Radius(RadiusLG, RadiusLG, 0, 0).
			Border(Hairline, ThemeBorder(c)).
			BorderWidth(Hairline, Hairline, 0, Hairline).
			Background(ThemeOverlayHover(c).Over(ThemeSurface(c)))

		strip.Children(func() {
			if snap.merging || snap.mergeErr != "" {
				renderComposerPRFlowBar(c, s, threadID, snap)
			} else {
				renderComposerPRIdleBar(c, s, threadID, snap)
			}
		})

		if h := strip.LayoutBounds().H; h > 0 && k < 1 {
			wrap.ClipY().Height(k * h).Opacity(k)
		} else if k < 1 {
			wrap.Opacity(k)
		}
	})
}

func renderComposerPRIdleBar(c *ui.Context, s *chatState, threadID string, snap composerPRThreadSnapshot) {
	v := &s.composerView
	prNumText := fmt.Sprintf("#%d", snap.state.Number)

	ui.Row(c.Key("composer-pr-bar-head")).
		Label("PR "+prNumText).
		FillWidth().
		Height(ControlLG).
		Padding(0, Space3, 0, Space4).
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Row(c.Key("composer-pr-bar-left")).
				Grow(1).
				MinWidth(0).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconGitPullRequest()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					ui.Text(c, prNumText).
						FontSize(uiFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Shrink(0)
					if snap.state.LocalCommits > 0 {
						ui.Row(c.Key("composer-pr-local-commits")).
							Padding(Space0, Space2).
							Radius(RadiusPill).
							Background(ThemeAccent(c).Alpha(0.12)).
							Shrink(0).
							Children(func() {
								ui.Text(c, fmt.Sprintf("本地 +%d 提交", snap.state.LocalCommits)).
									FontSize(uiFont(TypeCaption)).
									FontWeight(500).
									TextColor(ThemeAccent(c)).
									SingleLine()
							})
					}
					if snap.state.CrossRepo {
						ui.Row(c.Key("composer-pr-fork-badge")).
							Padding(Space0, Space2).
							Radius(RadiusPill).
							Background(ThemeWarning(c).Alpha(0.12)).
							Shrink(0).
							Children(func() {
								ui.Text(c, "Fork").
									FontSize(uiFont(TypeCaption)).
									FontWeight(500).
									TextColor(ThemeWarning(c)).
									SingleLine()
							})
					}
				})

			btnText, btnDisabled, btnVariant := composerPRPrimaryButtonSpec(snap.state)
			mergeBtn := Button(c, ButtonOpts{
				Key:      "composer-pr-merge-btn",
				Text:     btnText,
				Variant:  btnVariant,
				Size:     ControlSM,
				Disabled: btnDisabled,
				OnClick: func() {
					if togglePopoverOpen(&v.prConfirmOpen) {
						refreshComposerPRThreadState(s, threadID)
					}
				},
			})

			anchoredPopover(c, mergeBtn, anchoredPopoverProps{
				Key:       "composer-pr-merge-popover",
				Open:      &v.prConfirmOpen,
				Align:     AboveEnd,
				MaxWidth:  composerPRPopoverMaxWidth,
				OnDismiss: func() { v.prConfirmOpen = false },
			}, func(_ ui.Element) {
				renderComposerPRMergePopover(c, s, threadID, snap)
			})
		})
}

func renderComposerPRMergePopover(c *ui.Context, s *chatState, threadID string, snap composerPRThreadSnapshot) {
	v := &s.composerView
	method := gitPRCurrentMergeMethod(snap.mergeMethod)

	popoverCard(c, ui.Column(c.Key("composer-pr-merge-card")).Width(composerPRPopoverMaxWidth).Padding(Space4).Gap(Space3)).Children(func() {
		ui.Row(c.Key("composer-pr-merge-popover-head")).
			FillWidth().
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(Space2).
			Children(func() {
				ui.Text(c, fmt.Sprintf("合并 PR #%d", snap.state.Number)).
					FontSize(uiFont(TypeHeading)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				if snap.state.CrossRepo {
					ui.Row(c.Key("composer-pr-merge-popover-fork")).
						Padding(Space0, Space2).
						Radius(RadiusPill).
						Background(ThemeWarning(c).Alpha(0.12)).
						Children(func() {
							ui.Text(c, "Fork").
								FontSize(uiFont(TypeCaption)).
								FontWeight(500).
								TextColor(ThemeWarning(c)).
								SingleLine()
						})
				}
			})

		ui.Row(c.Key("composer-pr-merge-methods")).
			FillWidth().
			Padding(Space0).
			Gap(Space0).
			Radius(RadiusSM).
			Background(ThemeOverlayHover(c)).
			Children(func() {
				for _, m := range []string{"squash", "merge", "rebase"} {
					methodVal := m
					active := method == methodVal
					btn := ui.ButtonBase(c.Key("composer-pr-method-" + methodVal)).
						Label(gitPRMergeMethodShortLabel(methodVal)).
						Grow(1).
						Height(ControlSM).
						Center().
						Radius(RadiusXS).
						Transition(motionFeedback(c)).
						OnClick(func() {
							composerPRBarMu.Lock()
							st := getComposerPRBarStateLocked(s)
							entry := getComposerPRThreadEntryLocked(st, threadID)
							entry.mergeMethod = methodVal
							composerPRBarMu.Unlock()
							s.invalidate()
						})
					fg := ThemeSecondaryText(c)
					if active {
						btn.Background(ThemeSurface(c)).Border(Hairline, ThemeHairline(c))
						fg = ThemePrimaryText(c)
					} else if btn.Hovered() {
						btn.Background(ThemeOverlayHover(c))
						fg = ThemePrimaryText(c)
					}
					focusRing(c, btn)
					btn.Children(func() {
						ui.Text(c, gitPRMergeMethodShortLabel(methodVal)).
							FontSize(uiFont(TypeFootnote)).
							FontWeight(500).
							TextColor(fg).
							SingleLine()
					})
				}
			})

		commitSummary := "无本地新增提交，将直接合并远端 PR"
		summaryColor := ThemeSecondaryText(c)
		switch {
		case snap.state.Dirty:
			commitSummary = "有未提交的改动，请先提交后再合并"
			summaryColor = ThemeWarning(c)
		case snap.state.LocalCommits > 0 && snap.state.CrossRepo:
			commitSummary = fmt.Sprintf("Fork PR：将合并原 PR 并带入 %d 个本地提交", snap.state.LocalCommits)
		case snap.state.LocalCommits > 0:
			commitSummary = fmt.Sprintf("将推送 %d 个本地提交到 PR 分支", snap.state.LocalCommits)
		}
		ui.Row(c.Key("composer-pr-merge-commits")).
			FillWidth().
			Padding(Space2, Space3).
			Radius(RadiusSM).
			Border(Hairline, ThemeHairline(c)).
			Background(ThemeSurface(c)).
			Children(func() {
				ui.Text(c, commitSummary).
					FontSize(uiFont(TypeFootnote)).
					TextColor(summaryColor)
			})

		confirmText := "确认合并"
		if snap.state.LocalCommits > 0 && !snap.state.CrossRepo {
			confirmText = "确认推送并合并"
		}
		ui.Row(c.Key("composer-pr-merge-actions")).
			FillWidth().
			Justify(ui.End).
			Gap(Space2).
			Children(func() {
				Button(c, ButtonOpts{
					Key:     "composer-pr-merge-cancel",
					Text:    "取消",
					Variant: ButtonGhost,
					Size:    ControlSM,
					OnClick: func() { v.prConfirmOpen = false },
				})
				Button(c, ButtonOpts{
					Key:      "composer-pr-merge-confirm",
					Text:     confirmText,
					Variant:  ButtonPrimary,
					Size:     ControlSM,
					Disabled: snap.state.Dirty || snap.merging,
					OnClick: func() {
						v.prConfirmOpen = false
						startComposerPRMergeFlow(s, threadID, snap.state.Number, method, "")
					},
				})
			})
	})
}

func renderComposerPRFlowBar(c *ui.Context, s *chatState, threadID string, snap composerPRThreadSnapshot) {
	prNum := snap.mergedNumber
	if prNum <= 0 {
		prNum = snap.state.Number
	}

	ui.Column(c.Key("composer-pr-flow-body")).
		FillWidth().
		Padding(Space2, Space3, Space2, Space4).
		Gap(Space2).
		Children(func() {
			ui.Row(c.Key("composer-pr-flow-steps")).
				FillWidth().
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(Space2).
				Children(func() {
					ui.Row(c.Key("composer-pr-flow-step-list")).
						AlignItems(ui.Center).
						Wrap().
						Gap(Space2).
						Grow(1).
						MinWidth(0).
						Children(func() {
							for i, step := range composerPRMergeSteps {
								if i > 0 {
									ui.Text(c, "→").
										FontSize(uiFont(TypeCaption)).
										TextColor(ThemeTertiaryText(c)).
										SingleLine()
								}
								renderComposerPRStepItem(c, step.key, step.label, snap)
							}
						})

					if !snap.merging && snap.mergeErr != "" {
						Button(c, ButtonOpts{
							Key:     "composer-pr-retry-btn",
							Text:    "重试",
							Variant: ButtonPrimary,
							Size:    ControlSM,
							OnClick: func() {
								startComposerPRMergeFlow(s, threadID, prNum, snap.mergeMethod, snap.failedStep)
							},
						})
					}
				})

			if !snap.merging && snap.mergeErr != "" {
				errPrefix := "合并失败"
				if snap.failedStep != "" {
					errPrefix = composerPRStepLabel(snap.failedStep) + "失败"
				}
				ui.Row(c.Key("composer-pr-flow-error")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space2).
					Padding(Space1, Space2).
					Radius(RadiusSM).
					Border(Hairline, ThemeDanger(c).Alpha(0.30)).
					Background(ThemeDanger(c).Alpha(0.12)).
					Children(func() {
						ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
						ui.Text(c, errPrefix+"："+snap.mergeErr).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeDanger(c)).
							Grow(1).
							MinWidth(0)
					})
			}
		})
}

func renderComposerPRStepItem(c *ui.Context, stepKey, stepLabel string, snap composerPRThreadSnapshot) {
	const (
		stepPending = iota
		stepRunning
		stepDone
		stepFailed
	)
	state := stepPending
	switch {
	case snap.completedSteps[stepKey]:
		state = stepDone
	case snap.merging && snap.activeStep == stepKey:
		state = stepRunning
	case !snap.merging && snap.mergeErr != "" && snap.failedStep == stepKey:
		state = stepFailed
	}

	ui.Row(c.Key("composer-pr-step-" + stepKey)).
		AlignItems(ui.Center).
		Gap(Space1).
		Shrink(0).
		Children(func() {
			textColor := ThemeTertiaryText(c)
			switch state {
			case stepDone:
				ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
				textColor = ThemeSecondaryText(c)
			case stepRunning:
				renderRunningGlyph(c, "composer-pr-step-run-"+stepKey, IconSM, ThemeAccent(c))
				textColor = ThemePrimaryText(c)
			case stepFailed:
				ui.Icon(c, IconCircleX()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
				textColor = ThemeDanger(c)
			default:
				ui.Icon(c, IconCircleSmall()).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c))
			}
			ui.Text(c, stepLabel).
				FontSize(uiFont(TypeFootnote)).
				FontWeight(500).
				TextColor(textColor).
				SingleLine()
		})
}
