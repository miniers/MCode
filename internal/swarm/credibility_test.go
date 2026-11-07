package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 验证正常代码引用的核验：双引号、反引号包裹的标记与片段均可核验成功。
func TestCheckCitations_Verified(t *testing.T) {
	dir := t.TempDir()
	content := `package example

func CalculateTotal() int {
	return 42
}
`
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	report := `在本次任务中：
- calc.go:3 "func CalculateTotal() int {"
- ` + "`calc.go:4` `return 42`"

	cred := CheckCitations(dir, report)
	if cred.Cited != 2 {
		t.Fatalf("期望 Cited 为 2，实际: %d", cred.Cited)
	}
	if cred.Verified != 2 {
		t.Fatalf("期望 Verified 为 2，实际: %d", cred.Verified)
	}
	if len(cred.Unverified) != 0 {
		t.Fatalf("期望无未核实项，实际: %v", cred.Unverified)
	}
}

// 验证空白不敏感匹配：代码中的缩进与空格差异不影响比对结果。
func TestCheckCitations_WhitespaceInsensitive(t *testing.T) {
	dir := t.TempDir()
	content := "package example\n\nfunc CalculateTotal(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	report := `- calc.go:3 "func   CalculateTotal ( a,   b int )   int {"`
	cred := CheckCitations(dir, report)
	if cred.Cited != 1 || cred.Verified != 1 {
		t.Fatalf("空白不敏感匹配失败: %+v", cred)
	}
}

// 验证行号偏差在 3 行以内核验成功，超出 3 行标记为不可核实。
func TestCheckCitations_LineOffsetWithin3(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"TARGET_SNIPPET_HERE", // line 10
		"line 11",
		"line 12",
		"line 13",
		"line 14",
		"line 15",
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	// 偏差为 -3（第 7 行）：窗口 [4..10] 包含第 10 行
	report1 := `- target.txt:7 "TARGET_SNIPPET_HERE"`
	cred1 := CheckCitations(dir, report1)
	if cred1.Cited != 1 || cred1.Verified != 1 {
		t.Fatalf("偏差 -3 应核验成功: %+v", cred1)
	}

	// 偏差为 +3（第 13 行）：窗口 [10..15] 包含第 10 行
	report2 := `- target.txt:13 "TARGET_SNIPPET_HERE"`
	cred2 := CheckCitations(dir, report2)
	if cred2.Cited != 1 || cred2.Verified != 1 {
		t.Fatalf("偏差 +3 应核验成功: %+v", cred2)
	}

	// 偏差为 -4（第 6 行）：窗口 [3..9] 不包含第 10 行
	report3 := `- target.txt:6 "TARGET_SNIPPET_HERE"`
	cred3 := CheckCitations(dir, report3)
	if cred3.Cited != 1 || cred3.Verified != 0 || len(cred3.Unverified) != 1 || cred3.Unverified[0] != "target.txt:6" {
		t.Fatalf("偏差 -4 应为不可核实: %+v", cred3)
	}

	// 偏差为 +4（第 14 行）：窗口 [11..15] 不包含第 10 行
	report4 := `- target.txt:14 "TARGET_SNIPPET_HERE"`
	cred4 := CheckCitations(dir, report4)
	if cred4.Cited != 1 || cred4.Verified != 0 || len(cred4.Unverified) != 1 || cred4.Unverified[0] != "target.txt:14" {
		t.Fatalf("偏差 +4 应为不可核实: %+v", cred4)
	}
}

// 验证错误的代码片段会被标记为不可核实。
func TestCheckCitations_WrongSnippetUnverified(t *testing.T) {
	dir := t.TempDir()
	content := "package example\n\nfunc Hello() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	report := `- hello.go:3 "func NonExistent()"`
	cred := CheckCitations(dir, report)
	if cred.Cited != 1 {
		t.Fatalf("期望 Cited 为 1，实际: %d", cred.Cited)
	}
	if cred.Verified != 0 {
		t.Fatalf("期望 Verified 为 0，实际: %d", cred.Verified)
	}
	if len(cred.Unverified) != 1 || cred.Unverified[0] != "hello.go:3" {
		t.Fatalf("期望 Unverified 为 ['hello.go:3']，实际: %v", cred.Unverified)
	}
}

// 验证引用不存在的文件标记为不可核实。
func TestCheckCitations_MissingFileUnverified(t *testing.T) {
	dir := t.TempDir()
	report := `- missing/file.go:10 "func Missing()"`
	cred := CheckCitations(dir, report)
	if cred.Cited != 1 || cred.Verified != 0 {
		t.Fatalf("期望不存在文件被计入但未核实: %+v", cred)
	}
	if len(cred.Unverified) != 1 || cred.Unverified[0] != "missing/file.go:10" {
		t.Fatalf("期望 Unverified 为 ['missing/file.go:10']，实际: %v", cred.Unverified)
	}
}

// 验证 '..' 越界路径与外部绝对路径被拒绝并标记为不可核实。
func TestCheckCitations_DotDotEscapeUnverified(t *testing.T) {
	dir := t.TempDir()
	report := `- ../escape.go:10 "func Escaped()"`
	cred := CheckCitations(dir, report)
	if cred.Cited != 1 || cred.Verified != 0 {
		t.Fatalf("期望 '..' 越界路径被标记为不可核实: %+v", cred)
	}
	if len(cred.Unverified) != 1 || cred.Unverified[0] != "../escape.go:10" {
		t.Fatalf("期望 Unverified 记录越界引用，实际: %v", cred.Unverified)
	}

	// 外部绝对路径
	reportAbs := `- /tmp/outside_repo.go:10 "func Outside()"`
	credAbs := CheckCitations(dir, reportAbs)
	if credAbs.Cited != 1 || credAbs.Verified != 0 {
		t.Fatalf("期望外部绝对路径被标记为不可核实: %+v", credAbs)
	}
	if len(credAbs.Unverified) != 1 || credAbs.Unverified[0] != "/tmp/outside_repo.go:10" {
		t.Fatalf("期望 Unverified 记录外部绝对路径，实际: %v", credAbs.Unverified)
	}
}

// 验证去空格后短于 4 个字符的代码片段被忽略（不计入 Cited）。
func TestCheckCitations_ShortSnippetIgnored(t *testing.T) {
	dir := t.TempDir()
	content := "package example\n\nfunc A() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "example.go"), []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	report := `短引用测试：
- example.go:3 "fun"
- ` + "`example.go:3` ` a b c `"

	cred := CheckCitations(dir, report)
	if cred.Cited != 0 || cred.Verified != 0 || len(cred.Unverified) != 0 {
		t.Fatalf("短于 4 字符的代码片段应被忽略，实际: %+v", cred)
	}
}

// 验证 Credibility.Summary 输出文本格式，包括至多列出 3 项不可核实引用的截断。
func TestCredibility_Summary(t *testing.T) {
	// Cited == 0
	c0 := Credibility{Cited: 0}
	if s := c0.Summary(); s != "" {
		t.Fatalf("Cited==0 时期望空字符串，实际: %q", s)
	}

	// 全部核验通过（未核对读取记录）
	cAll := Credibility{Cited: 3, Verified: 3}
	if s := cAll.Summary(); s != "引用 3/3 核实，读取记录不可用" {
		t.Fatalf("期望 '引用 3/3 核实，读取记录不可用'，实际: %q", s)
	}

	// 部分未核验（1项）
	cPart1 := Credibility{Cited: 3, Verified: 2, Unverified: []string{"calc.go:10"}}
	if s := cPart1.Summary(); s != "引用 2/3 核实，不可核实：calc.go:10，读取记录不可用" {
		t.Fatalf("期望 '引用 2/3 核实，不可核实：calc.go:10，读取记录不可用'，实际: %q", s)
	}

	// 超过 3 项未核验时截断前 3 项
	cPart4 := Credibility{
		Cited:      5,
		Verified:   1,
		Unverified: []string{"a.go:1", "b.go:2", "c.go:3", "d.go:4"},
	}
	expected := "引用 1/5 核实，不可核实：a.go:1, b.go:2, c.go:3，读取记录不可用"
	if s := cPart4.Summary(); s != expected {
		t.Fatalf("期望 %q，实际: %q", expected, s)
	}
}

// 验证 BuildReport 在有档位与无档位情况下输出的可信度行位置。
func TestBuildReport_CredibilityLine(t *testing.T) {
	// 场景 1：有档位时，可信度紧跟在档位行之后
	h1 := Bee{
		Name:        "Bee-1",
		Task:        "重构计算器",
		Status:      StatusCompleted,
		Tier:        "quick",
		Credibility: &Credibility{Cited: 2, Verified: 2},
	}
	report1 := BuildReport([]Bee{h1})
	expectedTierLine := "- 档位：quick"
	expectedCredLine := "- 可信度：引用 2/2 核实，读取记录不可用"
	if !strings.Contains(report1, expectedTierLine) || !strings.Contains(report1, expectedCredLine) {
		t.Fatalf("汇报中缺少档位或可信度行:\n%s", report1)
	}
	tierIdx := strings.Index(report1, expectedTierLine)
	credIdx := strings.Index(report1, expectedCredLine)
	if credIdx <= tierIdx {
		t.Fatalf("可信度行应位于档位行之后:\n%s", report1)
	}
	betweenTierAndCred := report1[tierIdx+len(expectedTierLine) : credIdx]
	if strings.TrimSpace(betweenTierAndCred) != "" {
		t.Fatalf("可信度行应紧跟档位行之后，中间存在其他内容: %q", betweenTierAndCred)
	}

	// 场景 2：无档位时，可信度紧跟在状态行（合并结果）之后
	h2 := Bee{
		Name:        "Bee-2",
		Task:        "修复 bug",
		Status:      StatusCompleted,
		Credibility: &Credibility{Cited: 2, Verified: 1, Unverified: []string{"calc.go:10"}},
	}
	report2 := BuildReport([]Bee{h2})
	if strings.Contains(report2, "- 档位：") {
		t.Fatalf("无档位 bee 不应输出档位行:\n%s", report2)
	}
	expectedStatusLine := "- 合并结果：未合并"
	expectedCredLine2 := "- 可信度：引用 1/2 核实，不可核实：calc.go:10，读取记录不可用"
	if !strings.Contains(report2, expectedStatusLine) || !strings.Contains(report2, expectedCredLine2) {
		t.Fatalf("汇报中缺少合并结果或可信度行:\n%s", report2)
	}
	statusIdx := strings.Index(report2, expectedStatusLine)
	credIdx2 := strings.Index(report2, expectedCredLine2)
	if credIdx2 <= statusIdx {
		t.Fatalf("可信度行应位于合并结果行之后:\n%s", report2)
	}
	betweenStatusAndCred := report2[statusIdx+len(expectedStatusLine) : credIdx2]
	if strings.TrimSpace(betweenStatusAndCred) != "" {
		t.Fatalf("可信度行应紧跟合并结果行之后，中间存在其他内容: %q", betweenStatusAndCred)
	}
}

// citationsCase 描述一个引用核验场景：先写入 files 目录树，再比对核验统计。
type citationsCase struct {
	name           string
	files          map[string]string
	report         string
	wantCited      int
	wantVerified   int
	wantUnverified []string
	wantPath       string
}

// writeTestFiles 在 root 下按斜杠相对路径写入测试文件。
func writeTestFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
	}
}

// TestCheckCitations_TableDriven 覆盖路径后缀解析与片段解析的各类真实汇报写法。
func TestCheckCitations_TableDriven(t *testing.T) {
	calcContent := "package pkg\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	subCalcContent := "package sub\n\nfunc Sub(a, b int) int {\n\treturn a - b\n}\n"
	traceContent := "package swarm\n\nfunc ApplyReadTrace(root string) {\n\tvar cred Credibility\n\tcred.ReadChecked = true\n}\n"

	sidebarLines := make([]string, 20)
	for i := range sidebarLines {
		sidebarLines[i] = fmt.Sprintf("// line %d", i+1)
	}
	sidebarLines[13] = "// RANGE_TARGET_LINE"
	sidebarContent := strings.Join(sidebarLines, "\n") + "\n"

	calcOnly := map[string]string{"pkg/calc.go": calcContent}
	traceOnly := map[string]string{"internal/swarm/trace.go": traceContent}

	cases := []citationsCase{
		{
			name:         "裸文件名唯一后缀匹配",
			files:        calcOnly,
			report:       "- calc.go:3 \"func Add(a, b int) int {\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "裸文件名多候选命中其一",
			files:        map[string]string{"pkg/calc.go": calcContent, "pkg/sub/calc.go": subCalcContent},
			report:       "- calc.go:3 \"func Sub(a, b int) int {\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:           "裸文件名多候选均不命中",
			files:          map[string]string{"pkg/calc.go": calcContent, "pkg/sub/calc.go": subCalcContent},
			report:         "- calc.go:3 \"func Mul(a, b int) int {\"\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"calc.go:3"},
			wantPath:       "pkg/calc.go",
		},
		{
			name:         "绝对路径指向 root 之外按后缀映射",
			files:        calcOnly,
			report:       "- /tmp/not-in-root/pkg/calc.go:4 \"return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "全角括号不污染路径",
			files:        calcOnly,
			report:       "- （calc.go:4） \"return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "范围引用窗口覆盖结束行",
			files:        map[string]string{"internal/ui/sidebar.go": sidebarContent},
			report:       "- `internal/ui/sidebar.go:10-12` \"RANGE_TARGET_LINE\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "internal/ui/sidebar.go",
		},
		{
			name:         "一行多条引用各取各的片段",
			files:        calcOnly,
			report:       "- 见 `calc.go:3` `func Add(a, b int) int {` 与 `calc.go:4` `return a + b`。\n",
			wantCited:    2,
			wantVerified: 2,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "相邻引用不吞掉后一个片段",
			files:        calcOnly,
			report:       "- `calc.go:3` `calc.go:4` `return a + b`\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "片段写在引用之前",
			files:        calcOnly,
			report:       "- \"return a + b\" 见 calc.go:4\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "含省略号的片段",
			files:        calcOnly,
			report:       "- calc.go:3 \"func Add... return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "跨两行的片段",
			files:        calcOnly,
			report:       "- calc.go:3 \"func Add(a, b int) int { return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "标识符匹配函数签名行",
			files:        traceOnly,
			report:       "- trace.go:3 \"签名 ApplyReadTrace(root string) {\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "internal/swarm/trace.go",
		},
		{
			name:           "中文散文片段不可核实",
			files:          traceOnly,
			report:         "- trace.go:3 \"这段逻辑负责把用户输入标准化\"\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"trace.go:3"},
			wantPath:       "internal/swarm/trace.go",
		},
		{
			name:         "重复引用只计一次",
			files:        calcOnly,
			report:       "- calc.go:4 \"return a + b\"\n- 再确认 calc.go:4 \"return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "噪声 token 不计入",
			files:        calcOnly,
			report:       "- 121/:129 和 part0.bin:76700175\n- calc.go:4 \"return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
		{
			name:           "行号超出文件范围",
			files:          calcOnly,
			report:         "- calc.go:99 \"return a + b\"\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"calc.go:99"},
			wantPath:       "pkg/calc.go",
		},
		{
			name: "右侧片段是下一条引用的路径 token 时回退左侧",
			// 右侧引号里是下一条引用的路径 token，不是本条的代码片段；应回退到左侧的真实片段。
			files:        calcOnly,
			report:       "- \"func Add(a, b int) int {\" calc.go:3 `internal/remote/capabilities.go` 见 calc.go:4 \"return a + b\"\n",
			wantCited:    2,
			wantVerified: 2,
			wantPath:     "pkg/calc.go",
		},
		{
			name:         "孤立文件名片段被丢弃",
			files:        calcOnly,
			report:       "- `ComposerQuotaSheet.tsx` 见 calc.go:4\n",
			wantCited:    0,
			wantVerified: 0,
		},
		{
			name:         "占位符片段被丢弃后不计 Cited",
			files:        calcOnly,
			report:       "- `calc.go:4` \"[...]\"\n",
			wantCited:    0,
			wantVerified: 0,
		},
		{
			name:         "垃圾片段后仍取到同段真实片段",
			files:        calcOnly,
			report:       "- calc.go:4 \"[...]\" `ComposerQuotaSheet.tsx` \"return a + b\"\n",
			wantCited:    1,
			wantVerified: 1,
			wantPath:     "pkg/calc.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestFiles(t, dir, tc.files)
			cred := CheckCitations(dir, tc.report)
			if cred.Cited != tc.wantCited {
				t.Fatalf("期望 Cited 为 %d，实际: %d（%+v）", tc.wantCited, cred.Cited, cred)
			}
			if cred.Verified != tc.wantVerified {
				t.Fatalf("期望 Verified 为 %d，实际: %d（%+v）", tc.wantVerified, cred.Verified, cred)
			}
			if len(cred.Unverified) != len(tc.wantUnverified) {
				t.Fatalf("期望 Unverified 为 %v，实际: %v", tc.wantUnverified, cred.Unverified)
			}
			for i, want := range tc.wantUnverified {
				if cred.Unverified[i] != want {
					t.Fatalf("Unverified[%d] 期望 %q，实际 %q", i, want, cred.Unverified[i])
				}
			}
			if tc.wantPath != "" {
				if len(cred.CitedPaths) == 0 || cred.CitedPaths[0] != tc.wantPath {
					t.Fatalf("期望 CitedPaths[0] 为 %q，实际: %v", tc.wantPath, cred.CitedPaths)
				}
			}
		})
	}
}

// TestCheckCitations_IdentifierFallbackTightened 验证标识符回退收紧：
// 单个短标识符不再单独让片段通过，整词匹配取代子串匹配，单个标识符需不短于 8 个字符。
func TestCheckCitations_IdentifierFallbackTightened(t *testing.T) {
	dir := t.TempDir()
	writeTestFiles(t, dir, map[string]string{
		"internal/ui/settings.go":          "package ui\n\nfunc NewSettingsState(dataDir string, lockedKeys map[string]bool) *SettingsState {\n\treturn &SettingsState{}\n}\n",
		"internal/ui/chat_header_tools.go": "package ui\n\nfunc (h *chatHeaderTools) view() {\n\tapps = append(apps, openInApp{Name: app.Name})\n}\n",
		"internal/swarm/trace.go":          "package swarm\n\nfunc ApplyReadTrace(root string) {\n\tvar cred Credibility\n\tcred.ReadChecked = true\n}\n",
		"internal/ui/settings_factory.go":  "package ui\n\nfunc NewSettingsStateFactory() *SettingsState {\n\treturn nil\n}\n",
	})

	cases := []struct {
		name           string
		report         string
		wantCited      int
		wantVerified   int
		wantUnverified []string
	}{
		{
			// 窗口里只有 func NewSettingsState(dataDir string ...) 的签名，早已不存在的判断语句不得靠单一标识符通过。
			name:           "单个短标识符不再让片段通过",
			report:         "- `internal/ui/settings.go:3` `if dataDir == \"\" { return }`\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"internal/ui/settings.go:3"},
		},
		{
			// 子串 open 不得命中 openInApp。
			name:           "短标识符不做子串匹配",
			report:         "- `internal/ui/chat_header_tools.go:4` `open -a`\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"internal/ui/chat_header_tools.go:4"},
		},
		{
			name:         "单个长标识符整词命中",
			report:       "- `internal/ui/settings.go:3` `构造 NewSettingsState 后返回设置状态`\n",
			wantCited:    1,
			wantVerified: 1,
		},
		{
			name:           "长标识符需整词命中",
			report:         "- `internal/ui/settings_factory.go:3` `构造 NewSettingsState 后返回设置状态`\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"internal/ui/settings_factory.go:3"},
		},
		{
			name:         "两个标识符整词均命中",
			report:       "- `internal/swarm/trace.go:3` `应用 ApplyReadTrace 与 Credibility 记录`\n",
			wantCited:    1,
			wantVerified: 1,
		},
		{
			name:           "两个标识符缺一不通过",
			report:         "- `internal/swarm/trace.go:3` `应用 ApplyReadTrace 与 NotifyBus 记录`\n",
			wantCited:      1,
			wantVerified:   0,
			wantUnverified: []string{"internal/swarm/trace.go:3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred := CheckCitations(dir, tc.report)
			if cred.Cited != tc.wantCited {
				t.Fatalf("期望 Cited 为 %d，实际: %d（%+v）", tc.wantCited, cred.Cited, cred)
			}
			if cred.Verified != tc.wantVerified {
				t.Fatalf("期望 Verified 为 %d，实际: %d（%+v）", tc.wantVerified, cred.Verified, cred)
			}
			if len(cred.Unverified) != len(tc.wantUnverified) {
				t.Fatalf("期望 Unverified 为 %v，实际: %v", tc.wantUnverified, cred.Unverified)
			}
			for i, want := range tc.wantUnverified {
				if cred.Unverified[i] != want {
					t.Fatalf("Unverified[%d] 期望 %q，实际 %q", i, want, cred.Unverified[i])
				}
			}
		})
	}
}

// TestCheckCitations_ResolvedPathFeedsReadTrace 验证 CitedPaths 记录解析后的相对路径，便于读取核对。
func TestCheckCitations_ResolvedPathFeedsReadTrace(t *testing.T) {
	dir := t.TempDir()
	writeTestFiles(t, dir, map[string]string{
		"pkg/calc.go": "package pkg\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	})

	cred := CheckCitations(dir, "- calc.go:4 \"return a + b\"\n")
	if cred.Cited != 1 || cred.Verified != 1 {
		t.Fatalf("期望引用核验通过: %+v", cred)
	}

	cred.ApplyReadTrace([]string{"pkg/calc.go"})
	if !cred.ReadChecked || len(cred.NotRead) != 0 {
		t.Fatalf("解析后的相对路径应命中读取记录，实际: %+v", cred)
	}
}
