package mcodeui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// commandGaugePopoverWidth 是 CPU 仪表悬停浮层的宽度。
const commandGaugePopoverWidth float32 = 280

// commandActivityFillWidth 计算进程 CPU 占用条的目标宽度，并按整像素量化，避免动画目标随浮点抖动触发重绘。
func commandActivityFillWidth(rowWidth float32, cpu float64) float32 {
	width := rowWidth * float32(min(max(cpu, 0), 100)/100)
	return float32(math.Round(float64(width)))
}

// commandGaugeBarHeight 把一次 CPU 采样按 peak 归一化到 [1, full] 并取整像素，作为迷你图柱高的动画目标。
func commandGaugeBarHeight(value, peak float64, full float32) float32 {
	return float32(math.Max(1, math.Round(min(max(value, 0)/peak, 1)*float64(full))))
}

// isCommandCard 判断工具消息是否用命令卡片渲染：运行中的命令或 provider 托管的后台命令。
// 只认状态明确为 running / background 的命令：历史或中断会话里状态为空的命令仍走胶囊加详情。
// 等待审批的命令仍走普通胶囊，详情里要显示待批准的入参。
func isCommandCard(m Message) bool {
	return isCommandTool(m) && (isToolRunning(m) || isToolBackground(m)) && toolMessageState(m) != ToolPending
}

// commandOutputText 取命令工具消息正文里第一行（工具头）之后的实时输出。
func commandOutputText(m Message) string {
	if idx := strings.IndexByte(m.Text, '\n'); idx >= 0 {
		return m.Text[idx+1:]
	}
	return ""
}

