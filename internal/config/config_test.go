package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"mcode/internal/harness"
)

func TestSessionAttachmentLimit(t *testing.T) {
	for _, tc := range []struct{ input, want int }{
		{0, DefaultComposerAttachmentLimit}, {-1, DefaultComposerAttachmentLimit},
		{5, 5}, {99, MaxComposerAttachmentLimit},
	} {
		if got := (SessionConfig{ComposerAttachmentLimit: tc.input}).AttachmentLimit(); got != tc.want {
			t.Errorf("AttachmentLimit(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// 1. 文件不存在时 Load 返回 Defaults()，不报错，不创建文件。
func TestLoadNotExist(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load on non-existent config should not return error, got: %v", err)
	}

	expected := Defaults()
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("expected Defaults() %+v, got %+v", expected, cfg)
	}

	cfgPath := ConfigPath(dir)
	if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load should not create file if it does not exist, but stat gave: %v", err)
	}
}

// 旧配置中已删除的 session.thread_finish_action 和 providers.*.updateMonitor 加载时被忽略。
func TestLoadIgnoresRemovedSessionAndProviderSettings(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{
		"session": {"thread_finish_action": "archive", "queue_while_running": false},
		"providers": {"copilot": {"updateMonitor": false, "enabled": true}}
	}`)
	if err := os.WriteFile(ConfigPath(dir), legacy, 0o600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}
	cfg, warnings, err := LoadWithWarnings(dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("加载旧配置失败: 警告 %v，错误 %v", warnings, err)
	}
	if cfg.Session.QueueWhileRunning || !cfg.Providers.Copilot.Enabled {
		t.Fatalf("保留的设置未正常加载: session=%+v copilot=%+v", cfg.Session, cfg.Providers.Copilot)
	}
}

// 2. JSON 中缺失的字段保留默认值。
func TestLoadMissingFieldsKeepDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)

	// 只写入 general.provider 和 remote.extra_hosts
	partialJSON := []byte(`{
		"general": {
			"provider": "codex"
		},
		"remote": {
			"extra_hosts": ["lan.local"]
		}
	}`)
	if err := os.WriteFile(cfgPath, partialJSON, 0o600); err != nil {
		t.Fatalf("failed to write partial config: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Models.Provider != "codex" {
		t.Errorf("expected General.Provider 'codex', got %q", cfg.Models.Provider)
	}
	// 缺失字段应保留默认值
	if cfg.Permissions.Mode != ModeFullAccess {
		t.Errorf("expected default General.PermissionMode %q, got %q", ModeFullAccess, cfg.Permissions.Mode)
	}
	if !cfg.Notifications.NotifyWhenFinished {
		t.Errorf("expected default General.NotifyWhenFinished true, got false")
	}
	if !cfg.Notifications.NotifyOnApproval {
		t.Errorf("expected default General.NotifyOnApproval true, got false")
	}
	if !cfg.Notifications.PlayFinishSound {
		t.Errorf("expected default General.PlayFinishSound true, got false")
	}
	if !cfg.Startup.ShowTrayIcon {
		t.Errorf("expected default General.ShowTrayIcon true, got false")
	}
	if cfg.Startup.TrayShowQuota {
		t.Errorf("expected default General.TrayShowQuota false, got true")
	}
	if cfg.Startup.TrayQuotaHidden != nil {
		t.Errorf("expected default General.TrayQuotaHidden nil, got %v", cfg.Startup.TrayQuotaHidden)
	}
	if cfg.Shortcuts.GlobalShortcutEnabled {
		t.Errorf("expected default General.GlobalShortcutEnabled false, got true")
	}
	if cfg.Shortcuts.GlobalShortcut != "CmdOrCtrl+Shift+Space" {
		t.Errorf("expected default General.GlobalShortcut 'CmdOrCtrl+Shift+Space', got %q", cfg.Shortcuts.GlobalShortcut)
	}
	if !cfg.Archive.ConfirmBeforeDeleting {
		t.Errorf("expected default General.ConfirmBeforeDeleting true, got false")
	}
	if cfg.Appearance.Theme != ThemeSystem {
		t.Errorf("expected default Appearance.Theme %q, got %q", ThemeSystem, cfg.Appearance.Theme)
	}
	if !cfg.Providers.Claude.Enabled {
		t.Errorf("expected default Providers.Claude.Enabled true, got false")
	}
	if !cfg.Providers.Codex.Enabled {
		t.Errorf("expected default Providers.Codex.Enabled true, got false")
	}
	if !cfg.Providers.OpenAI.Enabled {
		t.Errorf("expected default Providers.OpenAI.Enabled true, got false")
	}
	if !cfg.Providers.Agy.Enabled {
		t.Errorf("expected default Providers.Agy.Enabled true, got false")
	}
	if cfg.Remote.Listen != DefaultRemoteListen {
		t.Errorf("expected default Remote.Listen %q, got %q", DefaultRemoteListen, cfg.Remote.Listen)
	}
	if len(cfg.Remote.ExtraHosts) != 1 || cfg.Remote.ExtraHosts[0] != "lan.local" {
		t.Errorf("expected ExtraHosts ['lan.local'], got %+v", cfg.Remote.ExtraHosts)
	}
}

// 3. version 大于当前版本时报错，文件内容不变。
func TestLoadVersionNewerThanSupported(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)

	futureVer := Version + 1
	futureJSON := []byte(fmt.Sprintf(`{
		"version": %d,
		"general": {
			"provider": "codex",
			"permission_mode": "agent"
		},
		"appearance": {
			"theme": "dark"
		}
	}`, futureVer))
	if err := os.WriteFile(cfgPath, futureJSON, 0o600); err != nil {
		t.Fatalf("failed to write future config: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatalf("expected error for version %d > %d, got nil", futureVer, Version)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("version %d", futureVer)) {
		t.Errorf("error message should mention version %d, got: %v", futureVer, err)
	}

	content, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("failed to re-read config file: %v", readErr)
	}
	if string(content) != string(futureJSON) {
		t.Fatalf("config file content was modified on version error")
	}

	bakPath := filepath.Join(dir, "config.json.bak")
	if _, err := os.Stat(bakPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config.json.bak should not be created when version is newer than supported")
	}
}

// 4. 非法 provider、permission mode 或 theme 报错，错误信息含字段名。
func TestValidateInvalidFields(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(cfg *Config)
		fieldHint string
	}{
		{
			name: "invalid provider",
			modify: func(c *Config) {
				c.Models.Provider = "unsupported_provider"
			},
			fieldHint: "models.provider",
		},
		{
			name: "invalid permission mode",
			modify: func(c *Config) {
				c.Permissions.Mode = "full_power"
			},
			fieldHint: "permissions.mode",
		},
		{
			name: "invalid theme",
			modify: func(c *Config) {
				c.Appearance.Theme = "cyberpunk"
			},
			fieldHint: "appearance.theme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.modify(&cfg)

			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.fieldHint) {
				t.Errorf("error %q should contain field name %q", err.Error(), tc.fieldHint)
			}

			// Save 也会校验
			dir := t.TempDir()
			if saveErr := Save(dir, cfg); saveErr == nil {
				t.Errorf("Save should reject invalid config")
			}
		})
	}
}

// 5. Save 后 Load 得到相等结构；文件权限 0600。
func TestSaveAndLoadRoundtripAndPermissions(t *testing.T) {
	dir := t.TempDir()

	cfg := Defaults()
	cfg.Models.Provider = ProviderCodex
	cfg.Permissions.Mode = ModeReadOnly
	cfg.Notifications.NotifyWhenFinished = false
	cfg.Archive.ConfirmBeforeDeleting = false
	cfg.Appearance.Theme = ThemeDark
	cfg.Providers.Claude.Bin = "/usr/local/bin/claude"
	cfg.Remote.Listen = "0.0.0.0:9000"
	cfg.Remote.ExtraHosts = []string{"example.com", "lan.dev"}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cfgPath := ConfigPath(dir)
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat saved config file failed: %v", err)
	}

	perm := fi.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected file permission 0600, got %04o", perm)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load saved config failed: %v", err)
	}

	if !reflect.DeepEqual(cfg, loaded) {
		t.Fatalf("loaded config does not match saved.\nSaved:  %+v\nLoaded: %+v", cfg, loaded)
	}
}

// 6. 模拟写入失败（例如目标目录只读）时原文件不被破坏。
func TestSaveFailureDoesNotCorruptOriginal(t *testing.T) {
	dir := t.TempDir()

	origCfg := Defaults()
	origCfg.Appearance.Theme = ThemeLight
	if err := Save(dir, origCfg); err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}

	cfgPath := ConfigPath(dir)
	origBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read original config failed: %v", err)
	}

	// 将目录置为只读（0500），使得无法在目录中创建临时文件
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("failed to chmod dir to 0500: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
	})

	newCfg := origCfg
	newCfg.Appearance.Theme = ThemeDark
	saveErr := Save(dir, newCfg)
	if saveErr == nil {
		t.Fatalf("expected Save to fail on read-only directory")
	}

	// 恢复目录权限以校验原文件
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("failed to restore dir permissions: %v", err)
	}

	afterBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after failed Save: %v", err)
	}

	if string(afterBytes) != string(origBytes) {
		t.Fatalf("original file was corrupted during failed save!\nBefore:\n%s\nAfter:\n%s", string(origBytes), string(afterBytes))
	}
}

// 8. 内存 SecretStore 的 Get/Set/Delete 行为。
func TestMemorySecretStore(t *testing.T) {
	store := NewMemorySecretStore()
	account := "provider.openai.api_key"

	// 1) 未找到时返回 mygo.ErrSecretNotFound
	val, err := store.Get(account)
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got val=%q, err=%v", val, err)
	}

	// 2) Set 后能正常 Get
	if err := store.Set(account, "sk-test-12345"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	val, err = store.Get(account)
	if err != nil || val != "sk-test-12345" {
		t.Fatalf("expected 'sk-test-12345', got val=%q, err=%v", val, err)
	}

	// 3) 覆盖写入
	if err := store.Set(account, "sk-test-67890"); err != nil {
		t.Fatalf("second Set failed: %v", err)
	}
	val, err = store.Get(account)
	if err != nil || val != "sk-test-67890" {
		t.Fatalf("expected 'sk-test-67890', got val=%q, err=%v", val, err)
	}

	// 4) 删除后再次 Get 返回 mygo.ErrSecretNotFound
	if err := store.Delete(account); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	val, err = store.Get(account)
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got val=%q, err=%v", val, err)
	}

	// 5) 删除不存在的项不报错
	if err := store.Delete("non_existent"); err != nil {
		t.Fatalf("deleting non-existent key should not return error, got: %v", err)
	}
}

// 9. Load 把旧权限模式规范化。
func TestHarnessPermissionModeNormalization(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
		valid bool
	}{
		{"readonly", "readonly", true},
		{"supervised", "supervised", true},
		{"autoAcceptEdits", "auto", true},
		{"agent", "auto", true},
		{"auto", "auto", true},
		{"fullAccess", "fullAccess", true},
		{"full_access", "fullAccess", true},
		{"", "", false},
		{"unknown", "unknown", false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			mode, err := harness.ParseMode(tt.input)
			if (err == nil) != tt.valid || (tt.valid && string(mode) != tt.want) {
				t.Errorf("ParseMode(%q) = %q, %v", tt.input, mode, err)
			}
			if got := harness.NormalizeMode(tt.input); got != tt.want {
				t.Errorf("NormalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoadLegacyPermissionModeNormalization(t *testing.T) {
	tests := []struct {
		legacy string
		want   string
	}{
		{"agent", ModeAuto},
		{"full_access", ModeFullAccess},
		{"autoAcceptEdits", ModeAuto},
		{"fullAccess", ModeFullAccess},
		{"supervised", ModeSupervised},
		{"readonly", ModeReadOnly},
		{"auto", ModeAuto},
	}

	for _, tt := range tests {
		t.Run(tt.legacy, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := ConfigPath(dir)

			raw := []byte(`{
				"general": {
					"permission_mode": "` + tt.legacy + `"
				}
			}`)
			if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			cfg, err := Load(dir)
			if err != nil {
				t.Fatalf("Load failed for mode %q: %v", tt.legacy, err)
			}

			if cfg.Permissions.Mode != tt.want {
				t.Errorf("Load with permission_mode %q: got %q, want %q", tt.legacy, cfg.Permissions.Mode, tt.want)
			}
		})
	}
}

// 10. Validate 接受所有合法权限模式（含旧值兼容）。
func TestValidatePermissionModes(t *testing.T) {
	validModes := []string{
		ModeReadOnly,
		ModeSupervised,
		ModeAutoAcceptEdits,
		ModeAuto,
		ModeFullAccess,
		"agent",
		"full_access",
	}

	for _, mode := range validModes {
		t.Run("valid_"+mode, func(t *testing.T) {
			cfg := Defaults()
			cfg.Permissions.Mode = mode
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate should accept mode %q, got error: %v", mode, err)
			}
		})
	}

	invalidModes := []string{
		"",
		"unknown",
		"full-access",
		"ask",
	}

	for _, mode := range invalidModes {
		t.Run("invalid_"+mode, func(t *testing.T) {
			cfg := Defaults()
			cfg.Permissions.Mode = mode
			if err := Validate(cfg); err == nil {
				t.Errorf("Validate should reject invalid mode %q", mode)
			}
		})
	}
}

// 11. Defaults 包含正确的 Models 默认值且通过 Validate。
func TestModelsDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Models.PinLimit != DefaultModelListLimit {
		t.Errorf("expected PinLimit %d, got %d", DefaultModelListLimit, cfg.Models.PinLimit)
	}
	for _, role := range ModelRoles {
		r := cfg.Models.Role(role)
		if r == nil {
			t.Fatalf("role %s pointer is nil", role)
		}
		if role == RoleUtility {
			if r.Provider != cfg.Models.Provider || r.Model != "" {
				t.Errorf("role %s should have provider=%q, model=\"\", got provider=%q, model=%q", role, cfg.Models.Provider, r.Provider, r.Model)
			}
		} else if r.Provider != "" || r.Model != "" {
			t.Errorf("role %s should be empty, got provider=%q, model=%q", role, r.Provider, r.Model)
		}
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Defaults() should pass Validate, got: %v", err)
	}
}

// 12. 设好四个角色的 provider/model 和置顶列表，Save 后 Load，结果完全相等。
func TestModelsSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()

	cfg := Defaults()
	cfg.Models.PinLimit = 10
	cfg.Models.Pins = []ModelPin{{Provider: "claude", ModelID: "claude-3-7-sonnet"}}
	cfg.Models.Roles.Lead = ModelRoleConfig{Provider: "claude", Model: "claude-3-7-sonnet"}
	cfg.Models.Roles.Coder = ModelRoleConfig{Provider: "codex", Model: "gpt-4o"}
	cfg.Models.Roles.Research = ModelRoleConfig{Provider: "agy", Model: "gemini-2.5-pro"}
	cfg.Models.Roles.Vision = ModelRoleConfig{Provider: "openai", Model: "gpt-4o-mini"}
	cfg.Models.Roles.Utility = ModelRoleConfig{Provider: "claude", Model: "claude-3-5-haiku"}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !reflect.DeepEqual(cfg.Models, loaded.Models) {
		t.Fatalf("loaded models mismatch:\nSaved:  %+v\nLoaded: %+v", cfg.Models, loaded.Models)
	}
}

// 13. v1->v2 迁移补充 models 默认配置段，保留其他字段，并生成备份文件 config.json.bak。
func TestMigrateV1ToV2AddsModels(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)

	v1JSON := []byte(`{
		"version": 1,
		"general": {
			"provider": "codex",
			"permission_mode": "readonly"
		},
		"appearance": {
			"theme": "dark"
		}
	}`)
	if err := os.WriteFile(cfgPath, v1JSON, 0o600); err != nil {
		t.Fatalf("failed to write v1 config: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v1 config failed: %v", err)
	}

	if loaded.Version != Version {
		t.Errorf("expected version %d after migration, got %d", Version, loaded.Version)
	}
	expectedDefaults := Defaults()
	expectedDefaults.Models.Provider = "codex"
	expectedDefaults.Models.Roles.Utility.Provider = ""
	expectedDefaults = Normalize(expectedDefaults)
	if !reflect.DeepEqual(loaded.Models, expectedDefaults.Models) {
		t.Errorf("expected Models %+v, got %+v", expectedDefaults.Models, loaded.Models)
	}
	if loaded.Models.Provider != "codex" {
		t.Errorf("expected General.Provider 'codex', got %q", loaded.Models.Provider)
	}
	if loaded.Permissions.Mode != "readonly" {
		t.Errorf("expected General.PermissionMode 'readonly', got %q", loaded.Permissions.Mode)
	}
	if loaded.Appearance.Theme != "dark" {
		t.Errorf("expected Appearance.Theme 'dark', got %q", loaded.Appearance.Theme)
	}

	bakPath := filepath.Join(dir, "config.json.bak")
	bakBytes, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected config.json.bak to exist: %v", err)
	}
	if string(bakBytes) != string(v1JSON) {
		t.Fatalf("config.json.bak content mismatch:\nExpected: %s\nGot:      %s", string(v1JSON), string(bakBytes))
	}
}

// 13a. v2->v3 迁移改名：hydra 分节改为 swarm，head（子代理）改为 bee，值原样保留。
func TestMigrateV2ToV3RenamesKeys(t *testing.T) {
	raw := map[string]any{
		"version": 2,
		"general": map[string]any{
			"provider":              "codex",
			"hydra_send_to_heads":   true,
			"hydra_lead_plans_only": true,
		},
		"hydra": map[string]any{
			"max_heads":                     3,
			"queue_heads":                   false,
			"always_heads":                  true,
			"review_heads":                  true,
			"auto_hides_idle_heads":         true,
			"auto_pops_heads":               true,
			"archives_codex_heads":          true,
			"tempers_head_effort":           false,
			"hydra_max_rounds":              7,
			"hydraHeadTimeoutMinutes":       55,
			"hydraRequestTimeoutMinutes":    240,
			"auto_forward_question_seconds": 10,
			"pairs": []any{
				map[string]any{
					"id":         "p1",
					"head_tiers": map[string]any{"edit": map[string]any{"model": "m1"}},
				},
			},
		},
	}

	out, err := migrateV2ToV3(raw)
	if err != nil {
		t.Fatalf("migrateV2ToV3 failed: %v", err)
	}
	if _, ok := out["hydra"]; ok {
		t.Fatalf("旧分节 hydra 未删除")
	}
	swarm, ok := out["swarm"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 swarm 分节: %+v", out["swarm"])
	}

	wantSection := map[string]any{
		"max_bees":                      3,
		"queue_bees":                    false,
		"always_bees":                   true,
		"review_bees":                   true,
		"auto_hides_idle_bees":          true,
		"auto_pops_bees":                true,
		"archives_codex_bees":           true,
		"tempers_bee_effort":            false,
		"swarm_max_rounds":              7,
		"swarmBeeTimeoutMinutes":        55,
		"swarmRequestTimeoutMinutes":    240,
		"auto_forward_question_seconds": 10,
	}
	for key, want := range wantSection {
		got, exists := swarm[key]
		if !exists {
			t.Errorf("swarm 缺少新键 %q", key)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("swarm.%s 应为 %v，得 %v", key, want, got)
		}
	}
	for _, oldKey := range []string{
		"max_heads", "queue_heads", "always_heads", "review_heads",
		"auto_hides_idle_heads", "auto_pops_heads",
		"archives_codex_heads", "tempers_head_effort", "hydra_max_rounds",
		"hydraHeadTimeoutMinutes", "hydraRequestTimeoutMinutes",
	} {
		if _, exists := swarm[oldKey]; exists {
			t.Errorf("swarm 旧键 %q 未删除", oldKey)
		}
	}

	pairs, ok := swarm["pairs"].([]any)
	if !ok || len(pairs) != 1 {
		t.Fatalf("pairs 未原样保留: %+v", swarm["pairs"])
	}
	pair, ok := pairs[0].(map[string]any)
	if !ok {
		t.Fatalf("pair 类型不对: %+v", pairs[0])
	}
	if _, exists := pair["head_tiers"]; exists {
		t.Errorf("pair 旧键 head_tiers 未删除")
	}
	if _, exists := pair["bee_tiers"]; !exists {
		t.Errorf("pair 缺少新键 bee_tiers")
	}

	general, ok := out["general"].(map[string]any)
	if !ok {
		t.Fatalf("general 分节丢失")
	}
	if got, exists := general["swarm_send_to_bees"]; !exists || got != true {
		t.Errorf("general.swarm_send_to_bees 应为 true，得 %v (exists=%t)", got, exists)
	}
	if got, exists := general["swarm_lead_plans_only"]; !exists || got != true {
		t.Errorf("general.swarm_lead_plans_only 应为 true，得 %v (exists=%t)", got, exists)
	}
	if _, exists := general["hydra_send_to_heads"]; exists {
		t.Errorf("general 旧键 hydra_send_to_heads 未删除")
	}
	if _, exists := general["hydra_lead_plans_only"]; exists {
		t.Errorf("general 旧键 hydra_lead_plans_only 未删除")
	}
}

// 13b. v2->v3 在新旧键同时存在时保留新键。
func TestMigrateV2ToV3KeepsNewKeys(t *testing.T) {
	raw := map[string]any{
		"version": 2,
		"general": map[string]any{
			"hydra_send_to_heads": true,
			"swarm_send_to_bees":  false,
		},
		"swarm": map[string]any{
			"max_bees":  2,
			"max_heads": 5,
		},
	}

	out, err := migrateV2ToV3(raw)
	if err != nil {
		t.Fatalf("migrateV2ToV3 failed: %v", err)
	}
	swarm := out["swarm"].(map[string]any)
	if got := swarm["max_bees"]; got != 2 {
		t.Errorf("已有新键 max_bees 应保留 2，得 %v", got)
	}
	if _, exists := swarm["max_heads"]; exists {
		t.Errorf("旧键 max_heads 未删除")
	}
	general := out["general"].(map[string]any)
	if got := general["swarm_send_to_bees"]; got != false {
		t.Errorf("已有新键 swarm_send_to_bees 应保留 false，得 %v", got)
	}
	if _, exists := general["hydra_send_to_heads"]; exists {
		t.Errorf("旧键 hydra_send_to_heads 未删除")
	}
}

// 13c. v2->v3 在缺少分节或值类型不对时不出错。
func TestMigrateV2ToV3ToleratesMissingOrUnexpectedSections(t *testing.T) {
	cases := []map[string]any{
		{"version": 2},
		{"version": 2, "general": "nope", "hydra": 123},
		{"version": 2, "swarm": []any{}, "hydra": map[string]any{"max_heads": 1}},
	}
	for i, raw := range cases {
		out, err := migrateV2ToV3(raw)
		if err != nil {
			t.Fatalf("case %d 不应报错: %v", i, err)
		}
		if out == nil {
			t.Fatalf("case %d 返回了 nil", i)
		}
	}
}

// 13d. v1→v4 整条迁移链：先补 models 默认段，再改名 hydra->swarm / head->bee，最后改名休眠与简洁回复相关的键。
func TestMigrateV1ToV4FullChain(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)

	v1JSON := []byte(`{
		"version": 1,
		"general": {
			"provider": "codex",
			"permission_mode": "readonly",
			"hydra_send_to_heads": true,
			"settle_sound": false,
			"sidebar_auto_settle_days": 3,
			"anti_slop_enabled": false,
			"thread_finish_action": "settle",
			"shortcut_overrides": {"settle-thread": "Shift+CmdOrCtrl+S"}
		},
		"hydra": {
			"max_heads": 4,
			"hydra_max_rounds": 5
		}
	}`)
	if err := os.WriteFile(cfgPath, v1JSON, 0o600); err != nil {
		t.Fatalf("failed to write v1 config: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v1 config through v4 chain failed: %v", err)
	}

	if loaded.Version != Version {
		t.Fatalf("expected version %d after migration, got %d", Version, loaded.Version)
	}
	expectedDefaults := Defaults()
	expectedDefaults.Models.Provider = "codex"
	expectedDefaults.Models.Roles.Utility.Provider = ""
	expectedDefaults = Normalize(expectedDefaults)
	if !reflect.DeepEqual(loaded.Models, expectedDefaults.Models) {
		t.Errorf("v1->v2 的 models 默认段未生效: %+v", loaded.Models)
	}
	if loaded.Orchestration.MaxBees != 4 {
		t.Errorf("expected Swarm.MaxBees 4，got %d", loaded.Orchestration.MaxBees)
	}
	if loaded.Orchestration.MaxRounds != 5 {
		t.Errorf("expected Swarm.MaxRounds 5，got %d", loaded.Orchestration.MaxRounds)
	}
	if !loaded.Orchestration.AlwaysBees {
		t.Errorf("旧 general 的 hydra_send_to_heads 未迁移到 orchestration.always_bees: %+v", loaded.Orchestration.AlwaysBees)
	}
	if loaded.Notifications.PlayDormantSound() {
		t.Errorf("旧 general.settle_sound=false 未迁移到 dormant_sound: %+v", loaded.Notifications.DormantSound)
	}
	if got := loaded.Archive.AutoDormantDays(); got != 3 {
		t.Errorf("旧 general.sidebar_auto_settle_days 未迁移到 sidebar_auto_dormant_days，AutoDormantDays() = %d", got)
	}
	if loaded.Session.ConciseReplies() {
		t.Errorf("旧 general.anti_slop_enabled=false 未迁移到 concise_replies_enabled: %+v", loaded.Session.ConciseRepliesEnabled)
	}
	if got := loaded.Shortcuts.ShortcutOverrides["dormant-thread"]; got != "Shift+CmdOrCtrl+S" {
		t.Errorf("快捷键 id settle-thread 未迁移为 dormant-thread，ShortcutOverrides = %+v", loaded.Shortcuts.ShortcutOverrides)
	}

	bakBytes, err := os.ReadFile(filepath.Join(dir, "config.json.bak"))
	if err != nil {
		t.Fatalf("expected config.json.bak to exist: %v", err)
	}
	if string(bakBytes) != string(v1JSON) {
		t.Fatalf("config.json.bak content mismatch:\nExpected: %s\nGot:      %s", string(v1JSON), string(bakBytes))
	}
}

// 13e. v3→v4 迁移：休眠与简洁回复相关的键改名，thread_finish_action 与快捷键 id 的值同步改。
func TestMigrateV3ToV4RenamesKeysAndValues(t *testing.T) {
	raw := map[string]any{
		"version": 3,
		"general": map[string]any{
			"provider":                         "codex",
			"settle_sound":                     false,
			"sidebar_auto_settle_days":         3,
			"anti_slop_enabled":                false,
			"thread_finish_action":             "settle",
			"shortcut_overrides":               map[string]any{"settle-thread": "Shift+CmdOrCtrl+S", "toggle-diff": "Cmd+B"},
			"unrelated_untouched_by_migration": "keep",
		},
	}

	out, err := migrateV3ToV4(raw)
	if err != nil {
		t.Fatalf("migrateV3ToV4 failed: %v", err)
	}
	general, ok := out["general"].(map[string]any)
	if !ok {
		t.Fatalf("general 分节丢失")
	}

	wantKeys := map[string]any{
		"dormant_sound":             false,
		"sidebar_auto_dormant_days": 3,
		"concise_replies_enabled":   false,
	}
	for key, want := range wantKeys {
		got, exists := general[key]
		if !exists || got != want {
			t.Errorf("general.%s 应为 %v，得 %v (exists=%t)", key, want, got, exists)
		}
	}
	for _, oldKey := range []string{"settle_sound", "sidebar_auto_settle_days", "anti_slop_enabled"} {
		if _, exists := general[oldKey]; exists {
			t.Errorf("general 旧键 %q 未删除", oldKey)
		}
	}

	if got := general["thread_finish_action"]; got != "dormant" {
		t.Errorf("thread_finish_action 应为 %q，得 %v", "dormant", got)
	}

	overrides, ok := general["shortcut_overrides"].(map[string]any)
	if !ok {
		t.Fatalf("shortcut_overrides 丢失: %+v", general["shortcut_overrides"])
	}
	if got := overrides["dormant-thread"]; got != "Shift+CmdOrCtrl+S" {
		t.Errorf("快捷键 id 应为 dormant-thread，得 %v", got)
	}
	if _, exists := overrides["settle-thread"]; exists {
		t.Errorf("旧快捷键 id settle-thread 未删除")
	}
	if got := overrides["toggle-diff"]; got != "Cmd+B" {
		t.Errorf("其他快捷键覆盖应原样保留，得 %v", got)
	}
}

// 13f. v3→v4 在新旧键同时存在时保留新键，且非 settle 的 thread_finish_action 不动。
func TestMigrateV3ToV4KeepsNewKeys(t *testing.T) {
	raw := map[string]any{
		"version": 3,
		"general": map[string]any{
			"settle_sound":            false,
			"dormant_sound":           true,
			"anti_slop_enabled":       false,
			"concise_replies_enabled": true,
			"thread_finish_action":    "archive",
		},
	}

	out, err := migrateV3ToV4(raw)
	if err != nil {
		t.Fatalf("migrateV3ToV4 failed: %v", err)
	}
	general := out["general"].(map[string]any)
	if got := general["dormant_sound"]; got != true {
		t.Errorf("已有新键 dormant_sound 应保留 true，得 %v", got)
	}
	if got := general["concise_replies_enabled"]; got != true {
		t.Errorf("已有新键 concise_replies_enabled 应保留 true，得 %v", got)
	}
	if _, exists := general["settle_sound"]; exists {
		t.Errorf("旧键 settle_sound 未删除")
	}
	if got := general["thread_finish_action"]; got != "archive" {
		t.Errorf("archive 不应被改写，得 %v", got)
	}
}

// 13g. v3→v4 在缺少分节或值类型不对时不出错。
func TestMigrateV3ToV4ToleratesMissingOrUnexpectedSections(t *testing.T) {
	cases := []map[string]any{
		{"version": 3},
		{"version": 3, "general": "nope"},
		{"version": 3, "general": map[string]any{"settle_sound": "not-a-bool", "thread_finish_action": 1}},
	}
	for i, raw := range cases {
		out, err := migrateV3ToV4(raw)
		if err != nil {
			t.Fatalf("case %d 不应报错: %v", i, err)
		}
		if out == nil {
			t.Fatalf("case %d 返回了 nil", i)
		}
	}
}

// 13h. v4→v5 为旧的三档配置插入 chore 档（在 read 之后），并给四档补齐默认 effort。
func TestMigrateV4ToV5AddsChoreAndEfforts(t *testing.T) {
	raw := map[string]any{
		"version": 4,
		"swarm": map[string]any{
			"default_tier": "edit",
			"tiers": []any{
				map[string]any{"id": "read", "name": "只读", "description": "只读调研", "read_only": true},
				map[string]any{"id": "edit", "name": "执行", "description": "执行明确修改", "read_only": false},
				map[string]any{"id": "deep", "name": "深度", "description": "我自己改过的描述", "read_only": false},
			},
		},
	}

	out, err := migrateV4ToV5(raw)
	if err != nil {
		t.Fatalf("migrateV4ToV5 failed: %v", err)
	}
	swarm, ok := out["swarm"].(map[string]any)
	if !ok {
		t.Fatalf("swarm 分节丢失")
	}
	tiers, ok := swarm["tiers"].([]any)
	if !ok {
		t.Fatalf("tiers 不是数组: %+v", swarm["tiers"])
	}

	wantIDs := []string{"read", "chore", "edit", "deep"}
	wantEfforts := []string{"medium", "medium", "medium", "high"}
	if len(tiers) != len(wantIDs) {
		t.Fatalf("迁移后应有 %d 个档位，实际 %d: %+v", len(wantIDs), len(tiers), tiers)
	}
	for i, id := range wantIDs {
		tier, ok := tiers[i].(map[string]any)
		if !ok {
			t.Fatalf("tier[%d] 不是对象: %+v", i, tiers[i])
		}
		if got := tier["id"]; got != id {
			t.Errorf("tier[%d] id 应为 %q，得 %v", i, id, got)
		}
		if got := tier["effort"]; got != wantEfforts[i] {
			t.Errorf("tier[%d] effort 应为 %q，得 %v", i, wantEfforts[i], got)
		}
	}

	chore := tiers[1].(map[string]any)
	if got := chore["name"]; got != "杂活" {
		t.Errorf("chore name 应为 杂活，得 %v", got)
	}
	if got := chore["description"]; got != choreTierDescription {
		t.Errorf("chore description 不符: %v", got)
	}
	if got := chore["read_only"]; got != false {
		t.Errorf("chore read_only 应为 false，得 %v", got)
	}

	if got := tiers[3].(map[string]any)["description"]; got != "我自己改过的描述" {
		t.Errorf("deep 的 description 不应被改写，得 %v", got)
	}
	if got := swarm["default_tier"]; got != "edit" {
		t.Errorf("default_tier 不应被改写，得 %v", got)
	}
}

// 13i. v4→v5 在已有 chore 档且用户已设 effort 时不重复插入、不覆盖已有 effort。
func TestMigrateV4ToV5KeepsExistingChoreAndEfforts(t *testing.T) {
	raw := map[string]any{
		"version": 4,
		"swarm": map[string]any{
			"tiers": []any{
				map[string]any{"id": "read", "effort": "low"},
				map[string]any{"id": "chore", "name": "杂活", "effort": "low"},
				map[string]any{"id": "edit", "effort": ""},
				map[string]any{"id": "DEEP", "effort": "xhigh"},
			},
		},
	}

	out, err := migrateV4ToV5(raw)
	if err != nil {
		t.Fatalf("migrateV4ToV5 failed: %v", err)
	}
	tiers := out["swarm"].(map[string]any)["tiers"].([]any)
	if len(tiers) != 4 {
		t.Fatalf("已有 chore 时不应新增档位，实际 %d: %+v", len(tiers), tiers)
	}

	wantIDs := []string{"read", "chore", "edit", "DEEP"}
	wantEfforts := []string{"low", "low", "medium", "xhigh"}
	for i, id := range wantIDs {
		tier := tiers[i].(map[string]any)
		if got := tier["id"]; got != id {
			t.Errorf("tier[%d] id 应保持 %q，得 %v", i, id, got)
		}
		if got := tier["effort"]; got != wantEfforts[i] {
			t.Errorf("tier[%d] effort 应为 %q，得 %v", i, wantEfforts[i], got)
		}
	}
}

// 13j. v4→v5 在 swarm.tiers 缺失或类型不对时原样跳过，不报错。
func TestMigrateV4ToV5ToleratesMissingOrUnexpectedTiers(t *testing.T) {
	cases := []map[string]any{
		{"version": 4},
		{"version": 4, "swarm": "nope"},
		{"version": 4, "swarm": map[string]any{"tiers": "nope"}},
	}
	for i, raw := range cases {
		out, err := migrateV4ToV5(raw)
		if err != nil {
			t.Fatalf("case %d 不应报错: %v", i, err)
		}
		if out == nil {
			t.Fatalf("case %d 返回了 nil", i)
		}
	}

	out, err := migrateV4ToV5(cases[2])
	if err != nil {
		t.Fatalf("migrateV4ToV5 failed: %v", err)
	}
	if got := out["swarm"].(map[string]any)["tiers"]; got != "nope" {
		t.Errorf("非数组 tiers 应原样保留，得 %v", got)
	}
}

// 13k. 磁盘上的 v4 配置加载后版本升到 5，并得到带默认强度的四档。
func TestLoadMigratesV4ToV5WithChoreTier(t *testing.T) {
	dir := t.TempDir()
	v4JSON := []byte(`{
		"version": 4,
		"general": {"provider": "codex"},
		"swarm": {
			"default_tier": "edit",
			"tiers": [
				{"id": "read", "name": "只读", "description": "只读调研", "read_only": true},
				{"id": "edit", "name": "执行", "description": "执行明确修改", "read_only": false},
				{"id": "deep", "name": "深度", "description": "跨文件分析", "read_only": false}
			]
		}
	}`)
	if err := os.WriteFile(ConfigPath(dir), v4JSON, 0o600); err != nil {
		t.Fatalf("写入 v4 配置失败: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v4 config failed: %v", err)
	}
	if loaded.Version != Version {
		t.Fatalf("expected version %d after migration, got %d", Version, loaded.Version)
	}

	wantIDs := []string{"read", "chore", "edit", "deep"}
	wantEfforts := []string{"medium", "medium", "medium", "high"}
	if len(loaded.Orchestration.Tiers) != len(wantIDs) {
		t.Fatalf("迁移后应有 %d 个档位，实际 %d: %+v", len(wantIDs), len(loaded.Orchestration.Tiers), loaded.Orchestration.Tiers)
	}
	for i, id := range wantIDs {
		if got := loaded.Orchestration.Tiers[i].ID; got != id {
			t.Errorf("tier[%d] id 应为 %q，得 %q", i, id, got)
		}
		if got := loaded.Orchestration.Tiers[i].Effort; got != wantEfforts[i] {
			t.Errorf("tier[%d] effort 应为 %q，得 %q", i, wantEfforts[i], got)
		}
	}
	if got := loaded.Orchestration.Tiers[3].Description; got != "跨文件分析" {
		t.Errorf("deep 的 description 不应被改写，得 %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "config.json.bak")); statErr != nil {
		t.Errorf("迁移前应生成 config.json.bak: %v", statErr)
	}
}

// 14. 校验 Models 字段非法取值，错误信息包含字段名。
func TestValidateModelsInvalid(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(cfg *Config)
		fieldHint string
	}{
		{
			name: "lead model requires provider",
			modify: func(c *Config) {
				c.Models.Roles.Lead = ModelRoleConfig{Model: "gpt-4o", Provider: ""}
			},
			fieldHint: "models.roles.lead.model",
		},
		{
			name: "coder model requires provider",
			modify: func(c *Config) {
				c.Models.Roles.Coder = ModelRoleConfig{Model: "claude-3-5-sonnet", Provider: ""}
			},
			fieldHint: "models.roles.coder.model",
		},
		{
			name: "research model requires provider",
			modify: func(c *Config) {
				c.Models.Roles.Research = ModelRoleConfig{Model: "gemini-2.5-pro", Provider: ""}
			},
			fieldHint: "models.roles.research.model",
		},
		{
			name: "vision model requires provider",
			modify: func(c *Config) {
				c.Models.Roles.Vision = ModelRoleConfig{Model: "gpt-4o", Provider: ""}
			},
			fieldHint: "models.roles.vision.model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.modify(&cfg)

			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.fieldHint) {
				t.Errorf("error %q should contain field hint %q", err.Error(), tc.fieldHint)
			}
		})
	}
}

func TestLoadModelsPartialConfig(t *testing.T) {
	for _, version := range []int{1, Version} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			dir := t.TempDir()
			raw := fmt.Sprintf(`{"version":%d,"models":{"roles":{"lead":{"provider":"codex","model":"gpt-5.5"}}}}`, version)
			if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := Defaults().Models
			want.Roles.Lead = ModelRoleConfig{Provider: "codex", Model: "gpt-5.5"}
			if !reflect.DeepEqual(cfg.Models, want) {
				t.Fatalf("部分模型配置未保留默认值或显式零值: %+v", cfg.Models)
			}
		})
	}
}

func TestMigrateInvalidConfigDoesNotPanic(t *testing.T) {
	for _, raw := range []string{`null`, `{"version":-1}`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			path := ConfigPath(dir)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("非法配置应返回错误")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != raw {
				t.Fatalf("非法配置被改写: %q, %v", got, err)
			}
		})
	}
}

func TestRemoteConfigEnabled(t *testing.T) {
	// 默认配置的 Remote.Enabled 为 false、Remote.Listen 等于 DefaultRemoteListen
	cfg := Defaults()
	if cfg.Remote.Enabled {
		t.Errorf("expected default Remote.Enabled false, got true")
	}
	if cfg.Remote.Listen != DefaultRemoteListen {
		t.Errorf("expected default Remote.Listen %q, got %q", DefaultRemoteListen, cfg.Remote.Listen)
	}

	// Remote.Enabled=true 保存后再加载仍为 true
	dir := t.TempDir()
	cfg.Remote.Enabled = true
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !loaded.Remote.Enabled {
		t.Errorf("expected loaded Remote.Enabled true, got false")
	}

	// 不含 enabled 字段的旧 JSON 加载后 Enabled 为 false
	oldDir := t.TempDir()
	oldJSON := []byte(`{
		"version": 2,
		"remote": {
			"listen": "0.0.0.0:43117"
		}
	}`)
	if err := os.WriteFile(ConfigPath(oldDir), oldJSON, 0o600); err != nil {
		t.Fatalf("failed to write old config: %v", err)
	}
	oldLoaded, err := Load(oldDir)
	if err != nil {
		t.Fatalf("Load old config failed: %v", err)
	}
	if oldLoaded.Remote.Enabled {
		t.Errorf("expected loaded Remote.Enabled false from config without enabled field, got true")
	}
}

// TestRemoteConfigTailscaleDefaults 验证旧配置缺字段时 Tailscale 入口关闭、主机名为 mcode。
func TestRemoteConfigTailscaleDefaults(t *testing.T) {
	if Defaults().Remote.TailscaleEnabled() {
		t.Fatal("默认配置应关闭 Tailscale 入口")
	}
	if got := Defaults().Remote.TailscaleHost(); got != "mcode" {
		t.Fatalf("默认主机名为 %q，期望 mcode", got)
	}

	dir := t.TempDir()
	oldJSON := []byte(fmt.Sprintf(`{"version":%d,"remote":{"listen":"0.0.0.0:43117"}}`, Version))
	if err := os.WriteFile(ConfigPath(dir), oldJSON, 0o600); err != nil {
		t.Fatalf("failed to write old config: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Remote.TailscaleEnabled() {
		t.Fatal("缺 tailscale_enabled 的旧配置应视为关闭")
	}
	if got := loaded.Remote.TailscaleHost(); got != "mcode" {
		t.Fatalf("缺 tailscale_hostname 时主机名为 %q，期望 mcode", got)
	}
}

// TestValidTailscaleHostname 覆盖节点名的 DNS label 规则：长度、字符集与首尾连字符。
func TestValidTailscaleHostname(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"默认名", "mcode", true},
		{"带连字符", "my-mac", true},
		{"大写字母", "MyMac", true},
		{"63 位", strings.Repeat("a", 63), true},
		{"空串", "", false},
		{"连字符开头", "-a", false},
		{"连字符结尾", "a-", false},
		{"域名形式", "a.b", false},
		{"中文", "节点", false},
		{"64 位", strings.Repeat("a", 64), false},
		{"下划线", "a_b", false},
	}
	for _, tc := range cases {
		if got := ValidTailscaleHostname(tc.host); got != tc.want {
			t.Errorf("%s: ValidTailscaleHostname(%q) = %v，期望 %v", tc.name, tc.host, got, tc.want)
		}
	}
}

// TestRemoteConfigTailscaleHostFallsBack 验证配置里残留非法节点名时回落为 mcode。
func TestRemoteConfigTailscaleHostFallsBack(t *testing.T) {
	for _, host := range []string{"-a", "a-", "a.b", "a_b", strings.Repeat("a", 64), "   "} {
		cfg := RemoteConfig{TailscaleHostname: host}
		if got := cfg.TailscaleHost(); got != "mcode" {
			t.Errorf("非法主机名 %q 应回落 mcode，得到 %q", host, got)
		}
	}
}

// TestRemoteConfigTailscaleEnabled 验证显式开启后加载与保存都生效。
func TestRemoteConfigTailscaleEnabled(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(fmt.Sprintf(`{"version":%d,"remote":{"tailscale_enabled":true,"tailscale_hostname":"mcode-laptop"}}`, Version))
	if err := os.WriteFile(ConfigPath(dir), raw, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !loaded.Remote.TailscaleEnabled() {
		t.Fatal("显式 tailscale_enabled=true 应生效")
	}
	if got := loaded.Remote.TailscaleHost(); got != "mcode-laptop" {
		t.Fatalf("主机名为 %q，期望 mcode-laptop", got)
	}

	saveDir := t.TempDir()
	cfg := Defaults()
	cfg.Remote.TailscaleOn = boolPtr(true)
	if err := Save(saveDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	roundTrip, err := Load(saveDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !roundTrip.Remote.TailscaleEnabled() {
		t.Fatal("保存后再加载 Tailscale 入口应为开启")
	}
	if got := roundTrip.Remote.TailscaleHost(); got != "mcode" {
		t.Fatalf("未填主机名时应回落 mcode，得到 %q", got)
	}
}

func TestCustomEndpoints_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Providers.Custom = []CustomEndpointConfig{
		{
			ID:      "ep1",
			Name:    "Local Ollama",
			BaseURL: "http://localhost:11434/v1",
			Model:   "llama3",
			Enabled: true,
		},
		{
			ID:      "ep2",
			Name:    "DeepSeek",
			BaseURL: "https://api.deepseek.com/v1",
			Model:   "deepseek-chat",
			Enabled: false,
		},
	}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.Providers.Custom) != 2 {
		t.Fatalf("expected 2 custom endpoints, got %d", len(loaded.Providers.Custom))
	}
	if !reflect.DeepEqual(loaded.Providers.Custom, cfg.Providers.Custom) {
		t.Fatalf("custom endpoints mismatch: got %+v, want %+v", loaded.Providers.Custom, cfg.Providers.Custom)
	}
}

func TestCustomEndpoints_Normalize(t *testing.T) {
	endpoints := []CustomEndpointConfig{
		{ID: "  ep1  ", Name: "  Name1  ", BaseURL: "  http://example.com/v1  ", Model: "  gpt-4  ", Enabled: true},
		{ID: "   ", Name: "Empty ID", BaseURL: "http://test.com", Model: "m1", Enabled: true},
		{ID: "ep1", Name: "Duplicate ep1", BaseURL: "http://duplicate.com", Model: "m2", Enabled: false},
		{ID: "ep2", Name: "Name2", BaseURL: "http://example2.com", Model: "m3", Enabled: true},
	}

	normalized := NormalizeCustomEndpoints(endpoints)
	if len(normalized) != 2 {
		t.Fatalf("expected 2 endpoints after normalization, got %d", len(normalized))
	}

	if normalized[0].ID != "ep1" || normalized[0].Name != "Name1" || normalized[0].BaseURL != "http://example.com/v1" || normalized[0].Model != "gpt-4" || !normalized[0].Enabled {
		t.Errorf("ep1 normalization incorrect: %+v", normalized[0])
	}
	if normalized[1].ID != "ep2" || normalized[1].Name != "Name2" {
		t.Errorf("ep2 normalization incorrect: %+v", normalized[1])
	}

	// 验证全局 Normalize 与 Load/Save 流程均生效
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Providers.Custom = endpoints
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.Providers.Custom) != 2 {
		t.Fatalf("expected 2 endpoints in loaded config, got %d", len(loaded.Providers.Custom))
	}
}

// TestCustomEndpoints_PersonaSourceRoundtrip 覆盖人设来源字段的存取往返。
func TestCustomEndpoints_PersonaSourceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Providers.Custom = []CustomEndpointConfig{
		{
			ID:            "magpie",
			Name:          "Magpie",
			BaseURL:       DefaultMagpieBaseURL,
			Enabled:       true,
			Magpie:        true,
			PersonaWebURL: "https://magpie.example.com:3430",
			PersonasJSON:  `{"personas":[{"id":"coder","name":"编码"}]}`,
			PersonaRules:  true,
		},
	}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.Providers.Custom) != 1 {
		t.Fatalf("expected 1 custom endpoint, got %d", len(loaded.Providers.Custom))
	}
	if !reflect.DeepEqual(loaded.Providers.Custom, cfg.Providers.Custom) {
		t.Fatalf("人设来源字段存取不一致: got %+v, want %+v", loaded.Providers.Custom, cfg.Providers.Custom)
	}
}

// TestCustomEndpoints_PersonaSourceNormalize 覆盖人设来源字段的规范化：Magpie 端点去空白与
// 末尾斜杠，非 Magpie 端点则三个字段全部清空。
func TestCustomEndpoints_PersonaSourceNormalize(t *testing.T) {
	normalized := NormalizeCustomEndpoints([]CustomEndpointConfig{
		{
			ID:            " magpie ",
			Magpie:        true,
			PersonaWebURL: "  https://magpie.example.com:3430/  ",
			PersonasJSON:  "\n [{\"id\":\"coder\"}] \n",
			PersonaRules:  true,
		},
		{
			ID:            "plain",
			PersonaWebURL: "https://magpie.example.com:3430",
			PersonasJSON:  `{"personas":[]}`,
			PersonaRules:  true,
		},
	})
	if len(normalized) != 2 {
		t.Fatalf("expected 2 endpoints after normalization, got %d", len(normalized))
	}
	if normalized[0].PersonaWebURL != "https://magpie.example.com:3430" {
		t.Errorf("web 管理地址应去掉空白与末尾斜杠: %q", normalized[0].PersonaWebURL)
	}
	if normalized[0].PersonasJSON != `[{"id":"coder"}]` {
		t.Errorf("人设列表应只去首尾空白: %q", normalized[0].PersonasJSON)
	}
	if !normalized[0].PersonaRules {
		t.Errorf("Magpie 端点的人设声明应保留")
	}
	if normalized[1].PersonaWebURL != "" || normalized[1].PersonasJSON != "" || normalized[1].PersonaRules {
		t.Errorf("非 Magpie 端点的人设来源字段应清空: %+v", normalized[1])
	}
}

// TestCustomEndpointWebKeySecretAccount 覆盖 web 管理密钥的账号命名。
func TestCustomEndpointWebKeySecretAccount(t *testing.T) {
	if got := CustomEndpointWebKeySecretAccount("abc"); got != "providers.custom.abc.webKey" {
		t.Fatalf("expected providers.custom.abc.webKey, got %q", got)
	}
	if CustomEndpointWebKeySecretAccount("abc") == CustomEndpointSecretAccount("abc") {
		t.Fatal("web 管理密钥与 API Key 不能共用同一个账号")
	}
}

// TestCustomEndpoints_QuotaProvidersJSONRoundtrip 覆盖 QuotaProviders 的 nil 与空切片区分，及 TrayQuotaHidden 默认值。
func TestCustomEndpoints_QuotaProvidersJSONRoundtrip(t *testing.T) {
	// 1. 断言 TrayQuotaHidden 默认为 nil
	cfg := Defaults()
	if cfg.Startup.TrayQuotaHidden != nil {
		t.Fatalf("expected default General.TrayQuotaHidden nil, got %v", cfg.Startup.TrayQuotaHidden)
	}

	// 2. JSON 往返，确认 QuotaProviders 的 nil 和空切片能区分开
	type wrapper struct {
		Endpoints []CustomEndpointConfig `json:"endpoints"`
	}
	w := wrapper{
		Endpoints: []CustomEndpointConfig{
			{
				ID:             "ep-nil",
				Name:           "EP Nil",
				Magpie:         true,
				QuotaProviders: nil,
			},
			{
				ID:             "ep-empty",
				Name:           "EP Empty",
				Magpie:         true,
				QuotaProviders: []string{},
			},
			{
				ID:             "ep-items",
				Name:           "EP Items",
				Magpie:         true,
				QuotaProviders: []string{"codex", "claude"},
			},
		},
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"quotaProviders":null`) {
		t.Errorf("expected quotaProviders:null for nil slice, got json: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"quotaProviders":[]`) {
		t.Errorf("expected quotaProviders:[] for empty slice, got json: %s", jsonStr)
	}

	var decoded wrapper
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(decoded.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(decoded.Endpoints))
	}

	if decoded.Endpoints[0].QuotaProviders != nil {
		t.Errorf("ep-nil: expected nil QuotaProviders, got %v", decoded.Endpoints[0].QuotaProviders)
	}

	if decoded.Endpoints[1].QuotaProviders == nil {
		t.Errorf("ep-empty: expected non-nil empty QuotaProviders, got nil")
	} else if len(decoded.Endpoints[1].QuotaProviders) != 0 {
		t.Errorf("ep-empty: expected len 0, got %d", len(decoded.Endpoints[1].QuotaProviders))
	}

	wantItems := []string{"codex", "claude"}
	if !reflect.DeepEqual(decoded.Endpoints[2].QuotaProviders, wantItems) {
		t.Errorf("ep-items: expected %v, got %v", wantItems, decoded.Endpoints[2].QuotaProviders)
	}

	// 3. 落盘 config.json 往返及规范化
	dir := t.TempDir()
	cfg.Startup.TrayQuotaHidden = []string{"claude", "zai"}
	cfg.Providers.Custom = append(w.Endpoints, CustomEndpointConfig{
		ID:             "plain-non-magpie",
		Name:           "Plain",
		Magpie:         false,
		QuotaProviders: []string{"codex"},
	})
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save config failed: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load config failed: %v", err)
	}
	if !reflect.DeepEqual(loaded.Startup.TrayQuotaHidden, []string{"claude", "zai"}) {
		t.Errorf("TrayQuotaHidden roundtrip mismatch: got %v", loaded.Startup.TrayQuotaHidden)
	}
	if loaded.Providers.Custom[0].QuotaProviders != nil {
		t.Errorf("loaded ep-nil QuotaProviders expected nil, got %v", loaded.Providers.Custom[0].QuotaProviders)
	}
	if loaded.Providers.Custom[1].QuotaProviders == nil {
		t.Errorf("loaded ep-empty QuotaProviders expected non-nil empty slice, got nil")
	} else if len(loaded.Providers.Custom[1].QuotaProviders) != 0 {
		t.Errorf("loaded ep-empty QuotaProviders expected len 0, got %d", len(loaded.Providers.Custom[1].QuotaProviders))
	}
	if !reflect.DeepEqual(loaded.Providers.Custom[2].QuotaProviders, wantItems) {
		t.Errorf("loaded ep-items QuotaProviders expected %v, got %v", wantItems, loaded.Providers.Custom[2].QuotaProviders)
	}
	// 非 Magpie 端点 QuotaProviders 应该被清空为 nil
	if loaded.Providers.Custom[3].QuotaProviders != nil {
		t.Errorf("non-magpie endpoint QuotaProviders should be normalized to nil, got %v", loaded.Providers.Custom[3].QuotaProviders)
	}
}

