package app

// 本文件把桌面端的设置读写接到远程扩展方法上：
// settings.get / settings.set 由 internal/remote/settings_ext_methods.go 负责，
// 它只依赖这里注入的 "settings" 服务。
//
// 写入只有一个出口：config.Update（同一把写锁下 Load → 改 → save），
// 校验全部放在 update 回调里，任何一键非法都返回错误，Update 因此不写文件，
// 配置文件字节不变，也不会出现「改了一半」的配置。
// 成功后调 client.notifyConfigChanged，桌面设置页不重启就刷新（见 internal/ui/settings.go）；
// 给远程客户端的 settings.changed 广播由 remote 层的 handler 统一发出。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

const (
	// settingsStewardNameMaxRunes 是总管昵称的长度上限，与协议文档一致；昵称允许留空。
	settingsStewardNameMaxRunes = 24
	// settingsStalledAlertMaxMinutes 是卡住提醒阈值的上限（一天），超出没有实际意义。
	settingsStalledAlertMaxMinutes = 1440
	// settingsStewardAvatarMaxRunes 是总管头像的 rune 上限：一个 emoji 或一个字，组合 emoji 占两个 rune。
	settingsStewardAvatarMaxRunes = 2
)

// settingsTrayQuotaProviders 是菜单栏额度面板能隐藏的直连供应商，与桌面设置页的勾选项一致。
var settingsTrayQuotaProviders = []string{"claude", "agy", "zai", "cline", "droid"}

// settingsPermissionModes 是 permissions.mode 允许的档位，与桌面权限选择器一致。
var settingsPermissionModes = []string{
	config.ModeReadOnly,
	config.ModeSupervised,
	config.ModeAutoAcceptEdits,
	config.ModeAuto,
	config.ModeFullAccess,
}

// remoteSettingsService 是注入给远程扩展方法的设置服务，实现 remote.settingsExtService。
type remoteSettingsService struct {
	client *Client
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		a.backend.SetService("settings", remoteSettingsService{client: a.remoteClient()})
	})
}

// Get 返回白名单键的当前值与快捷回复。
func (s remoteSettingsService) Get(ctx context.Context) (wire.SettingsSnapshot, error) {
	cfg, err := s.load()
	if err != nil {
		return wire.SettingsSnapshot{}, err
	}
	return settingsSnapshotFromConfig(cfg)
}

// Set 写入一批键值并返回写入后的快照。
func (s remoteSettingsService) Set(ctx context.Context, values map[string]json.RawMessage) (wire.SettingsSnapshot, error) {
	if s.client == nil {
		return wire.SettingsSnapshot{}, errors.New("设置服务不可用")
	}
	dir := s.client.configDir()
	if dir == "" {
		return wire.SettingsSnapshot{}, errors.New("配置目录不可用")
	}
	if len(values) == 0 {
		return wire.SettingsSnapshot{}, remote.ExtInvalidParams("Invalid params: 'values' must contain at least one key")
	}
	if err := config.Update(dir, func(latest *config.Config) error {
		if err := applySettingsValues(latest, values); err != nil {
			return err
		}
		// 落盘前校验整份配置：Update 在回调返回错误时不写文件，配置文件字节保持不变。
		// 校验错误也按参数错误回报，客户端能直接定位是哪一项不对。
		if err := config.Validate(*latest); err != nil {
			return remote.ExtInvalidParams("Invalid params: " + err.Error())
		}
		return nil
	}); err != nil {
		return wire.SettingsSnapshot{}, err
	}

	// 桌面设置页、读配置的短期缓存都靠这个通知刷新；远程客户端的 settings.changed 由 handler 广播。
	s.client.notifyConfigChanged()
	return s.Get(ctx)
}

func (s remoteSettingsService) load() (config.Config, error) {
	if s.client == nil {
		return config.Config{}, errors.New("设置服务不可用")
	}
	dir := s.client.configDir()
	if dir == "" {
		return config.Config{}, errors.New("配置目录不可用")
	}
	return config.Load(dir)
}

