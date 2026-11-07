package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MigrationFunc 表示单个版本的配置数据迁移函数。
type MigrationFunc func(raw map[string]any) (map[string]any, error)

// migrations 是有序迁移链。每一步将版本 v 升级至 v+1。
// 当前 Version 为 9。
var migrations = []MigrationFunc{
	migrateV1ToV2,
	migrateV2ToV3,
	migrateV3ToV4,
	migrateV4ToV5,
	migrateV5ToV6,
	migrateV6ToV7,
	migrateV7ToV8,
	migrateV8ToV9,
}

// migrateV1ToV2 将配置数据从 v1 迁移至 v2。
// 若 raw 中没有 models 键或值不是对象，写入 roles 为空对象的 models 段；
// 已有 models 时原样保留。
// v8 起 models 段不再有 context_budget/max_output_tokens/temperature，
// 旧键由 migrateV7ToV8 统一丢弃。
func migrateV1ToV2(raw map[string]any) (map[string]any, error) {
	modelsRaw, ok := raw["models"]
	_, isMap := modelsRaw.(map[string]any)
	if !ok || !isMap {
		raw["models"] = map[string]any{
			"roles": map[string]any{},
		}
	}
	return raw, nil
}

// swarmSectionKeyMoves 是 v2→v3 中 swarm 分节内需要改名的键：head（子代理）改为 bee。
var swarmSectionKeyMoves = [][2]string{
	{"max_heads", "max_bees"},
	{"queue_heads", "queue_bees"},
	{"always_heads", "always_bees"},
	{"review_heads", "review_bees"},
	{"auto_hides_idle_heads", "auto_hides_idle_bees"},
	{"shows_head_details", "shows_bee_details"},
	{"auto_pops_heads", "auto_pops_bees"},
	{"archives_codex_heads", "archives_codex_bees"},
	{"tempers_head_effort", "tempers_bee_effort"},
	{"hydra_max_rounds", "swarm_max_rounds"},
	{"hydraHeadTimeoutMinutes", "swarmBeeTimeoutMinutes"},
	{"hydraRequestTimeoutMinutes", "swarmRequestTimeoutMinutes"},
}

// pairKeyMoves 是 v2→v3 中 pairs 数组每个配对项内需要改名的键。
var pairKeyMoves = [][2]string{
	{"head_tiers", "bee_tiers"},
}

// generalKeyMoves 是 v2→v3 中 general 分节内需要改名的键。
var generalKeyMoves = [][2]string{
	{"hydra_send_to_heads", "swarm_send_to_bees"},
	{"hydra_lead_plans_only", "swarm_lead_plans_only"},
}

// migrateV2ToV3 将配置数据从 v2 迁移至 v3：swarm 更名为 swarm，bee（子代理）更名为 bee。
// 只改键名、不动值：旧键存在且新键不存在时才搬值，随后删除旧键（新旧同时存在时保留新键）。
// 分节或键的值类型不符合预期时原样跳过，不报错。
func migrateV2ToV3(raw map[string]any) (map[string]any, error) {
	renameKeys(raw, [][2]string{{"hydra", "swarm"}})

	if swarm, ok := raw["swarm"].(map[string]any); ok {
		renameKeys(swarm, swarmSectionKeyMoves)
		if pairs, ok := swarm["pairs"].([]any); ok {
			for _, pair := range pairs {
				if entry, ok := pair.(map[string]any); ok {
					renameKeys(entry, pairKeyMoves)
				}
			}
		}
	}

	if general, ok := raw["general"].(map[string]any); ok {
		renameKeys(general, generalKeyMoves)
	}

	return raw, nil
}

// generalKeyMovesV3ToV4 是 v3→v4 中 general 分节内需要改名的键。
var generalKeyMovesV3ToV4 = [][2]string{
	{"settle_sound", "dormant_sound"},
	{"sidebar_auto_settle_days", "sidebar_auto_dormant_days"},
	{"anti_slop_enabled", "concise_replies_enabled"},
}

