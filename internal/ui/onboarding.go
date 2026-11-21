package mcodeui

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// onboardingUI 保存新手引导所需的状态。
type onboardingUI struct {
	mu                     sync.Mutex
	open                   bool
	suspendedForSettings   bool
	nodeOK, npmOK, probing bool
	lastRefresh            time.Time
	saveError              string
	generation             uint64
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

// openOnboarding 处理新手引导中的对应流程。
func openOnboarding(s *chatState) {
	if s == nil {
		return
	}
	u := onboardingState(s)
	u.mu.Lock()
	u.open = true
	u.suspendedForSettings, u.saveError, u.animateOpen = false, "", true
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

// onboardingInstallDisabled 处理新手引导中的对应流程。
func onboardingInstallDisabled(id string, node, npm, probing bool) bool {
	return onboardingNeedsNode(id) && (probing || !node || !npm)
}

// onboardingMagpieStatus 处理新手引导中的对应流程。
func onboardingMagpieStatus(st *SettingsState) (string, bool) {
	if st == nil {
		return "检查中", false
	}
	st.mu.Lock()
	endpointsLoading := st.endpoints.loading
	endpointsErr := st.endpoints.err
	piLoading := st.piMagpie.loading
	piErr := st.piMagpie.err
	st.mu.Unlock()
	if (!endpointsLoading && endpointsErr != "") || (!piLoading && piErr != "") {
		return "连接失败", false
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
	loading := st.providerRows.loading
	loadErr := st.providerRows.loadErr
	st.mu.Unlock()
	if loading {
		return providerRowStatus{Summary: "检查中"}
	}
	if loadErr != "" {
		return providerRowStatus{Summary: "检测失败"}
	}
	if !loaded {
		return providerRowStatus{Summary: "检查中"}
	}
	return providerStatusFor(st, id)
}

// onboardingStatusTone 按就绪状态返回药丸色调：就绪绿、未安装灰、失败红、其余（需要登录或检查中）琥珀。
func onboardingStatusTone(status providerRowStatus, missing bool) Tone {
	if status.Ready {
		return ToneSuccess
	}
	if status.Summary == "检测失败" || status.Summary == "连接失败" {
		return ToneDanger
	}
	if missing {
		return ToneNeutral
	}
	return ToneWarning
}

// onboardingProviderRow 处理新手引导中的对应流程。
func onboardingProviderRow(c *ui.Context, s *chatState, st *SettingsState, id string, index int) {
	status := onboardingProviderStatus(st, id)
	missing := status.Summary != "检查中" && status.Summary != "检测失败" && st != nil && providerRowNotInstalled(st, id)
	u := onboardingState(s)
	u.mu.Lock()
	node, npm, probing := u.nodeOK, u.npmOK, u.probing
	u.mu.Unlock()
	msg := ""
	if st != nil {
		st.mu.Lock()
		msg = st.providerRows.updateMsg[id]
		st.mu.Unlock()
	}
	row := ui.Column(c.Key("onboarding-provider-" + id)).FillWidth().Gap(Space1).Padding(Space3).Radius(RadiusMD).Background(ThemeSurfaceCard(c))
	onboardingEntry(c, row, index)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			renderProviderRowIcon(c, id, IconMD)
			ui.Text(c, providerDisplayName(id)).FontSize(uiFont(TypeBody)).Grow(1)
			onboardingBadge(c, "onboarding-status-"+id, status.Summary, onboardingStatusTone(status, missing))
			if !status.Ready {
				action, label := "settings", "打开设置"
				if missing {
					action, label = "install", "安装"
				} else if id == "agy" && status.Summary != "检查中" && status.Summary != "检测失败" {
					action, label = "login", "登录"
				}
				Button(c, ButtonOpts{Key: "onboarding-action-" + id, Text: label, Variant: ButtonSecondary, Size: ControlSM, Disabled: st == nil || (action == "install" && onboardingInstallDisabled(id, node, npm, probing)), OnClick: func() {
					if action == "settings" {
						onboardingSuspend(s, "providers", false)
					} else {
						launchProviderTerminalAction(st, id, action)
					}
				}})
			}
		})
		if msg != "" {
			ui.Text(c, msg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
		}
		if missing && onboardingNeedsNode(id) && !probing && (!node || !npm) {
			ui.Text(c, "需要先安装 Node.js：https://nodejs.org/").FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c))
		}
	})
}

