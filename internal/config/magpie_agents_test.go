package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

// magpieAgentsTestConfig 构造一份带一个 Magpie 端点的配置，端点模型按参数给。
func magpieAgentsTestConfig(endpointID string, piModels []string) Config {
	cfg := Defaults()
	cfg.Providers.Custom = []CustomEndpointConfig{
		{ID: endpointID, Name: "Magpie JP", Magpie: true, Enabled: true, PiModels: piModels},
	}
	return cfg
}

// 旧配置只有 piModels：加载（Normalize）后并入 agents.pi，且 piModels 不被清空。
func TestNormalizeMagpieAgentsLegacyPiModels(t *testing.T) {
	cfg := magpieAgentsTestConfig("jp", []string{"cline/gpt-x", " cline/gpt-x ", "acme/a"})

	norm := Normalize(cfg)

	pi := norm.Providers.MagpieAgents[ProviderPi]
	if pi.Mode != MagpieModeInject {
		t.Errorf("pi 的 mode = %q，想要 inject", pi.Mode)
	}
	wantSel := []MagpieSelection{{EndpointID: "jp", Models: []string{"cline/gpt-x", "acme/a"}}}
	if !reflect.DeepEqual(pi.Selections, wantSel) {
		t.Errorf("pi 的勾选 = %+v，想要 %+v", pi.Selections, wantSel)
	}
	if got := norm.Providers.Custom[0].PiModels; !reflect.DeepEqual(got, []string{"cline/gpt-x", "acme/a"}) {
		t.Errorf("piModels 被改动 = %+v，想要去重后的原列表", got)
	}
}

// 用 SetMagpieAgentModels 改 pi 的勾选后，对应端点的 PiModels 同步。
func TestSetMagpieAgentModelsMirrorsPiModels(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a", "b"}))

	SetMagpieAgentModels(&cfg, ProviderPi, "jp", []string{" b ", "c", "c"})

	pi := cfg.Providers.MagpieAgents[ProviderPi]
	wantSel := []MagpieSelection{{EndpointID: "jp", Models: []string{"b", "c"}}}
	if !reflect.DeepEqual(pi.Selections, wantSel) {
		t.Errorf("pi 的勾选 = %+v，想要 %+v", pi.Selections, wantSel)
	}
	if got := cfg.Providers.Custom[0].PiModels; !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("piModels = %+v，想要 [b c]", got)
	}

	// 传空列表等于删除这条勾选：pi 没有勾选、也没有其它设置时整项消失，PiModels 一并清空。
	SetMagpieAgentModels(&cfg, ProviderPi, "jp", nil)
	if got := cfg.Providers.MagpieAgents[ProviderPi]; got.Mode != MagpieModeInject || len(got.Selections) != 0 {
		t.Errorf("删除勾选应保留 inject 模式，得到 %+v", got)
	}
	if got := cfg.Providers.Custom[0].PiModels; got != nil {
		t.Errorf("删除勾选后 piModels = %+v，想要 nil", got)
	}
}

// 手改 PiModels 再 Normalize：并入 agents.pi，已有模型在前。
func TestNormalizeMagpieAgentsPicksUpEditedPiModels(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a", "b"}))
	cfg.Providers.Custom[0].PiModels = []string{"c"}

	norm := Normalize(cfg)

	pi := norm.Providers.MagpieAgents[ProviderPi]
	wantSel := []MagpieSelection{{EndpointID: "jp", Models: []string{"a", "b", "c"}}}
	if !reflect.DeepEqual(pi.Selections, wantSel) {
		t.Errorf("pi 的勾选 = %+v，想要 %+v", pi.Selections, wantSel)
	}
	if got := norm.Providers.Custom[0].PiModels; !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("镜像后的 piModels = %+v，想要 [a b c]", got)
	}
}

// Normalize 幂等：连续两次结果相同。
func TestNormalizeMagpieAgentsIdempotent(t *testing.T) {
	cfg := Defaults()
	cfg.Providers.Custom = []CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", Magpie: true, Enabled: true, PiModels: []string{"a", "a", "b"}},
		{ID: "plain", Name: "Plain", BaseURL: "http://plain.example/v1", Enabled: true},
	}
	cfg.Providers.MagpieAgents = map[string]MagpieAgentConfig{
		ProviderClaude: {
			Mode: MagpieModeGlobal,
			Selections: []MagpieSelection{
				{EndpointID: " jp ", Models: []string{"b", "", "c"}},
				{EndpointID: "jp", Models: []string{"d"}},
			},
		},
		ProviderCodex: {Mode: "bogus", Selections: []MagpieSelection{{EndpointID: "gone", Models: []string{"x"}}}},
	}

	once := Normalize(cfg)
	twice := Normalize(once)

	if !reflect.DeepEqual(once.Providers.MagpieAgents, twice.Providers.MagpieAgents) {
		t.Errorf("magpieAgents 不幂等：\n once=%+v\ntwice=%+v", once.Providers.MagpieAgents, twice.Providers.MagpieAgents)
	}
	if !reflect.DeepEqual(once.Providers.Custom, twice.Providers.Custom) {
		t.Errorf("custom 端点不幂等：\n once=%+v\ntwice=%+v", once.Providers.Custom, twice.Providers.Custom)
	}
}

