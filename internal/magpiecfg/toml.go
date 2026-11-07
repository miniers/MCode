// 本文件移植自 magpie 的 internal/edit/toml.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。
//
// 与 magpie 的差异：去掉文件解析缓存（每次都读盘），未移植 Inline 内联表、
// toml_array、批量表替换以及写入时插入的 magpie 标记注释。行处理辅助函数
// 取自 magpie 的 internal/edit/text.go。

package magpiecfg

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// 整个 TOML 表被当作一个单元处理：调用方拥有的表（例如一个模型 provider）
// 原样整体替换，文件里其它每一行都不动。

// TOMLTables 按文件顺序列出显式的普通表头（不含数组表）。名字是表头的键
// 路径：带引号的部分保留引号，点号两侧的空白不属于名字，所以 `[ a . b ]`
// 的名字是 `a.b`。文件不存在返回 (nil, nil)；错误带路径。
func TOMLTables(path string) ([]string, error) {
	doc, err := tomlFileOf(path)
	if err != nil {
		return nil, err
	}
	return doc.names, nil
}

// GetTOMLTable 返回一个表的标量键；文件或表不存在时返回 (nil, nil)。读取
// 或解析错误带路径返回。字符串会被解码，其它标量（布尔、数字、日期和时间）
// 保留字面文本。数组和内联表省略。同名数组表是类型错误，不是表不存在。
func GetTOMLTable(path, name string) (map[string]string, error) {
	tables, err := tomlTablesOf(path)
	if err != nil {
		return nil, err
	}
	table, ok := tomlTableNamed(tables, name)
	if !ok {
		return nil, nil
	}
	if table.array {
		return nil, fmt.Errorf("%s: line %d, column %d: %q is an array table, expected an ordinary table", path, table.from+1, table.column, name)
	}
	out := map[string]string{}
	for _, kv := range table.keys {
		if kv.scalar {
			out[kv.name] = kv.value
		}
	}
	return out, nil
}

// GetTOMLDocument 把整个 TOML 文件解码成 map；文件不存在或为空时返回
// (nil, nil)。解码保留 TOML 的原生类型（int64、float64、bool、string 等），
// 供 drift 比较与整表还原使用。GetTOMLTable 把布尔退化成字面文本，不能用于
// 读写往返。
func GetTOMLDocument(path string) (map[string]any, error) {
	raw, err := Read(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, tomlDecodeError(err))
	}
	return doc, nil
}

// LookupTOMLValue 按点分路径在解码后的 TOML 文档里取值。路径为空或某一级
// 不是表时返回 (nil, false)。
func LookupTOMLValue(doc map[string]any, dotted string) (any, bool) {
	if dotted == "" {
		return nil, false
	}
	var cur any = doc
	for _, part := range strings.Split(dotted, ".") {
		table, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := table[part]
		if !ok {
			return nil, false
		}
		cur = value
	}
	return cur, true
}

// SetTOMLTable 用给定的键替换名为 name 的表，不存在时追加。同名数组表会被
// 拒绝。已有表的其它行不受影响。
func SetTOMLTable(path, name string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	block := []string{"[" + name + "]"}
	for _, kv := range kvs {
		block = append(block, kv.Path+" = "+tomlLiteral(kv.Value))
	}
	from, to := table.from, table.to
	var out []string
	if from < 0 {
		out = trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
		out = append(out, "")
	} else {
		out = append(out, lines[:from]...)
		out = append(out, block...)
		if to < len(lines) && strings.TrimSpace(lines[to]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[to:]...)
	}
	return writeTOML(path, out)
}

// GetTOMLTop 读取 TOML 文件表头之前的顶层键。字符串会被解码；其它值按写入
// 时的文本返回，不含尾随注释，跨多行的数组或内联表也包括在内。TOML 解析器
// 拒绝的文件退化为逐行读取。
func GetTOMLTop(path, key string) (string, bool) {
	doc, err := tomlFileOf(path)
	if err != nil {
		return getTOMLTopLines(path, key)
	}
	for _, kv := range doc.root.keys {
		if kv.name == key {
			if kv.scalar {
				return kv.value, true
			}
			return kv.text, true
		}
	}
	return "", false
}

