package mcodeui

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type tokenCounts struct {
	Colors    int `json:"colors"`
	FontSizes int `json:"font_sizes"`
	Radii     int `json:"radii"`
	Paddings  int `json:"paddings"`
	Heights   int `json:"heights"`
	RawWhite  int `json:"raw_white"`
}

type tokenBaseline struct {
	Version     int                    `json:"version"`
	Files       map[string]tokenCounts `json:"files"`
	ExemptTotal int                    `json:"exempt_total"`
}

func tokenFileKey(name string) string { return "internal/ui/" + name }

func tokenDefinitionFile(name string) bool {
	return name == "theme.go" || name == "tokens.go" || name == "elevation.go"
}

func tokenLiteral(expr ast.Expr) (*ast.BasicLit, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if ok {
		return lit, true
	}
	if unary, ok := expr.(*ast.UnaryExpr); ok && (unary.Op == token.SUB || unary.Op == token.ADD) {
		lit, ok := unary.X.(*ast.BasicLit)
		if ok && (lit.Kind == token.INT || lit.Kind == token.FLOAT) {
			return &ast.BasicLit{Kind: lit.Kind, Value: unary.Op.String() + lit.Value}, true
		}
	}
	return nil, false
}

func nonzeroTokenLiteral(expr ast.Expr) (*ast.BasicLit, bool) {
	lit, ok := tokenLiteral(expr)
	if !ok {
		return nil, false
	}
	if lit.Kind == token.INT || lit.Kind == token.FLOAT {
		value := constant.MakeFromLiteral(strings.TrimLeft(lit.Value, "+-"), lit.Kind, 0)
		if value.Kind() != constant.Unknown && constant.Sign(value) == 0 {
			return lit, false
		}
	}
	return lit, true
}

// designTokenNames 是 tokens.go 中可直接用于样式调用的设计令牌。
// 令牌算术（如 Space1 / 2）算出来仍是写死的数值，扫描时与数字字面量同等对待。
var designTokenNames = map[string]bool{
	"TasksSidebarWidth": true, "TasksToolbarHeight": true, "TasksSearchWidth": true, "TasksSearchMinWidth": true,
	"KanbanColumnMinWidth": true, "KanbanDoneRailWidth": true, "KanbanEmptyDropMinHeight": true,
	"KanbanCardActionSlotWidth": true, "KanbanFocusRingWidth": true, "KanbanDragSourceOpacity": true,
	"KanbanBoardPaneMinWidth": true, "KanbanDetailMinWidth": true, "KanbanDetailDefaultWidth": true, "KanbanDetailMaxWidth": true,
	"Space0": true, "Space1": true, "Space2": true, "Space3": true, "Space4": true,
	"Space5": true, "Space6": true, "Space7": true, "Space8": true,
	"RadiusXS": true, "RadiusSM": true, "RadiusMD": true, "RadiusLG": true,
	"RadiusXL": true, "RadiusPill": true,
	"ControlXS": true, "ControlSM": true, "ControlMD": true, "ControlLG": true, "ControlKbd": true, "Hairline": true,
	"IconSM": true, "IconMD": true, "IconLG": true,
	"TypeCaption": true, "TypeFootnote": true, "TypeBody": true, "TypeProse": true,
	"TypeHeading": true, "TypeTitle": true, "TypePageTitle": true, "TypeCode": true,
}

// tokenArithmeticArg 报告实参是否为运算符两侧直接引用设计令牌的二元运算。
// 这类实参与数字字面量等价，必须计入棘轮，否则 Space1/4、Space2/2 能绕过守卫。
func tokenArithmeticArg(expr ast.Expr) bool {
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	return operandHasDesignToken(binary.X) || operandHasDesignToken(binary.Y)
}

func operandHasDesignToken(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return designTokenNames[e.Name]
	case *ast.ParenExpr:
		return operandHasDesignToken(e.X)
	case *ast.UnaryExpr:
		return operandHasDesignToken(e.X)
	case *ast.BinaryExpr:
		return operandHasDesignToken(e.X) || operandHasDesignToken(e.Y)
	}
	return false
}

func tokenCallName(call *ast.CallExpr) (string, bool) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "ui" && (fun.Sel.Name == "Hex" || fun.Sel.Name == "RGB" || fun.Sel.Name == "RGBA") {
			return "colors", true
		}
		return fun.Sel.Name, false
	case *ast.Ident:
		return fun.Name, false
	}
	return "", false
}

