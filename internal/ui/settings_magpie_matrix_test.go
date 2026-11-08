package mcodeui

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// matrixTestState 构造已加载端点与各智能体设置的设置状态，避免矩阵数据触发异步加载。
func matrixTestState(t *testing.T, fake *FakeClient, gateways []CustomEndpoint, settings map[string]AgentMagpieSetting) *SettingsState {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	st.mu.Lock()
	st.endpoints.loaded = true
	st.endpoints.endpoints = append([]CustomEndpoint(nil), gateways...)
	st.piMagpie.loaded = true
	for _, agent := range magpieMatrixAgents {
		state := st.agentMagpieFor(agent)
		state.loaded = true
		state.setting = settings[agent]
	}
	st.mu.Unlock()
	return st
}

// matrixRows 拉平所有网关与家族的模型行，方便按模型 ID 查找。
func matrixRows(gateways []magpieMatrixGateway) []magpieMatrixModel {
	var rows []magpieMatrixModel
	for _, gateway := range gateways {
		for _, family := range gateway.families {
			rows = append(rows, family.models...)
		}
	}
	return rows
}

// findMatrixRow 返回指定模型 ID 的行。
func findMatrixRow(t *testing.T, gateways []magpieMatrixGateway, modelID string) magpieMatrixModel {
	t.Helper()
	for _, row := range matrixRows(gateways) {
		if row.model.ID == modelID {
			return row
		}
	}
	t.Fatalf("矩阵缺少模型 %s", modelID)
	return magpieMatrixModel{}
}

// TestMagpieMatrixData_SelectionPerAgent 覆盖：同一个模型在不同智能体列上的勾选互不影响。
func TestMagpieMatrixData_SelectionPerAgent(t *testing.T) {
	models := []PiMagpieModel{{ID: "model-a", Name: "模型 A"}, {ID: "model-b", Name: "模型 B"}}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models, Selected: []string{"model-a"}}}},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models, Selected: []string{"model-b"}}}},
	})
	data := magpieMatrixData(st, "")
	rowA := findMatrixRow(t, data, "model-a")
	if !rowA.selected["claude"] || rowA.selected["pi"] {
		t.Fatalf("模型 A 的勾选 = %+v", rowA.selected)
	}
	rowB := findMatrixRow(t, data, "model-b")
	if rowB.selected["claude"] || !rowB.selected["pi"] {
		t.Fatalf("模型 B 的勾选 = %+v", rowB.selected)
	}
}

// TestMagpieMatrixData_QueryFiltersModels 覆盖：搜索词只保留匹配的模型。
func TestMagpieMatrixData_QueryFiltersModels(t *testing.T) {
	models := []PiMagpieModel{{ID: "model-a", Name: "模型 A"}, {ID: "model-b", Name: "模型 B"}}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Models: models}}},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
	})
	data := magpieMatrixData(st, "模型 A")
	rows := matrixRows(data)
	if len(rows) != 1 || rows[0].model.ID != "model-a" {
		t.Fatalf("搜索后应只剩模型 A: %+v", rows)
	}
}

// TestMagpieMatrixData_SortByModelID 覆盖：同一家族内的模型按原始 ID 排序，与显示名无关。
func TestMagpieMatrixData_SortByModelID(t *testing.T) {
	models := []PiMagpieModel{
		{ID: "glm-5.3-flash", Name: "Zeta"},
		{ID: "glm-5.3-flash-free", Name: "Alpha"},
		{ID: "GLM-5.3-Flash-X", Name: "Mid"},
	}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Models: models}}},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
	})
	data := magpieMatrixData(st, "")
	if len(data) != 1 {
		t.Fatalf("网关数 = %d", len(data))
	}
	if len(data[0].families) != 1 {
		t.Fatalf("家族数 = %d", len(data[0].families))
	}
	rows := data[0].families[0].models
	want := []string{"glm-5.3-flash", "glm-5.3-flash-free", "GLM-5.3-Flash-X"}
	if len(rows) != len(want) {
		t.Fatalf("家族内模型数 = %d", len(rows))
	}
	for i, id := range want {
		if rows[i].model.ID != id {
			t.Fatalf("第 %d 行 ID = %s，期望 %s", i, rows[i].model.ID, id)
		}
	}
}

