package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// settingsGuardFile 是一个参与护栏检查的设置源码文件。
type settingsGuardFile struct {
	name string
	file *ast.File
}

// parseSettingsGuardFiles 解析 internal/ui 下所有以 settings 开头的非测试源码文件。
func parseSettingsGuardFiles(t *testing.T) []settingsGuardFile {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 internal/ui 目录失败: %v", err)
	}

	fset := token.NewFileSet()
	var files []settingsGuardFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "settings") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		files = append(files, settingsGuardFile{name: name, file: file})
	}
	return files
}

// settingsGuardCounts 按文件统计违例条数，只保留大于 0 的文件。
func settingsGuardCounts(t *testing.T, count func(name string, file *ast.File) int) map[string]int {
	t.Helper()

	counts := map[string]int{}
	for _, f := range parseSettingsGuardFiles(t) {
		if n := count(f.name, f.file); n > 0 {
			counts[f.name] = n
		}
	}
	return counts
}

// checkSettingsGuardBaseline 用白名单锁定基线：实际条数只能减少，不能新增。
func checkSettingsGuardBaseline(t *testing.T, name string, got, allowed map[string]int) {
	t.Helper()

	seen := map[string]bool{}
	var files []string
	for file := range allowed {
		if !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	for file := range got {
		if !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	sort.Strings(files)

	for _, file := range files {
		n := got[file]
		m, ok := allowed[file]
		if !ok {
			if n > 0 {
				t.Errorf("%s: %s 新增违例：实际 %d，允许 0", name, file, n)
			}
			continue
		}
		if m == 0 {
			t.Errorf("%s: %s 的白名单条目为 0，请删除", name, file)
			continue
		}
		if n > m {
			t.Errorf("%s: %s 新增违例：实际 %d，允许 %d", name, file, n, m)
		}
		if n < m {
			t.Errorf("%s: %s 违例已减少到 %d，请把白名单下调到 %d", name, file, n, n)
		}
	}
}

// isSettingsPageFile 判断是否为页面文件（settings_page_*.go 或 settings_sheet_*.go）。
func isSettingsPageFile(name string) bool {
	return strings.HasPrefix(name, "settings_page_") || strings.HasPrefix(name, "settings_sheet_")
}

// settingsProseLiteral 取 ui.Text 的第二个实参：字符串字面量，或首个实参为字面量的 fmt.Sprintf。
func settingsProseLiteral(expr ast.Expr) (string, bool) {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	x, ok := fn.X.(*ast.Ident)
	if !ok || x.Name != "fmt" || fn.Sel.Name != "Sprintf" {
		return "", false
	}
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// settingsIsProse 判断字面量是否是说明性句子：带句读，或汉字超过 6 个。
func settingsIsProse(s string) bool {
	if strings.ContainsAny(s, "。？！") {
		return true
	}
	han := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			han++
		}
	}
	return han > 6
}

// countSettingsProseText 统计页面文件里说明性字面量的 ui.Text 调用。
func countSettingsProseText(name string, file *ast.File) int {
	if !isSettingsPageFile(name) {
		return 0
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Text" {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "ui" || len(call.Args) < 2 {
			return true
		}
		if s, ok := settingsProseLiteral(call.Args[1]); ok && settingsIsProse(s) {
			n++
		}
		return true
	})
	return n
}

// TestSettingsGuard_ProseText 统计说明性 ui.Text 字面量，移除阶段：P2、P3。
func TestSettingsGuard_ProseText(t *testing.T) {
	allowed := map[string]int{
		"settings_page_mcp.go":              1,
		"settings_page_archive.go":          1,
		"settings_page_swarm.go":            3,
		"settings_page_models.go":           1,
		"settings_page_mcp_catalog.go":      1,
		"settings_page_providers_agents.go": 1,
	}
	checkSettingsGuardBaseline(t, "ProseText", settingsGuardCounts(t, countSettingsProseText), allowed)
}

// countSettingsNumericSpacing 统计间距调用参数中的数字字面量，包括零。
func countSettingsNumericSpacing(_ string, file *ast.File) int {
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Padding" && sel.Sel.Name != "Gap" && sel.Sel.Name != "Margin") {
			return true
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(node ast.Node) bool {
				if lit, ok := node.(*ast.BasicLit); ok && (lit.Kind == token.INT || lit.Kind == token.FLOAT || lit.Kind == token.IMAG) {
					n++
				}
				return true
			})
		}
		return true
	})
	return n
}

// TestSettingsGuard_NoNumericSpacing 的现有基线只减不增。
func TestSettingsGuard_NoNumericSpacing(t *testing.T) {
	allowed := map[string]int{
		"settings.go":                       10,
		"settings_agy.go":                   12,
		"settings_archive_worktrees.go":     5,
		"settings_extra.go":                 2,
		"settings_magpie_hub.go":            1,
		"settings_mcp_controls.go":          11,
		"settings_page_archive.go":          3,
		"settings_page_mcp_catalog.go":      9,
		"settings_page_models.go":           11,
		"settings_page_providers_agents.go": 1,
		"settings_page_remote.go":           1,
		"settings_page_skills.go":           3,
		"settings_pi_magpie.go":             2,
		"settings_sheet_skills.go":          4,
		"settings_tree.go":                  7,
	}
	checkSettingsGuardBaseline(t, "NumericSpacing", settingsGuardCounts(t, countSettingsNumericSpacing), allowed)
}

// settingsRawControlNames 是只允许出现在 settings_controls.go 的四个原始控件。
var settingsRawControlNames = map[string]bool{
	"Switch":    true,
	"TextInput": true,
	"Slider":    true,
	"Segmented": true,
}