// shortcutKeyMovesV3ToV4 是 v3→v4 中 general.shortcut_overrides 内需要改名的快捷键 id。
var shortcutKeyMovesV3ToV4 = [][2]string{
	{"settle-thread", "dormant-thread"},
}

// migrateV3ToV4 将配置数据从 v3 迁移至 v4：休眠与简洁回复相关的键改名，
// general.thread_finish_action 的值 "settle" 改为 "dormant"，
// general.shortcut_overrides 的键 "settle-thread" 改为 "dormant-thread"。
// 分节或键的值类型不符合预期时原样跳过，不报错。
func migrateV3ToV4(raw map[string]any) (map[string]any, error) {
	general, ok := raw["general"].(map[string]any)
	if !ok {
		return raw, nil
	}

	renameKeys(general, generalKeyMovesV3ToV4)

	if action, ok := general["thread_finish_action"].(string); ok && action == "settle" {
		general["thread_finish_action"] = "dormant"
	}

	if overrides, ok := general["shortcut_overrides"].(map[string]any); ok {
		renameKeys(overrides, shortcutKeyMovesV3ToV4)
	}

	return raw, nil
}

// defaultTierEffortsV4ToV5 是 v4→v5 中为内置档位补齐的默认推理强度。
var defaultTierEffortsV4ToV5 = map[string]string{
	"read":  "medium",
	"chore": "medium",
	"edit":  "medium",
	"deep":  "high",
}

// choreTierRawV4ToV5 返回 v4→v5 中插入旧配置的 chore（杂活）档位定义，
// 键名与 SwarmTier 的 json tag 保持一致。
func choreTierRawV4ToV5() map[string]any {
	return map[string]any{
		"id":          "chore",
		"name":        "杂活",
		"description": choreTierDescription,
		"read_only":   false,
		"effort":      "medium",
	}
}

// tierIDOf 返回档位条目的小写 id；条目不是对象或没有字符串 id 时返回空串。
func tierIDOf(item any) string {
	tier, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := tier["id"].(string)
	return strings.ToLower(strings.TrimSpace(id))
}

// migrateV4ToV5 将配置数据从 v4 迁移至 v5：为 swarm.tiers 补齐 chore（杂活）档位，
// 并给内置档位缺失或为空的 effort 填上默认强度。
// 已有 chore 档位时不重复插入；effort 非空时保持用户设置；
// deep 的 description 不改动；swarm.tiers 缺失或不是数组时原样跳过，不报错。
func migrateV4ToV5(raw map[string]any) (map[string]any, error) {
	swarm, ok := raw["swarm"].(map[string]any)
	if !ok {
		return raw, nil
	}
	tiers, ok := swarm["tiers"].([]any)
	if !ok {
		return raw, nil
	}

	readIdx := -1
	hasChore := false
	for i, item := range tiers {
		switch tierIDOf(item) {
		case "read":
			readIdx = i
		case "chore":
			hasChore = true
		}
	}

	if !hasChore {
		insertAt := 0
		if readIdx >= 0 {
			insertAt = readIdx + 1
		}
		tiers = append(tiers, nil)
		copy(tiers[insertAt+1:], tiers[insertAt:])
		tiers[insertAt] = choreTierRawV4ToV5()
		swarm["tiers"] = tiers
	}

	for _, item := range tiers {
		tier, ok := item.(map[string]any)
		if !ok {
			continue
		}
		want, ok := defaultTierEffortsV4ToV5[tierIDOf(item)]
		if !ok {
			continue
		}
		if cur, _ := tier["effort"].(string); strings.TrimSpace(cur) != "" {
			continue
		}
		tier["effort"] = want
	}

	return raw, nil
}

// migrateV5ToV6 将配置数据从 v5 迁移至 v6：给 deep（深度）档补上默认写入白名单，
// 让它只出方案文档、不改代码。deep 档已有 write_paths 键（含空数组）时不改动，
// deep 的 description 不迁移；swarm.tiers 缺失或不是数组时原样跳过，不报错。
func migrateV5ToV6(raw map[string]any) (map[string]any, error) {
	swarm, ok := raw["swarm"].(map[string]any)
	if !ok {
		return raw, nil
	}
	tiers, ok := swarm["tiers"].([]any)
	if !ok {
		return raw, nil
	}

	for _, item := range tiers {
		if tierIDOf(item) != "deep" {
			continue
		}
		tier, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := tier["write_paths"]; exists {
			continue
		}
		paths := defaultDeepWritePaths()
		list := make([]any, 0, len(paths))
		for _, p := range paths {
			list = append(list, p)
		}
		tier["write_paths"] = list
	}

	return raw, nil
}

