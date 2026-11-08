package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// v8SampleV7 是一份「用满 v7 全部段」的样例，用来验证每个字段都落到 v8 的新路径。
func v8SampleV7() map[string]any {
	return map[string]any{
		"version": 7,
		"general": map[string]any{
			"provider":                    "codex",
			"provider_selected":           true,
			"model_list":                  []any{map[string]any{"provider": "codex", "model_id": "gpt-5"}},
			"model_list_limit":            20,
			"model_preferences":           map[string]any{"codex/gpt-5": map[string]any{"effort": "high"}},
			"permission_mode":             "supervised",
			"notify_when_finished":        true,
			"notify_on_approval":          true,
			"notify_when_foreground":      true,
			"play_finish_sound":           true,
			"chime_when_attention_needed": false,
			"dormant_sound":               false,
			"notify_when_stalled":         false,
			"stalled_alert_minutes":       3,
			"show_tray_icon":              true,
			"tray_show_quota":             true,
			"tray_quota_hidden":           []any{"claude"},
			"quit_action":                 "minimize_to_tray",
			"global_shortcut_enabled":     true,
			"global_shortcut":             "CmdOrCtrl+Shift+M",
			"shortcut_overrides":          map[string]any{"dormant-thread": "CmdOrCtrl+Shift+D"},
			"show_reasoning":              false,
			"auto_collapse_reasoning":     false,
			"concise_replies_enabled":     false,
			"queue_while_running":         true,
			"insert_into_running_turn":    true,
			"auto_continue_after_limit":   true,
			"ask_compact_before_resume":   false,
			"thread_finish_action":        "archive",
			"skills_injects_prompt":       false,
			"terminal_shell":              "zsh",
			"tasks_auto_merge_worktree":   false,
			"confirm_before_deleting":     true,
			"sidebar_auto_dormant_days":   14,
		},
		"swarm": map[string]any{
			"enabled":                       true,
			"auto_delegate":                 false,
			"max_bees":                      3,
			"swarm_max_rounds":              9,
			"swarmBeeTimeoutMinutes":        45,
			"swarmRequestTimeoutMinutes":    200,
			"workspace_mode":                "shared",
			"retry_failed":                  true,
			"auto_merge":                    true,
			"review_before_merge":           true,
			"auto_push":                     true,
			"default_provider":              "codex",
			"default_model":                 "gpt-5",
			"default_effort":                "high",
			"pairs":                         []any{},
			"default_tier":                  "deep",
			"always_bees":                   true,
			"lead_plans_only":               true,
			"auto_clear_finished":           true,
			"auto_hides_idle_bees":          true,
			"auto_pops_bees":                true,
			"panel_width":                   420.0,
			"panel_height":                  260.0,
			"archives_codex_bees":           true,
			"auto_forward_question_seconds": 120,
			"delegate_tool":                 true,
			// v8 删掉的死开关，迁移后必须消失。
			"queue_bees":         true,
			"tempers_bee_effort": true,
			"review_bees":        true,
		},
		"steward": map[string]any{
			"enabled":               true,
			"enabled_choice":        true,
			"name":                  "默默",
			"avatar":                "默",
			"provider":              "codex",
			"model":                 "gpt-5",
			"effort":                "high",
			"plan_provider":         "claude",
			"plan_model":            "claude-3-7-sonnet",
			"plan_effort":           "medium",
			"daily_wake_limit":      3,
			"home_dir":              "~/steward",
			"quiet_start":           "23:00",
			"quiet_end":             "08:00",
			"daily_report":          true,
			"daily_report_at":       "09:30",
			"rotate_idle_minutes":   30,
			"rotate_context_tokens": 120000,
		},
		"nowledge": map[string]any{
			"inject_working_memory":         true,
			"inject_memory_tools_note":      false,
			"working_memory_max_characters": 9000,
			"sync_threads":                  false,
			"sync_helper_threads":           true,
			"sync_automation_threads":       true,
			"bee_recall":                    false,
			"space_overrides":               map[string]any{"/tmp/demo": "mcode"},
		},
		// 下列两个段在 v7 里只有死字段：迁移后死字段删掉，段本身因搬入 general 的键而保留。
		"workbench": map[string]any{
			"file_tree_hide_ignored": true,
		},
		"models": map[string]any{
			"temperature":       0.7,
			"context_budget":    200000,
			"max_output_tokens": 32000,
		},
	}
}

