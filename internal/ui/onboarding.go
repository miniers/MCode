package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// onboardingRoute 保存新手引导所需的状态。
type onboardingRoute int

const (
	onboardingRecommended onboardingRoute = iota
	onboardingDirect
)

// onboardingStep 保存新手引导所需的状态。
type onboardingStep int

const (
	onboardingWelcome onboardingStep = iota
	onboardingAgents
	onboardingMagpie
	onboardingFinish
)

// onboardingUI 保存新手引导所需的状态。
type onboardingUI struct {
	mu                     sync.Mutex
	open                   bool
	step                   onboardingStep
	route                  onboardingRoute
	suspendedForSettings   bool
	nodeOK, npmOK, probing bool
	lastRefresh            time.Time
	saveError              string
	generation             uint64
	animatedStep           onboardingStep
	direction              float32
	animateOpen            bool
}

var onboardingStates = struct {
	sync.Mutex
	states map[*chatState]*onboardingUI
}{states: make(map[*chatState]*onboardingUI)}
var onboardingComplete = CompleteOnboarding

// onboardingState 处理新手引导中的对应流程。
func onboardingState(s *chatState) *onboardingUI {
	onboardingStates.Lock()
	defer onboardingStates.Unlock()
	if onboardingStates.states[s] == nil {
		onboardingStates.states[s] = &onboardingUI{}
	}
	return onboardingStates.states[s]
}

// onboardingNextStep 处理新手引导中的对应流程。
func onboardingNextStep(route onboardingRoute, step onboardingStep) onboardingStep {
	if step >= onboardingFinish {
		return onboardingFinish
	}
	if route == onboardingDirect && step == onboardingAgents {
		return onboardingFinish
	}
	return step + 1
}

// onboardingPrevStep 处理新手引导中的对应流程。
func onboardingPrevStep(route onboardingRoute, step onboardingStep) onboardingStep {
	if step <= onboardingWelcome {
		return onboardingWelcome
	}
	if route == onboardingDirect && step == onboardingFinish {
		return onboardingAgents
	}
	return step - 1
}

// openOnboarding 处理新手引导中的对应流程。
func openOnboarding(s *chatState) {
	if s == nil {
		return
	}
	u := onboardingState(s)
	u.mu.Lock()
	u.open, u.step, u.route = true, onboardingWelcome, onboardingRecommended
	u.suspendedForSettings, u.saveError, u.animateOpen = false, "", true
	u.animatedStep, u.direction = onboardingWelcome, 1
	u.mu.Unlock()
	refreshOnboarding(s)
}

// closeOnboarding 处理新手引导中的对应流程。
func closeOnboarding(s *chatState, complete bool) {
	if s == nil {
		return
	}
	u := onboardingState(s)
	if complete {
		if err := onboardingComplete(); err != nil {
			u.mu.Lock()
			u.saveError = "无法保存引导状态，请重试"
			u.mu.Unlock()
			s.invalidate()
			return
		}
	}
	u.mu.Lock()
	u.open = false
	u.suspendedForSettings = false
	u.mu.Unlock()
	onboardingStates.Lock()
	delete(onboardingStates.states, s)
	onboardingStates.Unlock()
	s.invalidate()
}

// refreshOnboarding 处理新手引导中的对应流程。
func refreshOnboarding(s *chatState) {
	st := getChatSettings(s)
	if st != nil {
		st.loadProviderRows()
		st.loadAgy(false)
		loadCustomEndpoints(st)
		magpieLoadFor(st, "pi")()
	}
	u := onboardingState(s)
	u.mu.Lock()
	u.generation++
	generation := u.generation
	u.probing = true
	u.lastRefresh = time.Now()
	u.mu.Unlock()
	go func() {
		node, npm := probeOnboardingPrerequisites()
		u.mu.Lock()
		if u.generation == generation {
			u.nodeOK, u.npmOK, u.probing = node, npm, false
		}
		u.mu.Unlock()
		s.invalidate()
	}()
	s.invalidate()
}