func TestCustomEndpoints_NoAPIKeyInJSON(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Providers.Custom = []CustomEndpointConfig{
		{
			ID:      "ep1",
			Name:    "Endpoint 1",
			BaseURL: "http://localhost:8000/v1",
			Model:   "m1",
			Enabled: true,
		},
	}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content := strings.ToLower(string(data))
	if strings.Contains(content, "apikey") || strings.Contains(content, "api_key") {
		t.Fatalf("config json must not contain any apiKey field, got:\n%s", string(data))
	}
}

// 15. 旧 JSON（不含新分节 swarm、workbench、source_control 及新增字段）加载后，新字段取默认值。
func TestSessionPinSentMessageEnabled(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		cfg  SessionConfig
		want bool
	}{
		{"未设置视为关闭", SessionConfig{}, false},
		{"显式开启", SessionConfig{PinSentMessage: &on}, true},
		{"显式关闭", SessionConfig{PinSentMessage: &off}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.PinSentMessageEnabled(); got != tc.want {
				t.Fatalf("PinSentMessageEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadOldConfigWithoutNewSectionsGetsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)

	oldJSON := []byte(`{
		"version": 2,
		"general": {
			"provider": "codex",
			"permission_mode": "fullAccess"
		},
		"appearance": {
			"theme": "dark"
		}
	}`)
	if err := os.WriteFile(cfgPath, oldJSON, 0o600); err != nil {
		t.Fatalf("failed to write old config: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load old config failed: %v", err)
	}

	// Swarm
	if !cfg.Orchestration.IsEnabled() {
		t.Errorf("expected Swarm.IsEnabled() true, got false")
	}
	if !cfg.Orchestration.AutoDelegateEnabled() {
		t.Errorf("expected Swarm.AutoDelegateEnabled() true, got false")
	}
	if cfg.Orchestration.MaxBees != 4 {
		t.Errorf("expected Swarm.MaxBees 4, got %d", cfg.Orchestration.MaxBees)
	}
	if cfg.Orchestration.WorkspaceMode != WorkspaceModeWorktree {
		t.Errorf("expected Swarm.WorkspaceMode %q, got %q", WorkspaceModeWorktree, cfg.Orchestration.WorkspaceMode)
	}
	if !cfg.Orchestration.RetryFailed {
		t.Errorf("expected Swarm.RetryFailed true, got false")
	}
	if !cfg.Orchestration.AutoMerge {
		t.Errorf("expected Swarm.AutoMerge true, got false")
	}
	if cfg.Orchestration.DefaultProvider != "" {
		t.Errorf("expected empty Swarm.DefaultProvider, got %q", cfg.Orchestration.DefaultProvider)
	}
	if cfg.Orchestration.DefaultModel != "" {
		t.Errorf("expected empty Swarm.DefaultModel, got %q", cfg.Orchestration.DefaultModel)
	}

	// Workbench
	if !cfg.Workbench.AutoRescueStuckEnabled() {
		t.Errorf("expected Workbench.AutoRescueStuckEnabled() true, got false")
	}
	if cfg.Workbench.StuckNudgeTimeout != DefaultStuckNudgeTimeout {
		t.Errorf("expected Workbench.StuckNudgeTimeout %d, got %d", DefaultStuckNudgeTimeout, cfg.Workbench.StuckNudgeTimeout)
	}
	if cfg.Workbench.StuckInterruptTimeout != DefaultStuckInterruptTimeout {
		t.Errorf("expected Workbench.StuckInterruptTimeout %d, got %d", DefaultStuckInterruptTimeout, cfg.Workbench.StuckInterruptTimeout)
	}
	if !cfg.Workbench.EditorAutoSaveEnabled() {
		t.Errorf("expected Workbench.EditorAutoSaveEnabled() true, got false")
	}
	if cfg.Workbench.TerminalScrollbackLines != DefaultTerminalScrollback {
		t.Errorf("expected Workbench.TerminalScrollbackLines %d, got %d", DefaultTerminalScrollback, cfg.Workbench.TerminalScrollbackLines)
	}
	if !cfg.Workbench.KanbanAutoReviewEnabled() {
		t.Errorf("expected Workbench.KanbanAutoReviewEnabled() true, got false")
	}
	if cfg.Workbench.KanbanAutoStartOnCreate {
		t.Errorf("expected Workbench.KanbanAutoStartOnCreate false, got true")
	}
	if cfg.Workbench.BrowserQuickPorts != "3000, 5173, 8080, 4321" {
		t.Errorf("expected Workbench.BrowserQuickPorts default, got %q", cfg.Workbench.BrowserQuickPorts)
	}

	// SourceControl
	if !cfg.SourceControl.AutoCleanupMergedWorktreesEnabled() {
		t.Errorf("expected SourceControl.AutoCleanupMergedWorktreesEnabled() true, got false")
	}
	if !cfg.SourceControl.AutoCleanupWorktreesOnDeleteEnabled() {
		t.Errorf("expected SourceControl.AutoCleanupWorktreesOnDeleteEnabled() true, got false")
	}
	if !cfg.SourceControl.InitSubmodulesEnabled() {
		t.Errorf("expected SourceControl.InitSubmodulesEnabled() true, got false")
	}
	if !cfg.SourceControl.CloneIgnoredOnWorktreeEnabled() {
		t.Errorf("expected SourceControl.CloneIgnoredOnWorktreeEnabled() true, got false")
	}
	if cfg.SourceControl.CloneIgnoredPatterns != nil {
		t.Errorf("expected nil SourceControl.CloneIgnoredPatterns, got %q", *cfg.SourceControl.CloneIgnoredPatterns)
	}
	if got := cfg.SourceControl.CloneIgnoredPatternsText(); got != DefaultCloneIgnoredPatterns {
		t.Errorf("expected SourceControl.CloneIgnoredPatternsText() %q, got %q", DefaultCloneIgnoredPatterns, got)
	}
	if cfg.SourceControl.DefaultWorkspaceMode != WorkspaceModeLocal {
		t.Errorf("expected SourceControl.DefaultWorkspaceMode %q, got %q", WorkspaceModeLocal, cfg.SourceControl.DefaultWorkspaceMode)
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		t.Errorf("expected SourceControl.GenerateCommitMessageEnabled() true, got false")
	}
	if cfg.SourceControl.CommitInstructions != "" {
		t.Errorf("expected empty SourceControl.CommitInstructions, got %q", cfg.SourceControl.CommitInstructions)
	}

	// General 追加字段
	if cfg.Notifications.NotifyWhenForeground {
		t.Errorf("expected General.NotifyWhenForeground false, got true")
	}
	if !cfg.Notifications.ChimeOnAttention() {
		t.Errorf("expected General.ChimeOnAttention() true, got false")
	}
	if !cfg.Notifications.PlayDormantSound() {
		t.Errorf("expected General.PlayDormantSound() true, got false")
	}
	if !cfg.Session.AskCompactOnResume() {
		t.Errorf("expected General.AskCompactOnResume() true, got false")
	}
	if cfg.Session.AutoContinueAfterLimit {
		t.Errorf("expected General.AutoContinueAfterLimit false, got true")
	}
	if !cfg.Session.ShowReasoningEnabled() {
		t.Errorf("expected General.ShowReasoningEnabled() true, got false")
	}
	if !cfg.Session.AutoCollapseReasoningEnabled() {
		t.Errorf("expected General.AutoCollapseReasoningEnabled() true, got false")
	}
	if cfg.Session.PinSentMessageEnabled() {
		t.Errorf("expected General.PinSentMessageEnabled() false, got true")
	}
	if cfg.Session.ConciseReplies() {
		t.Errorf("expected General.ConciseReplies() false, got true")
	}
	if !cfg.Session.QueueWhileRunning {
		t.Errorf("expected General.QueueWhileRunning true, got false")
	}
	if cfg.Startup.QuitAction != QuitActionQuit {
		t.Errorf("expected General.QuitAction %q, got %q", QuitActionQuit, cfg.Startup.QuitAction)
	}
	if cfg.Workbench.TerminalShell != "" {
		t.Errorf("expected empty General.TerminalShell, got %q", cfg.Workbench.TerminalShell)
	}

	// Appearance 追加字段
	if cfg.Appearance.CodeFont != "" {
		t.Errorf("expected empty Appearance.CodeFont, got %q", cfg.Appearance.CodeFont)
	}
	if cfg.Appearance.ConversationDensity != DensityComfortable {
		t.Errorf("expected Appearance.ConversationDensity %q, got %q", DensityComfortable, cfg.Appearance.ConversationDensity)
	}
	if cfg.Appearance.SidebarCompact {
		t.Errorf("expected Appearance.SidebarCompact false, got true")
	}
}

// 16. Normalize 能把越界的值钳住，并将非法的枚举值回退为默认值。
func TestNormalizeClampsOutOfRangeValues(t *testing.T) {
	cfg := Defaults()

	// 上界越界测试
	cfg.Orchestration.MaxBees = 99
	cfg.Orchestration.MaxRounds = 250
	cfg.Orchestration.WorkspaceMode = "non_existent_mode"
	cfg.Workbench.StuckNudgeTimeout = 99999
	cfg.Workbench.StuckInterruptTimeout = 99999
	cfg.Workbench.TerminalScrollbackLines = 200000
	cfg.Workbench.BrowserQuickPorts = "   "

	cfg.Appearance.ConversationDensity = "ultra_dense"
	cfg.Startup.QuitAction = "terminate"
	cfg.SourceControl.DefaultWorkspaceMode = "custom_vcs"

	norm := Normalize(cfg)

	if norm.Orchestration.MaxBees != MaxSwarmMaxBees {
		t.Errorf("expected Swarm.MaxBees clamped to %d, got %d", MaxSwarmMaxBees, norm.Orchestration.MaxBees)
	}
	if norm.Orchestration.MaxRounds != MaxSwarmMaxRounds {
		t.Errorf("expected Swarm.MaxRounds clamped to %d, got %d", MaxSwarmMaxRounds, norm.Orchestration.MaxRounds)
	}
	if norm.Orchestration.WorkspaceMode != WorkspaceModeWorktree {
		t.Errorf("expected Swarm.WorkspaceMode fallback to %q, got %q", WorkspaceModeWorktree, norm.Orchestration.WorkspaceMode)
	}
	if norm.Workbench.StuckNudgeTimeout != MaxStuckNudgeTimeout {
		t.Errorf("expected Workbench.StuckNudgeTimeout clamped to %d, got %d", MaxStuckNudgeTimeout, norm.Workbench.StuckNudgeTimeout)
	}
	if norm.Workbench.StuckInterruptTimeout != MaxStuckInterruptTimeout {
		t.Errorf("expected Workbench.StuckInterruptTimeout clamped to %d, got %d", MaxStuckInterruptTimeout, norm.Workbench.StuckInterruptTimeout)
	}
	if norm.Workbench.TerminalScrollbackLines != MaxTerminalScrollback {
		t.Errorf("expected Workbench.TerminalScrollbackLines clamped to %d, got %d", MaxTerminalScrollback, norm.Workbench.TerminalScrollbackLines)
	}
	if norm.Workbench.BrowserQuickPorts != "3000, 5173, 8080, 4321" {
		t.Errorf("expected Workbench.BrowserQuickPorts fallback to default, got %q", norm.Workbench.BrowserQuickPorts)
	}
	if norm.Appearance.ConversationDensity != DensityComfortable {
		t.Errorf("expected Appearance.ConversationDensity fallback to %q, got %q", DensityComfortable, norm.Appearance.ConversationDensity)
	}
	if norm.Startup.QuitAction != QuitActionQuit {
		t.Errorf("expected General.QuitAction fallback to %q, got %q", QuitActionQuit, norm.Startup.QuitAction)
	}
	if norm.SourceControl.DefaultWorkspaceMode != WorkspaceModeLocal {
		t.Errorf("expected SourceControl.DefaultWorkspaceMode fallback to %q, got %q", WorkspaceModeLocal, norm.SourceControl.DefaultWorkspaceMode)
	}

	if err := Validate(norm); err != nil {
		t.Fatalf("normalized config should pass Validate, got: %v", err)
	}

	// 下界越界与非合法值测试
	cfg2 := Defaults()
	cfg2.Orchestration.MaxBees = -10
	cfg2.Orchestration.MaxRounds = -3
	cfg2.Workbench.StuckNudgeTimeout = -5
	cfg2.Workbench.StuckInterruptTimeout = 50 // 小于 NudgeTimeout，应被纠正大于 NudgeTimeout
	cfg2.Workbench.TerminalScrollbackLines = 10

	norm2 := Normalize(cfg2)
	if norm2.Orchestration.MaxBees != MinSwarmMaxBees {
		t.Errorf("expected Swarm.MaxBees clamped to %d, got %d", MinSwarmMaxBees, norm2.Orchestration.MaxBees)
	}
	if norm2.Orchestration.MaxRounds != MinSwarmMaxRounds {
		t.Errorf("expected Swarm.MaxRounds clamped to %d, got %d", MinSwarmMaxRounds, norm2.Orchestration.MaxRounds)
	}
	if norm2.Workbench.StuckNudgeTimeout != DefaultStuckNudgeTimeout {
		t.Errorf("expected Workbench.StuckNudgeTimeout default %d, got %d", DefaultStuckNudgeTimeout, norm2.Workbench.StuckNudgeTimeout)
	}
	if norm2.Workbench.StuckInterruptTimeout <= norm2.Workbench.StuckNudgeTimeout {
		t.Errorf("expected Workbench.StuckInterruptTimeout (%d) > StuckNudgeTimeout (%d)", norm2.Workbench.StuckInterruptTimeout, norm2.Workbench.StuckNudgeTimeout)
	}
	if norm2.Workbench.TerminalScrollbackLines != MinTerminalScrollback {
		t.Errorf("expected Workbench.TerminalScrollbackLines clamped to %d, got %d", MinTerminalScrollback, norm2.Workbench.TerminalScrollbackLines)
	}

	if err := Validate(norm2); err != nil {
		t.Fatalf("normalized config 2 should pass Validate, got: %v", err)
	}
}

// 发送消息快捷键：默认 Enter 发送；缺省（空串）与非法值归一化为 enter，shift_enter 原样保留。
func TestSessionComposerSendKey(t *testing.T) {
	if got := Defaults().Session.ComposerSendKey; got != ComposerSendKeyEnter {
		t.Fatalf("expected ComposerSendKey default %q, got %q", ComposerSendKeyEnter, got)
	}

	// 缺省与非法值都归为 Enter 发送。
	for _, raw := range []string{"", "Shift", "shift-enter", "enter "} {
		cfg := Defaults()
		cfg.Session.ComposerSendKey = raw
		if got := Normalize(cfg).Session.ComposerSendKey; got != ComposerSendKeyEnter {
			t.Errorf("expected %q normalized to %q, got %q", raw, ComposerSendKeyEnter, got)
		}
	}

	// shift_enter 原样保留。
	cfg := Defaults()
	cfg.Session.ComposerSendKey = ComposerSendKeyShiftEnter
	if got := Normalize(cfg).Session.ComposerSendKey; got != ComposerSendKeyShiftEnter {
		t.Errorf("expected %q preserved, got %q", ComposerSendKeyShiftEnter, got)
	}
}

// 外观的内容宽度：空值（旧配置没有这一项）归一化为标准，wide / full 原样保留且能通过校验，
// 非法值归一化后被改成标准，未经归一化的非法值被 Validate 拒绝。
func TestAppearanceContentWidth(t *testing.T) {
	cfg := Defaults()
	if cfg.Appearance.ContentWidth != ContentWidthStandard {
		t.Fatalf("expected Appearance.ContentWidth default %q, got %q", ContentWidthStandard, cfg.Appearance.ContentWidth)
	}

	// 空值归一化为标准
	empty := Defaults()
	empty.Appearance.ContentWidth = ""
	if norm := Normalize(empty); norm.Appearance.ContentWidth != ContentWidthStandard {
		t.Errorf("expected empty ContentWidth normalized to %q, got %q", ContentWidthStandard, norm.Appearance.ContentWidth)
	}

	// wide / full 合法：归一化不动它，校验也通过
	for _, width := range []string{ContentWidthStandard, ContentWidthWide, ContentWidthFull} {
		c := Defaults()
		c.Appearance.ContentWidth = width
		norm := Normalize(c)
		if norm.Appearance.ContentWidth != width {
			t.Errorf("expected ContentWidth %q preserved, got %q", width, norm.Appearance.ContentWidth)
		}
		if err := Validate(norm); err != nil {
			t.Errorf("expected ContentWidth %q to pass Validate, got: %v", width, err)
		}
	}

	// 非法值：归一化回标准，未归一化时报错
	invalid := Defaults()
	invalid.Appearance.ContentWidth = "ultra_wide"
	if norm := Normalize(invalid); norm.Appearance.ContentWidth != ContentWidthStandard {
		t.Errorf("expected invalid ContentWidth fallback to %q, got %q", ContentWidthStandard, norm.Appearance.ContentWidth)
	}
	err := Validate(invalid)
	if err == nil {
		t.Fatalf("expected Validate to reject invalid ContentWidth")
	}
	if want := `invalid appearance.content_width "ultra_wide": must be 'standard', 'wide' or 'full'`; err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

// 按比例档的内容宽度百分比：默认 85，旧配置缺这一项时归一化为默认，
// 60..100 原样保留且能通过校验，越界值归一化时钳制、未归一化时报错。
func TestAppearanceContentWidthPercent(t *testing.T) {
	cfg := Defaults()
	if cfg.Appearance.ContentWidthPercent != DefaultContentWidthPercent {
		t.Fatalf("expected Appearance.ContentWidthPercent default %d, got %d", DefaultContentWidthPercent, cfg.Appearance.ContentWidthPercent)
	}

	// 旧配置只有 content_width=full、没有百分比时按默认 85% 生效。
	legacy := Defaults()
	legacy.Appearance.ContentWidth = ContentWidthFull
	legacy.Appearance.ContentWidthPercent = 0
	if norm := Normalize(legacy); norm.Appearance.ContentWidthPercent != DefaultContentWidthPercent {
		t.Errorf("expected missing ContentWidthPercent normalized to %d, got %d", DefaultContentWidthPercent, norm.Appearance.ContentWidthPercent)
	}

	// 范围内的值原样保留，校验也通过
	for _, percent := range []int{MinContentWidthPercent, 75, MaxContentWidthPercent} {
		c := Defaults()
		c.Appearance.ContentWidthPercent = percent
		norm := Normalize(c)
		if norm.Appearance.ContentWidthPercent != percent {
			t.Errorf("expected ContentWidthPercent %d preserved, got %d", percent, norm.Appearance.ContentWidthPercent)
		}
		if err := Validate(norm); err != nil {
			t.Errorf("expected ContentWidthPercent %d to pass Validate, got: %v", percent, err)
		}
	}

	// 越界值：归一化钳制到范围内，未归一化时报错
	for _, tc := range []struct{ in, want int }{
		{MinContentWidthPercent - 10, MinContentWidthPercent},
		{MaxContentWidthPercent + 15, MaxContentWidthPercent},
	} {
		c := Defaults()
		c.Appearance.ContentWidthPercent = tc.in
		if norm := Normalize(c); norm.Appearance.ContentWidthPercent != tc.want {
			t.Errorf("expected ContentWidthPercent %d clamped to %d, got %d", tc.in, tc.want, norm.Appearance.ContentWidthPercent)
		}
		if err := Validate(c); err == nil {
			t.Fatalf("expected Validate to reject ContentWidthPercent %d", tc.in)
		}
	}
}

// Swarm 派发轮次上限：默认 10，按 orchestration.max_rounds 读写，Normalize 把负值归零、超出 100 截断。
func TestSwarmMaxRounds(t *testing.T) {
	cfg := Defaults()
	if cfg.Orchestration.MaxRounds != 10 {
		t.Fatalf("expected Swarm.MaxRounds default 10, got %d", cfg.Orchestration.MaxRounds)
	}

	loaded := Defaults()
	if err := json.Unmarshal([]byte(`{"orchestration":{"max_rounds":12}}`), &loaded); err != nil {
		t.Fatalf("Unmarshal orchestration.max_rounds failed: %v", err)
	}
	if loaded.Orchestration.MaxRounds != 12 {
		t.Fatalf("expected Swarm.MaxRounds 12, got %d", loaded.Orchestration.MaxRounds)
	}

	// 旧配置文件（v7 的 swarm.swarm_max_rounds）走迁移加载后仍落到 orchestration.max_rounds。
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"version":7,"swarm":{"swarm_max_rounds":12}}`), 0o600); err != nil {
		t.Fatalf("write v7 config: %v", err)
	}
	migrated, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v7 config failed: %v", err)
	}
	if migrated.Orchestration.MaxRounds != 12 {
		t.Fatalf("expected migrated Swarm.MaxRounds 12, got %d", migrated.Orchestration.MaxRounds)
	}

	loaded.Orchestration.MaxRounds = -3
	if got := Normalize(loaded).Orchestration.MaxRounds; got != MinSwarmMaxRounds {
		t.Errorf("expected negative Swarm.MaxRounds clamped to %d, got %d", MinSwarmMaxRounds, got)
	}

	loaded.Orchestration.MaxRounds = MaxSwarmMaxRounds + 1
	if got := Normalize(loaded).Orchestration.MaxRounds; got != MaxSwarmMaxRounds {
		t.Errorf("expected Swarm.MaxRounds clamped to %d, got %d", MaxSwarmMaxRounds, got)
	}
}

// Swarm 请求总时长预算：默认 180 分钟，按 orchestration.request_timeout_minutes 读写，
// Normalize 把非正值恢复默认、低于 30 取 30、超过 720 取 720。
func TestSwarmRequestTimeoutMinutes(t *testing.T) {
	cfg := Defaults()
	if cfg.Orchestration.RequestTimeoutMinutes != DefaultSwarmRequestTimeoutMinutes {
		t.Fatalf("expected Swarm.RequestTimeoutMinutes default %d, got %d", DefaultSwarmRequestTimeoutMinutes, cfg.Orchestration.RequestTimeoutMinutes)
	}

	loaded := Defaults()
	if err := json.Unmarshal([]byte(`{"orchestration":{"request_timeout_minutes":240}}`), &loaded); err != nil {
		t.Fatalf("Unmarshal orchestration.request_timeout_minutes failed: %v", err)
	}
	if loaded.Orchestration.RequestTimeoutMinutes != 240 {
		t.Fatalf("expected Swarm.RequestTimeoutMinutes 240, got %d", loaded.Orchestration.RequestTimeoutMinutes)
	}

	// 旧配置文件（v7 的 swarm.swarmRequestTimeoutMinutes）走迁移加载后仍落到 orchestration.request_timeout_minutes。
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"version":7,"swarm":{"swarmRequestTimeoutMinutes":240}}`), 0o600); err != nil {
		t.Fatalf("write v7 config: %v", err)
	}
	migrated, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v7 config failed: %v", err)
	}
	if migrated.Orchestration.RequestTimeoutMinutes != 240 {
		t.Fatalf("expected migrated Swarm.RequestTimeoutMinutes 240, got %d", migrated.Orchestration.RequestTimeoutMinutes)
	}

	loaded.Orchestration.RequestTimeoutMinutes = 0
	if got := Normalize(loaded).Orchestration.RequestTimeoutMinutes; got != DefaultSwarmRequestTimeoutMinutes {
		t.Errorf("expected zero Swarm.RequestTimeoutMinutes default %d, got %d", DefaultSwarmRequestTimeoutMinutes, got)
	}

	loaded.Orchestration.RequestTimeoutMinutes = MinSwarmRequestTimeoutMinutes - 10
	if got := Normalize(loaded).Orchestration.RequestTimeoutMinutes; got != MinSwarmRequestTimeoutMinutes {
		t.Errorf("expected small Swarm.RequestTimeoutMinutes clamped to %d, got %d", MinSwarmRequestTimeoutMinutes, got)
	}

	loaded.Orchestration.RequestTimeoutMinutes = MaxSwarmRequestTimeoutMinutes + 60
	if got := Normalize(loaded).Orchestration.RequestTimeoutMinutes; got != MaxSwarmRequestTimeoutMinutes {
		t.Errorf("expected large Swarm.RequestTimeoutMinutes clamped to %d, got %d", MaxSwarmRequestTimeoutMinutes, got)
	}
}

