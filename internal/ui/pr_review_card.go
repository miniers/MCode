// PR 审查卡片渲染（```pr-review 围栏代码块）。
package mcodeui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// prReviewItem 表示 pr-review 代码块中的单条审查意见。
type prReviewItem struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// prReviewResult 表示 pr-review 代码块的审查结论与意见列表。
type prReviewResult struct {
	Verdict  string         `json:"verdict"`
	Summary  string         `json:"summary"`
	Comments []prReviewItem `json:"comments"`
}

// prReviewVerdictLabel 返回审查结论对应的标题标签；未知结论返回空字符串。
func prReviewVerdictLabel(verdict string) string {
	switch verdict {
	case "merge":
		return "可以合并"
	case "comment":
		return "评论给作者"
	case "fix":
		return "合并后修复"
	case "close":
		return "不建议合并"
	default:
		return ""
	}
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
		return prReviewResult{Comments: items}, true
	case strings.HasPrefix(trimmed, "{"):
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
			return prReviewResult{}, false
		}
		_, hasVerdict := fields["verdict"]
		_, hasSummary := fields["summary"]
		_, hasComments := fields["comments"]
		if !hasVerdict && !hasSummary && !hasComments {
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
		return res, true
	default:
		return prReviewResult{}, false
	}
}

// prReviewCardTitle 返回审查卡片标题栏左侧文字。
func prReviewCardTitle(verdict string, count int) string {
	if label := prReviewVerdictLabel(verdict); label != "" {
		if count > 0 {
			return fmt.Sprintf("%s · %d 个问题", label, count)
		}
		return label
	}
	if count <= 0 {
		return "没有发现问题"
	}
	return fmt.Sprintf("%d 个问题", count)
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

// renderPRReviewBodyText 渲染审查摘要与问题正文的统一文本块。
func renderPRReviewBodyText(c *ui.Context, key, text string) {
	ui.Box(c.Key(key)).
		FillWidth().
		Padding(0, Space2, Space1, Space2).
		Children(func() {
			ui.Text(c, text).
				FontSize(scaledFont(TypeBody)).
				LineHeight(1.45).
				TextColor(ThemePrimaryText(c)).
				FillWidth().
				MinWidth(0).
				Selectable()
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
	summary := strings.TrimSpace(res.Summary)
	items := res.Comments

	key := fmt.Sprintf("code-block-%d", b.Index)
	codeFont := "monospace"
	if st := getChatSettings(s); st != nil {
		codeFont = codeFontFamily(st.cfg.Appearance)
	}

	ui.Column(c.Key(key)).
		Direction(ui.LeftToRight).
		AlignSelf(ui.Stretch).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusLG).
		Clip().
		Children(func() {
			header := ui.Row(c.Key(key+"-header")).
				Height(codeBlockHeaderHeight).
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Background(ThemeHover(c))
			if summary != "" || len(items) > 0 {
				header.BorderWidth(0, 0, 1, 0).BorderColor(ThemeBorder(c))
			}
			header.Children(func() {
				ui.Text(c, prReviewCardTitle(res.Verdict, len(items))).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Unselectable()
				ui.Spacer(c)
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

			if summary != "" {
				ui.Column(c.Key(key+"-summary")).
					FillWidth().
					Padding(Space2, Space2).
					Children(func() {
						renderPRReviewBodyText(c, key+"-summary-body", summary)
					})
			}

			for i, item := range items {
				itemCol := ui.Column(c.Key(fmt.Sprintf("%s-item-%d", key, i))).
					FillWidth().
					Padding(Space2, Space2).
					Gap(Space0)
				if i > 0 || summary != "" {
					itemCol.BorderWidth(Hairline, 0, 0, 0).BorderColor(ThemeBorder(c))
				}
				itemCol.Children(func() {
					loc := prReviewLocationText(item.Path, item.Line)
					if loc != "" {
						locBtn := ui.ButtonBase(c.Key(fmt.Sprintf("%s-loc-%d", key, i))).
							Label(loc).
							Tooltip(loc).
							FillWidth().
							MinHeight(ControlSM).
							Padding(Space1, Space2).
							Radius(RadiusSM).
							Justify(ui.Start).
							AlignItems(ui.Center).
							Cursor(ui.CursorPointer).
							Transition(motionFeedback(c))
						if locBtn.Hovered() {
							locBtn.Background(ThemeHover(c))
						}
						itemPath, itemLine := item.Path, item.Line
						locBtn.OnClick(func() {
							openPRReviewLocation(s, itemPath, itemLine)
						})
						locBtn.Children(func() {
							ui.Text(c, loc).
								Font(codeFont).
								FontSize(scaledFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine().
								Ellipsis("…").
								Shrink(1).
								MinWidth(0)
						})
					}
					if body := strings.TrimSpace(item.Body); body != "" {
						renderPRReviewBodyText(c, fmt.Sprintf("%s-body-%d", key, i), body)
					}
				})
			}
		})
}
