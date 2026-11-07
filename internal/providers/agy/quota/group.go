package quota

import "strings"

const (
	GroupGemini    = "Gemini Models"
	GroupClaudeGPT = "Claude and GPT models"
)

// ModelGroup 按模型名归类到 agy 的配额分组。
func ModelGroup(model string) string {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "claude") || strings.Contains(lower, "gpt") {
		return GroupClaudeGPT
	}
	return GroupGemini
}