// 29. 验证 utility 角色默认值跟随 General.Provider，且旧配置文件没有 utility 时自动取该默认值。
func TestUtilityRoleDefaultsAndLegacyConfig(t *testing.T) {
	// 1. Defaults 验证
	cfg := Defaults()
	if cfg.Models.Roles.Utility.Provider != cfg.Models.Provider {
		t.Errorf("expected utility provider %q, got %q", cfg.Models.Provider, cfg.Models.Roles.Utility.Provider)
	}
	if cfg.Models.Roles.Utility.Model != "" {
		t.Errorf("expected utility model to be empty, got %q", cfg.Models.Roles.Utility.Model)
	}

	role := cfg.Models.Role(RoleUtility)
	if role == nil {
		t.Fatalf("cfg.Models.Role(RoleUtility) returned nil")
	}
	if role.Provider != cfg.Models.Provider || role.Model != "" {
		t.Errorf("Role(RoleUtility) mismatch: provider=%q, model=%q", role.Provider, role.Model)
	}

	// 2. 旧配置（没有 utility）加载时自动补齐跟随 General.Provider
	dir := t.TempDir()
	legacyJSON := []byte(`{
		"version": 2,
		"general": {
			"provider": "codex",
			"permission_mode": "supervised"
		},
		"models": {
			"roles": {
				"lead": {"provider": "claude", "model": "sonnet"}
			}
		}
	}`)
	cfgPath := ConfigPath(dir)
	if err := os.WriteFile(cfgPath, legacyJSON, 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load legacy config: %v", err)
	}
	if loaded.Models.Roles.Utility.Provider != "codex" {
		t.Errorf("expected utility provider to follow General.Provider 'codex', got %q", loaded.Models.Roles.Utility.Provider)
	}
	if loaded.Models.Roles.Utility.Model != "" {
		t.Errorf("expected utility model to be empty, got %q", loaded.Models.Roles.Utility.Model)
	}
}

