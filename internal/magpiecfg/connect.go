package magpiecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/jsonc"
)

// Field 是一条点分键路径与目标值。Value 可以是字符串、map、
// json.RawMessage 等任何能编码成 JSON 的值（例如 modelPicker 对象）。
// Delete 为真时表示要删掉这个键，此时 Value 被忽略。
//
// KeepForeign 为真时只接管自己的值：文件里已经有这个键、而它的值不是我们
// 上次写入的值（applied 里没有记录也算），就整条跳过——不写、不记进
// applied、不动 stash，用户或别的工具写的对象保持原样。
//
// TOMLTable 用于 TOML 文件：Path 是一张表的名字（如
// model_providers.mcode-magpie），Value 必须是 map[string]any，整张表被
// 当作一个字段替换、删除和还原；TOML 的标量字段仍按点分路径走顶层键。
//
// PruneAncestors 只在恢复（Delete）时有意义：列出写这个键时由我们创建、
// 从浅到深的祖先对象，删掉这个键后它们如果变空就一并删掉（从深到浅），
// 不在用户文件里留下 "providers": {} 这样的空壳。
type Field struct {
	Path           string
	Value          any
	Delete         bool
	KeepForeign    bool
	TOMLTable      bool
	PruneAncestors []string
}

// Format 是配置文件的格式。零值 FormatJSON 让旧的 JSON/JSONC 调用方保持
// 原有行为；旧版本的状态文件没有 format 字段，读出来也是零值。
type Format string

const (
	// FormatJSON 是带注释的 JSON（settings.json、cline 的 providers.json）。
	FormatJSON Format = ""
	// FormatTOML 是 codex 的 config.toml。
	FormatTOML Format = "toml"
	// FormatYAML 预留给以后支持 YAML 配置文件的 agent；当前没有写 YAML 的
	// 编辑原语，计划里出现它会直接报错。
	FormatYAML Format = "yaml"
)

// normalizeFormat 把未知格式按 JSON 处理，这样旧状态文件与调用方里新的
// 计划格式不一致时仍按 JSON 读写，不会悄悄换成 TOML 编辑。
func normalizeFormat(format Format) Format {
	if format == FormatTOML {
		return FormatTOML
	}
	return FormatJSON
}

// validateFormat 校验计划声明的格式。normalizeFormat 会把未知值当 JSON，
// 写计划的调用方写错格式时应当立刻报错，而不是悄悄按 JSON 改文件。
func validateFormat(format Format) error {
	switch format {
	case FormatJSON, FormatTOML:
		return nil
	case FormatYAML:
		return errors.New("magpiecfg: 暂不支持 YAML 配置文件")
	default:
		return fmt.Errorf("magpiecfg: 未知的配置文件格式 %q", format)
	}
}

// FilePlan 描述一个要接管的配置文件及其字段。
type FilePlan struct {
	Path   string
	Format Format
	Fields []Field
}

// Plan 描述一次 connect 要接管的所有配置文件与字段。一个 agent 可以同时
// 接管多个文件（例如 cline 的 providers.json 与 models.json）。
type Plan struct {
	Agent string
	Files []FilePlan
}

// Status.State 的取值。
const (
	// StatusNone 表示这个 agent 没有被接管。
	StatusNone = "none"
	// StatusApplied 表示配置文件里仍是我们写入的值。
	StatusApplied = "applied"
	// StatusDrifted 表示用户改动了我们写过的键。
	StatusDrifted = "drifted"
	// StatusKept 表示用户选择了「保留我的修改」，不再跟踪。
	StatusKept = "kept"
	// StatusMissing 表示接管的配置文件已被删除。
	StatusMissing = "missing"
	// StatusError 表示读状态或配置文件失败。
	StatusError = "error"
)

// Status 是一次 drift 检测的结果。Drifted 只在 State 为 drifted 时有值。
type Status struct {
	State   string
	Path    string
	Drifted []string
	Err     string
}

// 与 magpie 的 internal/agent（stash.go、applied.go）相比，这里只实现单机、
// 多文件的 JSON/JSONC/TOML 最小流程：
//   - 不处理 WSL、不写 Windows 的 managed-settings.json，也不做重连
//     （reconnection）。
//   - mcode 不在启动时自动 Reapply：mcode 不是常驻的配置管理者，用户手改
//     settings.json 是常见操作，自动覆盖会与用户打架。设置页只显示 Drift
//     并给出「重新写入」按钮，由用户决定。
//   - 不按字段分组（Group）判定，每个键各自比较。