// migrateV6ToV7 将配置数据从 v6 迁移至 v7：v7 起总管默认开启，
// 所以把没有表态标记的旧配置里的 steward.enabled 置为 true。
// raw 里没有 steward 对象时什么都不做（新安装直接走 Defaults()）；
// steward.enabled_choice 为 true 表示用户亲手切换过启用开关，此时保持原样。
// 迁移不写 enabled_choice：标记只在用户亲手切换开关时写入，否则下次升级就无法区分意图。
func migrateV6ToV7(raw map[string]any) (map[string]any, error) {
	steward, ok := raw["steward"].(map[string]any)
	if !ok {
		return raw, nil
	}
	if chosen, _ := steward["enabled_choice"].(bool); chosen {
		return raw, nil
	}
	steward["enabled"] = true

	return raw, nil
}

// v8SectionMoves 是 v7→v8 的整段搬迁表（旧段路径, 新段路径），两端都用点分路径表示。
// 总管从顶层 steward 挤进 orchestration.steward；整段搬迁会把段内的未知字段一起带走，
// 用户手写的键不会因为升级而丢失。降级快照 exportV7 反向共用这张表。
var v8SectionMoves = [][2]string{
	{"swarm", "orchestration"},
	{"steward", "orchestration.steward"},
	{"nowledge", "memory"},
}

// v8KeyMoves 是 v7→v8 需要跨段搬家的键（旧点分路径, 新点分路径）。
// general 段在 v8 被拆成 session/startup/notifications/permissions/organization 等段，
// 键名本身不变、只换段；orchestration 的 max_rounds/bee_timeout_minutes/request_timeout_minutes
// 顺带去掉 swarm 前缀。
var v8KeyMoves = [][2]string{
	{"general.provider", "models.provider"},
	{"general.provider_selected", "models.provider_selected"},
	{"general.model_list", "models.pins"},
	{"general.model_list_limit", "models.pin_limit"},
	{"general.model_preferences", "models.preferences"},
	{"general.permission_mode", "permissions.mode"},
	{"general.notify_when_finished", "notifications.notify_when_finished"},
	{"general.notify_on_approval", "notifications.notify_on_approval"},
	{"general.notify_when_foreground", "notifications.notify_when_foreground"},
	{"general.play_finish_sound", "notifications.play_finish_sound"},
	{"general.chime_when_attention_needed", "notifications.chime_when_attention_needed"},
	{"general.dormant_sound", "notifications.dormant_sound"},
	{"general.notify_when_stalled", "notifications.notify_when_stalled"},
	{"general.stalled_alert_minutes", "notifications.stalled_alert_minutes"},
	{"general.show_tray_icon", "startup.show_tray_icon"},
	{"general.tray_show_quota", "startup.tray_show_quota"},
	{"general.tray_quota_hidden", "startup.tray_quota_hidden"},
	{"general.quit_action", "startup.quit_action"},
	{"general.global_shortcut_enabled", "shortcuts.global_shortcut_enabled"},
	{"general.global_shortcut", "shortcuts.global_shortcut"},
	{"general.shortcut_overrides", "shortcuts.shortcut_overrides"},
	{"general.show_reasoning", "session.show_reasoning"},
	{"general.auto_collapse_reasoning", "session.auto_collapse_reasoning"},
	{"general.concise_replies_enabled", "session.concise_replies_enabled"},
	{"general.queue_while_running", "session.queue_while_running"},
	{"general.insert_into_running_turn", "session.insert_into_running_turn"},
	{"general.auto_continue_after_limit", "session.auto_continue_after_limit"},
	{"general.ask_compact_before_resume", "session.ask_compact_before_resume"},
	{"general.thread_finish_action", "session.thread_finish_action"},
	{"general.skills_injects_prompt", "session.skills_injects_prompt"},
	{"general.terminal_shell", "workbench.terminal_shell"},
	{"general.tasks_auto_merge_worktree", "workbench.tasks_auto_merge_worktree"},
	{"general.confirm_before_deleting", "archive.confirm_before_deleting"},
	{"general.sidebar_auto_dormant_days", "archive.sidebar_auto_dormant_days"},
	{"swarm.swarm_max_rounds", "orchestration.max_rounds"},
	{"swarm.swarmBeeTimeoutMinutes", "orchestration.bee_timeout_minutes"},
	{"swarm.swarmRequestTimeoutMinutes", "orchestration.request_timeout_minutes"},
}

