package mcodeui

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// TrayQuotaGroup 包含某个提供方的全部额度信息与状态。
type TrayQuotaGroup struct {
	Provider string
	Title    string
	Windows  []ProviderQuotaWindow
	Err      string
}

// TrayQuotaPanelOptions 配置托盘额度面板的运行选项与回调。
type TrayQuotaPanelOptions struct {
	Tray         *mygo.Tray
	Fetch        func(ctx context.Context) []TrayQuotaGroup
	OnShowWindow func()
	OnQuit       func()
	OnSummary    func(text string)
}

// TrayQuotaPanel 控制 macOS 菜单栏托盘的全部账号额度浮动面板。
type TrayQuotaPanel struct {
	opts TrayQuotaPanelOptions

	mu          sync.Mutex
	groups      []TrayQuotaGroup
	refreshedAt time.Time
	loading     bool
	// lastErr 是上一次刷新没拿到数据时的提示；groups 仍保留上次的数据。
	lastErr string

	panel *mygo.Window

	ticker   *time.Ticker
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewTrayQuotaPanel 创建托盘额度面板实例。
func NewTrayQuotaPanel(opts TrayQuotaPanelOptions) *TrayQuotaPanel {
	return &TrayQuotaPanel{
		opts:   opts,
		stopCh: make(chan struct{}),
	}
}

// Start 启动后台定时刷新：立即在后台刷新一次，之后每 5 分钟刷新一次。
func (p *TrayQuotaPanel) Start() {
	p.mu.Lock()
	select {
	case <-p.stopCh:
		p.mu.Unlock()
		return
	default:
	}
	if p.ticker != nil {
		p.mu.Unlock()
		return
	}
	p.ticker = time.NewTicker(5 * time.Minute)
	ticker := p.ticker
	stopCh := p.stopCh
	p.mu.Unlock()

	p.Refresh(false)

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				p.Refresh(true)
			}
		}
	}()
}

// Stop 停止定时器并关闭面板。
func (p *TrayQuotaPanel) Stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		if p.ticker != nil {
			p.ticker.Stop()
		}
		close(p.stopCh)
		p.mu.Unlock()
	})
	p.Close()
}

// Toggle 打开或关闭面板：面板开着就关，关着就用 NewFlyout 打开。
// 打开时先显示缓存数据，如果缓存超过 1 分钟或为空就调用 Refresh(false)。
func (p *TrayQuotaPanel) Toggle() {
	mygo.RunOnMain(func() {
		p.mu.Lock()
		if p.panel != nil && !p.panel.IsDestroyed() {
			panel := p.panel
			p.panel = nil
			p.mu.Unlock()
			panel.Close()
			return
		}
		p.mu.Unlock()

		panel := mygo.NewFlyout(mygo.FlyoutOptions{
			Tray:      p.opts.Tray,
			Placement: mygo.PlacementBottom,
			Gap:       4,
			Width:     400,
			Height:    480,
			MaxHeight: 480,
			Focusable: true,
			Shadow:    true,
			Popover:   true,
			Content:   ui.View(p.renderView),
		})
		panel.OnClosed(func() {
			p.mu.Lock()
			if p.panel == panel {
				p.panel = nil
			}
			p.mu.Unlock()
		})

		p.mu.Lock()
		p.panel = panel
		now := time.Now()
		needsRefresh := len(p.groups) == 0 || p.refreshedAt.IsZero() || now.Sub(p.refreshedAt) > time.Minute
		p.mu.Unlock()

		if needsRefresh {
			p.Refresh(false)
		}
	})
}

// Close 关闭面板。
func (p *TrayQuotaPanel) Close() {
	mygo.RunOnMain(func() {
		p.mu.Lock()
		panel := p.panel
		p.panel = nil
		p.mu.Unlock()

		if panel != nil && !panel.IsDestroyed() {
			panel.Close()
		}
	})
}