func mustMigrateV7ToV8(t *testing.T, raw map[string]any) map[string]any {
	t.Helper()
	out, err := migrateV7ToV8(raw)
	if err != nil {
		t.Fatalf("migrateV7ToV8 failed: %v", err)
	}
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return blob
}

func lookupRaw(t *testing.T, raw map[string]any, path ...string) (any, bool) {
	t.Helper()
	var cur any = raw
	for _, seg := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := obj[seg]
		if !ok {
			return nil, false
		}
		cur = value
	}
	return cur, true
}

// normalizeRawNumbers 把数字递归归一化成 float64。
// 样例 map 里的数字是 Go 字面量（int），经过 JSON 解码就变成 float64，
// 比较前统一类型，避免「20 与 20」这种纯类型差异被当成不等。
func normalizeRawNumbers(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeRawNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeRawNumbers(item)
		}
		return out
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float32:
		return float64(typed)
	default:
		return v
	}
}

func wantRaw(t *testing.T, raw map[string]any, want any, path ...string) {
	t.Helper()
	got, ok := lookupRaw(t, raw, path...)
	if !ok {
		t.Fatalf("缺少 %v", path)
	}
	got = normalizeRawNumbers(got)
	want = normalizeRawNumbers(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v = %v, want %v", path, got, want)
	}
}

// 1. 完整 v7 样例迁移后，每个字段都落到新路径，旧段名消失。
func TestMigrateV7ToV8MovesGeneralSections(t *testing.T) {
	out := mustMigrateV7ToV8(t, v8SampleV7())

	wantRaw(t, out, "codex", "models", "provider")
	wantRaw(t, out, true, "models", "provider_selected")
	wantRaw(t, out, float64(20), "models", "pin_limit")
	wantRaw(t, out, "supervised", "permissions", "mode")
	wantRaw(t, out, true, "notifications", "notify_when_finished")
	wantRaw(t, out, false, "notifications", "chime_when_attention_needed")
	wantRaw(t, out, float64(3), "notifications", "stalled_alert_minutes")
	wantRaw(t, out, true, "startup", "show_tray_icon")
	wantRaw(t, out, "minimize_to_tray", "startup", "quit_action")
	wantRaw(t, out, "CmdOrCtrl+Shift+M", "shortcuts", "global_shortcut")
	wantRaw(t, out, false, "session", "show_reasoning")
	wantRaw(t, out, true, "session", "queue_while_running")
	wantRaw(t, out, "archive", "session", "thread_finish_action")
	wantRaw(t, out, false, "session", "skills_injects_prompt")
	wantRaw(t, out, "zsh", "workbench", "terminal_shell")
	wantRaw(t, out, false, "workbench", "tasks_auto_merge_worktree")
	wantRaw(t, out, true, "archive", "confirm_before_deleting")
	wantRaw(t, out, float64(14), "archive", "sidebar_auto_dormant_days")

	if _, exists := out["general"]; exists {
		t.Fatalf("general 段应被拆空并删除: %+v", out["general"])
	}
	if _, exists := out["swarm"]; exists {
		t.Fatalf("swarm 段应改名为 orchestration: %+v", out["swarm"])
	}
}