// renderCommandGauge 渲染 CPU 仪表：迷你柱状图加一个百分比，悬停或键盘聚焦时弹出浮层，
// 列出每个进程的 CPU 占用与内存合计。没有进程采样时不渲染。stalled 时整体改用警告色。
func renderCommandGauge(c *ui.Context, key string, st commandActivityState, stalled bool) {
	if !st.hasProcess || len(st.history) == 0 {
		return
	}
	color := ThemeAccent(c)
	if stalled {
		color = ThemeWarning(c)
	}
	peak := float64(100)
	for _, value := range st.history {
		peak = max(peak, value)
	}
	gauge := ui.Row(c.Key(key)).Shrink(0).Gap(Space2).AlignItems(ui.Center).Radius(RadiusXS).Focusable().Label("资源占用")
	focusRing(c, gauge)
	open := gauge.Hovered() || gauge.FocusVisible()
	barsHeight := scaledFont(TypeFootnote)
	gauge.Children(func() {
		ui.Row(c.Key(key + "-bars")).Height(barsHeight).Gap(Hairline).AlignItems(ui.End).Children(func() {
			for i, value := range st.history {
				bar := ui.Box(c.Key(fmt.Sprintf("%s-bar-%d", key, i))).Width(Space0).Background(color)
				target := commandGaugeBarHeight(value, peak, barsHeight)
				bar.Height(bar.AnimateTo("height", target, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut}))
			}
		})
		ui.Text(c, fmt.Sprintf("%.0f%%", st.cpu)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(color).SingleLine()
	})
	if len(st.processes) == 0 && st.rss <= 0 {
		return
	}
	PopoverCard(c, gauge, PopoverProps{Key: key + "-popover", Open: &open, Align: BelowEnd, Width: commandGaugePopoverWidth}, func() {
		ui.Column(c.Key(key + "-processes")).FillWidth().Gap(Hairline).Children(func() {
			for i, p := range st.processes {
				renderCommandProcessRow(c, key, i, p, stalled)
			}
			if st.rss > 0 {
				ui.Row(c.Key(key+"-memory")).FillWidth().Justify(ui.End).Padding(0, Space2).Children(func() {
					ui.Text(c, formatFileSize(st.rss)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
				})
			}
		})
	})
}

// renderCommandProcessRow 渲染浮层里的一行进程：进程名、对象和随 CPU 占用伸缩的底色条。
func renderCommandProcessRow(c *ui.Context, key string, i int, p commandProcessView, stalled bool) {
	row := ui.Row(c.Key(fmt.Sprintf("%s-process-%d", key, i))).FillWidth().Height(ControlXS).Radius(RadiusXS).Clip().Padding(0, Space2).Gap(Space2).AlignItems(ui.Center)
	opacity := motionEnter(c, row, "enter", i)
	if p.cpu < 1 {
		opacity *= 0.5
	}
	row.Opacity(opacity)
	tone := ThemeAccent(c)
	cpuColor := ThemeSecondaryText(c)
	if stalled {
		tone = ThemeWarning(c)
		cpuColor = tone
	}
	row.Children(func() {
		fill := ui.Box(c.Key(fmt.Sprintf("%s-fill-%d", key, i))).Absolute().Left(0).Top(0).Bottom(0).Radius(RadiusXS).Background(tone.Alpha(0.12)).PassThrough()
		width := commandActivityFillWidth(row.LayoutBounds().W, p.cpu)
		fill.Width(fill.AnimateTo("width", width, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut}))
		ui.Text(c, p.name).FontSize(scaledFont(TypeCaption)).FontWeight(500).TextColor(ThemePrimaryText(c)).SingleLine().Selectable()
		objText := ui.Text(c.Key(fmt.Sprintf("%s-process-%d-object", key, i)), p.object).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…").Selectable()
		if p.object != "" && objText.Truncated() {
			objText.Tooltip(p.object)
		}
		ui.Text(c, fmt.Sprintf("%.0f%%", p.cpu)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(cpuColor).SingleLine()
	})
}

// renderCommandCard 渲染运行中命令或后台命令的卡片：标题行（状态图标、标题、CPU 仪表、悬停才出现的复制与终止、耗时）
// 加可收起的主体（多步命令的步骤列表与实时输出）。点标题行展开或收起，展开状态与普通工具胶囊共用。
func renderCommandCard(c *ui.Context, s *chatState, m Message, beatTools []Message) {
	badge, stalled := s.runningCommandBadge(c, m)
	s.mu.Lock()
	ensureCommandActivityLocked(s, m)
	killing := s.killingTools[m.ID]
	s.mu.Unlock()
	st := commandActivitySnapshot(s, m.ID)
	background := isToolBackground(m)
	open := s.toolMessageExpanded(m)
	verb, target, detail := toolCallTitle(m)
	title := toolChipFullTitle(m, verb, target)
	rawCmd := commandActivityCommand(m)

	var appCfg config.AppearanceConfig
	if cs := getChatSettings(s); cs != nil {
		appCfg = cs.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	tone := ThemeAccent(c)
	metaColor := ThemeSecondaryText(c)
	if stalled {
		tone, metaColor = ThemeWarning(c), ThemeWarning(c)
	}

	ui.Column(c.Key("command-card-"+m.ID)).FillWidth().Radius(RadiusMD).Background(ThemeSurfaceCard(c)).Border(Hairline, ThemeBorder(c)).Children(func() {
		head := ui.Row(c.Key("command-card-head-"+m.ID)).FillWidth().Height(ControlLG).Radius(RadiusMD).Padding(0, Space3).Gap(Space3).AlignItems(ui.Center).Cursor(ui.CursorPointer).Focusable().Label(toolCallRowLabel(verb, target, detail))
		showActions := head.Hovered() || head.FocusWithin()
		if head.Hovered() {
			head.Background(ThemeHover(c))
		}
		focusRing(c, head)
		if head.Clicked() || (head.Focused() && (head.Shortcut(0, ui.KeyEnter) || head.Shortcut(0, ui.KeySpace))) {
			s.toggleToolChip(m, beatTools)
		}
		head.Children(func() {
			renderRunningGlyph(c, "command-card-run-"+m.ID, Space4, tone)
			// 标题外包一层可伸缩的行：运行中扫光的包裹行不带 Grow，由这一层把仪表与耗时推到右侧。
			ui.Row(c.Key("command-card-title-" + m.ID)).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				renderToolChipTitle(c, m, func() ui.Element {
					name := ui.Text(c, title).FontSize(scaledFont(TypeFootnote)).FontWeight(500).TextColor(ThemePrimaryText(c)).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					if toolChipDescription(m) == "" {
						name.Font(fontFam)
					}
					return name
				})
			})
			renderCommandGauge(c, "command-gauge-"+m.ID, st, stalled)
			renderCommandCardActions(c, s, m, rawCmd, killing, background, showActions)
			if badge != "" {
				ui.Text(c, badge).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(metaColor).SingleLine().Shrink(0)
			}
		})

		output := commandOutputText(m)
		hasSteps := len(st.steps) >= 2
		if !hasSteps && strings.TrimSpace(output) == "" {
			return
		}
		motionUnfoldBody(c, "command-card-body-"+m.ID, open, func() {
			dens := currentDensity(c, s)
			outputMax := toolOutputMaxHeight
			if dens.isCompact() {
				outputMax = toolOutputMaxHeightCompact
			}
			ui.Column(c.Key("command-card-content-"+m.ID)).FillWidth().Padding(0, Space3, Space3, Space3).Gap(Space2).Children(func() {
				if hasSteps {
					renderCommandCardSteps(c, s, m, st, fontFam)
				}
				renderCommandCardOutput(c, s, m, output, fontFam, outputMax)
			})
		})
	})
}

