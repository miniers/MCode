package reviewcheck

import "testing"

// TestClassifyPlanDocBeforeDoc 验证计划文档的判定先于普通文档和其他分类。
func TestClassifyPlanDocBeforeDoc(t *testing.T) {
	cases := []struct {
		path string
		want Class
	}{
		{"docs/superpowers/plans/2026-11-03-review-funnel.md", ClassPlan},
		{"./docs/superpowers/plans/x.md", ClassPlan},
		{"docs/notes/x.md", ClassDoc},
		{"README.txt", ClassDoc},
		{"internal/reviewcheck/scope.go", ClassCode},
		{"internal/reviewcheck/scope_test.go", ClassTest},
		{"web/src/App.spec.tsx", ClassTest},
		{"internal/foo/testdata/x.go", ClassTest},
		{"", ClassOther},
	}
	for _, tc := range cases {
		if got := Classify(tc.path); got != tc.want {
			t.Errorf("Classify(%q) = %q，期望 %q", tc.path, got, tc.want)
		}
	}
}

// TestIsSensitive 验证依赖清单、锁文件和 .github/ 下的路径会被标记为敏感。
func TestIsSensitive(t *testing.T) {
	sensitive := []string{
		"go.mod",
		"internal/go.sum",
		"web/package.json",
		"web/package-lock.json",
		"web/pnpm-lock.yaml",
		"web/yarn.lock",
		"Cargo.lock",
		"Gemfile.lock",
		"poetry.lock",
		"GO.MOD",
		".github/workflows/ci.yml",
	}
	for _, path := range sensitive {
		if !IsSensitive(path) {
			t.Errorf("IsSensitive(%q) = false，期望 true", path)
		}
	}
	notSensitive := []string{
		"internal/go.mod.md",
		"docs/package.json.md",
		"internal/maingo.mod",
		"Makefile",
		"github/ci.yml",
		"",
	}
	for _, path := range notSensitive {
		if IsSensitive(path) {
			t.Errorf("IsSensitive(%q) = true，期望 false", path)
		}
	}
}

// TestIsPlanDoc 验证计划文档只认 docs/superpowers/plans/ 下的 .md。
func TestIsPlanDoc(t *testing.T) {
	if !IsPlanDoc("docs/superpowers/plans/a.md") {
		t.Fatal("计划文档未被识别")
	}
	for _, path := range []string{"docs/superpowers/plans/a.mdx", "docs/plans/a.md", "docs/superpowers/plans/a.go"} {
		if IsPlanDoc(path) {
			t.Errorf("IsPlanDoc(%q) = true，期望 false", path)
		}
	}
}
