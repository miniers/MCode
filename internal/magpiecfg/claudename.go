package magpiecfg

import (
	"regexp"
	"slices"
	"strings"
)

// 本文件是 magpie（MIT License）里 Claude 模型名与档位规则的纯函数移植。
// 只依赖标准库，不读写文件，也不依赖本机安装的 magpie 程序；输入是网关侧
// 模型 ID 和本包内的小结构体，输出拼写后的模型名、档位映射与供写配置用的
// env 键名。

// claudeOneMMark 是 Claude Code 给百万上下文模型加的标记后缀。
const claudeOneMMark = "[1m]"

// claudeMemberEfforts 是模型可以被钉住的推理档位，最低的在前。
//
// 移植自 magpie internal/provider/groupeffort.go:41 的 MemberEfforts
// （MIT License）。
var claudeMemberEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// claudeAboutSuffixes 是 Claude Code 在 env 里跟着档位模型写的说明键后缀：
// 显示名、描述与模型能力。它们说的是旁边那个模型，档位换模型后就过时了，
// 所以只删不写。
//
// 移植自 magpie internal/agent/claude.go:48 的 claudeAbout（MIT License）。
var claudeAboutSuffixes = []string{"_NAME", "_DESCRIPTION", "_SUPPORTED_CAPABILITIES"}

// ClaudeSelection 是用户在网关端点上勾选的一个模型。
//
// 一期只用到 ID、显示名与上下文长度：ID 用于推导档位和拼写，Label 用作
// modelPicker 的 label，ContextWindow 用来判断是否加 [1m] 后缀（0 表示未知）。
type ClaudeSelection struct {
	ID            string // 网关侧模型 ID，原样
	Label         string // magpie_label，picker 的 label
	ContextWindow int    // 上下文窗口；0 表示未知
}

// ClaudeTiers 是 Claude Code 会解析的档位别名（/model opus、子代理的
// "model: haiku" 等），每档可以有自己指向的模型；没有自己模型的档位跟随
// 主模型。
//
// 移植自 magpie internal/agent/claude.go:166 的 claudeTiers（MIT License）。
var ClaudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

// claudeDotted 匹配 Claude 模型里带点号的版本号（claude-opus-4.6）。
//
// 移植自 magpie internal/provider/claudedash.go:22 的 claudeDotted
// （MIT License）。
var claudeDotted = regexp.MustCompile(`(?i)claude-(?:opus|sonnet|haiku|fable|mythos)-\d+(?:\.\d+)+`)

// ClaudeDashed 把 Claude 模型版本号里的每个点改成横线：
// relay/claude-opus-4.6-us[1m] 变成 relay/claude-opus-4-6-us[1m]。
// 其它厂商的 ID、provider 前缀、:effort 与 [1m] 都原样保留。
//
// 移植自 magpie internal/provider/claudedash.go:30 的 ClaudeDashed
// （MIT License）。Claude Code 只按横线形式认识版本号，点号形式会被它读成
// 更老的版本。
func ClaudeDashed(id string) string {
	if !strings.Contains(id, ".") {
		return id
	}
	return claudeDotted.ReplaceAllStringFunc(id, func(s string) string { return strings.ReplaceAll(s, ".", "-") })
}

// claudeSplit 把交给 agent 的模型引用拆成 ID 和跟在后面的部分：钉住的推理
// 档位（":high"）与 Claude Code 的 [1m] 标记。
//
// 移植自 magpie internal/provider/claudedash.go:40 的 claudeSplit
// （MIT License）。
func claudeSplit(ref string) (id, rest string) {
	id, marked := strings.CutSuffix(ref, claudeOneMMark)
	if i := strings.LastIndex(id, ":"); i > 0 && slices.Contains(claudeMemberEfforts, strings.ToLower(id[i+1:])) {
		id, rest = id[:i], id[i:]
	}
	if marked {
		rest += claudeOneMMark
	}
	return id, rest
}

// undashIn 返回横线拼写 d 对应的那个网关模型 ID：d 本身是服务中的 ID 时
// 不算（原样拼写永远优先）；两个 ID 拼写成同一个 d 时也不算。
//
// 移植自 magpie internal/provider/claudedash.go:67 的 undashIn
// （MIT License），入参从 magpie 的 Entry 列表换成本包的 ID 集合。
func undashIn(served map[string]bool, d string) (string, bool) {
	if served[d] {
		return "", false // 这个拼写本身就是网关服务的 ID
	}
	found := ""
	for e, ok := range served {
		if !ok || ClaudeDashed(e) != d {
			continue
		}
		if found != "" {
			return "", false // 两个 ID 拼写一样，都不知道指的是哪个
		}
		found = e
	}
	return found, found != ""
}