// 2. swarm 段改名 orchestration，其中 3 个键顺带去掉 swarm 前缀。
func TestMigrateV7ToV8RenamesSwarmKeys(t *testing.T) {
	out := mustMigrateV7ToV8(t, v8SampleV7())

	wantRaw(t, out, float64(3), "orchestration", "max_bees")
	wantRaw(t, out, float64(9), "orchestration", "max_rounds")
	wantRaw(t, out, float64(45), "orchestration", "bee_timeout_minutes")
	wantRaw(t, out, float64(200), "orchestration", "request_timeout_minutes")
	wantRaw(t, out, "shared", "orchestration", "workspace_mode")
	wantRaw(t, out, float64(420), "orchestration", "panel_width")

	for _, oldKey := range []string{"swarm_max_rounds", "swarmBeeTimeoutMinutes", "swarmRequestTimeoutMinutes"} {
		if _, exists := out["orchestration"].(map[string]any)[oldKey]; exists {
			t.Fatalf("旧键 %q 未删除", oldKey)
		}
	}
}

// 3. v7 的双写字段在新键缺失时兜底，随后被清理干净。
func TestMigrateV7ToV8DropsLegacyDoubleWrite(t *testing.T) {
	raw := map[string]any{
		"version": 7,
		"general": map[string]any{
			"swarm_send_to_bees":    true,
			"swarm_lead_plans_only": true,
		},
	}
	out := mustMigrateV7ToV8(t, raw)

	wantRaw(t, out, true, "orchestration", "always_bees")
	wantRaw(t, out, true, "orchestration", "lead_plans_only")
	if _, exists := out["general"]; exists {
		t.Fatalf("双写字段应被清理，general 不应保留: %+v", out["general"])
	}
}

// 3b. v8 删掉的死字段在迁移后不复存在（swarm 的死开关必须随段搬迁前的清理一起消失）。
func TestMigrateV7ToV8DropsDeadFields(t *testing.T) {
	out := mustMigrateV7ToV8(t, v8SampleV7())

	for _, key := range []string{"queue_bees", "tempers_bee_effort", "review_bees"} {
		if _, exists := lookupRaw(t, out, "orchestration", key); exists {
			t.Fatalf("死字段 orchestration.%s 应被删除", key)
		}
	}
	if _, exists := lookupRaw(t, out, "workbench", "file_tree_hide_ignored"); exists {
		t.Fatalf("死字段 workbench.file_tree_hide_ignored 应被删除")
	}
	// general.terminal_shell / tasks_auto_merge_worktree 按映射表搬进 workbench，所以该段不该整体消失。
	if _, exists := lookupRaw(t, out, "workbench", "terminal_shell"); !exists {
		t.Fatalf("搬进 workbench 的 terminal_shell 丢失: %+v", out["workbench"])
	}
	if _, exists := lookupRaw(t, out, "workbench", "tasks_auto_merge_worktree"); !exists {
		t.Fatalf("搬进 workbench 的 tasks_auto_merge_worktree 丢失: %+v", out["workbench"])
	}
	// models 段同理：死字段被删，但 general.provider 等按映射表搬进来的键要保留，所以段本身不消失。
	for _, key := range []string{"temperature", "context_budget", "max_output_tokens"} {
		if _, exists := lookupRaw(t, out, "models", key); exists {
			t.Fatalf("死字段 models.%s 应被删除", key)
		}
	}
	if _, exists := lookupRaw(t, out, "models", "provider"); !exists {
		t.Fatalf("搬进 models 的 provider 丢失: %+v", out["models"])
	}
}

// 4. 旧键缺失时保持缺失，不写入零值。
func TestMigrateV7ToV8KeepsMissingKeysMissing(t *testing.T) {
	raw := map[string]any{
		"version": 7,
		"general": map[string]any{
			"provider": "codex",
		},
	}
	out := mustMigrateV7ToV8(t, raw)

	if _, exists := out["session"]; exists {
		t.Fatalf("没有 general 键的段不应凭空出现: %+v", out["session"])
	}
	if _, exists := out["notifications"]; exists {
		t.Fatalf("没有 general 键的段不应凭空出现: %+v", out["notifications"])
	}
	wantRaw(t, out, "codex", "models", "provider")
}