func validatePlan(p Plan) error {
	if err := validateAgent(p.Agent); err != nil {
		return err
	}
	if len(p.Files) == 0 {
		return errors.New("magpiecfg: 计划里没有配置文件")
	}
	seen := make(map[string]bool, len(p.Files))
	for _, fp := range p.Files {
		path := strings.TrimSpace(fp.Path)
		if path == "" {
			return errors.New("magpiecfg: 配置文件路径为空")
		}
		if seen[path] {
			return fmt.Errorf("magpiecfg: %s 的配置文件 %s 重复", p.Agent, path)
		}
		seen[path] = true
		if err := validateFormat(fp.Format); err != nil {
			return fmt.Errorf("magpiecfg: %s: %w", p.Agent, err)
		}
		for _, f := range fp.Fields {
			if strings.TrimSpace(f.Path) == "" {
				return fmt.Errorf("magpiecfg: %s 的字段路径为空", p.Agent)
			}
			if normalizeFormat(fp.Format) == FormatTOML {
				if err := validateTOMLField(f); err != nil {
					return fmt.Errorf("magpiecfg: %s 的字段 %s: %w", p.Agent, f.Path, err)
				}
			}
		}
	}
	return nil
}

// validateTOMLField 拒绝写不成 TOML 标量/表的字段值：TOML 编辑原语只支持
// 标量与整表替换，JSON 里的数组、嵌套对象会被写成无法解析的文本。
func validateTOMLField(f Field) error {
	if f.Delete {
		return nil
	}
	if !f.TOMLTable {
		if !tomlScalar(f.Value) {
			return fmt.Errorf("值 %T 不能写成 TOML 标量", f.Value)
		}
		return nil
	}
	table, ok := f.Value.(map[string]any)
	if !ok {
		return fmt.Errorf("TOML 表字段的值必须是 map[string]any，得到 %T", f.Value)
	}
	for key, v := range table {
		if strings.TrimSpace(key) == "" {
			return errors.New("TOML 表的键为空")
		}
		if !tomlScalar(v) {
			return fmt.Errorf("表键 %s 的值 %T 不能写成 TOML 标量", key, v)
		}
	}
	return nil
}

// tomlScalar 报告 v 能否由 tomlLiteral 写成 TOML 标量。
func tomlScalar(v any) bool {
	switch v.(type) {
	case string, bool, Raw,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	}
	return false
}

// readConfig 读取配置文件；文件不存在时 exists 为 false，raw 为 nil。
func readConfig(path string) (raw []byte, exists bool, err error) {
	raw, err = Read(path)
	if err != nil {
		return nil, false, fmt.Errorf("magpiecfg: 读取 %s: %w", path, err)
	}
	return raw, raw != nil, nil
}

// checkConfig 拒绝无法解析的配置文件。空文件按空对象/空文档处理，不当作
// 错误。
func checkConfig(format Format, path string, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if normalizeFormat(format) == FormatTOML {
		if err := validateTOML(raw); err != nil {
			return fmt.Errorf("magpiecfg: %s 不是有效的 TOML，未做改动: %w", path, err)
		}
		return nil
	}
	if !ValidJSONC(raw) {
		return fmt.Errorf("magpiecfg: %s 不是有效的 JSON/JSONC，未做改动", path)
	}
	return nil
}

// currentField 读配置文件里一个键的当前值。Value 是规范化后的 JSON 文本
// （字符串带引号），Had 表示键是否存在。TOML 先整份解码再点分查找：GetTOMLTable
// 把布尔退化成 "true" 文本，拿它和应用记录比较会永远 drift。
//
// 每个调用点都先做过 checkConfig，所以这里再读失败只可能是并发改动；按
// 「不是我们写的值」处理，宁可少恢复也不覆盖用户。
func currentField(format Format, path, key string) fieldState {
	if normalizeFormat(format) == FormatTOML {
		doc, err := GetTOMLDocument(path)
		if err != nil {
			return fieldState{}
		}
		v, ok := LookupTOMLValue(doc, key)
		if !ok {
			return fieldState{}
		}
		text, err := canonicalJSON(v)
		if err != nil {
			return fieldState{}
		}
		return fieldState{Value: text, Had: true}
	}
	v, had := GetJSONRaw(path, key)
	return fieldState{Value: v, Had: had}
}

// foreignField 报告 key 是不是别人写的、需要完整保留的键：文件里存在这个
// 键，且它的值不是我们 applied 记录里的值（没记录过也算别人的）。
func foreignField(format Format, path string, target *appliedTarget, key string) bool {
	cur := currentField(format, path, key)
	if !cur.Had {
		return false
	}
	prev, ok := target.Fields[key]
	return !ok || !sameJSONValue(prev, cur)
}