// renderCommandCardActions 渲染标题行里的复制与终止按钮：悬停或键盘聚焦标题行时宽度展开，
// 其余时间收成零宽（按钮仍在树里，键盘 Tab 能到达，聚焦后标题行 FocusWithin 即展开）。
// 终止只杀这一条命令、不中断本轮；后台任务由后台任务面板停止，这里不给终止按钮。
func renderCommandCardActions(c *ui.Context, s *chatState, m Message, rawCmd string, killing, background, show bool) {
	full := ControlXS
	if !background {
		full = ControlXS*2 + Space1
	}
	target := float32(0)
	if show {
		target = full
	}
	acts := ui.Row(c.Key("command-card-actions-" + m.ID)).Shrink(0).Clip().Gap(Space1).AlignItems(ui.Center)
	width := acts.AnimateTo("width", target, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: ui.EaseOut})
	acts.Width(width).Opacity(width / full)
	acts.Children(func() {
		copyKey := "command-copy-" + m.ID
		CopyButton(c, CopyButtonProps{
			Key:      copyKey,
			Label:    "复制命令",
			Tooltip:  "复制命令",
			Mode:     CopyIcon,
			Copied:   copyFeedbackActive(copyKey, time.Now()),
			Disabled: rawCmd == "",
			Value:    func() string { return rawCmd },
			OnCopy:   func() { markCopyFeedback(s, copyKey) },
		}).Shrink(0)
		if !background {
			IconButton(c, IconButtonProps{Key: "tool-kill-" + m.ID, Icon: IconCircleStop(), Label: "终止此命令", Tooltip: "终止此命令", Size: ControlXS, Variant: ButtonGhost, Loading: killing, Disabled: killing, OnClick: func() { s.killRunningCommand(m.ID) }}).Shrink(0)
		}
	})
}

// renderCommandCardSteps 渲染多步命令的步骤列表：每步一个状态图标，默认最多三行，
// 任一步超出三行时右侧给「展开命令」按钮。
func renderCommandCardSteps(c *ui.Context, s *chatState, m Message, st commandActivityState, fontFam string) {
	s.mu.Lock()
	expanded := s.expandedCommandSteps[m.ID]
	s.mu.Unlock()
	hasOverflow := false
	ui.Row(c.Key("command-header-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Start).Children(func() {
		ui.Column(c.Key("command-steps-" + m.ID)).Grow(1).MinWidth(0).Gap(Space1).Children(func() {
			fontSize := scaledFont(TypeCaption)
			for i, step := range st.steps {
				status := commandStepStatus(i, st.maxStep)
				ui.Row(c.Key(fmt.Sprintf("command-step-%s-%d", m.ID, i))).FillWidth().Gap(Space2).AlignItems(ui.Start).Children(func() {
					ui.Box(c.Key(fmt.Sprintf("command-step-icon-wrap-%s-%d", m.ID, i))).Padding(Space0, 0, 0, 0).Shrink(0).Children(func() {
						renderTodoStatusIcon(c, fmt.Sprintf("command-step-icon-%s-%d", m.ID, i), status, true)
					})
					color := ThemeSecondaryText(c)
					if status == "completed" {
						color = color.Alpha(0.75)
					} else if status == "in_progress" {
						color = ThemePrimaryText(c)
					}
					label := ui.Text(c.Key(fmt.Sprintf("command-step-text-%s-%d", m.ID, i)), step).Font(fontFam).FontSize(fontSize).Grow(1).MinWidth(0).TextColor(color).Selectable()
					if !expanded {
						label.MaxLines(3).Ellipsis("…")
					}
					weight := 0
					if status == "in_progress" {
						weight = 500
						label.FontWeight(500)
					}
					if !hasOverflow {
						if w := label.LayoutBounds().W; w > 0 {
							_, stepH := cachedMeasureText(c, w, ui.Span{Text: step, Font: fontFam, Size: fontSize, Weight: weight})
							_, threeLineH := cachedMeasureText(c, 0, ui.Span{Text: "A\nA\nA", Font: fontFam, Size: fontSize, Weight: weight})
							if stepH > threeLineH+Hairline {
								hasOverflow = true
							}
						}
					}
				})
			}
		})
		if hasOverflow {
			toggleIcon := IconChevronRight()
			toggleLabel := "展开命令"
			if expanded {
				toggleIcon = IconChevronDown()
				toggleLabel = "收起命令"
			}
			IconButton(c, IconButtonProps{
				Key:     "command-steps-toggle-" + m.ID,
				Icon:    toggleIcon,
				Label:   toggleLabel,
				Tooltip: toggleLabel,
				Size:    ControlXS,
				Variant: ButtonGhost,
				OnClick: func() {
					s.mu.Lock()
					if s.expandedCommandSteps == nil {
						s.expandedCommandSteps = make(map[string]bool)
					}
					s.expandedCommandSteps[m.ID] = !expanded
					s.mu.Unlock()
					s.invalidate()
				},
			}).Shrink(0)
		}
	})
}

// renderCommandCardOutput 渲染实时输出：限高滚动，默认贴底跟随新输出，用户上滚则停止跟随。
func renderCommandCardOutput(c *ui.Context, s *chatState, m Message, text, fontFam string, outputMax float32) {
	if strings.TrimSpace(text) == "" {
		return
	}
	stScroll := s.toolOutputScrollStateFor(m.ID)
	if stScroll.MaxY <= 0 || stScroll.Y >= stScroll.MaxY-toolOutputFollowThreshold {
		stScroll.Y = 1e9
	}
	ui.Box(c.Key("tool-output-wrap-" + m.ID)).FillWidth().Children(func() {
		ui.Scroll(c.Key("tool-output-scroll-" + m.ID)).FillWidth().MaxHeight(outputMax).TrackScroll(stScroll).Children(func() {
			ui.Text(c, text).Font(fontFam).FontSize(scaledFont(codeFontSize())).LineHeight(1.4).TextColor(ThemeSecondaryText(c)).Selectable()
		})
	})
}