// TestMagpieMatrixData_GatewayOrder 覆盖：网关按配置顺序返回，未配置的排在最后。
func TestMagpieMatrixData_GatewayOrder(t *testing.T) {
	fake := NewFakeClient()
	gateways := []CustomEndpoint{
		{ID: "gw-z", Name: "乙网关", Magpie: true, Enabled: true, HasAPIKey: true},
		{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true},
	}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{
			{ID: "gw-z", Models: []PiMagpieModel{{ID: "model-z", Name: "模型 Z"}}},
			{ID: "gw-a", Models: []PiMagpieModel{{ID: "model-a", Name: "模型 A"}}},
			{ID: "gw-m", Models: []PiMagpieModel{{ID: "model-m", Name: "模型 M"}}},
		}},
		"codex": {Agent: "codex", Mode: "inject"},
		"pi":    {Agent: "pi", Mode: "inject"},
	})
	data := magpieMatrixData(st, "")
	if len(data) != 3 {
		t.Fatalf("网关数 = %d", len(data))
	}
	if data[0].id != "gw-z" || data[1].id != "gw-a" || data[2].id != "gw-m" {
		t.Fatalf("网关顺序 = %v", []string{data[0].id, data[1].id, data[2].id})
	}
	if data[0].name != "乙网关" || data[1].name != "甲网关" {
		t.Fatalf("网关名未按配置取: %q %q", data[0].name, data[1].name)
	}
}

// TestSaveMagpieMatrixFamily 覆盖：批量全选一个家族只发一次保存，并保留原有勾选。
func TestSaveMagpieMatrixFamily(t *testing.T) {
	models := []PiMagpieModel{
		{ID: "claude-sonnet", Name: "Claude Sonnet"},
		{ID: "claude-opus", Name: "Claude Opus"},
		{ID: "gpt-5", Name: "GPT-5"},
	}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Models: models, Selected: []string{"claude-sonnet"}}}},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
	})
	data := magpieMatrixData(st, "")
	if len(data) != 1 {
		t.Fatalf("网关数 = %d", len(data))
	}
	var family magpieMatrixFamily
	for _, candidate := range data[0].families {
		if candidate.title == "Claude" {
			family = candidate
			break
		}
	}
	if family.title != "Claude" || len(family.models) != 2 {
		t.Fatalf("Claude 家族 = %+v", family)
	}
	saveMagpieMatrixFamily(st, "claude", data[0], family, true, "")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.SetAgentMagpieModelsCalls()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fake.SetAgentMagpieModelsCalls()
	if len(calls) != 1 {
		t.Fatalf("保存调用次数 = %d: %+v", len(calls), calls)
	}
	if calls[0].Agent != "claude" || calls[0].EndpointID != "gw-a" {
		t.Fatalf("保存参数 = %+v", calls[0])
	}
	got := make(map[string]bool)
	for _, id := range calls[0].ModelIDs {
		got[id] = true
	}
	if len(got) != 2 || !got["claude-sonnet"] || !got["claude-opus"] || got["gpt-5"] {
		t.Fatalf("保存的模型 = %v", calls[0].ModelIDs)
	}
}