// 5. 显式空数组原样保留（区分「没配」和「清空」）。
func TestMigrateV7ToV8KeepsExplicitEmptyArrays(t *testing.T) {
	out := mustMigrateV7ToV8(t, v8SampleV7())

	got, ok := lookupRaw(t, out, "orchestration", "pairs")
	if !ok {
		t.Fatalf("orchestration.pairs 丢失")
	}
	list, isList := got.([]any)
	if !isList || len(list) != 0 {
		t.Fatalf("orchestration.pairs = %v, want 空数组", got)
	}

	wantRaw(t, out, []any{"claude"}, "startup", "tray_quota_hidden")
}

// 6. 新键已存在时以新键为准，旧值丢弃。
func TestMigrateV7ToV8PrefersExistingNewKey(t *testing.T) {
	raw := map[string]any{
		"version": 7,
		"general": map[string]any{"show_reasoning": true},
		"session": map[string]any{"show_reasoning": false},
	}
	out := mustMigrateV7ToV8(t, raw)

	wantRaw(t, out, false, "session", "show_reasoning")
}

// 7. 三态 bool 键的值类型不对时删键回落默认，而不是把错类型带进解码。
func TestMigrateV7ToV8DropsBadPointerTypes(t *testing.T) {
	raw := map[string]any{
		"version": 7,
		"general": map[string]any{
			"show_reasoning":      "yes",
			"dormant_sound":       true,
			"notify_when_stalled": 1,
		},
	}
	out := mustMigrateV7ToV8(t, raw)

	if _, exists := lookupRaw(t, out, "session", "show_reasoning"); exists {
		t.Fatalf("错类型的 session.show_reasoning 应被删除")
	}
	wantRaw(t, out, true, "notifications", "dormant_sound")
	if _, exists := lookupRaw(t, out, "notifications", "notify_when_stalled"); exists {
		t.Fatalf("错类型的 notifications.notify_when_stalled 应被删除")
	}

	// 迁移后的整体结构应能被 Load 正常解码（不因错类型整份失败）。
	dir := t.TempDir()
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal migrated config: %v", err)
	}
	if err := os.WriteFile(ConfigPath(dir), blob, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("迁移结果应可被 Load 解码: %v", err)
	}
}

// 8. 未知字段原样保留在原段 / 随段搬迁保留。
func TestMigrateV7ToV8PreservesUnknownKeys(t *testing.T) {
	raw := map[string]any{
		"version": 7,
		"general": map[string]any{
			"provider":    "codex",
			"unknown_key": "keep-me",
		},
		"swarm": map[string]any{
			"max_bees":      2,
			"swarm_unknown": "keep-me-too",
		},
		"nowledge": map[string]any{"nowledge_unknown": true},
	}
	out := mustMigrateV7ToV8(t, raw)

	wantRaw(t, out, "keep-me", "general", "unknown_key")
	wantRaw(t, out, "keep-me-too", "orchestration", "swarm_unknown")
	wantRaw(t, out, true, "memory", "nowledge_unknown")
}

// 9. 迁移幂等：对已迁移的结果再跑一次不改变内容。
func TestMigrateV7ToV8Idempotent(t *testing.T) {
	once := mustMigrateV7ToV8(t, v8SampleV7())

	blob, err := json.Marshal(once)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again map[string]any
	if err := json.Unmarshal(blob, &again); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	twice := mustMigrateV7ToV8(t, again)

	// 数字类型归一化只用于比较：once 里的数字还是 Go 字面量（int），
	// again 经过一次 JSON 往返已是 float64，直接 DeepEqual 会把类型差异误判成不幂等。
	first := normalizeRawNumbers(once)
	second := normalizeRawNumbers(twice)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("迁移不幂等:\nonce:  %+v\ntwice: %+v", once, twice)
	}
}

