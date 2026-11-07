package magpiecfg

import "testing"

func planField(plan Plan, path string) (Field, bool) {
	for _, f := range plan.Files[0].Fields {
		if f.Path == path {
			return f, true
		}
	}
	return Field{}, false
}

// claudeTestClaudeSelections 是三个 Claude 模型的勾选，第一个是主模型。
func claudeTestClaudeSelections() []ClaudeSelection {
	return []ClaudeSelection{
		{ID: "auto/claude-opus-5-5", Label: "Claude Opus 5.5"},
		{ID: "auto/claude-sonnet-5-5", Label: "Claude Sonnet 5.5"},
		{ID: "auto/claude-haiku-5-5", Label: "Claude Haiku 5.5"},
	}
}

func claudeTestOptions(selections ...ClaudeSelection) ClaudeOptions {
	if len(selections) == 0 {
		selections = claudeTestClaudeSelections()
	}
	return ClaudeOptions{
		BaseURL:    "https://magpie.test",
		APIKey:     "sk-test",
		Selections: selections,
	}
}

// claudePlanFieldString 断言字段存在、不是删除字段，并返回字符串值。
func claudePlanFieldString(t *testing.T, plan Plan, path string) string {
	t.Helper()
	f, ok := planField(plan, path)
	if !ok {
		t.Fatalf("%s: 缺少字段", path)
	}
	if f.Delete {
		t.Fatalf("%s: 不应该被删除", path)
	}
	s, ok := f.Value.(string)
	if !ok {
		t.Fatalf("%s: Value 类型 = %T，想要 string", path, f.Value)
	}
	return s
}

// claudePickerRows 取出计划里 modelPicker.options 的行，并断言不是删除字段。
func claudePickerRows(t *testing.T, plan Plan) []map[string]any {
	t.Helper()
	f, ok := planField(plan, "modelPicker")
	if !ok {
		t.Fatal("缺少 modelPicker 字段")
	}
	if f.Delete {
		t.Fatal("modelPicker 不应该被删除")
	}
	picker, ok := f.Value.(map[string]any)
	if !ok {
		t.Fatalf("modelPicker: Value 类型 = %T，想要 map[string]any", f.Value)
	}
	raw, ok := picker["options"].([]any)
	if !ok {
		t.Fatalf("modelPicker.options: 类型 = %T，想要 []any", picker["options"])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("modelPicker 行: 类型 = %T，想要 map[string]any", item)
		}
		rows = append(rows, row)
	}
	return rows
}

// claudePickerReplaces 报告计划里的 picker 是否带 replaceBuiltInOptions: true。
func claudePickerReplaces(t *testing.T, plan Plan) bool {
	t.Helper()
	f, _ := planField(plan, "modelPicker")
	picker, _ := f.Value.(map[string]any)
	replace, _ := picker["replaceBuiltInOptions"].(bool)
	return replace
}

func TestClaudePlanFields(t *testing.T) {
	_, settingsPath := claudeTestEnv(t)
	plan, err := ClaudePlan(claudeTestOptions())
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if plan.Agent != ClaudeAgent {
		t.Fatalf("Agent = %q，想要 %q", plan.Agent, ClaudeAgent)
	}
	if plan.Files[0].Path != settingsPath {
		t.Fatalf("Path = %q，想要 %q", plan.Files[0].Path, settingsPath)
	}
	want := map[string]string{
		"env.ANTHROPIC_BASE_URL":             "https://magpie.test",
		"env.ANTHROPIC_AUTH_TOKEN":           "sk-test",
		"env.ANTHROPIC_SMALL_FAST_MODEL":     "auto/claude-haiku-5-5",
		"model":                              "auto/claude-opus-5-5",
		"env.ANTHROPIC_DEFAULT_OPUS_MODEL":   "auto/claude-opus-5-5",
		"env.ANTHROPIC_DEFAULT_SONNET_MODEL": "auto/claude-sonnet-5-5",
		"env.ANTHROPIC_DEFAULT_HAIKU_MODEL":  "auto/claude-haiku-5-5",
		"env.NO_PROXY":                       "localhost,127.0.0.1,::1",
	}
	// 8 个赋值字段 + 2 个删除字段 + modelPicker。
	if len(plan.Files[0].Fields) != len(want)+3 {
		t.Fatalf("Fields 数量 = %d，想要 %d", len(plan.Files[0].Fields), len(want)+3)
	}
	for path, value := range want {
		if got := claudePlanFieldString(t, plan, path); got != value {
			t.Fatalf("%s = %q，想要 %q", path, got, value)
		}
	}
	for _, path := range []string{"env.ANTHROPIC_DEFAULT_FABLE_MODEL", "env.ANTHROPIC_MODEL"} {
		f, ok := planField(plan, path)
		if !ok || !f.Delete {
			t.Fatalf("%s 应该标记为删除，得到 %+v", path, f)
		}
	}

	rows := claudePickerRows(t, plan)
	if len(rows) != 3 {
		t.Fatalf("picker 行数 = %d，想要 3", len(rows))
	}
	wantRows := []struct{ model, label string }{
		{"auto/claude-opus-5-5", "Claude Opus 5.5"},
		{"auto/claude-sonnet-5-5", "Claude Sonnet 5.5"},
		{"auto/claude-haiku-5-5", "Claude Haiku 5.5"},
	}
	for i, wantRow := range wantRows {
		if rows[i]["model"] != wantRow.model {
			t.Fatalf("picker 第 %d 行 model = %v，想要 %q", i, rows[i]["model"], wantRow.model)
		}
		if rows[i]["label"] != wantRow.label {
			t.Fatalf("picker 第 %d 行 label = %v，想要 %q", i, rows[i]["label"], wantRow.label)
		}
		if rows[i]["description"] != claudePickerNote {
			t.Fatalf("picker 第 %d 行 description = %v，想要 %q", i, rows[i]["description"], claudePickerNote)
		}
	}
	if !claudePickerReplaces(t, plan) {
		t.Fatal("没有自定义选项时 picker 应该带 replaceBuiltInOptions: true")
	}
}