func scanDesignTokens(t *testing.T, dir string, baseline tokenBaseline) (tokenBaseline, []styleViolation) {
	t.Helper()
	result := tokenBaseline{Version: 1, Files: make(map[string]tokenCounts)}
	var violations []styleViolation
	eachProductionFile(t, dir, func(name string, file *ast.File, fset *token.FileSet) {
		if tokenDefinitionFile(name) {
			return
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", path, err)
		}
		comments, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析注释 %s: %v", path, err)
		}
		lines := strings.Split(string(src), "\n")
		exempt := make(map[int]bool)
		for _, group := range comments.Comments {
			for _, comment := range group.List {
				if !strings.HasPrefix(comment.Text, "// token-exempt:") {
					continue
				}
				// 两次解析使用相同源文件，注释偏移可直接映射到行号。
				offset := int(comment.Slash) - 1
				line := strings.Count(string(src[:offset]), "\n") + 1
				column := offset - strings.LastIndex(string(src[:offset]), "\n") - 1
				reason := strings.TrimSpace(strings.TrimPrefix(comment.Text, "// token-exempt:"))
				if reason == "" || strings.TrimSpace(lines[line-1][:column]) == "" {
					violations = append(violations, styleViolation{name, line, "token-exempt 必须位于代码行尾且原因非空"})
					continue
				}
				exempt[line] = true
				result.ExemptTotal++
			}
		}
		counts := tokenCounts{}
		_, existing := baseline.Files[tokenFileKey(name)]
		controls := strings.HasPrefix(name, "controls_")
		if controls {
			existing = false
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			line := fset.Position(call.Pos()).Line
			kind, color := tokenCallName(call)
			if color {
				hex, hexLiteral := uiHexLiteral(call)
				white := hexLiteral && (hex == "#ffffff" || hex == "#fff")
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && ((sel.Sel.Name == "RGB" && len(call.Args) == 3) || (sel.Sel.Name == "RGBA" && len(call.Args) == 4)) {
					white = true
					for _, arg := range call.Args[:3] {
						lit, ok := tokenLiteral(arg)
						if !ok || lit.Value != "255" {
							white = false
						}
					}
				}
				if white {
					counts.RawWhite++
					if !existing {
						violations = append(violations, styleViolation{name, line, "禁止原始白色"})
					}
				}
			}
			hasLiteral := false
			for _, arg := range call.Args {
				if _, ok := nonzeroTokenLiteral(arg); ok || tokenArithmeticArg(arg) {
					hasLiteral = true
				}
			}
			if len(call.Args) > 0 && !existing && (kind == "FontSize" || kind == "Radius") {
				if lit, ok := nonzeroTokenLiteral(call.Args[0]); ok {
					value, err := strconv.ParseFloat(lit.Value, 64)
					if err == nil && kind == "FontSize" && value < 10 {
						violations = append(violations, styleViolation{name, line, "字号小于 10"})
					}
					if err == nil && kind == "Radius" && value != 4 && value != 6 && value != 8 && value != 12 && value != 14 && value != 999 {
						violations = append(violations, styleViolation{name, line, "圆角不在允许值中"})
					}
				}
			}
			if exempt[line] {
				return true
			}
			counted := true
			switch {
			case color:
				counts.Colors++
			case kind == "FontSize" && hasLiteral:
				counts.FontSizes++
			case kind == "Radius" && hasLiteral:
				counts.Radii++
			case kind == "Padding" && hasLiteral:
				counts.Paddings++
			case kind == "Height" && hasLiteral:
				counts.Heights++
			default:
				counted = false
			}
			if counted && !existing {
				violations = append(violations, styleViolation{name, line, "新文件禁止设计字面量"})
			}
			return true
		})
		if !controls {
			result.Files[tokenFileKey(name)] = counts
		}
	})
	return result, violations
}

func loadTokenBaseline(t *testing.T, dir string) tokenBaseline {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "testdata", "design_token_baseline.json"))
	if err != nil {
		t.Fatalf("读取设计令牌基线: %v", err)
	}
	var baseline tokenBaseline
	if err := json.Unmarshal(data, &baseline); err != nil || baseline.Version != 1 || baseline.Files == nil {
		t.Fatalf("无效设计令牌基线: %v", err)
	}
	return baseline
}