// 30. AutoDormantDays：nil 视为 7，0 与负数视为关闭，正数返回原值。
func TestGeneralAutoDormantDays(t *testing.T) {
	days := func(v int) *int { return &v }
	tests := []struct {
		name string
		in   *int
		want int
	}{
		{name: "nil 默认 7 天", in: nil, want: 7},
		{name: "0 表示关闭", in: days(0), want: 0},
		{name: "负数视为关闭", in: days(-3), want: 0},
		{name: "正数返回原值", in: days(30), want: 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := ArchiveConfig{SidebarAutoDormantDays: tc.in}
			if got := g.AutoDormantDays(); got != tc.want {
				t.Errorf("AutoDormantDays() = %d, want %d", got, tc.want)
			}
		})
	}
}

// 31. ShortcutOverrides 与 SidebarAutoDormantDays 的 JSON 往返，并验证空值不写入 JSON。
func TestGeneralShortcutOverridesAndAutoDormantJSONRoundtrip(t *testing.T) {
	days := 14
	cfg := Defaults()
	cfg.Shortcuts.ShortcutOverrides = map[string]string{"toggle-diff": "Cmd+B"}
	cfg.Archive.SidebarAutoDormantDays = &days

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if got := loaded.Shortcuts.ShortcutOverrides["toggle-diff"]; got != "Cmd+B" {
		t.Errorf("ShortcutOverrides roundtrip = %q, want %q", got, "Cmd+B")
	}
	if got := loaded.Archive.AutoDormantDays(); got != 14 {
		t.Errorf("AutoDormantDays() after roundtrip = %d, want 14", got)
	}

	// 未设置时两个字段都不写进 JSON（omitempty）
	data, err = json.Marshal(Defaults())
	if err != nil {
		t.Fatalf("json.Marshal defaults failed: %v", err)
	}
	if strings.Contains(string(data), "shortcut_overrides") || strings.Contains(string(data), "sidebar_auto_dormant_days") {
		t.Errorf("empty shortcut/auto-dormant fields must be omitted, got: %s", data)
	}
}

