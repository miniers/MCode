package mcodeui

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

type quotaGroupListOptions struct {
	HideSingleGroupTitle bool
}

func quotaGroupTitleVisible(groupCount int, opts quotaGroupListOptions) bool {
	return !opts.HideSingleGroupTitle || groupCount != 1
}

func renderQuotaGroupList(c *ui.Context, keyPrefix string, groups []trayQuotaGroupModel, opts quotaGroupListOptions) {
	ui.Column(c.Key(keyPrefix+"list")).
		FillWidth().
		Padding(0, Space4).
		Gap(Space5).
		Children(func() {
			for gIdx, g := range groups {
				groupKey := fmt.Sprintf("%sg-%d", keyPrefix, gIdx)
				if gIdx > 0 {
					// 相邻供应商之间用细分割线隔开
					ui.Row(c.Key(groupKey + "-divider")).
						FillWidth().
						Height(Hairline).
						Background(ThemeDivider(c))
				}

				ui.Column(c.Key(groupKey)).
					FillWidth().
					Gap(Space3).
					Children(func() {
						// 标题行：IconMD 图标 + 供应商名 + 套餐名，右侧是最早重置时间（有错误时改为错误文字）
						if quotaGroupTitleVisible(len(groups), opts) {
							ui.Row(c.Key(groupKey + "-title-row")).
								FillWidth().
								Height(ControlSM).
								AlignItems(ui.Center).
								Gap(Space3).
								Children(func() {
									ui.Box(c.Key(groupKey+"-icon-box")).
										Size(IconMD, IconMD).
										Center().
										Children(func() {
											renderProviderIconColor(c, g.IconID, IconMD, ThemeSecondaryText(c))
										})

									ui.Text(c, g.Title).
										FontSize(uiFont(TypeProse)).
										Bold().
										TextColor(ThemePrimaryText(c)).
										SingleLine()

									if !g.HasErr && g.Plan != "" {
										ui.Text(c, "· "+g.Plan).
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeSecondaryText(c)).
											SingleLine()
									}

									ui.Spacer(c)

									if g.HasErr {
										ui.Text(c, g.ErrText).
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeSecondaryText(c)).
											SingleLine()
										return
									}

									if g.HeadText != "" {
										textColor := ThemeSecondaryText(c)
										if g.HeadWarn {
											textColor = ThemeWarning(c)
										}
										ui.Text(c, g.HeadText).
											FontSize(uiFont(TypeFootnote)).
											TextColor(textColor).
											SingleLine()
									}
								})
						} else if g.HasErr {
							ui.Text(c, g.ErrText).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
						}

						if g.HasErr {
							return
						}

						// 额度行左缩进 = 图标宽 IconMD + 图标与名称间距 Space3，与供应商名左边缘对齐。
						const rowIndent float32 = ControlSM

						for aIdx, acc := range g.Accounts {
							accKey := fmt.Sprintf("%s-a-%d", groupKey, aIdx)
							curAcc := acc

							// 圆点：在用账号用强调色，最近使用的账号用弱一级的次要色，其他账号用更弱的遮罩色
							renderDot := func(n int) {
								dotColor := ThemeOverlay(c, 0.18)
								if curAcc.IsActive && !curAcc.IsDisabled {
									dotColor = ThemeAccent(c)
								} else if curAcc.IsRecent && !curAcc.IsDisabled {
									dotColor = ThemeSecondaryText(c)
								}
								ui.Box(c.Key(fmt.Sprintf("%s-dot-%d", accKey, n))).
									Size(Space2, Space2).
									Radius(RadiusPill).
									Background(dotColor)
							}

							// 进度条：整行与半格共用，填充与配速虚影逻辑保持一致；fixedWidth 为 0 表示撑满剩余宽度。
							renderBar := func(win trayQuotaWindowModel, winKey string, projected bool, fixedWidth float32) {
								clamped := math.Max(0, math.Min(100, win.UsedPercent))
								ratio := float32(clamped / 100.0)

								bar := ui.Row(c.Key(winKey + "-bar")).
									Height(quotaBarHeight).
									Radius(quotaBarRadius).
									Background(quotaBarTrackColor(c)).
									Clip()
								if fixedWidth > 0 {
									bar.Width(fixedWidth)
								} else {
									bar.Grow(1).
										Width(0).
										MinWidth(ControlSM)
								}
								bar.Children(func() {
									if !win.hasElapsed {
										// 没有重置时间：按已用比例单色填充。
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
									// 有时间信息时按分段渲染：竖线落在时间进度上，整条用同一分档色，悬停时叠加同色虚影。
									renderQuotaBarSegments(c, winKey, quotaBarSegments(clamped/100, win.elapsedRatio, win.paceKnown, projected), quotaLevelColor(c, win.UsedPercent), keyPrefix+"pace-marker")
								})
							}

							// 整行宽窗口（带读数或不画条）：标签 + 定宽进度条 + 定宽百分比。
							renderWindow := func(win trayQuotaWindowModel, wIdx int) {
								winKey := fmt.Sprintf("%s-w-%d", accKey, wIdx)

								winRow := ui.Row(c.Key(winKey)).
									FillWidth().
									Height(ControlXS).
									AlignItems(ui.Center).
									Tooltip(win.Tooltip)
								// 悬停整行额度窗口时显示按当前配速预测的虚影。
								projected := winRow.Hovered()
								winRow.Children(func() {
									ui.Box(c.Key(winKey + "-indent")).
										Width(rowIndent)

									ui.Row(c.Key(winKey + "-content")).
										Grow(1).
										AlignItems(ui.Center).
										Gap(Space4).
										Children(func() {
											ui.Row(c.Key(winKey + "-label-row")).
												Grow(1).
												AlignItems(ui.Center).
												Gap(Space1).
												Children(func() {
													ui.Text(c, win.ShortLabel).
														FontSize(uiFont(TypeFootnote)).
														TextColor(ThemeSecondaryText(c)).
														SingleLine().
														Shrink(0)

													if win.RightText != "" && trayQuotaLabeledText(win.ShortLabel, win.RightText) != curAcc.HeadText {
														rightColor := ThemeTertiaryText(c)
														if win.IsEarlyRunOut {
															rightColor = ThemeWarning(c)
														}
														ui.Text(c, win.RightText).
															FontSize(uiFont(TypeCaption)).
															TextColor(rightColor).
															SingleLine().
															Ellipsis("…").
															Shrink(1).
															MinWidth(0)
													}

													// 画条窗口自带的读数（如金额）跟在标签后，进度条与百分比照旧。
													if win.Display != "" && !win.NoBar {
														ui.Text(c, win.Display).
															FontSize(uiFont(TypeCaption)).
															TextColor(ThemeTertiaryText(c)).
															SingleLine().
															Ellipsis("…").
															Shrink(1).
															MinWidth(0)
													}

													ui.Spacer(c)
												})

											if win.NoBar {
												content := ""
												switch {
												case win.Text:
													content = win.Display
												case win.BalanceText != "":
													content = win.BalanceText
												case win.Unlimited:
													content = "不限量"
												}
												if content != "" {
													ui.Text(c, content).
														FontSize(uiFont(TypeBody)).
														TextColor(ThemePrimaryText(c)).
														SingleLine().
														Ellipsis("…").
														Shrink(1).
														MinWidth(0)
												}
												return
											}

											renderBar(win, winKey, projected, 120)

											ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(win.UsedPercent)))).
												FontSize(uiFont(TypeBody)).
												FontWeight(600).
												TextColor(quotaPercentColor(c, win.UsedPercent)).
												Width(40).
												TextAlign(ui.End)
										})
								})
							}

							// 半格：同一行左右并排的画条窗口，标签 + 撑满的进度条 + 定宽百分比。
							renderHalf := func(win trayQuotaWindowModel, wIdx int) {
								winKey := fmt.Sprintf("%s-w-%d", accKey, wIdx)

								half := ui.Row(c.Key(winKey)).
									Grow(1).
									Width(0).
									AlignItems(ui.Center).
									Gap(Space2).
									Tooltip(win.Tooltip)
								// 悬停半格同样显示按当前配速预测的虚影。
								projected := half.Hovered()
								half.Children(func() {
									labelColor := ThemeSecondaryText(c)
									if win.IsEarlyRunOut {
										labelColor = ThemeWarning(c)
									}
									ui.Text(c, win.ShortLabel).
										FontSize(uiFont(TypeFootnote)).
										TextColor(labelColor).
										SingleLine().
										Shrink(0)

									// 画条窗口自带的读数（如金额）跟在标签后，进度条与百分比照旧。
									if win.Display != "" {
										ui.Text(c, win.Display).
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeTertiaryText(c)).
											SingleLine().
											Ellipsis("…").
											Shrink(1).
											MinWidth(0)
									}

									renderBar(win, winKey, projected, 0)

									ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(win.UsedPercent)))).
										FontSize(uiFont(TypeBody)).
										FontWeight(600).
										TextColor(quotaPercentColor(c, win.UsedPercent)).
										Width(36).
										TextAlign(ui.End)
								})
							}

							// 半格行：缩进后放一或两个半格；只有一个时右侧留空占位，使各行左右半格按列对齐。
							renderPair := func(firstIdx, secondIdx int) {
								pairKey := fmt.Sprintf("%s-pair-%d", accKey, firstIdx)
								ui.Row(c.Key(pairKey)).
									FillWidth().
									Height(ControlXS).
									AlignItems(ui.Center).
									Children(func() {
										ui.Box(c.Key(pairKey + "-indent")).
											Width(rowIndent)
										ui.Row(c.Key(pairKey + "-halves")).
											Grow(1).
											AlignItems(ui.Center).
											Gap(Space5).
											Children(func() {
												renderHalf(curAcc.Windows[firstIdx], firstIdx)
												if secondIdx < 0 {
													ui.Box(c.Key(pairKey + "-empty")).
														Grow(1).
														Width(0)
													return
												}
												renderHalf(curAcc.Windows[secondIdx], secondIdx)
											})
									})
							}

							ui.Column(c.Key(accKey)).
								FillWidth().
								Gap(Space3).
								Children(func() {
									if !g.MultiAccount && curAcc.IsDisabled {
										ui.Row(c.Key(accKey + "-stopped")).
											FillWidth().
											Height(ControlXS).
											AlignItems(ui.Center).
											Children(func() {
												ui.Box(c.Key(accKey + "-stopped-indent")).Width(rowIndent)
												ui.Text(c, "已停止").
													FontSize(uiFont(TypeFootnote)).
													TextColor(ThemeSecondaryText(c)).
													SingleLine()
											})
										return
									}

									// 多账号时账号名单独一行（含状态圆点与套餐名），额度行统一缩进到供应商名。
									if g.MultiAccount {
										ui.Row(c.Key(accKey + "-namerow")).
											FillWidth().
											Height(ControlXS).
											AlignItems(ui.Center).
											Children(func() {
												ui.Box(c.Key(accKey + "-name-indent")).
													Width(rowIndent)

												ui.Row(c.Key(accKey + "-name")).
													Grow(1).
													AlignItems(ui.Center).
													Gap(Space3).
													Children(func() {
														renderDot(0)

														nameColor := ThemePrimaryText(c)
														if curAcc.IsDisabled {
															nameColor = ThemeSecondaryText(c)
														}
														ui.Text(c, curAcc.DisplayName).
															FontSize(uiFont(TypeFootnote)).
															TextColor(nameColor).
															SingleLine().
															Ellipsis("…").
															Shrink(1).
															MinWidth(0)

														if curAcc.Plan != "" {
															ui.Text(c, "· "+curAcc.Plan).
																FontSize(uiFont(TypeFootnote)).
																TextColor(ThemeSecondaryText(c)).
																SingleLine()
														}

														ui.Spacer(c)

														if curAcc.HeadText != "" {
															textColor := ThemeSecondaryText(c)
															if curAcc.HeadWarn {
																textColor = ThemeWarning(c)
															}
															ui.Text(c, curAcc.HeadText).
																FontSize(uiFont(TypeFootnote)).
																TextColor(textColor).
																SingleLine()
														}
													})
											})
									}

									// 相邻画条窗口两两并排，宽窗口独占整行。
									for wIdx := 0; wIdx < len(curAcc.Windows); wIdx++ {
										win := curAcc.Windows[wIdx]
										if win.Wide {
											renderWindow(win, wIdx)
											continue
										}
										secondIdx := -1
										if wIdx+1 < len(curAcc.Windows) && !curAcc.Windows[wIdx+1].Wide {
											secondIdx = wIdx + 1
										}
										renderPair(wIdx, secondIdx)
										if secondIdx >= 0 {
											wIdx++
										}
									}
								})
						}
					})
			}
		})
}
