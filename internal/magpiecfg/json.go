// 本文件移植自 magpie 的 internal/edit/json.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。
//
// 与 magpie 的差异：去掉文件内容缓存（每次都读盘），未移植顶层数组编辑与
// PatchJSON。

package magpiecfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

// KV 是一条点分键路径（例如 "model.name"）和它的新值。
type KV struct {
	Path  string
	Value any
}

// GetJSON 从 JSON 或 JSONC 文件读取一条点分键路径。字符串结果不带引号；
// 需要按 JSON 语义比较、或值可能是对象/数组时用 GetJSONRaw。
func GetJSON(path, keyPath string) (string, bool) {
	raw, err := Read(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	r := gjson.GetBytes(raw, keyPath)
	if !r.Exists() {
		return "", false
	}
	return r.String(), true
}

// GetJSONRaw 从 JSON 或 JSONC 文件读取一条点分键路径，返回规范化后的原始
// JSON 文本：字符串带引号，对象键按字典序排列、空白最少，所以同一个对象的
// 不同缩进或键序会得到同一段文本。键不存在、或文件不是有效 JSONC 时返回
// ("", false)。
func GetJSONRaw(path, keyPath string) (string, bool) {
	raw, err := Read(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return jsonRawIn(raw, keyPath)
}

// jsonRawIn 在已读入的 JSONC 文本里取 keyPath 的原始 JSON 文本并规范化。
func jsonRawIn(raw []byte, keyPath string) (string, bool) {
	// 注释在同长度的副本里被替换成空格，gjson 的偏移因此仍对应原始文本；
	// 这里只取值，不需要偏移。
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return "", false
	}
	r := gjson.GetBytes(stripped, keyPath)
	if !r.Exists() {
		return "", false
	}
	return canonicalRawJSON(r.Raw)
}

// canonicalJSON 把值编码成稳定的 JSON 文本：对象键按字典序排列、没有多余
// 空白，字符串保留引号。json.RawMessage 会先校验再压缩。
func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("magpiecfg: 无法编码 JSON 值: %w", err)
	}
	return string(b), nil
}

