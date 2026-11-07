package swarm

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tier 描述编排的一个执行档位：主会话在派发时选择档位，档位映射到具体模型。
type Tier struct {
	ID           string
	Name         string
	Description  string
	ReadOnly     bool
	WritePaths   []string
	Provider     string
	Model        string
	Effort       string
	FastMode     bool
	SystemPrompt string
}

// TierOverride 描述针对特定档位的覆盖配置。
type TierOverride struct {
	Provider string
	Model    string
	Effort   string
	FastMode *bool
}

// FindTier 按引用查找档位：ref 先去除首尾空白，为空时返回 false；
// 先按 ID 做大小写不敏感匹配，再按 Name 做大小写不敏感匹配。
func FindTier(tiers []Tier, ref string) (Tier, bool) {
	want := strings.TrimSpace(ref)
	if want == "" {
		return Tier{}, false
	}
	for _, t := range tiers {
		if strings.EqualFold(strings.TrimSpace(t.ID), want) {
			return t, true
		}
	}
	for _, t := range tiers {
		if strings.EqualFold(strings.TrimSpace(t.Name), want) {
			return t, true
		}
	}
	return Tier{}, false
}

// MatchWritePaths 判断仓库相对路径 p（斜杠分隔）是否命中写入白名单 patterns。
// patterns 为空表示不限制，直接返回 true；任一 pattern 命中即返回 true。
// pattern 按 / 分段：整段为 ** 时匹配零个或多个路径段，其他段用 path.Match 匹配单段。
func MatchWritePaths(patterns []string, p string) bool {
	if len(patterns) == 0 {
		return true
	}
	segs := splitPathSegments(p)
	for _, pattern := range patterns {
		if matchPathSegments(splitPathSegments(pattern), segs) {
			return true
		}
	}
	return false
}

// splitPathSegments 把斜杠分隔的路径拆成段，忽略首尾与连续的多余斜杠。
func splitPathSegments(p string) []string {
	trimmed := strings.Trim(strings.TrimSpace(p), "/")
	if trimmed == "" {
		return nil
	}
	segs := strings.Split(trimmed, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// matchPathSegments 逐段匹配 pattern 与路径段：** 段匹配零个或多个路径段，
// 其他段用 path.Match 做单段匹配（不含 / 的通配符）。
func matchPathSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchPathSegments(pat, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func isBoundaryRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '.' && r != '_' && r != '/'
}

// MentionsInMessage 判断消息中是否以合法边界提及了 ref。
// 二者均转小写，ref 去除首尾空白后长度小于 3 则返回 false。
// 当 ref 在 message 中出现且两侧均为边界时返回 true；
// 边界定义为字符串首尾或非字母、数字、'-'、'.'、'_'、'/' 的 rune。
func MentionsInMessage(message, ref string) bool {
	msg := strings.ToLower(message)
	r := strings.ToLower(strings.TrimSpace(ref))
	if utf8.RuneCountInString(r) < 3 {
		return false
	}

	msgRunes := []rune(msg)
	refRunes := []rune(r)
	n := len(msgRunes)
	m := len(refRunes)
	if m > n {
		return false
	}

	for i := 0; i <= n-m; i++ {
		match := true
		for j := 0; j < m; j++ {
			if msgRunes[i+j] != refRunes[j] {
				match = false
				break
			}
		}
		if match {
			leftOK := (i == 0) || isBoundaryRune(msgRunes[i-1])
			rightOK := (i+m == n) || isBoundaryRune(msgRunes[i+m])
			if leftOK && rightOK {
				return true
			}
		}
	}
	return false
}

// AuthorizeDirectModel 检查用户当前消息是否授权直属指定模型。
// model 必须非空；provider 非空且 providerEnabled != nil 时若 !providerEnabled(provider) 则返回 false；
// 当 MentionsInMessage(message, model) 为真，或 model 包含 '/' 且 MentionsInMessage(message, 最后一个 '/' 之后的部分) 为真时授权。
func AuthorizeDirectModel(message, provider, model string, providerEnabled func(string) bool) bool {
	m := strings.TrimSpace(model)
	if m == "" {
		return false
	}
	prov := strings.TrimSpace(provider)
	if prov != "" && providerEnabled != nil && !providerEnabled(prov) {
		return false
	}
	if MentionsInMessage(message, m) {
		return true
	}
	if idx := strings.LastIndex(m, "/"); idx != -1 {
		after := m[idx+1:]
		if MentionsInMessage(message, after) {
			return true
		}
	}
	return false
}
