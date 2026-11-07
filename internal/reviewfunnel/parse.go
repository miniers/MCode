package reviewfunnel

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// maxConcerns 是单条结论保留的疑点上限。
	maxConcerns = 10
	// maxConcernChars 是单条疑点的字符上限。
	maxConcernChars = 300
	// maxReasonChars 是结论理由的字符上限。
	maxReasonChars = 500
)

// pathLineRefPattern 匹配 path:line 形式的源码引用，例如 internal/x.go:12。
var pathLineRefPattern = regexp.MustCompile(`[A-Za-z0-9_.\-/]+\.[A-Za-z0-9_]+:[0-9]+`)

// reviewEntry 是模型输出中的单条审查结论，字段与 JSON 契约一一对应。
type reviewEntry struct {
	Index    *int     `json:"index"`
	Name     string   `json:"name"`
	Verdict  string   `json:"verdict"`
	Reason   string   `json:"reason"`
	Concerns []string `json:"concerns"`
}

// ParseBatch 解析批量审查的模型输出。
//
// text 可以是纯 JSON、json 代码围栏，或前后带解释文字的 JSON；解析器会扫描
// 最外层对象并取第一个包含 reviews 数组的对象。ids 是本次请求的 index 列表。
//
// 返回值：合法结论按 index 索引；缺失或单条非法的 index 放进 missing；
// 只有整段文本找不到可用 JSON 对象时才返回 error，单条损坏不影响同批其他条目。
func ParseBatch(text string, ids []int) (map[int]Decision, []int, error) {
	entries, found := decodeBatchEntries(text)
	if !found {
		return nil, nil, errors.New("reviewfunnel: 没有找到包含 reviews 数组的 JSON 对象")
	}

	wanted := make(map[int]struct{}, len(ids))
	order := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, dup := wanted[id]; dup {
			continue
		}
		wanted[id] = struct{}{}
		order = append(order, id)
	}

	counts := make(map[int]int, len(entries))
	for _, entry := range entries {
		if entry.Index != nil {
			counts[*entry.Index]++
		}
	}
	badNames := conflictingNameIndexes(entries)

	result := make(map[int]Decision, len(order))
	for _, entry := range entries {
		if entry.Index == nil {
			continue
		}
		idx := *entry.Index
		if _, ok := wanted[idx]; !ok {
			continue
		}
		if counts[idx] > 1 {
			continue
		}
		if _, bad := badNames[idx]; bad {
			continue
		}
		decision, ok := entry.decision()
		if !ok {
			continue
		}
		result[idx] = decision
	}

	missing := make([]int, 0, len(order))
	for _, id := range order {
		if _, ok := result[id]; !ok {
			missing = append(missing, id)
		}
	}
	return result, missing, nil
}

// ParseDecision 从单只 bee 的模型输出中解析一条结论，用于批量解析失败后的回退。
//
// 接受两种形状：单条 {"verdict":...} 对象，或 reviews 数组里只有一条的批量对象。
// 校验规则与 ParseBatch 相同；找不到合法结论时返回 false。
func ParseDecision(text string) (Decision, bool) {
	var decision Decision
	found := false

	forEachJSONObject(text, func(obj map[string]json.RawMessage) bool {
		if raw, ok := obj["reviews"]; ok {
			var entries []reviewEntry
			if err := json.Unmarshal(raw, &entries); err == nil && len(entries) == 1 {
				if d, ok := entries[0].decision(); ok {
					decision = d
					found = true
					return true
				}
			}
		}
		entry := entryFromObject(obj)
		if strings.TrimSpace(entry.Verdict) == "" {
			return false
		}
		if d, ok := entry.decision(); ok {
			decision = d
			found = true
			return true
		}
		return false
	})

	return decision, found
}