// 10. 从磁盘加载 v7 配置：完整链路、字段落位、并生成 config.json.bak。
func TestLoadMigratesV7ToV8RoundTrip(t *testing.T) {
	dir := t.TempDir()
	blob, err := json.Marshal(v8SampleV7())
	if err != nil {
		t.Fatalf("marshal v7 sample: %v", err)
	}
	if err := os.WriteFile(ConfigPath(dir), blob, 0o600); err != nil {
		t.Fatalf("write v7 config: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v7 config failed: %v", err)
	}
	if cfg.Version != Version {
		t.Fatalf("version = %d, want %d", cfg.Version, Version)
	}
	if cfg.Models.Provider != "codex" || !cfg.Models.ProviderSelected {
		t.Fatalf("models.provider/provider_selected 迁移失败: %+v", cfg.Models)
	}
	if cfg.Models.PinLimit != 20 || len(cfg.Models.Pins) != 1 {
		t.Fatalf("models.pins/pin_limit 迁移失败: %+v", cfg.Models)
	}
	if cfg.Permissions.Mode != ModeSupervised {
		t.Fatalf("permissions.mode = %q", cfg.Permissions.Mode)
	}
	if cfg.Session.ShowReasoning == nil || *cfg.Session.ShowReasoning {
		t.Fatalf("session.show_reasoning 显式 false 未保留: %+v", cfg.Session.ShowReasoning)
	}
	if !cfg.Session.QueueWhileRunning || !cfg.Session.InsertIntoRunningTurn || !cfg.Session.AutoContinueAfterLimit {
		t.Fatalf("session 布尔字段迁移失败: %+v", cfg.Session)
	}
	if cfg.Session.ThreadFinishAction != FinishActionArchive {
		t.Fatalf("session.thread_finish_action = %q", cfg.Session.ThreadFinishAction)
	}
	if !cfg.Notifications.NotifyWhenFinished || !cfg.Notifications.NotifyWhenForeground || !cfg.Notifications.PlayFinishSound {
		t.Fatalf("notifications 迁移失败: %+v", cfg.Notifications)
	}
	if !cfg.Startup.ShowTrayIcon || cfg.Startup.QuitAction != QuitActionMinimizeToTray {
		t.Fatalf("startup 迁移失败: %+v", cfg.Startup)
	}
	if cfg.Shortcuts.GlobalShortcut != "CmdOrCtrl+Shift+M" {
		t.Fatalf("shortcuts 迁移失败: %+v", cfg.Shortcuts)
	}
	if cfg.Workbench.TerminalShell != "zsh" {
		t.Fatalf("workbench.terminal_shell = %q", cfg.Workbench.TerminalShell)
	}
	if !cfg.Archive.ConfirmBeforeDeleting || cfg.Archive.SidebarAutoDormantDays == nil || *cfg.Archive.SidebarAutoDormantDays != 14 {
		t.Fatalf("archive 迁移失败: %+v", cfg.Archive)
	}
	if cfg.Orchestration.MaxBees != 3 || cfg.Orchestration.MaxRounds != 9 {
		t.Fatalf("orchestration 数值迁移失败: %+v", cfg.Orchestration)
	}
	if cfg.Orchestration.BeeTimeoutMinutes != 45 || cfg.Orchestration.RequestTimeoutMinutes != 200 {
		t.Fatalf("orchestration 超时迁移失败: %+v", cfg.Orchestration)
	}
	if cfg.Orchestration.Steward.Name != "默默" || cfg.Orchestration.Steward.Model != "gpt-5" {
		t.Fatalf("orchestration.steward 迁移失败: %+v", cfg.Orchestration.Steward)
	}
	if cfg.Memory.SyncThreads == nil || *cfg.Memory.SyncThreads {
		t.Fatalf("memory.sync_threads 迁移失败: %+v", cfg.Memory)
	}

	if _, err := os.Stat(filepath.Join(dir, "config.json.bak")); err != nil {
		t.Fatalf("迁移前应生成 config.json.bak: %v", err)
	}
}