func TestClaudePlanSingleNonClaudeModel(t *testing.T) {
	claudeTestEnv(t)
	plan, err := ClaudePlan(claudeTestOptions(ClaudeSelection{ID: "auto/deepseek-v3-2"}))
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	for _, path := range []string{
		"model",
		"env.ANTHROPIC_SMALL_FAST_MODEL",
		"env.ANTHROPIC_DEFAULT_OPUS_MODEL",
		"env.ANTHROPIC_DEFAULT_SONNET_MODEL",
		"env.ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		if got := claudePlanFieldString(t, plan, path); got != "auto/deepseek-v3-2" {
			t.Fatalf("%s = %q，想要 %q", path, got, "auto/deepseek-v3-2")
		}
	}
	f, ok := planField(plan, "env.ANTHROPIC_DEFAULT_FABLE_MODEL")
	if !ok || !f.Delete {
		t.Fatalf("fable 档应该标记为删除，得到 %+v", f)
	}
	rows := claudePickerRows(t, plan)
	if len(rows) != 1 {
		t.Fatalf("picker 行数 = %d，想要 1", len(rows))
	}
	// 没有 Label 时丢掉路由前缀。
	if rows[0]["label"] != "deepseek-v3-2" {
		t.Fatalf("label = %v，想要 %q", rows[0]["label"], "deepseek-v3-2")
	}
	if rows[0]["model"] != "auto/deepseek-v3-2" {
		t.Fatalf("model = %v，想要 %q", rows[0]["model"], "auto/deepseek-v3-2")
	}
}

func TestClaudePlanOneM(t *testing.T) {
	claudeTestEnv(t)
	plan, err := ClaudePlan(claudeTestOptions(
		ClaudeSelection{ID: "auto/claude-opus-5-5", Label: "Opus", ContextWindow: 1_000_000},
		ClaudeSelection{ID: "auto/claude-sonnet-5-5", Label: "Sonnet", ContextWindow: 999_999},
	))
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if got := claudePlanFieldString(t, plan, "model"); got != "auto/claude-opus-5-5[1m]" {
		t.Fatalf("model = %q，想要 %q", got, "auto/claude-opus-5-5[1m]")
	}
	if got := claudePlanFieldString(t, plan, "env.ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "auto/claude-opus-5-5[1m]" {
		t.Fatalf("opus 档 = %q，想要 %q", got, "auto/claude-opus-5-5[1m]")
	}
	rows := claudePickerRows(t, plan)
	if len(rows) != 2 {
		t.Fatalf("picker 行数 = %d，想要 2", len(rows))
	}
	if rows[0]["model"] != "auto/claude-opus-5-5[1m]" {
		t.Fatalf("picker 第 0 行 model = %v，想要带 [1m]", rows[0]["model"])
	}
	if rows[1]["model"] != "auto/claude-sonnet-5-5" {
		t.Fatalf("picker 第 1 行 model = %v，不足 100 万上下文不应带 [1m]", rows[1]["model"])
	}
}