// getTOMLTopLines 是 GetTOMLTop 对不是合法 TOML 的文件的处理：在第一个像
// 表头的行之前，找第一行像 `key = value` 的内容。
func getTOMLTopLines(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return "", false
	}
	for _, line := range splitLines(string(raw)) {
		if tomlTable.MatchString(line) {
			break
		}
		if m := tomlKV.FindStringSubmatch(line); m != nil && strings.Trim(m[1], `"`) == key {
			return tomlValue(m[2]), true
		}
	}
	return "", false
}

// SetTOMLTop 按字符串设置 TOML 文件的顶层键。已有的键整体替换，连同它值
// 占的每一行；新键紧跟在最后一个已有顶层键的值之后，没有顶层键时放在文件
// 开头。每个键都在读到的文件内容里定位，所以整次编辑只有一次写入，要么全部
// 生效要么完全不生效。
func SetTOMLTop(path string, kvs ...KV) error {
	return setTOMLTop(path, false, kvs...)
}

// SetTOMLTopPreserving 是 SetTOMLTop，但保留已有键的空白和尾随注释；新键
// 的写法与 SetTOMLTop 相同。
func SetTOMLTopPreserving(path string, kvs ...KV) error {
	return setTOMLTop(path, true, kvs...)
}

func setTOMLTop(path string, preserve bool, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	type span struct {
		to   int
		line string
	}
	replace := map[int]span{}
	var added []string
	addedAt := map[string]int{}
	at := 0
	if n := len(doc.root.keys); n > 0 {
		at = doc.root.keys[n-1].to
	}
	for _, kv := range kvs {
		line := kv.Path + " = " + tomlLiteral(kv.Value)
		found := false
		for _, k := range doc.root.keys {
			if k.name == kv.Path {
				if preserve {
					line = k.prefix + tomlLiteral(kv.Value) + k.suffix
				}
				replace[k.from] = span{k.to, line}
				found = true
				break
			}
		}
		switch i, ok := addedAt[kv.Path]; {
		case found:
		case ok:
			added[i] = line
		default:
			addedAt[kv.Path] = len(added)
			added = append(added, line)
		}
	}
	out := make([]string, 0, len(lines)+len(added))
	for i := 0; i < len(lines); {
		if i == at {
			out = append(out, added...)
			added = nil
		}
		if r, ok := replace[i]; ok {
			out = append(out, r.line)
			i = r.to
			continue
		}
		out = append(out, lines[i])
		i++
	}
	out = append(out, added...)
	return writeTOML(path, out)
}

// DelTOMLTop 删除顶层（表头之前）键，连同它值的每一行。
func DelTOMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	at := 0
	for _, kv := range doc.root.keys {
		if drop[kv.name] {
			out = append(out, lines[at:kv.from]...)
			at = kv.to
		}
	}
	out = append(out, lines[at:]...)
	return writeTOML(path, out)
}

// DelTOMLTable 删除名为 name 的表（表头和表体）。子表和数组表块不在这个
// 范围内，会保留。
func DelTOMLTable(path, name string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	from, to := table.from, table.to
	if from < 0 {
		return nil
	}
	// 把表头前的空行一起带走，免得留下空隙。
	for from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	out := append([]string{}, lines[:from]...)
	if to < len(lines) && len(out) > 0 && strings.TrimSpace(lines[to]) != "" {
		out = append(out, "")
	}
	out = append(out, lines[to:]...)
	return writeTOML(path, out)
}

type tomlTableSpan struct {
	from, to int
	column   int // 表名起点的 1 基列号
	name     string
	array    bool
	keys     []tomlKeySpan
}

type tomlKeySpan struct {
	from, to       int // to 为 -1 表示还没被下一个表达式或文件末尾界定
	column         int // 键起点的 1 基列号
	name, value    string
	text           string // 从 prefix 到 suffix 的值原文
	prefix, suffix string // 完整值之前和之后的原文
	comment        int    // 尾随注释的列号，没有时为 -1
	scalar         bool
}

