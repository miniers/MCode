package mcodeui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// modelBrandKey 按模型判断胶囊使用的品牌键：模型名/ID 命中已知品牌时返回品牌键，
// 否则退回 Provider 的图标键，让品牌图标与配色跟随模型本身而不是接入方。
func modelBrandKey(providerID, modelID, modelName string) string {
	haystack := strings.ToLower(modelName + " " + modelID)
	switch {
	case strings.Contains(haystack, "claude"),
		strings.Contains(haystack, "opus"),
		strings.Contains(haystack, "sonnet"),
		strings.Contains(haystack, "haiku"),
		strings.Contains(haystack, "fable"):
		return "claude"
	case strings.Contains(haystack, "deepseek"):
		return "deepseek"
	case strings.Contains(haystack, "gemini"):
		return "gemini"
	case strings.Contains(haystack, "grok"):
		return "grok"
	case strings.Contains(haystack, "glm"):
		return "zai"
	case strings.Contains(haystack, "llama"),
		strings.HasPrefix(haystack, "meta/"),
		strings.HasPrefix(haystack, "meta-"):
		return "meta"
	case strings.Contains(haystack, "gpt"), strings.Contains(haystack, "codex"):
		return "codex"
	default:
		return providerIconKey(providerID)
	}
}

// modelBrandForModel 用模型选择组件的显示名推导品牌键，供调用方少写一次全名计算。
func modelBrandForModel(providers []ProviderInfo, providerID, modelID string) string {
	return modelBrandKey(providerID, modelID, modelPickerDisplayName(providers, providerID, modelID))
}

// modelBrandPrefixes 是短名需要去掉的品牌前缀（带一个空格），比较时不区分大小写。
var modelBrandPrefixes = map[string]string{
	"claude":   "Claude ",
	"deepseek": "DeepSeek ",
	"meta":     "Meta ",
}

// kebabModelAcronyms 是 kebab 模型 ID 里需要整体大写的缩写段（如 gpt、glm、qwq）。
var kebabModelAcronyms = map[string]bool{
	"gpt": true,
	"glm": true,
	"qwq": true,
}