// missingAncestors 返回 keyPath 上从第一个不存在的祖先开始的所有祖先对象
// （点分路径，从浅到深；JSON 文件）。写这个键时 SetJSON 会顺手把不存在的
// 祖先建成对象，断开时要能把这些由我们创建的壳从深到浅删掉；用户原本就有
// 的对象（包括空对象）不在返回值里，保持原样。TOML 没有嵌套自动创建的
// 语义。
func missingAncestors(format Format, path, keyPath string) []string {
	if normalizeFormat(format) == FormatTOML {
		return nil
	}
	parts := splitPath(keyPath)
	var missing []string
	for i := 1; i < len(parts); i++ {
		ancestor := strings.Join(parts[:i], ".")
		if len(missing) > 0 {
			missing = append(missing, ancestor)
			continue
		}
		if _, had := GetJSONRaw(path, ancestor); !had {
			missing = append(missing, ancestor)
		}
	}
	return missing
}

// writeFileFields 按计划写一个文件的值、删键。JSON 与 TOML 共用一个入口，
// 格式决定用哪套编辑原语。两个操作包在 Atomically 里：删除失败时刚写入的
// 值会一起回滚，不留下写了一半的配置。
func writeFileFields(fp FilePlan) error {
	format, path, fields := fp.Format, fp.Path, fp.Fields
	if normalizeFormat(format) == FormatTOML {
		return writeTOMLFields(path, fields)
	}
	sets := make([]KV, 0, len(fields))
	dels := make([]string, 0, len(fields))
	var prunes []string
	for _, f := range fields {
		if f.Delete {
			dels = append(dels, f.Path)
			prunes = append(prunes, f.PruneAncestors...)
			continue
		}
		sets = append(sets, KV{Path: f.Path, Value: f.Value})
	}
	if len(sets) == 0 && len(dels) == 0 {
		return nil
	}
	return Atomically(func() error {
		if len(sets) > 0 {
			if err := SetJSON(path, sets...); err != nil {
				return fmt.Errorf("magpiecfg: 写入 %s: %w", path, err)
			}
		}
		if len(dels) > 0 {
			if err := DelJSON(path, dels...); err != nil {
				return fmt.Errorf("magpiecfg: 删除 %s 的字段: %w", path, err)
			}
		}
		if len(prunes) > 0 {
			if err := pruneEmptyJSON(path, prunes); err != nil {
				return fmt.Errorf("magpiecfg: 清理 %s 的空对象: %w", path, err)
			}
		}
		return nil
	}, path)
}

// writeTOMLFields 把字段写进 TOML 文件。标量走顶层键，用
// SetTOMLTopPreserving 保留原有空白与尾随注释，断开时才能逐字节还原；整表
// 字段由 Field.TOMLTable 标记，按表整体替换或删除。标量先写，新建文件时
// 顶层键会落在表头之前，与常见的 config.toml 布局一致。
func writeTOMLFields(path string, fields []Field) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]KV, 0, len(fields))
	var dels []string
	var tables []Field
	for _, f := range fields {
		if f.TOMLTable {
			tables = append(tables, f)
			continue
		}
		if f.Delete {
			dels = append(dels, f.Path)
			continue
		}
		sets = append(sets, KV{Path: f.Path, Value: f.Value})
	}
	return Atomically(func() error {
		if len(sets) > 0 {
			if err := SetTOMLTopPreserving(path, sets...); err != nil {
				return fmt.Errorf("magpiecfg: 写入 %s: %w", path, err)
			}
		}
		for _, key := range dels {
			if err := DelTOMLTop(path, key); err != nil {
				return fmt.Errorf("magpiecfg: 删除 %s 的字段: %w", path, err)
			}
		}
		for _, f := range tables {
			if f.Delete {
				if err := DelTOMLTable(path, f.Path); err != nil {
					return fmt.Errorf("magpiecfg: 删除 %s 的 %s: %w", path, f.Path, err)
				}
				continue
			}
			table, ok := f.Value.(map[string]any)
			if !ok {
				return fmt.Errorf("magpiecfg: 字段 %s 的表值类型是 %T", f.Path, f.Value)
			}
			kvs := make([]KV, 0, len(table))
			for _, key := range slices.Sorted(maps.Keys(table)) {
				kvs = append(kvs, KV{Path: key, Value: table[key]})
			}
			if err := SetTOMLTable(path, f.Path, kvs...); err != nil {
				return fmt.Errorf("magpiecfg: 写入 %s 的 %s: %w", path, f.Path, err)
			}
		}
		return nil
	}, path)
}