// onboardingSuspend 处理新手引导中的对应流程。
func onboardingSuspend(s *chatState, page string, gateway bool) {
	st := getChatSettings(s)
	if st == nil {
		return
	}
	u := onboardingState(s)
	u.mu.Lock()
	u.suspendedForSettings = true
	u.mu.Unlock()
	openSettingsWindow(s, page)
	st = currentSettingsWindowState()
	if st == nil {
		u.mu.Lock()
		u.suspendedForSettings = false
		u.mu.Unlock()
		return
	}
	if gateway && st != nil {
		gateways, _ := magpieGateways(st)
		if len(gateways) > 0 {
			openMagpieGatewaySheet(st, gateways[0].ID)
		}
	}
}

// onboardingResume 处理新手引导中的对应流程。
func onboardingResume(s *chatState, st *SettingsState) bool {
	onboardingStates.Lock()
	u := onboardingStates.states[s]
	onboardingStates.Unlock()
	if u == nil {
		return false
	}
	settingsOpen := st != nil && st.IsOpen()
	u.mu.Lock()
	resume := u.open && u.suspendedForSettings && !settingsOpen
	if resume {
		u.suspendedForSettings = false
	}
	visible := u.open && !u.suspendedForSettings
	u.mu.Unlock()
	if resume {
		refreshOnboarding(s)
	}
	return visible && !settingsOpen
}

// onboardingSetStep 处理新手引导中的对应流程。
func onboardingSetStep(s *chatState, step onboardingStep) {
	u := onboardingState(s)
	u.mu.Lock()
	u.step = step
	u.mu.Unlock()
	s.invalidate()
}

// onboardingInstallDisabled 处理新手引导中的对应流程。
func onboardingInstallDisabled(id string, node, npm, probing bool) bool {
	return onboardingNeedsNode(id) && (probing || !node || !npm)
}

// onboardingMagpieStatus 处理新手引导中的对应流程。
func onboardingMagpieStatus(st *SettingsState) (string, bool) {
	if st == nil {
		return "检查中", false
	}
	connected, known := magpieConnected(st)
	if connected {
		return "已连接", true
	}
	if !known {
		return "检查中", false
	}
	gateways, _ := magpieGateways(st)
	for _, gateway := range gateways {
		if gateway.Enabled && !gateway.HasAPIKey {
			return "缺少 API Key", false
		}
	}
	return "未连接", false
}

// onboardingProviderStatus 处理新手引导中的对应流程。
func onboardingProviderStatus(st *SettingsState, id string) providerRowStatus {
	if st == nil {
		return providerRowStatus{Summary: "检查中"}
	}
	st.mu.Lock()
	loaded := st.providerRows.loaded
	st.mu.Unlock()
	if !loaded {
		return providerRowStatus{Summary: "检查中"}
	}
	status := providerStatusFor(st, id)
	if id == "codex" && !providerRowNotInstalled(st, id) && !strings.Contains(status.Summary, "登录") {
		status.Summary = "已安装，登录状态未知"
		status.Ready = false
	}
	return status
}

// onboardingSummaryRoute 把推荐路径的 Magpie 状态合入摘要；直连路径记为跳过。
func onboardingSummaryRoute(route onboardingRoute, magpie string) string {
	if route == onboardingDirect {
		return "已跳过"
	}
	return magpie
}

// onboardingFinishReady 判断完成页是否显示就绪：推荐路径只需 Magpie 已连接，
// 直连路径看任一 CLI 是否可用。
func onboardingFinishReady(route onboardingRoute, st *SettingsState, connected bool) bool {
	if st == nil {
		return false
	}
	if route == onboardingRecommended {
		return connected
	}
	for _, id := range []string{"claude", "codex", "agy"} {
		if onboardingProviderStatus(st, id).Ready {
			return true
		}
	}
	return false
}

// onboardingStatusTone 处理新手引导中的对应流程。
func onboardingStatusTone(status providerRowStatus, missing bool) Tone {
	if status.Ready {
		return ToneSuccess
	}
	if missing {
		return ToneWarning
	}
	return ToneNeutral
}