// settingsSnapshotFromConfig 把所有白名单键编成一个快照。
// 指针字段给「生效值」而不是 null：界面要显示开关当前是开还是关，nil 的三态由桌面端自己处理。
func settingsSnapshotFromConfig(cfg config.Config) (wire.SettingsSnapshot, error) {
	trayHidden := append([]string(nil), cfg.Startup.TrayQuotaHidden...)
	values := map[string]any{
		remote.SettingsKeyModelsProvider:                     cfg.Models.Provider,
		remote.SettingsKeyPermissionsMode:                    cfg.Permissions.Mode,
		remote.SettingsKeySessionAutoCollapseReasoning:       cfg.Session.AutoCollapseReasoningEnabled(),
		remote.SettingsKeySessionPinSentMessage:              cfg.Session.PinSentMessageEnabled(),
		remote.SettingsKeySessionConciseRepliesEnabled:       cfg.Session.ConciseReplies(),
		remote.SettingsKeyOrchestrationEnabled:               cfg.Orchestration.IsEnabled(),
		remote.SettingsKeyOrchestrationAutoDelegate:          cfg.Orchestration.AutoDelegateEnabled(),
		remote.SettingsKeyOrchestrationMaxBees:               cfg.Orchestration.MaxBees,
		remote.SettingsKeyOrchestrationAlwaysBees:            cfg.Orchestration.AlwaysBees,
		remote.SettingsKeyOrchestrationLeadPlansOnly:         cfg.Orchestration.LeadPlansOnly,
		remote.SettingsKeyOrchestrationWorkspaceMode:         orchestrationWorkspaceMode(cfg.Orchestration.WorkspaceMode),
		remote.SettingsKeyOrchestrationMaxRounds:             cfg.Orchestration.MaxRounds,
		remote.SettingsKeyOrchestrationRetryFailed:           cfg.Orchestration.RetryFailed,
		remote.SettingsKeyOrchestrationAutoClearFinished:     cfg.Orchestration.AutoClearFinished,
		remote.SettingsKeyOrchestrationReviewBeforeMerge:     cfg.Orchestration.ReviewModeValue() != config.ReviewModeOff,
		remote.SettingsKeyOrchestrationReviewMode:            cfg.Orchestration.ReviewModeValue(),
		remote.SettingsKeyOrchestrationReviewProvider:        cfg.Orchestration.ReviewProvider,
		remote.SettingsKeyOrchestrationReviewModel:           cfg.Orchestration.ReviewModel,
		remote.SettingsKeyOrchestrationReviewEffort:          cfg.Orchestration.ReviewEffort,
		remote.SettingsKeyOrchestrationReviewCheckCommand:    cfg.Orchestration.ReviewCheckCommand,
		remote.SettingsKeyOrchestrationReviewMaxChangedLines: cfg.Orchestration.ReviewMaxChangedLines,
		remote.SettingsKeyOrchestrationAutoMerge:             cfg.Orchestration.AutoMerge,
		remote.SettingsKeyOrchestrationAutoPush:              cfg.Orchestration.AutoPush,
		remote.SettingsKeyOrchestrationAutoForwardSeconds:    cfg.Orchestration.AutoForwardQuestionSeconds,
		remote.SettingsKeyNotifyWhenFinished:                 cfg.Notifications.NotifyWhenFinished,
		remote.SettingsKeyNotifyWhenForeground:               cfg.Notifications.NotifyWhenForeground,
		remote.SettingsKeyNotifyOnApproval:                   cfg.Notifications.NotifyOnApproval,
		remote.SettingsKeyPlayFinishSound:                    cfg.Notifications.PlayFinishSound,
		remote.SettingsKeyChimeWhenAttentionNeeded:           cfg.Notifications.ChimeOnAttention(),
		remote.SettingsKeyDormantSound:                       cfg.Notifications.PlayDormantSound(),
		remote.SettingsKeyNotifyWhenStalled:                  cfg.Notifications.StalledAlertEnabled(),
		remote.SettingsKeyStalledAlertMinutes:                int(cfg.Notifications.StalledAlertAfter().Minutes()),
		remote.SettingsKeyTrayQuotaHidden:                    trayHidden,
		remote.SettingsKeyConfirmBeforeDeleting:              cfg.Archive.ConfirmBeforeDeleting,
		remote.SettingsKeySidebarAutoDormantDays:             cfg.Archive.AutoDormantDays(),
		remote.SettingsKeyStewardName:                        strings.TrimSpace(cfg.Orchestration.Steward.Name),
		remote.SettingsKeyStewardAvatar:                      cfg.Orchestration.Steward.Avatar,
		remote.SettingsKeyStewardQuietStart:                  cfg.Orchestration.Steward.QuietStart,
		remote.SettingsKeyStewardQuietEnd:                    cfg.Orchestration.Steward.QuietEnd,
		remote.SettingsKeyStewardEnabled:                     cfg.Orchestration.Steward.Enabled,
		remote.SettingsKeyStewardCLIFallback:                 cfg.Orchestration.Steward.CLIFallback,
		remote.SettingsKeyStewardAutoCollapseReasoning:       cfg.Orchestration.Steward.AutoCollapseReasoningEnabled(),
	}
	out := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return wire.SettingsSnapshot{}, fmt.Errorf("marshal setting %s: %w", key, err)
		}
		out[key] = raw
	}
	quickReplies := make([]wire.SettingsQuickReply, 0, len(cfg.QuickReplies))
	for _, item := range cfg.QuickReplies {
		quickReplies = append(quickReplies, wire.SettingsQuickReply{Label: item.Label, Message: item.Message})
	}
	return wire.SettingsSnapshot{
		Values:       out,
		WritableKeys: append([]string(nil), remote.SettingsWritableKeys...),
		QuickReplies: quickReplies,
	}, nil
}