func TestClaudePlanSkipsEmptySelections(t *testing.T) {
	claudeTestEnv(t)
	plan, err := ClaudePlan(claudeTestOptions(
		ClaudeSelection{ID: "  "},
		ClaudeSelection{ID: "auto/claude-haiku-5-5"},
	))
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if got := claudePlanFieldString(t, plan, "model"); got != "auto/claude-haiku-5-5" {
		t.Fatalf("model = %q，想要 %q", got, "auto/claude-haiku-5-5")
	}
	if rows := claudePickerRows(t, plan); len(rows) != 1 {
		t.Fatalf("picker 行数 = %d，想要 1", len(rows))
	}
}

func TestClaudePlanCustomOptionKeepsBuiltInOptions(t *testing.T) {
	_, settingsPath := claudeTestEnv(t)
	writeSettings(t, settingsPath, `{"env":{"ANTHROPIC_CUSTOM_MODEL_OPTION":"my-own"}}`)
	plan, err := ClaudePlan(claudeTestOptions())
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if claudePickerReplaces(t, plan) {
		t.Fatal("用户设了 ANTHROPIC_CUSTOM_MODEL_OPTION 时不应写 replaceBuiltInOptions")
	}
}

func TestClaudePlanKeepsForeignPicker(t *testing.T) {
	_, settingsPath := claudeTestEnv(t)
	writeSettings(t, settingsPath, `{"modelPicker":{"options":[{"model":"mine","label":"Mine","description":"用户自己写的"}]}}`)
	plan, err := ClaudePlan(claudeTestOptions())
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	f, ok := planField(plan, "modelPicker")
	if !ok {
		t.Fatal("缺少 modelPicker 字段")
	}
	if !f.KeepForeign {
		t.Fatal("文件里已有非 magpie 的 picker 时应标记 KeepForeign")
	}
	if len(claudePickerRows(t, plan)) != 3 {
		t.Fatal("计划里的 picker 仍应该是本次的三个勾选模型")
	}
}

func TestClaudePlanTakesOverMagpiePicker(t *testing.T) {
	_, settingsPath := claudeTestEnv(t)
	writeSettings(t, settingsPath, `{"modelPicker":{"options":[
		{"model":"auto/claude-haiku-5-5","label":"旧 Haiku","description":"via magpie"}
	],"replaceBuiltInOptions":true}}`)
	plan, err := ClaudePlan(claudeTestOptions())
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	f, ok := planField(plan, "modelPicker")
	if !ok {
		t.Fatal("缺少 modelPicker 字段")
	}
	if f.KeepForeign {
		t.Fatal("magpie 写的 picker 应该被接管，不该标记 KeepForeign")
	}
	rows := claudePickerRows(t, plan)
	if len(rows) != 3 || rows[0]["label"] != "Claude Opus 5.5" {
		t.Fatalf("picker 应重写成本次勾选，得到 %+v", rows)
	}
}

func TestClaudePlanDeletesStaleKeys(t *testing.T) {
	t.Run("子代理指向网关模型", func(t *testing.T) {
		_, settingsPath := claudeTestEnv(t)
		writeSettings(t, settingsPath, `{"env":{
			"ANTHROPIC_MODEL":"old",
			"CLAUDE_CODE_SUBAGENT_MODEL":"auto/claude-sonnet-5-5:high",
			"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":"旧 Opus"
		}}`)
		opts := claudeTestOptions()
		opts.Served = []string{"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5"}
		plan, err := ClaudePlan(opts)
		if err != nil {
			t.Fatalf("ClaudePlan: %v", err)
		}
		for _, path := range []string{
			"env.ANTHROPIC_MODEL",
			"env.CLAUDE_CODE_SUBAGENT_MODEL",
			"env.ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		} {
			f, ok := planField(plan, path)
			if !ok || !f.Delete {
				t.Fatalf("%s 应该标记为删除，得到 %+v", path, f)
			}
		}
	})

	t.Run("子代理是用户自己的模型", func(t *testing.T) {
		_, settingsPath := claudeTestEnv(t)
		writeSettings(t, settingsPath, `{"env":{"CLAUDE_CODE_SUBAGENT_MODEL":"user/own-model"}}`)
		opts := claudeTestOptions()
		opts.Served = []string{"auto/claude-opus-5-5"}
		plan, err := ClaudePlan(opts)
		if err != nil {
			t.Fatalf("ClaudePlan: %v", err)
		}
		if _, ok := planField(plan, "env.CLAUDE_CODE_SUBAGENT_MODEL"); ok {
			t.Fatal("用户自己钉的子代理模型不应产生字段")
		}
	})
}