func TestWorkbenchContextAndKanbanDefaults(t *testing.T) {
	cfg := Defaults()

	if !cfg.Workbench.ContextGuidanceEnabled() {
		t.Error("expected ContextGuidanceEnabled default true")
	}
	if cfg.Workbench.ContextInjectMaxCharacters != DefaultContextInjectMaxCharacters {
		t.Errorf("ContextInjectMaxCharacters = %d, want %d", cfg.Workbench.ContextInjectMaxCharacters, DefaultContextInjectMaxCharacters)
	}
	if cfg.Workbench.KanbanDefaultView != KanbanViewKanban {
		t.Errorf("KanbanDefaultView = %q, want %q", cfg.Workbench.KanbanDefaultView, KanbanViewKanban)
	}
	if !cfg.Workbench.KanbanRunInWorktreeEnabled() {
		t.Error("expected KanbanRunInWorktreeEnabled default true")
	}
	if !cfg.Workbench.KanbanMCPToolsEnabled() {
		t.Error("expected KanbanMCPToolsEnabled default true")
	}
	if !cfg.Workbench.KanbanEntryOrdinarySessions() {
		t.Error("expected KanbanEntryOrdinarySessions default true")
	}
	if cfg.Workbench.BrowserStartURL != DefaultBrowserStartURL {
		t.Errorf("BrowserStartURL = %q, want %q", cfg.Workbench.BrowserStartURL, DefaultBrowserStartURL)
	}
	if !cfg.Workbench.BrowserConsoleCaptureEnabled() {
		t.Error("expected BrowserConsoleCaptureEnabled default true")
	}

	var zero WorkbenchConfig
	if !zero.ContextGuidanceEnabled() || !zero.KanbanRunInWorktreeEnabled() ||
		!zero.KanbanMCPToolsEnabled() || !zero.KanbanEntryOrdinarySessions() || !zero.BrowserConsoleCaptureEnabled() {
		t.Error("zero WorkbenchConfig should treat nil switches as enabled")
	}

	off := false
	disabled := WorkbenchConfig{
		ContextInjectsGuidance: &off,
		KanbanRunInWorktree:    &off,
		KanbanMCPEnabled:       &off,
		KanbanEntryOrdinary:    &off,
		BrowserConsoleCapture:  &off,
	}
	if disabled.ContextGuidanceEnabled() || disabled.KanbanRunInWorktreeEnabled() ||
		disabled.KanbanMCPToolsEnabled() || disabled.KanbanEntryOrdinarySessions() || disabled.BrowserConsoleCaptureEnabled() {
		t.Error("explicit false switches should disable")
	}

	// 序列化往返：显式 false 的 kanban_entry_ordinary 会写出并能读回。
	data, err := json.Marshal(disabled)
	if err != nil {
		t.Fatalf("marshal WorkbenchConfig: %v", err)
	}
	if !strings.Contains(string(data), `"kanban_entry_ordinary":false`) {
		t.Errorf("expected kanban_entry_ordinary in JSON, got: %s", data)
	}
	var decoded WorkbenchConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal WorkbenchConfig: %v", err)
	}
	if decoded.KanbanEntryOrdinarySessions() {
		t.Error("expected decoded KanbanEntryOrdinarySessions false")
	}
}

