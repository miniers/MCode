package mcodeui

import "mcode/internal/swarm"

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