// ClaudeSpelled 是交给 Claude Code 的模型写法：ref（服务中的 ID，可带
// :effort 与 [1m]）的横线拼写只有在 served 能唯一还原回 ref 自己、且横线
// 拼写本身没人服务时才用，否则原样返回。served 是网关服务中的全部 ID；
// 为空时一律原样返回。
//
// 移植自 magpie internal/provider/claudedash.go:87 的 ClaudeSpelled
// （MIT License），入参从 magpie 的 Served() 换成本包的 ID 集合。
func ClaudeSpelled(ref string, served map[string]bool) string {
	id, rest := claudeSplit(ref)
	d := ClaudeDashed(id)
	if d == id {
		return ref
	}
	if real, ok := undashIn(served, d); ok && real == id {
		return d + rest
	}
	return ref
}

// MarkOneM 给上下文窗口至少 1,000,000 的模型加 [1m] 标记；已经有标记或 ID
// 为空时原样返回。Claude Code 会把没标记的 Claude 模型当成 200K 窗口，
// 反复提前压缩上下文。
//
// 移植自 magpie internal/agent/claude.go:2665 的 claude1MFor（MIT License），
// 窗口长度由调用方按模型元数据传入，不再从 magpie 目录里查。
func MarkOneM(id string, contextWindow int) string {
	if id == "" || strings.HasSuffix(id, claudeOneMMark) {
		return id
	}
	if contextWindow >= 1_000_000 {
		return id + claudeOneMMark
	}
	return id
}

// ClaudeTierModels 算出 opus、sonnet、haiku、fable 四档各自指向的模型 ID
// （未做点号拼写与 [1m] 标记，交给调用方逐个处理）。
//
// 移植自 magpie internal/agent/claude.go:478 的 follow 与
// internal/agent/claude.go:508 的 claudeLight（MIT License）。magpie 从整份
// 模型目录里找同前缀的 Haiku，这里只在 selections 里找，永远不指向用户没
// 勾选的模型。opus 与 sonnet 档也在 selections 里找同系列模型，这是对
// follow 的有意偏离，理由见 claudeTierSibling。
func ClaudeTierModels(main string, selections []ClaudeSelection) map[string]string {
	tiers := make(map[string]string, len(ClaudeTiers))
	for _, tier := range ClaudeTiers {
		tiers[tier] = claudeFollow(tier, main, selections)
	}
	return tiers
}

// claudeFollow 是一档的结果：勾选里有同系列的档位模型时指向它，否则跟随
// 主模型 main。fable 档例外，非 Fable 主模型时留空。
//
// 移植自 magpie internal/agent/claude.go:478 的 follow（MIT License），
// opus 与 sonnet 档的取法见 claudeTierSibling。
func claudeFollow(tier, main string, selections []ClaudeSelection) string {
	switch tier {
	case "opus", "sonnet":
		if m := claudeTierSibling(tier, main, selections); m != "" {
			return m
		}
	case "haiku":
		if l := claudeLight(main, selections); l != "" {
			return l
		}
	case "fable":
		// 主模型不是 Fable 时留空：在非 Fable 主模型上设 Fable 会要 credits，
		// 别的厂商模型甚至跑不起来，所以该档的 env 键会被删掉。
		if !ClaudeIsFableName(main) {
			return ""
		}
	}
	return main
}

// claudeLight 是 selections 里与 main 同前缀的最新 Haiku：provider 前缀、
// 路由组（group/）与路由器上的厂商路径（vendor/claude-）都保留。main 不是
// Claude 模型、自己已经是 Haiku，或者勾选里没有同前缀 Haiku 时返回空。
// 同一模型的带日期 ID 与不带日期 ID 之间取不带日期的。
//
// 移植自 magpie internal/agent/claude.go:508 的 claudeLight（MIT License），
// 数据来源从整份目录换成本次勾选的模型。
func claudeLight(main string, selections []ClaudeSelection) string {
	ref := strings.TrimSuffix(main, claudeOneMMark)
	i := claudeFamilyIndex(ref)
	if i < 0 || strings.Contains(ref[i:], "haiku") {
		return ""
	}
	return claudePickTier(claudeTierPrefix(ref, i, "haiku"), selections)
}

// claudeFamilyIndex 返回 ref 里 Claude 模型名的起点（"/claude-" 或
// "-claude-" 的位置）：provider 前缀、路由组（group/）与路由器上的厂商路径
// （vendor/claude-）都留在它前面。ref 不是 Claude 模型时返回 -1。
func claudeFamilyIndex(ref string) int {
	i := strings.Index(ref, "/claude-")
	if j := strings.Index(ref, "-claude-"); i < 0 || j >= 0 && j < i {
		i = j
	}
	return i
}