func TestNormalizeWorkbenchContextAndKanbanFields(t *testing.T) {
	cfg := Defaults()
	cfg.Workbench.ContextInjectMaxCharacters = 999999
	cfg.Workbench.KanbanDefaultView = "timeline"
	cfg.Workbench.BrowserStartURL = "   "

	cfg.Workbench.StuckNudgeTimeout = 100

	norm := Normalize(cfg)
	if norm.Workbench.ContextInjectMaxCharacters != MaxContextInjectMaxCharacters {
		t.Errorf("ContextInjectMaxCharacters = %d, want clamp %d", norm.Workbench.ContextInjectMaxCharacters, MaxContextInjectMaxCharacters)
	}
	if norm.Workbench.KanbanDefaultView != KanbanViewKanban {
		t.Errorf("KanbanDefaultView = %q, want fallback %q", norm.Workbench.KanbanDefaultView, KanbanViewKanban)
	}
	if norm.Workbench.BrowserStartURL != DefaultBrowserStartURL {
		t.Errorf("BrowserStartURL = %q, want %q", norm.Workbench.BrowserStartURL, DefaultBrowserStartURL)
	}
	if norm.Workbench.StuckNudgeTimeout != MinStuckNudgeTimeout {
		t.Errorf("StuckNudgeTimeout = %d, want clamp %d", norm.Workbench.StuckNudgeTimeout, MinStuckNudgeTimeout)
	}
	if err := Validate(norm); err != nil {
		t.Fatalf("normalized config should pass Validate, got: %v", err)
	}

	zeroed := Defaults()
	zeroed.Workbench.ContextInjectMaxCharacters = 0
	zeroed.Workbench.KanbanDefaultView = ""
	zeroed.Workbench.BrowserStartURL = ""

	normZero := Normalize(zeroed)
	if normZero.Workbench.ContextInjectMaxCharacters != DefaultContextInjectMaxCharacters {
		t.Errorf("ContextInjectMaxCharacters = %d, want default %d", normZero.Workbench.ContextInjectMaxCharacters, DefaultContextInjectMaxCharacters)
	}
	if normZero.Workbench.KanbanDefaultView != KanbanViewKanban {
		t.Errorf("KanbanDefaultView = %q, want default %q", normZero.Workbench.KanbanDefaultView, KanbanViewKanban)
	}
	if normZero.Workbench.BrowserStartURL != DefaultBrowserStartURL {
		t.Errorf("BrowserStartURL = %q, want default %q", normZero.Workbench.BrowserStartURL, DefaultBrowserStartURL)
	}

	lower := Defaults()
	lower.Workbench.ContextInjectMaxCharacters = 1

	lower.Workbench.KanbanDefaultView = KanbanViewList
	normLower := Normalize(lower)
	if normLower.Workbench.ContextInjectMaxCharacters != MinContextInjectMaxCharacters {
		t.Errorf("ContextInjectMaxCharacters = %d, want clamp %d", normLower.Workbench.ContextInjectMaxCharacters, MinContextInjectMaxCharacters)
	}
	if normLower.Workbench.KanbanDefaultView != KanbanViewList {
		t.Errorf("KanbanDefaultView = %q, want %q", normLower.Workbench.KanbanDefaultView, KanbanViewList)
	}
}

func TestMemoryConfigDefaultsAndSwitches(t *testing.T) {
	cfg := Defaults()
	if !cfg.Memory.WorkingMemoryEnabled() {
		t.Error("expected WorkingMemoryEnabled default true")
	}
	if !cfg.Memory.MemoryToolsNoteEnabled() {
		t.Error("expected MemoryToolsNoteEnabled default true")
	}
	if !cfg.Memory.ThreadSyncEnabled() {
		t.Error("expected ThreadSyncEnabled default true")
	}
	if cfg.Memory.WorkingMemoryBudget() != DefaultWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryBudget = %d, want %d", cfg.Memory.WorkingMemoryBudget(), DefaultWorkingMemoryMaxCharacters)
	}
	if got := cfg.Memory.SpaceFor("/tmp/demo"); got != "" {
		t.Errorf("SpaceFor without override = %q, want empty", got)
	}

	// 零值：开关按 nil 视为开启，预算取默认。
	var zero MemoryConfig
	if !zero.WorkingMemoryEnabled() || !zero.MemoryToolsNoteEnabled() || !zero.ThreadSyncEnabled() {
		t.Error("zero MemoryConfig should treat nil switches as enabled")
	}
	if zero.WorkingMemoryBudget() != DefaultWorkingMemoryMaxCharacters {
		t.Errorf("zero WorkingMemoryBudget = %d, want %d", zero.WorkingMemoryBudget(), DefaultWorkingMemoryMaxCharacters)
	}

	// 显式关闭。
	off := false
	disabled := MemoryConfig{InjectWorkingMemory: &off, InjectMemoryToolsNote: &off, SyncThreads: &off}
	if disabled.WorkingMemoryEnabled() || disabled.MemoryToolsNoteEnabled() || disabled.ThreadSyncEnabled() {
		t.Error("explicit false switches should disable")
	}

	// 预算夹值。
	if got := (MemoryConfig{WorkingMemoryMaxCharacters: MinWorkingMemoryMaxCharacters - 1}).WorkingMemoryBudget(); got != MinWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryBudget = %d, want clamp %d", got, MinWorkingMemoryMaxCharacters)
	}
	if got := (MemoryConfig{WorkingMemoryMaxCharacters: MaxWorkingMemoryMaxCharacters + 1}).WorkingMemoryBudget(); got != MaxWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryBudget = %d, want clamp %d", got, MaxWorkingMemoryMaxCharacters)
	}
	if got := (MemoryConfig{WorkingMemoryMaxCharacters: 4000}).WorkingMemoryBudget(); got != 4000 {
		t.Errorf("WorkingMemoryBudget = %d, want 4000", got)
	}

	// SpaceOverrides 命中与未命中。
	withOverride := MemoryConfig{SpaceOverrides: map[string]string{"/tmp/demo": " mcode "}}
	if got := withOverride.SpaceFor("/tmp/demo"); got != "mcode" {
		t.Errorf("SpaceFor hit = %q, want %q", got, "mcode")
	}
	if got := withOverride.SpaceFor("/tmp/other"); got != "" {
		t.Errorf("SpaceFor miss = %q, want empty", got)
	}

	// 序列化往返：显式 false 能写出并读回；空字段省略。
	data, err := json.Marshal(disabled)
	if err != nil {
		t.Fatalf("json.Marshal nowledge failed: %v", err)
	}
	if !strings.Contains(string(data), "\"inject_working_memory\":false") ||
		!strings.Contains(string(data), "\"inject_memory_tools_note\":false") ||
		!strings.Contains(string(data), "\"sync_threads\":false") {
		t.Errorf("explicit false switches must be serialized, got: %s", data)
	}
	if strings.Contains(string(data), "space_overrides") {
		t.Errorf("empty space_overrides must be omitted, got: %s", data)
	}
	var decoded MemoryConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal nowledge failed: %v", err)
	}
	if decoded.WorkingMemoryEnabled() || decoded.MemoryToolsNoteEnabled() || decoded.ThreadSyncEnabled() {
		t.Error("decoded nowledge switches should stay false")
	}
}

func TestNowledgeBeeRecallSwitch(t *testing.T) {
	// 默认与零值都视为开启。
	if !Defaults().Memory.BeeRecallEnabled() {
		t.Error("expected BeeRecallEnabled default true")
	}
	var zero MemoryConfig
	if !zero.BeeRecallEnabled() {
		t.Error("zero MemoryConfig should treat nil bee_recall as enabled")
	}

	// 显式关闭。
	off := false
	disabled := MemoryConfig{BeeRecall: &off}
	if disabled.BeeRecallEnabled() {
		t.Error("explicit false bee_recall should disable")
	}

	// 显式开启。
	on := true
	if !(MemoryConfig{BeeRecall: &on}).BeeRecallEnabled() {
		t.Error("explicit true bee_recall should enable")
	}

	// 序列化：显式 false 写出，nil 省略。
	data, err := json.Marshal(disabled)
	if err != nil {
		t.Fatalf("json.Marshal nowledge failed: %v", err)
	}
	if !strings.Contains(string(data), "\"bee_recall\":false") {
		t.Errorf("explicit false bee_recall must be serialized, got: %s", data)
	}
	var decoded MemoryConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal nowledge failed: %v", err)
	}
	if decoded.BeeRecallEnabled() {
		t.Error("decoded bee_recall should stay false")
	}
	nilData, err := json.Marshal(MemoryConfig{})
	if err != nil {
		t.Fatalf("json.Marshal nowledge failed: %v", err)
	}
	if strings.Contains(string(nilData), "bee_recall") {
		t.Errorf("nil bee_recall must be omitted, got: %s", nilData)
	}
}

func TestNormalizeNowledgeWorkingMemoryBudget(t *testing.T) {
	cfg := Defaults()
	cfg.Memory.WorkingMemoryMaxCharacters = MaxWorkingMemoryMaxCharacters + 1
	if got := Normalize(cfg).Memory.WorkingMemoryMaxCharacters; got != MaxWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryMaxCharacters = %d, want clamp %d", got, MaxWorkingMemoryMaxCharacters)
	}

	zeroed := Defaults()
	zeroed.Memory.WorkingMemoryMaxCharacters = 0
	if got := Normalize(zeroed).Memory.WorkingMemoryMaxCharacters; got != DefaultWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryMaxCharacters = %d, want default %d", got, DefaultWorkingMemoryMaxCharacters)
	}

	lower := Defaults()
	lower.Memory.WorkingMemoryMaxCharacters = 1
	if got := Normalize(lower).Memory.WorkingMemoryMaxCharacters; got != MinWorkingMemoryMaxCharacters {
		t.Errorf("WorkingMemoryMaxCharacters = %d, want clamp %d", got, MinWorkingMemoryMaxCharacters)
	}

	// Normalize 不能改掉人工设置的 space 覆盖。
	keep := Defaults()
	keep.Memory.SpaceOverrides = map[string]string{"/tmp/demo": "mcode"}
	if got := Normalize(keep).Memory.SpaceFor("/tmp/demo"); got != "mcode" {
		t.Errorf("SpaceFor after Normalize = %q, want %q", got, "mcode")
	}
}

func TestWorkbenchOpenInAppsDefaults(t *testing.T) {
	cfg := Defaults()
	if len(cfg.Workbench.OpenInApps) != 0 {
		t.Fatalf("OpenInApps should stay empty in Defaults, got %+v", cfg.Workbench.OpenInApps)
	}

	apps := cfg.Workbench.OpenInAppsOrDefault()
	if len(apps) == 0 {
		t.Fatal("OpenInAppsOrDefault should fall back to the default list")
	}
	byName := make(map[string]OpenInAppConfig, len(apps))
	for _, app := range apps {
		byName[app.Name] = app
		if !app.Enabled {
			t.Errorf("%s should be enabled by default", app.Name)
		}
	}
	for _, name := range []string{"Visual Studio Code", "Cursor", "Zed", "Xcode", "Windsurf", "Sublime Text"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("default open-in apps missing %s", name)
		}
	}
	if iterm := byName["iTerm2"]; iterm.BundleID != "com.googlecode.iterm2" || iterm.AppPath != "/Applications/iTerm.app" {
		t.Errorf("iTerm2 default = %+v", iterm)
	}
	if qs := byName["QSpace Pro"]; qs.AppPath != "/Applications/QSpace Pro.app" || qs.BundleID == "" {
		t.Errorf("QSpace Pro default = %+v", qs)
	}
	if byName["Visual Studio Code"].BundleID != "com.microsoft.VSCode" {
		t.Errorf("Visual Studio Code bundle id = %q", byName["Visual Studio Code"].BundleID)
	}

	// 返回的是副本：修改结果不能影响默认列表。
	apps[0].Enabled = false
	if again := cfg.Workbench.OpenInAppsOrDefault(); !again[0].Enabled {
		t.Error("OpenInAppsOrDefault should return a copy of the defaults")
	}

	// 已配置时使用配置列表。
	custom := WorkbenchConfig{OpenInApps: []OpenInAppConfig{
		{Name: "访达"},
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
	}}
	got := custom.OpenInAppsOrDefault()
	if len(got) != 2 || got[0].Name != "访达" || got[1].AppPath != "/Applications/Cursor.app" {
		t.Fatalf("OpenInAppsOrDefault with configured list = %+v", got)
	}
}

// Windows 默认打开方式列表：只用 exe 路径，不含 macOS 专属应用。
func TestDefaultOpenInAppsWindows(t *testing.T) {
	localAppData := filepath.Join("C:", "Users", "me", "AppData", "Local")
	programFiles := filepath.Join("C:", "Program Files")
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("ProgramFiles", programFiles)

	apps := defaultOpenInAppsForGOOS("windows")
	if len(apps) != 5 {
		t.Fatalf("windows default open-in apps = %+v", apps)
	}
	byName := make(map[string]OpenInAppConfig, len(apps))
	for _, app := range apps {
		byName[app.Name] = app
		if app.BundleID != "" {
			t.Errorf("%s should not have a bundle id on windows", app.Name)
		}
		if !strings.HasSuffix(strings.ToLower(app.AppPath), ".exe") {
			t.Errorf("%s path should be an exe, got %q", app.Name, app.AppPath)
		}
	}
	for _, name := range []string{"Visual Studio Code", "Cursor", "Zed", "Windsurf", "Sublime Text"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("windows default open-in apps missing %s", name)
		}
	}
	for _, name := range []string{"Xcode", "iTerm2", "Terminal", "Ghostty", "QSpace Pro"} {
		if _, ok := byName[name]; ok {
			t.Errorf("windows default open-in apps should not contain %s", name)
		}
	}
	if got, want := byName["Cursor"].AppPath, filepath.Join(localAppData, "Programs", "cursor", "Cursor.exe"); got != want {
		t.Errorf("Cursor default path = %q, want %q", got, want)
	}
}

func TestNormalizeOpenInApps(t *testing.T) {
	cfg := Defaults()
	cfg.Workbench.OpenInApps = []OpenInAppConfig{
		{Name: "  Cursor ", AppPath: " /Applications/Cursor.app ", BundleID: " com.todesktop.230313mzl4w4u92 ", Enabled: true},
		{},
		{Name: "", AppPath: "/Applications/Zed.app"},
	}
	norm := Normalize(cfg)
	apps := norm.Workbench.OpenInApps
	if len(apps) != 2 {
		t.Fatalf("normalized OpenInApps = %+v, want 2 entries", apps)
	}
	if apps[0].Name != "Cursor" || apps[0].AppPath != "/Applications/Cursor.app" || apps[0].BundleID != "com.todesktop.230313mzl4w4u92" {
		t.Errorf("first entry not trimmed: %+v", apps[0])
	}
	if apps[1].Name != "Zed" {
		t.Errorf("entry without name should be named after the app, got %q", apps[1].Name)
	}
	if err := Validate(norm); err != nil {
		t.Fatalf("normalized config should pass Validate, got: %v", err)
	}

	empty := Defaults()
	empty.Workbench.OpenInApps = []OpenInAppConfig{{}, {Name: "  "}}
	if got := Normalize(empty).Workbench.OpenInApps; len(got) != 0 {
		t.Errorf("empty entries should collapse to nil/default, got %+v", got)
	}
}

func TestValidateWorkbenchContextAndKanbanFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"context inject too small", func(c *Config) { c.Workbench.ContextInjectMaxCharacters = MinContextInjectMaxCharacters - 1 }},
		{"context inject too large", func(c *Config) { c.Workbench.ContextInjectMaxCharacters = MaxContextInjectMaxCharacters + 1 }},
		{"unknown kanban view", func(c *Config) { c.Workbench.KanbanDefaultView = "timeline" }},

		{"stuck nudge below new minimum", func(c *Config) { c.Workbench.StuckNudgeTimeout = MinStuckNudgeTimeout - 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatalf("expected Validate to reject %s", tc.name)
			}
		})
	}

	ok := Defaults()
	ok.Workbench.StuckNudgeTimeout = MinStuckNudgeTimeout
	ok.Workbench.KanbanDefaultView = KanbanViewList
	if err := Validate(ok); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