// 支持的 global 保留，不支持的 agent 归一成 off。
func TestNormalizeMagpieAgentsModes(t *testing.T) {
	cfg := Defaults()
	cfg.Providers.MagpieAgents = map[string]MagpieAgentConfig{
		ProviderClaude: {Mode: MagpieModeGlobal},
		ProviderCodex: {
			Mode:       MagpieModeGlobal,
			Selections: []MagpieSelection{{EndpointID: "jp", Models: []string{"x"}}},
		},
		ProviderCline: {Mode: MagpieModeGlobal},
		ProviderPi:    {Mode: MagpieModeGlobal},
		ProviderOmp: {
			Mode:       "bogus",
			Selections: []MagpieSelection{{EndpointID: "jp", Models: []string{"y"}}},
		},
	}

	norm := Normalize(cfg)

	if got := norm.Providers.MagpieAgents[ProviderClaude].Mode; got != MagpieModeGlobal {
		t.Errorf("claude 的 mode = %q，想要 global", got)
	}
	if got := norm.Providers.MagpieAgents[ProviderCodex].Mode; got != MagpieModeGlobal {
		t.Errorf("codex 的 global 应保留，得到 %q", got)
	}
	if got := MagpieAgentSetting(norm, ProviderCline).Mode; got != MagpieModeOff {
		t.Errorf("cline 应为 off，得到 %q", got)
	}
	if got := norm.Providers.MagpieAgents[ProviderPi].Mode; got != MagpieModeGlobal {
		t.Errorf("pi 的 global 应保留，得到 %q", got)
	}
	if got := norm.Providers.MagpieAgents[ProviderOmp].Mode; got != MagpieModeOff {
		t.Errorf("omp 应归一成 off，得到 %q", got)
	}
	if !MagpieAgentSupportsGlobal(ProviderClaude) {
		t.Error("claude 应支持 global")
	}
	if !MagpieAgentSupportsGlobal(ProviderCodex) {
		t.Error("codex 应支持 global")
	}
	if MagpieAgentSupportsGlobal(ProviderCline) {
		t.Error("cline 不应支持 global")
	}
	if !MagpieAgentSupportsGlobal(ProviderPi) {
		t.Error("pi 应支持 global")
	}
	if MagpieAgentSupportsGlobal(ProviderOmp) {
		t.Error("omp 一期不应支持 global")
	}
	if !MagpieAgentSupportsInject(ProviderClaude) || !MagpieAgentSupportsInject(ProviderCodex) || !MagpieAgentSupportsInject(ProviderPi) {
		t.Error("claude、codex 与 pi 应支持 inject")
	}
	if MagpieAgentSupportsInject(ProviderCline) {
		t.Error("cline 不应支持 inject")
	}
	if MagpieAgentSupportsInject(ProviderOmp) {
		t.Error("omp 没有 magpie 注入通道")
	}
}

// 重复模型、空串、空端点、重复端点、已删除端点的勾选都要按规则处理。
func TestNormalizeMagpieAgentsSelections(t *testing.T) {
	cfg := Defaults()
	cfg.Providers.MagpieAgents = map[string]MagpieAgentConfig{
		ProviderClaude: {
			Mode: MagpieModeGlobal,
			Selections: []MagpieSelection{
				{EndpointID: " jp ", Models: []string{"a", " ", "a", "b"}},
				{EndpointID: "", Models: []string{"dropped"}},
				{EndpointID: "jp", Models: []string{"c", "a"}},
				{EndpointID: "deleted", Models: []string{"ghost"}},
				{EndpointID: "empty", Models: []string{" ", ""}},
			},
		},
	}

	norm := Normalize(cfg)

	want := []MagpieSelection{
		{EndpointID: "jp", Models: []string{"a", "b", "c"}},
		{EndpointID: "deleted", Models: []string{"ghost"}},
		{EndpointID: "empty"},
	}
	if got := norm.Providers.MagpieAgents[ProviderClaude].Selections; !reflect.DeepEqual(got, want) {
		t.Errorf("勾选 = %+v，想要 %+v", got, want)
	}
	if norm.Providers.MagpieAgents[ProviderClaude].Mode != MagpieModeGlobal {
		t.Errorf("claude 的 global 不该被降级")
	}
}