func TestClaudePlanSpellServed(t *testing.T) {
	cases := []struct {
		name   string
		served []string
		want   string
	}{
		{name: "网关只服务点号版，改写为横线", served: []string{"auto/claude-sonnet-4.5"}, want: "auto/claude-sonnet-4-5"},
		{name: "网关把横线版当作独立模型，保持点号", served: []string{"auto/claude-sonnet-4-5"}, want: "auto/claude-sonnet-4.5"},
		{name: "没有 served", served: nil, want: "auto/claude-sonnet-4.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claudeTestEnv(t)
			opts := claudeTestOptions(ClaudeSelection{ID: "auto/claude-sonnet-4.5"})
			opts.Served = tc.served
			plan, err := ClaudePlan(opts)
			if err != nil {
				t.Fatalf("ClaudePlan: %v", err)
			}
			if got := claudePlanFieldString(t, plan, "model"); got != tc.want {
				t.Fatalf("model = %q，想要 %q", got, tc.want)
			}
		})
	}
}

func TestClaudePlanNoProxyMerge(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "没有原值",
			content: `{}`,
			want:    "localhost,127.0.0.1,::1",
		},
		{
			name:    "已有用户值",
			content: `{"env":{"NO_PROXY":"foo"}}`,
			want:    "foo,localhost,127.0.0.1,::1",
		},
		{
			name:    "已含 localhost",
			content: `{"env":{"NO_PROXY":"localhost,foo"}}`,
			want:    "localhost,foo,127.0.0.1,::1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, settingsPath := claudeTestEnv(t)
			writeSettings(t, settingsPath, tc.content)
			plan, err := ClaudePlan(claudeTestOptions())
			if err != nil {
				t.Fatalf("ClaudePlan: %v", err)
			}
			f, ok := planField(plan, "env.NO_PROXY")
			if !ok {
				t.Fatal("缺少 env.NO_PROXY 字段")
			}
			if f.Value != tc.want {
				t.Fatalf("env.NO_PROXY = %q，想要 %q", f.Value, tc.want)
			}
		})
	}
}

func TestClaudePlanRejectsEmpty(t *testing.T) {
	claudeTestEnv(t)
	cases := []struct {
		name string
		opts ClaudeOptions
	}{
		{
			name: "空地址",
			opts: ClaudeOptions{APIKey: "sk-test", Selections: claudeTestClaudeSelections()},
		},
		{
			name: "空 key",
			opts: ClaudeOptions{BaseURL: "https://magpie.test", Selections: claudeTestClaudeSelections()},
		},
		{
			name: "空模型",
			opts: ClaudeOptions{BaseURL: "https://magpie.test", APIKey: "sk-test"},
		},
		{
			name: "只有空白",
			opts: ClaudeOptions{
				BaseURL:    "  ",
				APIKey:     " \t",
				Selections: []ClaudeSelection{{ID: "\n"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ClaudePlan(tc.opts); err == nil {
				t.Fatalf("ClaudePlan(%+v) 应该报错", tc.opts)
			}
		})
	}
}

// 用户指定的主模型在勾选里时，model 与档位都按它计算，picker 顺序保持不变。
func TestClaudePlanMainFromOption(t *testing.T) {
	claudeTestEnv(t)
	opts := claudeTestOptions()
	opts.Main = "auto/claude-sonnet-5-5"
	plan, err := ClaudePlan(opts)
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if got := claudePlanFieldString(t, plan, "model"); got != "auto/claude-sonnet-5-5" {
		t.Fatalf("model = %q，想要用户指定的 sonnet", got)
	}
	if got := claudePlanFieldString(t, plan, "env.ANTHROPIC_DEFAULT_SONNET_MODEL"); got != "auto/claude-sonnet-5-5" {
		t.Fatalf("sonnet 档 = %q，想要跟随主模型", got)
	}
	if got := claudePlanFieldString(t, plan, "env.ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "auto/claude-opus-5-5" {
		t.Fatalf("opus 档 = %q，想要勾选里的 opus", got)
	}
	rows := claudePickerRows(t, plan)
	if len(rows) != 3 || rows[0]["model"] != "auto/claude-opus-5-5" {
		t.Fatalf("picker 应保持勾选顺序，得到 %+v", rows)
	}
}

// 主模型不在清洗后的勾选里时回退到第一项。
func TestClaudePlanMainNotSelectedFallsBack(t *testing.T) {
	claudeTestEnv(t)
	opts := claudeTestOptions(
		ClaudeSelection{ID: "auto/claude-haiku-5-5"},
		ClaudeSelection{ID: "auto/claude-sonnet-5-5"},
	)
	opts.Main = " auto/claude-opus-5-5 "
	plan, err := ClaudePlan(opts)
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	if got := claudePlanFieldString(t, plan, "model"); got != "auto/claude-haiku-5-5" {
		t.Fatalf("model = %q，想要回退到第一项 haiku", got)
	}
}
