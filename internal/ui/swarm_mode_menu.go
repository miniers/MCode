package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/swarm"
)

// swarmModeOptionHeight 是菜单里每行模式选项的最小高度：比常规行高出一截，四档一眼扫完。
const swarmModeOptionHeight = float32(38)

// swarmModeOption 描述会话级 Swarm 工作模式菜单中的一项。
// 模式取值与内部 swarm.Mode* 常量一致。
type swarmModeOption struct {
	Mode   string
	Label  string // 菜单主文案
	Detail string // 菜单说明
	Short  string // 标题栏胶囊上的短文案
}

// swarmModeOptions 是四种会话级工作模式。
var swarmModeOptions = []swarmModeOption{
	{Mode: swarm.ModeInherited, Label: "跟随全局设置", Detail: "按「设置 › 编排」里的全局开关决定", Short: "跟随"},
	{Mode: swarm.ModeCollaborative, Label: "协同模式", Detail: "主导规划并委派大任务；小改动可亲自执行", Short: "协同"},
	{Mode: swarm.ModeLeadPlansOnly, Label: "主代理只规划不动手", Detail: "主代理只规划并委派，零代码改动", Short: "只规划"},
	{Mode: swarm.ModeAlwaysBees, Label: "消息直达子代理", Detail: "绕过主代理模型；每条消息直接派发给子代理执行", Short: "直达"},
}

// swarmModeOptionFor 返回指定模式对应的菜单项，未知模式按「跟随全局设置」处理。
func swarmModeOptionFor(mode string) swarmModeOption {
	normalized := swarm.NormalizeMode(mode)
	for _, opt := range swarmModeOptions {
		if opt.Mode == normalized {
			return opt
		}
	}
	return swarmModeOptions[0]
}

// swarmModeMenuState 维护会话级工作模式菜单的开合状态。
// open/threadID 供标题胶囊入口使用。
// 所有字段只在 UI 线程读写，swarmModeMenus 由 swarmModeMenuMu 保护。
type swarmModeMenuState struct {
	open     bool
	threadID string
}

var (
	swarmModeMenuMu sync.Mutex
	swarmModeMenus  = make(map[*chatState]*swarmModeMenuState)
)

// getSwarmModeMenuState 返回绑定在 chatState 上的工作模式菜单状态。
func getSwarmModeMenuState(s *chatState) *swarmModeMenuState {
	swarmModeMenuMu.Lock()
	defer swarmModeMenuMu.Unlock()
	st, ok := swarmModeMenus[s]
	if !ok {
		st = &swarmModeMenuState{}
		swarmModeMenus[s] = st
	}
	return st
}

// threadSwarmMode 返回指定会话保存的 Swarm 工作模式，空值视同 inherited。
func (s *chatState) threadSwarmMode(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return swarm.NormalizeMode(th.SwarmMode)
		}
	}
	return swarm.ModeInherited
}

// renderSwarmModeMenu 在会话标题左侧渲染 Swarm 工作模式胶囊与菜单。
// 仅在该会话开启 Swarm 且它自己不是 bee 时出现。
func renderSwarmModeMenu(c *ui.Context, s *chatState, th Thread) {
	if s == nil || th.ID == "" {
		return
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	enabled := hp.state.Enabled
	isBee := hp.state.IsBee
	swarmPanelMu.Unlock()
	if !enabled || isBee {
		return
	}

	ms := getSwarmModeMenuState(s)
	if ms.threadID != th.ID {
		ms.threadID = th.ID
		ms.open = false
	}

	current := swarmModeOptionFor(s.threadSwarmMode(th.ID))
	trigger := Button(c.Key("chat-header-swarm-mode-btn"), ButtonOpts{
		Icon:  IconNetwork(),
		Text:  current.Short,
		Label: "编排工作模式：" + current.Label,
		OnClick: func() {
			ms.open = !ms.open
			s.invalidate()
		},
		Variant: ButtonSecondary,
	})

	appPopover(c.Key("chat-header-swarm-mode-popover"), trigger, &ms.open, func() {
		ms.open = false
	}, func() {
		renderSwarmModeMenuContent(c, s, th.ID, &ms.open)
	})
}

// renderSwarmModeMenuContent 渲染工作模式菜单内容：四个模式，当前项打勾。
// open 指向所属弹出层的开合状态，选中后置为 false 关闭菜单。
func renderSwarmModeMenuContent(c *ui.Context, s *chatState, threadID string, open *bool) {
	current := swarmModeOptionFor(s.threadSwarmMode(threadID))
	popoverCard(c, ui.Column(c.Key("chat-header-swarm-mode-menu")).
		Gap(Space0).
		Padding(Space2).
		MinWidth(240)).
		Children(func() {
			ui.Text(c, "工作模式").
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space2, Space3, Space1, Space3)
			for _, opt := range swarmModeOptions {
				o := opt
				isCurrent := o.Mode == current.Mode
				optBtn := ui.ButtonBase(c.Key("chat-header-swarm-mode-option-"+o.Mode)).
					Label(o.Label).
					Height(swarmModeOptionHeight).
					Padding(Space1, Space3).
					Radius(RadiusSM).
					FillWidth()
				if optBtn.Hovered() {
					optBtn.Background(ThemeHover(c))
				}
				optBtn.OnClick(func() {
					*open = false
					s.invalidate()
					if isCurrent {
						return
					}
					s.setThreadSwarmMode(threadID, o.Mode)
				})
				optBtn.Children(func() {
					ui.Row(c.Key("chat-header-swarm-mode-row-" + o.Mode)).
						Gap(Space3).
						AlignItems(ui.Center).
						Grow(1).
						Children(func() {
							if isCurrent {
								ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
							} else {
								ui.Box(c.Key("chat-header-swarm-mode-check-"+o.Mode)).Size(14, 14)
							}
							ui.Column(c.Key("chat-header-swarm-mode-text-" + o.Mode)).
								Gap(Space0).
								Grow(1).
								AlignItems(ui.Start).
								Children(func() {
									ui.Text(c, o.Label).
										FontSize(TypeBody).
										TextColor(ThemePrimaryText(c)).
										SingleLine()
									ui.Text(c, o.Detail).
										FontSize(TypeCaption).
										TextColor(ThemeSecondaryText(c)).
										SingleLine()
								})
						})
				})
			}
		})
}

// setThreadSwarmMode 保存指定会话的 Swarm 工作模式，成功后同步本地会话列表。
func (s *chatState) setThreadSwarmMode(threadID, mode string) {
	if threadID == "" || mode == "" {
		return
	}
	go func() {
		if err := s.client.SetThreadSwarmMode(threadID, mode); err != nil {
			s.setToast("设置编排工作模式失败: " + err.Error())
			return
		}
		s.mu.Lock()
		for i := range s.threads {
			if s.threads[i].ID == threadID {
				s.threads[i].SwarmMode = mode
				break
			}
		}
		for _, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == threadID {
					list[i].SwarmMode = mode
					break
				}
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}