// prettifyKebabModelID 把 kebab 形式的模型 ID 排版成可读短名：claude-opus-5-5 → Claude Opus 5.5，
// gpt-5.6-luna → GPT-5.6 Luna。只处理不含空格且带连字符的输入，其余原样返回。规则：
// 丢弃 8 位纯数字日期段；连续的短数字段（每段不超过 2 位）用 . 拼成版本号；
// 缩写段全大写，v 紧跟数字的段首字母大写成 V4.1，数字开头的段保持原样，其余段首字母大写；
// 段间用空格连接，但缩写段后紧跟数字开头的段时改用 - 连接。
func prettifyKebabModelID(s string) string {
	if !strings.Contains(s, "-") || strings.ContainsAny(s, " \t\n") {
		return s
	}
	raw := strings.Split(s, "-")
	merged := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		part := raw[i]
		if part == "" {
			continue
		}
		if allDigits(part) {
			if len(part) == 8 {
				continue
			}
			if len(part) <= 2 {
				run := []string{part}
				for i+1 < len(raw) && allDigits(raw[i+1]) && len(raw[i+1]) <= 2 {
					i++
					run = append(run, raw[i])
				}
				merged = append(merged, strings.Join(run, "."))
				continue
			}
		}
		merged = append(merged, part)
	}
	if len(merged) == 0 {
		return s
	}
	segments := make([]string, len(merged))
	for i, part := range merged {
		segments[i] = prettifyKebabSegment(part)
	}
	var b strings.Builder
	for i, seg := range segments {
		if i > 0 {
			if kebabModelAcronyms[strings.ToLower(merged[i-1])] && seg[0] >= '0' && seg[0] <= '9' {
				b.WriteByte('-')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(seg)
	}
	return b.String()
}

// prettifyKebabSegment 排版 kebab ID 的单个段：缩写段全大写，v4.1 这类版本段大写首字母，
// 数字开头的段保持原样，其余段只把首字母大写。
func prettifyKebabSegment(part string) string {
	if part == "" {
		return part
	}
	if kebabModelAcronyms[strings.ToLower(part)] {
		return strings.ToUpper(part)
	}
	if part[0] == 'v' && len(part) > 1 && part[1] >= '0' && part[1] <= '9' {
		return "V" + part[1:]
	}
	if part[0] >= '0' && part[0] <= '9' {
		return part
	}
	runes := []rune(part)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// modelShortName 把模型显示名压成胶囊用的短名：先去掉「 · 」之后的附加说明（如 magpie 的
// routing group、端点名），再取 ID 末段、去日期与 (auto)/(latest) 后缀，
// 也去掉上下文窗口后缀（如 (1M context)、(200K context window)、[1m]，但 (Thinking) 这类括号保留），
// 再去掉品牌前缀（仅当剩余部分以字母开头，避免「Claude 3.7 Sonnet」变成「3.7 Sonnet」）。
func modelShortName(name, brand string) string {
	name = strings.TrimSpace(name)
	if head, _, ok := strings.Cut(name, " · "); ok {
		if trimmed := strings.TrimSpace(head); trimmed != "" {
			name = trimmed
		}
	}
	if !strings.Contains(name, " ") && strings.Contains(name, "/") {
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
	}
	name = trimModelDateSuffix(name)
	for {
		trimmed := strings.TrimSpace(name)
		lower := strings.ToLower(trimmed)
		next := ""
		switch {
		case strings.HasSuffix(lower, "(auto)"):
			next = strings.TrimSpace(trimmed[:len(trimmed)-len("(auto)")])
		case strings.HasSuffix(lower, "(latest)"):
			next = strings.TrimSpace(trimmed[:len(trimmed)-len("(latest)")])
		case strings.HasSuffix(trimmed, ")"):
			next, _ = modelContextWindowParenTail(trimmed)
		case strings.HasSuffix(trimmed, "]"):
			next, _ = modelContextWindowBracketTail(trimmed)
		}
		if next == "" {
			name = trimmed
			break
		}
		name = next
	}
	name = prettifyKebabModelID(name)
	if _, ok := colorProviderIcons[brand]; !ok {
		return name
	}
	prefix, ok := modelBrandPrefixes[brand]
	if !ok || len(name) <= len(prefix) {
		return name
	}
	if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
		return name
	}
	rest := name[len(prefix):]
	if r := []rune(rest); len(r) > 0 && unicode.IsLetter(r[0]) {
		return rest
	}
	return name
}

// modelContextWindowParenTail 去掉名字结尾的上下文窗口括号，如 (1M context)、(200K Context Window)、
// (1m context) (auto) 中已先去掉 (auto) 后的情形。括号内文字必须去掉 context/context window 后
// 剩下的部分以数字开头才算上下文标记，因此 (Thinking)、(context) 之类一律保留。
func modelContextWindowParenTail(name string) (string, bool) {
	if !strings.HasSuffix(name, ")") {
		return "", false
	}
	open := strings.LastIndex(name, "(")
	if open < 0 {
		return "", false
	}
	inner := strings.ToLower(strings.TrimSpace(name[open+1 : len(name)-1]))
	value := ""
	switch {
	case strings.HasSuffix(inner, "context window"):
		value = strings.TrimSpace(inner[:len(inner)-len("context window")])
	case strings.HasSuffix(inner, "context"):
		value = strings.TrimSpace(inner[:len(inner)-len("context")])
	default:
		return "", false
	}
	if value == "" || value[0] < '0' || value[0] > '9' {
		return "", false
	}
	return strings.TrimSpace(name[:open]), true
}

// modelContextWindowBracketTail 去掉名字结尾的方括号上下文标记，如 claude-opus-5-5[1m] 的 [1m]、
// model[200k] 的 [200k]；方括号内必须只有数字加 k 或 m（不区分大小写），其他方括号保留。
func modelContextWindowBracketTail(name string) (string, bool) {
	if !strings.HasSuffix(name, "]") {
		return "", false
	}
	open := strings.LastIndex(name, "[")
	if open < 0 {
		return "", false
	}
	inner := strings.ToLower(strings.TrimSpace(name[open+1 : len(name)-1]))
	if len(inner) < 2 {
		return "", false
	}
	unit := inner[len(inner)-1]
	if unit != 'k' && unit != 'm' {
		return "", false
	}
	for i := 0; i < len(inner)-1; i++ {
		if inner[i] < '0' || inner[i] > '9' {
			return "", false
		}
	}
	return strings.TrimSpace(name[:open]), true
}

// modelShortDisplayName 返回胶囊使用的模型短名：先取完整显示名，再按品牌压短。
func modelShortDisplayName(providers []ProviderInfo, providerID, modelID string) string {
	full := modelPickerDisplayName(providers, providerID, modelID)
	return modelShortName(full, modelBrandKey(providerID, modelID, full))
}

// modelCapsuleFullNameTip 把胶囊短名省略的信息补回用途说明：胶囊只显示短名时，
// 把模型全名和推理强度接在用途说明后面，供悬停提示使用。base 为空时不加开头换行。
func modelCapsuleFullNameTip(base string, providers []ProviderInfo, sel ModelSelection) string {
	full := modelPickerDisplayName(providers, sel.Provider, sel.Model)
	info := modelPickerModelInfo(providers, sel.Provider, sel.Model)
	tip := effortSignalTip(info.Efforts, sel.Effort, info.DefaultEffort)
	line := "当前模型：" + full
	if tip != "" {
		line += " · " + tip
	}
	if strings.TrimSpace(base) == "" {
		return line
	}
	return base + "\n" + line
}

// renderModelBrandIcon 按品牌绘制模型图标：品牌有彩色资源时用品牌图标，否则退回 Provider 图标。
func renderModelBrandIcon(c *ui.Context, providerID, brand string, size float32) {
	if _, ok := colorProviderIcons[brand]; ok {
		renderProviderIcon(c, brand, size)
		return
	}
	renderProviderIcon(c, providerID, size)
}

// renderModelPairIcons 并排绘制主代理与子代理的品牌图标（各一个完整图标）：
// 子代理未配置模型、或两侧 provider 与 model 完全相同时只画一个。
func renderModelPairIcons(c *ui.Context, key string, providers []ProviderInfo, leadProvider, leadModel, beesProvider, beesModel string, size float32) {
	beesProvider = strings.TrimSpace(beesProvider)
	beesModel = strings.TrimSpace(beesModel)
	row := ui.Row(c.Key(key)).Gap(Space0).AlignItems(ui.Center)
	row.Children(func() {
		renderModelBrandIcon(c, leadProvider, modelBrandForModel(providers, leadProvider, leadModel), size)
		if beesModel == "" {
			return
		}
		if providerIconKey(beesProvider) == providerIconKey(leadProvider) && beesModel == leadModel {
			return
		}
		renderModelBrandIcon(c, beesProvider, modelBrandForModel(providers, beesProvider, beesModel), size)
	})
}

// effortSignalHeights 返回 n 格信号棒的高度：从 4 线性升到 12，最后一格最高。
func effortSignalHeights(n int) []float32 {
	if n < 2 {
		return nil
	}
	heights := make([]float32, n)
	for i := 0; i < n; i++ {
		heights[i] = 4 + 8*float32(i)/float32(n-1)
	}
	return heights
}

// effortSignalFilled 返回信号格已点亮的格数：当前档位索引加一，未设置时按默认档回退。
func effortSignalFilled(efforts []string, current, def string) int {
	if len(efforts) == 0 {
		return 0
	}
	return effortIndex(efforts, current, def) + 1
}

// effortSignalSampleColor 在等距色标 colors 上按 i/(n-1) 线性插值取色；只有一个色时直接返回。
func effortSignalSampleColor(colors []ui.Color, i, n int) ui.Color {
	if len(colors) == 0 {
		return ui.Transparent
	}
	if len(colors) == 1 || n < 2 {
		return colors[0]
	}
	t := float32(i) / float32(n-1)
	if t <= 0 {
		return colors[0]
	}
	if t >= 1 {
		return colors[len(colors)-1]
	}
	pos := t * float32(len(colors)-1)
	idx := min(int(pos), len(colors)-2)
	return colors[idx].Mix(colors[idx+1], pos-float32(idx))
}

// effortSignalThemeColor 把信号格颜色适配到当前主题：深色原样返回，
// 浅色下近白色改用主文字色、其余颜色压暗，保证在浅色底上可辨。
func effortSignalThemeColor(c *ui.Context, col ui.Color) ui.Color {
	if isDark(c) {
		return col
	}
	if col.R >= 200 && col.G >= 200 && col.B >= 200 {
		return ThemePrimaryText(c)
	}
	return col.Mix(ThemePrimaryText(c), 0.25)
}

// effortSignalTip 返回信号格的悬停提示：当前档位与最高档，档位不足两档时为空串。
func effortSignalTip(efforts []string, current, def string) string {
	if len(efforts) < 2 {
		return ""
	}
	cur := effortLabel(efforts[effortIndex(efforts, current, def)])
	top := effortLabel(efforts[len(efforts)-1])
	return "强度：" + cur + "（最高：" + top + "）"
}

// effortSignalDuration 是信号格亮灭进度的动画时长，与逐格换色的过渡配合成从左到右依次变化。
const effortSignalDuration = 220 * time.Millisecond

// renderEffortSignal 渲染胶囊里的推理强度信号格：从低到高排列的细竖条，
// 已选到的档位实心（颜色取自 effortTitleGradient），未选到的空心。
func renderEffortSignal(c *ui.Context, key string, props EffortSliderCardProps) {
	n := len(props.Efforts)
	if n < 2 {
		return
	}
	filled := effortSignalFilled(props.Efforts, props.Current, props.DefaultEffort)
	heights := effortSignalHeights(n)
	colors := effortTitleGradient(props)

	row := ui.Row(c.Key(key)).Height(IconSM).Gap(Space0).AlignItems(ui.End)
	progress := row.AnimateWith(key+"/progress", float32(filled), animateDuration(c, effortSignalDuration), ui.EaseOut)
	row.Children(func() {
		for i := 0; i < n; i++ {
			bar := ui.Box(c.Key(fmt.Sprintf("%s/bar-%d", key, i))).
				Width(3).
				Height(heights[i]).
				Radius(RadiusXS).
				Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
			if progress >= float32(i)+0.5 {
				col := effortSignalThemeColor(c, effortSignalSampleColor(colors, i, n))
				bar.Background(col).Border(1, col)
			} else {
				bar.Border(1, ThemeSecondaryText(c).Alpha(0.4))
			}
		}
	})
}
