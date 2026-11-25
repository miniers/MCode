package mcodeui

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

type quotaGroupListOptions struct {
	HideSingleGroupTitle bool
	SelectedAccount      string
	OnSelectAccount      func(account string)
	AccountDisabled      func(account string) bool
}

func quotaGroupTitleVisible(groupCount int, opts quotaGroupListOptions) bool {
	return !opts.HideSingleGroupTitle || groupCount != 1
}

// quotaPercentWidth 按当前字体测量「100%」宽度并留出余量，确保不同字号与缩放下都能完整显示且按列对齐。
func quotaPercentWidth(c *ui.Context) float32 {
	w, _ := cachedMeasureText(c, 0, ui.Span{Text: "100%", Size: uiFont(TypeFootnote), Weight: 600, Features: "tnum"})
	w = float32(math.Ceil(float64(w))) + Space1
	minW := 36 * uiScaleFactor()
	if w < minW {
		return minW
	}
	return w
}

// quotaStatusColWidth 按当前字体测量右列紧凑状态词宽度，确保多行与不同缩放下右对齐不截断。
func quotaStatusColWidth(c *ui.Context) float32 {
	var maxW float32
	for _, sample := range []string{"12/18 重置", "4h59m 重置", "周四用尽"} {
		w, _ := cachedMeasureText(c, 0, ui.Span{Text: sample, Size: uiFont(TypeCaption), Weight: 500, Features: "tnum"})
		if w > maxW {
			maxW = w
		}
	}
	w := float32(math.Ceil(float64(maxW))) + Space1
	minW := 72 * uiScaleFactor()
	if w < minW {
		return minW
	}
	return w
}

// quotaStatusColor 把紧凑状态词或悬停详情的语义色调映射为主题颜色。
func quotaStatusColor(c *ui.Context, tone quotaStatusTone) ui.Color {
	switch tone {
	case quotaStatusDanger:
		return ThemeDanger(c)
	case quotaStatusWarn:
		return ThemeWarning(c)
	default:
		return ThemeTertiaryText(c)
	}
}

// renderQuotaPercent 按指定水平对齐方式渲染等宽数字百分比，供表格列与单账号行共用。
func renderQuotaPercent(c *ui.Context, winKey string, usedPercent float64, justify ui.Align) {
	pct := int(math.Round(usedPercent))
	ui.Row(c.Key(winKey + "-percent")).
		Label(fmt.Sprintf("%d%%", pct)).
		MinWidth(quotaPercentWidth(c)).
		Shrink(0).
		Justify(justify).
		AlignItems(ui.Center).
		FontSize(uiFont(TypeFootnote)).
		FontWeight(600).
		FontFeatures("tnum").
		TextColor(quotaPercentColor(c, usedPercent)).
		Children(func() {
			rollingNumber(c, winKey+"-number", pct)
			ui.Text(c, "%")
		})
}

// renderQuotaGroupError 渲染分组报错文字，使用危险色并在悬停时提示完整报错。
func renderQuotaGroupError(c *ui.Context, g trayQuotaGroupModel) {
	errTooltip := g.ErrFull
	if errTooltip == "" {
		errTooltip = g.ErrText
	}
	ui.Text(c, g.ErrText).
		FontSize(uiFont(TypeFootnote)).
		TextColor(ThemeDanger(c)).
		SingleLine().
		Tooltip(errTooltip)
}

// quotaGroupColumnsResolved 返回分组表头列；若模型未预先填充则从账号窗口即时推导。
func quotaGroupColumnsResolved(g trayQuotaGroupModel) []trayQuotaColumn {
	if len(g.Columns) > 0 {
		return g.Columns
	}
	return deriveTrayQuotaColumns(g.Accounts)
}

type quotaRowColCell struct {
	win       *trayQuotaWindowModel
	inlineTag bool
}

