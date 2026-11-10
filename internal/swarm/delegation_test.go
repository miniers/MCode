package swarm

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRosterNameAndIndex(t *testing.T) {
	// 基础索引与轮次
	if got := RosterName(0); got != "Aspen" {
		t.Errorf("expected Aspen, got %s", got)
	}
	if got := RosterName(24); got != "Zephyr" {
		t.Errorf("expected Zephyr, got %s", got)
	}
	if got := RosterName(25); got != "Aspen 2" {
		t.Errorf("expected Aspen 2, got %s", got)
	}
	if got := RosterName(29); got != "Ember 2" {
		t.Errorf("expected Ember 2, got %s", got)
	}

	// "dune" 大小写不敏感精确解析
	idx, ok := RosterIndex("dune")
	if !ok || idx != 3 {
		t.Errorf("expected (3, true) for 'dune', got (%d, %v)", idx, ok)
	}
	if got := RosterName(idx); got != "Dune" {
		t.Errorf("expected Dune, got %s", got)
	}

	// "Ember 2" 轮次解析
	idx, ok = RosterIndex("Ember 2")
	if !ok || idx != 4 {
		t.Errorf("expected (4, true) for 'Ember 2', got (%d, %v)", idx, ok)
	}
	if got := RosterName(idx); got != "Ember" {
		t.Errorf("expected Ember, got %s", got)
	}

	// 未知名字 "Ives"
	idx, ok = RosterIndex("Ives")
	if ok {
		t.Errorf("expected ok=false for 'Ives', got index %d", idx)
	}
}

func TestParseDelegations_NoBlock(t *testing.T) {
	text := "Here is the response without any delegation block."
	entries, found, err := ParseDelegations(text)
	if found {
		t.Errorf("expected found=false, got true")
	}
	if err != nil {
		t.Errorf("expected err=nil, got %v", err)
	}
	if entries != nil {
		t.Errorf("expected entries=nil, got %v", entries)
	}
}