// countSettingsRawControls 统计 settings_controls.go 之外的原始控件调用。
func countSettingsRawControls(name string, file *ast.File) int {
	if name == "settings_controls.go" {
		return 0
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if ok && x.Name == "ui" && settingsRawControlNames[sel.Sel.Name] {
			n++
		}
		return true
	})
	return n
}

// TestSettingsGuard_RawControls 统计原始控件调用，移除阶段：P2 到 P5。
func TestSettingsGuard_RawControls(t *testing.T) {
	allowed := map[string]int{}
	checkSettingsGuardBaseline(t, "RawControls", settingsGuardCounts(t, countSettingsRawControls), allowed)
}

// countSettingsRenderPurity 统计页面文件里的 exec.、client.、os. 选择器与 go 语句。
func countSettingsRenderPurity(name string, file *ast.File) int {
	if !isSettingsPageFile(name) {
		return 0
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.SelectorExpr:
			if x, ok := v.X.(*ast.Ident); ok && (x.Name == "exec" || x.Name == "client" || x.Name == "os") {
				n++
			}
		case *ast.GoStmt:
			n++
		}
		return true
	})
	return n
}

// TestSettingsGuard_RenderPurity 统计渲染函数里的副作用，移除阶段：P5。
func TestSettingsGuard_RenderPurity(t *testing.T) {
	allowed := map[string]int{
		"settings_page_providers_agents.go": 18,
		"settings_page_archive.go":          13,
		"settings_page_models.go":           7,
		"settings_page_about.go":            5,
		"settings_page_mcp.go":              2,
		"settings_page_mcp_cache.go":        3,
		"settings_page_nowledge.go":         3,
		"settings_page_skills.go":           3,
		"settings_page_mcp_catalog.go":      2,
		"settings_page_open_in.go":          1,
	}
	checkSettingsGuardBaseline(t, "RenderPurity", settingsGuardCounts(t, countSettingsRenderPurity), allowed)
}

// countSettingsStateMounting 统计 map[*SettingsState] 类型与 ui.Local 调用。
func countSettingsStateMounting(_ string, file *ast.File) int {
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.MapType:
			if star, ok := v.Key.(*ast.StarExpr); ok {
				if x, ok := star.X.(*ast.Ident); ok && x.Name == "SettingsState" {
					n++
				}
			}
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "ui" && sel.Sel.Name == "Local" {
				n++
			}
		}
		return true
	})
	return n
}

// TestSettingsGuard_StateMounting 统计页面状态的旧挂载方式，移除阶段：P5。
func TestSettingsGuard_StateMounting(t *testing.T) {
	allowed := map[string]int{}
	checkSettingsGuardBaseline(t, "StateMounting", settingsGuardCounts(t, countSettingsStateMounting), allowed)
}

// countSettingsLockBranches 统计 settings.go、settings_tree.go 之外的 isLocked 调用。
func countSettingsLockBranches(name string, file *ast.File) int {
	if name == "settings.go" || name == "settings_tree.go" {
		return 0
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "isLocked" {
			n++
		}
		return true
	})
	return n
}

// TestSettingsGuard_LockBranches 统计页面里的锁定分支，移除阶段：P1 到 P3。
func TestSettingsGuard_LockBranches(t *testing.T) {
	allowed := map[string]int{
		"settings_page_providers_agents.go": 4, // 供应商面板入口和 agy 菜单栏额度行各算一次锁定，等 P5 由渲染器统一处理锁定后归零。
	}
	checkSettingsGuardBaseline(t, "LockBranches", settingsGuardCounts(t, countSettingsLockBranches), allowed)
}

// countSettingsSheetLink 统计面板文件里对 link 函数的调用。
func countSettingsSheetLink(name string, file *ast.File) int {
	if !strings.HasPrefix(name, "settings_sheet_") {
		return 0
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "link" {
			n++
		}
		return true
	})
	return n
}

// TestSettingsGuard_SheetNoLink 是永久规则：面板内不出现入口行，白名单保持为空。
func TestSettingsGuard_SheetNoLink(t *testing.T) {
	allowed := map[string]int{}
	checkSettingsGuardBaseline(t, "SheetNoLink", settingsGuardCounts(t, countSettingsSheetLink), allowed)
}

// settingsGuardSelfTestSource 是自检用的内存源码。
const settingsGuardSelfTestSource = `package mcodeui

func sample(c *ui.Context) {
	ui.Text(c, "这里是一段很长的说明文字。")
	ui.Switch(c, "x", true, nil)
	ui.Column(c).Padding(0, 1+2).Gap(Space2).Margin(0)
}
`

// TestSettingsGuard_DetectsViolation 确认计数函数能识别违例。
func TestSettingsGuard_DetectsViolation(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "settings_page_selftest.go", settingsGuardSelfTestSource, 0)
	if err != nil {
		t.Fatalf("解析自检源码失败: %v", err)
	}

	checks := []struct {
		name  string
		count func(name string, file *ast.File) int
	}{
		{"ProseText", countSettingsProseText},
		{"RawControls", countSettingsRawControls},
	}
	for _, check := range checks {
		if got := check.count("settings_page_selftest.go", file); got != 1 {
			t.Errorf("%s 自检计数为 %d，期望 1", check.name, got)
		}
	}
	if got := countSettingsNumericSpacing("settings_page_selftest.go", file); got != 4 {
		t.Errorf("NumericSpacing 自检计数为 %d，期望 4", got)
	}
}

// TestSettingsGuard_NoLegacyPages 冻结：旧行 helper 文件已删除，不得重新引入。
func TestSettingsGuard_NoLegacyPages(t *testing.T) {
	if _, err := os.Stat("settings_pages.go"); !os.IsNotExist(err) {
		t.Fatalf("internal/ui/settings_pages.go 已删除，不应重新引入（stat err = %v）", err)
	}
}
