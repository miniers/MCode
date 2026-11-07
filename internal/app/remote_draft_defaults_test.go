package app

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote"
)

// TestRemoteDraftDefaultsIgnoresRoleModels 验证 providers.list 的草稿默认值不再读模型角色：
// 即使配置了 Lead/Coder 角色，provider 与 model 仍按通用配置与 provider 默认模型决定，
// 执行侧（bees）留空表示跟随主导。
func TestRemoteDraftDefaultsIgnoresRoleModels(t *testing.T) {
	// remoteDraftDefaults 会读 ui-state.json 里的上次选择，隔离到临时数据目录。
	t.Setenv("MCODE_DATA", t.TempDir())

	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Models.ProviderSelected = true
	cfg.Models.Roles.Lead = config.ModelRoleConfig{Provider: "codex", Model: "gpt-5.5"}
	cfg.Models.Roles.Coder = config.ModelRoleConfig{Provider: "codex", Model: "gpt-5.5"}

	draft := remoteDraftDefaults(cfg)

	if draft.Provider != "claude" {
		t.Fatalf("provider = %q，期望 claude（不采用 Lead 角色）", draft.Provider)
	}
	wantModel := remote.DefaultModel("claude")
	if got := derefDraftString(draft.Model); got != wantModel {
		t.Fatalf("model = %q，期望 %q（provider 默认模型，不采用 Lead 角色）", got, wantModel)
	}
	if draft.SwarmBeesProvider != nil || draft.SwarmBeesModel != nil {
		t.Fatalf("执行侧 = (%v, %v)，期望都为空（不采用 Coder 角色）",
			draft.SwarmBeesProvider, draft.SwarmBeesModel)
	}
}