// parseTOMLTable 在原始行里定位一个表和它完整的键/值表达式。范围是
// [from, to)；表不存在时 from 为 -1。子数组表是独立的块，不在目标表的范围
// 内；保留它会在父表删除后隐式重建父表。
func parseTOMLTable(lines []string, name string) (tomlTableSpan, error) {
	tables, err := parseTOMLTables(lines)
	if err != nil {
		return tomlTableSpan{}, err
	}
	if table, ok := tomlTableNamed(tables, name); ok {
		if table.array {
			return tomlTableSpan{}, fmt.Errorf("line %d, column %d: %q is an array table, expected an ordinary table", table.from+1, table.column, name)
		}
		return table, nil
	}
	return tomlTableSpan{from: -1, to: -1}, nil
}

func tomlTableNamed(tables []tomlTableSpan, name string) (tomlTableSpan, bool) {
	for _, table := range tables {
		if table.name == name {
			return table, true
		}
	}
	return tomlTableSpan{}, false
}

// tomlTablesOf 是文件的 parseTOMLTables；文件不存在时返回 (nil, nil)。
func tomlTablesOf(path string) ([]tomlTableSpan, error) {
	doc, err := tomlFileOf(path)
	return doc.tables, err
}

// tomlFileOf 解析文件，文件不存在时是空文档且没有错误。
func tomlFileOf(path string) (tomlFile, error) {
	b, err := Read(path)
	if err != nil {
		return tomlFile{}, err
	}
	if b == nil {
		return tomlFile{}, nil
	}
	doc, err := parseTOMLFile(splitLines(string(TrimBOM(b))))
	if err != nil {
		return tomlFile{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, table := range doc.tables {
		if !table.array {
			doc.names = append(doc.names, table.name)
		}
	}
	return doc, nil
}

// parseTOMLTables 在一次解析中找出普通表和数组表块。只有顶层表达式界定
// 编辑范围，所以多行值内部的行不会被误认成键、注释或表头。
func parseTOMLTables(lines []string) ([]tomlTableSpan, error) {
	doc, err := parseTOMLFile(lines)
	return doc.tables, err
}

// tomlFile 是文件顶层（表头之前）的键，作为覆盖 [0, 第一个表头) 的无名根
// 表，以及按文件顺序排列的表块。
type tomlFile struct {
	root   tomlTableSpan
	tables []tomlTableSpan
	names  []string // TOMLTables
}

// parseTOMLFile 是带顶层键的 parseTOMLTables，定位方式相同：一个键的范围
// 覆盖它整个值，无论占多少行。
func parseTOMLFile(lines []string) (tomlFile, error) {
	p := unstable.Parser{KeepComments: true}
	p.Reset([]byte(strings.Join(lines, "\n")))
	shapeOf := tomlShaper(lines)
	root := tomlTableSpan{to: len(lines)}
	var tables []tomlTableSpan
	current := func() *tomlTableSpan {
		if len(tables) == 0 {
			return &root
		}
		return &tables[len(tables)-1]
	}
	finishKey := func(to int) error {
		table := current()
		if len(table.keys) == 0 {
			return nil
		}
		kv := &table.keys[len(table.keys)-1]
		if kv.to != -1 {
			return nil
		}
		// 数组节点没有完整的 Raw 范围。下一个表达式（包括独立注释）界定
		// 值的结束，剩下的只是尾部空行。
		kv.to = to
		for kv.to > kv.from+1 && strings.TrimSpace(lines[kv.to-1]) == "" {
			kv.to--
		}
		if kv.to <= kv.from {
			return fmt.Errorf("line %d, column %d: cannot locate the end of the TOML value", kv.from+1, kv.column)
		}
		last := lines[kv.to-1]
		end := len(last)
		if kv.comment >= 0 {
			end = kv.comment
		}
		// 注释来自解析器节点，所以字符串里的 '#' 是值的一部分。保留结束值
		// 与注释之间的全部空白。
		end = len(strings.TrimRight(last[:end], " \t\r"))
		kv.suffix = last[end:]
		text := strings.Join(lines[kv.from:kv.to], "\n")
		kv.text = text[len(kv.prefix) : len(text)-len(kv.suffix)]
		return nil
	}
	for p.NextExpression() {
		expr := p.Expression()
		keyRange := expr.Raw
		var parts, rawParts []string
		if expr.Kind != unstable.Comment {
			for keys := expr.Key(); keys.Next(); {
				key := keys.Node()
				if len(parts) == 0 {
					keyRange = key.Raw
				}
				parts = append(parts, string(key.Data))
				// 表名用解析器自己的键文本，所以带引号的部分保留引号，而
				// 表头点号两侧的空白（如 `[ a . b ]`）不属于名字。
				rawParts = append(rawParts, string(p.Raw(key.Raw)))
				keyRange.Length = key.Raw.Offset + key.Raw.Length - keyRange.Offset
			}
		}
		shape := shapeOf(keyRange)
		i := shape.Start.Line - 1
		if err := finishKey(i); err != nil {
			return tomlFile{}, err
		}
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			current().to = i
			tables = append(tables, tomlTableSpan{
				from: i, to: len(lines), name: strings.Join(rawParts, "."),
				column: shape.Start.Column,
				array:  expr.Kind == unstable.ArrayTable,
			})
		case unstable.KeyValue:
			table := current()
			value := expr.Value()
			// 从解析出的键之后开始，键本身可能含 '='。
			_, valueText, _ := strings.Cut(lines[i][shape.End.Column-1:], "=")
			start := len(lines[i]) - len(strings.TrimLeft(valueText, " \t"))
			kv := tomlKeySpan{
				from: i, to: -1, name: strings.Join(parts, "."),
				column: shape.Start.Column,
				prefix: lines[i][:start], comment: -1,
				value: string(value.Data), scalar: value.Kind != unstable.Array && value.Kind != unstable.InlineTable,
			}
			if comment := expr.Next(); comment != nil && comment.Kind == unstable.Comment {
				kv.comment = shapeOf(comment.Raw).Start.Column - 1
			}
			table.keys = append(table.keys, kv)
		}
	}
	if err := p.Error(); err != nil {
		return tomlFile{}, tomlParseError(&p, err)
	}
	if err := finishKey(len(lines)); err != nil {
		return tomlFile{}, err
	}
	return tomlFile{root: root, tables: tables}, nil
}

// tomlShaper 是 lines 文档的解析器 Shape，从每行起点查找，而不是像 Shape
// 那样数每个范围之前的换行数：后者每个键都要遍历一遍文件，在 Codex 那种
// 1.5 MB 的 config.toml 上要三分之一秒。
func tomlShaper(lines []string) func(unstable.Range) unstable.Shape {
	starts := make([]int, len(lines))
	for i, off := 1, 0; i < len(lines); i++ {
		off += len(lines[i-1]) + 1
		starts[i] = off
	}
	at := func(off int) unstable.Position {
		i := sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
		return unstable.Position{Offset: off, Line: i + 1, Column: off - starts[i] + 1}
	}
	return func(r unstable.Range) unstable.Shape {
		return unstable.Shape{Start: at(int(r.Offset)), End: at(int(r.Offset + r.Length))}
	}
}

func tomlParseError(p *unstable.Parser, err error) error {
	pe, ok := err.(*unstable.ParserError)
	if !ok {
		return err
	}
	data := p.Data()
	// EOF 错误使用空高亮。不要把空高亮传给 Range：解析器的切片偏移计算
	// 会在 nil 高亮上 panic。
	pos := unstable.Position{
		Offset: len(data), Line: bytes.Count(data, []byte{'\n'}) + 1,
		Column: len(data) - bytes.LastIndexByte(data, '\n'),
	}
	if len(pe.Highlight) > 0 {
		pos = p.Shape(p.Range(pe.Highlight)).Start
	}
	return fmt.Errorf("line %d, column %d: %w", pos.Line, pos.Column, err)
}

// writeTOML 在改动文件系统之前先校验确切的字节。解码同时检查重复键和冲突
// 定义；它从不重写文档，所以注释和格式在通过校验后原样保留。
func writeTOML(path string, lines []string) error {
	orig, _ := Read(path)
	data := []byte(joinLinesLike(lines, string(orig)))
	if err := validateTOML(data); err != nil {
		return fmt.Errorf("%s: edited TOML is invalid: %w", path, err)
	}
	return WriteAtomic(path, data)
}

func validateTOML(data []byte) error {
	// 先检查语法，让 EOF 错误使用与读取时相同、对 nil 高亮安全的诊断，
	// 再让解码器检查 TOML 定义。
	var p unstable.Parser
	p.Reset(data)
	for p.NextExpression() {
	}
	if err := p.Error(); err != nil {
		return tomlParseError(&p, err)
	}
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		return tomlDecodeError(err)
	}
	return nil
}

