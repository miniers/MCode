package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// beeWord 匹配界面文案里残留的小写单词 bee / bees。
var beeWord = regexp.MustCompile(`\b(?:bee|bees)\b`)

// swarmUITextLiteral 判断字面量是否是给用户看的文案：swarm-/bee- 开头的 key 不算。
func swarmUITextLiteral(v string) bool {
	if strings.HasPrefix(v, "swarm-") || strings.HasPrefix(v, "bee-") || strings.HasPrefix(v, "bee_") {
		return false
	}
	return true
}

// TestSwarmCopyUsesSubagent 扫描 swarm.go 的字符串字面量，界面文案不得出现小写 bee。
func TestSwarmCopyUsesSubagent(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "swarm.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 swarm.go: %v", err)
	}
	var offenders []string
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v := strings.Trim(lit.Value, "`\"")
		if !swarmUITextLiteral(v) || !beeWord.MatchString(v) {
			return true
		}
		offenders = append(offenders, fset.Position(lit.Pos()).String()+" "+v)
		return true
	})
	if len(offenders) > 0 {
		t.Errorf("界面文案仍在使用 bee，应改为「子代理」:\n%s", strings.Join(offenders, "\n"))
	}
}
