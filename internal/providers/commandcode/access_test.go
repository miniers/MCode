package commandcode

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCatalog(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".commandcode", "cache", "model-catalog", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog-abc.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadCLICatalogAndFilter(t *testing.T) {
	home := t.TempDir()
	writeCatalog(t, home, `{"body":{"data":[
		{"id":"deepseek/deepseek-v4-pro","aliases":[],"category":"opensource","minimumPlan":"individual-go"},
		{"id":"claude-sonnet-5-5","aliases":["claude-sonnet-latest"],"category":"opensource","minimumPlan":"individual-goat"},
		{"id":"claude-opus-5","aliases":[],"category":"premium","minimumPlan":"individual-pro"},
		{"id":"legacy-premium","aliases":[],"category":"premium"}
	]}}`)
	catalog := readCLICatalog(home)
	if len(catalog) != 5 {
		t.Fatalf("目录应含 4 个 id 加 1 个别名: %+v", catalog)
	}
	models := []Model{
		{ID: "deepseek/deepseek-v4-pro", Default: true},
		{ID: "claude-sonnet-5-5"},
		{ID: "claude-sonnet-latest"},
		{ID: "claude-opus-5"},
		{ID: "legacy-premium"},
		{ID: "not-in-catalog"},
	}

	got := filterModels(models, catalog, account{PlanID: "individual-go-v1"})
	want := []string{"deepseek/deepseek-v4-pro", "not-in-catalog"}
	if len(got) != len(want) {
		t.Fatalf("Go 方案应只剩 %v，实际 %+v", want, got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("第 %d 项应为 %s，实际 %s", i, id, got[i].ID)
		}
	}

	if got := filterModels(models, catalog, account{PlanID: "individual-pro"}); len(got) != len(models) {
		// Pro 方案够得着全部模型；没有 minimumPlan 的 legacy 模型按类别放行，目录外的模型不拦。
		t.Fatalf("Pro 方案应全部可用: %+v", got)
	}
	if got := filterModels(models, catalog, account{PlanID: "individual-go", HasCredits: true}); len(got) != len(models) {
		t.Fatalf("有额度时全部可用: %+v", got)
	}
	if got := filterModels(models, catalog, account{}); len(got) != len(models) {
		t.Fatalf("没有有效订阅时不过滤: %+v", got)
	}
}

func TestFilterModelsKeepsOneDefault(t *testing.T) {
	catalog := map[string]modelAccess{
		"a": {MinimumPlan: "individual-pro", Category: "premium"},
		"b": {MinimumPlan: "individual-go", Category: "opensource"},
	}
	got := filterModels([]Model{{ID: "a", Default: true}, {ID: "b"}}, catalog, account{PlanID: "individual-go"})
	if len(got) != 1 || got[0].ID != "b" || !got[0].Default {
		t.Fatalf("默认模型被过滤掉后应由第一行接任: %+v", got)
	}
}

func TestFilterModelsNeverEmpty(t *testing.T) {
	catalog := map[string]modelAccess{"a": {MinimumPlan: "individual-max", Category: "premium"}}
	got := filterModels([]Model{{ID: "a"}}, catalog, account{PlanID: "individual-go"})
	if len(got) != 1 {
		t.Fatalf("全被过滤时应原样返回: %+v", got)
	}
}