// TestRenderMagpieModelMatrix_GatewayName 覆盖：矩阵只有一个网关时不显示网关名，多个时显示。
func TestRenderMagpieModelMatrix_GatewayName(t *testing.T) {
	models := []PiMagpieModel{{ID: "model-a", Name: "模型 A"}}
	render := func(t *testing.T, gateways []CustomEndpoint) *ui.Tester {
		t.Helper()
		fake := NewFakeClient()
		st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
			"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: gateways[0].ID, Models: models}}},
			"codex":  {Agent: "codex", Mode: "inject"},
			"pi":     {Agent: "pi", Mode: "inject"},
		})
		if len(gateways) > 1 {
			st.mu.Lock()
			st.agentMagpie["claude"].setting.Endpoints = append(st.agentMagpie["claude"].setting.Endpoints, PiMagpieEndpoint{ID: gateways[1].ID, Models: models})
			st.mu.Unlock()
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
		}, 1000, 2000)
		tt.Frame()
		return tt
	}
	single := render(t, []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}})
	if countExactText(single.Texts(), "甲网关") != 0 {
		t.Fatalf("单网关不应显示网关名: %v", single.Texts())
	}
	multiple := render(t, []CustomEndpoint{
		{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true},
		{ID: "gw-b", Name: "乙网关", Magpie: true, Enabled: true, HasAPIKey: true},
	})
	if countExactText(multiple.Texts(), "甲网关") != 1 || countExactText(multiple.Texts(), "乙网关") != 1 {
		t.Fatalf("多网关应各显示一次网关名: %v", multiple.Texts())
	}
}

// TestMagpieMatrixModelIDLabel 覆盖：名称与 ID 不同才显示原始 ID，名称回退成 ID 时不重复。
func TestMagpieMatrixModelIDLabel(t *testing.T) {
	cases := []struct {
		name  string
		model PiMagpieModel
		want  string
	}{
		{"名称与 ID 不同", PiMagpieModel{Name: "Claude Sonnet 5.5", ID: "auto/claude-sonnet-5-5"}, "auto/claude-sonnet-5-5"},
		{"名称为空", PiMagpieModel{ID: "auto/claude-sonnet-5-5"}, ""},
		{"名称等于 ID", PiMagpieModel{Name: "auto/claude-sonnet-5-5", ID: "auto/claude-sonnet-5-5"}, ""},
		{"ID 为空", PiMagpieModel{Name: "Claude Sonnet 5.5"}, ""},
	}
	for _, tc := range cases {
		if got := magpieMatrixModelIDLabel(tc.model); got != tc.want {
			t.Errorf("%s: magpieMatrixModelIDLabel(%+v) = %q，期望 %q", tc.name, tc.model, got, tc.want)
		}
	}
}

// TestRenderMagpieModelMatrix_ModelID 覆盖：名称后显示原始 ID，名称等于 ID 时只显示一次。
func TestRenderMagpieModelMatrix_ModelID(t *testing.T) {
	models := []PiMagpieModel{
		{ID: "auto/claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
		{ID: "same-model", Name: "same-model"},
	}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Models: models}}},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
	})
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 2000)
	tt.Frame()
	texts := tt.Texts()
	if countExactText(texts, "Claude Sonnet 5.5") != 1 {
		t.Fatalf("模型名应显示一次: %v", texts)
	}
	if countExactText(texts, "auto/claude-sonnet-5-5") != 1 {
		t.Fatalf("原始 ID 应显示一次: %v", texts)
	}
	if countExactText(texts, "same-model") != 1 {
		t.Fatalf("名称等于 ID 时应只显示一次: %v", texts)
	}
}

// TestMagpieMatrixDefaultBadge 覆盖：多个智能体的默认模型合并成一个胶囊文案。
func TestMagpieMatrixDefaultBadge(t *testing.T) {
	cases := []struct {
		agents []string
		want   string
	}{
		{nil, ""},
		{[]string{"claude"}, "Claude 默认"},
		{[]string{"claude", "pi"}, "Claude · pi 默认"},
		{[]string{"pi", "pi"}, "pi 默认"},
		{[]string{"codex", "claude"}, "Codex · Claude 默认"},
		{[]string{"mcode"}, "mcode 默认"},
		{[]string{"claude", "mcode"}, "Claude · mcode 默认"},
	}
	for _, tc := range cases {
		if got := magpieMatrixDefaultBadge(tc.agents); got != tc.want {
			t.Errorf("magpieMatrixDefaultBadge(%v) = %q，期望 %q", tc.agents, got, tc.want)
		}
	}
}