// fieldsToState 把一个计划里的字段换算成 applied 记录：写过的键记下规范化
// 后的 JSON 文本，删掉的键记为「不存在」。
func fieldsToState(fields []Field) (map[string]fieldState, error) {
	out := make(map[string]fieldState, len(fields))
	for _, f := range fields {
		if f.Delete {
			out[f.Path] = fieldState{Table: f.TOMLTable}
			continue
		}
		v, err := canonicalJSON(f.Value)
		if err != nil {
			return nil, fmt.Errorf("magpiecfg: 字段 %s: %w", f.Path, err)
		}
		out[f.Path] = fieldState{Value: v, Had: true, Table: f.TOMLTable}
	}
	return out, nil
}

// recordedValue 把状态里记录的规范化文本还原成可写进文件的 JSON 值。旧
// 版本状态文件里的字符串没有引号，canonicalFieldText 会为它们补上引号。
func recordedValue(st fieldState) (json.RawMessage, error) {
	text, ok := canonicalFieldText(st.Value)
	if !ok {
		return nil, fmt.Errorf("magpiecfg: 无法解析记录的值 %q", st.Value)
	}
	return json.RawMessage(text), nil
}

// decodeRecordedField 把状态里记录的值还原成可写回配置文件的字段。JSON
// 保持原来的 RawMessage；TOML 把记录里的 JSON 值解码成 Go 值（数字还原成
// int64/float64），整表字段带上 Table 标记，恢复时按整表写回。
func decodeRecordedField(format Format, key string, st fieldState) (Field, error) {
	f := Field{Path: key, TOMLTable: st.Table, PruneAncestors: st.Pruned}
	if !st.Had {
		f.Delete = true
		return f, nil
	}
	if normalizeFormat(format) != FormatTOML {
		v, err := recordedValue(st)
		if err != nil {
			return Field{}, err
		}
		f.Value = v
		return f, nil
	}
	text, ok := canonicalFieldText(st.Value)
	if !ok {
		return Field{}, fmt.Errorf("magpiecfg: 无法解析记录的值 %q", st.Value)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Field{}, fmt.Errorf("magpiecfg: 无法解析记录的值 %q: %w", st.Value, err)
	}
	f.Value = normalizeJSONNumbers(v)
	return f, nil
}

// normalizeJSONNumbers 把 UseNumber 解出来的数字换成 TOML 写入支持的
// int64/float64；大整数或其它形态保留文本，避免精度被改写。
func normalizeJSONNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case map[string]any:
		for key, child := range x {
			x[key] = normalizeJSONNumbers(child)
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = normalizeJSONNumbers(child)
		}
		return x
	}
	return v
}

// restoreFields 按 stash 恢复 applied 里仍是我们写入的键，并清掉这些键的
// 记录；skip 里的键留给调用方按新计划重写。返回要写回文件的编辑。
func restoreFields(format Format, path string, target *appliedTarget, stFile map[string]fieldState, skip map[string]fieldState) ([]Field, error) {
	var edits []Field
	for _, key := range slices.Sorted(maps.Keys(target.Fields)) {
		prev := target.Fields[key]
		if _, ok := skip[key]; ok {
			continue
		}
		if sameJSONValue(currentField(format, path, key), prev) {
			if orig, ok := stFile[key]; ok {
				f, err := decodeRecordedField(format, key, orig)
				if err != nil {
					return nil, fmt.Errorf("magpiecfg: 恢复 %s 的原值: %w", key, err)
				}
				// 旧状态文件的 stash 没有 table 标记；它记的是我们写过的键，
				// applied 里的标记才是权威的恢复方式（整表而不是标量）。
				f.TOMLTable = f.TOMLTable || prev.Table
				edits = append(edits, f)
			}
		}
		delete(stFile, key)
		delete(target.Fields, key)
	}
	return edits, nil
}