// MagpieAgentSetting 读设置：空 mode 视为 inject，且返回值是副本。
func TestMagpieAgentSetting(t *testing.T) {
	cfg := Defaults()
	cfg.Providers.MagpieAgents = map[string]MagpieAgentConfig{
		ProviderClaude: {Selections: []MagpieSelection{{EndpointID: "jp", Models: []string{"a"}}}},
	}

	setting := MagpieAgentSetting(cfg, ProviderClaude)
	if setting.Mode != MagpieModeInject {
		t.Errorf("空 mode 应视为 inject，得到 %q", setting.Mode)
	}
	setting.Selections[0].Models[0] = "mutated"
	if got := cfg.Providers.MagpieAgents[ProviderClaude].Selections[0].Models[0]; got != "a" {
		t.Errorf("MagpieAgentSetting 返回了共享切片，配置被改成 %q", got)
	}
	if got := MagpieAgentSetting(cfg, ProviderCodex); got.Mode != MagpieModeOff {
		t.Errorf("不存在的 agent 应返回 off，得到 %q", got.Mode)
	}
}

// SetMagpieAgentMode 写模式；off 且无勾选时整项消失。
func TestSetMagpieAgentMode(t *testing.T) {
	cfg := Defaults()

	SetMagpieAgentMode(&cfg, ProviderClaude, MagpieModeGlobal)
	if got := MagpieAgentSetting(cfg, ProviderClaude).Mode; got != MagpieModeGlobal {
		t.Errorf("claude 的 mode = %q，想要 global", got)
	}

	SetMagpieAgentMode(&cfg, ProviderCodex, MagpieModeGlobal)
	if got := MagpieAgentSetting(cfg, ProviderCodex).Mode; got != MagpieModeGlobal {
		t.Errorf("codex 的 mode = %q，想要 global", got)
	}

	// inject 即使没有勾选也要保留。
	SetMagpieAgentMode(&cfg, ProviderCodex, MagpieModeInject)
	if got := cfg.Providers.MagpieAgents[ProviderCodex].Mode; got != MagpieModeInject {
		t.Errorf("codex 的 inject 应保留，得到 %q", got)
	}
	if got := MagpieAgentSetting(cfg, ProviderClaude).Mode; got != MagpieModeGlobal {
		t.Errorf("codex 的写入不应影响 claude，得到 %q", got)
	}

	// 最后一个项也清掉后，map 置 nil，JSON 里不再出现 magpieAgents。
	SetMagpieAgentMode(&cfg, ProviderCodex, MagpieModeOff)
	SetMagpieAgentMode(&cfg, ProviderClaude, MagpieModeOff)
	if cfg.Providers.MagpieAgents != nil {
		t.Errorf("map 为空时应置 nil，得到 %+v", cfg.Providers.MagpieAgents)
	}
}

