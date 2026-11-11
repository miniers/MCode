package mcodeui

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const reviewDiffCacheLimit = 8
const reviewWideWidth float32 = 640

// 键含工作区和两端版本；不同轮次、作用域不能误用相同路径的正文。
type reviewDiffKey struct{ workspace, threadID, scope, turnID, base, head, path, surface string }
type reviewContentEntry struct {
	loading bool
	err     string
	content *ReviewFileContent
}
type reviewDiffView struct {
	state   *editor.DiffState
	restore bool
	anchor  int
	side    editor.DiffSide
	offset  float64
}

func reviewFileReason(file ReviewFileContent) string {
	switch {
	case file.Binary:
		return "二进制文件"
	case file.TooLarge:
		return "文件过大"
	case file.Missing:
		return "文件版本不可用"
	}
	return ""
}

func newReviewDiffView(file ReviewFileContent, language string) *reviewDiffView {
	if reviewFileReason(file) != "" {
		return nil
	}
	old, next := editor.NewBuffer(file.OldText), editor.NewBuffer(file.NewText)
	lang := editor.LanguageNamed(language)
	if lang == nil {
		lang = editor.LanguageFor(file.Path)
	}
	old.SetLanguage(lang)
	next.SetLanguage(lang)
	return &reviewDiffView{state: &editor.DiffState{Old: old, New: next, Layout: editor.DiffUnified, Inline: editor.DiffInlineWordAlt, Indicators: editor.DiffClassic, Font: ui.Font{Size: codeFontSize()}}}
}

