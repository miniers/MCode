package remote

import (
	"context"
	"encoding/json"
	"sort"

	"mcode/internal/remote/wire"
)

// 本文件把桌面设置开放给远程客户端：
//   - settings.get：返回白名单键的当前值与只读的快捷回复；
//   - settings.set：按白名单原子写入（任一键非法整体拒绝），成功后广播 settings.changed。
//
// 白名单刻意只覆盖「界面偏好」这一层：providers 凭据、remote TLS / Cloudflare token、
// source_control、memory、workbench 等都不开放，避免远程一改就把本机连不上。
// 数据与桌面端同源：internal/app/remote_settings.go 注入 "settings" 服务，
// 由它转 config.Load / config.Update。

const (
	// settingsExtServiceName 是设置服务在服务注册表里的名字，与 internal/app 的注入保持一致。
	settingsExtServiceName = "settings"
	// settingsExtChangedMethod 是设置写入成功后广播的通知方法名。
	settingsExtChangedMethod = "settings.changed"
)

// 白名单键名。导出给 internal/app 复用，避免两边各写一份字符串而拼错。
const (
	SettingsKeyModelsProvider                     = "models.provider"
	SettingsKeyPermissionsMode                    = "permissions.mode"
	SettingsKeySessionAutoCollapseReasoning       = "session.auto_collapse_reasoning"
	SettingsKeySessionPinSentMessage              = "session.pin_sent_message"
	SettingsKeySessionConciseRepliesEnabled       = "session.concise_replies_enabled"
	SettingsKeyOrchestrationEnabled               = "orchestration.enabled"
	SettingsKeyOrchestrationAutoDelegate          = "orchestration.auto_delegate"
	SettingsKeyOrchestrationMaxBees               = "orchestration.max_bees"
	SettingsKeyOrchestrationAlwaysBees            = "orchestration.always_bees"
	SettingsKeyOrchestrationLeadPlansOnly         = "orchestration.lead_plans_only"
	SettingsKeyOrchestrationWorkspaceMode         = "orchestration.workspace_mode"
	SettingsKeyOrchestrationMaxRounds             = "orchestration.max_rounds"
	SettingsKeyOrchestrationRetryFailed           = "orchestration.retry_failed"
	SettingsKeyOrchestrationAutoClearFinished     = "orchestration.auto_clear_finished"
	SettingsKeyOrchestrationReviewBeforeMerge     = "orchestration.review_before_merge"
	SettingsKeyOrchestrationReviewMode            = "orchestration.review_mode"
	SettingsKeyOrchestrationReviewProvider        = "orchestration.review_provider"
	SettingsKeyOrchestrationReviewModel           = "orchestration.review_model"
	SettingsKeyOrchestrationReviewEffort          = "orchestration.review_effort"
	SettingsKeyOrchestrationReviewCheckCommand    = "orchestration.review_check_command"
	SettingsKeyOrchestrationReviewMaxChangedLines = "orchestration.review_max_changed_lines"
	SettingsKeyOrchestrationAutoMerge             = "orchestration.auto_merge"
	SettingsKeyOrchestrationAutoPush              = "orchestration.auto_push"
	SettingsKeyOrchestrationAutoForwardSeconds    = "orchestration.auto_forward_question_seconds"
	SettingsKeyNotifyWhenFinished                 = "notifications.notify_when_finished"
	SettingsKeyNotifyWhenForeground               = "notifications.notify_when_foreground"
	SettingsKeyNotifyOnApproval                   = "notifications.notify_on_approval"
	SettingsKeyPlayFinishSound                    = "notifications.play_finish_sound"
	SettingsKeyChimeWhenAttentionNeeded           = "notifications.chime_when_attention_needed"
	SettingsKeyDormantSound                       = "notifications.dormant_sound"
	SettingsKeyNotifyWhenStalled                  = "notifications.notify_when_stalled"
	SettingsKeyStalledAlertMinutes                = "notifications.stalled_alert_minutes"
	SettingsKeyTrayQuotaHidden                    = "startup.tray_quota_hidden"
	SettingsKeyConfirmBeforeDeleting              = "archive.confirm_before_deleting"
	SettingsKeySidebarAutoDormantDays             = "archive.sidebar_auto_dormant_days"
	SettingsKeyStewardName                        = "steward.name"
	SettingsKeyStewardAvatar                      = "steward.avatar"
	SettingsKeyStewardQuietStart                  = "steward.quiet_start"
	SettingsKeyStewardQuietEnd                    = "steward.quiet_end"
	SettingsKeyStewardEnabled                     = "steward.enabled"
	SettingsKeyStewardCLIFallback                 = "steward.cli_fallback"
	SettingsKeyStewardAutoCollapseReasoning       = "steward.auto_collapse_reasoning"
)

