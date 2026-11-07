package reviewcheck

import (
	"reflect"
	"testing"
)

// TestExtractPathsFromBackticksAndPlainText 验证反引号、裸路径和 :行号 的提取，
// 同时拒绝 URL、绝对路径和 ../ 越界路径。
func TestExtractPathsFromBackticksAndPlainText(t *testing.T) {
	brief := "先改 `internal/reviewcheck/scope.go` 和 `internal/reviewcheck/command.go:120`，" +
		"再补 docs/superpowers/plans/x.md。不要碰 https://example.com/a.go、/abs/b.go、../c.go。" +
		"参考 `internal/a.go internal/b.go`。"
	want := []string{
		"internal/reviewcheck/scope.go",
		"internal/reviewcheck/command.go",
		"internal/a.go",
		"internal/b.go",
		"docs/superpowers/plans/x.md",
	}
	if got := ExtractPaths(brief); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractPaths() = %#v，期望 %#v", got, want)
	}
}

// TestParseScopeHardAndSoft 验证允许集、目录前缀、WritePaths glob 和软硬越界。
func TestParseScopeHardAndSoft(t *testing.T) {
	brief := "只改 `internal/y.go` 和 `internal/swarm/`"
	changed := []string{
		"internal/y.go",
		"internal/swarm/manager.go",
		"internal/allowed/a.go",
		"cmd/tool/main.go",
		"internal/x.go",
		"internal/y_test.go",
		"docs/notes.md",
		"docs/superpowers/plans/x.md",
	}
	writePaths := []string{"internal/allowed/*.go", "cmd/tool/"}
	scope := ParseScope(brief, changed, writePaths)
	if !scope.Known {
		t.Fatal("Known = false，期望 true")
	}
	if got := scope.Hard; !reflect.DeepEqual(got, []string{"internal/x.go"}) {
		t.Errorf("Hard = %#v，期望仅 internal/x.go", got)
	}
	wantSoft := []string{"internal/y_test.go", "docs/notes.md", "docs/superpowers/plans/x.md"}
	if !reflect.DeepEqual(scope.Soft, wantSoft) {
		t.Errorf("Soft = %#v，期望 %#v", scope.Soft, wantSoft)
	}
	for _, key := range []string{"internal/y.go", "internal/swarm/", "internal/allowed/*.go", "cmd/tool/"} {
		if _, ok := scope.Allowed[key]; !ok {
			t.Errorf("Allowed 缺少 %q：%#v", key, scope.Allowed)
		}
	}
}

// TestParseScopeMissingBriefPaths 验证 brief 没有路径时 Known=false，
// 且硬编码改动仍会按分类进入 Soft/Hard。
func TestParseScopeMissingBriefPaths(t *testing.T) {
	scope := ParseScope("把这个问题修好，注意兼容旧数据", []string{"internal/x.go", "foo_test.go"}, nil)
	if scope.Known {
		t.Fatal("Known = true，期望 false")
	}
	if !reflect.DeepEqual(scope.Hard, []string{"internal/x.go"}) {
		t.Errorf("Hard = %#v，期望 internal/x.go", scope.Hard)
	}
	if !reflect.DeepEqual(scope.Soft, []string{"foo_test.go"}) {
		t.Errorf("Soft = %#v，期望 foo_test.go", scope.Soft)
	}
}

// TestParseScopePlanDocSoft 验证只改计划文档时算软越界而不是硬越界。
func TestParseScopePlanDocSoft(t *testing.T) {
	scope := ParseScope("更新计划 `docs/superpowers/plans/other.md`", []string{"docs/superpowers/plans/x.md"}, nil)
	if len(scope.Hard) != 0 {
		t.Fatalf("Hard = %#v，期望为空", scope.Hard)
	}
	if !reflect.DeepEqual(scope.Soft, []string{"docs/superpowers/plans/x.md"}) {
		t.Errorf("Soft = %#v，期望计划文档", scope.Soft)
	}
}
