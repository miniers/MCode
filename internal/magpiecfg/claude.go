package magpiecfg

import (
	"encoding/json"
	"errors"
	"strings"
)

// ClaudeAgent 是 claude 在状态目录和计划里的 agent 名。
const ClaudeAgent = "claude"

// claudePickerNote 是 modelPicker 每行 description 的固定文案。它同时是
// 「这个 picker 是 magpie/mcode 写的」判据：每一行都以它结尾才认领。
//
// 移植自 magpie internal/agent/claude.go:1036 的 o.Note 与
// internal/agent/claude.go:3044 的 magpiesPicker（MIT License）。
const claudePickerNote = "via magpie"

// claudeCustomOptionEnv 是用户自己在 /model 里加的那一行；有它时不能设
// replaceBuiltInOptions，否则内置行连同他那行一起被藏掉。
//
// 移植自 magpie internal/agent/claude.go:3054 的 claudeCustomOptionEnv
// （MIT License）。
const claudeCustomOptionEnv = "ANTHROPIC_CUSTOM_MODEL_OPTION"

// ClaudeOptions 是构造 Claude 全局配置计划的输入（移植说明第 1 节的输入
// 契约）：端点地址与 key、用户按勾选顺序排好的模型，以及网关 /v1/models
// 的全量 ID。Served 只用来判断点号/横线拼写，为空时不做改写。
type ClaudeOptions struct {
	BaseURL    string
	APIKey     string
	Selections []ClaudeSelection // 按勾选顺序，空 ID 会被丢掉
	Served     []string          // /v1/models 的全量 ID
	// Main 是用户手动指定的主模型；空或不在 Selections 里时用 Selections[0]。
	Main string
}

// ClaudePlan 构造把 Claude Code 全局配置指向 magpie 网关的计划：主模型、
// 四个档位、子代理模型、过时的档位说明键，以及 /model 用的 modelPicker。
// 写法照 magpie 的 connect 流程（internal/agent/claude.go:1750 起的 kvs 与
// internal/agent/claude.go:976 的 writePicker，MIT License）；档位与拼写规则
// 见 claudename.go。
//
// baseURL 是不带 /v1 的根地址（调用方已去掉）；apiKey 会以明文写进
// settings.json。地址与 key 都不能为空，勾选至少一个模型。
func ClaudePlan(opts ClaudeOptions) (Plan, error) {
	baseURL := strings.TrimSpace(opts.BaseURL)
	apiKey := strings.TrimSpace(opts.APIKey)
	selections := claudeCleanSelections(opts.Selections)
	switch {
	case baseURL == "":
		return Plan{}, errors.New("magpiecfg: 网关地址为空")
	case apiKey == "":
		return Plan{}, errors.New("magpiecfg: API Key 为空")
	case len(selections) == 0:
		return Plan{}, errors.New("magpiecfg: 模型为空")
	}
	path, err := ClaudeSettingsPath()
	if err != nil {
		return Plan{}, err
	}

	served := claudeServedSet(opts.Served)
	windows := make(map[string]int, len(selections))
	for _, sel := range selections {
		windows[sel.ID] = sel.ContextWindow
	}
	// 与 magpie 一样先按模型窗口加 [1m]，再用 served 校验点号/横线拼写
	// （internal/agent/claude.go:1049 与 internal/agent/claude.go:1775，
	// magpie）。
	spell := func(id string) string { return ClaudeSpelled(MarkOneM(id, windows[id]), served) }

	main := selections[0].ID
	if want := strings.TrimSpace(opts.Main); want != "" && claudeHasSelection(selections, want) {
		main = want
	}
	tiers := ClaudeTierModels(main, selections)

	fields := []Field{
		{Path: "env.ANTHROPIC_BASE_URL", Value: baseURL},
		{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: apiKey},
	}
	// ANTHROPIC_SMALL_FAST_MODEL 与 haiku 档同一个值，magpie 单独再写一次
	// （internal/agent/claude.go:1774，magpie）。
	if haiku := spell(tiers["haiku"]); haiku != "" {
		fields = append(fields, Field{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: haiku})
	} else {
		fields = append(fields, Field{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Delete: true})
	}
	// model 写在档位之前，与 magpie 的 kvs 顺序一致
	// （internal/agent/claude.go:1775，magpie）。
	fields = append(fields, Field{Path: "model", Value: spell(main)})
	for _, tier := range ClaudeTiers {
		key := "env." + TierEnv(tier)
		if tiers[tier] == "" {
			// 档位没有模型（非 Fable 主模型的 fable 档）时删键，让 Claude Code
			// 用回自己的默认值（internal/agent/claude.go:1781，magpie）。
			fields = append(fields, Field{Path: key, Delete: true})
			continue
		}
		fields = append(fields, Field{Path: key, Value: spell(tiers[tier])})
	}
	// ANTHROPIC_MODEL 会压过 settings.json 的 model 与 /model 里的选择
	// （internal/agent/claude.go:1756，magpie）。
	fields = append(fields, Field{Path: "env.ANTHROPIC_MODEL", Delete: true})

	// 子代理：只删指向网关模型的旧值，让子代理跟随会话主模型；用户自己钉的
	// 别的模型不产生字段，原值保持不动。magpie 的判定见
	// internal/agent/claude.go:1446 的 subagentOwn（MIT License）。
	if sub, ok := GetJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); ok && strings.TrimSpace(sub) != "" && ClaudeServed(sub, served) {
		fields = append(fields, Field{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Delete: true})
	}

	// 12 个档位说明键说的是上一任模型，magpie 从不写、只在档位换模型时删
	// （internal/agent/claude.go:48 与 internal/agent/claude.go:708，magpie）。
	// 这里存在即删，让 Connect 的 stash 把原值记下来供断开时归还。
	for _, key := range ClaudeAboutEnv() {
		if _, ok := GetJSONRaw(path, "env."+key); ok {
			fields = append(fields, Field{Path: "env." + key, Delete: true})
		}
	}

	fields = append(fields, claudePickerField(path, selections, spell))

	noProxy, _ := GetJSON(path, "env.NO_PROXY")
	fields = append(fields, Field{Path: "env.NO_PROXY", Value: mergeNoProxy(noProxy)})

	return Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: fields,
	}}}, nil
}

