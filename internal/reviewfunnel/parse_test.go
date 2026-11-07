package reviewfunnel

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseBatchMissingIndex(t *testing.T) {
	text := `{"reviews":[{"index":0,"name":"alpha","verdict":"approve","reason":"ok","concerns":[]}]}`
	got, missing, err := ParseBatch(text, []int{0, 1})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(got) != 1 || got[0].Verdict != VerdictApprove {
		t.Fatalf("有效条目解析错误: %+v", got)
	}
	if !reflect.DeepEqual(missing, []int{1}) {
		t.Fatalf("missing = %v, want [1]", missing)
	}
}

func TestParseBatchRejectNeedsReason(t *testing.T) {
	text := `{"reviews":[{"index":0,"name":"alpha","verdict":"reject","reason":"   ","concerns":["internal/x.go:12 空指针"]}]}`
	got, missing, err := ParseBatch(text, []int{0})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空 reason 的 reject 不应生效: %+v", got)
	}
	if !reflect.DeepEqual(missing, []int{0}) {
		t.Fatalf("missing = %v, want [0]", missing)
	}
}

func TestParseBatchRejectNeedsPathLineConcern(t *testing.T) {
	bad := `{"reviews":[{"index":0,"name":"alpha","verdict":"reject","reason":"实现有问题","concerns":["感觉不太对"]}]}`
	got, missing, err := ParseBatch(bad, []int{0})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(got) != 0 || !reflect.DeepEqual(missing, []int{0}) {
		t.Fatalf("没有 path:line 的 reject 应视为缺失: got=%+v missing=%v", got, missing)
	}

	good := `{"reviews":[{"index":0,"name":"alpha","verdict":"reject","reason":"缺少边界检查","concerns":["internal/reviewfunnel/parse.go:12 数组越界"]}]}`
	got, missing, err = ParseBatch(good, []int{0})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("合法 reject 不应缺失: %v", missing)
	}
	if got[0].Verdict != VerdictReject || len(got[0].Concerns) != 1 {
		t.Fatalf("合法 reject 解析错误: %+v", got[0])
	}
}

func TestParseBatchSingleBadEntryDoesNotFailBatch(t *testing.T) {
	text := `{"reviews":[
		{"index":0,"name":"alpha","verdict":"approve","reason":"ok","concerns":[]},
		{"index":1,"name":"beta","verdict":"ok","reason":"???","concerns":[]},
		{"index":2,"name":"gamma","verdict":"escalate","reason":"需要人工确认","concerns":[]}
	]}`
	got, missing, err := ParseBatch(text, []int{0, 1, 2})
	if err != nil {
		t.Fatalf("单条损坏不应让整批失败: %v", err)
	}
	if len(got) != 2 || got[0].Verdict != VerdictApprove || got[2].Verdict != VerdictEscalate {
		t.Fatalf("有效条目解析错误: %+v", got)
	}
	if !reflect.DeepEqual(missing, []int{1}) {
		t.Fatalf("missing = %v, want [1]", missing)
	}
}

func TestParseBatchFencedJSON(t *testing.T) {
	text := "审查结果如下：\n```json\n" +
		`{"reviews":[{"index":2,"name":"gamma","verdict":"approve","reason":"ok","concerns":[]}]}` +
		"\n```\n以上。"
	got, missing, err := ParseBatch(text, []int{2})
	if err != nil {
		t.Fatalf("围栏 JSON 解析失败: %v", err)
	}
	if len(got) != 1 || got[2].Verdict != VerdictApprove {
		t.Fatalf("围栏 JSON 解析错误: %+v", got)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
}

func TestParseBatchDuplicateIndex(t *testing.T) {
	text := `{"reviews":[
		{"index":1,"name":"beta","verdict":"approve","reason":"ok","concerns":[]},
		{"index":1,"name":"beta-again","verdict":"reject","reason":"bad","concerns":["internal/x.go:1 boom"]}
	]}`
	got, missing, err := ParseBatch(text, []int{1})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("重复 index 应视为缺失: %+v", got)
	}
	if !reflect.DeepEqual(missing, []int{1}) {
		t.Fatalf("missing = %v, want [1]", missing)
	}
}

func TestParseBatchUnknownIndex(t *testing.T) {
	text := `{"reviews":[{"index":9,"name":"ghost","verdict":"approve","reason":"ok","concerns":[]}]}`
	got, missing, err := ParseBatch(text, []int{0})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	if len(got) != 0 || !reflect.DeepEqual(missing, []int{0}) {
		t.Fatalf("未知 index 不应进入结果: got=%+v missing=%v", got, missing)
	}
}

func TestParseBatchConcernsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"reviews":[{"index":0,"name":"alpha","verdict":"approve","reason":"ok","concerns":[`)
	for i := 0; i < 15; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q", fmt.Sprintf("%03d", i)+strings.Repeat("x", 340))
	}
	b.WriteString(`]}]}`)

	got, _, err := ParseBatch(b.String(), []int{0})
	if err != nil {
		t.Fatalf("ParseBatch 返回错误: %v", err)
	}
	concerns := got[0].Concerns
	if len(concerns) != 10 {
		t.Fatalf("concerns 数量 = %d, want 10", len(concerns))
	}
	for i, concern := range concerns {
		if n := utf8.RuneCountInString(concern); n != 300 {
			t.Fatalf("concern[%d] 长度 = %d, want 300", i, n)
		}
	}
}

func TestParseDecision(t *testing.T) {
	single := `{"verdict":"approve","reason":"看起来没问题","concerns":[]}`
	decision, ok := ParseDecision("noise before\n" + single + "\nnoise after")
	if !ok || decision.Verdict != VerdictApprove {
		t.Fatalf("单条结论解析失败: %+v ok=%v", decision, ok)
	}

	batch := `{"reviews":[{"index":4,"name":"delta","verdict":"reject","reason":"越界","concerns":["internal/x.go:9 未处理错误"]}]}`
	decision, ok = ParseDecision(batch)
	if !ok || decision.Verdict != VerdictReject {
		t.Fatalf("单条批量结论解析失败: %+v ok=%v", decision, ok)
	}

	if _, ok := ParseDecision(`{"verdict":"ok","reason":"x","concerns":[]}`); ok {
		t.Fatalf("非法 verdict 不应解析成功")
	}
	if _, ok := ParseDecision("没有 JSON"); ok {
		t.Fatalf("没有 JSON 不应解析成功")
	}
}

func TestParseBatchNoJSON(t *testing.T) {
	if _, _, err := ParseBatch("模型没有返回 JSON", []int{0}); err == nil {
		t.Fatalf("没有 JSON 时应返回错误")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"1234", 1},
		{"12345", 2},
		{strings.Repeat("x", 4096), 1024},
	}
	for _, tt := range tests {
		if got := EstimateTokens(tt.in); got != tt.want {
			t.Fatalf("EstimateTokens(%d bytes) = %d, want %d", len(tt.in), got, tt.want)
		}
	}
}