// TestMagpieMatrixColumnGeometry 覆盖：表头与各列勾选框共用固定列宽且中心对齐，
// 「默认」胶囊不会改变勾选框位置。
func TestMagpieMatrixColumnGeometry(t *testing.T) {
	models := []PiMagpieModel{
		{ID: "gpt-default", Name: "默认模型"},
		{ID: "gpt-plain", Name: "普通模型"},
	}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	endpoints := func() []PiMagpieEndpoint {
		return []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models, Selected: []string{"gpt-default"}}}
	}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject", DefaultEndpointID: "gw-a", DefaultModel: "gpt-default", Endpoints: endpoints()},
		"codex":  {Agent: "codex", Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models}}},
		"pi":     {Agent: "pi", Mode: "inject", DefaultEndpointID: "gw-a", DefaultModel: "gpt-default", Endpoints: endpoints()},
		"mcode":  {Agent: "mcode", Mode: "off", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models}}},
	})
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 2000)
	tt.Frame()

	centers := make(map[string]float32, len(magpieMatrixAgents))
	for _, agent := range magpieMatrixAgents {
		rect, ok := tt.Find(magpieMatrixAgentTitle(agent))
		if !ok {
			t.Fatalf("缺少表头 %s", magpieMatrixAgentTitle(agent))
		}
		centers[agent] = rect.X + rect.W/2
	}
	for i := 1; i < len(magpieMatrixAgents); i++ {
		prev, cur := magpieMatrixAgents[i-1], magpieMatrixAgents[i]
		if gap := centers[cur] - centers[prev]; math.Abs(float64(gap-(magpieMatrixColWidth+Space2))) > 0.5 {
			t.Errorf("%s 与 %s 表头中心距 = %v，期望 %v", magpieMatrixAgentTitle(cur), magpieMatrixAgentTitle(prev), gap, magpieMatrixColWidth+Space2)
		}
	}
	for _, agent := range magpieMatrixAgents {
		for _, model := range models {
			rect, ok := tt.Find("切换 " + magpieMatrixAgentTitle(agent) + " " + model.Name)
			if !ok {
				t.Fatalf("缺少 %s 的勾选框 %s", magpieMatrixAgentTitle(agent), model.Name)
			}
			if center := rect.X + rect.W/2; math.Abs(float64(center-centers[agent])) > 0.5 {
				t.Errorf("%s %s 勾选框中心 = %v，表头中心 = %v", magpieMatrixAgentTitle(agent), model.Name, center, centers[agent])
			}
		}
	}
	if countExactText(tt.Texts(), "Claude · pi 默认") != 1 {
		t.Fatalf("多 agent 默认应合并成一个胶囊: %v", tt.Texts())
	}
	if countExactText(tt.Texts(), "Claude 默认") != 0 || countExactText(tt.Texts(), "pi 默认") != 0 {
		t.Fatalf("默认胶囊不应按 agent 拆分: %v", tt.Texts())
	}
}