// v8LegacyDoubleWrite 是 v7 曾经双写的两个字段：v8 不再有 general 段，
// 所以只能在新键缺失时作为兜底，旧值不进反向快照。
// v7 的新键在 swarm 段里（与 v8 的 orchestration 同名），所以兜底必须排在
// 整段搬迁之后：swarm 已并进 orchestration 时，显式写下的新键（哪怕 false）优先。
var v8LegacyDoubleWrite = [][2]string{
	{"general.swarm_send_to_bees", "orchestration.always_bees"},
	{"general.swarm_lead_plans_only", "orchestration.lead_plans_only"},
}

// v8DroppedKeys 是 v8 彻底删除、迁移时直接丢弃的旧键。
// general 的双写键不在此列：它们由 v8LegacyDoubleWrite 兜底读走后即被删除。
// TODO(v9): 确认不再有 v7 以下配置在线后，可以连同迁移一起移除。
var v8DroppedKeys = []string{
	"swarm.review_bees",
	"swarm.queue_bees",
	"swarm.tempers_bee_effort",
	"workbench.file_tree_hide_ignored",
	"models.temperature",
	"models.context_budget",
	"models.max_output_tokens",
}

// splitKeyPath 把点分路径拆成父路径段与叶子键名。
func splitKeyPath(path string) ([]string, string) {
	parts := strings.Split(path, ".")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// objectAt 返回 root 中 path 路径指向的对象；create 为 true 时补建缺失的段，
// 路径上遇到非对象值或（create 为 false 时）缺失的段则返回 nil。
func objectAt(root map[string]any, path []string, create bool) map[string]any {
	cur := root
	for _, seg := range path {
		next, ok := cur[seg]
		if !ok {
			if !create {
				return nil
			}
			obj := map[string]any{}
			cur[seg] = obj
			cur = obj
			continue
		}
		obj, ok := next.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj
	}
	return cur
}

// takePath 取出并删除 root 中 path 处的值；键不存在或路径上出现非对象时返回 false。
func takePath(root map[string]any, path string) (any, bool) {
	parents, leaf := splitKeyPath(path)
	obj := objectAt(root, parents, false)
	if obj == nil {
		return nil, false
	}
	value, ok := obj[leaf]
	if !ok {
		return nil, false
	}
	delete(obj, leaf)
	return value, true
}

// setPathIfAbsent 在 path 处写入 value；该键已存在时保留已有值（新键优先）。
func setPathIfAbsent(root map[string]any, path string, value any) {
	parents, leaf := splitKeyPath(path)
	obj := objectAt(root, parents, true)
	if obj == nil {
		return
	}
	if _, exists := obj[leaf]; exists {
		return
	}
	obj[leaf] = value
}

// dropPath 删除 path 处的键；不存在时无事发生。
func dropPath(root map[string]any, path string) {
	parents, leaf := splitKeyPath(path)
	if obj := objectAt(root, parents, false); obj != nil {
		delete(obj, leaf)
	}
}

// absorbSection 把 from 段整体并入 to 段（to 已有同名键时保留 to 的值），随后删除 from 段；
// from/to 都是点分路径。任一端不是对象时原样跳过，不报错。
func absorbSection(root map[string]any, from, to string) {
	oldValue, ok := takePath(root, from)
	if !ok {
		return
	}
	oldSection, ok := oldValue.(map[string]any)
	if !ok {
		return
	}
	target := objectAt(root, strings.Split(to, "."), true)
	if target == nil {
		// 目标是已有的非对象值：保留它，把旧段按原路径放回，不让迁移丢数据。
		setPathIfAbsent(root, from, oldSection)
		return
	}
	for k, v := range oldSection {
		if _, exists := target[k]; !exists {
			target[k] = v
		}
	}
}

// pruneEmptyObjects 删除被搬空的顶层段，避免升级后留下 "general": {} 这类空壳。
func pruneEmptyObjects(root map[string]any) {
	for key, value := range root {
		section, ok := value.(map[string]any)
		if !ok || len(section) > 0 {
			continue
		}
		delete(root, key)
	}
}

// v8PointerBoolPaths 是 v8 的三态 bool / 三态 int 指针字段的新路径。
// 值不是 bool（或 int）时删键回落默认值，而不是把错类型带进 json.Unmarshal，
// 否则旧配置里一个手改错的键会让整个配置文件读不出来。
var v8PointerBoolPaths = []string{
	"session.show_reasoning",
	"session.auto_collapse_reasoning",
	"session.pin_sent_message",
	"session.concise_replies_enabled",
	"session.ask_compact_before_resume",
	"session.skills_injects_prompt",
	"notifications.chime_when_attention_needed",
	"notifications.dormant_sound",
	"notifications.notify_when_stalled",
	"orchestration.enabled",
	"orchestration.auto_delegate",
	"memory.inject_working_memory",
	"memory.inject_memory_tools_note",
	"memory.sync_threads",
	"memory.sync_helper_threads",
	"memory.sync_automation_threads",
	"memory.bee_recall",
}

// sanitizePointerBools 丢弃错类型的三态 bool 值：显式 false 的语义不能丢，
// 但错类型只能当作未设置。
func sanitizePointerBools(root map[string]any) {
	for _, path := range v8PointerBoolPaths {
		value, ok := takePath(root, path)
		if !ok {
			continue
		}
		if _, isBool := value.(bool); !isBool {
			continue
		}
		setPathIfAbsent(root, path, value)
	}
}

// migrateV7ToV8 把 v7 的平铺结构拆成 v8 的分段结构：
// swarm 改名 orchestration、steward 嵌进 orchestration、nowledge 改名 memory，
// general 按用途拆到 session/startup/notifications/permissions/shortcuts/archive/workbench/models，
// 并丢弃 v8 删掉的字段。新键已存在时以新键为准，缺失的键保持缺失（不补零值），
// 段内未知字段随段搬迁保留。
func migrateV7ToV8(raw map[string]any) (map[string]any, error) {
	// swarm 的 3 个键先改名，再整体搬到 orchestration（否则搬完就找不到旧路径了）。
	for _, move := range v8KeyMoves {
		value, ok := takePath(raw, move[0])
		if !ok {
			continue
		}
		setPathIfAbsent(raw, move[1], value)
	}

	// v8 删掉的字段必须在整段搬迁之前清理：swarm.* 里的死开关一旦随段搬进 orchestration，
	// 再按旧路径删就删不到了。
	for _, path := range v8DroppedKeys {
		dropPath(raw, path)
	}

	for _, move := range v8SectionMoves {
		absorbSection(raw, move[0], move[1])
	}

	// v7 的双写字段只在新键缺失时负责兜底，随后不再回填。
	// 必须等 swarm 整段并进 orchestration 之后再兜底：v7 的语义是 swarm 段显式写了新键
	// 就以新键为准（哪怕值是 false），提前兜底会拿 general 的旧值盖掉它。
	for _, move := range v8LegacyDoubleWrite {
		value, ok := takePath(raw, move[0])
		if !ok {
			continue
		}
		setPathIfAbsent(raw, move[1], value)
	}

	sanitizePointerBools(raw)
	pruneEmptyObjects(raw)
	return raw, nil
}

// migrateV8ToV9 把布尔开关 orchestration.review_before_merge 换成三档 orchestration.review_mode：
// 旧值为 true 迁成 human，其余情况迁成 off，然后删除旧键；新键已存在时以新键为准。
// 没有旧键且原本就没有 orchestration 段时什么都不写，留给 Normalize 补默认值 off，
// 避免凭空造出一个只含 review_mode 的空段。
func migrateV8ToV9(raw map[string]any) (map[string]any, error) {
	mode := ReviewModeOff
	legacy, ok := takePath(raw, "orchestration.review_before_merge")
	if ok {
		dropPath(raw, "orchestration.review_before_merge")
		if enabled, isBool := legacy.(bool); isBool && enabled {
			mode = ReviewModeHuman
		}
	}
	if !ok && objectAt(raw, []string{"orchestration"}, false) == nil {
		return raw, nil
	}
	setPathIfAbsent(raw, "orchestration.review_mode", mode)
	return raw, nil
}

// exportV7 把 v8 内存结构反向映射成 v7 的原始 JSON 结构，用于写 config.v7.json 降级快照。
// 反向映射与 migrateV7ToV8 共用 v8SectionMoves / v8KeyMoves，不维护第二份映射；
// v8 删掉的字段不回填，旧版本读到的是它自己的默认值。
// TODO(v9): 不再需要跨大版本回退时，连同写快照的逻辑一起移除。
func exportV7(cfg Config) (map[string]any, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config for v7 snapshot: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal config for v7 snapshot: %w", err)
	}

	// 反向搬键要先做：新路径都在 orchestration 上，拆段之后就找不到了。
	for _, move := range v8KeyMoves {
		value, ok := takePath(raw, move[1])
		if !ok {
			continue
		}
		setPathIfAbsent(raw, move[0], value)
	}

	// 反向搬段：先拆 orchestration.steward 与 memory，最后把 orchestration 还原成 swarm。
	for i := len(v8SectionMoves) - 1; i >= 0; i-- {
		absorbSection(raw, v8SectionMoves[i][1], v8SectionMoves[i][0])
	}

	pruneEmptyObjects(raw)
	raw["version"] = 7
	return raw, nil
}