// EstimateTokens 按字节数除以 4 向上取整估算 token 数，用于展示和调参。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// decision 校验单个条目并转成 Decision；不合法时返回 false。
func (e reviewEntry) decision() (Decision, bool) {
	verdict := strings.ToLower(strings.TrimSpace(e.Verdict))
	switch verdict {
	case VerdictApprove, VerdictReject, VerdictEscalate:
	default:
		return Decision{}, false
	}

	reason := normalizeLine(e.Reason, maxReasonChars)
	concerns := normalizeConcerns(e.Concerns)

	if verdict == VerdictReject {
		if reason == "" {
			return Decision{}, false
		}
		hasReference := false
		for _, concern := range concerns {
			if pathLineRefPattern.MatchString(concern) {
				hasReference = true
				break
			}
		}
		if !hasReference {
			return Decision{}, false
		}
	}

	return Decision{
		Verdict:  verdict,
		Reason:   reason,
		Concerns: concerns,
	}, true
}

// decodeBatchEntries 找到并解码包含 reviews 数组的 JSON 对象。
func decodeBatchEntries(text string) ([]reviewEntry, bool) {
	var entries []reviewEntry
	found := false
	forEachJSONObject(text, func(obj map[string]json.RawMessage) bool {
		raw, ok := obj["reviews"]
		if !ok {
			return false
		}
		var list []reviewEntry
		if err := json.Unmarshal(raw, &list); err != nil {
			return false
		}
		entries = list
		found = true
		return true
	})
	return entries, found
}

// forEachJSONObject 按出现顺序扫描文本中能解码的 JSON 对象；fn 返回 true 时停止。
// 从每个 { 开始解码可以容忍代码围栏和前后解释文字，并自然取最外层对象。
func forEachJSONObject(text string, fn func(map[string]json.RawMessage) bool) {
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[i:]))
		var obj map[string]json.RawMessage
		if err := decoder.Decode(&obj); err != nil {
			continue
		}
		if fn(obj) {
			return
		}
	}
}

// entryFromObject 从已解码的 JSON 对象读取单条结论字段。
func entryFromObject(obj map[string]json.RawMessage) reviewEntry {
	var entry reviewEntry
	if raw, ok := obj["index"]; ok {
		var index int
		if err := json.Unmarshal(raw, &index); err == nil {
			entry.Index = &index
		}
	}
	if raw, ok := obj["name"]; ok {
		_ = json.Unmarshal(raw, &entry.Name)
	}
	if raw, ok := obj["verdict"]; ok {
		_ = json.Unmarshal(raw, &entry.Verdict)
	}
	if raw, ok := obj["reason"]; ok {
		_ = json.Unmarshal(raw, &entry.Reason)
	}
	if raw, ok := obj["concerns"]; ok {
		_ = json.Unmarshal(raw, &entry.Concerns)
	}
	return entry
}

// conflictingNameIndexes 找出被多个不同 index 复用的 name，这些 index 都按缺失处理。
// ParseBatch 只拿到 ids、拿不到期望的 name，因此这里只能校验批内 name 与 index
// 是否一一对应；与请求侧名字的比对由调用方完成。
func conflictingNameIndexes(entries []reviewEntry) map[int]struct{} {
	nameToIndex := make(map[string]int)
	conflicts := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Index == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if prev, ok := nameToIndex[name]; ok && prev != *entry.Index {
			conflicts[name] = struct{}{}
			continue
		}
		nameToIndex[name] = *entry.Index
	}
	if len(conflicts) == 0 {
		return nil
	}
	bad := make(map[int]struct{}, len(conflicts))
	for _, entry := range entries {
		if entry.Index == nil {
			continue
		}
		if _, ok := conflicts[strings.TrimSpace(entry.Name)]; ok {
			bad[*entry.Index] = struct{}{}
		}
	}
	return bad
}

// normalizeLine 把理由压成单行并截断到 max 个字符。
func normalizeLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(s, max)
}

// normalizeConcerns 清洗疑点：去空、去重、限长，最多保留 maxConcerns 条。
func normalizeConcerns(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, concern := range in {
		concern = strings.TrimSpace(concern)
		if concern == "" {
			continue
		}
		concern = truncateRunes(concern, maxConcernChars)
		if _, dup := seen[concern]; dup {
			continue
		}
		seen[concern] = struct{}{}
		out = append(out, concern)
		if len(out) >= maxConcerns {
			break
		}
	}
	return out
}

// truncateRunes 按字符（rune）截断字符串，避免切断多字节字符。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