// syncFileTarget 把一份文件的计划字段同步进 applied 与 stash 记录（不写
// 文件）。字段集合变化时旧键不会遗留：
//
//  1. 计划里不再出现的键，当前值仍等于我们写的值时按 stash 恢复原值
//     （原本不存在就删除）；用户手改过的只清记录、不碰文件。
//  2. 新出现的键在写入前先把当前值记进 stash，保留最早的接管前原值。
func syncFileTarget(fp FilePlan, target *appliedTarget, stFile map[string]fieldState) ([]Field, error) {
	format, path := fp.Format, fp.Path
	kept := make([]Field, 0, len(fp.Fields))
	for _, f := range fp.Fields {
		if f.KeepForeign && foreignField(format, path, target, f.Path) {
			continue
		}
		kept = append(kept, f)
	}
	next, err := fieldsToState(kept)
	if err != nil {
		return nil, err
	}

	edits, err := restoreFields(format, path, target, stFile, next)
	if err != nil {
		return nil, err
	}

	// 新出现的键：先记下写入前的原值。已经记过的保留最早的接管前值；
	// 当前值仍是我们的值时不覆盖 stash。
	for _, f := range kept {
		cur := currentField(format, path, f.Path)
		if prev, ok := target.Fields[f.Path]; ok && sameJSONValue(prev, cur) {
			continue
		}
		if _, ok := stFile[f.Path]; !ok {
			// 原值不存在时也要记住这个键是整张表，断开时才会按表删除，
			// 而不是当成顶层标量删掉。
			cur.Table = f.TOMLTable
			// 路径上原本不存在的祖先对象由这次写入创建，断开时要一并清理。
			cur.Pruned = missingAncestors(format, path, f.Path)
			stFile[f.Path] = cur
		}
	}

	target.Fields = next
	return append(edits, kept...), nil
}

// sameOutsideFields 报告 current 与我们上次写入的 written 除了 keys 之外
// 是否内容一致。两份文档都解析成值、删掉这些键再按 JSON 语义比较，键序与
// 排版的差异不算改动。written 为空（旧状态或没记录到）时按不可信处理。
//
// 断开时按原文整份写回只在「文件里除了我们的字段之外什么都没变」时才安全：
// 用户改过别的键，整份写回会把那次改动抹掉。
func sameOutsideFields(current []byte, written string, keys []string) bool {
	if strings.TrimSpace(written) == "" {
		return false
	}
	cur, ok := decodeDocument(current)
	if !ok {
		return false
	}
	prev, ok := decodeDocument([]byte(written))
	if !ok {
		return false
	}
	for _, key := range keys {
		deleteDocumentPath(cur, splitPath(key))
		deleteDocumentPath(prev, splitPath(key))
	}
	a, err := canonicalJSON(cur)
	if err != nil {
		return false
	}
	b, err := canonicalJSON(prev)
	if err != nil {
		return false
	}
	return a == b
}

// decodeDocument 把整份 JSON/JSONC 文档解开成值。数字保留原文
// （json.Number），两边比较时不会因为 float64 精度而误判。
func decodeDocument(raw []byte) (map[string]any, bool) {
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(stripped))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	doc, ok := v.(map[string]any)
	return doc, ok
}

// deleteDocumentPath 按键路径从 doc 里删掉一个键；路径不存在或中间是标量
// 时什么也不做。
func deleteDocumentPath(doc map[string]any, parts []string) {
	for i, part := range parts {
		key := unescapeKey(part)
		if i == len(parts)-1 {
			delete(doc, key)
			return
		}
		child, ok := doc[key].(map[string]any)
		if !ok {
			return
		}
		doc = child
	}
}

// fileEdit 是一次同步里一个文件的写入内容；created 表示文件原本不存在、
// 由我们创建，清空后要整个删掉。
type fileEdit struct {
	plan    FilePlan
	edits   []Field
	created bool
	// restore 非空时直接把接管前的原文写回这个文件：文件自我们最后一次
	// 写入后没有被改过，逐字节还原比逐字段恢复更忠实（键序、缩进和末尾
	// 换行都是用户原来的）。
	restore string
}