// renameKeys 按 pairs 就地改名：旧键存在且新键不存在时才搬值，随后删除旧键；值类型不做校验。
func renameKeys(m map[string]any, pairs [][2]string) {
	for _, pair := range pairs {
		oldKey, newKey := pair[0], pair[1]
		value, ok := m[oldKey]
		if !ok {
			continue
		}
		if _, exists := m[newKey]; !exists {
			m[newKey] = value
		}
		delete(m, oldKey)
	}
}

// migrateRaw 检查配置字节流中的 version 字段。
// 若版本大于支持的 Version，报错并不改写文件；
// 若版本低于支持的 Version，在迁移前先将原文件备份为 config.json.bak，再执行迁移链。
func migrateRaw(dataDir string, raw []byte) ([]byte, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("decode config version: %w", err)
	}

	fileVer := header.Version
	if fileVer < 0 {
		return nil, fmt.Errorf("invalid config version %d", fileVer)
	}
	if fileVer == 0 {
		fileVer = 1
	}

	if fileVer > Version {
		return nil, fmt.Errorf("config version %d is newer than supported version %d", fileVer, Version)
	}

	if fileVer == Version {
		return raw, nil
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshal config for migration: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("config must be a JSON object")
	}

	// fileVer < Version：执行迁移链前备份为 config.json.bak
	bakPath := filepath.Join(dataDir, "config.json.bak")
	if err := os.WriteFile(bakPath, raw, 0o600); err != nil {
		return nil, fmt.Errorf("create config backup before migration: %w", err)
	}

	for v := fileVer; v < Version; v++ {
		idx := v - 1
		if idx >= len(migrations) {
			return nil, fmt.Errorf("missing migration from version %d to %d", v, v+1)
		}
		var err error
		data, err = migrations[idx](data)
		if err != nil {
			return nil, fmt.Errorf("migrate config from version %d to %d: %w", v, v+1, err)
		}
	}
	data["version"] = Version

	migratedBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal migrated config: %w", err)
	}

	return migratedBytes, nil
}