// onboardingMagpieRow 渲染引导列表中的 Magpie 就绪状态行。
func onboardingMagpieRow(c *ui.Context, s *chatState, st *SettingsState, index int) {
	status, connected := onboardingMagpieStatus(st)
	row := ui.Column(c.Key("onboarding-magpie-row")).FillWidth().Gap(Space1).Padding(Space3).Radius(RadiusMD).Background(ThemeSurfaceCard(c))
	onboardingEntry(c, row, index)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconNetwork()).Size(IconMD, IconMD).TextColor(ThemeSecondaryText(c))
			ui.Text(c, "Magpie").FontSize(uiFont(TypeBody)).Grow(1)
			tone := onboardingStatusTone(providerRowStatus{Ready: connected, Summary: status}, status == "未连接")
			onboardingBadge(c, "onboarding-magpie-status", status, tone)
			if !connected {
				Button(c, ButtonOpts{
					Key:      "onboarding-action-magpie",
					Text:     "打开设置",
					Variant:  ButtonSecondary,
					Size:     ControlSM,
					Disabled: st == nil,
					OnClick:  func() { onboardingSuspend(s, "magpie", true) },
				})
			}
		})
	})
}

// onboardingReadyCount 统计 claude、codex、agy 与 Magpie 中已就绪的项数。
func onboardingReadyCount(st *SettingsState) int {
	count := 0
	for _, id := range []string{"claude", "codex", "agy"} {
		if onboardingProviderStatus(st, id).Ready {
			count++
		}
	}
	if _, connected := onboardingMagpieStatus(st); connected {
		count++
	}
	return count
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
	saveError := u.saveError
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
				ui.Row(c.Key("onboarding-header")).FillWidth().Padding(Space4, Space5, 0, Space5).AlignItems(ui.Center).Justify(ui.End).Children(func() {
					IconButton(c, IconButtonProps{Key: "onboarding-close", Icon: IconX(), Label: "关闭", Variant: ButtonGhost, OnClick: func() { closeOnboarding(s, true) }})
				})
				ui.Scroll(c.Key("onboarding-scroll")).Grow(1).Padding(Space4, Space5).Children(func() {
					ui.Column(c.Key("onboarding-body")).FillWidth().Gap(Space4).Children(func() {
						renderOnboardingHero(c)
						ui.Text(c, "欢迎使用 mcode").FontSize(uiFont(TypePageTitle)).TextColor(ThemePrimaryText(c))
						ui.Column(c.Key("onboarding-list")).FillWidth().Gap(Space2).Children(func() {
							onboardingProviderRow(c, s, st, "claude", 0)
							onboardingProviderRow(c, s, st, "codex", 1)
							onboardingProviderRow(c, s, st, "agy", 2)
							onboardingMagpieRow(c, s, st, 3)
						})
					})
				})
				if saveError != "" {
					ui.Text(c, saveError).Padding(0, Space5, Space2, Space5).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				}
				readyCount := onboardingReadyCount(st)
				ui.Row(c.Key("onboarding-footer")).FillWidth().Gap(Space2).Padding(Space3, Space5, Space5, Space5).AlignItems(ui.Center).Children(func() {
					onboardingButton(c, "onboarding-later", "稍后", false, func() { closeOnboarding(s, true) })
					ui.Box(c).Grow(1)
					ui.Text(c, fmt.Sprintf("%d/4 就绪", readyCount)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
					onboardingButton(c, "onboarding-start", "开始使用", true, func() { closeOnboarding(s, true) })
				})
			})
		})
	})
	if !open {
		closeOnboarding(s, true)
	}
}