func compareTokenBaseline(current, baseline tokenBaseline) []styleViolation {
	var violations []styleViolation
	for name, got := range current.Files {
		want, exists := baseline.Files[name]
		if !exists {
			continue
		}
		metrics := []struct {
			name string
			got  int
			want int
		}{{"colors", got.Colors, want.Colors}, {"font_sizes", got.FontSizes, want.FontSizes}, {"radii", got.Radii, want.Radii}, {"paddings", got.Paddings, want.Paddings}, {"heights", got.Heights, want.Heights}, {"raw_white", got.RawWhite, want.RawWhite}}
		for _, metric := range metrics {
			if metric.got != metric.want {
				message := fmt.Sprintf("%s 差值 %+d", metric.name, metric.got-metric.want)
				if metric.got < metric.want {
					message += "，请下调基线"
				}
				violations = append(violations, styleViolation{name, 1, message})
			}
		}
	}
	if current.ExemptTotal != baseline.ExemptTotal {
		message := fmt.Sprintf("exempt_total 差值 %+d", current.ExemptTotal-baseline.ExemptTotal)
		if current.ExemptTotal < baseline.ExemptTotal {
			message += "，请下调基线"
		}
		violations = append(violations, styleViolation{"testdata/design_token_baseline.json", 1, message})
	}
	return violations
}