// TestMagpieMatrixErrorLines 覆盖：相同错误按文字去重，只有部分 agent 出错时加 agent 名前缀。
func TestMagpieMatrixErrorLines(t *testing.T) {
	cases := []struct {
		name string
		errs map[string]string
		want []string
	}{
		{
			name: "全部相同",
			errs: map[string]string{"claude": "未设置 API Key", "codex": "未设置 API Key", "pi": "未设置 API Key", "mcode": "未设置 API Key"},
			want: []string{"未设置 API Key"},
		},
		{
			name: "部分相同",
			errs: map[string]string{"claude": "未设置 API Key", "codex": "连接超时", "pi": "未设置 API Key", "mcode": "连接超时"},
			want: []string{"Claude、pi：未设置 API Key", "Codex、mcode：连接超时"},
		},
		{
			name: "全部为空",
			errs: map[string]string{},
			want: nil,
		},
		{
			name: "单个 agent 出错",
			errs: map[string]string{"codex": "未设置 API Key"},
			want: []string{"Codex：未设置 API Key"},
		},
		{
			name: "空白文字按未出错处理",
			errs: map[string]string{"claude": "  ", "codex": "未设置 API Key"},
			want: []string{"Codex：未设置 API Key"},
		},
	}
	for _, tc := range cases {
		got := magpieMatrixErrorLines(magpieMatrixAgents, tc.errs)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: magpieMatrixErrorLines(%v) = %v，期望 %v", tc.name, tc.errs, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: 第 %d 行 = %q，期望 %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// TestRenderMagpieModelMatrix_DedupEndpointError 覆盖：各 agent 同一个端点错误只显示一行。
func TestRenderMagpieModelMatrix_DedupEndpointError(t *testing.T) {
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	settings := make(map[string]AgentMagpieSetting, len(magpieMatrixAgents))
	for _, agent := range magpieMatrixAgents {
		settings[agent] = AgentMagpieSetting{Agent: agent, Mode: "inject", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Err: "未设置 API Key"}}}
	}
	st := matrixTestState(t, fake, gateways, settings)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 2000)
	tt.Frame()
	if count := countExactText(tt.Texts(), "未设置 API Key"); count != 1 {
		t.Fatalf("相同错误应只显示一行，实际 %d 行: %v", count, tt.Texts())
	}
}

// TestMagpieInstalledAgents_EnabledProviders 覆盖关闭供应商和完整列顺序。
func TestMagpieInstalledAgents_EnabledProviders(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claudeEnabled bool
		piEnabled     bool
		want          []string
	}{
		{name: "关闭 pi", claudeEnabled: true, piEnabled: false, want: []string{"claude", "codex", "mcode"}},
		{name: "关闭 claude", claudeEnabled: false, piEnabled: true, want: []string{"codex", "pi", "mcode"}},
		{name: "全部启用", claudeEnabled: true, piEnabled: true, want: []string{"claude", "codex", "pi", "mcode"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := matrixTestState(t, NewFakeClient(), nil, nil)
			st.mu.Lock()
			st.cfg.Providers.Claude.Enabled = tc.claudeEnabled
			st.cfg.Providers.Codex.Enabled = true
			st.providerRows.settings = map[string]ProviderSetting{"pi": {ID: "pi", Enabled: tc.piEnabled}}
			st.mu.Unlock()
			if got := magpieInstalledAgents(st); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("智能体列 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestMagpieMatrixAgents_AlwaysIncludesMcode 覆盖：其它智能体都未安装时，内置引擎仍有自己的列。
func TestMagpieMatrixAgents_AlwaysIncludesMcode(t *testing.T) {
	models := []PiMagpieModel{{ID: "model-a", Name: "模型 A"}}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "off"},
		"codex":  {Agent: "codex", Mode: "off"},
		"pi":     {Agent: "pi", Mode: "off"},
		"mcode":  {Agent: "mcode", Mode: "off", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models}}},
	})
	st.mu.Lock()
	st.providerRows.available = map[string]bool{"claude": false, "codex": false, "pi": false}
	st.mu.Unlock()
	if agents := magpieInstalledAgents(st); len(agents) != 1 || agents[0] != magpieBuiltinAgent {
		t.Fatalf("未安装其它智能体时的列 = %v", agents)
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 2000)
	tt.Frame()
	if _, ok := tt.Find(magpieBuiltinAgent); !ok {
		t.Fatalf("缺少 mcode 表头: %v", tt.Texts())
	}
	if _, ok := tt.Find("切换 mcode 模型 A"); !ok {
		t.Fatalf("缺少 mcode 勾选框: %v", tt.Texts())
	}
	if countExactText(tt.Texts(), magpieAgentTitle("claude")) > 0 {
		t.Fatalf("未安装的 claude 不应有列: %v", tt.Texts())
	}
}