// 布局切换保留同一全文状态，并用新侧行锚点恢复定位，不能沿用布局相关的像素距离。
func (v *reviewDiffView) layout(split bool, width float32) {
	layout := editor.DiffUnified
	if split && width >= reviewWideWidth {
		layout = editor.DiffSplit
	}
	d := v.state
	if d.Layout == layout {
		return
	}
	y := d.ScrollY()
	bestTop := -1.0
	for _, side := range []editor.DiffSide{editor.DiffOld, editor.DiffNew} {
		buffer := d.Old
		if side == editor.DiffNew {
			buffer = d.New
		}
		lo, hi := 0, max(0, buffer.LineCount()-1)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if d.LineTop(side, mid) <= y {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		top := d.LineTop(side, lo)
		if top <= y && top > bestTop {
			bestTop = top
			v.anchor, v.side = lo, side
		}
	}
	if bestTop < 0 {
		bestTop = 0
		v.anchor, v.side = 0, editor.DiffNew
	}
	v.offset, v.restore = y-bestTop, true
	d.Layout = layout
	d.SetScrollY(y)
}

func renderReviewDiffView(c *ui.Context, key string, view *reviewDiffView, split bool, width float32) ui.Element {
	if view.restore {
		view.state.SetScrollY(view.state.LineTop(view.side, view.anchor) + view.offset)
		view.restore = false
	}
	view.layout(split, width)
	if view.restore {
		c.Invalidate()
	}
	view.state.Font.Size = TypeCode
	return editor.DiffView(c.Key(key), view.state).Grow(1).MinHeight(0).FillWidth()
}

func reviewKey(s *chatState, threadID string, snapshot ReviewSnapshot, path string) reviewDiffKey {
	return reviewDiffKey{workspace: reviewWorkspace(s, threadID), threadID: threadID, scope: reviewScope(snapshot.Scope), turnID: snapshot.TurnID, base: snapshot.BaseRevision, head: snapshot.HeadRevision, path: path}
}

// 异步评论固定来源会话，不能从当前选中的另一个会话推导工作区。
func reviewWorkspace(s *chatState, threadID string) string {
	s.mu.Lock()
	var projectID, main string
	for _, thread := range s.threads {
		if thread.ID == threadID {
			projectID = thread.ProjectID
			break
		}
	}
	for _, project := range s.projects {
		if projectID != "" && project.ID == projectID {
			main = project.Path
			break
		}
	}
	s.mu.Unlock()
	root := main
	if s.client != nil {
		if dir, ok := s.client.ThreadWorkDir(threadID); ok {
			root = dir
		}
	}
	return root + "\x00" + main
}

func touchReviewKey(order []reviewDiffKey, key reviewDiffKey) []reviewDiffKey {
	for i, k := range order {
		if k == key {
			order = append(order[:i], order[i+1:]...)
			break
		}
	}
	return append(order, key)
}

// 此缓存只由 UI 线程使用；隐藏面板不会创建或保留无上限的 Buffer。
func reviewViewFor(dp *diffPanelState, key reviewDiffKey, content ReviewFileContent, language string) *reviewDiffView {
	if reviewFileReason(content) != "" {
		return nil
	}
	if dp.views == nil {
		dp.views = map[reviewDiffKey]*reviewDiffView{}
	}
	view := dp.views[key]
	if view == nil {
		view = newReviewDiffView(content, language)
		dp.views[key] = view
	}
	dp.viewOrder = touchReviewKey(dp.viewOrder, key)
	for len(dp.viewOrder) > reviewDiffCacheLimit {
		delete(dp.views, dp.viewOrder[0])
		dp.viewOrder = dp.viewOrder[1:]
	}
	return view
}

// 正文只在文件选中或时间线片段实际展开后读取。后台不接触 editor 状态。
func reviewContentFor(s *chatState, dp *diffPanelState, key reviewDiffKey) reviewContentEntry {
	diffPanelMu.Lock()
	if entry := dp.contents[key]; entry != nil {
		dp.contentOrder = touchReviewKey(dp.contentOrder, key)
		copy := *entry
		diffPanelMu.Unlock()
		return copy
	}
	if dp.contents == nil {
		dp.contents = map[reviewDiffKey]*reviewContentEntry{}
	}
	entry := &reviewContentEntry{loading: true}
	dp.contents[key] = entry
	dp.contentOrder = touchReviewKey(dp.contentOrder, key)
	for len(dp.contentOrder) > reviewDiffCacheLimit {
		delete(dp.contents, dp.contentOrder[0])
		dp.contentOrder = dp.contentOrder[1:]
	}
	gen := dp.requestGen
	diffPanelMu.Unlock()
	client, ok := s.client.(reviewClient)
	if !ok {
		return reviewContentEntry{err: "文件版本不可用"}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := client.ReviewGet(ctx, key.threadID, key.scope, key.turnID, key.path)
		result := reviewContentEntry{}
		switch {
		case err != nil:
			result.err = err.Error()
		case snapshot.Unavailable != "":
			result.err = snapshot.Unavailable
		case snapshot.HeadRevision != key.head || snapshot.BaseRevision != key.base:
			result.err = "改动已更新，请刷新"
		case snapshot.File == nil || snapshot.File.Path != key.path:
			result.err = "文件版本不可用"
		default:
			content := *snapshot.File
			result.content = &content
		}
		diffPanelMu.Lock()
		if dp.requestGen == gen && dp.contents[key] == entry {
			*entry = result
		}
		diffPanelMu.Unlock()
		s.invalidate()
	}()
	return reviewContentEntry{loading: true}
}

func reviewBusy(c *ui.Context, key string) {
	if reduceMotion(c) {
		ui.Icon(c.Key(key), IconFile()).Size(IconSM, IconSM).Label("正在读取改动")
		return
	}
	LoadingIndicator(c, key, "", IconSM)
}

func renderReviewPanel(c *ui.Context, s *chatState, dp *diffPanelState) {
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	diffPanelMu.Lock()
	needLoad := dp.threadID != tid || dp.requestGen == 0
	diffPanelMu.Unlock()
	if needLoad {
		s.loadDiff(tid, "")
	}
	diffPanelMu.Lock()
	snapshot, loading, errMsg := dp.snapshot, dp.loading, dp.err
	turn, path := dp.turnID, dp.selectedPath
	diffPanelMu.Unlock()
	ui.Column(c.Key("review-panel")).Fill().Background(ThemeWindowBg(c)).Draw(func(_ *ui.Painter, r ui.Rect) {
		if dp.reviewWidth != r.W {
			dp.reviewWidth = r.W
			c.Invalidate()
		}
	}).Children(func() {
		renderDiffHeader(c, s, dp, tid, turn, reviewTurns(s))
		renderReviewCommentsAction(c, s, dp, tid)
		if loading {
			ui.Row(c.Key("review-busy")).Height(Space4).Children(func() { reviewBusy(c, "review-loading") })
		}
		switch {
		case tid == "":
			renderDiffEmptyState(c)
			return
		case errMsg != "":
			renderDiffStatusState(c, "review-error", IconOctagonAlert(), errMsg, "", func() { s.loadDiff(tid, turn) })
			return
		case snapshot.Unavailable != "":
			renderDiffStatusState(c, "review-unavailable", iconFileSearch, snapshot.Unavailable, "", nil)
			return
		case len(snapshot.Files) == 0:
			if !loading {
				renderDiffEmptyState(c)
			}
			return
		}
		wide := dp.reviewWidth >= reviewWideWidth
		ui.Row(c.Key("review-body")).Grow(1).MinHeight(0).FillWidth().Children(func() {
			if wide || path == "" {
				dp.rowsList.Key = func(i int) any { return snapshot.Files[i].Path }
				list := ui.List(c.Key("review-files"), &dp.rowsList, len(snapshot.Files), func(i int) {
					file := snapshot.Files[i]
					row := ui.ButtonBase(c.Key("review-file-" + file.Path)).Label(file.Path).FillWidth().Padding(Space3).Radius(RadiusSM).Transition(motionListItem(c))
					if file.Path == path {
						row.Background(ThemeSelected(c))
					}
					if row.Clicked() && !loading {
						selectReviewFile(s, dp, tid, file.Path)
					}
					row.Children(func() {
						ui.Text(c, file.Path).FontSize(TypeFootnote).SingleLine().TextColor(ThemePrimaryText(c))
						renderDiffStatusBadge(c, file.Status)
						renderReviewReadButton(c, s, dp, tid, snapshot, file, "list")
					})
				}).FillHeight().MinHeight(0)
				if wide {
					list.Width(200).Shrink(0)
				} else {
					list.Grow(1).FillWidth()
				}
			}
			if path != "" {
				ui.Column(c.Key("review-selected-" + path)).Grow(1).MinWidth(0).FillHeight().Transition(motionCollapse(c)).Children(func() {
					ui.Row(c.Key("review-file-toolbar")).FillWidth().Height(diffHeaderHeight).AlignItems(ui.Center).Gap(Space2).Children(func() {
						if !wide {
							btn := ui.ButtonBase(c.Key("review-back")).Label("返回改动列表").Padding(Space2)
							if btn.Clicked() {
								selectReviewFile(s, dp, tid, "")
							}
							btn.Children(func() { ui.Icon(c, IconArrowLeft()).Size(IconSM, IconSM) })
						}
						ui.Text(c, path).SingleLine().Grow(1).FontSize(TypeFootnote)
						for _, file := range snapshot.Files {
							if file.Path == path {
								renderReviewReadButton(c, s, dp, tid, snapshot, file, "header")
								break
							}
						}
						if wide {
							label := "并排"
							if dp.preferSplit {
								label = "统一"
							}
							btn := ui.ButtonBase(c.Key("review-layout")).Label(label).Padding(Space2)
							if btn.Clicked() {
								dp.preferSplit = !dp.preferSplit
								c.Invalidate()
							}
							btn.Children(func() { ui.Text(c, label).FontSize(TypeFootnote) })
						}
					})
					for _, file := range snapshot.Files {
						if file.Path == path {
							renderReviewFile(c, s, dp, tid, snapshot, file, dp.preferSplit, dp.reviewWidth)
							break
						}
					}
				})
			}
		})
	})
}

func renderReviewFile(c *ui.Context, s *chatState, dp *diffPanelState, tid string, snapshot ReviewSnapshot, file ReviewFile, split bool, width float32, surface ...string) {
	metadata := ReviewFileContent{Binary: file.Binary, TooLarge: file.TooLarge, Missing: file.Missing}
	if reason := reviewFileReason(metadata); reason != "" {
		renderDiffStatusState(c, "review-file-unavailable", IconFile(), reason, "", nil)
		return
	}
	key := reviewKey(s, tid, snapshot, file.Path)
	entry := reviewContentFor(s, dp, key)
	if entry.loading {
		reviewBusy(c, "review-file-loading")
		return
	}
	if entry.err != "" {
		renderDiffStatusState(c, "review-content-error", IconOctagonAlert(), entry.err, "", func() { s.loadDiff(tid, snapshot.TurnID) })
		return
	}
	if entry.content == nil {
		return
	}
	if reason := reviewFileReason(*entry.content); reason != "" {
		renderDiffStatusState(c, "review-content-unavailable", IconFile(), reason, "", nil)
		return
	}
	if len(surface) > 0 {
		key.surface = surface[0]
	}
	view := reviewViewFor(dp, key, *entry.content, file.Language)
	configureReviewHunkActions(c, s, dp, tid, snapshot, file, *entry.content, view)
	configureReviewComments(c, s, dp, key, *entry.content, view)
	renderReviewDiffView(c, fmt.Sprintf("review-diff-%s-%s", key.head, key.path), view, split, width)
	renderReviewOrphanComments(c, s, dp, key, *entry.content)
}