// 21. general.provider 接受模型选择器能写回的全部 provider（CLI 家族、ACP、预设、自定义端点），
// 只有真正未知的 ID 才报错。
func TestValidateAcceptsSelectableProviders(t *testing.T) {
	cfg := Defaults()
	if !cfg.Providers.Mcode.Enabled {
		t.Fatal("mcode 应默认启用")
	}
	if !IsKnownProvider(cfg, ProviderMcode) {
		t.Fatal("mcode 应为已知 provider")
	}
	cfg.Providers.ACP = map[string]ProviderConfig{"cursor": {Enabled: true}}
	cfg.Providers.Presets = map[string]PresetProviderConfig{"deepseek": {Enabled: true}}
	cfg.Providers.Custom = []CustomEndpointConfig{{ID: "local", Name: "Local", Enabled: true}}

	ids := []string{
		ProviderClaude, ProviderCodex, ProviderAgy, ProviderMemory,
		ProviderCopilot, ProviderPi, ProviderMcode, ProviderOmp, ProviderCommandCode,
		"cline", "cursor", "droid",
		"deepseek",
		CustomEndpointProviderKind(cfg.Providers.Custom[0]),
	}
	for _, id := range ids {
		cfg.Models.Provider = id
		if err := Validate(cfg); err != nil {
			t.Errorf("provider %q 应被接受，实际报错: %v", id, err)
		}
	}

	cfg.Models.Provider = "unsupported_provider"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "models.provider") {
		t.Fatalf("未知 provider 应被拒绝并点名字段，实际: %v", err)
	}
}

// 22. 磁盘上的非法字段不再让 Load 整体失败：非法值重置为默认值并返回警告，
// 合法字段原样保留，Update 仍能存下去。
func TestLoadResetsInvalidFields(t *testing.T) {
	dir := t.TempDir()
	raw := `{
		"models": {"provider": "unsupported_provider"},
		"permissions": {"mode": "full_power"},
		"appearance": {"theme": "cyberpunk"},
		"workbench": {"kanban_default_view": "list"}
	}`
	if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	for _, load := range []struct {
		name string
		run  func() (Config, []string, error)
	}{
		{"LoadWithWarnings", func() (Config, []string, error) { return LoadWithWarnings(dir) }},
		{"Load", func() (Config, []string, error) {
			cfg, err := Load(dir)
			return cfg, nil, err
		}},
	} {
		t.Run(load.name, func(t *testing.T) {
			cfg, warnings, err := load.run()
			if err != nil {
				t.Fatalf("非法字段不应让 Load 失败: %v", err)
			}
			want := Defaults()
			if cfg.Models.Provider != want.Models.Provider {
				t.Errorf("models.provider 应重置为 %q，实际 %q", want.Models.Provider, cfg.Models.Provider)
			}
			if cfg.Permissions.Mode != ModeSupervised {
				t.Errorf("permissions.mode 非法时应回退为 %q，实际 %q", ModeSupervised, cfg.Permissions.Mode)
			}
			if cfg.Appearance.Theme != want.Appearance.Theme {
				t.Errorf("appearance.theme 应重置为 %q，实际 %q", want.Appearance.Theme, cfg.Appearance.Theme)
			}
			// 合法字段保持原值（Normalize 负责的枚举回落不算重置）。
			if cfg.Workbench.KanbanDefaultView != KanbanViewList {
				t.Errorf("合法的 workbench.kanban_default_view 应保留，实际 %q", cfg.Workbench.KanbanDefaultView)
			}
			if load.name == "LoadWithWarnings" {
				for _, field := range []string{"models.provider", "permissions.mode", "appearance.theme"} {
					found := false
					for _, w := range warnings {
						if strings.Contains(w, field) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("警告中缺少字段 %q，实际: %v", field, warnings)
					}
				}
			}
		})
	}

	if err := Update(dir, func(c *Config) error {
		c.Appearance.Theme = ThemeDark
		return nil
	}); err != nil {
		t.Fatalf("Update 不应因磁盘上的非法字段失败: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 保存后的配置失败: %v", err)
	}
	if saved.Appearance.Theme != ThemeDark {
		t.Fatalf("Update 未生效，theme = %q", saved.Appearance.Theme)
	}
}

// TestSourceControlCloneIgnored 覆盖克隆依赖目录开关与名单的默认值、解析与去重。
func TestSourceControlCloneIgnored(t *testing.T) {
	var nilCfg SourceControlConfig
	if !nilCfg.CloneIgnoredOnWorktreeEnabled() {
		t.Errorf("expected CloneIgnoredOnWorktreeEnabled() true when nil, got false")
	}
	if got := nilCfg.CloneIgnoredPatternsText(); got != DefaultCloneIgnoredPatterns {
		t.Errorf("expected default patterns %q, got %q", DefaultCloneIgnoredPatterns, got)
	}
	def := nilCfg.CloneIgnoredPatternList()
	if len(def) != 8 {
		t.Fatalf("expected 8 default patterns, got %d: %v", len(def), def)
	}
	for i, want := range []string{"node_modules", ".env", ".env.*", ".next", ".turbo", "target", "Pods", ".gradle"} {
		if def[i] != want {
			t.Errorf("default pattern[%d] = %q, want %q", i, def[i], want)
		}
	}

	disabled := false
	custom := " node_modules, ,.env ,node_modules"
	cfg := SourceControlConfig{CloneIgnoredOnWorktree: &disabled, CloneIgnoredPatterns: &custom}
	if cfg.CloneIgnoredOnWorktreeEnabled() {
		t.Errorf("expected CloneIgnoredOnWorktreeEnabled() false when explicitly false, got true")
	}
	if got := cfg.CloneIgnoredPatternsText(); got != custom {
		t.Errorf("expected raw patterns text %q, got %q", custom, got)
	}
	got := cfg.CloneIgnoredPatternList()
	want := []string{"node_modules", ".env"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CloneIgnoredPatternList() = %v, want %v", got, want)
	}
}

// 终端字号：默认值参与 Defaults()，Normalize 把零值与越界值夹到合法区间，JSON 键名为 terminal_font_size。

func TestNowledgeThreadScopeSwitches(t *testing.T) {
	// 默认与零值都视为关闭（bee 子会话、自动化运行会话默认不同步）。
	if Defaults().Memory.HelperThreadSyncEnabled() {
		t.Error("expected HelperThreadSyncEnabled default false")
	}
	if Defaults().Memory.AutomationThreadSyncEnabled() {
		t.Error("expected AutomationThreadSyncEnabled default false")
	}
	var zero MemoryConfig
	if zero.HelperThreadSyncEnabled() || zero.AutomationThreadSyncEnabled() {
		t.Error("zero MemoryConfig should treat nil switches as disabled")
	}

	// 显式开启。
	on := true
	enabled := MemoryConfig{SyncHelperThreads: &on, SyncAutomationThreads: &on}
	if !enabled.HelperThreadSyncEnabled() || !enabled.AutomationThreadSyncEnabled() {
		t.Error("explicit true switches should enable")
	}

	// 序列化往返：显式 true 写出并读回。
	data, err := json.Marshal(enabled)
	if err != nil {
		t.Fatalf("json.Marshal nowledge failed: %v", err)
	}
	for _, want := range []string{"\"sync_helper_threads\":true", "\"sync_automation_threads\":true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("explicit true switch %s must be serialized, got: %s", want, data)
		}
	}
	var decoded MemoryConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal nowledge failed: %v", err)
	}
	if !decoded.HelperThreadSyncEnabled() || !decoded.AutomationThreadSyncEnabled() {
		t.Error("decoded switches should stay true")
	}

	// nil 省略。
	nilData, err := json.Marshal(MemoryConfig{})
	if err != nil {
		t.Fatalf("json.Marshal nowledge failed: %v", err)
	}
	for _, unwanted := range []string{"sync_helper_threads", "sync_automation_threads"} {
		if strings.Contains(string(nilData), unwanted) {
			t.Errorf("nil %s must be omitted, got: %s", unwanted, nilData)
		}
	}
}

// ProviderConfig 的 pi 专有设置（长缓存、视觉模型）JSON 往返：设置值保留，未设置时省略。
func TestProviderConfig_PiTuningJSONRoundtrip(t *testing.T) {
	want := ProviderConfig{
		Enabled:       true,
		Bin:           "/opt/homebrew/bin/pi",
		PiCacheLong:   true,
		PiVisionModel: "anthropic/claude-haiku-4-5",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	for _, key := range []string{`"piCacheLong":true`, `"piVisionModel":"anthropic/claude-haiku-4-5"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("序列化结果应包含 %s，got: %s", key, data)
		}
	}

	var got ProviderConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("往返后不一致: got %+v, want %+v", got, want)
	}

	// 未设置时两项都应省略（omitempty）。
	empty, err := json.Marshal(ProviderConfig{})
	if err != nil {
		t.Fatalf("json.Marshal empty failed: %v", err)
	}
	for _, key := range []string{"piCacheLong", "piVisionModel"} {
		if strings.Contains(string(empty), key) {
			t.Errorf("未设置时 %s 应被省略，got: %s", key, empty)
		}
	}
}

// 32. StalledAlertEnabled / StalledAlertAfter：nil 与 0 走默认值，负数阈值回退默认 10 分钟。
func TestGeneralStalledAlertSettings(t *testing.T) {
	bTrue := true
	bFalse := false

	for _, tc := range []struct {
		name        string
		notify      *bool
		minutes     int
		wantEnabled bool
		wantAfter   time.Duration
	}{
		{"默认开启 10 分钟", nil, 0, true, 10 * time.Minute},
		{"显式关闭", &bFalse, 0, false, 10 * time.Minute},
		{"显式开启", &bTrue, 0, true, 10 * time.Minute},
		{"负数回退默认", nil, -5, true, 10 * time.Minute},
		{"自定义阈值", nil, 3, true, 3 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NotificationsConfig{NotifyWhenStalled: tc.notify, StalledAlertMinutes: tc.minutes}
			if got := g.StalledAlertEnabled(); got != tc.wantEnabled {
				t.Errorf("StalledAlertEnabled() = %v, want %v", got, tc.wantEnabled)
			}
			if got := g.StalledAlertAfter(); got != tc.wantAfter {
				t.Errorf("StalledAlertAfter() = %v, want %v", got, tc.wantAfter)
			}
		})
	}

	if got := Defaults().Notifications.StalledAlertAfter(); got != 10*time.Minute {
		t.Errorf("默认 StalledAlertAfter() = %v, want 10m", got)
	}
	if !Defaults().Notifications.StalledAlertEnabled() {
		t.Error("默认 StalledAlertEnabled() 应为 true")
	}
}

// StewardConfig 默认值、身份归一与主目录回退。
func TestStewardConfigDefaultsAndNormalize(t *testing.T) {
	def := Defaults().Orchestration.Steward
	if !def.Enabled {
		t.Error("总管默认应为开启")
	}
	if def.Provider != "" || def.Model != "" || def.HomeDir != "" {
		t.Errorf("总管默认 provider/model/home_dir 应为空，got %+v", def)
	}
	if def.Name != "MoCo" || def.Avatar != "🤖" {
		t.Errorf("总管默认 name/avatar 不正确，got name=%q avatar=%q", def.Name, def.Avatar)
	}
	if got := def.DisplayName(); got != "MoCo" {
		t.Errorf("默认 DisplayName() = %q，想要 MoCo", got)
	}
	if got := def.ResolvedHomeDir("/data"); got != filepath.Join("/data", "steward") {
		t.Errorf("默认 ResolvedHomeDir() = %q，想要 /data/steward", got)
	}
	if got := (StewardConfig{HomeDir: " /custom "}).ResolvedHomeDir("/data"); got != "/custom" {
		t.Errorf("自定义 ResolvedHomeDir() = %q，想要 /custom", got)
	}

	cfg := Defaults()
	cfg.Orchestration.Steward.Provider = " claude "
	cfg.Orchestration.Steward.Model = " opus "
	cfg = Normalize(cfg)
	if cfg.Orchestration.Steward.Provider != "claude" || cfg.Orchestration.Steward.Model != "opus" {
		t.Errorf("provider/model 未去空白：%+v", cfg.Orchestration.Steward)
	}

	// 名字去空白；头像只保留前 2 个 rune（组合 emoji 占两个 rune，所以要留两个）。
	cfg = Defaults()
	cfg.Orchestration.Steward.Name = "   "
	cfg.Orchestration.Steward.Avatar = " 🤖🤖🤖 "
	cfg = Normalize(cfg)
	if cfg.Orchestration.Steward.Name != "" || cfg.Orchestration.Steward.Avatar != "🤖🤖" {
		t.Errorf("name/avatar 未按约定归一：name=%q avatar=%q", cfg.Orchestration.Steward.Name, cfg.Orchestration.Steward.Avatar)
	}
	if got := cfg.Orchestration.Steward.DisplayName(); got != DefaultStewardName {
		t.Errorf("空白名字归一后 DisplayName() = %q，想要 %q", got, DefaultStewardName)
	}
	cfg.Orchestration.Steward.Name = " 小助 "
	cfg = Normalize(cfg)
	if got := cfg.Orchestration.Steward.DisplayName(); got != "小助" {
		t.Errorf("自定义名字归一后 DisplayName() = %q，想要 小助", got)
	}
}

// DisplayName：去掉首尾空白后为空时用默认名字，否则用配置里的名字。
func TestStewardDisplayName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"", DefaultStewardName},
		{"   ", DefaultStewardName},
		{" 小助 ", "小助"},
	}
	for _, c := range cases {
		if got := (StewardConfig{Name: c.name}).DisplayName(); got != c.want {
			t.Errorf("DisplayName(%q) = %q，想要 %q", c.name, got, c.want)
		}
	}
}

// 33b. Nickname / DisplayName / Label / AvatarChar 在昵称与头像的四种空与非空组合下的返回值。
func TestStewardNicknameLabelAvatar(t *testing.T) {
	cases := []struct {
		label   string
		name    string
		avatar  string
		nick    string
		display string
		want    string
		char    string
	}{
		{"昵称空+头像空", "", "", "", DefaultStewardName, DefaultStewardName, "总"},
		{"昵称阿默+头像空", "阿默", "", "阿默", "阿默", "阿默 · 总管", "阿"},
		{"昵称带空格+头像空", " 阿默 ", "", "阿默", "阿默", "阿默 · 总管", "阿"},
		{"昵称空+头像✦", "", "✦", "", DefaultStewardName, DefaultStewardName, "✦"},
		{"昵称阿默+头像✦", "阿默", "✦", "阿默", "阿默", "阿默 · 总管", "✦"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			s := StewardConfig{Name: c.name, Avatar: c.avatar}
			if got := s.Nickname(); got != c.nick {
				t.Errorf("Nickname() = %q，想要 %q", got, c.nick)
			}
			if got := s.DisplayName(); got != c.display {
				t.Errorf("DisplayName() = %q，想要 %q", got, c.display)
			}
			if got := s.Label(); got != c.want {
				t.Errorf("Label() = %q，想要 %q", got, c.want)
			}
			if got := s.AvatarChar(); got != c.char {
				t.Errorf("AvatarChar() = %q，想要 %q", got, c.char)
			}
		})
	}
}

// 33a. 旧配置里的 permission_tier 键不再使用：能正常加载，保存后不再写回。
func TestStewardConfigIgnoresRemovedTierKey(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{
		"version": 7,
		"general": {"provider": "codex"},
		"steward": {"enabled": true, "name": "小助", "permission_tier": "act"}
	}`)
	if err := os.WriteFile(ConfigPath(dir), legacy, 0o600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("带头 permission_tier 的配置应能加载: %v", err)
	}
	if cfg.Orchestration.Steward.DisplayName() != "小助" || !cfg.Orchestration.Steward.Enabled {
		t.Fatalf("其余字段应照常加载，got %+v", cfg.Orchestration.Steward)
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	raw, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		t.Fatalf("读取保存后的配置失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析保存后的配置失败: %v", err)
	}
	orchestration, _ := out["orchestration"].(map[string]any)
	steward, _ := orchestration["steward"].(map[string]any)
	if _, exists := steward["permission_tier"]; exists {
		t.Errorf("保存后不应再写回 permission_tier: %s", raw)
	}
	if _, exists := steward["tier"]; exists {
		t.Errorf("保存后不应出现 tier 键: %s", raw)
	}
}

// 34. 旧配置文件没有 steward 键时，加载后总管按新的默认值开启，其余字段照常加载。
func TestStewardLegacyConfigWithoutKey(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{
		"version": 4,
		"general": {"provider": "codex"},
		"swarm": {"max_bees": 2}
	}`)
	if err := os.WriteFile(ConfigPath(dir), legacy, 0o600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 旧配置失败: %v", err)
	}
	if !cfg.Orchestration.Steward.Enabled {
		t.Error("旧配置无 steward 键时应按默认值开启总管")
	}
	if cfg.Orchestration.MaxBees != 2 {
		t.Errorf("旧配置其余字段应照常加载，Swarm.MaxBees = %d", cfg.Orchestration.MaxBees)
	}
}

func TestLoadConfigIgnoresRemovedStewardKeys(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{
		"version": 9,
		"orchestration": {"steward": {
			"enabled": true,
			"enabled_choice": true,
			"name": "前台",
			"cli_fallback": true,
			"daily_wake_limit": 30,
			"daily_report": true,
			"daily_report_at": "18:00",
			"event_policies": {"automation_finished": "patrol"},
			"rotate_idle_minutes": 60,
			"rotate_context_tokens": 120000,
			"passthrough_off": true,
			"auto_collapse_reasoning_off": true,
			"effort": "high",
			"plan_provider": "claude",
			"plan_model": "opus",
			"plan_effort": "high"
		}}
	}`)
	if err := os.WriteFile(ConfigPath(dir), legacy, 0o600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("加载带旧设置键的配置失败: %v", err)
	}
	if !cfg.Orchestration.Steward.Enabled || !cfg.Orchestration.Steward.EnabledChoice ||
		cfg.Orchestration.Steward.Name != "前台" || !cfg.Orchestration.Steward.CLIFallback {
		t.Fatalf("保留的前台设置未正常加载: %+v", cfg.Orchestration.Steward)
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	saved, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		t.Fatalf("读取保存后的配置失败: %v", err)
	}
	var savedDoc map[string]any
	if err := json.Unmarshal(saved, &savedDoc); err != nil {
		t.Fatalf("解析保存后的配置失败: %v", err)
	}
	orchestration, _ := savedDoc["orchestration"].(map[string]any)
	steward, _ := orchestration["steward"].(map[string]any)
	if steward == nil {
		t.Fatalf("保存后的配置缺少 orchestration.steward: %s", saved)
	}
	for _, key := range []string{
		"daily_wake_limit", "daily_report", "daily_report_at", "event_policies",
		"rotate_idle_minutes", "rotate_context_tokens", "passthrough_off",
		"auto_collapse_reasoning_off", "effort", "plan_provider", "plan_model", "plan_effort",
	} {
		if _, ok := steward[key]; ok {
			t.Errorf("保存后 steward 仍包含已删除配置键 %q: %s", key, saved)
		}
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("再次加载保存后的配置失败: %v", err)
	}
}