// onboardingProviderRow 处理新手引导中的对应流程。
func onboardingProviderRow(c *ui.Context, s *chatState, st *SettingsState, id string, index int) {
	status := onboardingProviderStatus(st, id)
	missing := st != nil && providerRowNotInstalled(st, id)
	u := onboardingState(s)
	u.mu.Lock()
	node, npm, probing := u.nodeOK, u.npmOK, u.probing
	u.mu.Unlock()
	row := ui.Column(c.Key("onboarding-provider-" + id)).FillWidth().Gap(Space1).Padding(Space3).Radius(RadiusMD).Background(ThemeSurfaceCard(c))
	onboardingEntry(c, row, index)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			renderProviderRowIcon(c, id, IconMD)
			ui.Text(c, providerDisplayName(id)).FontSize(uiFont(TypeBody)).Grow(1)
			onboardingBadge(c, "onboarding-status-"+id, status.Summary, onboardingStatusTone(status, missing))
			action, label := "settings", "设置"
			if missing {
				action, label = "install", "安装"
			} else if id == "agy" && !status.Ready && status.Summary != "检查中" {
				action, label = "login", "登录"
			}
			Button(c, ButtonOpts{Key: "onboarding-action-" + id, Text: label, Variant: ButtonSecondary, Size: ControlSM, Disabled: st == nil || (action == "install" && onboardingInstallDisabled(id, node, npm, probing)), OnClick: func() {
				if action == "settings" {
					onboardingSuspend(s, "providers", false)
				} else {
					launchProviderTerminalAction(st, id, action)
				}
			}})
		})
		if missing && onboardingNeedsNode(id) && !probing && (!node || !npm) {
			ui.Text(c, "需要先安装 Node.js：https://nodejs.org/").FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c))
		}
		if id == "agy" {
			ui.Text(c, "安装后在终端运行 agy，选择 Google 登录").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
		}
	})
}

// onboardingButton 处理新手引导中的对应流程。
func onboardingButton(c *ui.Context, key, text string, primary bool, fn func()) {
	variant := ButtonSecondary
	if primary {
		variant = ButtonPrimary
	}
	Button(c, ButtonOpts{Key: key, Text: text, Variant: variant, OnClick: fn})
}

