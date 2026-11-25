// PR 审查卡片渲染（```pr-review 围栏代码块）。
package mcodeui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/git"
)

// prReviewItem 表示 pr-review 代码块中的单条审查意见。
type prReviewItem struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Headline string `json:"headline"`
	Impact   string `json:"impact"`
	Fix      string `json:"fix"`
	Patch    string `json:"patch"`
}

// prReviewResult 表示 pr-review 代码块的审查结论与意见列表。
type prReviewResult struct {
	Verdict  string         `json:"verdict"`
	Reason   string         `json:"reason"`
	Summary  string         `json:"summary"`
	Comments []prReviewItem `json:"comments"`
}

type prReviewStatus int

const (
	prReviewStatusPending prReviewStatus = iota
	prReviewStatusAgent
	prReviewStatusSent
	prReviewStatusIgnored
)

type prReviewCardStateKey struct {
	chat       *chatState
	threadID   string
	blockIndex int
}

// shortcut: 状态只在内存，重启后复原为待处理，需要时再持久化。
type prReviewItemStateKey struct {
	chat       *chatState
	threadID   string
	blockIndex int
	itemIndex  int
}

type prReviewItemUIState struct {
	status   prReviewStatus
	open     bool
	openSet  bool
	diffOpen bool
}

var (
	prReviewStateMu sync.Mutex
	prReviewStates  = make(map[prReviewItemStateKey]prReviewItemUIState)
	prReviewBusy    = make(map[prReviewCardStateKey]bool)
)

type indexedPRReviewItem struct {
	index int
	item  prReviewItem
}

func getPRReviewItemState(key prReviewItemStateKey, defaultOpen bool) (prReviewItemUIState, bool) {
	prReviewStateMu.Lock()
	st := prReviewStates[key]
	prReviewStateMu.Unlock()
	open := defaultOpen
	if st.openSet {
		open = st.open
	}
	return st, open
}

func updatePRReviewItemState(key prReviewItemStateKey, fn func(*prReviewItemUIState)) {
	prReviewStateMu.Lock()
	st := prReviewStates[key]
	fn(&st)
	prReviewStates[key] = st
	prReviewStateMu.Unlock()
}

func isPRReviewCardBusy(key prReviewCardStateKey) bool {
	prReviewStateMu.Lock()
	busy := prReviewBusy[key]
	prReviewStateMu.Unlock()
	return busy
}

// prReviewVerdictLabel 返回审查结论对应的胶囊文案；未知结论返回空字符串。
func prReviewVerdictLabel(verdict string) string {
	switch verdict {
	case "merge":
		return "可直接合并"
	case "fix":
		return "修复后合并"
	case "comment":
		return "需作者修改"
	case "close":
		return "不建议合并"
	default:
		return ""
	}
}

func prReviewVerdictTone(verdict string) Tone {
	switch verdict {
	case "merge":
		return ToneSuccess
	case "fix", "comment":
		return ToneWarning
	case "close":
		return ToneDanger
	default:
		return ToneNeutral
	}
}

func prReviewVerdictHeaderBg(c *ui.Context, verdict string) ui.Color {
	switch verdict {
	case "merge":
		return ThemeSuccess(c).Alpha(0.08).Over(ThemeSurfaceCard(c))
	case "fix", "comment":
		return ThemeWarning(c).Alpha(0.08).Over(ThemeSurfaceCard(c))
	case "close":
		return ThemeDanger(c).Alpha(0.08).Over(ThemeSurfaceCard(c))
	default:
		return ThemeHover(c)
	}
}

func normalizePRReviewItem(item prReviewItem) prReviewItem {
	sev := strings.ToLower(strings.TrimSpace(item.Severity))
	if sev != "blocking" {
		sev = "suggestion"
	}
	item.Severity = sev
	item.Path = strings.TrimSpace(item.Path)
	item.Body = strings.TrimSpace(item.Body)
	item.Headline = strings.TrimSpace(item.Headline)
	if item.Headline == "" {
		item.Headline = item.Body
	}
	item.Impact = strings.TrimSpace(item.Impact)
	item.Fix = strings.TrimSpace(item.Fix)
	item.Patch = strings.Trim(item.Patch, "\r\n")
	if strings.TrimSpace(item.Patch) == "" {
		item.Patch = ""
	}
	return item
}