// claudePickerField 构造 /model 用的 modelPicker 字段。
//
// 移植自 magpie internal/agent/claude.go:986 的 writePicker（MIT License），
// 与 magpie 的差异：options 按用户勾选顺序而不是目录顺序；label 只用
// ClaudeSelection.Label；description 固定 via magpie。归属按移植说明第 4 节
// 的两层判定：文件里已有的 picker 每一行都以 via magpie 结尾才接管，否则
// 置 KeepForeign，Connect/Reapply 不会碰它。
func claudePickerField(path string, selections []ClaudeSelection, spell func(string) string) Field {
	rows := make([]any, 0, len(selections))
	for _, sel := range selections {
		rows = append(rows, map[string]any{
			"model":       spell(sel.ID),
			"label":       claudePickerLabel(sel),
			"description": claudePickerNote,
		})
	}
	picker := map[string]any{"options": rows}
	// replace 只看用户自定义选项：mcode 的目录恒为「收窄」状态，所以照
	// magpie 的 replace := claudeNarrowed() && !ownOption()
	// （internal/agent/claude.go:1013，magpie）只由 ownOption 决定。
	if own, _ := GetJSON(path, "env."+claudeCustomOptionEnv); strings.TrimSpace(own) == "" {
		picker["replaceBuiltInOptions"] = true
	}
	keepForeign := false
	if raw, ok := GetJSONRaw(path, "modelPicker"); ok {
		keepForeign = !claudePickerOurs(raw)
	}
	return Field{Path: "modelPicker", Value: picker, KeepForeign: keepForeign}
}

// claudePickerOurs 判断一个 modelPicker 值是不是 magpie/mcode 写的：至少一行，
// 且每一行都有 model、description 以 via magpie 结尾。
//
// 移植自 magpie internal/agent/claude.go:3044 的 magpiesPicker
// （MIT License）。
func claudePickerOurs(raw string) bool {
	var picker struct {
		Options []struct {
			Model       string `json:"model"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if json.Unmarshal([]byte(raw), &picker) != nil || len(picker.Options) == 0 {
		return false
	}
	for _, o := range picker.Options {
		if o.Model == "" || !strings.HasSuffix(o.Description, claudePickerNote) {
			return false
		}
	}
	return true
}

// claudePickerLabel 是 picker 一行的 label：有显示名用它，否则把 ID 的前缀
// （auto/ 之类的路由组）去掉。
func claudePickerLabel(sel ClaudeSelection) string {
	if label := strings.TrimSpace(sel.Label); label != "" {
		return label
	}
	if i := strings.LastIndex(sel.ID, "/"); i >= 0 {
		return sel.ID[i+1:]
	}
	return sel.ID
}

// claudeHasSelection 报告清洗后的勾选里是否有该模型 ID。
func claudeHasSelection(selections []ClaudeSelection, id string) bool {
	for _, sel := range selections {
		if sel.ID == id {
			return true
		}
	}
	return false
}

// claudeCleanSelections 去空、去空白，保留勾选顺序。
func claudeCleanSelections(in []ClaudeSelection) []ClaudeSelection {
	out := make([]ClaudeSelection, 0, len(in))
	for _, sel := range in {
		id := strings.TrimSpace(sel.ID)
		if id == "" {
			continue
		}
		out = append(out, ClaudeSelection{
			ID:            id,
			Label:         strings.TrimSpace(sel.Label),
			ContextWindow: sel.ContextWindow,
		})
	}
	return out
}

// claudeServedSet 把网关的全量模型 ID 变成 ClaudeSpelled 用的集合。
func claudeServedSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// mergeNoProxy 把 localhost、127.0.0.1、::1 合并进逗号分隔的代理忽略名单：
// 保留原值，已有的项（不区分大小写）不重复追加。与 internal/providers/pi
// 的 mergeNoProxy 语义一致。
func mergeNoProxy(val string) string {
	required := []string{"localhost", "127.0.0.1", "::1"}
	if strings.TrimSpace(val) == "" {
		return strings.Join(required, ",")
	}
	seen := make(map[string]bool)
	for _, p := range strings.Split(val, ",") {
		if trimmed := strings.ToLower(strings.TrimSpace(p)); trimmed != "" {
			seen[trimmed] = true
		}
	}
	res := val
	for _, req := range required {
		if seen[strings.ToLower(req)] {
			continue
		}
		if res != "" && !strings.HasSuffix(res, ",") {
			res += ","
		}
		res += req
		seen[strings.ToLower(req)] = true
	}
	return res
}