// SettingsWritableKeys 是 settings.set 接受的键，顺序与 docs/superpowers/plans/2026-11-01-web-parity.md
// 的表格一致；settings.get 原样返回它，客户端据此渲染表单。
// 只读，不要在运行期修改。
var SettingsWritableKeys = []string{
	SettingsKeyModelsProvider,
	SettingsKeyPermissionsMode,
	SettingsKeySessionAutoCollapseReasoning,
	SettingsKeySessionPinSentMessage,
	SettingsKeySessionConciseRepliesEnabled,
	SettingsKeyOrchestrationEnabled,
	SettingsKeyOrchestrationAutoDelegate,
	SettingsKeyOrchestrationMaxBees,
	SettingsKeyOrchestrationAlwaysBees,
	SettingsKeyOrchestrationLeadPlansOnly,
	SettingsKeyOrchestrationWorkspaceMode,
	SettingsKeyOrchestrationMaxRounds,
	SettingsKeyOrchestrationRetryFailed,
	SettingsKeyOrchestrationAutoClearFinished,
	SettingsKeyOrchestrationReviewBeforeMerge,
	SettingsKeyOrchestrationReviewMode,
	SettingsKeyOrchestrationReviewProvider,
	SettingsKeyOrchestrationReviewModel,
	SettingsKeyOrchestrationReviewEffort,
	SettingsKeyOrchestrationReviewCheckCommand,
	SettingsKeyOrchestrationReviewMaxChangedLines,
	SettingsKeyOrchestrationAutoMerge,
	SettingsKeyOrchestrationAutoPush,
	SettingsKeyOrchestrationAutoForwardSeconds,
	SettingsKeyNotifyWhenFinished,
	SettingsKeyNotifyWhenForeground,
	SettingsKeyNotifyOnApproval,
	SettingsKeyPlayFinishSound,
	SettingsKeyChimeWhenAttentionNeeded,
	SettingsKeyDormantSound,
	SettingsKeyNotifyWhenStalled,
	SettingsKeyStalledAlertMinutes,
	SettingsKeyTrayQuotaHidden,
	SettingsKeyConfirmBeforeDeleting,
	SettingsKeySidebarAutoDormantDays,
	SettingsKeyStewardName,
	SettingsKeyStewardAvatar,
	SettingsKeyStewardQuietStart,
	SettingsKeyStewardQuietEnd,
	SettingsKeyStewardEnabled,
	SettingsKeyStewardCLIFallback,
	SettingsKeyStewardAutoCollapseReasoning,
}

// IsSettingsWritableKey 返回 key 是否在白名单内。
func IsSettingsWritableKey(key string) bool {
	for _, item := range SettingsWritableKeys {
		if item == key {
			return true
		}
	}
	return false
}

// settingsExtService 是设置服务，由 app 层注入（见 internal/app/remote_settings.go）。
// Set 收到的键名已由 handler 校验在白名单内，服务只负责类型与取值校验、落盘与通知；
// 校验失败必须返回 wire.InvalidParams 错误，且不得改动配置文件。
type settingsExtService interface {
	// Get 返回白名单键的当前值与快捷回复。
	Get(ctx context.Context) (wire.SettingsSnapshot, error)
	// Set 写入一批键值并返回写入后的快照；任一键失败则整体不生效。
	Set(ctx context.Context, values map[string]json.RawMessage) (wire.SettingsSnapshot, error)
}

func init() {
	RegisterMethod("settings.get", handleSettingsGet)
	RegisterMethod("settings.set", handleSettingsSet)
}

// handleSettingsGet 返回白名单键的当前值。
func handleSettingsGet(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SettingsGetParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	svc, ok := backendService[settingsExtService](d.backend, settingsExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	snapshot, err := svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return normalizeSettingsSnapshot(snapshot), nil
}

// handleSettingsSet 按白名单原子写入，成功后广播 settings.changed。
func handleSettingsSet(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SettingsSetParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if len(p.Values) == 0 {
		return nil, ExtInvalidParams("Invalid params: 'values' must contain at least one key")
	}
	// 白名单在 handler 先拦一道：越权键连服务都调不到，配置文件自然不受影响。
	unknown := make([]string, 0, len(p.Values))
	for key := range p.Values {
		if !IsSettingsWritableKey(key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, ExtInvalidParams("Invalid params: '" + unknown[0] + "' is not a writable setting")
	}
	svc, ok := backendService[settingsExtService](d.backend, settingsExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	snapshot, err := svc.Set(ctx, p.Values)
	if err != nil {
		return nil, err
	}
	snapshot = normalizeSettingsSnapshot(snapshot)
	if d != nil {
		// 桌面侧的刷新由 service 自己调 client.notifyConfigChanged，这里只负责推给远程客户端。
		d.NotifyAuthed(settingsExtChangedMethod, wire.SettingsChangedNotification{Values: snapshot.Values})
	}
	return snapshot, nil
}

// normalizeSettingsSnapshot 把服务给的快照收敛成协议形状：
// 白名单固定用本地常量（不让服务决定可写什么）、丢弃白名单外的值（免得漏出凭据）、
// 空列表给空数组而不是 null。
func normalizeSettingsSnapshot(snapshot wire.SettingsSnapshot) wire.SettingsSnapshot {
	values := make(map[string]json.RawMessage, len(SettingsWritableKeys))
	for _, key := range SettingsWritableKeys {
		if v, ok := snapshot.Values[key]; ok {
			values[key] = v
		}
	}
	quickReplies := snapshot.QuickReplies
	if quickReplies == nil {
		quickReplies = []wire.SettingsQuickReply{}
	}
	return wire.SettingsSnapshot{
		Values:       values,
		WritableKeys: append([]string(nil), SettingsWritableKeys...),
		QuickReplies: quickReplies,
	}
}