// 没有 magpie 设置时 JSON 里不出现 magpieAgents；有设置时才写。
func TestMagpieAgentsJSONOmitsEmpty(t *testing.T) {
	raw, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatalf("marshal config failed: %v", err)
	}
	if strings.Contains(string(raw), "magpieAgents") {
		t.Errorf("没有 magpie 设置时应省略 magpieAgents，得到 %s", raw)
	}

	norm := Normalize(magpieAgentsTestConfig("jp", []string{"a"}))
	raw, err = json.Marshal(norm)
	if err != nil {
		t.Fatalf("marshal config failed: %v", err)
	}
	for _, field := range []string{`"magpieAgents"`, `"piModels"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("config JSON 缺少字段 %s：%s", field, raw)
		}
	}
}

// 旧配置写完再读回来：magpieAgents 与 piModels 都还在，且两边一致。
func TestMagpieAgentsSaveLoadKeepsPiModels(t *testing.T) {
	testenv.TempHome(t)
	dataDir := t.TempDir()

	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"cline/gpt-x", "acme/a"}))
	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	wantSel := []MagpieSelection{{EndpointID: "jp", Models: []string{"cline/gpt-x", "acme/a"}}}
	if got := loaded.Providers.MagpieAgents[ProviderPi].Selections; !reflect.DeepEqual(got, wantSel) {
		t.Errorf("读回的 pi 勾选 = %+v，想要 %+v", got, wantSel)
	}
	if got := loaded.Providers.Custom[0].PiModels; !reflect.DeepEqual(got, wantSel[0].Models) {
		t.Errorf("读回的 piModels = %+v，想要 %+v", got, wantSel[0].Models)
	}
}

// SetMagpieAgentDefault：两个字段成对写入、任一为空就一起清空，且写入时不校验
// 端点或模型是否已勾选；只带默认值的 agent 项不被 cleanup 删除。
func TestSetMagpieAgentDefault(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a", "b"}))

	SetMagpieAgentDefault(&cfg, " "+ProviderClaude+" ", " jp ", " b ")
	setting := cfg.Providers.MagpieAgents[ProviderClaude]
	if setting.DefaultEndpointID != "jp" || setting.DefaultModel != "b" {
		t.Fatalf("默认模型 = %q/%q，想要 jp/b", setting.DefaultEndpointID, setting.DefaultModel)
	}
	if _, ok := cfg.Providers.MagpieAgents[ProviderClaude]; !ok {
		t.Fatal("只有默认值的 claude 项不应被 cleanup 删除")
	}

	// 没勾选的模型也照存，生效与否由使用方判断。
	SetMagpieAgentDefault(&cfg, ProviderClaude, "jp", "not-selected")
	if got := cfg.Providers.MagpieAgents[ProviderClaude].DefaultModel; got != "not-selected" {
		t.Fatalf("默认模型 = %q，想要 not-selected", got)
	}

	// 任一参数为空都清空两个字段；inject 且没有勾选的项随后被删除。
	SetMagpieAgentDefault(&cfg, ProviderClaude, "jp", "  ")
	if _, ok := cfg.Providers.MagpieAgents[ProviderClaude]; ok {
		t.Fatalf("清空默认模型后 claude 项应删除，得到 %+v", cfg.Providers.MagpieAgents[ProviderClaude])
	}
	if got := cfg.Providers.MagpieAgents[ProviderPi].Selections; len(got) != 1 {
		t.Errorf("清空 claude 默认值不应影响 pi 的勾选，得到 %+v", got)
	}
}

// 默认模型的 JSON 字段名是 defaultEndpointID / defaultModel，保存再读取后不变。
func TestMagpieAgentDefaultSaveLoad(t *testing.T) {
	testenv.TempHome(t)
	dataDir := t.TempDir()

	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a"}))
	SetMagpieAgentModels(&cfg, ProviderClaude, "jp", []string{"a", "b"})
	SetMagpieAgentDefault(&cfg, ProviderClaude, "jp", "b")
	raw, err := json.Marshal(cfg.Providers.MagpieAgents[ProviderClaude])
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	for _, field := range []string{`"defaultEndpointID":"jp"`, `"defaultModel":"b"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("JSON 缺少字段 %s：%s", field, raw)
		}
	}

	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	setting := loaded.Providers.MagpieAgents[ProviderClaude]
	if setting.DefaultEndpointID != "jp" || setting.DefaultModel != "b" {
		t.Errorf("读回的默认模型 = %q/%q，想要 jp/b", setting.DefaultEndpointID, setting.DefaultModel)
	}
}

func TestNormalizeMagpieModeDefaults(t *testing.T) {
	for _, tc := range []struct {
		agent    string
		mode     MagpieMode
		selected bool
		want     MagpieMode
	}{
		{ProviderClaude, "", false, MagpieModeOff},
		{ProviderPi, "", false, MagpieModeOff},
		{ProviderClaude, "", true, MagpieModeInject},
		{ProviderPi, "", true, MagpieModeInject},
		{ProviderCodex, "", true, MagpieModeOff},
		{ProviderClaude, "invalid", true, MagpieModeInject},
		{ProviderCodex, "invalid", true, MagpieModeOff},
	} {
		if got := normalizeMagpieMode(tc.agent, tc.mode, tc.selected); got != tc.want {
			t.Errorf("%s / %q / %v = %q，想要 %q", tc.agent, tc.mode, tc.selected, got, tc.want)
		}
	}
	for _, agent := range []string{ProviderCline, "unknown"} {
		for _, mode := range []MagpieMode{"", MagpieModeOff, MagpieModeInject, MagpieModeGlobal, "invalid"} {
			if got := normalizeMagpieMode(agent, mode, true); got != MagpieModeOff {
				t.Errorf("%s / %q = %q，想要 off", agent, mode, got)
			}
		}
	}
}