// tomlDecodeError 把 pelletier 的 DecodeError 转成带行列号的错误；其它错误
// 原样返回。
func tomlDecodeError(err error) error {
	if de, ok := err.(*toml.DecodeError); ok {
		line, column := de.Position()
		return fmt.Errorf("line %d, column %d: %w", line, column, err)
	}
	return err
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Raw 是一个原样写出的 TOML 值：调用方已经拼好的数组或内联表。
type Raw string

func tomlLiteral(v any) string {
	switch x := v.(type) {
	case Raw:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint8:
		return strconv.FormatUint(uint64(x), 10)
	case uint16:
		return strconv.FormatUint(uint64(x), 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float32:
		return tomlFloat(float64(x), 32)
	case float64:
		return tomlFloat(x, 64)
	}
	return tomlString(toString(v))
}

// tomlFloat 写 TOML 浮点数。FormatFloat 的 'g' 会把 1.0 写成 "1"（读回来
// 是整数），所以没有小数点或指数时补上 ".0"，保住浮点类型。
func tomlFloat(v float64, bits int) string {
	s := strconv.FormatFloat(v, 'g', -1, bits)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// tomlString 把 s 写成 TOML 基本字符串。Go 的 strconv.Quote 不能替代：它把
// 控制字节转义成 \x01，而 TOML 没有 \x 转义，这样的值会让文件没有 TOML
// 解析器能读。writeTOML 会在任何改动落盘前拒绝该文件，所以配置不会被写坏，
// 但这次编辑也就做不成了——程序自己的值必须能写进去。转义按 TOML 规范；
// 其它可打印字符（包括非 ASCII）原样写入。
func tomlString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			// 一个不构成 rune 的字节：TOML 没有转义单个字节的办法，只能
			// 转义码点，所以不是合法 UTF-8 的值写不出来。这里原样保留，
			// 由 writeTOML 拒绝该文件，而不是悄悄换成 U+FFFD。
			b.WriteByte(s[i-size])
			continue
		}
		switch r {
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// 以下为 magpie internal/edit/text.go 移植的行处理辅助函数。TOML 顶层项
// 由解析器（toml.go）定位，再在不重排文件其余部分的前提下编辑。

var (
	// tomlTable 和 tomlKV 只用于读取 TOML 解析器拒绝的文件；见 GetTOMLTop。
	tomlTable = regexp.MustCompile(`^\s*\[`)
	tomlKV    = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+|"[^"]*")\s*=\s*(.*?)\s*$`)
)

// unquote 去掉开头的带引号标量（以及它后面的内容，例如行内注释）；
// v 不以引号开头时 ok 为 false。
func unquote(v string) (string, bool) {
	if v == "" || (v[0] != '"' && v[0] != '\'') {
		return "", false
	}
	q := v[0]
	for i := 1; i < len(v); i++ {
		if q == '"' && v[i] == '\\' {
			i++
			continue
		}
		if v[i] == q {
			if q == '"' {
				if s, err := strconv.Unquote(v[:i+1]); err == nil {
					return s, true
				}
			}
			return v[1:i], true
		}
	}
	return v[1:], true
}

func tomlValue(v string) string {
	if s, ok := unquote(v); ok {
		return s
	}
	if i := strings.Index(v, "#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

// splitLines 按 '\n' 切分，但把结尾换行保留成最后一个空元素，这样 joinLines
// 能精确还原文件。
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

// joinLinesLike 是给从 orig 编辑出的行用的 joinLines：orig 每一处换行都是
// \r\n 时，新增或替换的行也用 \r\n，文件不会变成混合换行。未改动的行本来
// 就带着 \r，原本混用换行的文件保持原样。
func joinLinesLike(lines []string, orig string) string {
	s := joinLines(lines)
	if n := strings.Count(orig, "\n"); n > 0 && strings.Count(orig, "\r\n") == n {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	}
	return s
}

func joinLines(lines []string) string {
	s := strings.Join(lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