// renderOnboardingDialog 处理新手引导中的对应流程。
func renderOnboardingDialog(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getChatSettings(s)
	if !onboardingResume(s, st) {
		return
	}
	u := onboardingState(s)
	u.mu.Lock()
	step, route, saveError := u.step, u.route, u.saveError
	u.mu.Unlock()
	open := true
	width, height := c.Size()
	width = min(float32(640), max(float32(0), width-32))
	height = max(float32(0), height-32)
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		panel.MaxWidth(width).MaxHeight(height)
		card := applyElevation(c, ui.Column(c.Key("onboarding-card")).Label("新用户引导").Width(width).MaxHeight(height).Radius(RadiusLG).Background(ThemeWindowBg(c)).Border(Hairline, ThemeBorder(c)).Clip(), ElevDialog)
		u.mu.Lock()
		animateOpen := u.animateOpen
		u.animateOpen = false
		u.mu.Unlock()
		if animateOpen && !reduceMotion(c) {
			card.Jump("onboarding-open", 0)
		}
		pop := float32(1)
		if reduceMotion(c) {
			card.Jump("onboarding-open", 1)
		} else {
			pop = card.AnimateTo("onboarding-open", 1, springSnappy)
		}
		card.Scale(0.96+0.04*pop, 0.96+0.04*pop).Opacity(pop)
		panel.Children(func() {
			card.Children(func() {
				ui.Row(c.Key("onboarding-header")).FillWidth().Padding(Space4, Space5, 0, Space5).AlignItems(ui.Center).Children(func() {
					ui.Row(c).Grow(1).Gap(Space2).AlignItems(ui.Center).Children(func() {
						for i := onboardingWelcome; i <= onboardingFinish; i++ {
							dot := ui.Box(c.Key(fmt.Sprintf("onboarding-dot-%d", i))).Height(Space2).Radius(RadiusPill)
							target := float32(0)
							if i == step {
								target = 1
							}
							v := dot.AnimateTo("active", target, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})
							dot.Width(Space2 + Space3*v).Background(ThemeBorder(c).Mix(ThemeAccent(c), v))
						}
					})
					IconButton(c, IconButtonProps{Key: "onboarding-close", Icon: IconX(), Label: "关闭", Variant: ButtonGhost, OnClick: func() { closeOnboarding(s, true) }})
				})
				ui.Scroll(c.Key("onboarding-scroll")).Grow(1).Padding(Space4, Space5).Children(func() {
					body := ui.Column(c.Key("onboarding-body")).FillWidth().Gap(Space4)
					u.mu.Lock()
					if u.animatedStep != step {
						if step < u.animatedStep {
							u.direction = -1
						} else {
							u.direction = 1
						}
						u.animatedStep = step
						if !reduceMotion(c) {
							body.Jump("step", 0)
						}
					}
					dir := u.direction
					u.mu.Unlock()
					progress := float32(1)
					if reduceMotion(c) {
						body.Jump("step", 1)
					} else {
						progress = body.AnimateTo("step", 1, springGentle)
					}
					body.Translate(dir*(1-progress)*24, 0).Opacity(progress).Children(func() { renderOnboardingStep(c, s, st, step, route) })
				})
				if saveError != "" {
					ui.Text(c, saveError).Padding(0, Space5, Space2, Space5).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				}
				ui.Row(c.Key("onboarding-footer")).FillWidth().Gap(Space2).Padding(Space3, Space5, Space5, Space5).AlignItems(ui.Center).Children(func() {
					if step > onboardingWelcome {
						onboardingButton(c, "onboarding-back", "上一步", false, func() { onboardingSetStep(s, onboardingPrevStep(route, step)) })
					}
					ui.Box(c).Grow(1)
					switch step {
					case onboardingWelcome:
						onboardingButton(c, "onboarding-direct", "只用直连", false, func() {
							u.mu.Lock()
							u.route = onboardingDirect
							u.mu.Unlock()
							onboardingSetStep(s, onboardingAgents)
						})
						onboardingButton(c, "onboarding-recommended", "使用推荐配置", true, func() {
							u.mu.Lock()
							u.route = onboardingRecommended
							u.mu.Unlock()
							onboardingSetStep(s, onboardingAgents)
						})
					case onboardingFinish:
						onboardingButton(c, "onboarding-settings", "打开设置", false, func() { onboardingSuspend(s, "providers", false) })
						onboardingButton(c, "onboarding-start", "开始使用", true, func() { closeOnboarding(s, true) })
					default:
						onboardingButton(c, "onboarding-refresh", "重新检测", false, func() { refreshOnboarding(s) })
						label := "继续"
						if step == onboardingMagpie {
							label = "跳过"
						}
						onboardingButton(c, "onboarding-next", label, true, func() { onboardingSetStep(s, onboardingNextStep(route, step)) })
					}
				})
			})
		})
	})
	if !open {
		closeOnboarding(s, true)
	}
}