// quotaRowAllocateColumns 将账号的窗口按表头列顺序归位，多余或无条窗口放到 extra。
func quotaRowAllocateColumns(windows []trayQuotaWindowModel, cols []trayQuotaColumn) (cells []quotaRowColCell, extra []trayQuotaWindowModel) {
	cells = make([]quotaRowColCell, len(cols))
	used := make([]bool, len(windows))

	for ci, col := range cols {
		for wi := range windows {
			if used[wi] || windows[wi].NoBar {
				continue
			}
			if windows[wi].ShortLabel == col.ShortLabel {
				used[wi] = true
				cells[ci] = quotaRowColCell{win: &windows[wi]}
				break
			}
		}
	}

	for wi := range windows {
		if used[wi] {
			continue
		}
		if windows[wi].NoBar {
			extra = append(extra, windows[wi])
			continue
		}
		placed := false
		for ci := range cells {
			if cells[ci].win == nil {
				cells[ci] = quotaRowColCell{win: &windows[wi], inlineTag: true}
				placed = true
				break
			}
		}
		if !placed {
			extra = append(extra, windows[wi])
		}
	}
	return cells, extra
}

// quotaRowTooltipText 组合额度行的原生 Tooltip 文案。
func quotaRowTooltipText(left, right, detail string) string {
	if left != "" && right != "" {
		return left + " · " + right
	}
	if detail != "" {
		return detail
	}
	return left
}

// renderQuotaRadioRing 渲染 14×14 单选圈，选中切换带 DurFast 过渡，尊重 ReduceMotion。
func renderQuotaRadioRing(c *ui.Context, key string, selected, disabled, rowHovered bool) {
	borderColor := ThemeBorder(c)
	bgColor := ui.Transparent
	switch {
	case disabled:
		borderColor = ThemeHairline(c)
		bgColor = ThemeOverlayHover(c)
	case selected:
		borderColor = ThemeAccent(c)
		bgColor = ThemeAccent(c)
	case rowHovered:
		borderColor = ThemeAccent(c)
	}

	ring := ui.Box(c.Key(key)).
		Size(IconSM, IconSM).
		Radius(RadiusPill).
		Border(1.5, borderColor).
		Background(bgColor).
		Center().
		Shrink(0).
		Transition(motionTransition(c, ui.ElementTransition{Duration: DurFast, Ease: EaseOut, Colors: true}))

	dotTarget := float32(0)
	if selected {
		dotTarget = 1
	}
	if reduceMotion(c) {
		ring.Jump("radio-dot", dotTarget)
	}
	dotScale := ring.AnimateTo("radio-dot", dotTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseOut})
	ring.Children(func() {
		if dotScale > 0 {
			ui.Box(c.Key(key+"-dot")).
				Size(6, 6).
				Radius(RadiusPill).
				Background(ThemeOnAccent(c)).
				Scale(dotScale, dotScale).
				Opacity(dotScale)
		}
	})
}

// renderQuotaActiveBadge 渲染自动选号模式下当前在用账号的「在用」微标。
func renderQuotaActiveBadge(c *ui.Context, key string) {
	ui.Row(c.Key(key)).
		Height(Space5).
		Padding(0, Space2).
		Radius(RadiusPill).
		Background(ThemeAccentSubtle(c)).
		AlignItems(ui.Center).
		Gap(3).
		Shrink(0).
		Children(func() {
			ui.Box(c.Key(key+"-dot")).
				Size(4, 4).
				Radius(RadiusPill).
				Background(ThemeAccent(c))
			ui.Text(c, "在用").
				FontSize(uiFont(TypeMicro)).
				FontWeight(500).
				TextColor(ThemeAccent(c)).
				SingleLine()
		})
}

// quotaGroupSummaryMaxText 生成分组折叠时在标题行右侧展示的最高已用百分比摘要（如「最高 80%」）。
func quotaGroupSummaryMaxText(g trayQuotaGroupModel) string {
	maxUsed := -1.0
	for _, acc := range g.Accounts {
		for _, w := range acc.Windows {
			if w.NoBar {
				continue
			}
			if w.UsedPercent > maxUsed {
				maxUsed = w.UsedPercent
			}
		}
	}
	if maxUsed < 0 {
		return ""
	}
	clamped := math.Max(0, math.Min(100, maxUsed))
	return fmt.Sprintf("最高 %d%%", int(math.Round(clamped)))
}