// parsePRReview 解析 pr-review 代码块中的 JSON 对象或旧版 JSON 数组；非法输入返回 false。
func parsePRReview(raw string) (prReviewResult, bool) {
	trimmed := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(trimmed, "["):
		var items []prReviewItem
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil || items == nil {
			return prReviewResult{}, false
		}
		for i := range items {
			items[i] = normalizePRReviewItem(items[i])
		}
		return prReviewResult{Comments: items}, true
	case strings.HasPrefix(trimmed, "{"):
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
			return prReviewResult{}, false
		}
		_, hasVerdict := fields["verdict"]
		_, hasReason := fields["reason"]
		_, hasSummary := fields["summary"]
		_, hasComments := fields["comments"]
		if !hasVerdict && !hasReason && !hasSummary && !hasComments {
			return prReviewResult{}, false
		}
		var res prReviewResult
		if err := json.Unmarshal([]byte(trimmed), &res); err != nil {
			return prReviewResult{}, false
		}
		res.Verdict = strings.TrimSpace(res.Verdict)
		if prReviewVerdictLabel(res.Verdict) == "" {
			res.Verdict = ""
		}
		res.Summary = strings.TrimSpace(res.Summary)
		res.Reason = strings.TrimSpace(res.Reason)
		if res.Reason == "" {
			res.Reason = res.Summary
		}
		for i := range res.Comments {
			res.Comments[i] = normalizePRReviewItem(res.Comments[i])
		}
		return res, true
	default:
		return prReviewResult{}, false
	}
}

func prReviewCountText(blockingCount, suggestionCount int) string {
	switch {
	case blockingCount > 0 && suggestionCount > 0:
		return fmt.Sprintf("%d 阻塞 · %d 建议", blockingCount, suggestionCount)
	case blockingCount > 0:
		return fmt.Sprintf("%d 阻塞", blockingCount)
	case suggestionCount > 0:
		return fmt.Sprintf("%d 建议", suggestionCount)
	default:
		return ""
	}
}

// prReviewLocationText 格式化文件定位文案；line <= 0 时只显示路径。
func prReviewLocationText(path string, line int) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", p, line)
	}
	return p
}

// openPRReviewLocation 在点击文件定位行时解析路径并跳转到文件面板对应行。
func openPRReviewLocation(s *chatState, path string, line int) {
	p := strings.TrimSpace(path)
	if s == nil || p == "" {
		return
	}
	threadID := selectedThreadIDSnapshot(s)
	if threadID == "" {
		return
	}
	workDir := turnFilesWorkDir(s, threadID)
	abs := resolveTurnFilePath(workDir, p)
	if abs == "" {
		return
	}
	first, last := 0, 0
	if line > 0 {
		first, last = line, line
	}
	openTurnFileAt(s, threadID, workDir, turnEditedFile{path: p, abs: abs}, first, last)
}

func formatPRReviewCommentBody(item prReviewItem) string {
	headline := strings.TrimSpace(item.Headline)
	impact := strings.TrimSpace(item.Impact)
	fix := strings.TrimSpace(item.Fix)
	legacyBody := strings.TrimSpace(item.Body)
	if headline == "" && impact == "" && fix == "" && legacyBody != "" {
		return legacyBody
	}
	if impact == "" && fix == "" && legacyBody != "" && headline == legacyBody && item.Severity != "blocking" {
		return legacyBody
	}
	var parts []string
	if headline != "" {
		if item.Severity == "blocking" {
			headline = "[阻塞] " + headline
		}
		parts = append(parts, headline)
	}
	if impact != "" {
		parts = append(parts, "影响："+impact)
	}
	if fix != "" {
		parts = append(parts, "建议："+fix)
	}
	return strings.Join(parts, "\n\n")
}