func TestParseDelegations_EmptyArray(t *testing.T) {
	text := "I have completed all the work myself.\n```swarm\n[]\n```\nDone."
	entries, found, err := ParseDelegations(text)
	if !found {
		t.Errorf("expected found=true, got false")
	}
	if err != nil {
		t.Errorf("expected err=nil, got %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(entries))
	}
}

func TestParseDelegations_SingleEntryObject(t *testing.T) {
	text := `Prefix text
` + "```swarm\n" + `{
  "task": "Refactor store",
  "prompt": "Please refactor internal/store/swarm.go",
  "bee": "Dune",
  "project": "mcode"
}
` + "```\nSuffix text"

	entries, found, err := ParseDelegations(text)
	if !found {
		t.Fatalf("expected found=true")
	}
	if err != nil {
		t.Fatalf("expected err=nil, got %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	d := entries[0]
	if d.Task != "Refactor store" {
		t.Errorf("expected Task 'Refactor store', got %s", d.Task)
	}
	if d.Prompt != "Please refactor internal/store/swarm.go" {
		t.Errorf("expected Prompt 'Please refactor internal/store/swarm.go', got %s", d.Prompt)
	}
	if d.Name != "Dune" {
		t.Errorf("expected Name 'Dune', got %s", d.Name)
	}
	if d.Project != "mcode" {
		t.Errorf("expected Project 'mcode', got %s", d.Project)
	}
}

func TestParseDelegations_MultiEntryArray(t *testing.T) {
	text := "```swarm-sent\n" + `[
  {
    "bee": "Aspen",
    "prompt": "Fix bug in task",
    "repo": "repo-1"
  },
  {
    "bee": "Ember",
    "task": "Write docs",
    "prompt": "Document public APIs"
  }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "Aspen" || entries[0].Task != "Fix bug in task" || entries[0].Project != "repo-1" {
		t.Errorf("unexpected entry 0: %+v", entries[0])
	}
	if entries[1].Name != "Ember" || entries[1].Task != "Write docs" || entries[1].Prompt != "Document public APIs" {
		t.Errorf("unexpected entry 1: %+v", entries[1])
	}
}

func TestParseDelegations_NestedCodeBlocksInPrompt(t *testing.T) {
	text := "```swarm\n" + `[
  {
    "task": "Implement parser",
    "prompt": "Here is the target code:\n` + "```" + `go\nfunc hello() string { return \"world\" }\n` + "```" + `\nPlease test it."
  }
]
` + "```\n```text\nOther text"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Prompt, "func hello()") {
		t.Errorf("expected prompt to contain nested code block, got: %s", entries[0].Prompt)
	}
	if !strings.Contains(entries[0].Prompt, "```go") {
		t.Errorf("expected prompt to contain ```go, got: %s", entries[0].Prompt)
	}
}

func TestParseDelegations_BadJSON(t *testing.T) {
	text := "```swarm\n{ invalid json here\n```"
	entries, found, err := ParseDelegations(text)
	if !found {
		t.Errorf("expected found=true")
	}
	if entries != nil {
		t.Errorf("expected entries=nil")
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrBadDelegationBlock) {
		t.Errorf("expected ErrBadDelegationBlock, got %v", err)
	}
}

func TestParseDelegations_EmptyPromptFiltered(t *testing.T) {
	text := "```swarm\n" + `[
  { "prompt": "   ", "task": "empty" },
  { "prompt": "valid prompt", "task": "" }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("expected found=true, err=nil, got found=%v, err=%v", found, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 valid entry, got %d", len(entries))
	}
	if entries[0].Task != "valid prompt" {
		t.Errorf("expected task to fallback to prompt, got %s", entries[0].Task)
	}
}

func TestParseDelegations_AllEmptyPromptsReturnErr(t *testing.T) {
	text := "```swarm\n" + `[
  { "prompt": "   ", "task": "empty" }
]
` + "```"

	_, found, err := ParseDelegations(text)
	if !found {
		t.Errorf("expected found=true")
	}
	if err == nil || !errors.Is(err, ErrBadDelegationBlock) {
		t.Errorf("expected ErrBadDelegationBlock, got %v", err)
	}
}

func TestParseDelegations_ProfileAndPair(t *testing.T) {
	text := "```swarm\n" + `[
  {
    "task": "Routed work",
    "prompt": "Do the routed thing",
    "profile": "  quick  ",
    "pair": "Deep Pair"
  }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Profile != "quick" {
		t.Errorf("expected Profile 'quick', got %q", entries[0].Profile)
	}
	if entries[0].Pair != "Deep Pair" {
		t.Errorf("expected Pair 'Deep Pair', got %q", entries[0].Pair)
	}
}

// skills 数组形式、逗号字符串形式、去空去重、超 8 个截断、类型错误忽略、不写时为 nil。
func TestParseDelegations_Skills(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "数组形式",
			body: `{"prompt": "P", "skills": ["alpha", "beta"]}`,
			want: []string{"alpha", "beta"},
		},
		{
			name: "逗号字符串形式",
			body: `{"prompt": "P", "skills": " alpha , beta ,gamma "}`,
			want: []string{"alpha", "beta", "gamma"},
		},
		{
			name: "去空去重保序",
			body: `{"prompt": "P", "skills": ["  ", "Alpha", "", "alpha", " beta "]}`,
			want: []string{"Alpha", "beta"},
		},
		{
			name: "超过 8 个截断",
			body: `{"prompt": "P", "skills": ["s1","s2","s3","s4","s5","s6","s7","s8","s9","s10"]}`,
			want: []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"},
		},
		{
			name: "类型错误忽略",
			body: `{"prompt": "P", "skills": {"a": 1}}`,
			want: nil,
		},
		{
			name: "数组内非字符串项忽略",
			body: `{"prompt": "P", "skills": ["alpha", 3, null, "beta"]}`,
			want: []string{"alpha", "beta"},
		},
		{
			name: "不写 skills 时为 nil",
			body: `{"prompt": "P"}`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := "```swarm\n" + `[` + tc.body + `]` + "\n```"
			entries, found, err := ParseDelegations(text)
			if !found || err != nil {
				t.Fatalf("unexpected found=%v, err=%v", found, err)
			}
			if len(entries) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(entries))
			}
			if !reflect.DeepEqual(entries[0].Skills, tc.want) {
				t.Errorf("Skills = %#v, want %#v", entries[0].Skills, tc.want)
			}
		})
	}
}

// replaces 键用于声明要取代的在跑 bee：读取时 TrimSpace，不写时为空。
func TestParseDelegations_Replaces(t *testing.T) {
	text := "```swarm\n" + `[{"prompt": "P", "replaces": " Alice "}, {"prompt": "Q"}]` + "\n```"
	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Replaces != "Alice" {
		t.Errorf("Replaces = %q, want %q", entries[0].Replaces, "Alice")
	}
	if entries[1].Replaces != "" {
		t.Errorf("未写 replaces 时 Replaces = %q, want 空", entries[1].Replaces)
	}
}

// reworkOf / rework_of 键声明返工对象：两种写法都接受并 TrimSpace，不写时为空。
func TestParseDelegations_ReworkOf(t *testing.T) {
	text := "```swarm\n" + `[{"prompt": "P", "reworkOf": " Aspen "}, {"prompt": "Q", "rework_of": "bee-thread-7"}, {"prompt": "R"}]` + "\n```"
	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].ReworkOf != "Aspen" || entries[1].ReworkOf != "bee-thread-7" || entries[2].ReworkOf != "" {
		t.Fatalf("ReworkOf 解析错误: %q, %q, %q", entries[0].ReworkOf, entries[1].ReworkOf, entries[2].ReworkOf)
	}
}

// beePrompt 不参与技能拼接：不含 skills 的条目输出与以前逐字一致。
func TestBeePrompt_WithoutSkillsUnchanged(t *testing.T) {
	d := Delegation{Task: "T", Prompt: "P"}
	want := BeePromptPrefix + "Aspen" + "，由主代理派发，在独立工作副本中完成以下任务。\n\n## 任务：T\n\nP\n\n## 工作方式\n" + beeWorkRules + "\n\n## 汇报\n" + beeReportRules
	if got := beePrompt("Aspen", d); got != want {
		t.Errorf("beePrompt 输出变化：\ngot  %q\nwant %q", got, want)
	}

	// 即使声明了 skills，beePrompt 也不自行拼接技能块（由 manager 追加）。
	withSkills := Delegation{Task: "T", Prompt: "P", Skills: []string{"alpha"}}
	if got := beePrompt("Aspen", withSkills); got != want {
		t.Errorf("beePrompt 不应自行拼接技能块：\ngot  %q\nwant %q", got, want)
	}
}

func TestBeePrompt_ScopedSelfChecks(t *testing.T) {
	got := beePrompt("Aspen", Delegation{Task: "T", Prompt: "P"})
	for _, want := range []string{"有范围的自测", "-timeout", "跑了哪些自测命令及结果"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bee 提示词应包含 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "不运行构建、测试") {
		t.Fatalf("bee 提示词不应再禁止全部构建与测试:\n%s", got)
	}
}

func TestBeeWorkRules_NetworkResearch(t *testing.T) {
	for _, want := range []string{"自测不依赖网络", "web_fetch、web_search", "curl（必须带 -m 超时）", "只读不写", "不向外部服务提交、发布或上传内容", "不发送密钥或用户信息"} {
		if !strings.Contains(beeWorkRules, want) {
			t.Errorf("bee 工作规则应包含 %q", want)
		}
	}
	if strings.Contains(beeWorkRules, "不访问网络") {
		t.Error("bee 工作规则不应禁止访问网络")
	}
}

func TestStreamParser_EmitsOnClosingBrace(t *testing.T) {
	entry1 := `{"task": "A", "prompt": "do A"}`
	entry2 := `{"task": "B", "prompt": "do B"}`
	text := "```swarm\n[\n" + entry1 + ",\n" + entry2 + "\n]\n```\n"

	p := NewStreamParser()
	counts := make([]int, len(text))
	total := 0
	for i := 0; i < len(text); i++ {
		total += len(p.Feed(text[i : i+1]))
		counts[i] = total
	}

	firstClose := strings.Index(text, entry1) + len(entry1) - 1
	secondClose := strings.Index(text, entry2) + len(entry2) - 1
	if firstClose > 0 && counts[firstClose-1] != 0 {
		t.Errorf("expected no entry before the first closing brace, got %d", counts[firstClose-1])
	}
	if counts[firstClose] != 1 {
		t.Errorf("expected 1 entry when the first closing brace arrives, got %d", counts[firstClose])
	}
	if counts[secondClose] != 2 {
		t.Errorf("expected 2 entries when the second closing brace arrives, got %d", counts[secondClose])
	}
	if total != 2 {
		t.Errorf("expected 2 entries total, got %d", total)
	}
	if !p.Closed() {
		t.Errorf("expected parser closed after the closing fence")
	}
	if p.Emitted() != 2 {
		t.Errorf("expected Emitted()=2, got %d", p.Emitted())
	}
}

func TestStreamParser_HalfWrittenEmitsNothing(t *testing.T) {
	text := "```swarm\n[{\"task\": \"A\", \"prompt\": \"尚未写完\""
	p := NewStreamParser()
	for i := 0; i < len(text); i++ {
		if got := p.Feed(text[i : i+1]); len(got) != 0 {
			t.Fatalf("expected no entries for half-written JSON, got %+v", got)
		}
	}
	if p.Closed() {
		t.Errorf("expected parser not closed for half-written JSON")
	}
	if p.Emitted() != 0 {
		t.Errorf("expected Emitted()=0, got %d", p.Emitted())
	}
}

func TestStreamParser_TwoEntriesInOneChunkInOrder(t *testing.T) {
	text := "```swarm-sent\n" + `[
  {
    "bee": "Aspen",
    "prompt": "Fix bug in task",
    "repo": "repo-1"
  },
  {
    "bee": "Ember",
    "task": "Write docs",
    "prompt": "Document public APIs"
  }
]
` + "```"

	p := NewStreamParser()
	got := p.Feed(text)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Name != "Aspen" || got[0].Task != "Fix bug in task" || got[0].Project != "repo-1" {
		t.Errorf("unexpected entry 0: %+v", got[0])
	}
	if got[1].Name != "Ember" || got[1].Task != "Write docs" || got[1].Prompt != "Document public APIs" {
		t.Errorf("unexpected entry 1: %+v", got[1])
	}
	if !p.Closed() || p.Emitted() != 2 {
		t.Errorf("expected closed with 2 emitted, got closed=%v emitted=%d", p.Closed(), p.Emitted())
	}
}

func TestStreamParser_NestedAndQuotedBraces(t *testing.T) {
	raw, err := json.Marshal(map[string]string{
		"task":   "parser",
		"prompt": "看这段代码 {\"a\": {\"b\": 1}} 以及单独的右括号 } 和引号 \"x\"，还有行首围栏\n```go\nfunc f() { return }\n```",
	})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	text := "```swarm\n[" + string(raw) + "]\n```\n"

	// 分片喂入时边界会落在字符串、转义与大括号内部
	p := NewStreamParser()
	var got []Delegation
	for i := 0; i < len(text); i += 3 {
		end := i + 3
		if end > len(text) {
			end = len(text)
		}
		got = append(got, p.Feed(text[i:end])...)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Prompt, "func f() { return }") {
		t.Errorf("expected prompt to keep nested code block, got %q", got[0].Prompt)
	}
	if !strings.Contains(got[0].Prompt, "{\"a\": {\"b\": 1}}") {
		t.Errorf("expected prompt to keep nested braces, got %q", got[0].Prompt)
	}
}

func TestStreamParser_SingleBareObject(t *testing.T) {
	text := "```swarm\n" + `{
  "task": "Refactor store",
  "prompt": "Please refactor internal/store/swarm.go",
  "bee": "Dune",
  "project": "mcode"
}` + "\n```\n尾随文本"

	p := NewStreamParser()
	got := p.Feed(text)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	d := got[0]
	if d.Task != "Refactor store" || d.Name != "Dune" || d.Project != "mcode" {
		t.Errorf("unexpected entry: %+v", d)
	}
	if !p.Closed() {
		t.Errorf("expected parser closed after the bare object")
	}
}

func TestStreamParser_IgnoresTextAfterClosingFence(t *testing.T) {
	first := "```swarm\n[{\"prompt\": \"第一个块\"}]\n```\n"
	second := "\n```swarm\n[{\"prompt\": \"第二个块\"}]\n```\n"

	// 同一分片中带出第二个块：只产出第一个块的条目
	p := NewStreamParser()
	got := p.Feed(first + second)
	if len(got) != 1 || got[0].Prompt != "第一个块" {
		t.Fatalf("expected only the first block entry, got %+v", got)
	}
	if extra := p.Feed("\n```swarm\n[{\"prompt\": \"第三个块\"}]\n```"); len(extra) != 0 {
		t.Errorf("expected later text ignored after close, got %+v", extra)
	}
	if p.Emitted() != 1 {
		t.Errorf("expected Emitted()=1, got %d", p.Emitted())
	}
}

func TestStreamParser_MatchesParseDelegations(t *testing.T) {
	texts := []string{
		"```swarm\n[\n" +
			`{"task": "A", "prompt": "do A"}` + ",\n" +
			`{"task": "B", "prompt": "do B"}` + "\n]\n```\n",
		"```swarm-sent\n" + `[
  {
    "bee": "Aspen",
    "prompt": "Fix bug in task",
    "repo": "repo-1"
  },
  {
    "bee": "Ember",
    "task": "Write docs",
    "prompt": "Document public APIs"
  }
]
` + "```",
		"```swarm\n" + `[
  {
    "task": "Implement parser",
    "prompt": "Here is the target code:\n` + "```" + `go\nfunc hello() string { return \"world\" }\n` + "```" + `\nPlease test it."
  }
]
` + "```\n```text\nOther text",
		"```swarm\n" + `{
  "task": "Refactor store",
  "prompt": "Please refactor internal/store/swarm.go",
  "bee": "Dune",
  "project": "mcode"
}
` + "```\nSuffix text",
	}

	for i, text := range texts {
		want, found, err := ParseDelegations(text)
		if !found || err != nil {
			t.Fatalf("text %d: ParseDelegations found=%v err=%v", i, found, err)
		}

		p := NewStreamParser()
		var got []Delegation
		for start := 0; start < len(text); start += 7 {
			end := start + 7
			if end > len(text) {
				end = len(text)
			}
			got = append(got, p.Feed(text[start:end])...)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("text %d: streamed entries %+v do not match ParseDelegations %+v", i, got, want)
		}
	}
}

// TestParseDelegations_LegacyFenceAndBeeKey 验证旧围栏 ```hydra 与旧名字键 head 仍然能解析。
func TestParseDelegations_LegacyFenceAndBeeKey(t *testing.T) {
	text := "```hydra\n" + `[{"prompt": "旧围栏任务", "head": "Aspen"}]` + "\n```\n"
	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("旧围栏应仍能解析, found=%v err=%v", found, err)
	}
	if len(entries) != 1 || entries[0].Name != "Aspen" || entries[0].Prompt != "旧围栏任务" {
		t.Fatalf("旧键 head 未被识别: %+v", entries)
	}

	// 旧围栏的 -sent 变体同样兼容。
	if got, found, err := ParseDelegations("```hydra-sent\n" + `[{"prompt": "旧围栏任务"}]` + "\n```\n"); !found || err != nil || len(got) != 1 {
		t.Fatalf("旧围栏 -sent 变体应仍能解析: found=%v err=%v got=%+v", found, err, got)
	}
}

// TestParseDelegations_BeeKeyWins 验证 bee 与 bee 同时出现时以 bee 为准。
func TestParseDelegations_BeeKeyWins(t *testing.T) {
	text := "```swarm\n" + `[{"prompt": "任务", "bee": "Birch", "head": "Aspen"}]` + "\n```\n"
	entries, found, err := ParseDelegations(text)
	if !found || err != nil || len(entries) != 1 {
		t.Fatalf("解析失败: found=%v err=%v entries=%+v", found, err, entries)
	}
	if entries[0].Name != "Birch" {
		t.Fatalf("bee 应优先于 bee, 实际 Name=%q", entries[0].Name)
	}
}

// TestParseDelegations_RepairsInvalidEscape 验证提示词里写成字面反斜杠（如 .onnx）
// 造成的非法转义会被修复，且解码后仍是字面反斜杠。
func TestParseDelegations_RepairsInvalidEscape(t *testing.T) {
	text := "```swarm\n" + `[
  {
    "task": "Export model",
    "prompt": "第一行\n第二行\u4e2d 导出到 out\model.onnx 并检查 weights\data.bin"
  }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Prompt, `out\model.onnx`) {
		t.Errorf("expected literal backslash to survive, got: %s", entries[0].Prompt)
	}
	if !strings.Contains(entries[0].Prompt, `weights\data.bin`) {
		t.Errorf("expected literal backslash to survive, got: %s", entries[0].Prompt)
	}
	// 同一块里的合法转义仍按 JSON 规则解码。
	if !strings.Contains(entries[0].Prompt, "第一行\n第二行中") {
		t.Errorf("expected valid escapes to keep decoding, got: %q", entries[0].Prompt)
	}
}

// TestStreamParser_RepairsInvalidEscape 验证流式解析器逐字节喂入含非法转义的块时
// 仍能产出条目。
func TestStreamParser_RepairsInvalidEscape(t *testing.T) {
	entry := `{"task": "Export", "prompt": "保存到 out\model.onnx"}`
	text := "```swarm\n[\n" + entry + "\n]\n```\n"

	p := NewStreamParser()
	var got []Delegation
	for i := 0; i < len(text); i++ {
		got = append(got, p.Feed(text[i:i+1])...)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Prompt, `out\model.onnx`) {
		t.Errorf("expected literal backslash in prompt, got: %s", got[0].Prompt)
	}
	if !p.Closed() {
		t.Errorf("expected parser closed after the closing fence")
	}
}

// TestParseDelegations_KeepsValidEscapes 验证合法转义（\n、\t、\uXXXX）不受修复影响。
func TestParseDelegations_KeepsValidEscapes(t *testing.T) {
	text := "```swarm\n" + `[
  {
    "task": "Keep escapes",
    "prompt": "第一行\n第二行\t制表\u4e2d"
  }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found || err != nil {
		t.Fatalf("unexpected found=%v, err=%v", found, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := "第一行\n第二行\t制表中"
	if entries[0].Prompt != want {
		t.Errorf("expected %q, got %q", want, entries[0].Prompt)
	}
}

// TestParseDelegations_RepairKeepsStructuralErrors 验证修复不会掩盖缺逗号这类结构错误。
func TestParseDelegations_RepairKeepsStructuralErrors(t *testing.T) {
	text := "```swarm\n" + `[
  { "prompt": "a\nb" "task": "missing comma" }
]
` + "```"

	entries, found, err := ParseDelegations(text)
	if !found {
		t.Errorf("expected found=true")
	}
	if entries != nil {
		t.Errorf("expected entries=nil")
	}
	if err == nil || !errors.Is(err, ErrBadDelegationBlock) {
		t.Errorf("expected ErrBadDelegationBlock, got %v", err)
	}
}

// TestStreamParser_LegacyFence 验证流式解析器同样认旧围栏 ```hydra。
func TestStreamParser_LegacyFence(t *testing.T) {
	p := NewStreamParser()
	var got []Delegation
	chunk := "```hydra\n" + `[{"prompt": "流式旧围栏", "head": "Cove"}]` + "\n```\n"
	for start := 0; start < len(chunk); start += 5 {
		end := start + 5
		if end > len(chunk) {
			end = len(chunk)
		}
		got = append(got, p.Feed(chunk[start:end])...)
	}
	if len(got) != 1 || got[0].Name != "Cove" {
		t.Fatalf("流式旧围栏解析失败: %+v", got)
	}
	if !p.Closed() {
		t.Fatal("旧围栏块结束后应标记为已闭合")
	}
}