// orchestrationWorkspaceMode 返回生效的工作空间模式：空串按 worktree 处理，与桌面选择器的默认值一致。
func orchestrationWorkspaceMode(mode string) string {
	if mode == "" {
		return config.WorkspaceModeWorktree
	}
	return mode
}

// settingsWorkspaceModes 是 orchestration.workspace_mode 允许的取值，与桌面选择器一致。
var settingsWorkspaceModes = []string{config.WorkspaceModeWorktree, config.WorkspaceModeShared}

// settingsReviewModes 是 orchestration.review_mode 允许的取值。
var settingsReviewModes = []string{config.ReviewModeOff, config.ReviewModeAI, config.ReviewModeHuman}

// applySettingsValues 按键名升序逐个写：出错时错误文案稳定，便于客户端定位。
func applySettingsValues(cfg *config.Config, values map[string]json.RawMessage) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := applySettingsValue(cfg, key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

// applySettingsValue 写入单个键，值不合法时返回 InvalidParams 且不改动 cfg。
func applySettingsValue(cfg *config.Config, key string, raw json.RawMessage) error {
	switch key {
	case remote.SettingsKeyModelsProvider:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if !config.IsKnownProvider(*cfg, value) {
			return settingsValueError(key, "必须是已配置的 provider，收到 "+strconv.Quote(value))
		}
		cfg.Models.Provider = value
		// 与桌面 rememberUsedProvider 一致：显式选过 Agent 之后就不再算首次启动。
		cfg.Models.ProviderSelected = true
		return nil

	case remote.SettingsKeyPermissionsMode:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if !containsString(settingsPermissionModes, value) {
			return settingsValueError(key, "可选 "+strings.Join(settingsPermissionModes, "、")+"，收到 "+strconv.Quote(value))
		}
		cfg.Permissions.Mode = value
		return nil

	case remote.SettingsKeySessionAutoCollapseReasoning:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Session.AutoCollapseReasoning = &value
		return nil

	case remote.SettingsKeySessionPinSentMessage:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Session.PinSentMessage = &value
		return nil

	case remote.SettingsKeySessionConciseRepliesEnabled:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Session.ConciseRepliesEnabled = &value
		return nil

	case remote.SettingsKeyOrchestrationEnabled:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.Enabled = &value
		return nil

	case remote.SettingsKeyOrchestrationAutoDelegate:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.AutoDelegate = &value
		return nil

	case remote.SettingsKeyOrchestrationMaxBees:
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < config.MinSwarmMaxBees || value > config.MaxSwarmMaxBees {
			return settingsValueError(key, fmt.Sprintf("范围 %d..%d，收到 %d", config.MinSwarmMaxBees, config.MaxSwarmMaxBees, value))
		}
		cfg.Orchestration.MaxBees = value
		return nil

	case remote.SettingsKeyOrchestrationAlwaysBees:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.AlwaysBees = value
		return nil

	case remote.SettingsKeyOrchestrationLeadPlansOnly:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.LeadPlansOnly = value
		return nil

	case remote.SettingsKeyOrchestrationWorkspaceMode:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if !containsString(settingsWorkspaceModes, value) {
			return settingsValueError(key, "可选 "+strings.Join(settingsWorkspaceModes, "、")+"，收到 "+strconv.Quote(value))
		}
		cfg.Orchestration.WorkspaceMode = value
		return nil

	case remote.SettingsKeyOrchestrationMaxRounds:
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < config.MinSwarmMaxRounds || value > config.MaxSwarmMaxRounds {
			return settingsValueError(key, fmt.Sprintf("范围 %d..%d，收到 %d", config.MinSwarmMaxRounds, config.MaxSwarmMaxRounds, value))
		}
		cfg.Orchestration.MaxRounds = value
		return nil

	case remote.SettingsKeyOrchestrationRetryFailed:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.RetryFailed = value
		return nil

	case remote.SettingsKeyOrchestrationAutoClearFinished:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.AutoClearFinished = value
		return nil

	case remote.SettingsKeyOrchestrationReviewBeforeMerge:
		// 旧布尔键保留做兼容：true 映射到人工审查，false 关闭审查。
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		if value {
			cfg.Orchestration.ReviewMode = config.ReviewModeHuman
		} else {
			cfg.Orchestration.ReviewMode = config.ReviewModeOff
		}
		return nil

	case remote.SettingsKeyOrchestrationReviewMode:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if !containsString(settingsReviewModes, value) {
			return settingsValueError(key, "可选 "+strings.Join(settingsReviewModes, "、")+"，收到 "+strconv.Quote(value))
		}
		cfg.Orchestration.ReviewMode = value
		return nil

	case remote.SettingsKeyOrchestrationReviewProvider:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.ReviewProvider = value
		return nil

	case remote.SettingsKeyOrchestrationReviewModel:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.ReviewModel = value
		return nil

	case remote.SettingsKeyOrchestrationReviewEffort:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.ReviewEffort = value
		return nil

	case remote.SettingsKeyOrchestrationReviewCheckCommand:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if len(value) > config.MaxReviewCheckCommandLen {
			return settingsValueError(key, fmt.Sprintf("最长 %d 字节，收到 %d 字节", config.MaxReviewCheckCommandLen, len(value)))
		}
		cfg.Orchestration.ReviewCheckCommand = value
		return nil

	case remote.SettingsKeyOrchestrationReviewMaxChangedLines:
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < config.MinReviewMaxChangedLines || value > config.MaxReviewMaxChangedLines {
			return settingsValueError(key, fmt.Sprintf("范围 %d..%d，收到 %d", config.MinReviewMaxChangedLines, config.MaxReviewMaxChangedLines, value))
		}
		cfg.Orchestration.ReviewMaxChangedLines = value
		return nil

	case remote.SettingsKeyOrchestrationAutoMerge:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.AutoMerge = value
		return nil

	case remote.SettingsKeyOrchestrationAutoPush:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.AutoPush = value
		return nil

	case remote.SettingsKeyOrchestrationAutoForwardSeconds:
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < 0 || value > config.MaxSwarmAutoForwardQuestionSeconds {
			return settingsValueError(key, fmt.Sprintf("范围 0..%d，收到 %d", config.MaxSwarmAutoForwardQuestionSeconds, value))
		}
		cfg.Orchestration.AutoForwardQuestionSeconds = value
		return nil

	case remote.SettingsKeyNotifyWhenFinished:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.NotifyWhenFinished = value
		return nil

	case remote.SettingsKeyNotifyWhenForeground:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.NotifyWhenForeground = value
		return nil

	case remote.SettingsKeyNotifyOnApproval:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.NotifyOnApproval = value
		return nil

	case remote.SettingsKeyPlayFinishSound:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.PlayFinishSound = value
		return nil

	case remote.SettingsKeyChimeWhenAttentionNeeded:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.ChimeWhenAttentionNeeded = &value
		return nil

	case remote.SettingsKeyDormantSound:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.DormantSound = &value
		return nil

	case remote.SettingsKeyNotifyWhenStalled:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Notifications.NotifyWhenStalled = &value
		return nil

	case remote.SettingsKeyStalledAlertMinutes:
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < 0 || value > settingsStalledAlertMaxMinutes {
			return settingsValueError(key, fmt.Sprintf("范围 0..%d，收到 %d", settingsStalledAlertMaxMinutes, value))
		}
		cfg.Notifications.StalledAlertMinutes = value
		return nil

	case remote.SettingsKeyTrayQuotaHidden:
		var list []string
		if err := decodeSettingsValue(key, raw, &list); err != nil {
			return err
		}
		hidden := make([]string, 0, len(list))
		seen := make(map[string]bool, len(list))
		for _, item := range list {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if !containsString(settingsTrayQuotaProviders, item) {
				return settingsValueError(key, "只能包含 "+strings.Join(settingsTrayQuotaProviders, "、")+"，收到 "+strconv.Quote(item))
			}
			if seen[item] {
				continue
			}
			seen[item] = true
			hidden = append(hidden, item)
		}
		cfg.Startup.TrayQuotaHidden = hidden
		return nil

	case remote.SettingsKeyConfirmBeforeDeleting:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Archive.ConfirmBeforeDeleting = value
		return nil

	case remote.SettingsKeySidebarAutoDormantDays:
		// null 表示回到默认值（7 天），因此允许显式清空。
		if isJSONNull(raw) {
			cfg.Archive.SidebarAutoDormantDays = nil
			return nil
		}
		value, err := decodeSettingsInt(key, raw)
		if err != nil {
			return err
		}
		if value < 0 {
			return settingsValueError(key, fmt.Sprintf("不能为负数，收到 %d", value))
		}
		cfg.Archive.SidebarAutoDormantDays = &value
		return nil

	case remote.SettingsKeyStewardName:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		trimmed := strings.TrimSpace(value)
		if n := len([]rune(trimmed)); n > settingsStewardNameMaxRunes {
			return settingsValueError(key, fmt.Sprintf("总管昵称最多 %d 个字", settingsStewardNameMaxRunes))
		}
		cfg.Orchestration.Steward.Name = trimmed
		return nil

	case remote.SettingsKeyStewardAvatar:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		// 与桌面输入框、配置归一一致：一个 emoji 或一个字，组合 emoji 最多两个 rune；留空由界面取昵称首字，无昵称时取「总」。
		trimmed := strings.TrimSpace(value)
		if n := len([]rune(trimmed)); n > settingsStewardAvatarMaxRunes {
			return settingsValueError(key, fmt.Sprintf("最多 %d 个字（一个 emoji 或一个字），收到 %d 个", settingsStewardAvatarMaxRunes, n))
		}
		cfg.Orchestration.Steward.Avatar = trimmed
		return nil

	case remote.SettingsKeyStewardQuietStart:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if err := validateQuietClock(key, value); err != nil {
			return err
		}
		cfg.Orchestration.Steward.QuietStart = value
		return nil

	case remote.SettingsKeyStewardQuietEnd:
		value, err := decodeSettingsString(key, raw)
		if err != nil {
			return err
		}
		if err := validateQuietClock(key, value); err != nil {
			return err
		}
		cfg.Orchestration.Steward.QuietEnd = value
		return nil

	case remote.SettingsKeyStewardEnabled:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.Steward.Enabled = value
		// 与桌面开关一致：用户亲手切过之后置 true，后续迁移不再改 enabled。
		cfg.Orchestration.Steward.EnabledChoice = true
		return nil

	case remote.SettingsKeyStewardCLIFallback:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		cfg.Orchestration.Steward.CLIFallback = value
		return nil

	case remote.SettingsKeyStewardAutoCollapseReasoning:
		value, err := decodeSettingsBool(key, raw)
		if err != nil {
			return err
		}
		// 对外的生效值与开关相反：读 true 表示折叠开着，对应 AutoCollapseReasoningOff 为 false。
		cfg.Orchestration.Steward.AutoCollapseReasoningOff = !value
		return nil
	}
	// handler 已经拦过白名单，走到这里说明两边白名单不同步。
	return remote.ExtInvalidParams("Invalid params: '" + key + "' is not a writable setting")
}

// validateQuietClock 校验安静时段的时刻：空字符串表示不设，否则必须是 24 小时制的 HH:MM。
func validateQuietClock(key, value string) error {
	if value == "" {
		return nil
	}
	if _, ok := config.ParseClock(value); !ok {
		return settingsValueError(key, "格式必须是 HH:MM 或留空，收到 "+strconv.Quote(value))
	}
	return nil
}

func settingsValueError(key, reason string) error {
	return remote.ExtInvalidParams("Invalid params: '" + key + "': " + reason)
}

// decodeSettingsValue 严格解码：未知字段、类型不符都直接报错，不做静默容错。
func decodeSettingsValue(key string, raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return settingsValueError(key, "值不合法："+err.Error())
	}
	return nil
}

func decodeSettingsString(key string, raw json.RawMessage) (string, error) {
	var value string
	if err := decodeSettingsValue(key, raw, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func decodeSettingsBool(key string, raw json.RawMessage) (bool, error) {
	var value bool
	if err := decodeSettingsValue(key, raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func decodeSettingsInt(key string, raw json.RawMessage) (int, error) {
	var value int
	if err := decodeSettingsValue(key, raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