// syncFiles 把多个文件的计划字段同步进配置文件，并更新 applied 与 stash。
// Connect 与 Reapply 共用这一条路径。已经不在计划里的文件按 stash 恢复，
// 避免换计划后留下上一份配置。所有文件与两个状态文件包在同一个 Atomically
// 里：任何一步失败都回滚到操作前，不留下写了一半的配置。
func syncFiles(dataDir, agent string, ap *appliedFile, st *stashFile, files []FilePlan) error {
	planned := make(map[string]bool, len(files))
	writes := make([]fileEdit, 0, len(files))
	for _, fp := range files {
		planned[fp.Path] = true
		target, taken := ap.Files[fp.Path]
		format := normalizeFormat(fp.Format)
		// 记录里的格式与计划不一致（或还没有记录）时，旧记录里的键和规范化
		// 文本对新格式没有意义，从头记。
		fresh := !taken || normalizeFormat(Format(target.Format)) != format
		if fresh {
			target = appliedTarget{Format: string(format)}
		}
		raw, exists, err := readConfig(fp.Path)
		if err != nil {
			return err
		}
		// 文件原本不存在、由这次写入创建时，恢复后可能要整个删掉。已有记录
		// 说它当初是我们创建的就继续保留这个标记。
		if !exists {
			target.Created = true
		}
		// 上一次写进这个文件的键；syncFileTarget 会就地删掉 target.Fields 里
		// 的键，所以比较之前先记下来。
		var prevKeys []string
		if !fresh && format != FormatTOML {
			prevKeys = slices.Sorted(maps.Keys(target.Fields))
		}
		// 原文快照（断开时按原文逐字节写回）只在我们自己的字段之外没有别人的
		// 改动时才可信。首次接管时现在的文件就是原文；之后的写入只要当前内容
		// 与我们管理的键之外完全一致（没被改过，或只改了我们管理的键），快照
		// 继续有效；改过别的键就作废，断开时退回字段级恢复。
		if format != FormatTOML {
			switch {
			case fresh:
				if exists && len(bytes.TrimSpace(raw)) > 0 {
					if st.Orig == nil {
						st.Orig = map[string]string{}
					}
					st.Orig[fp.Path] = string(raw)
				} else {
					delete(st.Orig, fp.Path)
				}
			case sameOutsideFields(raw, target.Written, prevKeys):
				// 快照仍然可信。
			default:
				delete(st.Orig, fp.Path)
			}
		}
		stFile := st.Files[fp.Path]
		if stFile == nil {
			stFile = map[string]fieldState{}
			st.Files[fp.Path] = stFile
		}
		edits, err := syncFileTarget(fp, &target, stFile)
		if err != nil {
			return err
		}
		target.Format = string(normalizeFormat(fp.Format))
		ap.Files[fp.Path] = target
		writes = append(writes, fileEdit{plan: fp, edits: edits})
	}

	// 计划里不再出现的文件：恢复原值并清掉记录。
	for _, path := range slices.Sorted(maps.Keys(ap.Files)) {
		if planned[path] {
			continue
		}
		target := ap.Files[path]
		format := Format(target.Format)
		edits, err := restoreFields(format, path, &target, st.Files[path], nil)
		if err != nil {
			return err
		}
		writes = append(writes, fileEdit{
			plan:    FilePlan{Path: path, Format: format},
			edits:   edits,
			created: target.Created,
		})
		delete(ap.Files, path)
		delete(st.Files, path)
		// 这份文件退出计划时走的就是字段级恢复，原文快照留着只会被下一次
		// 接管误用。
		delete(st.Orig, path)
	}

	paths := []string{appliedPath(dataDir, agent), stashPath(dataDir, agent)}
	for _, w := range writes {
		paths = append(paths, w.plan.Path)
	}
	return Atomically(func() error {
		// stash 先落盘：配置文件写入失败时用户仍能按原值恢复。
		if err := saveStash(dataDir, agent, st); err != nil {
			return err
		}
		for _, w := range writes {
			w.plan.Fields = w.edits
			if err := writeFileFields(w.plan); err != nil {
				return err
			}
			if normalizeFormat(w.plan.Format) == FormatTOML {
				continue
			}
			// 记下写完后整份内容的规范化文本，断开时用它判断文件有没有被
			// 别人改过（排版变化不算）。
			t, ok := ap.Files[w.plan.Path]
			if !ok {
				continue
			}
			now, err := Read(w.plan.Path)
			if err != nil {
				return fmt.Errorf("magpiecfg: 读取 %s: %w", w.plan.Path, err)
			}
			if text, ok := canonicalDocumentText(now); ok {
				t.Written = text
				ap.Files[w.plan.Path] = t
			}
		}
		for _, w := range writes {
			if !w.created {
				continue
			}
			now, err := Read(w.plan.Path)
			if err != nil {
				return fmt.Errorf("magpiecfg: 读取 %s: %w", w.plan.Path, err)
			}
			if !emptyDocument(w.plan.Format, now) {
				continue
			}
			if err := os.Remove(w.plan.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("magpiecfg: 删除 %s: %w", w.plan.Path, err)
			}
		}
		return saveApplied(dataDir, agent, ap)
	}, paths...)
}

// Connect 把 p 的文件写进配置，并在 stash 里留下写入前的原值。
//
// 已有 applied 记录、且当前值仍是我们上次写入的值时，不覆盖 stash：那说明这是重写，
// 不是新接管，原值还是更早那次 connect 之前的。计划里任一配置文件无法解析
// 时直接报错，什么都不写；已经不在计划里的文件按 stash 恢复。
func Connect(dataDir string, p Plan) error {
	if err := validatePlan(p); err != nil {
		return err
	}
	for _, fp := range p.Files {
		raw, _, err := readConfig(fp.Path)
		if err != nil {
			return err
		}
		if err := checkConfig(fp.Format, fp.Path, raw); err != nil {
			return err
		}
	}
	stash, err := loadStash(dataDir, p.Agent)
	if err != nil {
		return err
	}
	applied, err := loadApplied(dataDir, p.Agent)
	if err != nil {
		return err
	}
	applied.Kept = false
	return syncFiles(dataDir, p.Agent, applied, stash, p.Files)
}