// TestMagpieMatrixMcode_OffModeStillCheckable 覆盖：mcode 没有模式，Mode 为 off 时勾选框仍可用，
// 勾选以 agent 为 mcode 保存。
func TestMagpieMatrixMcode_OffModeStillCheckable(t *testing.T) {
	models := []PiMagpieModel{{ID: "model-a", Name: "模型 A"}}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject"},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
		"mcode":  {Agent: "mcode", Mode: "off", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models}}},
	})
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 2000)
	tt.Frame()
	access, ok := tt.Access("切换 mcode 模型 A")
	if !ok {
		t.Fatalf("找不到 mcode 勾选框: %v", tt.Texts())
	}
	if access.Disabled {
		t.Fatal("Mode 为 off 时 mcode 勾选框不应禁用")
	}
	if err := tt.Click("切换 mcode 模型 A"); err != nil {
		t.Fatalf("点击 mcode 勾选框: %v", err)
	}
	var calls []setAgentMagpieModelsCall
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		calls = fake.SetAgentMagpieModelsCalls()
		if len(calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(calls) != 1 {
		t.Fatalf("保存调用次数 = %d: %+v", len(calls), calls)
	}
	if calls[0].Agent != magpieBuiltinAgent || calls[0].EndpointID != "gw-a" || len(calls[0].ModelIDs) != 1 || calls[0].ModelIDs[0] != "model-a" {
		t.Fatalf("保存参数 = %+v", calls[0])
	}
}

// TestSaveMagpieMatrixFamily_Mcode 覆盖：家族批量勾选对内置引擎生效。
func TestSaveMagpieMatrixFamily_Mcode(t *testing.T) {
	models := []PiMagpieModel{
		{ID: "claude-sonnet", Name: "Claude Sonnet"},
		{ID: "claude-opus", Name: "Claude Opus"},
		{ID: "gpt-5", Name: "GPT-5"},
	}
	fake := NewFakeClient()
	gateways := []CustomEndpoint{{ID: "gw-a", Name: "甲网关", Magpie: true, Enabled: true, HasAPIKey: true}}
	st := matrixTestState(t, fake, gateways, map[string]AgentMagpieSetting{
		"claude": {Agent: "claude", Mode: "inject"},
		"codex":  {Agent: "codex", Mode: "inject"},
		"pi":     {Agent: "pi", Mode: "inject"},
		"mcode":  {Agent: "mcode", Mode: "off", Endpoints: []PiMagpieEndpoint{{ID: "gw-a", Name: "甲网关", Models: models, Selected: []string{"claude-sonnet"}}}},
	})
	data := magpieMatrixData(st, "")
	if len(data) != 1 {
		t.Fatalf("网关数 = %d", len(data))
	}
	var family magpieMatrixFamily
	for _, candidate := range data[0].families {
		if candidate.title == "Claude" {
			family = candidate
			break
		}
	}
	if family.title != "Claude" || len(family.models) != 2 {
		t.Fatalf("Claude 家族 = %+v", family)
	}
	saveMagpieMatrixFamily(st, magpieBuiltinAgent, data[0], family, true, "")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.SetAgentMagpieModelsCalls()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fake.SetAgentMagpieModelsCalls()
	if len(calls) != 1 {
		t.Fatalf("保存调用次数 = %d: %+v", len(calls), calls)
	}
	if calls[0].Agent != magpieBuiltinAgent || calls[0].EndpointID != "gw-a" {
		t.Fatalf("保存参数 = %+v", calls[0])
	}
	got := make(map[string]bool)
	for _, id := range calls[0].ModelIDs {
		got[id] = true
	}
	if len(got) != 2 || !got["claude-sonnet"] || !got["claude-opus"] || got["gpt-5"] {
		t.Fatalf("保存的模型 = %v", calls[0].ModelIDs)
	}
}