// Refresh 刷新额度数据：单飞控制（正在加载时直接返回），在 goroutine 里调用 Fetch，带 30 秒超时。
func (p *TrayQuotaPanel) Refresh(force bool) {
	p.mu.Lock()
	if !force {
		now := time.Now()
		if len(p.groups) > 0 && !p.refreshedAt.IsZero() && now.Sub(p.refreshedAt) <= time.Minute {
			p.mu.Unlock()
			return
		}
	}
	if p.loading {
		p.mu.Unlock()
		return
	}
	p.loading = true
	p.mu.Unlock()

	mygo.RunOnMain(func() {
		p.mu.Lock()
		panel := p.panel
		p.mu.Unlock()
		if panel != nil && !panel.IsDestroyed() {
			panel.Invalidate()
		}
	})

	go func() {
		defer func() {
			p.mu.Lock()
			p.loading = false
			p.mu.Unlock()

			mygo.RunOnMain(func() {
				p.mu.Lock()
				panel := p.panel
				p.mu.Unlock()
				if panel != nil && !panel.IsDestroyed() {
					panel.Invalidate()
				}
			})
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var groups []TrayQuotaGroup
		if p.opts.Fetch != nil {
			groups = p.opts.Fetch(ctx)
		}

		now := time.Now()
		p.mu.Lock()
		p.mergeTrayQuotaFetch(groups, now)
		// 空结果且旧数据非空时 groups 还是上次的数据，摘要也按它计算。
		groups = p.groups
		p.mu.Unlock()

		summary := trayQuotaSummary(groups)

		mygo.RunOnMain(func() {
			if p.opts.OnSummary != nil {
				p.opts.OnSummary(summary)
			}
			p.mu.Lock()
			panel := p.panel
			p.mu.Unlock()
			if panel != nil && !panel.IsDestroyed() {
				panel.Invalidate()
			}
		})
	}()
}

const trayQuotaFallbackSortMinutes = 100 * quotaMonthMinutes

// mergeTrayQuotaFetch 合并一次拉取结果：拿到空数据而手里已有旧数据时，保留旧数据与刷新时间，
// 记下「显示的是上次数据」；否则接受新数据与时间并清掉提示。调用方须持有 p.mu。
func (p *TrayQuotaPanel) mergeTrayQuotaFetch(groups []TrayQuotaGroup, now time.Time) {
	if len(groups) == 0 && len(p.groups) > 0 {
		p.lastErr = "刷新失败，显示的是上次数据"
		return
	}
	p.groups = groups
	p.refreshedAt = now
	p.lastErr = ""
}

// trayQuotaWindowDrawsBar 判断额度窗口是否绘制进度条。
// Text、Balance（余额且用量为 0）、Unlimited 窗口不绘制额度条，也不参与托盘摘要百分比计算。
func trayQuotaWindowDrawsBar(w ProviderQuotaWindow) bool {
	if w.Text || w.Unlimited {
		return false
	}
	if w.Balance != nil && w.UsedPercent == 0 {
		return false
	}
	return true
}

func trayQuotaFallbackLabel(label string) string {
	clean := strings.TrimSpace(label)
	if strings.EqualFold(clean, "credits") {
		return "额度"
	}
	return truncateRunes(clean, 8)
}

func formatTrayQuotaBalance(balance float64, unit string) string {
	val := strconv.FormatFloat(math.Round(balance*100)/100, 'f', -1, 64)
	unit = strings.TrimSpace(unit)
	if unit != "" {
		return fmt.Sprintf("剩余 %s %s", val, unit)
	}
	return fmt.Sprintf("剩余 %s", val)
}

// trayQuotaSummary 从各 Provider 额度组中提炼托盘摘要文字（如 "58%"）。
func trayQuotaSummary(groups []TrayQuotaGroup) string {
	var allWindows []ProviderQuotaWindow
	for _, g := range groups {
		if strings.TrimSpace(g.Err) != "" {
			continue
		}
		isAgy := strings.EqualFold(g.Provider, "agy")
		accGroups := groupQuotaWindowsByAccount(g.Windows)
		var groupMainWindows []ProviderQuotaWindow
		for _, acc := range accGroups {
			if acc.disabled {
				continue
			}
			for _, w := range acc.windows {
				if quotaWindowIsAuxiliary(w) {
					continue
				}
				if !trayQuotaWindowDrawsBar(w) {
					continue
				}
				if isAgy {
					if _, ok := agyQuotaStripWindowKey(w.Label); !ok {
						continue
					}
				}
				groupMainWindows = append(groupMainWindows, w)
			}
		}

		if len(groupMainWindows) == 0 {
			continue
		}

		var activeWindows []ProviderQuotaWindow
		for _, w := range groupMainWindows {
			if w.Active && w.Source != "magpie" {
				activeWindows = append(activeWindows, w)
			}
		}
		if len(activeWindows) > 0 {
			allWindows = append(allWindows, activeWindows...)
		} else {
			allWindows = append(allWindows, groupMainWindows...)
		}
	}

	if len(allWindows) == 0 {
		return ""
	}

	maxUsed := allWindows[0].UsedPercent
	for _, w := range allWindows[1:] {
		if w.UsedPercent > maxUsed {
			maxUsed = w.UsedPercent
		}
	}

	clamped := math.Max(0, math.Min(100, maxUsed))
	return fmt.Sprintf("%d%%", int(math.Round(clamped)))
}

type trayQuotaPanelModel struct {
	UpdatedText string
	IsStale     bool
	Loading     bool
	Groups      []trayQuotaGroupModel
	// StaleNote 非空时在内容上方用次要文字提示：当前展示的是上次刷新的数据。
	StaleNote string
}

type trayQuotaGroupModel struct {
	Provider string
	IconID   string
	Title    string
	// Plan 是该供应商的套餐名；多账号时不展示，避免把不同账号的套餐混在一起。
	Plan         string
	HeadText     string
	HeadWarn     bool
	MultiAccount bool
	HasErr       bool
	ErrText      string
	Accounts     []trayQuotaAccountModel
}

type trayQuotaAccountModel struct {
	Account     string
	DisplayName string
	// Plan 是该账号的套餐名（取组内第一个非空 Plan）。
	Plan     string
	IsActive bool
	// IsRecent 表示这是 Magpie 记录的、该 provider 最近应答过请求的账号。
	// 托盘没有会话上下文，只能说明「最近使用」，不代表当前会话在用。
	IsRecent   bool
	IsDisabled bool
	// HeadText 是该账号的头部文案，多账号时显示在账号名行右侧。
	HeadText string
	// HeadWarn 表示 HeadText 是提前用尽提示（需要警示色）。
	HeadWarn bool
	Windows  []trayQuotaWindowModel
}

// trayQuotaAccountDisplayName 去掉账号名里与提供方重名的前缀，例如 "codex · a@b.c" 只保留 "a@b.c"。
// 前缀与 Provider、IconID 或 Title 大小写不敏感地相等时才去掉；为空时回退到『默认账号』。
func trayQuotaAccountDisplayName(name, provider, iconID, title string) string {
	trimmed := strings.TrimSpace(name)
	if before, after, ok := strings.Cut(trimmed, " · "); ok {
		prefix := strings.TrimSpace(before)
		named := prefix != "" && (strings.EqualFold(prefix, provider) ||
			strings.EqualFold(prefix, iconID) ||
			strings.EqualFold(prefix, title))
		if named {
			trimmed = strings.TrimSpace(after)
		}
	}
	if trimmed == "" {
		return "默认账号"
	}
	return trimmed
}

type trayQuotaWindowModel struct {
	ShortLabel    string
	UsedPercent   float64
	RightText     string
	IsEarlyRunOut bool
	Wide          bool
	Tooltip       string
	resetsAt      time.Time
	sortMinutes   int
	// elapsedRatio/hasElapsed 是时间已过比例及其是否可信；paceKnown 表示配速可判定。
	elapsedRatio float64
	hasElapsed   bool
	paceKnown    bool

	NoBar       bool
	Text        bool
	Unlimited   bool
	BalanceText string
	Display     string
}

// trayQuotaLabeledText 为文案添加非空窗口标签。
func trayQuotaLabeledText(label, text string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return text
	}
	return label + ": " + text
}