// renderOnboardingStep 处理新手引导中的对应流程。
func renderOnboardingStep(c *ui.Context, s *chatState, st *SettingsState, step onboardingStep, route onboardingRoute) {
	titles := [...]string{"欢迎使用 mcode", "选择你的智能体", "连接 Magpie", "准备好了"}
	ui.Text(c, titles[step]).FontSize(uiFont(TypePageTitle)).TextColor(ThemePrimaryText(c))
	switch step {
	case onboardingWelcome:
		renderOnboardingHero(c)
		ui.Row(c.Key("onboarding-hero")).FillWidth().Gap(Space3).Children(func() {
			cards := []struct {
				title, description string
				icon               *ui.SVG
			}{
				{"统一路由", "一个网关接入多家模型", IconNetwork()},
				{"额度与人设", "菜单栏看额度，随时切人设", IconChartBar()},
				{"子代理", "编排子代理由内置引擎运行", IconSwarm()},
			}
			for i, card := range cards {
				el := ui.Column(c.Key(card.title)).Grow(1).Padding(Space3).Gap(Space2).Radius(RadiusMD).Background(ThemeSurfaceCard(c))
				onboardingEntry(c, el, i)
				el.Children(func() {
					ui.Icon(c, card.icon).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
					ui.Text(c, card.title).FontSize(uiFont(TypeFootnote))
					ui.Text(c, card.description).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				})
			}
		})
		ui.Text(c, "mcode 与 Magpie 网关深度整合。推荐连接 Magpie：一个网关统一调用多个模型，菜单栏显示 Magpie 额度，可以切换人设。").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		ui.Text(c, "也可以只用 Claude、Codex 或 agy 直连。跳过后，Magpie 人设、Magpie 额度和网关模型路由不可用，以后可以随时在设置里补上。").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
	case onboardingAgents:
		ui.Text(c, "装好任意一个就能开始。安装会在系统终端里进行，完成后回来点「重新检测」。").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		for i, id := range []string{"claude", "codex", "agy"} {
			onboardingProviderRow(c, s, st, id, i)
		}
	case onboardingMagpie:
		ui.Text(c, "填写网关地址和 API Key。本机网关默认地址是 http://127.0.0.1:3425/v1。").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		status, connected := onboardingMagpieStatus(st)
		tone := ToneNeutral
		if connected {
			tone = ToneSuccess
		}
		ui.Row(c).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			onboardingBadge(c, "onboarding-magpie-status", status, tone)
			ui.Box(c).Grow(1)
			onboardingButton(c, "onboarding-magpie-settings", "打开 Magpie 设置", false, func() { onboardingSuspend(s, "magpie", true) })
		})
	case onboardingFinish:
		magpie, connected := onboardingMagpieStatus(st)
		ready := onboardingFinishReady(route, st, connected)
		if ready {
			ui.Box(c.Key("onboarding-celebration-wrap")).Width(onboardingCelebrationSize).Height(onboardingCelebrationSize).Children(func() {
				if !reduceMotion(c) {
					ring := ui.Box(c.Key("onboarding-celebration-ring")).Absolute().Width(onboardingNodeSize).Height(onboardingNodeSize).Radius(RadiusPill).Border(Hairline, ThemeAccent(c))
					k := ring.Keyframes("ring", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, DurSlow), Value: 1, Ease: ui.EaseOut}})
					ring.Scale(1+0.8*k, 1+0.8*k).Opacity(1 - k)
				}
				check := ui.Row(c.Key("onboarding-celebration")).Width(onboardingNodeSize).Height(onboardingNodeSize).Radius(RadiusPill).Background(ThemeAccent(c).Alpha(0.12)).AlignItems(ui.Center).Justify(ui.Center)
				pop := float32(1)
				if reduceMotion(c) {
					check.Jump("celebration", 1)
				} else {
					pop = check.AnimateTo("celebration", 1, springSnappy)
				}
				check.Scale(pop, pop).Opacity(pop).Children(func() { ui.Icon(c, IconCheck()).Size(IconLG, IconLG).TextColor(ThemeAccent(c)) })
			})
		}
		for i, id := range []string{"claude", "codex", "agy"} {
			status := onboardingProviderStatus(st, id)
			onboardingSummaryRow(c, i, providerDisplayName(id), status.Summary, status.Ready)
		}
		onboardingSummaryRow(c, 3, "Magpie", onboardingSummaryRoute(route, magpie), connected)
	}
}

// onboardingSummaryRow 渲染完成页中的单条状态。
func onboardingSummaryRow(c *ui.Context, index int, name, status string, ready bool) {
	row := ui.Row(c.Key("onboarding-summary-" + name)).Gap(Space2).AlignItems(ui.Center)
	onboardingEntry(c, row, index)
	row.Children(func() {
		if ready && status != "已跳过" {
			ui.Icon(c, IconCheck()).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
		} else if status == "已跳过" {
			ui.Text(c, "−").TextColor(ThemeSecondaryText(c))
		} else {
			ui.Box(c).Width(Space2).Height(Space2).Radius(RadiusPill).Background(ThemeWarning(c))
		}
		ui.Text(c, name+"："+status).FontSize(uiFont(TypeBody))
	})
}
