package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// swarmTierColor 按档位 ID 返回对应的视觉强调色。
// read 档位为蓝绿色 (teal 48, 176, 199)，edit 档位为系统强调色，deep 档位为紫色 (175, 82, 222)，
// chore 档位取人设色里的琥珀，与前三档都能区分；
// 其他自定义档位根据 ID 稳定哈希映射到调色板（橙、粉、靛蓝、绿）。
//
// read 与 deep 的色值在 theme.go 的 swarmTierTealColor / swarmTierPurpleColor，
// settings_swarm_tiers_test.go 逐个断言 (48, 176, 199) 与 (175, 82, 222)。
func swarmTierColor(c *ui.Context, tierID string) ui.Color {
	switch strings.ToLower(strings.TrimSpace(tierID)) {
	case "read":
		return swarmTierTealColor
	case "edit":
		return ThemeAccent(c)
	case "deep":
		return swarmTierPurpleColor
	case "chore":
		return ThemeIdentity("tide")
	default:
		var h uint32 = 2166136261
		id := strings.TrimSpace(tierID)
		for i := 0; i < len(id); i++ {
			h ^= uint32(id[i])
			h *= 16777619
		}
		switch h % 4 {
		case 0:
			return ThemeWarning(c) // 橙色
		case 1:
			return ThemeIdentity("grove") // 粉色
		case 2:
			return ThemeIdentity("zephyr") // 靛蓝色
		default:
			return ThemeSuccess(c) // 绿色
		}
	}
}

// renderSwarmTierChip 渲染统一的档位徽章（pill），背景为档位色 15% 透明度，文本 11pt 加粗；
// 只读档位在文字前追加 10px 闭合锁图标。
func renderSwarmTierChip(c *ui.Context, key string, name string, tierID string, readOnly bool) {
	renderSwarmTierChipColored(c, key, name, swarmTierColor(c, tierID), readOnly)
}

// renderSwarmTierChipColored 用调用方给定的强调色渲染档位徽章：外观与 renderSwarmTierChip 完全一致，
// 只是颜色由调用方直接提供（例如汇报胶囊已解析出档位颜色，无需再按 ID 查一次）。
func renderSwarmTierChipColored(c *ui.Context, key string, name string, col ui.Color, readOnly bool) {
	ui.Row(c.Key(key)).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		Background(col.Alpha(0.15)).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			if readOnly {
				ui.Icon(c, IconLock()).
					Size(10, 10).
					TextColor(col)
			}
			ui.Text(c, name).
				FontSize(uiFont(TypeCaption)).
				FontWeight(600).
				TextColor(col).
				SingleLine()
		})
}

// swarmModelCapsuleText 生成档位或模型选择胶囊的简短展示文案。
// 模型为空时显示“跟随会话 Bee 模型”；指定模型后追加推理强度（如 · 高）与快速模式标记 (⚡)。
func swarmModelCapsuleText(providers []ProviderInfo, provider, model, effort string, fast bool) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "跟随会话子代理模型"
	}
	mInfo := findSwarmModelInfo(providers, provider, model)
	dispName := mInfo.Name
	if dispName == "" {
		dispName = model
	}
	effort = strings.TrimSpace(effort)
	if effort != "" {
		dispName = dispName + " · " + effortLabel(effort)
	}
	if fast {
		dispName = dispName + " ⚡"
	}
	return dispName
}