// 34a. v6->v7 迁移：总管改为默认开启，但不动用户亲手切换过的开关。
func TestMigrateV6ToV7StewardEnabled(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantEnabled bool
		wantChoice  bool
	}{
		{
			name:        "无标记时打开",
			body:        `{"version": 6, "general": {"provider": "codex"}, "steward": {"enabled": false, "name": "小助"}}`,
			wantEnabled: true,
			wantChoice:  false,
		},
		{
			name:        "用户亲手关过时保持关闭",
			body:        `{"version": 6, "general": {"provider": "codex"}, "steward": {"enabled": false, "enabled_choice": true}}`,
			wantEnabled: false,
			wantChoice:  true,
		},
		{
			name:        "没有 steward 段",
			body:        `{"version": 6, "general": {"provider": "codex"}}`,
			wantEnabled: true,
			wantChoice:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(ConfigPath(dir), []byte(c.body), 0o600); err != nil {
				t.Fatalf("写入 v6 配置失败: %v", err)
			}

			cfg, err := Load(dir)
			if err != nil {
				t.Fatalf("Load v6 配置失败: %v", err)
			}
			if cfg.Version != Version {
				t.Errorf("迁移后版本 = %d，想要 %d", cfg.Version, Version)
			}
			if cfg.Orchestration.Steward.Enabled != c.wantEnabled {
				t.Errorf("迁移后 Steward.Enabled = %v，想要 %v", cfg.Orchestration.Steward.Enabled, c.wantEnabled)
			}
			if cfg.Orchestration.Steward.EnabledChoice != c.wantChoice {
				t.Errorf("迁移后 Steward.EnabledChoice = %v，想要 %v（迁移不写标记）", cfg.Orchestration.Steward.EnabledChoice, c.wantChoice)
			}
		})
	}
}

// 35. ParseClock：严格按 HH:MM 解析，越界、空串与非数字一律非法。
func TestParseClock(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"00:00", 0, true},
		{"23:59", 23*60 + 59, true},
		{"08:30", 8*60 + 30, true},
		{"8:05", 8*60 + 5, true},
		{"  22:15 ", 22*60 + 15, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"-1:00", 0, false},
		{"", 0, false},
		{"   ", 0, false},
		{"ab:cd", 0, false},
		{"1200", 0, false},
		{"12:00:00", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseClock(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseClock(%q) = (%d, %v)，想要 (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// 36. QuietWindow：不跨午夜、跨午夜两段、边界开闭与结束时刻计算。
func TestStewardQuietWindow(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	day := StewardConfig{QuietStart: "12:00", QuietEnd: "14:00"}
	night := StewardConfig{QuietStart: "23:00", QuietEnd: "08:00"}
	cases := []struct {
		name    string
		cfg     StewardConfig
		now     time.Time
		want    bool
		wantEnd time.Time
	}{
		{"不跨午夜时段内", day, at(30, 13, 0), true, at(30, 14, 0)},
		{"不跨午夜时段前", day, at(30, 11, 59), false, time.Time{}},
		{"恰在开始时刻", day, at(30, 12, 0), true, at(30, 14, 0)},
		{"恰在结束时刻", day, at(30, 14, 0), false, time.Time{}},
		{"跨午夜夜里一段", night, at(30, 23, 30), true, at(31, 8, 0)},
		{"跨午夜开始时刻", night, at(30, 23, 0), true, at(31, 8, 0)},
		{"跨午夜凌晨一段", night, at(31, 3, 0), true, at(31, 8, 0)},
		{"跨午夜结束时刻", night, at(31, 8, 0), false, time.Time{}},
		{"跨午夜白天", night, at(30, 15, 0), false, time.Time{}},
		{"关闭", StewardConfig{}, at(30, 13, 0), false, time.Time{}},
		{"只填一端", StewardConfig{QuietStart: "12:00"}, at(30, 13, 0), false, time.Time{}},
		{"首尾相等", StewardConfig{QuietStart: "12:00", QuietEnd: "12:00"}, at(30, 12, 0), false, time.Time{}},
		{"非法值", StewardConfig{QuietStart: "x", QuietEnd: "14:00"}, at(30, 13, 0), false, time.Time{}},
	}
	for _, c := range cases {
		got, end := c.cfg.QuietWindow(c.now)
		if got != c.want || !end.Equal(c.wantEnd) {
			t.Errorf("%s：QuietWindow(%v) = (%v, %v)，想要 (%v, %v)", c.name, c.now, got, end, c.want, c.wantEnd)
		}
		if got && end.Location() != loc {
			t.Errorf("%s：结束时刻时区 = %v，想要 %v", c.name, end.Location(), loc)
		}
	}
}

// 37. Normalize：安静时段非法或首尾相等时清空，合法时规范成两位小时两位分钟。
func TestStewardQuietNormalize(t *testing.T) {
	cases := []struct {
		start, end         string
		wantStart, wantEnd string
	}{
		{" 8:00 ", "9:5", "", ""},
		{" 8:00 ", "09:05", "08:00", "09:05"},
		{"23:00", "08:00", "23:00", "08:00"},
		{"25:00", "08:00", "", ""},
		{"23:00", "", "", ""},
		{"10:00", "10:00", "", ""},
		{"", "", "", ""},
	}
	for _, c := range cases {
		cfg := Defaults()
		cfg.Orchestration.Steward.QuietStart, cfg.Orchestration.Steward.QuietEnd = c.start, c.end
		cfg = Normalize(cfg)
		if cfg.Orchestration.Steward.QuietStart != c.wantStart || cfg.Orchestration.Steward.QuietEnd != c.wantEnd {
			t.Errorf("Normalize(%q, %q) = (%q, %q)，想要 (%q, %q)", c.start, c.end,
				cfg.Orchestration.Steward.QuietStart, cfg.Orchestration.Steward.QuietEnd, c.wantStart, c.wantEnd)
		}
	}
}

// NoProgressAfter：零值与负数都回退默认 45 分钟，显式配置生效，开关关闭时返回 0。
func TestStewardNoProgressAfterDefault(t *testing.T) {
	for _, minutes := range []int{0, -5} {
		cfg := StewardConfig{NoProgressMinutes: minutes}
		if got := cfg.NoProgressAfter(); got != DefaultNoProgressMinutes*time.Minute {
			t.Fatalf("NoProgressMinutes=%d 应回退默认 %d 分钟，实际为 %v", minutes, DefaultNoProgressMinutes, got)
		}
	}
}

func TestStewardNoProgressAfterCustom(t *testing.T) {
	cfg := StewardConfig{NoProgressMinutes: 20}
	if got := cfg.NoProgressAfter(); got != 20*time.Minute {
		t.Fatalf("自定义阈值应为 20 分钟，实际为 %v", got)
	}
}

func TestStewardNoProgressAfterOff(t *testing.T) {
	cfg := StewardConfig{NoProgressMinutes: 20, NoProgressOff: true}
	if got := cfg.NoProgressAfter(); got != 0 {
		t.Fatalf("关闭看门狗应返回 0，实际为 %v", got)
	}
}

// 36. RetentionConfig：retention 段缺失时 bee 消息保留任务默认开启，
// 显式 false 能读出来，Save 后重新 Load 值不变，且空值不写入 JSON。
func TestRetentionBeeMessages(t *testing.T) {
	t.Run("没有 retention 段时默认开启", func(t *testing.T) {
		dir := t.TempDir()
		raw := `{"version": 8}`
		if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
			t.Fatalf("写配置文件失败: %v", err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if !cfg.Retention.BeeMessagesEnabled() {
			t.Fatalf("没有 retention 段时应默认开启")
		}
		if cfg.Retention.BeeMessages != nil {
			t.Fatalf("没有 retention 段时不应被赋默认指针: %v", *cfg.Retention.BeeMessages)
		}
		if got := Defaults().Retention.BeeMessagesEnabled(); !got {
			t.Fatalf("Defaults() 的 Retention 应为开启")
		}
	})

	t.Run("显式 false 能读出", func(t *testing.T) {
		dir := t.TempDir()
		raw := `{"version": 8, "retention": {"bee_messages": false}}`
		if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
			t.Fatalf("写配置文件失败: %v", err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Retention.BeeMessagesEnabled() {
			t.Fatalf("bee_messages 为 false 时应读出关闭")
		}
	})

	t.Run("保存后重新读取值不变", func(t *testing.T) {
		dir := t.TempDir()
		cfg := Defaults()
		off := false
		cfg.Retention.BeeMessages = &off
		if err := Save(dir, cfg); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatalf("重新 Load 失败: %v", err)
		}
		if loaded.Retention.BeeMessagesEnabled() {
			t.Fatalf("保存的 bee_messages=false 未能读回")
		}

		on := true
		loaded.Retention.BeeMessages = &on
		if err := Save(dir, loaded); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
		loaded, err = Load(dir)
		if err != nil {
			t.Fatalf("重新 Load 失败: %v", err)
		}
		if !loaded.Retention.BeeMessagesEnabled() {
			t.Fatalf("保存的 bee_messages=true 未能读回")
		}
	})

	t.Run("未设置时不写入 JSON", func(t *testing.T) {
		data, err := json.Marshal(Defaults())
		if err != nil {
			t.Fatalf("json.Marshal defaults 失败: %v", err)
		}
		if strings.Contains(string(data), "bee_messages") {
			t.Fatalf("未设置 bee_messages 时不应写入 JSON: %s", data)
		}
	})
}

// RetentionConfig：冷归档开关缺省为开启，显式 false 能读出来，JSON 往返值不变。
func TestRetentionColdArchive(t *testing.T) {
	if !Defaults().Retention.ColdArchiveEnabled() {
		t.Fatalf("Defaults() 的冷归档应为开启")
	}

	t.Run("没有 retention 段时默认开启", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(ConfigPath(dir), []byte(`{"version": 8}`), 0o600); err != nil {
			t.Fatalf("写配置文件失败: %v", err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if !cfg.Retention.ColdArchiveEnabled() {
			t.Fatalf("没有 retention 段时应默认开启")
		}
		if cfg.Retention.ColdArchive != nil {
			t.Fatalf("没有 retention 段时不应被赋默认指针: %v", *cfg.Retention.ColdArchive)
		}
	})

	t.Run("显式 false 能读出", func(t *testing.T) {
		dir := t.TempDir()
		raw := `{"version": 8, "retention": {"cold_archive": false}}`
		if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
			t.Fatalf("写配置文件失败: %v", err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Retention.ColdArchiveEnabled() {
			t.Fatalf("cold_archive 为 false 时应读出关闭")
		}
	})

	t.Run("保存后重新读取值不变", func(t *testing.T) {
		dir := t.TempDir()
		cfg := Defaults()
		off := false
		cfg.Retention.ColdArchive = &off
		if err := Save(dir, cfg); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatalf("重新 Load 失败: %v", err)
		}
		if loaded.Retention.ColdArchiveEnabled() {
			t.Fatalf("保存的 cold_archive=false 未能读回")
		}

		on := true
		loaded.Retention.ColdArchive = &on
		if err := Save(dir, loaded); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
		loaded, err = Load(dir)
		if err != nil {
			t.Fatalf("重新 Load 失败: %v", err)
		}
		if !loaded.Retention.ColdArchiveEnabled() {
			t.Fatalf("保存的 cold_archive=true 未能读回")
		}
	})
}

// Normalize 对审查漏斗字段的处理：命令去空白并按字节截断，行数上限夹到 0..500。
func TestNormalizeReviewFunnelConfig(t *testing.T) {
	cfg := Defaults()
	cfg.Orchestration.ReviewCheckCommand = "  go test ./...  "
	cfg.Orchestration.ReviewMaxChangedLines = -3
	norm := Normalize(cfg)
	if norm.Orchestration.ReviewCheckCommand != "go test ./..." {
		t.Errorf("检查命令未去空白: %q", norm.Orchestration.ReviewCheckCommand)
	}
	if norm.Orchestration.ReviewMaxChangedLines != 0 {
		t.Errorf("负的行数上限应归 0，得到 %d", norm.Orchestration.ReviewMaxChangedLines)
	}

	cfg = Defaults()
	cfg.Orchestration.ReviewCheckCommand = strings.Repeat("a", MaxReviewCheckCommandLen+20)
	cfg.Orchestration.ReviewMaxChangedLines = MaxReviewMaxChangedLines + 10
	norm = Normalize(cfg)
	if len(norm.Orchestration.ReviewCheckCommand) != MaxReviewCheckCommandLen {
		t.Errorf("超长命令应截断到 %d 字节，得到 %d", MaxReviewCheckCommandLen, len(norm.Orchestration.ReviewCheckCommand))
	}
	if norm.Orchestration.ReviewMaxChangedLines != MaxReviewMaxChangedLines {
		t.Errorf("超限的行数上限应归 %d，得到 %d", MaxReviewMaxChangedLines, norm.Orchestration.ReviewMaxChangedLines)
	}

	// 多字节字符不能按字节硬切出半个 rune。
	cfg = Defaults()
	cfg.Orchestration.ReviewCheckCommand = strings.Repeat("检", MaxReviewCheckCommandLen)
	norm = Normalize(cfg)
	if got := norm.Orchestration.ReviewCheckCommand; len(got) > MaxReviewCheckCommandLen || !strings.HasSuffix(got, "检") {
		t.Errorf("多字节截断结果不合法: len=%d", len(got))
	}
}

// 审查漏斗配置的默认值：新装用户 40，旧配置缺字段读入后也是 40；显式写 0 必须保持 0。
func TestReviewFunnelConfigDefaults(t *testing.T) {
	if got := Defaults().Orchestration.ReviewMaxChangedLines; got != DefaultReviewMaxChangedLines {
		t.Fatalf("Defaults 行数上限 = %d, 期望 %d", got, DefaultReviewMaxChangedLines)
	}
	if got := Defaults().Orchestration.ReviewCheckCommand; got != "" {
		t.Fatalf("Defaults 检查命令 = %q, 期望空", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"version": 9, "orchestration": {"review_mode": "ai"}}`), 0o600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 旧配置失败: %v", err)
	}
	if cfg.Orchestration.ReviewMaxChangedLines != DefaultReviewMaxChangedLines {
		t.Fatalf("旧配置读入的行数上限 = %d, 期望 %d", cfg.Orchestration.ReviewMaxChangedLines, DefaultReviewMaxChangedLines)
	}
	if cfg.Orchestration.ReviewCheckCommand != "" {
		t.Fatalf("旧配置读入的检查命令 = %q, 期望空", cfg.Orchestration.ReviewCheckCommand)
	}

	// 0 表示关闭行数规则，必须能落盘并读回，不能被默认值覆盖。
	cfg.Orchestration.ReviewMaxChangedLines = 0
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("重新 Load 失败: %v", err)
	}
	if reloaded.Orchestration.ReviewMaxChangedLines != 0 {
		t.Fatalf("显式 0 读回后 = %d, 期望 0", reloaded.Orchestration.ReviewMaxChangedLines)
	}
}

func TestLoadLegacyAppearanceMotion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"appearance":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Appearance.ReduceMotion {
		t.Fatal("旧配置默认应显示动效")
	}
}