// trayQuotaHead 汇总账号窗口的带标签倒计时文案。
// 第一个提前用尽提示优先并返回 warn=true；否则取晚于 now 的最早重置窗口。
func trayQuotaHead(windows []trayQuotaWindowModel, now time.Time) (text string, warn bool) {
	var earliest trayQuotaWindowModel
	for _, win := range windows {
		if win.IsEarlyRunOut {
			return trayQuotaLabeledText(win.ShortLabel, win.RightText), true
		}
		if !win.resetsAt.IsZero() && win.resetsAt.After(now) {
			if earliest.resetsAt.IsZero() || win.resetsAt.Before(earliest.resetsAt) {
				earliest = win
			}
		}
	}
	if !earliest.resetsAt.IsZero() {
		return trayQuotaLabeledText(earliest.ShortLabel, formatLimitReset(now, earliest.resetsAt)), false
	}
	return "", false
}

// buildTrayQuotaModel 将快照转换为行模型（纯函数，方便独立测试）。
func buildTrayQuotaModel(groups []TrayQuotaGroup, refreshedAt, now time.Time, loading bool) trayQuotaPanelModel {
	m := trayQuotaPanelModel{
		Loading: loading,
	}

	if !refreshedAt.IsZero() && !now.IsZero() {
		diff := now.Sub(refreshedAt)
		if diff > usageStaleAfter {
			m.IsStale = true
		}
		if diff < time.Minute {
			m.UpdatedText = "刚刚更新"
		} else {
			m.UpdatedText = fmt.Sprintf("%d 分钟前更新", int(diff.Minutes()))
		}
	}

	for _, g := range groups {
		iconID := g.Provider
		if after, ok := strings.CutPrefix(g.Provider, "magpie:"); ok {
			iconID = after
		}
		gm := trayQuotaGroupModel{
			Provider: g.Provider,
			IconID:   iconID,
			Title:    g.Title,
		}

		if strings.TrimSpace(g.Err) != "" {
			gm.HasErr = true
			if isUsageAuthError(g.Err) {
				gm.ErrText = "需要重新登录"
			} else {
				gm.ErrText = truncateRunes(strings.TrimSpace(g.Err), 24)
			}
			m.Groups = append(m.Groups, gm)
			continue
		}

		accGroups := groupQuotaWindowsByAccount(g.Windows)
		for _, acc := range accGroups {
			am := trayQuotaAccountModel{
				Account:     acc.account,
				DisplayName: trayQuotaAccountDisplayName(acc.account, g.Provider, iconID, g.Title),
				Plan:        quotaGroupPlan(acc.windows),
				IsDisabled:  acc.disabled,
			}
			if acc.active && !acc.disabled && !quotaWindowsFromMagpie(acc.windows) {
				am.IsActive = true
			}
			// Magpie 卡片没有 Active 标记，账号组里任一窗口的 Last 表示最近应答过请求。
			if !acc.disabled && quotaWindowsFromMagpie(acc.windows) {
				for _, w := range acc.windows {
					if w.Last {
						am.IsRecent = true
						break
					}
				}
			}
			if acc.disabled {
				am.DisplayName += " · 已停止"
			} else if am.IsRecent {
				am.DisplayName += " · 最近使用"
			}

			if !acc.disabled {
				isAgy := strings.EqualFold(g.Provider, "agy")
				for _, w := range acc.windows {
					if quotaWindowIsAuxiliary(w) {
						continue
					}

					var shortLabel string
					var sortMinutes int
					if isAgy {
						key, ok := agyQuotaStripWindowKey(w.Label)
						if !ok {
							continue
						}
						shortLabel = key
						switch key {
						case "5h":
							sortMinutes = 5 * quotaHourMinutes
						case "周":
							sortMinutes = quotaWeekMinutes
						}
					} else {
						key, minutes, ok := quotaStripWindowKey(w)
						if ok {
							shortLabel = key
							sortMinutes = minutes
						} else {
							shortLabel = trayQuotaFallbackLabel(w.Label)
							sortMinutes = trayQuotaFallbackSortMinutes
						}
					}

					state, runOutAt := quotaPace(now, w)
					rightText := paceRunOutText(now, runOutAt, state)
					elapsedRatio, hasElapsed := quotaElapsedRatio(now, w)
					isEarly := false
					if rightText != "" {
						isEarly = true
					} else {
						rightText = formatLimitReset(now, w.ResetsAt)
					}

					winModel := trayQuotaWindowModel{
						ShortLabel:    shortLabel,
						UsedPercent:   w.UsedPercent,
						RightText:     rightText,
						IsEarlyRunOut: isEarly,
						resetsAt:      w.ResetsAt,
						sortMinutes:   sortMinutes,
						elapsedRatio:  elapsedRatio,
						hasElapsed:    hasElapsed,
						paceKnown:     state != paceStateUnknown,
					}
					switch {
					case w.Text:
						winModel.NoBar = true
						winModel.Text = true
						winModel.Display = truncateRunes(strings.TrimSpace(w.Display), 24)
					case w.Balance != nil && w.UsedPercent == 0:
						winModel.NoBar = true
						winModel.BalanceText = truncateRunes(formatTrayQuotaBalance(*w.Balance, w.Unit), 24)
					case w.Unlimited:
						winModel.NoBar = true
						winModel.Unlimited = true
					default:
						if d := strings.TrimSpace(w.Display); d != "" {
							winModel.Display = truncateRunes(d, 32)
						}
					}
					winModel.Wide = winModel.NoBar || winModel.Display != ""

					resetText := formatLimitReset(now, w.ResetsAt)
					var tipParts []string
					if winModel.ShortLabel != "" {
						tipParts = append(tipParts, winModel.ShortLabel)
					}
					if resetText != "" {
						tipParts = append(tipParts, resetText)
					}
					if winModel.IsEarlyRunOut && winModel.RightText != "" {
						tipParts = append(tipParts, winModel.RightText)
					}
					winModel.Tooltip = strings.Join(tipParts, " · ")

					am.Windows = append(am.Windows, winModel)
				}

				slices.SortStableFunc(am.Windows, func(a, b trayQuotaWindowModel) int {
					return a.sortMinutes - b.sortMinutes
				})

				am.HeadText, am.HeadWarn = trayQuotaHead(am.Windows, now)
			}

			gm.Accounts = append(gm.Accounts, am)
		}

		gm.MultiAccount = len(gm.Accounts) > 1
		if !gm.MultiAccount && len(gm.Accounts) == 1 {
			gm.Plan = gm.Accounts[0].Plan
		}

		// 多账号时倒计时按账号分别显示，组头部不再汇总。
		if !gm.MultiAccount {
			for _, am := range gm.Accounts {
				if am.IsDisabled {
					continue
				}
				gm.HeadText = am.HeadText
				gm.HeadWarn = am.HeadWarn
				break
			}
		}

		m.Groups = append(m.Groups, gm)
	}

	return m
}