// claudeTierPrefix 是 ref 从 i 起的同系列 tier 档模型前缀：
// auto/claude-opus-5-5 与 haiku 得到 auto/claude-haiku-。
func claudeTierPrefix(ref string, i int, tier string) string {
	return ref[:i+1] + "claude-" + tier + "-"
}

// claudeUndated 去掉 ID 末尾 -YYYYMMDD 形式的日期。
func claudeUndated(id string) string {
	if k := strings.LastIndex(id, "-"); k >= 0 && len(id)-k == 9 && strings.Trim(id[k+1:], "0123456789") == "" {
		return id[:k]
	}
	return id
}

// claudePickTier 在 selections 里挑前缀为 prefix 的模型：同一模型的带日期
// ID 与不带日期 ID 之间取不带日期的，其余取拼写最新的。
func claudePickTier(prefix string, selections []ClaudeSelection) string {
	best := ""
	for _, sel := range selections {
		if !strings.HasPrefix(sel.ID, prefix) {
			continue
		}
		if u, b := claudeUndated(sel.ID), claudeUndated(best); best == "" || u > b || u == b && len(sel.ID) < len(best) {
			best = sel.ID
		}
	}
	return best
}

// claudeTierSibling 是 selections 里与 main 同前缀的 tier 档模型。main 不是
// Claude 模型、自己就是该档，或者勾选里没有同前缀的该档模型时返回空，由
// 调用方退回 main。
//
// 这是对 magpie follow 的有意偏离：magpie 的 opus、sonnet 档永远跟随主模型
// （internal/agent/claude.go:478，magpie）。用户明确勾选了同系列模型时，档位
// 应指向它，而不是跟随主模型。
func claudeTierSibling(tier, main string, selections []ClaudeSelection) string {
	ref := strings.TrimSuffix(main, claudeOneMMark)
	i := claudeFamilyIndex(ref)
	if i < 0 {
		return ""
	}
	prefix := claudeTierPrefix(ref, i, tier)
	if strings.HasPrefix(ref, prefix) {
		return "" // 主模型自己就是该档
	}
	return claudePickTier(prefix, selections)
}

// TierEnv 是档位模型在 settings.json env 里的键名：
// TierEnv("opus") = "ANTHROPIC_DEFAULT_OPUS_MODEL"。
//
// 移植自 magpie internal/agent/claude.go:536 的 tierEnv（MIT License）。
func TierEnv(tier string) string {
	return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL"
}

// ClaudeAboutEnv 返回四个档位共 12 个说明键：
// ANTHROPIC_DEFAULT_<TIER>_MODEL_NAME、_DESCRIPTION、_SUPPORTED_CAPABILITIES。
//
// 移植自 magpie internal/agent/claude.go:51 的 claudeAboutEnv（MIT License）。
func ClaudeAboutEnv() []string {
	out := make([]string, 0, len(ClaudeTiers)*len(claudeAboutSuffixes))
	for _, tier := range ClaudeTiers {
		for _, suffix := range claudeAboutSuffixes {
			out = append(out, TierEnv(tier)+suffix)
		}
	}
	return out
}

// ClaudeIsFableName 说 main 是不是 claude-fable-<版本> 形式的 Fable 模型，
// 点号版本按横线形式判断。只做 claudeName 的 Fable 前缀判定，不实现完整的
// 模型名归一化。
//
// 移植自 magpie internal/agent/claude.go:185 的 claudeName（MIT License）。
func ClaudeIsFableName(main string) bool {
	name := strings.ToLower(ClaudeDashed(strings.TrimSuffix(main, claudeOneMMark)))
	const prefix = "claude-fable-"
	i := strings.Index(name, prefix)
	if i < 0 {
		return false
	}
	rest := name[i+len(prefix):]
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// ClaudeServed 说 ref（可带 :effort 与 [1m]）去掉后缀后是不是网关服务的
// 模型 ID。用来判断配置里指向网关的旧值（如
// CLAUDE_CODE_SUBAGENT_MODEL）该不该删。
//
// 移植自 magpie internal/agent/claude.go:551 的 claudeMagpieModel
// （MIT License）；magpie 还会把横线拼写映射回目录里的点号 ID，mcode 一期
// 只写不读，所以只按给出的 ID 集合查。
func ClaudeServed(ref string, served map[string]bool) bool {
	id, _ := claudeSplit(ref)
	return served[id]
}
