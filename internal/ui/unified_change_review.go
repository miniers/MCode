package mcodeui

import (
	"fmt"
	"hash/maphash"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

const (
	changeReviewSplitMinWidth     float32 = 520
	changeReviewDefaultMaxExpand          = 3
	changeReviewFitMaxLines               = 300
	changeReviewMaxViewportHeight float32 = 420
)

var changeReviewHashSeed = maphash.MakeSeed()

// changeReviewState 维护统一变更审查组件的视图与展开/已查看/DiffView 缓存状态。
// 所有字段受 mu 保护，可在渲染帧间安全复用 *editor.DiffState。
type changeReviewState struct {
	mu                sync.Mutex
	scopeKey          string
	split             bool
	width             float32
	collapsed         map[string]bool
	viewed            map[string]bool
	diffKey           string
	views             map[string]*editor.DiffState
	expandedDeferred  map[string]git.FileDiff
	expandingDeferred map[string]bool
}

func newChangeReviewState() *changeReviewState {
	return &changeReviewState{
		collapsed: make(map[string]bool),
		viewed:    make(map[string]bool),
		views:     make(map[string]*editor.DiffState),
	}
}

// reset 在切换 Bee / 切换 Task / 重载审查数据时重置逐文件状态与 DiffView 缓存。
func (st *changeReviewState) reset() {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.resetLocked()
	st.mu.Unlock()
}

func (st *changeReviewState) resetLocked() {
	st.scopeKey = ""
	st.collapsed = make(map[string]bool)
	st.viewed = make(map[string]bool)
	st.diffKey = ""
	st.views = make(map[string]*editor.DiffState)
	st.expandedDeferred = nil
	st.expandingDeferred = nil
}

func changeReviewFilesFingerprint(files []git.FileDiff) string {
	if len(files) == 0 {
		return ""
	}
	var h maphash.Hash
	h.SetSeed(changeReviewHashSeed)
	var buf [32]byte
	writeInt := func(v int) {
		b := strconv.AppendInt(buf[:0], int64(v), 10)
		h.Write(b)
		h.WriteByte('|')
	}
	writeInt(len(files))
	for _, f := range files {
		h.WriteString(f.Status)
		h.WriteByte(0)
		h.WriteString(f.OldPath)
		h.WriteByte(0)
		h.WriteString(f.NewPath)
		h.WriteByte(0)
		if f.Binary {
			h.WriteByte('B')
		}
		if f.IsDeferred {
			h.WriteByte('D')
		}
		writeInt(f.Additions)
		writeInt(f.Deletions)
		writeInt(len(f.Hunks))
		for _, hk := range f.Hunks {
			writeInt(hk.OldStart)
			writeInt(hk.OldLines)
			writeInt(hk.NewStart)
			writeInt(hk.NewLines)
			h.WriteString(hk.Header)
			h.WriteByte(0)
			writeInt(len(hk.Lines))
			if len(hk.Lines) > 0 {
				h.WriteByte(hk.Lines[0].Kind)
				h.WriteString(hk.Lines[0].Text)
				h.WriteByte(0)
				last := hk.Lines[len(hk.Lines)-1]
				h.WriteByte(last.Kind)
				h.WriteString(last.Text)
				h.WriteByte(0)
			}
		}
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func (st *changeReviewState) syncFilesLocked(files []git.FileDiff, maxExpandedDefault int) {
	fp := changeReviewFilesFingerprint(files)
	if st.diffKey == fp {
		if st.collapsed == nil {
			st.collapsed = make(map[string]bool)
		}
		if st.viewed == nil {
			st.viewed = make(map[string]bool)
		}
		if st.views == nil {
			st.views = make(map[string]*editor.DiffState)
		}
		return
	}
	st.diffKey = fp
	st.views = make(map[string]*editor.DiffState)
	st.collapsed = make(map[string]bool, len(files))
	st.viewed = make(map[string]bool, len(files))
	st.expandedDeferred = nil
	st.expandingDeferred = nil
	if maxExpandedDefault <= 0 {
		maxExpandedDefault = changeReviewDefaultMaxExpand
	}
	expandAll := len(files) <= maxExpandedDefault
	for i, f := range files {
		path := diffRowFilePath(f)
		st.collapsed[path] = !expandAll && i > 0
	}
}

func (st *changeReviewState) viewForLocked(path string, file git.FileDiff) *editor.DiffState {
	if file.Binary || file.IsDeferred || len(file.Hunks) == 0 {
		return nil
	}
	if st.views == nil {
		st.views = make(map[string]*editor.DiffState)
	}
	if existing, ok := st.views[path]; ok && existing != nil {
		return existing
	}
	view := &editor.DiffState{
		Layout:     editor.DiffUnified,
		Inline:     editor.DiffInlineWordAlt,
		Indicators: editor.DiffClassic,
		Font:       ui.Font{Size: codeFontSize()},
	}
	view.SetPatch(gitFilePatch(file))
	lang := editor.LanguageFor(path)
	if view.Old != nil {
		view.Old.SetLanguage(lang)
	}
	if view.New != nil {
		view.New.SetLanguage(lang)
	}
	st.views[path] = view
	return view
}

func changeReviewFileStats(f git.FileDiff) (adds, dels int) {
	if f.Additions > 0 || f.Deletions > 0 || f.IsDeferred {
		return f.Additions, f.Deletions
	}
	stats := toolDiffFileStatsFor(f)
	return stats.adds, stats.dels
}

func changeReviewTotalStats(files []git.FileDiff) (adds, dels int) {
	for _, f := range files {
		if f.Binary {
			continue
		}
		a, d := changeReviewFileStats(f)
		adds += a
		dels += d
	}
	return adds, dels
}

func changeReviewStatusBadge(c *ui.Context, status string) (string, ui.Color) {
	switch status {
	case "added", "A":
		return "新增", ThemeSuccess(c)
	case "deleted", "D":
		return "删除", ThemeDanger(c)
	case "renamed", "R":
		return "重命名", ThemeAccent(c)
	case "copied", "C":
		return "复制", ThemeAccent(c)
	case "modified", "M":
		return "修改", ThemeWarning(c)
	default:
		return "", ThemeSecondaryText(c)
	}
}

type changeReviewDiffOpts struct {
	Files              []git.FileDiff
	Loading            bool
	LoadingText        string
	Err                string
	ErrPrefix          string
	Truncated          bool
	EmptyText          string
	OnRetry            func()
	RetryKey           string
	RetryLabel         string
	MaxExpandedDefault int
	ShowReadToggle     bool
	ScopeKey           string
	Invalidate         func()
}

func expandChangeReviewDeferredFile(c *ui.Context, st *changeReviewState, path string, file git.FileDiff, invalidate func()) {
	if st == nil || path == "" || !file.IsDeferred {
		return
	}
	st.mu.Lock()
	if _, ok := st.expandedDeferred[path]; ok {
		st.mu.Unlock()
		return
	}
	if st.expandingDeferred == nil {
		st.expandingDeferred = make(map[string]bool)
	}
	if st.expandingDeferred[path] {
		st.mu.Unlock()
		return
	}
	st.expandingDeferred[path] = true
	diffKey := st.diffKey
	st.mu.Unlock()

	if invalidate != nil {
		invalidate()
	} else {
		c.Invalidate()
	}

	go func() {
		expanded, err := git.ExpandDeferred(file)
		st.mu.Lock()
		if st.diffKey != diffKey {
			st.mu.Unlock()
			return
		}
		delete(st.expandingDeferred, path)
		if err == nil {
			if st.expandedDeferred == nil {
				st.expandedDeferred = make(map[string]git.FileDiff)
			}
			st.expandedDeferred[path] = expanded
		}
		st.mu.Unlock()
		if invalidate != nil {
			invalidate()
		}
	}()
}

// renderChangeReviewDiff 渲染共享的现代 DiffView 变更审查列表，支持加载、错误重试、空态、
// 截断横幅、统一/并排切换、全展开/全收起与单文件「已查看」计数。
func renderChangeReviewDiff(c *ui.Context, keyPrefix string, st *changeReviewState, opts changeReviewDiffOpts) {
	if st == nil {
		st = newChangeReviewState()
	}
	triggerInvalidate := func() {
		if opts.Invalidate != nil {
			opts.Invalidate()
		} else {
			c.Invalidate()
		}
	}

	if opts.Loading {
		loadingText := opts.LoadingText
		if loadingText == "" {
			loadingText = "正在加载改动…"
		}
		ui.Row(c.Key(keyPrefix+"-loading-row")).FillWidth().Padding(Space3, 0).Children(func() {
			LoadingIndicator(c, keyPrefix+"-loading", loadingText, IconSM)
		})
		return
	}

	if opts.Err != "" {
		errText := opts.ErrPrefix + opts.Err
		ui.Row(c.Key(keyPrefix+"-err")).
			FillWidth().
			MinWidth(0).
			Wrap().
			Gap(Space3).
			AlignItems(ui.Center).
			Padding(Space3, Space3).
			Radius(RadiusSM).
			Background(ThemeDanger(c).Alpha(0.12)).
			Children(func() {
				ui.Text(c, errText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					MinWidth(0).
					Shrink(1)
				if opts.OnRetry != nil {
					retryKey := opts.RetryKey
					if retryKey == "" {
						retryKey = keyPrefix + "-retry"
					}
					retryLabel := opts.RetryLabel
					if retryLabel == "" {
						retryLabel = "重试"
					}
					Button(c, ButtonOpts{
						Key:     retryKey,
						Text:    "重试",
						Label:   retryLabel,
						Variant: ButtonSecondary,
						Size:    ControlSM,
						OnClick: opts.OnRetry,
					})
				}
			})
		return
	}

	if len(opts.Files) == 0 {
		emptyText := opts.EmptyText
		if emptyText == "" {
			emptyText = "无改动"
		}
		ui.Row(c.Key(keyPrefix+"-empty")).
			FillWidth().
			Padding(Space4, 0).
			Children(func() {
				ui.Text(c, emptyText).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemeSecondaryText(c))
			})
		return
	}

	st.mu.Lock()
	if opts.ScopeKey != "" && st.scopeKey != "" && st.scopeKey != opts.ScopeKey {
		st.resetLocked()
	}
	if opts.ScopeKey != "" {
		st.scopeKey = opts.ScopeKey
	}
	st.syncFilesLocked(opts.Files, opts.MaxExpandedDefault)
	split := st.split
	cachedWidth := st.width
	collapsed := make([]bool, len(opts.Files))
	viewed := make([]bool, len(opts.Files))
	resolved := make([]git.FileDiff, len(opts.Files))
	expanding := make([]bool, len(opts.Files))
	viewedCount := 0
	allExpanded := true
	allCollapsed := true
	for i, f := range opts.Files {
		path := diffRowFilePath(f)
		if exp, ok := st.expandedDeferred[path]; ok {
			resolved[i] = exp
		} else {
			resolved[i] = f
		}
		expanding[i] = st.expandingDeferred[path]
		collapsed[i] = st.collapsed[path]
		if collapsed[i] {
			allExpanded = false
		} else {
			allCollapsed = false
		}
		viewed[i] = st.viewed[path]
		if viewed[i] {
			viewedCount++
		}
	}
	st.mu.Unlock()

	root := ui.Column(c.Key(keyPrefix + "-root")).FillWidth().MinWidth(0).Gap(Space2)
	root.Draw(func(_ *ui.Painter, r ui.Rect) {
		w := float32(math.Round(float64(r.W)))
		if w <= 0 {
			return
		}
		st.mu.Lock()
		oldW := st.width
		changed := oldW != w
		if changed {
			st.width = w
		}
		splitPref := st.split
		st.mu.Unlock()
		if changed && splitPref && ((oldW < changeReviewSplitMinWidth) != (w < changeReviewSplitMinWidth)) {
			c.Invalidate()
		}
	})

	availWidth := cachedWidth
	if availWidth <= 0 {
		availWidth = root.Bounds().W
	}
	if availWidth <= 0 {
		winW, _ := c.Size()
		availWidth = winW
	}
	useSplit := split && availWidth >= changeReviewSplitMinWidth

	totalAdds, totalDels := changeReviewTotalStats(resolved)

	root.Children(func() {
		if opts.Truncated {
			ui.Row(c.Key(keyPrefix+"-truncated")).
				FillWidth().
				MinWidth(0).
				Gap(Space2).
				AlignItems(ui.Center).
				Padding(Space2, Space3).
				Radius(RadiusSM).
				Background(ThemeWarning(c).Alpha(0.12)).
				Children(func() {
					ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeWarning(c)).Shrink(0)
					ui.Text(c, "改动过多，已截断").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeWarning(c)).
						MinWidth(0).
						Shrink(1)
				})
		}

		// 紧凑共享工具条
		ui.Row(c.Key(keyPrefix+"-toolbar")).
			FillWidth().
			MinWidth(0).
			Wrap().
			AlignItems(ui.Center).
			Gap(Space2).
			Padding(Space1, 0).
			Children(func() {
				ui.Text(c, fmt.Sprintf("%d 个文件", len(opts.Files))).
					FontSize(uiFont(TypeFootnote)).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					SingleLine().
					Shrink(0)
				if totalAdds > 0 {
					ui.Text(c, fmt.Sprintf("+%d", totalAdds)).
						Font("monospace").
						FontSize(uiFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemeSuccess(c)).
						SingleLine().
						Shrink(0)
				}
				if totalDels > 0 {
					ui.Text(c, fmt.Sprintf("-%d", totalDels)).
						Font("monospace").
						FontSize(uiFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ThemeDanger(c)).
						SingleLine().
						Shrink(0)
				}
				if opts.ShowReadToggle {
					ui.Text(c, fmt.Sprintf("已查看 %d/%d", viewedCount, len(opts.Files))).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						Shrink(0)
				}

				ui.Spacer(c)

				unifiedVariant, splitVariant := ButtonSecondary, ButtonGhost
				if split {
					unifiedVariant, splitVariant = ButtonGhost, ButtonSecondary
				}
				Button(c, ButtonOpts{
					Key:     keyPrefix + "-mode-unified",
					Text:    "统一",
					Label:   "统一",
					Tooltip: "统一视图",
					Variant: unifiedVariant,
					Size:    ControlXS,
					OnClick: func() {
						st.mu.Lock()
						st.split = false
						st.mu.Unlock()
						triggerInvalidate()
					},
				})
				splitTooltip := "并排对比视图"
				if availWidth < changeReviewSplitMinWidth {
					splitTooltip = "并排对比（当前宽度不足，自动降级统一视图）"
				}
				Button(c, ButtonOpts{
					Key:     keyPrefix + "-mode-split",
					Text:    "并排",
					Label:   "并排",
					Tooltip: splitTooltip,
					Variant: splitVariant,
					Size:    ControlXS,
					OnClick: func() {
						st.mu.Lock()
						st.split = true
						st.mu.Unlock()
						triggerInvalidate()
					},
				})

				Button(c, ButtonOpts{
					Key:      keyPrefix + "-expand-all",
					Text:     "全部展开",
					Label:    "全部展开",
					Variant:  ButtonGhost,
					Size:     ControlXS,
					Disabled: allExpanded,
					OnClick: func() {
						st.mu.Lock()
						if st.collapsed == nil {
							st.collapsed = make(map[string]bool)
						}
						for _, f := range opts.Files {
							st.collapsed[diffRowFilePath(f)] = false
						}
						st.mu.Unlock()
						triggerInvalidate()
					},
				})
				Button(c, ButtonOpts{
					Key:      keyPrefix + "-collapse-all",
					Text:     "全部收起",
					Label:    "全部收起",
					Variant:  ButtonGhost,
					Size:     ControlXS,
					Disabled: allCollapsed,
					OnClick: func() {
						st.mu.Lock()
						if st.collapsed == nil {
							st.collapsed = make(map[string]bool)
						}
						for _, f := range opts.Files {
							st.collapsed[diffRowFilePath(f)] = true
						}
						st.mu.Unlock()
						triggerInvalidate()
					},
				})
			})

		// 逐文件现代 DiffView 卡片
		for i, file := range resolved {
			i := i
			file := file
			path := diffRowFilePath(file)
			isOpen := !collapsed[i]
			isRead := viewed[i]
			isExpanding := expanding[i]
			adds, dels := changeReviewFileStats(file)
			cardKey := fmt.Sprintf("%s-card-%d-%s", keyPrefix, i, path)

			ui.Column(c.Key(cardKey)).
				FillWidth().
				MinWidth(0).
				Radius(RadiusMD).
				Background(ThemeSurface(c)).
				Border(Hairline, ThemeBorder(c)).
				Clip().
				Children(func() {
					head := ui.Row(c.Key(cardKey + "-head")).
						Label(path).
						Tooltip(path).
						FillWidth().
						MinWidth(0).
						Height(reviewCardHeadHeight).
						Padding(0, Space2, 0, Space2).
						Gap(Space2).
						AlignItems(ui.Center).
						Focusable()
					hovered := head.Hovered()
					if hovered {
						head.Background(ThemeSurface(c).Mix(ThemePrimaryText(c), 0.04))
					}
					if isOpen {
						head.BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c))
					}
					focusRing(c, head)
					head.OnClick(func() {
						st.mu.Lock()
						if st.collapsed == nil {
							st.collapsed = make(map[string]bool)
						}
						st.collapsed[path] = isOpen
						st.mu.Unlock()
						triggerInvalidate()
					})

					target := float32(0)
					if isOpen {
						target = 1
					}
					if reduceMotion(c) {
						head.Jump("open", target)
					}
					turn := head.AnimateTo("open", target, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})

					head.Children(func() {
						ui.Icon(c, IconChevronRight()).
							Size(IconSM, IconSM).
							TextColor(ThemeTertiaryText(c)).
							Rotate(90 * turn).
							Shrink(0)

						if statusText, statusColor := changeReviewStatusBadge(c, file.Status); statusText != "" {
							ui.Text(c, statusText).
								FontSize(uiFont(TypeCaption)).
								FontWeight(500).
								TextColor(statusColor).
								SingleLine().
								Shrink(0)
						}

						pathColor := ThemePrimaryText(c)
						if isRead {
							pathColor = ThemeTertiaryText(c)
						}
						ui.Text(c, path).
							Font("monospace").
							FontSize(uiFont(TypeFootnote)).
							TextColor(pathColor).
							SingleLine().
							MinWidth(0).
							Grow(1).
							Shrink(1).
							Ellipsis("…").
							Tooltip(path)

						if adds > 0 {
							ui.Text(c, fmt.Sprintf("+%d", adds)).
								Font("monospace").
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeSuccess(c)).
								SingleLine().
								Shrink(0)
						}
						if dels > 0 {
							ui.Text(c, fmt.Sprintf("-%d", dels)).
								Font("monospace").
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeDanger(c)).
								SingleLine().
								Shrink(0)
						}

						if opts.ShowReadToggle {
							renderReviewReadButton(c, path, isRead, hovered, func() {
								st.mu.Lock()
								if st.viewed == nil {
									st.viewed = make(map[string]bool)
								}
								st.viewed[path] = !isRead
								st.mu.Unlock()
								triggerInvalidate()
							})
						}
					})

					motionUnfoldBody(c, cardKey+"-body", isOpen, func() {
						switch {
						case file.Binary:
							ui.Row(c.Key(cardKey+"-binary")).FillWidth().Padding(Space3, Space4).Children(func() {
								ui.Text(c, "二进制文件").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c))
							})
						case file.IsDeferred:
							ui.Row(c.Key(cardKey+"-deferred")).FillWidth().Padding(Space3, Space4).Gap(Space2).AlignItems(ui.Center).Children(func() {
								if isExpanding {
									LoadingIndicator(c, cardKey+"-expanding", "正在展开改动…", IconSM)
									return
								}
								ui.Text(c, "改动较大，已延迟展开").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									Grow(1).
									MinWidth(0)
								Button(c, ButtonOpts{
									Key:     cardKey + "-expand-btn",
									Text:    "展开改动",
									Label:   "展开改动",
									Variant: ButtonSecondary,
									Size:    ControlSM,
									OnClick: func() {
										expandChangeReviewDeferredFile(c, st, path, file, opts.Invalidate)
									},
								})
							})
						case len(file.Hunks) == 0:
							ui.Row(c.Key(cardKey+"-no-hunks")).FillWidth().Padding(Space3, Space4).Children(func() {
								ui.Text(c, "无内容改动").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c))
							})
						default:
							st.mu.Lock()
							view := st.viewForLocked(path, file)
							st.mu.Unlock()
							if view == nil {
								return
							}
							layout := editor.DiffUnified
							if useSplit {
								layout = editor.DiffSplit
							}
							view.Layout = layout
							view.Font.Size = codeFontSize()
							fit := diffFileLineCount(file) <= changeReviewFitMaxLines
							view.FitContent = fit
							el := editor.DiffView(c.Key(cardKey+"-diffview"), view).FillWidth()
							if !fit {
								el.Height(changeReviewMaxViewportHeight)
							}
						}
					})
				})
		}
	})
}