// canonicalRawJSON 把一段 JSON 文本重新编码成稳定形式。数字用
// json.Number 承载，避免大整数或小数精度被改写。不是完整的单个 JSON 值时
// 返回 false。
func canonicalRawJSON(text string) (string, bool) {
	if !json.Valid([]byte(text)) {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// canonicalDocumentText 把整份 JSON/JSONC 文档规范成稳定文本（键排序、无
// 多余空白），用来比较两份文件的“内容”是否相同而忽略排版。
func canonicalDocumentText(raw []byte) (string, bool) {
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return "", false
	}
	return canonicalRawJSON(string(stripped))
}

// canonicalFieldText 把状态文件里记录的文本规范成 JSON 值文本。旧版本的
// fieldState.Value 存的是 GetJSON 的结果，字符串不带引号；这类文本按裸
// 字符串补上引号，旧状态文件因此仍能和新格式的值比较。
func canonicalFieldText(text string) (string, bool) {
	if canon, ok := canonicalRawJSON(text); ok {
		return canon, true
	}
	b, err := json.Marshal(text)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// sameJSONValue 按 JSON 语义比较两个字段状态：先比键是否存在，再比规范化
// 后的文本。缩进、键序不同但内容相同的对象相等。
func sameJSONValue(a, b fieldState) bool {
	if a.Had != b.Had {
		return false
	}
	if !a.Had {
		return true
	}
	if a.Value == b.Value {
		return true
	}
	ca, aok := canonicalFieldText(a.Value)
	cb, bok := canonicalFieldText(b.Value)
	if !aok || !bok {
		return false
	}
	return ca == cb
}

// SetJSON 设置一条或多条点分键路径，保留注释、键顺序和缩进。文件和父目录
// 不存在时创建。
func SetJSON(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	orig := raw
	raw = dropLoneCommas(raw)
	for _, kv := range kvs {
		raw, err = setJSONBytes(raw, kv.Path, kv.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	out, err := checkJSONC(keepCRLF(raw, orig), dropLoneCommas(orig))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return WriteAtomic(path, out)
}

// keepCRLF 把一次编辑的结果 out 按原文件 orig 换行风格改写：orig 的每个
// 换行都是 \r\n 时，编辑新增的行也换成 \r\n；原本混用换行风格的文件保持
// 原样。
func keepCRLF(out, orig []byte) []byte {
	n := bytes.Count(orig, []byte("\n"))
	if n == 0 || bytes.Count(orig, []byte("\r\n")) != n {
		return out
	}
	out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
}

func setJSONBytes(raw []byte, keyPath string, value any) ([]byte, error) {
	// 注释在同长度的副本里被替换成空格，因此 gjson 的偏移可以 1:1 映射回
	// 原始字节。
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, fmt.Errorf("invalid JSON/JSONC")
	}
	root := gjson.ParseBytes(stripped)
	if !root.IsObject() {
		return nil, fmt.Errorf("top level is not a JSON object")
	}
	parts := splitPath(keyPath)
	for i := len(parts); i >= 1; i-- {
		r := gjson.GetBytes(stripped, strings.Join(parts[:i], "."))
		if !r.Exists() {
			continue
		}
		if i == len(parts) || !r.IsObject() {
			// 精确的键已存在（替换值），或者祖先存在但是标量（用需要的
			// 嵌套对象替换它）。
			return splice(raw, r.Index, len(r.Raw), marshalAt(raw, r.Index, nest(parts[i:], value)))
		}
		return insertKey(raw, stripped, r.Index, unescapeKey(parts[i]), nest(parts[i+1:], value))
	}
	return insertKey(raw, stripped, 0, unescapeKey(parts[0]), nest(parts[1:], value))
}

func nest(parts []string, value any) any {
	for i := len(parts) - 1; i >= 0; i-- {
		value = map[string]any{unescapeKey(parts[i]): value}
	}
	return value
}

// splitPath 按 gjson 的规则在点处分隔键路径：用反斜杠转义的点属于键本身
// （VS Code 的 settings.json 里 "chat.defaultModel" 就是一个键）。各段保留
// 转义，供 gjson 使用。
func splitPath(keyPath string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(keyPath); i++ {
		switch keyPath[i] {
		case '\\':
			i++
		case '.':
			parts = append(parts, keyPath[start:i])
			start = i + 1
		}
	}
	return append(parts, keyPath[start:])
}

// unescapeKey 是键路径的一段所命名的键。
func unescapeKey(part string) string { return strings.ReplaceAll(part, `\.`, ".") }

func splice(raw []byte, at, n int, with []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw)-n+len(with))
	out = append(out, raw[:at]...)
	out = append(out, with...)
	out = append(out, raw[at+n:]...)
	return out, nil
}

// insertKey 把 key: value 加进 from 处或其后开始的对象的第一个成员。
func insertKey(raw, stripped []byte, from int, key string, value any) ([]byte, error) {
	open := from
	for open < len(raw) && stripped[open] != '{' {
		open++
	}
	close := matchBrace(stripped, open)
	if open >= len(raw) || close < 0 {
		return nil, fmt.Errorf("cannot locate object for %q", key)
	}
	k, _ := json.Marshal(key)
	pretty := bytes.IndexByte(raw, '\n') >= 0
	empty := len(bytes.TrimSpace(raw[open+1:close])) == 0
	// 只有注释的对象里没有成员让新成员排在它前面：接一个逗号就成了对象
	// 的尾随逗号，而只有部分程序读得懂尾随逗号。
	members := len(bytes.TrimSpace(stripped[open+1:close])) > 0

	if !pretty {
		v, _ := json.Marshal(value)
		ins := string(k) + ":" + string(v)
		if members {
			ins += ","
		}
		return splice(raw, open+1, 0, []byte(ins))
	}

	base := lineIndent(raw, open)
	unit := indentUnit(raw, open, base)
	child := base + unit
	v, _ := json.MarshalIndent(value, child, unit)
	kv := child + string(k) + ": " + string(v)
	if empty {
		return splice(raw, open, close-open+1, []byte("{\n"+kv+"\n"+base+"}"))
	}
	if !members {
		return splice(raw, open+1, 0, []byte("\n"+kv))
	}
	return splice(raw, open+1, 0, []byte("\n"+kv+","))
}

// marshalAt 按 pos 所在行的缩进渲染 value。
func marshalAt(raw []byte, pos int, value any) []byte {
	if bytes.IndexByte(raw, '\n') < 0 {
		b, _ := json.Marshal(value)
		return b
	}
	base := lineIndent(raw, pos)
	b, _ := json.MarshalIndent(value, base, indentUnit(raw, pos, base))
	return b
}

func lineIndent(raw []byte, pos int) string {
	start := bytes.LastIndexByte(raw[:pos], '\n') + 1
	i := start
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return string(raw[start:i])
}

// indentUnit 从 pos 之后那一行猜缩进步长，猜不出时用两个空格（文件用制表
// 符时就用制表符）。
func indentUnit(raw []byte, pos int, base string) string {
	if nl := bytes.IndexByte(raw[pos:], '\n'); nl >= 0 {
		next := lineIndent(raw, pos+nl+1)
		if len(next) > len(base) && strings.HasPrefix(next, base) {
			return next[len(base):]
		}
	}
	if strings.Contains(base, "\t") || bytes.Contains(raw, []byte("\n\t")) {
		return "\t"
	}
	return "  "
}

// matchBrace 返回与 open 处的 '{' 配对的 '}' 的下标，跳过字符串字面量。
// 它要求输入里没有注释。
func matchBrace(b []byte, open int) int {
	depth := 0
	inStr := false
	for i := open; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// DelJSON 从 JSON/JSONC 文件删除键路径，其余内容保持原样。键不存在时忽略；
// 删空的对象收缩成 {}。
func DelJSON(path string, keyPaths ...string) error {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	orig := raw
	raw = dropLoneCommas(raw)
	changed := false
	for _, kp := range keyPaths {
		out, ok, err := delJSONBytes(raw, kp)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if ok {
			raw, changed = out, true
		}
	}
	if !changed {
		return nil
	}
	out, err := checkJSONC(raw, dropLoneCommas(orig))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return WriteAtomic(path, out)
}

// pruneEmptyJSON 删掉 keyPaths 里已经变空的对象：它们是写嵌套键时由我们
// 顺手创建的空壳（例如 providers.json 里的 "providers"），恢复后不该留在
// 用户文件里。只删现在确实为空的对象，用户往里写了东西就保持原样。从后往
// 前处理：调用方按从浅到深给出祖先，先删深的那层，浅的才可能接着变空。
func pruneEmptyJSON(path string, keyPaths []string) error {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	orig := raw
	raw = dropLoneCommas(raw)
	changed := false
	for i := len(keyPaths) - 1; i >= 0; i-- {
		kp := keyPaths[i]
		stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
		r := gjson.GetBytes(stripped, kp)
		if !r.Exists() || !r.IsObject() || len(r.Map()) != 0 {
			continue
		}
		out, ok, err := delJSONBytes(raw, kp)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if ok {
			raw, changed = out, true
		}
	}
	if !changed {
		return nil
	}
	out, err := checkJSONC(raw, dropLoneCommas(orig))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return WriteAtomic(path, out)
}

// pastLineEnd 返回 raw 在 i 处的行尾之后的位置（\n 或 \r\n），没有行尾时
// 返回 i：连同所在行一起删除的成员要带走整个行尾，CRLF 文件才不会留下一
// 行空白。
func pastLineEnd(raw []byte, i int) int {
	switch {
	case bytes.HasPrefix(raw[i:], []byte("\r\n")):
		return i + 2
	case bytes.HasPrefix(raw[i:], []byte("\n")):
		return i + 1
	}
	return i
}

// objectOpenAt 返回 s 中从 i 起第一个 '{' 的位置：gjson 的 Index 指向值的
// 起点，值前面可能还有空白。
func objectOpenAt(s []byte, i int) int {
	for i < len(s) && s[i] != '{' {
		i++
	}
	return i
}

// parentObjectOpen 返回包含 keyPath 最后一层键的对象的 '{' 位置：顶层键的
// 父对象就是整份文档的对象。找不到时返回 -1。
func parentObjectOpen(stripped []byte, keyPath string) int {
	r := gjson.ParseBytes(stripped)
	if parts := splitPath(keyPath); len(parts) > 1 {
		r = gjson.GetBytes(stripped, strings.Join(parts[:len(parts)-1], "."))
	}
	if !r.Exists() || !r.IsObject() {
		return -1
	}
	if open := objectOpenAt(stripped, r.Index); open < len(stripped) {
		return open
	}
	return -1
}

func delJSONBytes(raw []byte, keyPath string) ([]byte, bool, error) {
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, false, fmt.Errorf("invalid JSON/JSONC")
	}
	r := gjson.GetBytes(stripped, keyPath)
	if !r.Exists() || r.Index == 0 {
		return raw, false, nil
	}
	// 值的位置 → 越过 ':' 和空白回到键字符串
	a := r.Index
	for a > 0 && (stripped[a-1] == ' ' || stripped[a-1] == '\t' || stripped[a-1] == '\n' || stripped[a-1] == '\r' || stripped[a-1] == ':') {
		a--
	}
	if a == 0 || stripped[a-1] != '"' {
		return nil, false, fmt.Errorf("cannot locate key %q", keyPath)
	}
	a-- // 键的收尾引号
	for a > 0 && stripped[a-1] != '"' {
		a--
	}
	a-- // 键的开头引号
	b := r.Index + len(r.Raw)

	ws := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	// 成员自身的跨度：独占一行时就是整行
	from, to := a, b
	ls := bytes.LastIndexByte(raw[:a], '\n') + 1
	alone := len(bytes.TrimSpace(raw[ls:a])) == 0
	// 成员是父对象的第一个成员时（它前面只有空白），插入时加在 '{' 后面的
	// 换行和缩进也算这次插入的一部分：内联对象（"providers": {"other":
	// ...}）插一个新成员再删掉，才能逐字节还原成原来的样子。成员前面有
	// 注释时不算第一个，免得把注释一起带走。
	first := -1
	if open := parentObjectOpen(stripped, keyPath); open >= 0 && len(bytes.TrimSpace(raw[open+1:a])) == 0 {
		first = open
	}
	j := b
	for j < len(stripped) && ws(stripped[j]) {
		j++
	}
	var out []byte
	if j < len(stripped) && stripped[j] == ',' {
		// 不是最后一个成员：连同后面的逗号一起删
		to = j + 1
		switch {
		case first >= 0:
			from = first + 1
		case alone:
			from = ls
			to = pastLineEnd(raw, to)
		}
		out, _ = splice(raw, from, to-from, nil)
	} else {
		// 最后一个成员：它自己的尾随逗号要去掉（jsonc 在 stripped 里把它
		// 抹掉了，所以上面看不到），否则唯一成员的逗号会留下前头什么都没有
		// 的 "{ , }"，OpenCode 会判它无效。
		if t := nextToken(raw, b); t < len(raw) && raw[t] == ',' {
			raw = append(raw[:t:t], raw[t+1:]...)
			stripped = append(stripped[:t:t], stripped[t+1:]...)
		}
		// 再删这一行，以及它前面的逗号
		if alone {
			from = ls
			e := to
			for e < len(raw) && (raw[e] == ' ' || raw[e] == '\t') {
				e++
			}
			if p := pastLineEnd(raw, e); p > e {
				to = p
			}
		}
		out, _ = splice(raw, from, to-from, nil)
		k := a
		for k > 0 && ws(stripped[k-1]) {
			k--
		}
		if k > 0 && stripped[k-1] == ',' {
			out, _ = splice(out, k-1, 1, nil)
		}
	}

	// 把空了的父对象收缩成 {}
	s2 := jsonc.ToJSONInPlace(append([]byte(nil), out...))
	parent := gjson.ParseBytes(s2)
	if parts := splitPath(keyPath); len(parts) > 1 {
		parent = gjson.GetBytes(s2, strings.Join(parts[:len(parts)-1], "."))
	}
	if parent.IsObject() && len(parent.Map()) == 0 {
		open := objectOpenAt(s2, parent.Index)
		close := matchBrace(s2, open)
		if close > open && len(bytes.TrimSpace(out[open+1:close])) == 0 {
			out, _ = splice(out, open, close-open+1, []byte("{}"))
		}
	}
	return out, true, nil
}