// Drift 检查配置文件里的值是否仍是我们写入的。它只读，不写任何东西。
// 多个文件时 Drifted 用「文件名: 键」区分来源，文件按路径排序。
func Drift(dataDir, agent string) Status {
	applied, err := loadApplied(dataDir, agent)
	if err != nil {
		return Status{State: StatusError, Err: err.Error()}
	}
	if len(applied.Files) == 0 {
		return Status{State: StatusNone}
	}
	paths := slices.Sorted(maps.Keys(applied.Files))
	status := Status{Path: paths[0]}
	if applied.Kept {
		status.State = StatusKept
		return status
	}
	missing := false
	for _, path := range paths {
		target := applied.Files[path]
		format := Format(target.Format)
		raw, exists, err := readConfig(path)
		if err != nil {
			status.State, status.Err = StatusError, err.Error()
			return status
		}
		if !exists {
			missing = true
			continue
		}
		if err := checkConfig(format, path, raw); err != nil {
			status.State, status.Err = StatusError, err.Error()
			return status
		}
		for _, key := range slices.Sorted(maps.Keys(target.Fields)) {
			if sameJSONValue(currentField(format, path, key), target.Fields[key]) {
				continue
			}
			label := key
			if len(paths) > 1 {
				label = filepath.Base(path) + ": " + key
			}
			status.Drifted = append(status.Drifted, label)
		}
	}
	if missing {
		status.State = StatusMissing
		return status
	}
	if len(status.Drifted) > 0 {
		status.State = StatusDrifted
		return status
	}
	status.State = StatusApplied
	return status
}

// Reapply 按 p 的字段重写配置文件（用户可能在设置页换了端点或模型）。
// 它不动 stash：用户手改过的值不能被当成原值。已有 Kept 标记时拒绝，
// 因为用户已经明确要求保留自己的修改。接管路径集合必须与计划完全一致，
// 增删文件走 Connect。
func Reapply(dataDir string, p Plan) error {
	if err := validatePlan(p); err != nil {
		return err
	}
	applied, err := loadApplied(dataDir, p.Agent)
	if err != nil {
		return err
	}
	switch {
	case len(applied.Files) == 0:
		return fmt.Errorf("magpiecfg: %s 尚未接管，不能重新写入", p.Agent)
	case applied.Kept:
		return fmt.Errorf("magpiecfg: %s 已保留用户修改，不能重新写入", p.Agent)
	case len(applied.Files) != len(p.Files):
		return fmt.Errorf("magpiecfg: %s 接管的文件与计划不一致", p.Agent)
	}
	for _, fp := range p.Files {
		target, ok := applied.Files[fp.Path]
		if !ok {
			return fmt.Errorf("magpiecfg: 接管的是别的文件，与计划里的 %s 不一致", fp.Path)
		}
		if normalizeFormat(Format(target.Format)) != normalizeFormat(fp.Format) {
			return fmt.Errorf("magpiecfg: %s 的接管格式与计划不一致", p.Agent)
		}
	}
	for _, fp := range p.Files {
		raw, _, err := readConfig(fp.Path)
		if err != nil {
			return err
		}
		if err := checkConfig(fp.Format, fp.Path, raw); err != nil {
			return err
		}
	}
	stash, err := loadStash(dataDir, p.Agent)
	if err != nil {
		return err
	}
	return syncFiles(dataDir, p.Agent, applied, stash, p.Files)
}

// Keep 把「保留我的修改」落盘：此后 Drift 返回 kept，Reapply 拒绝，
// Disconnect 不再改配置文件。stash 保留，用户仍可在之后明确恢复。
func Keep(dataDir, agent string) error {
	applied, err := loadApplied(dataDir, agent)
	if err != nil {
		return err
	}
	if len(applied.Files) == 0 {
		return nil
	}
	applied.Kept = true
	for path, target := range applied.Files {
		target.Fields = map[string]fieldState{}
		applied.Files[path] = target
	}
	return saveApplied(dataDir, agent, applied)
}