// 11. 反向映射：v8 结构能还原成语义等价的 v7（往返）。
func TestExportV7RoundTripsMigratedConfig(t *testing.T) {
	sample := v8SampleV7()
	// pairs 是显式空数组：带 omitempty 的切片导出时会整体省略，往返比较里先摘掉，
	// 「显式空数组保留」由 TestMigrateV7ToV8KeepsExplicitEmptyArrays 单独覆盖。
	delete(sample["swarm"].(map[string]any), "pairs")
	migrated := mustMigrateV7ToV8(t, sample)
	blob, err := json.Marshal(migrated)
	if err != nil {
		t.Fatalf("marshal migrated: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(blob, &cfg); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	out, err := exportV7(cfg)
	if err != nil {
		t.Fatalf("exportV7 failed: %v", err)
	}

	wantRaw(t, out, 7, "version")
	wantRaw(t, out, "codex", "general", "provider")
	wantRaw(t, out, "supervised", "general", "permission_mode")
	wantRaw(t, out, false, "general", "show_reasoning")
	wantRaw(t, out, float64(9), "swarm", "swarm_max_rounds")
	wantRaw(t, out, float64(45), "swarm", "swarmBeeTimeoutMinutes")
	wantRaw(t, out, float64(3), "swarm", "max_bees")
	wantRaw(t, out, "默默", "steward", "name")
	wantRaw(t, out, false, "nowledge", "sync_threads")

	for _, gone := range []string{"session", "startup", "notifications", "permissions", "shortcuts", "archive", "orchestration", "memory"} {
		if _, exists := out[gone]; exists {
			t.Fatalf("v7 快照不应含 v8 段 %q: %+v", gone, out[gone])
		}
	}

	// 反向结果再走一遍正向迁移，应回到同一份 v8 语义。
	againBlob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal v7 snapshot: %v", err)
	}
	remigrated := mustMigrateV7ToV8(t, func() map[string]any {
		var raw map[string]any
		if err := json.Unmarshal(againBlob, &raw); err != nil {
			t.Fatalf("unmarshal v7 snapshot map: %v", err)
		}
		return raw
	}())
	// 比较解出来的结构体而不是原始 map：map 层面 roles:{} 这类空壳的写法会有出入，
	// 语义等价才是往返测试要保证的东西。
	var first, second Config
	if err := json.Unmarshal(mustMarshal(t, migrated), &first); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if err := json.Unmarshal(mustMarshal(t, remigrated), &second); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("往返后语义不等价:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// 12. Save 会写出同目录的 config.v7.json 降级快照。
func TestSaveWritesV7Snapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Models.Provider = "codex"
	off := false
	cfg.Session.ShowReasoning = &off
	cfg.Orchestration.MaxRounds = 4

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, v7SnapshotFileName))
	if err != nil {
		t.Fatalf("应写出 %s: %v", v7SnapshotFileName, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	wantRaw(t, raw, 7, "version")
	wantRaw(t, raw, "codex", "general", "provider")
	wantRaw(t, raw, false, "general", "show_reasoning")
	wantRaw(t, raw, float64(4), "swarm", "swarm_max_rounds")
	if _, exists := raw["orchestration"]; exists {
		t.Fatalf("快照不应含 orchestration 段: %+v", raw["orchestration"])
	}
}

// 13. 快照写失败不影响 Save 的结果。
func TestSaveV7SnapshotFailureDoesNotBreakSave(t *testing.T) {
	dir := t.TempDir()
	// 占住快照路径：目录无法被 rename 覆盖，写入必然失败。
	if err := os.MkdirAll(filepath.Join(dir, v7SnapshotFileName), 0o700); err != nil {
		t.Fatalf("mkdir snapshot path: %v", err)
	}

	if err := Save(dir, Defaults()); err != nil {
		t.Fatalf("快照失败不应让 Save 失败: %v", err)
	}
	if _, err := os.Stat(ConfigPath(dir)); err != nil {
		t.Fatalf("config.json 应正常写出: %v", err)
	}
}