func TestDesignTokenRatchet(t *testing.T) {
	if os.Getenv("UPDATE_DESIGN_TOKEN") == "1" {
		current, _ := scanDesignTokens(t, ".", tokenBaseline{})
		data, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("testdata/design_token_baseline.json", append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	baseline := loadTokenBaseline(t, ".")
	current, violations := scanDesignTokens(t, ".", baseline)
	for _, v := range append(violations, compareTokenBaseline(current, baseline)...) {
		t.Error(v)
	}
}

func TestNoTinyFontSizes(t *testing.T) {
	baseline := loadTokenBaseline(t, ".")
	_, violations := scanDesignTokens(t, ".", baseline)
	for _, v := range violations {
		if strings.Contains(v.msg, "字号小于 10") {
			t.Error(v)
		}
	}
}

func TestNoRawWhite(t *testing.T) {
	_, violations := scanDesignTokens(t, ".", loadTokenBaseline(t, "."))
	for _, v := range violations {
		if strings.Contains(v.msg, "禁止原始白色") {
			t.Error(v)
		}
	}
}

func TestTokenValuesMatchSpec(t *testing.T) {
	want := map[string]int{
		"Space0": 2, "Space1": 4, "Space2": 6, "Space3": 8, "Space4": 12, "Space5": 16, "Space6": 20, "Space7": 24, "Space8": 32,
		"RadiusXS": 4, "RadiusSM": 6, "RadiusMD": 8, "RadiusLG": 12, "RadiusXL": 14, "RadiusPill": 999,
		"ControlXS": 20, "ControlSM": 24, "ControlMD": 28, "ControlLG": 32, "ControlKbd": 18, "IconSM": 14, "IconMD": 16, "IconLG": 20, "RowHeight": 28, "RowHeightCompact": 24, "Hairline": 1,
		"TypeCaption": 11, "TypeFootnote": 12, "TypeBody": 13, "TypeProse": 14, "TypeHeading": 13, "TypeTitle": 15, "TypePageTitle": 17, "TypeCode": 12,
		"DurFast": 90, "DurSnap": 140, "DurBase": 200, "DurSheet": 280, "DurSlow": 420,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tokens.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				expected, required := want[name.Name]
				if !required {
					continue
				}
				seen[name.Name] = true
				if i >= len(value.Values) {
					t.Errorf("%s 缺少显式值", name.Name)
					continue
				}
				expr := value.Values[i]
				if strings.HasPrefix(name.Name, "Dur") {
					binary, ok := expr.(*ast.BinaryExpr)
					if !ok || binary.Op != token.MUL {
						t.Errorf("%s 必须以毫秒定义", name.Name)
						continue
					}
					unit, ok := binary.Y.(*ast.SelectorExpr)
					if !ok || unit.Sel.Name != "Millisecond" {
						t.Errorf("%s 必须以毫秒定义", name.Name)
						continue
					}
					pkg, ok := unit.X.(*ast.Ident)
					if !ok || pkg.Name != "time" {
						t.Errorf("%s 必须以 time.Millisecond 定义", name.Name)
						continue
					}
					expr = binary.X
				}
				lit, ok := tokenLiteral(expr)
				if !ok || lit.Value != strconv.Itoa(expected) {
					t.Errorf("%s 的值应为 %d", name.Name, expected)
				}
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("缺少令牌 %s", name)
		}
	}
}

func TestDesignTokenProbes(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		baseline tokenBaseline
		message  string
	}{
		{"颜色字面量", "package probe\nfunc f() { _ = ui.Hex(\"#FFFFFF\") }\n", tokenBaseline{}, "禁止原始白色"},
		{"小字号", "package probe\nfunc f() { box.FontSize(9) }\n", tokenBaseline{}, "字号小于 10"},
		{"新文件", "package probe\nfunc f() { box.Padding(12) }\n", tokenBaseline{}, "新文件禁止设计字面量"},
		{"空豁免原因", "package probe\nfunc f() { box.Height(20) } // token-exempt:   \n", tokenBaseline{}, "原因非空"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProbeFile(t, dir, "probe.go", tc.src)
			_, violations := scanDesignTokens(t, dir, tc.baseline)
			requireViolation(t, violations, "probe.go", 2, tc.message)
			if tc.name == "颜色字面量" {
				requireViolation(t, violations, "probe.go", 2, "新文件禁止设计字面量")
			}
		})
	}
	t.Run("零值不计入字面量", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { box.Padding(0, Space2); box.Radius(0.0); box.FontSize(0); box.Height(0) }\n")
		_, violations := scanDesignTokens(t, dir, tokenBaseline{})
		if len(violations) != 0 {
			t.Fatalf("零值不应报错: %v", violations)
		}
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { box.Padding(0, 6) }\n")
		_, violations = scanDesignTokens(t, dir, tokenBaseline{})
		requireViolation(t, violations, "probe.go", 2, "新文件禁止设计字面量")
	})
	t.Run("令牌算术计入字面量", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { box.Padding(Space1 / 2) }\n")
		current, violations := scanDesignTokens(t, dir, tokenBaseline{})
		if got := current.Files[tokenFileKey("probe.go")].Paddings; got != 1 {
			t.Fatalf("令牌算术应计入 paddings，实际 %d", got)
		}
		requireViolation(t, violations, "probe.go", 2, "新文件禁止设计字面量")
	})
	t.Run("单个令牌不计入字面量", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { box.Padding(Space3) }\n")
		current, violations := scanDesignTokens(t, dir, tokenBaseline{})
		if got := current.Files[tokenFileKey("probe.go")].Paddings; got != 0 {
			t.Fatalf("单个令牌不应计入 paddings，实际 %d", got)
		}
		if len(violations) != 0 {
			t.Fatalf("单个令牌不应报错: %v", violations)
		}
	})
	t.Run("基础组件不允许基线预算", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "controls_probe.go", "package probe\nfunc f() { box.Padding(6); box.FontSize(9); box.Radius(5); _ = ui.Hex(\"#fff\") }\n")
		baseline := tokenBaseline{Files: map[string]tokenCounts{tokenFileKey("controls_probe.go"): {Paddings: 1, FontSizes: 1, Radii: 1, Colors: 1, RawWhite: 1}}}
		current, violations := scanDesignTokens(t, dir, baseline)
		for _, message := range []string{"新文件禁止设计字面量", "字号小于 10", "圆角不在允许值中", "禁止原始白色"} {
			requireViolation(t, violations, "controls_probe.go", 2, message)
		}
		if _, ok := current.Files[tokenFileKey("controls_probe.go")]; ok {
			t.Fatal("基础组件不应写入基线")
		}
	})
	t.Run("原始白色棘轮", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { _ = ui.Hex(\"#FfF\"); _ = ui.RGBA(255, 255, 255, 0.5) }\n")
		baseline := tokenBaseline{Files: map[string]tokenCounts{tokenFileKey("probe.go"): {RawWhite: 1}}}
		current, violations := scanDesignTokens(t, dir, baseline)
		if len(violations) != 0 {
			t.Fatalf("存量文件不应直接报错: %v", violations)
		}
		requireViolation(t, compareTokenBaseline(current, baseline), tokenFileKey("probe.go"), 1, "raw_white 差值 +1")
	})
	t.Run("有效豁免不计数", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "probe.go", "package probe\nfunc f() { box.Height(20) } // token-exempt: 特殊高度\n")
		current, violations := scanDesignTokens(t, dir, tokenBaseline{})
		if len(violations) != 0 || current.ExemptTotal != 1 || current.Files[tokenFileKey("probe.go")].Heights != 0 {
			t.Fatalf("豁免统计错误: %+v %v", current, violations)
		}
	})
}