func (p *TrayQuotaPanel) renderView(c *ui.Context) {
	applyAppTheme(c)
	p.mu.Lock()
	groups := make([]TrayQuotaGroup, len(p.groups))
	copy(groups, p.groups)
	refreshedAt := p.refreshedAt
	loading := p.loading
	lastErr := p.lastErr
	p.mu.Unlock()

	now := time.Now()
	m := buildTrayQuotaModel(groups, refreshedAt, now, loading)
	m.StaleNote = lastErr
	p.renderModelView(c, m)
}

func trayPanelRootBackground(c *ui.Context, goos string) ui.Color {
	if goos == "darwin" {
		return ui.Transparent
	}
	return ThemeSurface(c)
}

func (p *TrayQuotaPanel) renderModelView(c *ui.Context, m trayQuotaPanelModel) {
	c.Root().Background(trayPanelRootBackground(c, runtime.GOOS))

	ui.Column(c.Key("tray-quota-panel-root")).
		Fill().
		Padding(Space4, 0).
		Gap(Space3).
		Children(func() {
			// 顶部：标题『额度』，右侧更新时间或加载状态与刷新按钮
			ui.Row(c.Key("tray-quota-header")).
				FillWidth().
				Padding(0, Space4).
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.Text(c, "额度").
						FontSize(TypeBody).
						Bold().
						TextColor(ThemePrimaryText(c))

					ui.Spacer(c)

					if m.UpdatedText != "" {
						textColor := ThemeSecondaryText(c)
						if m.IsStale {
							textColor = ThemeWarning(c)
						}
						ui.Text(c, m.UpdatedText).
							FontSize(TypeCaption).
							TextColor(textColor)
					}

					if m.Loading {
						ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					} else {
						refreshBtn := ui.ButtonBase(c.Key("tray-quota-refresh-btn")).
							Label("刷新").
							Tooltip("刷新额度").
							Size(ControlXS, ControlXS).
							Radius(RadiusXS).
							Center().
							Background(ui.Transparent).
							OnClick(func() {
								p.Refresh(true)
							})
						if refreshBtn.Hovered() {
							refreshBtn.Background(ThemeHover(c))
						}
						refreshBtn.Children(func() {
							ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
						})
					}
				})

			// 顶部与列表的分隔线
			ui.Row(c.Key("tray-quota-header-divider-wrap")).
				FillWidth().
				Padding(0, Space4).
				Children(func() {
					ui.Row(c.Key("tray-quota-header-divider")).
						FillWidth().
						Height(Hairline).
						Background(ThemeDivider(c))
				})

			// 上次刷新失败时保留旧数据，这里用次要文字提示数据不是最新的
			if m.StaleNote != "" {
				ui.Text(c, m.StaleNote).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c)).
					Padding(0, Space4)
			}

			// 内容区域（超高滚动）
			ui.Scroll(c.Key("tray-quota-scroll")).
				Grow(1).
				FillWidth().
				Children(func() {
					if len(m.Groups) == 0 {
						ui.Row(c.Key("tray-quota-empty")).
							FillWidth().
							Padding(Space8, Space4).
							AlignItems(ui.Center).
							Children(func() {
								ui.Spacer(c)
								ui.Text(c, "暂无可用额度数据").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
								ui.Spacer(c)
							})
						return
					}

					renderQuotaGroupList(c, "tray-quota-", m.Groups, quotaGroupListOptions{})
				})

			// 底部与列表的分隔线
			ui.Row(c.Key("tray-quota-footer-divider-wrap")).
				FillWidth().
				Padding(0, Space4).
				Children(func() {
					ui.Row(c.Key("tray-quota-footer-divider")).
						FillWidth().
						Height(Hairline).
						Background(ThemeDivider(c))
				})

			// 底部操作按钮
			ui.Row(c.Key("tray-quota-footer")).
				FillWidth().
				Padding(0, Space4).
				AlignItems(ui.Center).
				Children(func() {
					btnShow := ui.ButtonBase(c.Key("tray-quota-btn-show-window")).
						Label("显示窗口").
						Height(ControlSM).
						Radius(RadiusSM).
						Padding(0, Space4).
						Background(ui.Transparent).
						OnClick(func() {
							p.Close()
							if p.opts.OnShowWindow != nil {
								p.opts.OnShowWindow()
							}
						})
					if btnShow.Hovered() {
						btnShow.Background(ThemeHover(c))
					}
					btnShow.Children(func() {
						ui.Text(c, "显示窗口").
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c))
					})

					ui.Spacer(c)

					btnQuit := ui.ButtonBase(c.Key("tray-quota-btn-quit")).
						Label("退出").
						Height(ControlSM).
						Radius(RadiusSM).
						Padding(0, Space4).
						Background(ui.Transparent).
						OnClick(func() {
							if p.opts.OnQuit != nil {
								p.opts.OnQuit()
							}
						})
					if btnQuit.Hovered() {
						btnQuit.Background(ThemeHover(c))
					}
					btnQuit.Children(func() {
						ui.Text(c, "退出").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})
				})
		})
}