func renderQuotaGroupList(c *ui.Context, keyPrefix string, groups []trayQuotaGroupModel, opts quotaGroupListOptions) {
	selectable := opts.OnSelectAccount != nil
	pctW := quotaPercentWidth(c)
	winColW := max(92*uiScaleFactor(), pctW+Space1+36*uiScaleFactor())
	statusColW := quotaStatusColWidth(c)
	const colGap = Space3

	ui.Column(c.Key(keyPrefix + "list")).
		FillWidth().
		Gap(Space3).
		Children(func() {
			for gIdx, g := range groups {
				groupKey := fmt.Sprintf("%sg-%d", keyPrefix, gIdx)
				if gIdx > 0 {
					ui.Row(c.Key(groupKey+"-divider-wrap")).
						FillWidth().
						Padding(0, Space4).
						Children(func() {
							ui.Row(c.Key(groupKey + "-divider")).
								FillWidth().
								Height(Hairline).
								Background(ThemeDivider(c))
						})
				}

				groupCol := ui.Column(c.Key(groupKey)).
					FillWidth().
					Gap(Space1)
				groupExpanded := ui.Local(groupCol, "group-expanded", func() bool { return true })
				noReadingsOpen := ui.Local(groupCol, "no-readings-open", func() bool { return false })
				collapsibleGroup := quotaGroupTitleVisible(len(groups), opts) && !g.HasErr && len(groups) > 1

				groupCol.Children(func() {
					if quotaGroupTitleVisible(len(groups), opts) {
						renderTitleContent := func(showChevron bool, collapsed bool) {
							ui.Box(c.Key(groupKey+"-icon-box")).
								Size(IconMD, IconMD).
								Center().
								Shrink(0).
								Children(func() {
									renderProviderIconColor(c, g.IconID, IconMD, ThemeSecondaryText(c))
								})

							ui.Text(c, g.Title).
								FontSize(uiFont(TypeBody)).
								Bold().
								TextColor(ThemePrimaryText(c)).
								SingleLine().
								Ellipsis("…").
								Shrink(1).
								MinWidth(0)

							if !g.HasErr && g.Plan != "" {
								ui.Text(c, "· "+g.Plan).
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									SingleLine().
									Shrink(0)
							}

							ui.Spacer(c)

							if g.HasErr {
								renderQuotaGroupError(c, g)
								return
							}

							if showChevron {
								if collapsed {
									if summary := quotaGroupSummaryMaxText(g); summary != "" {
										ui.Text(c, summary).
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeTertiaryText(c)).
											SingleLine().
											Shrink(0)
									}
								}
								chevron := ui.Icon(c.Key(groupKey+"-chevron"), IconChevronDown()).
									Size(IconSM, IconSM).
									TextColor(ThemeTertiaryText(c)).
									Shrink(0)
								targetAngle := float32(0)
								if collapsed {
									targetAngle = -90
								}
								if reduceMotion(c) {
									chevron.Jump(groupKey+"-chevron", targetAngle)
								}
								chevron.Rotate(chevron.AnimateTo(groupKey+"-chevron", targetAngle, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap}))
							}
						}

						if collapsibleGroup {
							hdrBtn := ui.ButtonBase(c.Key(groupKey+"-title-btn")).
								Label(g.Title).
								Height(ControlSM).
								Margin(0, Space2).
								Padding(0, Space2).
								Radius(RadiusSM).
								AlignItems(ui.Center).
								Gap(Space2).
								Expanded(*groupExpanded).
								Transition(motionFeedback(c))
							if hdrBtn.Hovered() {
								hdrBtn.Background(ThemeOverlayHover(c))
							}
							focusRing(c, hdrBtn)
							hdrBtn.OnClick(func() {
								*groupExpanded = !*groupExpanded
							})
							hdrBtn.Children(func() {
								renderTitleContent(true, !*groupExpanded)
							})
						} else {
							ui.Row(c.Key(groupKey+"-title-row")).
								FillWidth().
								Height(ControlSM).
								Padding(0, Space4).
								AlignItems(ui.Center).
								Gap(Space2).
								Children(func() {
									renderTitleContent(false, false)
								})
						}
					} else if g.HasErr {
						ui.Row(c.Key(groupKey+"-err-row")).
							FillWidth().
							Padding(0, Space4).
							Children(func() {
								renderQuotaGroupError(c, g)
							})
					}

					if g.HasErr {
						return
					}

					renderGroupBody := func() {
						renderBar := func(win trayQuotaWindowModel, winKey string, projected bool, fixedWidth float32) {
							clamped := math.Max(0, math.Min(100, win.UsedPercent))
							ratio := float32(clamped / 100.0)

							bar := ui.Row(c.Key(winKey + "-bar")).
								Height(quotaBarHeight).
								Radius(quotaBarRadius).
								Background(quotaBarTrackColor(c)).
								Clip().
								Grow(1).
								Width(0).
								MinWidth(24)
							if !win.hasElapsed {
								if reduceMotion(c) {
									bar.Jump("ratio", ratio)
								}
								ratio = bar.AnimateTo("ratio", ratio, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut})
							}
							bar.Children(func() {
								if !win.hasElapsed {
									if ratio > 0 {
										ui.Box(c.Key(winKey + "-fill")).
											FillHeight().
											Width(0).
											Grow(ratio).
											Radius(quotaBarRadius).
											Background(quotaLevelColor(c, win.UsedPercent))
									}
									if ratio < 1 {
										ui.Box(c.Key(winKey + "-empty")).
											Width(0).
											Grow(1 - ratio)
									}
									return
								}
								width := bar.Bounds().W
								if width <= 0 {
									width = max(fixedWidth, ControlSM)
								}
								pixels := math.Max(1, float64(width-quotaMarkerWidth))
								used := math.Round(clamped/100*pixels) / pixels
								elapsed := math.Round(win.elapsedRatio*pixels) / pixels
								renderQuotaBar(c, winKey, used, elapsed, win.paceKnown, projected, quotaLevelColor(c, win.UsedPercent), keyPrefix+"pace-marker")
							})
						}

						renderStatusText := func(key, text string, tone quotaStatusTone) {
							ui.Row(c.Key(key)).
								Width(statusColW).
								Shrink(0).
								Justify(ui.End).
								AlignItems(ui.Center).
								Children(func() {
									if text == "" {
										return
									}
									st := ui.Text(c, text).
										FontSize(uiFont(TypeCaption)).
										TextColor(quotaStatusColor(c, tone)).
										SingleLine().
										Ellipsis("…")
									if tone != quotaStatusNormal {
										st.FontWeight(500)
									}
								})
						}

						tableMode := selectable || g.MultiAccount
						if !tableMode && len(g.Accounts) == 1 {
							acc := g.Accounts[0]
							accKey := fmt.Sprintf("%s-a-0", groupKey)
							hasAnyStatus := false
							for _, w := range acc.Windows {
								if w.CompactStatus != "" {
									hasAnyStatus = true
									break
								}
							}
							for wIdx, win := range acc.Windows {
								winKey := fmt.Sprintf("%s-w-%d", accKey, wIdx)
								curWin := win
								row := ui.Row(c.Key(winKey)).
									Height(RowHeight).
									Margin(0, Space2).
									Padding(0, Space2).
									Radius(RadiusSM).
									Focusable().
									AlignItems(ui.Center).
									Gap(Space3).
									Transition(motionFeedback(c))
								rowActive := row.Hovered() || row.Focused()
								if rowActive {
									row.Background(ThemeOverlayHover(c))
								}
								focusRing(c, row)
								if tip := quotaRowTooltipText(curWin.hoverLeft, curWin.hoverRight, curWin.HoverDetail); tip != "" {
									row.Tooltip(tip)
								}
								row.Children(func() {
									ui.Text(c, curWin.ShortLabel).
										FontSize(uiFont(TypeFootnote)).
										TextColor(ThemeSecondaryText(c)).
										MinWidth(32 * uiScaleFactor()).
										SingleLine().
										Shrink(0)

									if curWin.NoBar {
										ui.Spacer(c)
										content := ""
										switch {
										case curWin.Text:
											content = curWin.Display
										case curWin.BalanceText != "":
											content = curWin.BalanceText
										case curWin.Unlimited:
											content = "不限量"
										}
										if content != "" {
											ui.Text(c, content).
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemePrimaryText(c)).
												SingleLine().
												Ellipsis("…").
												Shrink(1).
												MinWidth(0)
										}
										if hasAnyStatus {
											renderStatusText(winKey+"-status", curWin.CompactStatus, curWin.statusTone)
										}
										return
									}

									if curWin.Display != "" {
										ui.Text(c, curWin.Display).
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeTertiaryText(c)).
											SingleLine().
											Ellipsis("…").
											Shrink(1).
											MinWidth(0)
									}
									renderBar(curWin, winKey, rowActive, 0)
									renderQuotaPercent(c, winKey, curWin.UsedPercent, ui.End)
									if hasAnyStatus {
										renderStatusText(winKey+"-status", curWin.CompactStatus, curWin.statusTone)
									}
								})
							}
						} else {
							cols := quotaGroupColumnsResolved(g)
							hasAnyStatus := false
							hasWarnOrDanger := false
							for _, acc := range g.Accounts {
								if acc.CompactStatus != "" {
									hasAnyStatus = true
								}
								if acc.statusTone != quotaStatusNormal {
									hasWarnOrDanger = true
								}
							}

							if len(cols) > 0 {
								statusHead := "重置"
								if hasWarnOrDanger {
									statusHead = "状态"
								}
								ui.Row(c.Key(groupKey+"-col-head")).
									FillWidth().
									Height(ControlKbd).
									Padding(0, Space4).
									AlignItems(ui.Center).
									Gap(colGap).
									Children(func() {
										ui.Text(c, "账号").
											FontSize(uiFont(TypeCaption)).
											FontWeight(500).
											TextColor(ThemeTertiaryText(c)).
											Grow(1).
											MinWidth(0).
											SingleLine()

										for ci, col := range cols {
											ui.Row(c.Key(fmt.Sprintf("%s-col-%d", groupKey, ci))).
												Width(winColW).
												Shrink(0).
												AlignItems(ui.Center).
												Children(func() {
													ui.Text(c, col.ShortLabel).
														FontSize(uiFont(TypeCaption)).
														FontWeight(500).
														TextColor(ThemeTertiaryText(c)).
														SingleLine()
												})
										}

										if hasAnyStatus || len(cols) > 0 {
											ui.Row(c.Key(groupKey + "-col-status")).
												Width(statusColW).
												Shrink(0).
												Justify(ui.End).
												AlignItems(ui.Center).
												Children(func() {
													ui.Text(c, statusHead).
														FontSize(uiFont(TypeCaption)).
														FontWeight(500).
														TextColor(ThemeTertiaryText(c)).
														SingleLine()
												})
										}
									})
							}

							for aIdx, acc := range g.Accounts {
								accKey := fmt.Sprintf("%s-a-%d", groupKey, aIdx)
								curAcc := acc

								renderDot := func() {
									dotColor := ThemeOverlay(c, 0.18)
									switch {
									case curAcc.IsActive:
										dotColor = ThemeAccent(c)
									case curAcc.IsRecent:
										dotColor = ThemeSecondaryText(c)
									case curAcc.statusTone == quotaStatusDanger:
										dotColor = ThemeDanger(c)
									}
									ui.Box(c.Key(accKey+"-dot")).
										Size(Space2, Space2).
										Radius(RadiusPill).
										Background(dotColor).
										Shrink(0)
								}

								accDisabled := selectable && opts.AccountDisabled != nil && opts.AccountDisabled(curAcc.Account)
								accSelected := selectable && opts.SelectedAccount != "" && curAcc.Account == opts.SelectedAccount

								cells, extra := quotaRowAllocateColumns(curAcc.Windows, cols)

								var rowEl ui.Element
								var rowActive bool
								if selectable {
									btn := ui.ButtonBase(c.Key(accKey+"-btn")).
										Label(curAcc.DisplayName).
										Height(RowHeight).
										Margin(0, Space2).
										Padding(0, Space2).
										Radius(RadiusSM).
										AlignItems(ui.Center).
										Gap(colGap).
										Checked(accSelected).
										Disabled(accDisabled).
										Transition(motionFeedback(c))
									rowActive = (!accDisabled && btn.Hovered()) || btn.Focused()
									switch {
									case accDisabled:
										btn.Opacity(0.55)
									case accSelected && rowActive:
										btn.Background(ThemeAccentSubtle(c)).Border(Hairline, ThemeAccent(c).Alpha(0.32))
									case accSelected:
										btn.Background(ThemeAccentSubtle(c).Alpha(0.75)).Border(Hairline, ThemeAccent(c).Alpha(0.32))
									case rowActive:
										btn.Background(ThemeOverlayHover(c))
									}
									if !accDisabled {
										focusRing(c, btn)
										btn.OnClick(func() {
											opts.OnSelectAccount(curAcc.Account)
										})
									}
									rowEl = btn
								} else {
									row := ui.Row(c.Key(accKey+"-row")).
										Height(RowHeight).
										Margin(0, Space2).
										Padding(0, Space2).
										Radius(RadiusSM).
										Focusable().
										AlignItems(ui.Center).
										Gap(colGap).
										Transition(motionFeedback(c))
									rowActive = row.Hovered() || row.Focused()
									if rowActive {
										row.Background(ThemeOverlayHover(c))
									}
									focusRing(c, row)
									rowEl = row
								}
								if tip := quotaRowTooltipText(curAcc.hoverLeft, curAcc.hoverRight, curAcc.HoverDetail); tip != "" {
									rowEl.Tooltip(tip)
								}

								rowEl.Children(func() {
									ui.Row(c.Key(accKey + "-acc-cell")).
										Grow(1).
										MinWidth(0).
										AlignItems(ui.Center).
										Gap(Space2).
										Children(func() {
											if selectable {
												renderQuotaRadioRing(c, accKey+"-radio", accSelected, accDisabled, rowActive)
											} else {
												renderDot()
											}

											ui.Text(c, curAcc.DisplayName).
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemePrimaryText(c)).
												SingleLine().
												Ellipsis("…").
												Shrink(1).
												MinWidth(0)

											if selectable && opts.SelectedAccount == "" && curAcc.IsActive {
												renderQuotaActiveBadge(c, accKey+"-active-badge")
											} else if !selectable && g.MultiAccount && curAcc.Plan != "" {
												ui.Text(c, curAcc.Plan).
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeTertiaryText(c)).
													SingleLine().
													Shrink(0)
											}
										})

									barFallbackW := max(winColW-pctW-Space1, 24)
									for ci := range cols {
										cellKey := fmt.Sprintf("%s-c-%d", accKey, ci)
										cell := cells[ci]
										ui.Row(c.Key(cellKey)).
											Width(winColW).
											Shrink(0).
											AlignItems(ui.Center).
											Gap(Space1).
											Children(func() {
												if cell.win == nil {
													ui.Row(c.Key(cellKey + "-dash")).
														FillWidth().
														Justify(ui.Center).
														Children(func() {
															ui.Text(c, "—").
																FontSize(uiFont(TypeCaption)).
																TextColor(ThemeTertiaryText(c)).
																SingleLine()
														})
													return
												}
												if cell.inlineTag {
													ui.Text(c, cell.win.ShortLabel).
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeTertiaryText(c)).
														SingleLine().
														Shrink(0)
												}
												renderBar(*cell.win, cellKey, rowActive, barFallbackW)
												renderQuotaPercent(c, cellKey, cell.win.UsedPercent, ui.Start)
											})
									}

									if hasAnyStatus || len(cols) > 0 {
										renderStatusText(accKey+"-status", curAcc.CompactStatus, curAcc.statusTone)
									}
								})

								for exIdx, ex := range extra {
									exKey := fmt.Sprintf("%s-ex-%d", accKey, exIdx)
									curEx := ex
									ui.Row(c.Key(exKey)).
										Height(ControlSM).
										Margin(0, Space2).
										Padding(0, Space2, 0, Space6).
										AlignItems(ui.Center).
										Gap(Space2).
										Children(func() {
											if curEx.ShortLabel != "" {
												ui.Text(c, curEx.ShortLabel).
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeTertiaryText(c)).
													SingleLine().
													Shrink(0)
											}
											if curEx.NoBar {
												ui.Spacer(c)
												content := ""
												switch {
												case curEx.Text:
													content = curEx.Display
												case curEx.BalanceText != "":
													content = curEx.BalanceText
												case curEx.Unlimited:
													content = "不限量"
												}
												if content != "" {
													ui.Text(c, content).
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeSecondaryText(c)).
														SingleLine().
														Ellipsis("…").
														Shrink(1).
														MinWidth(0)
												}
												return
											}
											if curEx.Display != "" {
												ui.Text(c, curEx.Display).
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeTertiaryText(c)).
													SingleLine().
													Ellipsis("…").
													Shrink(1).
													MinWidth(0)
											}
											renderBar(curEx, exKey, rowActive, 0)
											renderQuotaPercent(c, exKey, curEx.UsedPercent, ui.Start)
										})
								}
							}
						}

						if len(g.NoReadings) > 0 {
							discLabel := fmt.Sprintf("%d 项无读数", len(g.NoReadings))
							ui.Column(c.Key(groupKey+"-disclosure")).
								FillWidth().
								Padding(Space0, Space4, 0, Space4).
								Gap(Space1).
								Children(func() {
									discBtn := ui.ButtonBase(c.Key(groupKey+"-no-readings-btn")).
										Label(discLabel).
										AlignSelf(ui.Start).
										Padding(Space0, Space2).
										Radius(RadiusXS).
										AlignItems(ui.Center).
										Gap(Space1).
										Expanded(*noReadingsOpen).
										Transition(motionFeedback(c))
									if discBtn.Hovered() {
										discBtn.Background(ThemeOverlayHover(c))
									}
									focusRing(c, discBtn)
									discBtn.OnClick(func() {
										*noReadingsOpen = !*noReadingsOpen
									})
									discBtn.Children(func() {
										arrow := ui.Icon(c.Key(groupKey+"-nr-arrow"), IconChevronRight()).
											Size(12, 12).
											TextColor(ThemeTertiaryText(c)).
											Shrink(0)
										arrowAngle := float32(0)
										if *noReadingsOpen {
											arrowAngle = 90
										}
										if reduceMotion(c) {
											arrow.Jump(groupKey+"-nr-arrow", arrowAngle)
										}
										arrow.Rotate(arrow.AnimateTo(groupKey+"-nr-arrow", arrowAngle, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap}))
										ui.Text(c, discLabel).
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeTertiaryText(c)).
											SingleLine()
									})

									motionUnfoldBody(c, groupKey+"-no-readings-body", *noReadingsOpen, func() {
										ui.Column(c.Key(groupKey+"-nr-list")).
											FillWidth().
											Padding(Space1, Space2).
											Radius(RadiusSM).
											Background(ThemeOverlayHover(c)).
											Gap(2).
											Children(func() {
												for nrIdx, item := range g.NoReadings {
													name := item.Label
													if g.MultiAccount && item.Account != "" {
														if name != "" {
															name = item.Account + " · " + name
														} else {
															name = item.Account
														}
													}
													ui.Row(c.Key(fmt.Sprintf("%s-nr-%d", groupKey, nrIdx))).
														FillWidth().
														Height(ControlXS).
														AlignItems(ui.Center).
														Gap(Space2).
														Children(func() {
															ui.Text(c, name).
																FontSize(uiFont(TypeCaption)).
																TextColor(ThemeTertiaryText(c)).
																SingleLine().
																Ellipsis("…").
																Shrink(1).
																MinWidth(0)
															ui.Spacer(c)
															ui.Text(c, item.Value).
																FontSize(uiFont(TypeCaption)).
																TextColor(ThemeTertiaryText(c)).
																SingleLine().
																Shrink(0)
														})
												}
											})
									})
								})
						}
					}

					if collapsibleGroup {
						motionUnfoldBody(c, groupKey+"-body", *groupExpanded, renderGroupBody)
					} else {
						renderGroupBody()
					}
				})
			}
		})
}