type changeReviewBannerOpts struct {
	SummaryRow    func()
	WarningNotice string
	MetaText      string
	ReasonText    string
	ConcernsTitle string
	Concerns      []string
	AcceptanceBee *SwarmBee
	ErrorLines    []string
	FooterAction  func()
}

// renderChangeReviewBanner 渲染结构化的审查摘要横幅：统计与状态行、警告/决定层/审查意见/疑点列表、
// 验收命令进度或结果摘要、以及错误与辅助操作入口。
func renderChangeReviewBanner(c *ui.Context, key string, opts changeReviewBannerOpts) {
	ui.Column(c.Key(key)).
		FillWidth().
		MinWidth(0).
		Padding(Space3, Space3).
		Radius(RadiusSM).
		Background(ThemeSurface(c)).
		BorderWidth(1).
		BorderColor(ThemeBorder(c)).
		Gap(Space2).
		Children(func() {
			if opts.SummaryRow != nil {
				opts.SummaryRow()
			}
			if opts.WarningNotice != "" {
				ui.Text(c, opts.WarningNotice).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					Wrap().
					MinWidth(0)
			}
			if opts.MetaText != "" {
				ui.Text(c, opts.MetaText).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Wrap().
					MinWidth(0)
			}
			if opts.ReasonText != "" {
				ui.Text(c, opts.ReasonText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					Wrap().
					MinWidth(0)
			}
			if len(opts.Concerns) > 0 {
				if opts.ConcernsTitle != "" {
					ui.Text(c, opts.ConcernsTitle).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				}
				for _, concern := range opts.Concerns {
					ui.Text(c, "· "+concern).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						Wrap().
						MinWidth(0)
				}
			}
			if opts.AcceptanceBee != nil && len(opts.AcceptanceBee.Acceptance) > 0 {
				if opts.AcceptanceBee.AcceptanceStep > 0 && opts.AcceptanceBee.AcceptanceStep <= len(opts.AcceptanceBee.Acceptance) {
					renderAcceptanceProgress(c, *opts.AcceptanceBee)
				} else {
					ui.Row(c.Key(key+"-acceptance-summary")).
						FillWidth().
						MinWidth(0).
						Gap(Space1).
						AlignItems(ui.Center).
						Children(func() {
							ui.Icon(c, IconTerminal()).Size(11, 11).TextColor(ThemeSecondaryText(c)).Shrink(0)
							ui.Text(c, fmt.Sprintf("验收命令（%d 条）：%s", len(opts.AcceptanceBee.Acceptance), strings.Join(opts.AcceptanceBee.Acceptance, " · "))).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								Wrap().
								MinWidth(0)
						})
				}
			}
			if opts.FooterAction != nil {
				opts.FooterAction()
			}
			for _, errLine := range opts.ErrorLines {
				if errLine == "" {
					continue
				}
				ui.Text(c, errLine).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					Wrap().
					MinWidth(0)
			}
		})
}

type changeReviewApprovalAction struct {
	Key      any
	Text     string
	Label    string
	Variant  ButtonVariant
	Size     float32
	Disabled bool
	Loading  bool
	Tooltip  string
	OnClick  func()
}

type changeReviewApprovalBarOpts struct {
	ShowFeedback        bool
	FeedbackKey         string
	FeedbackLabel       string
	FeedbackPlaceholder string
	Feedback            *string
	ErrorText           string
	Busy                bool
	BusyKey             string
	BusyText            string
	PrimaryFirst        bool
	Primary             *changeReviewApprovalAction
	Secondary           *changeReviewApprovalAction
}

// renderChangeReviewApprovalBar 渲染统一的底部审批动作条：可选反馈输入框、错误行与主/次操作按钮。
func renderChangeReviewApprovalBar(c *ui.Context, key string, opts changeReviewApprovalBarOpts) {
	ui.Column(c.Key(key+"-approval-bar")).
		FillWidth().
		MinWidth(0).
		Gap(Space3).
		Children(func() {
			if opts.ShowFeedback && opts.Feedback != nil {
				in := ui.TextInput(c.Key(opts.FeedbackKey), opts.Feedback).FillWidth()
				if opts.FeedbackLabel != "" {
					in.Label(opts.FeedbackLabel)
				}
				if opts.FeedbackPlaceholder != "" {
					in.Placeholder(opts.FeedbackPlaceholder)
				}
			}
			if opts.ErrorText != "" {
				ui.Text(c, opts.ErrorText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					Wrap().
					MinWidth(0)
			}
			if opts.Busy {
				busyText := opts.BusyText
				if busyText == "" {
					busyText = "处理中…"
				}
				busyKey := opts.BusyKey
				if busyKey == "" {
					busyKey = key + "-approval-busy"
				}
				ui.Row(c.Key(key+"-busy-row")).
					Gap(Space1).
					AlignItems(ui.Center).
					Shrink(0).
					Children(func() {
						renderRunningGlyph(c, busyKey, 12, ThemeAccent(c))
						ui.Text(c, busyText).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					})
				return
			}
			if opts.Primary != nil || opts.Secondary != nil {
				renderAction := func(a *changeReviewApprovalAction) {
					if a == nil {
						return
					}
					Button(c, ButtonOpts{
						Key:      a.Key,
						Text:     a.Text,
						Label:    a.Label,
						Variant:  a.Variant,
						Size:     a.Size,
						Disabled: a.Disabled,
						Loading:  a.Loading,
						Tooltip:  a.Tooltip,
						OnClick:  a.OnClick,
					})
				}
				ui.Row(c.Key(key+"-review-actions")).
					Gap(Space3).
					AlignItems(ui.Center).
					Wrap().
					Children(func() {
						if opts.PrimaryFirst {
							renderAction(opts.Primary)
							renderAction(opts.Secondary)
						} else {
							renderAction(opts.Secondary)
							renderAction(opts.Primary)
						}
					})
			}
		})
}
