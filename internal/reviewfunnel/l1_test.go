package reviewfunnel

import (
	"strings"
	"testing"

	"mcode/internal/reviewcheck"
)

// change 按路径自动分类，构造 L1 改动记录。
func change(path string) Change {
	return Change{Path: path, Class: reviewcheck.Classify(path)}
}

// knownScope 构造一个 brief 已声明范围、没有硬越界的 Scope。
func knownScope(allowed ...string) reviewcheck.Scope {
	scope := reviewcheck.Scope{Allowed: map[string]struct{}{}, Known: true}
	for _, p := range allowed {
		scope.Allowed[p] = struct{}{}
	}
	return scope
}

func TestDecideL1PlanOnly(t *testing.T) {
	in := L1Input{
		Changes:  []Change{change("docs/superpowers/plans/2026-11-03-review-funnel.md")},
		Added:    200,
		Deleted:  10,
		MaxLines: 40,
	}
	decision, ok := DecideL1(in)
	if !ok {
		t.Fatalf("计划文档应自动通过，得到 %+v", decision)
	}
	if decision.Verdict != VerdictApprove {
		t.Fatalf("verdict = %q, want %q", decision.Verdict, VerdictApprove)
	}
	if !strings.Contains(decision.Reason, "计划文档") {
		t.Fatalf("reason 未说明命中的规则: %q", decision.Reason)
	}
}

func TestDecideL1DocsAndTests(t *testing.T) {
	tests := []struct {
		name    string
		changes []Change
		want    string
	}{
		{
			name:    "纯文档",
			changes: []Change{change("README.md"), change("docs/guide.md")},
			want:    "仅文档",
		},
		{
			name:    "纯测试",
			changes: []Change{change("internal/reviewfunnel/l1_test.go"), change("web/src/a.spec.ts")},
			want:    "仅测试",
		},
		{
			name:    "文档加测试",
			changes: []Change{change("README.md"), change("internal/reviewfunnel/l1_test.go")},
			want:    "文档与测试",
		},
		{
			name:    "计划加文档",
			changes: []Change{change("docs/superpowers/plans/x.md"), change("docs/notes.md")},
			want:    "仅文档",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := L1Input{Changes: tt.changes, Added: 500, Deleted: 200, MaxLines: 40}
			decision, ok := DecideL1(in)
			if !ok {
				t.Fatalf("%s 应自动通过，得到 %+v", tt.name, decision)
			}
			if !strings.Contains(decision.Reason, tt.want) {
				t.Fatalf("reason = %q, 期望包含 %q", decision.Reason, tt.want)
			}
		})
	}
}

func TestDecideL1LineThreshold(t *testing.T) {
	path := "internal/reviewfunnel/l1.go"
	base := L1Input{
		Changes:  []Change{change(path)},
		Scope:    knownScope(path),
		MaxLines: 40,
	}

	base.Added, base.Deleted = 30, 10
	decision, ok := DecideL1(base)
	if !ok {
		t.Fatalf("40 行且无硬越界应自动通过，得到 %+v", decision)
	}
	if !strings.Contains(decision.Reason, "小改动") {
		t.Fatalf("reason 未说明小改动规则: %q", decision.Reason)
	}

	base.Added, base.Deleted = 31, 10
	if decision, ok := DecideL1(base); ok {
		t.Fatalf("41 行不应自动通过，得到 %+v", decision)
	}

	base.Added, base.Deleted = 1, 1
	base.MaxLines = 0
	if decision, ok := DecideL1(base); ok {
		t.Fatalf("MaxLines=0 应关闭行数规则，得到 %+v", decision)
	}
}

func TestDecideL1UnknownScope(t *testing.T) {
	path := "internal/reviewfunnel/prompt.go"
	base := L1Input{
		Changes:  []Change{change(path)},
		Added:    1,
		Deleted:  1,
		MaxLines: 40,
	}

	base.Scope = reviewcheck.Scope{Known: false}
	if decision, ok := DecideL1(base); ok {
		t.Fatalf("Known=false 不应自动通过，得到 %+v", decision)
	}

	base.Scope = reviewcheck.Scope{
		Allowed: map[string]struct{}{path: {}},
		Known:   true,
		Hard:    []string{"internal/swarm/manager.go"},
	}
	if decision, ok := DecideL1(base); ok {
		t.Fatalf("存在硬越界时不应自动通过，得到 %+v", decision)
	}
}

func TestDecideL1SensitiveFileForcesL2(t *testing.T) {
	for _, path := range []string{"go.mod", "go.sum", ".github/workflows/ci.yml", "package-lock.json"} {
		in := L1Input{
			Changes:  []Change{change(path)},
			Added:    1,
			Deleted:  1,
			Scope:    knownScope(path),
			MaxLines: 40,
		}
		decision, ok := DecideL1(in)
		if ok {
			t.Fatalf("敏感文件 %s 不应自动通过，得到 %+v", path, decision)
		}
		if !strings.Contains(decision.Reason, "敏感") {
			t.Fatalf("reason = %q, 期望说明敏感文件", decision.Reason)
		}
	}
}

func TestDecideL1EmptyChanges(t *testing.T) {
	if decision, ok := DecideL1(L1Input{MaxLines: 40}); ok {
		t.Fatalf("空改动不应自动通过，得到 %+v", decision)
	}
}