// emptyDocument 报告 raw 是否只是我们创建的文件被清空后的样子。JSON 沿用
// emptyJSONDocument 的判定；TOML 没有「空对象」的概念，去掉空白后为空才算。
func emptyDocument(format Format, raw []byte) bool {
	if normalizeFormat(format) == FormatTOML {
		return len(bytes.TrimSpace(raw)) == 0
	}
	return emptyJSONDocument(raw)
}

// emptyJSONDocument 报告 raw 是否只剩空对象/空数组（可以嵌套）。
//
// 不能只看是不是 "{}"：DelJSON 删掉一个对象的最后一个成员时只把该对象缩成
// "{}"，不会删掉父键，所以我们创建的文件恢复后会留下 {"env": {}} 这样的
// 空壳。带注释的文件保守保留——用户可能特意在里面写了说明。
func emptyJSONDocument(raw []byte) bool {
	b, ok := blankComments(raw)
	if !ok || !bytes.Equal(b, raw) {
		return false
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return false
	}
	obj, ok := v.(map[string]any)
	return ok && emptyJSONValue(obj)
}

// emptyJSONValue 报告 v 是否只是空对象/空数组的组合。
func emptyJSONValue(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for _, child := range v {
			if !emptyJSONValue(child) {
				return false
			}
		}
		return true
	case []any:
		for _, child := range v {
			if !emptyJSONValue(child) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Disconnect 恢复 connect 之前的配置并删除状态文件。
//
// 逐字段判断：当前值仍等于我们写入的值才用 stash 恢复，用户手改过的键保持
// 原样。Kept 状态下配置文件整体不动——用户已经选择保留自己的修改，再按
// stash 覆盖等于否定了这个选择；这时只清掉我们的记录。
func Disconnect(dataDir, agent string) error {
	applied, err := loadApplied(dataDir, agent)
	if err != nil {
		return err
	}
	if len(applied.Files) == 0 {
		return nil
	}
	if applied.Kept {
		return removeState(dataDir, agent)
	}
	stash, err := loadStash(dataDir, agent)
	if err != nil {
		return err
	}
	paths := []string{appliedPath(dataDir, agent), stashPath(dataDir, agent)}
	writes := make([]fileEdit, 0, len(applied.Files))
	for _, path := range slices.Sorted(maps.Keys(applied.Files)) {
		target := applied.Files[path]
		format := Format(target.Format)
		paths = append(paths, path)
		raw, exists, err := readConfig(path)
		if err != nil {
			return err
		}
		if !exists {
			// 用户已经把整个配置文件删了，没有可恢复的对象，只清记录。
			continue
		}
		if err := checkConfig(format, path, raw); err != nil {
			return err
		}
		// 文件自我们最后一次写入后没被改过（只改排版不算）：直接写回接管前
		// 的原文，键序、缩进和末尾换行都是用户原来的样子，字段级编辑做不到。
		if orig, ok := stash.Orig[path]; ok && target.Written != "" && normalizeFormat(format) != FormatTOML {
			if text, ok := canonicalDocumentText(raw); ok && text == target.Written {
				writes = append(writes, fileEdit{plan: FilePlan{Path: path, Format: format}, restore: orig})
				continue
			}
		}
		edits, err := restoreFields(format, path, &target, stash.Files[path], nil)
		if err != nil {
			return err
		}
		writes = append(writes, fileEdit{
			plan:    FilePlan{Path: path, Format: format},
			edits:   edits,
			created: target.Created,
		})
	}
	// 配置与两个状态文件一起放进 Atomically：任何一步失败都把配置和记录
	// 恢复到操作前，不出现「文件恢复了但记录没了」的半截状态。
	return Atomically(func() error {
		for _, w := range writes {
			if w.restore != "" {
				if err := WriteAtomic(w.plan.Path, []byte(w.restore)); err != nil {
					return fmt.Errorf("magpiecfg: 恢复 %s 的原文: %w", w.plan.Path, err)
				}
				continue
			}
			w.plan.Fields = w.edits
			if err := writeFileFields(w.plan); err != nil {
				return err
			}
			if !w.created {
				continue
			}
			now, err := Read(w.plan.Path)
			if err != nil {
				return fmt.Errorf("magpiecfg: 读取 %s: %w", w.plan.Path, err)
			}
			if emptyDocument(w.plan.Format, now) {
				if err := os.Remove(w.plan.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("magpiecfg: 删除 %s: %w", w.plan.Path, err)
				}
			}
		}
		for _, p := range []string{appliedPath(dataDir, agent), stashPath(dataDir, agent)} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("magpiecfg: 删除状态文件 %s: %w", p, err)
			}
		}
		_ = os.Remove(stateDir(dataDir, agent))
		return nil
	}, paths...)
}
