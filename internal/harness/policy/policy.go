// Package policy 统一渲染宿主注入给 agent 的规则提示词（harness policy）。
//
// 各段文案仍由原模块生成（swarm.LeadPolicy、swarm.TierPromptSection、
// swarm.LimitsPromptSection、kanban.CoordinatorPreface 等），本包只负责三件事：
// 输出版本标记、按固定顺序拼接、按能力开关丢弃未启用或内容为空的段。
// 这样 agent 收到的前缀始终带同一行版本号，排查时能直接对上规则版本。
package policy

import "strings"

// Version 是宿主规则渲染器的版本号；文案或拼装顺序变化时递增。
const Version = "2026-11-10.1"

// Marker 是渲染结果的首行版本标记，测试与排查都以它为准。
const Marker = `<mcode_harness_policy version="` + Version + `"/>`

// segmentSeparator 是段与段之间的分隔，沿用原拼装处的空行分隔。
const segmentSeparator = "\n\n"

// Capabilities 声明本次会话启用的宿主能力；关闭的能力对应的段不输出。
type Capabilities struct {
	// Swarm 表示编排（bee）对本会话生效。
	Swarm bool
	// Kanban 表示看板协调员规则可注入。
	Kanban bool
	// Context 表示项目上下文（project_context）规则可注入。
	Context bool
	// Automation 表示自动化相关规则可注入。
	Automation bool
}

// Sections 承载各段已渲染好的提示词文本；空串表示该段不输出。
// 字段命名对应实际注入点，新增段请同时更新 Render 里的顺序。
type Sections struct {
	// SwarmLead 是编排主代理策略（swarm.LeadPolicy）。
	SwarmLead string
	// SwarmTiers 是档位说明（swarm.TierPromptSection）。
	SwarmTiers string
	// SwarmLimits 是本次请求的编排上限说明（swarm.LimitsPromptSection）。
	SwarmLimits string
	// SwarmRoster 是在跑 bee 清单（swarm.RosterPromptSection），排在 SwarmLimits 之后。
	SwarmRoster string
	// Kanban 是看板协调员上下文（kanban.Service.CoordinatorPreface）。
	Kanban string
	// ProjectContext 是项目 Context 指导词（projctx.BuildGuidance* 产出的 <project_context> 块）。
	ProjectContext string
	// Automation 是自动化相关规则段。
	Automation string
}

// Render 按固定顺序拼接已启用的段，并在首行输出版本标记。
// 顺序为 SwarmLead、SwarmTiers、SwarmLimits、SwarmRoster、Kanban、ProjectContext、Automation。
// 所有段都为空或都被能力开关关闭时返回空串，不输出孤立的版本标记。
func Render(caps Capabilities, sections Sections) string {
	parts := make([]string, 0, 8)
	appendSection := func(enabled bool, text string) {
		if !enabled || text == "" {
			return
		}
		parts = append(parts, text)
	}
	appendSection(caps.Swarm, sections.SwarmLead)
	appendSection(caps.Swarm, sections.SwarmTiers)
	appendSection(caps.Swarm, sections.SwarmLimits)
	appendSection(caps.Swarm, sections.SwarmRoster)
	appendSection(caps.Kanban, sections.Kanban)
	appendSection(caps.Context, sections.ProjectContext)
	appendSection(caps.Automation, sections.Automation)
	if len(parts) == 0 {
		return ""
	}
	return Marker + segmentSeparator + strings.Join(parts, segmentSeparator)
}