func TestSetMagpieAgentNewSelectionDefaultsOff(t *testing.T) {
	for _, agent := range []string{ProviderClaude, ProviderCodex, ProviderPi} {
		cfg := Defaults()
		SetMagpieAgentModels(&cfg, agent, "jp", []string{"model"})
		if got := cfg.Providers.MagpieAgents[agent].Mode; got != MagpieModeOff {
			t.Errorf("%s 的模式 = %q，想要 off", agent, got)
		}
	}
	cfg := Defaults()
	SetMagpieAgentDefault(&cfg, ProviderClaude, "jp", "model")
	if got := cfg.Providers.MagpieAgents[ProviderClaude].Mode; got != MagpieModeOff {
		t.Errorf("默认模型的模式 = %q，想要 off", got)
	}
}

func TestPiLastSelectionDoesNotReturnAfterLoad(t *testing.T) {
	testenv.TempHome(t)
	dataDir := t.TempDir()
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"model"}))
	SetMagpieAgentModels(&cfg, ProviderPi, "jp", nil)
	cfg = Normalize(cfg)
	if err := Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := MagpieAgentSetting(loaded, ProviderPi).Selections; len(got) != 0 {
		t.Errorf("取消的勾选重新出现: %+v", got)
	}
	if got := loaded.Providers.Custom[0].PiModels; len(got) != 0 {
		t.Errorf("旧 piModels 未清空: %+v", got)
	}
}

// 内置引擎没有自己的勾选时沿用 pi 的勾选与默认模型。
func TestMagpieAgentSettingMcodeFallsBackToPi(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a", "b"}))
	SetMagpieAgentDefault(&cfg, ProviderPi, "jp", "b")

	mcode := MagpieAgentSetting(cfg, ProviderMcode)
	want := []MagpieSelection{{EndpointID: "jp", Models: []string{"a", "b"}}}
	if !reflect.DeepEqual(mcode.Selections, want) {
		t.Fatalf("mcode 的勾选 = %+v，想要沿用 pi 的 %+v", mcode.Selections, want)
	}
	if mcode.DefaultEndpointID != "jp" || mcode.DefaultModel != "b" {
		t.Fatalf("mcode 的默认模型 = %q/%q，想要沿用 pi", mcode.DefaultEndpointID, mcode.DefaultModel)
	}
	if mcode.Mode != MagpieModeInject {
		t.Fatalf("mcode 的模式 = %q，想要 inject", mcode.Mode)
	}
}

// mcode 键存在时只管自己的数据：勾选清空后也不能回退到 pi。
func TestMagpieAgentSettingMcodeEmptySelectionDoesNotFallBack(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a"}))
	SetMagpieAgentModels(&cfg, ProviderMcode, "jp", nil)

	if _, ok := cfg.Providers.MagpieAgents[ProviderMcode]; !ok {
		t.Fatal("清空勾选后 mcode 键不应被删除")
	}
	cfg = Normalize(cfg)
	if _, ok := cfg.Providers.MagpieAgents[ProviderMcode]; !ok {
		t.Fatal("Normalize 后 mcode 键不应被删除")
	}
	if got := MagpieAgentSetting(cfg, ProviderMcode).Selections; len(got) != 0 {
		t.Fatalf("mcode 的勾选 = %+v，想要空", got)
	}
	if got := MagpieAgentSetting(cfg, ProviderPi).Selections; len(got) != 1 {
		t.Fatalf("pi 的勾选被影响 = %+v", got)
	}
}

// mcode 的写入不镜像到 piModels，也不改变 pi 的勾选。
func TestSetMagpieAgentModelsMcodeDoesNotMirrorPiModels(t *testing.T) {
	cfg := Normalize(magpieAgentsTestConfig("jp", []string{"a"}))
	SetMagpieAgentModels(&cfg, ProviderMcode, "jp", []string{"b", "c"})

	if got := cfg.Providers.Custom[0].PiModels; !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("piModels = %+v，想要 [a]", got)
	}
	if got := MagpieAgentSetting(cfg, ProviderPi).Selections; len(got) != 1 || !reflect.DeepEqual(got[0].Models, []string{"a"}) {
		t.Fatalf("pi 的勾选 = %+v", got)
	}
	want := []MagpieSelection{{EndpointID: "jp", Models: []string{"b", "c"}}}
	if got := MagpieAgentSetting(cfg, ProviderMcode).Selections; !reflect.DeepEqual(got, want) {
		t.Fatalf("mcode 的勾选 = %+v，想要 %+v", got, want)
	}
}