func formatPRReviewSingleFixPrompt(item prReviewItem) string {
	var b strings.Builder
	b.WriteString("请只修复以下这处 PR 审查问题，完成后运行相关测试：\n")
	if loc := prReviewLocationText(item.Path, item.Line); loc != "" {
		fmt.Fprintf(&b, "- 位置：%s\n", loc)
	}
	if item.Headline != "" {
		fmt.Fprintf(&b, "- 问题：%s\n", item.Headline)
	}
	if item.Fix != "" {
		fmt.Fprintf(&b, "- 建议改法：%s\n", item.Fix)
	}
	return strings.TrimSpace(b.String())
}

func formatPRReviewBlockingFixPrompt(entries []indexedPRReviewItem) string {
	var b strings.Builder
	b.WriteString("请修复以下阻塞合并的 PR 审查问题，完成后运行相关测试：\n")
	for n, entry := range entries {
		fmt.Fprintf(&b, "%d. ", n+1)
		if loc := prReviewLocationText(entry.item.Path, entry.item.Line); loc != "" {
			fmt.Fprintf(&b, "%s：", loc)
		}
		b.WriteString(entry.item.Headline)
		if entry.item.Fix != "" {
			fmt.Fprintf(&b, "（建议改法：%s）", entry.item.Fix)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// 调用方持有 diffPanelMu。
func deleteMatchingPRReviewDraftsLocked(dp *diffPanelState, targets []prReviewItem) {
	if dp == nil || len(targets) == 0 || len(dp.comments) == 0 {
		return
	}
	dp.comments = slices.DeleteFunc(dp.comments, func(comment *reviewComment) bool {
		if comment == nil {
			return false
		}
		for _, target := range targets {
			p := strings.TrimSpace(target.Path)
			if p != "" && target.Line > 0 && comment.key.path == p && comment.lines.Start == target.Line-1 {
				return true
			}
		}
		return false
	})
}

func submitPRReviewCardComments(s *chatState, dp *diffPanelState, tid string, prNumber int, cardKey prReviewCardStateKey, targets []indexedPRReviewItem) {
	if s == nil || dp == nil || tid == "" || prNumber <= 0 {
		return
	}
	client, ok := s.client.(reviewPRClient)
	if !ok {
		s.setToast("PR 审查不可用")
		return
	}
	prComments := make([]git.PRReviewComment, 0, len(targets))
	validTargets := make([]indexedPRReviewItem, 0, len(targets))
	for _, entry := range targets {
		p := strings.TrimSpace(entry.item.Path)
		body := formatPRReviewCommentBody(entry.item)
		if p == "" || entry.item.Line <= 0 || body == "" {
			continue
		}
		prComments = append(prComments, git.PRReviewComment{
			Path: p,
			Line: entry.item.Line,
			Side: "RIGHT",
			Body: body,
		})
		validTargets = append(validTargets, entry)
	}
	if len(prComments) == 0 {
		s.setToast("缺少有效的文件路径或行号，无法发送到 PR")
		return
	}

	prReviewStateMu.Lock()
	if prReviewBusy[cardKey] {
		prReviewStateMu.Unlock()
		return
	}
	prReviewBusy[cardKey] = true
	prReviewStateMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.GitPRSubmitReview(ctx, tid, prNumber, "COMMENT", "", prComments)
		prReviewStateMu.Lock()
		delete(prReviewBusy, cardKey)
		if err == nil {
			for _, entry := range validTargets {
				k := prReviewItemStateKey{
					chat:       cardKey.chat,
					threadID:   cardKey.threadID,
					blockIndex: cardKey.blockIndex,
					itemIndex:  entry.index,
				}
				st := prReviewStates[k]
				st.status = prReviewStatusSent
				prReviewStates[k] = st
			}
		}
		prReviewStateMu.Unlock()
		if err != nil {
			s.setToast("发到 PR 失败：" + err.Error())
			return
		}
		rawItems := make([]prReviewItem, 0, len(validTargets))
		for _, entry := range validTargets {
			rawItems = append(rawItems, entry.item)
		}
		diffPanelMu.Lock()
		deleteMatchingPRReviewDraftsLocked(dp, rawItems)
		diffPanelMu.Unlock()
		s.setToast("已发到 PR")
	}()
}

func renderPRReviewStatusCapsule(c *ui.Context, key string, status prReviewStatus) {
	switch status {
	case prReviewStatusAgent:
		Capsule(c, CapsuleProps{Key: key, Text: "已交给 agent", Tone: ToneAccent, Size: ControlXS})
	case prReviewStatusSent:
		Capsule(c, CapsuleProps{Key: key, Text: "已发到 PR", Icon: IconCheck(), Tone: ToneSuccess, Size: ControlXS})
	case prReviewStatusIgnored:
		Capsule(c, CapsuleProps{Key: key, Text: "已忽略", Tone: ToneNeutral, Size: ControlXS})
	}
}

func renderPRReviewPatchBlock(c *ui.Context, itemKey, patch, codeFont string, diffOpen bool, onToggle func()) {
	ui.Column(c.Key(itemKey+"-patch")).
		FillWidth().
		Border(1, ThemeBorder(c)).
		Radius(RadiusMD).
		Background(ThemeSurfaceMain(c)).
		Clip().
		Children(func() {
			toggleText := "展开"
			if diffOpen {
				toggleText = "收起"
			}
			toggleBtn := ui.ButtonBase(c.Key(itemKey+"-patch-toggle")).
				Label("参考改法").
				FillWidth().
				MinHeight(ControlSM).
				Padding(Space1, Space3).
				AlignItems(ui.Center).
				Cursor(ui.CursorPointer).
				Transition(motionFeedback(c)).
				OnClick(onToggle)
			if toggleBtn.Hovered() {
				toggleBtn.Background(ThemeOverlayHover(c))
			}
			toggleBtn.Children(func() {
				ui.Text(c, "参考改法").
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Unselectable()
				ui.Spacer(c)
				ui.Text(c, toggleText).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeTertiaryText(c)).
					Unselectable()
			})
			if !diffOpen {
				return
			}
			ui.Column(c.Key(itemKey+"-patch-lines")).
				FillWidth().
				Transition(motionCollapse(c)).
				BorderWidth(Hairline, 0, 0, 0).
				BorderColor(ThemeHairline(c)).
				Padding(Space1, 0).
				Children(func() {
					for li, line := range strings.Split(patch, "\n") {
						line = strings.TrimSuffix(line, "\r")
						fg := ThemeTertiaryText(c)
						bg := ui.Transparent
						switch {
						case strings.HasPrefix(line, "+"):
							fg = ThemeSuccess(c)
							bg = ThemeSuccess(c).Alpha(0.12)
						case strings.HasPrefix(line, "-"):
							fg = ThemeDanger(c)
							bg = ThemeDanger(c).Alpha(0.12)
						}
						ui.Box(c.Key(fmt.Sprintf("%s-patch-line-%d", itemKey, li))).
							FillWidth().
							Padding(1, Space3).
							Background(bg).
							Children(func() {
								ui.Text(c, line).
									Font(codeFont).
									FontSize(scaledFont(TypeCode)).
									LineHeight(1.5).
									TextColor(fg).
									FillWidth().
									MinWidth(0).
									Selectable()
							})
					}
				})
		})
}

func renderPRReviewIssueRow(
	c *ui.Context,
	s *chatState,
	dp *diffPanelState,
	cardKey prReviewCardStateKey,
	itemKey string,
	entry indexedPRReviewItem,
	defaultOpen bool,
	firstInGroup bool,
	inPRScope bool,
	prNumber int,
	busy bool,
	codeFont string,
) {
	item := entry.item
	stateKey := prReviewItemStateKey{
		chat:       cardKey.chat,
		threadID:   cardKey.threadID,
		blockIndex: cardKey.blockIndex,
		itemIndex:  entry.index,
	}
	st, itemOpen := getPRReviewItemState(stateKey, defaultOpen)

	row := ui.Column(c.Key(itemKey)).FillWidth()
	if !firstInGroup {
		row.BorderWidth(Hairline, 0, 0, 0).BorderColor(ThemeHairline(c))
	}
	row.Children(func() {
		ui.Column(c.Key(itemKey+"-head")).
			FillWidth().
			Padding(Space2, Space4).
			Gap(Space1).
			Children(func() {
				headline := item.Headline
				if headline == "" {
					headline = prReviewLocationText(item.Path, item.Line)
				}
				toggleBtn := ui.ButtonBase(c.Key(itemKey+"-toggle")).
					Label(headline).
					FillWidth().
					MinHeight(ControlSM).
					Padding(Space0, Space1).
					Radius(RadiusSM).
					Justify(ui.Start).
					AlignItems(ui.Center).
					Gap(Space2).
					Cursor(ui.CursorPointer).
					Transition(motionFeedback(c))
				if toggleBtn.Hovered() {
					toggleBtn.Background(ThemeOverlayHover(c))
				}
				toggleBtn.OnClick(func() {
					updatePRReviewItemState(stateKey, func(cur *prReviewItemUIState) {
						cur.open = !itemOpen
						cur.openSet = true
					})
					c.Invalidate()
				})
				toggleBtn.Children(func() {
					chevron := IconChevronRight()
					if itemOpen {
						chevron = IconChevronDown()
					}
					ui.Icon(c, chevron).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c)).Shrink(0)
					headlineColor := ThemePrimaryText(c)
					if st.status == prReviewStatusIgnored || st.status == prReviewStatusSent {
						headlineColor = ThemeSecondaryText(c)
					}
					ui.Text(c, headline).
						FontSize(scaledFont(TypeBody)).
						LineHeight(1.45).
						TextColor(headlineColor).
						Grow(1).
						MinWidth(0)
					if st.status != prReviewStatusPending {
						renderPRReviewStatusCapsule(c, itemKey+"-status", st.status)
					}
				})

				if loc := prReviewLocationText(item.Path, item.Line); loc != "" {
					ui.Row(c.Key(itemKey+"-loc-row")).
						FillWidth().
						Padding(0, 0, 0, IconSM+Space2).
						Children(func() {
							locBtn := ui.ButtonBase(c.Key(itemKey+"-loc")).
								Label(loc).
								Tooltip(loc).
								MinHeight(ControlXS).
								Padding(Space0, Space2).
								Radius(RadiusSM).
								Justify(ui.Start).
								AlignItems(ui.Center).
								Gap(Space1).
								Cursor(ui.CursorPointer).
								Transition(motionFeedback(c))
							if locBtn.Hovered() {
								locBtn.Background(ThemeOverlayHover(c))
							}
							itemPath, itemLine := item.Path, item.Line
							locBtn.OnClick(func() {
								openPRReviewLocation(s, itemPath, itemLine)
							})
							locBtn.Children(func() {
								ui.Icon(c, IconFile()).Size(12, 12).TextColor(ThemeTertiaryText(c)).Shrink(0)
								ui.Text(c, loc).
									Font(codeFont).
									FontSize(scaledFont(TypeCaption)).
									TextColor(ThemeSecondaryText(c)).
									SingleLine().
									Ellipsis("…").
									Shrink(1).
									MinWidth(0)
							})
						})
				}
			})

		if !itemOpen {
			return
		}
		ui.Column(c.Key(itemKey+"-detail")).
			FillWidth().
			Transition(motionCollapse(c)).
			Padding(0, Space4, Space3, Space4+IconSM+Space2).
			Gap(Space2).
			Children(func() {
				if item.Impact != "" {
					ui.Row(c.Key(itemKey + "-impact")).
						FillWidth().
						AlignItems(ui.Start).
						Gap(Space3).
						Children(func() {
							ui.Text(c, "影响").
								FontSize(scaledFont(TypeFootnote)).
								TextColor(ThemeTertiaryText(c)).
								SingleLine().
								Shrink(0).
								Unselectable()
							ui.Text(c, item.Impact).
								FontSize(scaledFont(TypeBody)).
								LineHeight(1.45).
								TextColor(ThemeSecondaryText(c)).
								Grow(1).
								MinWidth(0).
								Selectable()
						})
				}
				if item.Fix != "" {
					ui.Row(c.Key(itemKey + "-fix")).
						FillWidth().
						AlignItems(ui.Start).
						Gap(Space3).
						Children(func() {
							ui.Text(c, "建议").
								FontSize(scaledFont(TypeFootnote)).
								TextColor(ThemeTertiaryText(c)).
								SingleLine().
								Shrink(0).
								Unselectable()
							ui.Text(c, item.Fix).
								FontSize(scaledFont(TypeBody)).
								LineHeight(1.45).
								TextColor(ThemeSecondaryText(c)).
								Grow(1).
								MinWidth(0).
								Selectable()
						})
				}
				if item.Patch != "" {
					renderPRReviewPatchBlock(c, itemKey, item.Patch, codeFont, st.diffOpen, func() {
						updatePRReviewItemState(stateKey, func(cur *prReviewItemUIState) {
							cur.diffOpen = !cur.diffOpen
						})
						c.Invalidate()
					})
				}
				ui.Row(c.Key(itemKey+"-actions")).
					FillWidth().
					Wrap().
					AlignItems(ui.Center).
					Gap(Space2).
					Padding(Space1, 0, 0, 0).
					Children(func() {
						if st.status != prReviewStatusPending {
							Button(c, ButtonOpts{
								Key:     itemKey + "-restore",
								Text:    "恢复",
								Label:   fmt.Sprintf("恢复第 %d 条", entry.index+1),
								Variant: ButtonGhost,
								Size:    ControlSM,
								OnClick: func() {
									updatePRReviewItemState(stateKey, func(cur *prReviewItemUIState) {
										cur.status = prReviewStatusPending
									})
									c.Invalidate()
								},
							})
							return
						}
						Button(c, ButtonOpts{
							Key:     itemKey + "-agent-fix",
							Text:    "让 agent 修复",
							Label:   fmt.Sprintf("让 agent 修复第 %d 条", entry.index+1),
							Icon:    IconSparkles(),
							Variant: ButtonSecondary,
							Size:    ControlSM,
							OnClick: func() {
								if s != nil {
									s.sendTextOnly(formatPRReviewSingleFixPrompt(item))
								}
								updatePRReviewItemState(stateKey, func(cur *prReviewItemUIState) {
									cur.status = prReviewStatusAgent
								})
								c.Invalidate()
							},
						})
						if inPRScope {
							Button(c, ButtonOpts{
								Key:      itemKey + "-send-pr",
								Text:     "发到 PR",
								Label:    fmt.Sprintf("将第 %d 条发到 PR", entry.index+1),
								Icon:     IconSend(),
								Variant:  ButtonGhost,
								Size:     ControlSM,
								Disabled: busy,
								OnClick: func() {
									submitPRReviewCardComments(s, dp, cardKey.threadID, prNumber, cardKey, []indexedPRReviewItem{entry})
								},
							})
						}
						Button(c, ButtonOpts{
							Key:     itemKey + "-ignore",
							Text:    "忽略",
							Label:   fmt.Sprintf("忽略第 %d 条", entry.index+1),
							Variant: ButtonGhost,
							Size:    ControlSM,
							OnClick: func() {
								updatePRReviewItemState(stateKey, func(cur *prReviewItemUIState) {
									cur.status = prReviewStatusIgnored
								})
								if dp != nil {
									diffPanelMu.Lock()
									deleteMatchingPRReviewDraftsLocked(dp, []prReviewItem{item})
									diffPanelMu.Unlock()
								}
								c.Invalidate()
							},
						})
					})
			})
	})
}

func renderPRReviewIssueGroup(
	c *ui.Context,
	s *chatState,
	dp *diffPanelState,
	cardKey prReviewCardStateKey,
	groupKey string,
	title string,
	dotColor ui.Color,
	entries []indexedPRReviewItem,
	firstBlockingIdx int,
	inPRScope bool,
	prNumber int,
	busy bool,
	codeFont string,
) {
	if len(entries) == 0 {
		return
	}
	ui.Column(c.Key(groupKey)).FillWidth().Children(func() {
		ui.Row(c.Key(groupKey+"-header")).
			FillWidth().
			Padding(Space1, Space4).
			Background(ThemeOverlayHover(c)).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				ui.Box(c.Key(groupKey+"-dot")).Size(6, 6).Radius(RadiusPill).Background(dotColor)
				ui.Text(c, title).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Unselectable()
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%d", len(entries))).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeTertiaryText(c)).
					Unselectable()
			})
		for i, entry := range entries {
			itemKey := fmt.Sprintf("%s-item-%d", groupKey, entry.index)
			renderPRReviewIssueRow(
				c, s, dp, cardKey, itemKey, entry,
				entry.index == firstBlockingIdx, i == 0,
				inPRScope, prNumber, busy, codeFont,
			)
		}
	})
}

// renderPRReviewCard 把 ```pr-review 代码块渲染为审查卡片；流式未闭合或 JSON 解析失败时
// 退回通用代码块容器原样显示，避免丢失内容。
func renderPRReviewCard(c *ui.Context, s *chatState, b *markdown.CodeBlockView) {
	if b == nil {
		return
	}
	if b.Open {
		renderCodeBlockContainer(c, s, b)
		return
	}
	res, ok := parsePRReview(b.Code())
	if !ok {
		renderCodeBlockContainer(c, s, b)
		return
	}

	threadID := selectedThreadIDSnapshot(s)
	cardKey := prReviewCardStateKey{chat: s, threadID: threadID, blockIndex: b.Index}
	busy := isPRReviewCardBusy(cardKey)

	dp := getDiffPanelState(s)
	inPRScope := false
	prNumber := 0
	if dp != nil {
		diffPanelMu.Lock()
		if dp.scope == "pr" && dp.prNumber > 0 {
			inPRScope = true
			prNumber = dp.prNumber
		}
		diffPanelMu.Unlock()
	}

	var blockingEntries, suggestionEntries []indexedPRReviewItem
	for i, item := range res.Comments {
		entry := indexedPRReviewItem{index: i, item: item}
		if item.Severity == "blocking" {
			blockingEntries = append(blockingEntries, entry)
		} else {
			suggestionEntries = append(suggestionEntries, entry)
		}
	}
	firstBlockingIdx := -1
	if len(blockingEntries) > 0 {
		firstBlockingIdx = blockingEntries[0].index
	}

	var pendingBlocking, pendingAll []indexedPRReviewItem
	for _, entry := range blockingEntries {
		stateKey := prReviewItemStateKey{chat: s, threadID: threadID, blockIndex: b.Index, itemIndex: entry.index}
		st, _ := getPRReviewItemState(stateKey, entry.index == firstBlockingIdx)
		if st.status == prReviewStatusPending {
			pendingBlocking = append(pendingBlocking, entry)
			pendingAll = append(pendingAll, entry)
		}
	}
	for _, entry := range suggestionEntries {
		stateKey := prReviewItemStateKey{chat: s, threadID: threadID, blockIndex: b.Index, itemIndex: entry.index}
		st, _ := getPRReviewItemState(stateKey, false)
		if st.status == prReviewStatusPending {
			pendingAll = append(pendingAll, entry)
		}
	}

	key := fmt.Sprintf("code-block-%d", b.Index)
	codeFont := "monospace"
	if st := getChatSettings(s); st != nil {
		codeFont = codeFontFamily(st.cfg.Appearance)
	}

	ui.Column(c.Key(key)).
		Direction(ui.LeftToRight).
		AlignSelf(ui.Stretch).
		Background(ThemeSurfaceCard(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusLG).
		Clip().
		Children(func() {
			ui.Column(c.Key(key+"-header")).
				FillWidth().
				Padding(Space3, Space4).
				Gap(Space2).
				Background(prReviewVerdictHeaderBg(c, res.Verdict)).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeHairline(c)).
				Children(func() {
					ui.Row(c.Key(key + "-topline")).
						FillWidth().
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							ui.Row(c.Key(key + "-verdict-group")).
								Grow(1).
								MinWidth(0).
								Wrap().
								AlignItems(ui.Center).
								Gap(Space2).
								Children(func() {
									if verdictLabel := prReviewVerdictLabel(res.Verdict); verdictLabel != "" {
										Capsule(c, CapsuleProps{
											Key:  key + "-verdict-pill",
											Text: verdictLabel,
											Tone: prReviewVerdictTone(res.Verdict),
											Size: ControlXS,
										})
									} else if len(res.Comments) == 0 {
										ui.Text(c, "没有发现问题").
											FontSize(scaledFont(TypeCaption)).
											TextColor(ThemeSecondaryText(c)).
											Unselectable()
									}
									if countText := prReviewCountText(len(blockingEntries), len(suggestionEntries)); countText != "" {
										ui.Text(c, countText).
											FontSize(scaledFont(TypeCaption)).
											TextColor(ThemeSecondaryText(c)).
											Unselectable()
									}
								})
							copyKey := key + "-copy"
							copied := copyFeedbackActive(copyKey, time.Now())
							CopyButton(c, CopyButtonProps{
								Key: copyKey, Label: "复制代码", Tooltip: "复制代码", Text: "复制",
								Copied: copied, Disabled: b.Open, Value: b.Code,
								OnCopy: func() {
									markCopyFeedback(s, copyKey)
									if s != nil {
										s.setToast("已复制")
									}
								},
							})
						})

					if res.Reason != "" {
						ui.Text(c, res.Reason).
							FontSize(scaledFont(TypeBody)).
							LineHeight(1.45).
							TextColor(ThemePrimaryText(c)).
							FillWidth().
							MinWidth(0).
							Selectable()
					}
				})

			renderPRReviewIssueGroup(
				c, s, dp, cardKey, key+"-blocking",
				"阻塞合并", ThemeDanger(c), blockingEntries,
				firstBlockingIdx, inPRScope, prNumber, busy, codeFont,
			)
			renderPRReviewIssueGroup(
				c, s, dp, cardKey, key+"-suggestion",
				"建议修复", ThemeWarning(c), suggestionEntries,
				firstBlockingIdx, inPRScope, prNumber, busy, codeFont,
			)

			ui.Row(c.Key(key+"-footer")).
				FillWidth().
				Wrap().
				AlignItems(ui.Center).
				Gap(Space2).
				Padding(Space2, Space4).
				Background(ThemeSurfaceMain(c)).
				BorderWidth(Hairline, 0, 0, 0).
				BorderColor(ThemeHairline(c)).
				Children(func() {
					if len(pendingBlocking) > 0 {
						Button(c, ButtonOpts{
							Key:     key + "-fix-blocking",
							Text:    "修复阻塞项",
							Icon:    IconSparkles(),
							Variant: ButtonPrimary,
							Size:    ControlSM,
							OnClick: func() {
								if s != nil {
									s.sendTextOnly(formatPRReviewBlockingFixPrompt(pendingBlocking))
								}
								prReviewStateMu.Lock()
								for _, entry := range pendingBlocking {
									k := prReviewItemStateKey{
										chat:       cardKey.chat,
										threadID:   cardKey.threadID,
										blockIndex: cardKey.blockIndex,
										itemIndex:  entry.index,
									}
									st := prReviewStates[k]
									st.status = prReviewStatusAgent
									prReviewStates[k] = st
								}
								prReviewStateMu.Unlock()
								c.Invalidate()
							},
						})
					}
					Button(c, ButtonOpts{
						Key:     key + "-re-review",
						Text:    "复审",
						Icon:    IconRefreshCw(),
						Variant: ButtonGhost,
						Size:    ControlSM,
						OnClick: func() {
							if dp != nil {
								diffPanelMu.Lock()
								dp.prAIAuto = true
								diffPanelMu.Unlock()
							}
							if s != nil {
								s.sendTextOnly("代码已更新，请按同样的要求和输出格式重新审查当前分支")
							}
						},
					})
					if inPRScope && len(res.Comments) > 0 {
						Button(c, ButtonOpts{
							Key:      key + "-publish-all",
							Text:     "发到 PR",
							Label:    "发全部到 PR",
							Icon:     IconSend(),
							Variant:  ButtonSecondary,
							Size:     ControlSM,
							Disabled: busy || len(pendingAll) == 0,
							OnClick: func() {
								submitPRReviewCardComments(s, dp, cardKey.threadID, prNumber, cardKey, pendingAll)
							},
						})
					}
					if inPRScope {
						mergeVariant := ButtonSecondary
						if len(pendingBlocking) == 0 {
							mergeVariant = ButtonPrimary
						}
						Button(c, ButtonOpts{
							Key:     key + "-merge-pr",
							Text:    "合并 PR",
							Icon:    IconCheck(),
							Variant: mergeVariant,
							Size:    ControlSM,
							OnClick: func() {
								if s != nil {
									s.openGitPRDialog()
								}
							},
						})
					}
				})
		})
}
