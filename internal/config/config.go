// Package config 读写 <data>/config.json，并通过 mygo.Secrets 保存密钥。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Version 是当前支持的 config.json 结构版本。
const Version = 9

// 模型角色常量，分别对应设置 -> 模型的 规划角色 (Lead Agent)、编码角色 (Coder)、快搜角色 (Research)、视觉角色 (Vision)。
// 模型角色概念已弃用，运行时不再读取，仅为兼容旧配置文件保留。
const (
	RoleLead     = "lead"
	RoleCoder    = "coder"
	RoleResearch = "research"
	RoleVision   = "vision"
	RoleUtility  = "utility"
)

// ModelRoles 定义所有模型角色列表（按界面显示顺序排列）。
var ModelRoles = []string{RoleLead, RoleCoder, RoleResearch, RoleVision, RoleUtility}

// 预设常量定义。
const (
	// 支持的 Provider。
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
	ProviderCline  = "cline"
	ProviderMemory = "memory"
	ProviderAgy    = "agy"

	// CLI 家族中具名字段的 Provider。
	ProviderCopilot     = "copilot"
	ProviderPi          = "pi"
	ProviderOmp         = "omp"
	ProviderCommandCode = "commandcode"

	// 权限模式常量（与 internal/harness 中 ModeReadOnly、ModeSupervised、ModeAutoAcceptEdits、ModeAuto、ModeFullAccess 保持严格一致）。
	ModeReadOnly        = "readonly"        // 对应 internal/harness.ModeReadOnly
	ModeSupervised      = "supervised"      // 对应 internal/harness.ModeSupervised
	ModeAutoAcceptEdits = "autoAcceptEdits" // 已废弃：仅读取旧数据时使用
	ModeAuto            = "auto"            // 对应 internal/harness.ModeAuto
	ModeFullAccess      = "fullAccess"      // 对应 internal/harness.ModeFullAccess

	// 兼容旧模式常量（与 internal/harness 中旧值一致）
	ModeAgent = "agent" // 旧值，规范化为 ModeAuto

	// 外观主题常量。
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"

	// 会话完成动作常量。
	FinishActionDormant = "dormant"
	FinishActionArchive = "archive"

	// 工作区模式常量。
	WorkspaceModeLocal    = "local"
	WorkspaceModeWorktree = "worktree"
	WorkspaceModeShared   = "shared"

	// 合并前审查模式常量：off 直接合入，ai 由审查模型自动审，human 等人工批准。
	ReviewModeOff   = "off"
	ReviewModeAI    = "ai"
	ReviewModeHuman = "human"

	// 合并前审查漏斗：检查命令最长字节数与 L1 自动通过的改动行数上限。
	MinReviewMaxChangedLines     = 0
	DefaultReviewMaxChangedLines = 40
	MaxReviewMaxChangedLines     = 500
	MaxReviewCheckCommandLen     = 500

	// 对话密度常量。
	DensityCompact     = "compact"
	DensityComfortable = "comfortable"

	// 对话内容宽度常量。
	ContentWidthStandard = "standard"
	ContentWidthWide     = "wide"
	// ContentWidthFull 是「按比例」档：内容宽度按 ContentWidthPercent 随聊天区宽度缩放，
	// 常量值沿用 full，保证旧配置（content_width=full）继续生效。
	ContentWidthFull = "full"

	// 发送消息快捷键常量：enter 为 Enter 发送、Shift+Enter 换行；shift_enter 反之。
	ComposerSendKeyEnter      = "enter"
	ComposerSendKeyShiftEnter = "shift_enter"

	// 按比例档的内容宽度百分比：默认 85，范围 60..100。
	DefaultContentWidthPercent = 85
	MinContentWidthPercent     = 60
	MaxContentWidthPercent     = 100

	// 退出时行为常量。
	QuitActionQuit           = "quit"
	QuitActionMinimizeToTray = "minimize_to_tray"

	// Workbench 超时与缓冲区默认值及范围。
	DefaultStuckNudgeTimeout     = 600
	MinStuckNudgeTimeout         = 300
	MaxStuckNudgeTimeout         = 7200
	DefaultStuckInterruptTimeout = 1800
	MaxStuckInterruptTimeout     = 14400
	DefaultTerminalScrollback    = 1000
	MinTerminalScrollback        = 100
	MaxTerminalScrollback        = 100000
	DefaultTerminalFontSize      = 13
	MinTerminalFontSize          = 9
	MaxTerminalFontSize          = 28

	// Workbench 上下文注入、看板与编辑器默认值及范围。
	DefaultContextInjectMaxCharacters = 32000
	MinContextInjectMaxCharacters     = 4000
	MaxContextInjectMaxCharacters     = 128000
	KanbanViewKanban                  = "kanban"
	KanbanViewList                    = "list"
	DefaultBrowserStartURL            = "http://localhost:3000"
	DefaultEditorFontSize             = 12
	MinEditorFontSize                 = 10
	MaxEditorFontSize                 = 18

	// Nowledge Mem 记忆注入默认值及范围。
	DefaultWorkingMemoryMaxCharacters = 8000
	MinWorkingMemoryMaxCharacters     = 1000
	MaxWorkingMemoryMaxCharacters     = 32000

	// Appearance 默认值及范围。
	DefaultCodeFontSize = 12
	MinCodeFontSize     = 10
	MaxCodeFontSize     = 24

	// 文字大小（百分比）只缩放侧栏会话列表与聊天内容，默认 100。
	DefaultTextScale = 100

	// Swarm 最大并发数范围。
	MinSwarmMaxBees = 0
	MaxSwarmMaxBees = 8

	// Swarm 派发轮次上限范围。
	MinSwarmMaxRounds = 0
	MaxSwarmMaxRounds = 100

	// Swarm Bee 运行超时（分钟）默认值及范围。
	DefaultSwarmBeeTimeoutMinutes = 60
	MinSwarmBeeTimeoutMinutes     = 5
	MaxSwarmBeeTimeoutMinutes     = 120

	// 单次用户请求的编排总时长预算（分钟）默认值及范围。
	DefaultSwarmRequestTimeoutMinutes = 180
	MinSwarmRequestTimeoutMinutes     = 30
	MaxSwarmRequestTimeoutMinutes     = 720

	// 子线程提问自动转交主线程的最长等待秒数，0 表示关闭。
	MaxSwarmAutoForwardQuestionSeconds = 3600

	// Swarm 档位默认值与建议上限。
	DefaultSwarmTierID     = "edit"
	MaxSuggestedSwarmTiers = 5

	// 远程控制默认 HTTP 端口。
	DefaultRemoteHTTPPort = 43117
	// 远程控制默认 TLS 端口。
	DefaultRemoteTLSPort = 8443
	// 绑定所有网卡，供局域网设备访问；连接仍需配对鉴权。
	DefaultRemoteListen = "0.0.0.0:43117"
	// 远程控制默认 TLS 监听地址。
	DefaultRemoteTLSListen = "0.0.0.0:8443"

	// ModelList 限制常量。
	DefaultModelListLimit = 30
	MaxModelListLimit     = 30
	MinModelListLimit     = 1

	// DefaultStalledAlertMinutes 是会话卡住提醒的默认无进展阈值（分钟）。
	DefaultStalledAlertMinutes = 10

	// DefaultNoProgressMinutes 是会话或 bee 连续无文件改动、无看板变化的默认阈值（分钟）。
	DefaultNoProgressMinutes = 45

	// 总管的默认名字，用户没设昵称时用它。
	DefaultStewardName = "总管"

	// 总管（Steward）默认每日唤醒上限与取值范围。
	DefaultStewardDailyWakeLimit = 30
	MinStewardDailyWakeLimit     = 1
	MaxStewardDailyWakeLimit     = 200
)

// TextScaleLevels 是「文字大小」的可选档位（百分比）：
// 只有这几个档位，其他值（含缺省 0）一律按 100 处理。
var TextScaleLevels = []int{85, 100, 115, 130, 150}

// 快捷回复的长度上限：标题按「按钮上那行字」来定，正文按「填进草稿的一段话」来定。
const (
	MaxQuickReplyLabelRunes   = 40
	MaxQuickReplyMessageRunes = 4000
)

// QuickReply 是输入框上方的一枚快捷回复：Label 是按钮上的文字，Message 是点击后填进草稿的正文。
// 空列表表示不显示快捷回复这一行。
type QuickReply struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

// ValidateQuickReply 校验单条快捷回复：标题与正文都去掉首尾空白后必须在 1..上限 之间。
// 返回的文案可以直接展示给用户。
func ValidateQuickReply(item QuickReply) error {
	if len([]rune(strings.TrimSpace(item.Label))) == 0 {
		return fmt.Errorf("标题不能为空")
	}
	if len([]rune(strings.TrimSpace(item.Label))) > MaxQuickReplyLabelRunes {
		return fmt.Errorf("标题不能超过 %d 个字", MaxQuickReplyLabelRunes)
	}
	if len([]rune(strings.TrimSpace(item.Message))) == 0 {
		return fmt.Errorf("内容不能为空")
	}
	if len([]rune(strings.TrimSpace(item.Message))) > MaxQuickReplyMessageRunes {
		return fmt.Errorf("内容不能超过 %d 个字", MaxQuickReplyMessageRunes)
	}
	return nil
}

// ValidateQuickReplies 校验整组快捷回复，返回与入参等长的逐行文案（空字符串表示该行合法）
// 以及第一条错误的可展示文案；空列表合法，表示不使用快捷回复。
func ValidateQuickReplies(items []QuickReply) ([]string, string) {
	errs := make([]string, len(items))
	first := ""
	for i, item := range items {
		if err := ValidateQuickReply(item); err != nil {
			errs[i] = err.Error()
			if first == "" {
				first = fmt.Sprintf("第 %d 条快捷回复：%s", i+1, err.Error())
			}
		}
	}
	return errs, first
}

// NormalizeQuickReplies 把每条标题与正文的首尾空白去掉，并丢掉标题和正文都为空的行；
// 全部为空时返回 nil，等价于不使用快捷回复。
func NormalizeQuickReplies(items []QuickReply) []QuickReply {
	if len(items) == 0 {
		return nil
	}
	out := make([]QuickReply, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		message := strings.TrimSpace(item.Message)
		if label == "" && message == "" {
			continue
		}
		out = append(out, QuickReply{Label: label, Message: message})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ModelPin 表示模型选择器中的固定模型。
type ModelPin struct {
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

// Key 返回模型标识符 provider/model_id。
func (p ModelPin) Key() string {
	return p.Provider + "/" + p.ModelID
}

// ModelPreference 记录模型在新建会话时的默认偏好设置。
type ModelPreference struct {
	Effort   *string `json:"effort,omitempty"`
	FastMode bool    `json:"fast_mode,omitempty"`
}

// ModelSelectionConfig 记录一次完整的模型选择（provider + model + 推理强度 + 快速模式）。
// Provider 为空表示未指定，调用方按各自的回退规则处理。
type ModelSelectionConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Fast     bool   `json:"fast,omitempty"`
}

// SwarmPair 表示一组 Swarm 主代理与子代理模型配对。
type SwarmPair struct {
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	LeadProvider   string                       `json:"lead_provider"`
	LeadModel      string                       `json:"lead_model"`
	LeadEffort     *string                      `json:"lead_effort,omitempty"`
	LeadFastMode   bool                         `json:"lead_fast_mode,omitempty"`
	WorkerProvider string                       `json:"worker_provider"`
	WorkerModel    string                       `json:"worker_model"`
	WorkerEffort   *string                      `json:"worker_effort,omitempty"`
	WorkerFastMode bool                         `json:"worker_fast_mode,omitempty"`
	BeeTiers       map[string]SwarmTierOverride `json:"bee_tiers,omitempty"`
}

// SwarmTier 表示单个子代理（bee）档位定义。
// Provider 与 Model 为空表示跟随会话当前生效的子代理模型。
type SwarmTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	// WritePaths 非空时该档 bee 只能把匹配这些 glob 的改动合并回主工作区。
	WritePaths   []string `json:"write_paths,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	FastMode     bool     `json:"fast_mode,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
}

// SwarmTierOverride 表示针对特定档位的配对级覆盖配置。
type SwarmTierOverride struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	FastMode *bool  `json:"fast_mode,omitempty"` // nil 表示继承
}

// choreTierDescription 是内置 chore（杂活）档位的描述，默认档位与 v4→v5 迁移共用。
const choreTierDescription = "机械活：补丁重放或重做已有补丁、改名、格式化、修测试或补测试、文档与文案修改；brief 已给出文件与改法"

// deepTierDescription 是内置 deep（深度）档位的描述；v5→v6 迁移不改已有档位的描述。
const deepTierDescription = "只做定位、方案设计与关键决策：brief 只给现象、目标或边界而没给改法时，先复现、二分、实测或设计接口，产出带 path:line 的分步方案；复杂时把方案写成 md 文档；不改代码。"

// defaultDeepWritePaths 返回内置 deep（深度）档的默认写入白名单：只允许落地方案文档，不改代码。
// 每次返回新切片，避免调用方之间共享底层数组。
func defaultDeepWritePaths() []string {
	return []string{"docs/superpowers/plans/**/*.md"}
}

// DefaultSwarmTiers 返回内置的四组子代理档位：只读、杂活、执行、深度。
// 所有档位的 Provider 与 Model 均为空，默认跟随会话生效的子代理模型；
// Effort 为各档默认推理强度，深度档用 high，其余用 medium。
func DefaultSwarmTiers() []SwarmTier {
	return []SwarmTier{
		{
			ID:          "read",
			Name:        "只读",
			Description: "只读调研：检索、阅读、总结代码与文档，不修改任何文件",
			ReadOnly:    true,
			Effort:      "medium",
		},
		{
			ID:          "chore",
			Name:        "杂活",
			Description: choreTierDescription,
			Effort:      "medium",
		},
		{
			ID:          DefaultSwarmTierID,
			Name:        "执行",
			Description: "执行步骤已写清楚的明确修改，单文件或少量文件",
			Effort:      "medium",
		},
		{
			ID:          "deep",
			Name:        "深度",
			Description: deepTierDescription,
			WritePaths:  defaultDeepWritePaths(),
			Effort:      "high",
		},
	}
}

func copyStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func copyBoolPtr(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func (p SwarmPair) clone() SwarmPair {
	p.LeadEffort = copyStringPtr(p.LeadEffort)
	p.WorkerEffort = copyStringPtr(p.WorkerEffort)
	if p.BeeTiers != nil {
		cloned := make(map[string]SwarmTierOverride, len(p.BeeTiers))
		for k, v := range p.BeeTiers {
			v.FastMode = copyBoolPtr(v.FastMode)
			cloned[k] = v
		}
		p.BeeTiers = cloned
	}
	return p
}

func boolPtr(b bool) *bool {
	return &b
}

// Config 对应 <data>/config.json。敏感项（API key 等）不在此处存储。
type Config struct {
	Version       int                 `json:"version"`
	Session       SessionConfig       `json:"session"`
	Models        ModelsConfig        `json:"models"`
	Providers     ProvidersConfig     `json:"providers"`
	Orchestration OrchestrationConfig `json:"orchestration"`
	Memory        MemoryConfig        `json:"memory"`
	Startup       StartupConfig       `json:"startup"`
	Notifications NotificationsConfig `json:"notifications"`
	Permissions   PermissionsConfig   `json:"permissions"`
	Appearance    AppearanceConfig    `json:"appearance"`
	Workbench     WorkbenchConfig     `json:"workbench"`
	QuickReplies  []QuickReply        `json:"quick_replies,omitempty"` // 输入框上方的快捷回复按钮，空表示不显示
	Shortcuts     ShortcutsConfig     `json:"shortcuts"`
	Remote        RemoteConfig        `json:"remote"`
	SourceControl SourceControlConfig `json:"source_control"`
	Archive       ArchiveConfig       `json:"archive"`
	Retention     RetentionConfig     `json:"retention,omitempty"` // 数据保留任务开关，默认全开
	Automation    AutomationConfig    `json:"automation,omitempty"`
}

// AutomationConfig 自动化后台行为设置。
type AutomationConfig struct {
	// PreventSleep 表示有自动化正在运行或 5 分钟内将要运行时阻止电脑休眠，nil 视为开启。
	PreventSleep *bool `json:"prevent_sleep,omitempty"`
}

// PreventSleepEnabled 返回自动化防休眠是否开启，nil 视为开启。
func (a AutomationConfig) PreventSleepEnabled() bool {
	if a.PreventSleep == nil {
		return true
	}
	return *a.PreventSleep
}

type configAlias Config

// UnmarshalJSON 在反序列化时将 Utility.Provider 清空，使旧配置或未显式配置 utility 的配置
// 能在 Normalize 中正确继承 Models.Provider。
func (c *Config) UnmarshalJSON(data []byte) error {
	c.Models.Roles.Utility.Provider = ""
	return json.Unmarshal(data, (*configAlias)(c))
}

// SessionConfig 会话行为设置。
type SessionConfig struct {
	ShowReasoning          *bool  `json:"show_reasoning,omitempty"`            // 显示思考过程，nil 视为开启
	AutoCollapseReasoning  *bool  `json:"auto_collapse_reasoning,omitempty"`   // 思考内容自动折叠，nil 视为开启
	PinSentMessage         *bool  `json:"pin_sent_message,omitempty"`          // 发送后把消息置顶到时间线顶部，nil 视为关闭
	ConciseRepliesEnabled  *bool  `json:"concise_replies_enabled,omitempty"`   // 简洁回复引导，nil 视为关闭
	QueueWhileRunning      bool   `json:"queue_while_running"`                 // 运行中插入消息，默认 true
	InsertIntoRunningTurn  bool   `json:"insert_into_running_turn"`            // 运行中的消息插入当前回合（Provider 支持中途注入时），不支持则排队；默认 true
	AutoContinueAfterLimit bool   `json:"auto_continue_after_limit"`           // 额度用尽后自动等待续跑，默认 false
	AskCompactBeforeResume *bool  `json:"ask_compact_before_resume,omitempty"` // 恢复旧会话前询问压缩上下文，nil 视为开启
	ThreadFinishAction     string `json:"thread_finish_action"`                // 会话完成动作：dormant | archive，默认 dormant
	ComposerSendKey        string `json:"composer_send_key,omitempty"`         // 发送消息快捷键：enter | shift_enter，默认 enter（Enter 发送，Shift+Enter 换行）
	SkillsInjectsPrompt    *bool  `json:"skills_injects_prompt,omitempty"`     // 技能规则注入提示词，nil 视为开启
}

// PermissionsConfig 权限设置。
type PermissionsConfig struct {
	Mode string `json:"mode"` // readonly | supervised | autoAcceptEdits | auto | fullAccess
}

// NotificationsConfig 通知与提示音设置。
type NotificationsConfig struct {
	NotifyWhenFinished       bool  `json:"notify_when_finished"`                  // 轮次结束通知
	NotifyWhenForeground     bool  `json:"notify_when_foreground"`                // 前台也发送通知，默认 false
	NotifyOnApproval         bool  `json:"notify_on_approval"`                    // 待审批时发系统通知
	PlayFinishSound          bool  `json:"play_finish_sound"`                     // 播放完成提示音
	ChimeWhenAttentionNeeded *bool `json:"chime_when_attention_needed,omitempty"` // 需要用户介入时播放提示音，nil 视为开启
	DormantSound             *bool `json:"dormant_sound,omitempty"`               // 会话休眠时播放提示音，nil 视为开启
	NotifyWhenStalled        *bool `json:"notify_when_stalled,omitempty"`         // 会话长时间没有进展时提醒，nil 视为开启
	StalledAlertMinutes      int   `json:"stalled_alert_minutes,omitempty"`       // 卡住提醒的无进展阈值（分钟），0 或负数视为默认 10
}

// StartupConfig 启动、托盘与退出设置。
type StartupConfig struct {
	ShowTrayIcon    bool     `json:"show_tray_icon"`              // 显示托盘图标
	TrayShowQuota   bool     `json:"tray_show_quota"`             // 菜单栏图标旁显示在用账号最紧张的额度百分比，默认 false
	TrayQuotaHidden []string `json:"tray_quota_hidden,omitempty"` // 菜单栏额度面板里隐藏的直连供应商 id（claude、agy、zai、cline、droid），默认全部展示
	QuitAction      string   `json:"quit_action"`                 // 退出时行为：quit | minimize_to_tray，默认 quit
}

// ShortcutsConfig 全局快捷键与应用快捷键覆盖设置。
type ShortcutsConfig struct {
	GlobalShortcutEnabled bool              `json:"global_shortcut_enabled"`      // 全局快捷键开关
	GlobalShortcut        string            `json:"global_shortcut"`              // 全局快捷键（mygo accelerator 格式）
	ShortcutOverrides     map[string]string `json:"shortcut_overrides,omitempty"` // 应用快捷键覆盖（键为快捷键 id，值为按键规格，如 Cmd+Shift+P）
}

// ArchiveConfig 会话归档（休眠）设置。
type ArchiveConfig struct {
	ConfirmBeforeDeleting  bool `json:"confirm_before_deleting"`             // 删除前确认
	SidebarAutoDormantDays *int `json:"sidebar_auto_dormant_days,omitempty"` // 会话无新消息超过 N 天自动休眠，nil 视为 7，0 表示关闭
}

// RetentionConfig 数据保留（历史消息压缩）设置。
type RetentionConfig struct {
	// BeeMessages 表示是否执行 bee 子代理会话消息保留任务，nil 视为开启。
	// 主会话不受该任务影响，关闭后旧会话的压缩结果也不会恢复。
	BeeMessages *bool `json:"bee_messages,omitempty"`
	// ColdArchive 表示是否执行会话冷归档任务，nil 视为开启。
	ColdArchive *bool `json:"cold_archive,omitempty"`
}

// BeeMessagesEnabled 返回是否执行 bee 子代理会话消息保留任务，nil 视为开启。
func (r RetentionConfig) BeeMessagesEnabled() bool {
	if r.BeeMessages == nil {
		return true
	}
	return *r.BeeMessages
}

// ColdArchiveEnabled 返回是否执行会话冷归档任务，nil 视为开启。
func (r RetentionConfig) ColdArchiveEnabled() bool {
	if r.ColdArchive == nil {
		return true
	}
	return *r.ColdArchive
}

// SkillsInjectPromptEnabled 返回是否注入技能规则到提示词，nil 时默认开启。
func (s SessionConfig) SkillsInjectPromptEnabled() bool {
	if s.SkillsInjectsPrompt == nil {
		return true
	}
	return *s.SkillsInjectsPrompt
}

// ChimeOnAttention 返回需要用户介入时是否播放提示音，nil 时默认开启。
func (n NotificationsConfig) ChimeOnAttention() bool {
	if n.ChimeWhenAttentionNeeded == nil {
		return true
	}
	return *n.ChimeWhenAttentionNeeded
}

// StalledAlertEnabled 返回会话长时间没有进展时是否提醒，nil 时默认开启。
func (n NotificationsConfig) StalledAlertEnabled() bool {
	if n.NotifyWhenStalled == nil {
		return true
	}
	return *n.NotifyWhenStalled
}

// StalledAlertAfter 返回判定会话卡住的无进展阈值：0 或负数视为默认 10 分钟。
func (n NotificationsConfig) StalledAlertAfter() time.Duration {
	minutes := n.StalledAlertMinutes
	if minutes <= 0 {
		minutes = DefaultStalledAlertMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// PlayDormantSound 返回会话休眠时是否播放提示音，nil 时默认开启。
func (n NotificationsConfig) PlayDormantSound() bool {
	if n.DormantSound == nil {
		return true
	}
	return *n.DormantSound
}

// AutoDormantDays 返回会话无新消息自动休眠的天数：nil 视为 7，负数视为 0（关闭），其余返回原值。
func (a ArchiveConfig) AutoDormantDays() int {
	if a.SidebarAutoDormantDays == nil {
		return 7
	}
	if *a.SidebarAutoDormantDays < 0 {
		return 0
	}
	return *a.SidebarAutoDormantDays
}

// AskCompactOnResume 返回恢复旧会话前是否询问压缩上下文，nil 时默认开启。
func (s SessionConfig) AskCompactOnResume() bool {
	if s.AskCompactBeforeResume == nil {
		return true
	}
	return *s.AskCompactBeforeResume
}

// ShowReasoningEnabled 返回是否显示思考过程，nil 时默认开启。
func (s SessionConfig) ShowReasoningEnabled() bool {
	if s.ShowReasoning == nil {
		return true
	}
	return *s.ShowReasoning
}

// AutoCollapseReasoningEnabled 返回是否自动折叠思考内容，nil 时默认开启。
func (s SessionConfig) AutoCollapseReasoningEnabled() bool {
	if s.AutoCollapseReasoning == nil {
		return true
	}
	return *s.AutoCollapseReasoning
}

// PinSentMessageEnabled 返回发送后是否把消息置顶到时间线顶部，nil 时默认关闭。
func (s SessionConfig) PinSentMessageEnabled() bool {
	if s.PinSentMessage == nil {
		return false
	}
	return *s.PinSentMessage
}

// ConciseReplies 返回是否启用简洁回复引导，nil 时默认关闭。
func (s SessionConfig) ConciseReplies() bool {
	if s.ConciseRepliesEnabled == nil {
		return false
	}
	return *s.ConciseRepliesEnabled
}

// AppearanceConfig 外观设置。
type AppearanceConfig struct {
	Theme               string `json:"theme"`                 // system | light | dark
	CodeFont            string `json:"code_font"`             // 代码字体名称，空表示系统等宽字体
	CodeFontSize        int    `json:"code_font_size"`        // 代码字号，默认 12，范围 10..24
	ConversationDensity string `json:"conversation_density"`  // 对话密度：compact | comfortable，默认 comfortable
	ContentWidth        string `json:"content_width"`         // 对话内容宽度：standard | wide | full（按比例），默认 standard
	ContentWidthPercent int    `json:"content_width_percent"` // 按比例档的内容宽度百分比，60..100，默认 85
	SidebarCompact      bool   `json:"sidebar_compact"`       // 侧边栏紧凑密度，默认 false
	TextScale           int    `json:"text_scale"`            // 文字大小百分比，默认 100，取值见 TextScaleLevels
}

// EffectiveTextScale 返回归一化后的文字大小百分比：不在档位里的值（含缺省的 0）按默认 100 处理。
func (a AppearanceConfig) EffectiveTextScale() int {
	for _, level := range TextScaleLevels {
		if a.TextScale == level {
			return level
		}
	}
	return DefaultTextScale
}

// EffectiveContentWidthPercent 返回归一化后的按比例档内容宽度百分比：
// 缺省的 0 用默认值，越界值钳制到 [MinContentWidthPercent, MaxContentWidthPercent]。
func (a AppearanceConfig) EffectiveContentWidthPercent() int {
	if a.ContentWidthPercent == 0 {
		return DefaultContentWidthPercent
	}
	if a.ContentWidthPercent < MinContentWidthPercent {
		return MinContentWidthPercent
	}
	if a.ContentWidthPercent > MaxContentWidthPercent {
		return MaxContentWidthPercent
	}
	return a.ContentWidthPercent
}

// OrchestrationConfig 编排（子代理 bee）设置。
type OrchestrationConfig struct {
	Enabled               *bool  `json:"enabled,omitempty"`                 // 编排总开关，nil 视为开启
	AutoDelegate          *bool  `json:"auto_delegate,omitempty"`           // 自动委托开关，nil 视为开启
	MaxBees               int    `json:"max_bees"`                          // 最大并发子代理（bee）数，1..8，0 表示不限
	MaxRounds             int    `json:"max_rounds"`                        // 主会话最多可调起子代理的轮次，0 表示不限制，上限 100
	BeeTimeoutMinutes     int    `json:"bee_timeout_minutes,omitempty"`     // 子代理运行超时（分钟），0 表示使用默认值 60
	RequestTimeoutMinutes int    `json:"request_timeout_minutes,omitempty"` // 单次用户请求的编排总时长预算（分钟），0 表示默认 180，范围 30 到 720
	WorkspaceMode         string `json:"workspace_mode"`                    // 子代理工作区模式：worktree | shared，默认 worktree
	RetryFailed           bool   `json:"retry_failed"`                      // 失败自动重试，默认 true
	AutoMerge             bool   `json:"auto_merge"`                        // 「自动合并」：仅本地提交/squash 合并并清理 worktree，不做远端操作，默认 true
	// 合并前审查：ReviewMode 取 off | ai | human；三个模型字段空表示自动选择最便宜的可用审查模型。
	// ReviewMaxChangedLines 故意不带 omitempty：0 表示关闭行数规则，必须能落盘并读回；
	// 旧配置缺字段时由 Load 的 Defaults() 预填为 40。
	ReviewMode     string `json:"review_mode,omitempty"`     // 合并前审查模式：off | ai | human，默认 off
	ReviewProvider string `json:"review_provider,omitempty"` // 审查会话的提供方，空表示自动选择最便宜的可用审查模型
	ReviewModel    string `json:"review_model,omitempty"`    // 审查会话的模型，空表示自动选择最便宜的可用审查模型
	ReviewEffort   string `json:"review_effort,omitempty"`   // 审查会话的推理强度，空表示自动选择最便宜的可用审查模型
	// 审查前的检查命令，空表示自动识别；超过 MaxReviewCheckCommandLen 字节会在 Normalize 中截断。
	ReviewCheckCommand string `json:"review_check_command,omitempty"`
	// L1 自动通过的改动行数上限 0..500，0 表示关闭；没写时 Defaults() 预填 40。
	ReviewMaxChangedLines int         `json:"review_max_changed_lines"`
	AutoPush              bool        `json:"auto_push"`                // 「自动推送」：开启后才允许自动 git push 分支并用 gh/glab 建 PR/MR，默认 false
	DefaultProvider       string      `json:"default_provider"`         // 子代理默认提供方，空表示跟随主代理
	DefaultModel          string      `json:"default_model"`            // 子代理默认模型，空表示跟随主代理
	DefaultEffort         string      `json:"default_effort,omitempty"` // 子代理默认推理强度，空表示跟随模型默认
	Pairs                 []SwarmPair `json:"pairs,omitempty"`          // 主代理与子代理模型配对列表
	Tiers                 []SwarmTier `json:"tiers,omitempty"`          // 档位定义列表
	DefaultTier           string      `json:"default_tier,omitempty"`   // 默认档位 ID
	AlwaysBees            bool        `json:"always_bees"`              // 所有用户消息一律直接派给子代理，默认 false
	LeadPlansOnly         bool        `json:"lead_plans_only"`          // 主代理仅规划不改写文件，默认 true
	AutoClearFinished     bool        `json:"auto_clear_finished"`      // 自动从面板清理已完成子代理，默认 true
	AutoHidesIdleBees     bool        `json:"auto_hides_idle_bees"`     // 侧边栏自动隐藏空闲子代理，默认 true
	ShowsBeeDetails       bool        `json:"shows_bee_details"`        // 显示子代理详细信息，默认 true
	AutoPopsBees          bool        `json:"auto_pops_bees"`           // 自动弹出子代理窗口，默认 true
	PanelWidth            float32     `json:"panel_width,omitempty"`    // 浮动面板拖拽设定的宽度（pt），0 表示自动
	PanelHeight           float32     `json:"panel_height,omitempty"`   // 浮动面板拖拽设定的高度（pt），0 表示自动
	ArchivesCodexBees     bool        `json:"archives_codex_bees"`      // Codex 子会话完成后自动归档，默认 false
	// AutoForwardQuestionSeconds 子线程提问超过该秒数无人回答时自动交给主线程，0 表示关闭，默认 180。
	AutoForwardQuestionSeconds int `json:"auto_forward_question_seconds"`
	// DelegateTool 开启后，主代理除 ```swarm 块外还能用 swarm_delegate 工具派发子代理，默认 false。
	DelegateTool bool `json:"delegate_tool,omitempty"`
	// Steward 总管配置。
	Steward StewardConfig `json:"steward"`
}

// ReviewModeValue 返回归一化后的合并前审查模式：空串或未知值一律按 off 处理。
func (o OrchestrationConfig) ReviewModeValue() string {
	switch strings.ToLower(strings.TrimSpace(o.ReviewMode)) {
	case ReviewModeAI:
		return ReviewModeAI
	case ReviewModeHuman:
		return ReviewModeHuman
	default:
		return ReviewModeOff
	}
}

// truncateUTF8 把 s 截到不超过 max 字节，且不切断多字节字符。
func truncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// IsEnabled 返回编排是否启用，nil 视为开启。
func (o OrchestrationConfig) IsEnabled() bool {
	if o.Enabled == nil {
		return true
	}
	return *o.Enabled
}

// AutoDelegateEnabled 返回自动委托是否开启，nil 视为开启。
func (o OrchestrationConfig) AutoDelegateEnabled() bool {
	if o.AutoDelegate == nil {
		return true
	}
	return *o.AutoDelegate
}

// BeeTimeoutDuration 返回子代理运行超时时间；小于等于 0 时返回默认 40 分钟。
func (o OrchestrationConfig) BeeTimeoutDuration() time.Duration {
	if o.BeeTimeoutMinutes <= 0 {
		return DefaultSwarmBeeTimeoutMinutes * time.Minute
	}
	return time.Duration(o.BeeTimeoutMinutes) * time.Minute
}

// AutoForwardQuestionDelay 返回子线程提问自动转交主线程的等待时长；小于等于 0 返回 0，上限 3600 秒。
func (o OrchestrationConfig) AutoForwardQuestionDelay() time.Duration {
	if o.AutoForwardQuestionSeconds <= 0 {
		return 0
	}
	secs := o.AutoForwardQuestionSeconds
	if secs > MaxSwarmAutoForwardQuestionSeconds {
		secs = MaxSwarmAutoForwardQuestionSeconds
	}
	return time.Duration(secs) * time.Second
}

// Pair 按 ID 查找配对项，未找到时返回 false。
func (o OrchestrationConfig) Pair(id string) (SwarmPair, bool) {
	for _, p := range o.Pairs {
		if p.ID == id {
			return p.clone(), true
		}
	}
	return SwarmPair{}, false
}

// AddPair 追加配对项；若 ID 为空或已存在，自动生成新 UUID；返回最终追加的 pair。
func (o *OrchestrationConfig) AddPair(p SwarmPair) SwarmPair {
	exists := func(id string) bool {
		for _, existing := range o.Pairs {
			if existing.ID == id {
				return true
			}
		}
		return false
	}
	if p.ID == "" || exists(p.ID) {
		for {
			newID := uuid.NewString()
			if !exists(newID) {
				p.ID = newID
				break
			}
		}
	}
	o.Pairs = append(o.Pairs, p.clone())
	return p
}

// UpdatePair 按 ID 原位更新配对项，若未找到返回 false。
func (o *OrchestrationConfig) UpdatePair(p SwarmPair) bool {
	for i, existing := range o.Pairs {
		if existing.ID == p.ID {
			o.Pairs[i] = p.clone()
			return true
		}
	}
	return false
}

// RemovePair 按 ID 删除配对项，返回是否成功删除。
func (o *OrchestrationConfig) RemovePair(id string) bool {
	for i, existing := range o.Pairs {
		if existing.ID == id {
			o.Pairs = append(o.Pairs[:i], o.Pairs[i+1:]...)
			return true
		}
	}
	return false
}

// WorkbenchConfig 工作台与看板设置。
type WorkbenchConfig struct {
	AutoRescueStuck         *bool  `json:"auto_rescue_stuck,omitempty"`  // 卡死任务自动救援，nil 视为开启
	StuckNudgeTimeout       int    `json:"stuck_nudge_timeout"`          // 无活动提醒阈值（秒），默认 600
	StuckInterruptTimeout   int    `json:"stuck_interrupt_timeout"`      // 无活动强制中断阈值（秒），默认 1800
	EditorAutoSave          *bool  `json:"editor_auto_save,omitempty"`   // 编辑器自动保存，nil 视为开启
	TerminalScrollbackLines int    `json:"terminal_scrollback_lines"`    // 终端缓冲区行数，默认 1000
	TerminalFontSize        int    `json:"terminal_font_size"`           // 终端字号（pt），默认 13
	TerminalShell           string `json:"terminal_shell"`               // 默认终端 shell，空表示跟随系统 SHELL
	KanbanAutoReview        *bool  `json:"kanban_auto_review,omitempty"` // 看板任务完成后自动转为 Review，nil 视为开启
	KanbanAutoStartOnCreate bool   `json:"kanban_auto_start_on_create"`  // 看板任务创建后自动启动，默认 false
	BrowserQuickPorts       string `json:"browser_quick_ports"`          // 浏览器快捷端口列表，默认 "3000, 5173, 8080, 4321"
	// TasksAutoMergeWorktree 看板验收后自动合并 worktree，nil 视为开启。
	TasksAutoMergeWorktree *bool `json:"tasks_auto_merge_worktree,omitempty"`

	ContextInjectsGuidance     *bool    `json:"context_injects_guidance,omitempty"`   // 注入项目说明，nil 视为开启
	ContextInjectMaxCharacters int      `json:"context_inject_max_characters"`        // 项目说明注入字符上限，默认 32000，范围 4000..128000
	ContextInjectCustomDocs    []string `json:"context_inject_custom_docs,omitempty"` // 额外注入的 docs 文档名
	KanbanDefaultView          string   `json:"kanban_default_view"`                  // 看板默认视图："kanban" 或 "list"，默认 "kanban"
	KanbanRunInWorktree        *bool    `json:"kanban_run_in_worktree,omitempty"`     // 看板任务在 worktree 中执行，nil 视为开启
	KanbanMCPEnabled           *bool    `json:"kanban_mcp_enabled,omitempty"`         // 注册看板 MCP 工具，nil 视为开启
	KanbanEntryOrdinary        *bool    `json:"kanban_entry_ordinary,omitempty"`      // 普通会话暴露单一 kanban 入口工具，nil 视为开启
	BrowserStartURL            string   `json:"browser_start_url"`                    // 浏览器起始地址，默认 "http://localhost:3000"
	BrowserConsoleCapture      *bool    `json:"browser_console_capture,omitempty"`    // 捕获浏览器控制台输出，nil 视为开启
	EditorFontSize             int      `json:"editor_font_size"`                     // 编辑器字号，默认 12，范围 10..18
	// OpenInApps 标题栏「打开方式」里可选的应用；为空时使用 DefaultOpenInApps()。
	OpenInApps []OpenInAppConfig `json:"open_in_apps,omitempty"`
}

// OpenInAppConfig 描述「打开方式」中的一个应用。
// AppPath 为空表示访达（不指定应用，直接打开目录）；BundleID 仅作记录，打开时优先用 AppPath。
type OpenInAppConfig struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id,omitempty"`
	AppPath  string `json:"app_path,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// AutoRescueStuckEnabled 返回卡死任务自动救援是否开启，nil 视为开启。
func (w WorkbenchConfig) AutoRescueStuckEnabled() bool {
	if w.AutoRescueStuck == nil {
		return true
	}
	return *w.AutoRescueStuck
}

// EditorAutoSaveEnabled 返回编辑器自动保存是否开启，nil 视为开启。
func (w WorkbenchConfig) EditorAutoSaveEnabled() bool {
	if w.EditorAutoSave == nil {
		return true
	}
	return *w.EditorAutoSave
}

// AutoMergeTaskWorktree 返回看板验收后是否自动合并 worktree，nil 时默认开启。
func (w WorkbenchConfig) AutoMergeTaskWorktree() bool {
	if w.TasksAutoMergeWorktree == nil {
		return true
	}
	return *w.TasksAutoMergeWorktree
}

// KanbanAutoReviewEnabled 返回看板 autoReview 是否开启，nil 视为开启。
func (w WorkbenchConfig) KanbanAutoReviewEnabled() bool {
	if w.KanbanAutoReview == nil {
		return true
	}
	return *w.KanbanAutoReview
}

// ContextGuidanceEnabled 返回是否注入项目说明，nil 视为开启。
func (w WorkbenchConfig) ContextGuidanceEnabled() bool {
	if w.ContextInjectsGuidance == nil {
		return true
	}
	return *w.ContextInjectsGuidance
}

// KanbanRunInWorktreeEnabled 返回看板任务是否在 worktree 中执行，nil 视为开启。
func (w WorkbenchConfig) KanbanRunInWorktreeEnabled() bool {
	if w.KanbanRunInWorktree == nil {
		return true
	}
	return *w.KanbanRunInWorktree
}

// KanbanMCPToolsEnabled 返回是否注册看板 MCP 工具，nil 视为开启。
func (w WorkbenchConfig) KanbanMCPToolsEnabled() bool {
	if w.KanbanMCPEnabled == nil {
		return true
	}
	return *w.KanbanMCPEnabled
}

// KanbanEntryOrdinarySessions 返回普通会话是否暴露单一 kanban 入口工具，nil 视为开启。
func (w WorkbenchConfig) KanbanEntryOrdinarySessions() bool {
	if w.KanbanEntryOrdinary == nil {
		return true
	}
	return *w.KanbanEntryOrdinary
}

// BrowserConsoleCaptureEnabled 返回是否捕获浏览器控制台输出，nil 视为开启。
func (w WorkbenchConfig) BrowserConsoleCaptureEnabled() bool {
	if w.BrowserConsoleCapture == nil {
		return true
	}
	return *w.BrowserConsoleCapture
}

// Editor 列表是「用其他应用打开」的默认条目。
var (
	// macOSDefaultOpenInApps 是 macOS 上的默认打开方式列表，路径均为 .app。
	macOSDefaultOpenInApps = []OpenInAppConfig{
		{Name: "Visual Studio Code", BundleID: "com.microsoft.VSCode", AppPath: "/Applications/Visual Studio Code.app"},
		{Name: "Cursor", BundleID: "com.todesktop.230313mzl4w4u92", AppPath: "/Applications/Cursor.app"},
		{Name: "Zed", BundleID: "dev.zed.Zed", AppPath: "/Applications/Zed.app"},
		{Name: "Xcode", BundleID: "com.apple.dt.Xcode", AppPath: "/Applications/Xcode.app"},
		{Name: "Windsurf", AppPath: "/Applications/Windsurf.app"},
		{Name: "Sublime Text", AppPath: "/Applications/Sublime Text.app"},
		{Name: "iTerm2", BundleID: "com.googlecode.iterm2", AppPath: "/Applications/iTerm.app"},
		{Name: "Terminal", BundleID: "com.apple.Terminal", AppPath: "/System/Applications/Utilities/Terminal.app"},
		{Name: "Ghostty", BundleID: "com.mitchellh.ghostty", AppPath: "/Applications/Ghostty.app"},
		{Name: "QSpace Pro", BundleID: "com.jinghaoshe.qspace.pro", AppPath: "/Applications/QSpace Pro.app"},
	}

	// defaultOpenInApps 按当前平台返回默认打开方式列表。
	defaultOpenInApps = defaultOpenInAppsForGOOS(runtime.GOOS)
)

// defaultOpenInAppsForGOOS 返回指定平台的默认打开方式列表。
func defaultOpenInAppsForGOOS(goos string) []OpenInAppConfig {
	if goos == "windows" {
		return windowsDefaultOpenInApps()
	}
	return macOSDefaultOpenInApps
}

// windowsDefaultOpenInApps 返回 Windows 上的默认打开方式列表，路径取自环境变量；
// 未安装的应用由打开方式菜单按路径过滤。
func windowsDefaultOpenInApps() []OpenInAppConfig {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	apps := make([]OpenInAppConfig, 0, 5)
	add := func(name string, candidates ...string) {
		path := ""
		for _, p := range candidates {
			if p == "" {
				continue
			}
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				path = p
				break
			}
			if path == "" {
				path = p
			}
		}
		if path == "" {
			return
		}
		apps = append(apps, OpenInAppConfig{Name: name, AppPath: path})
	}
	add("Visual Studio Code",
		windowsOpenInAppPath(localAppData, "Programs", "Microsoft VS Code", "Code.exe"),
		windowsOpenInAppPath(programFiles, "Microsoft VS Code", "Code.exe"))
	add("Cursor", windowsOpenInAppPath(localAppData, "Programs", "cursor", "Cursor.exe"))
	add("Zed", windowsOpenInAppPath(localAppData, "Programs", "Zed", "Zed.exe"))
	add("Windsurf", windowsOpenInAppPath(localAppData, "Programs", "Windsurf", "Windsurf.exe"))
	add("Sublime Text", windowsOpenInAppPath(programFiles, "Sublime Text", "sublime_text.exe"))
	return apps
}

// windowsOpenInAppPath 拼接 Windows 上的应用可执行文件路径；base 为空时返回空串。
func windowsOpenInAppPath(base string, rel ...string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, rel...)...)
}

// DefaultOpenInApps 返回「打开方式」的默认应用列表（全部启用）。
// 字段为空时使用该列表，打开方式菜单再按本机安装与否过滤。
func DefaultOpenInApps() []OpenInAppConfig {
	apps := make([]OpenInAppConfig, len(defaultOpenInApps))
	for i, app := range defaultOpenInApps {
		app.Enabled = true
		apps[i] = app
	}
	return apps
}

// OpenInAppsOrDefault 返回配置中的打开方式列表；未配置时返回默认列表。
func (w WorkbenchConfig) OpenInAppsOrDefault() []OpenInAppConfig {
	if len(w.OpenInApps) == 0 {
		return DefaultOpenInApps()
	}
	apps := make([]OpenInAppConfig, len(w.OpenInApps))
	copy(apps, w.OpenInApps)
	return apps
}

// normalizeOpenInApps 清理打开方式列表：去掉名称与应用路径都为空的空项，并去掉名称和路径两端空白。
func normalizeOpenInApps(apps []OpenInAppConfig) []OpenInAppConfig {
	if len(apps) == 0 {
		return nil
	}
	out := make([]OpenInAppConfig, 0, len(apps))
	for _, app := range apps {
		app.Name = strings.TrimSpace(app.Name)
		app.BundleID = strings.TrimSpace(app.BundleID)
		app.AppPath = strings.TrimSpace(app.AppPath)
		if app.Name == "" && app.AppPath == "" {
			continue
		}
		if app.Name == "" {
			app.Name = strings.TrimSuffix(filepath.Base(app.AppPath), ".app")
		}
		out = append(out, app)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DefaultCloneIgnoredPatterns 默认的克隆依赖目录名单：新建 worktree 时把这些被 Git 忽略的条目写时复制进去。
const DefaultCloneIgnoredPatterns = "node_modules, .env, .env.*, .next, .turbo, target, Pods, .gradle"

// SourceControlConfig 版本控制设置。
type SourceControlConfig struct {
	AutoCleanupMergedWorktrees   *bool                `json:"auto_cleanup_merged_worktrees,omitempty"`    // 合并后自动清理 worktree，nil 视为开启
	AutoCleanupWorktreesOnDelete *bool                `json:"auto_cleanup_worktrees_on_delete,omitempty"` // 删除会话时清理 worktree，nil 视为开启
	InitSubmodules               *bool                `json:"init_submodules,omitempty"`                  // 新 worktree 初始化子模块，nil 视为开启
	CloneIgnoredOnWorktree       *bool                `json:"clone_ignored_on_worktree,omitempty"`        // 新 worktree 写时复制被忽略的依赖目录，nil 视为开启，仅 macOS APFS 生效
	CloneIgnoredPatterns         *string              `json:"clone_ignored_patterns,omitempty"`           // 逗号分隔的条目名模式，nil 用默认名单
	DefaultWorkspaceMode         string               `json:"default_workspace_mode"`                     // 默认新会话工作区模式：local | worktree，默认 local
	GenerateCommitMessage        *bool                `json:"generate_commit_message,omitempty"`          // 提交信息由 AI 生成，nil 视为开启
	CommitInstructions           string               `json:"commit_instructions"`                        // 提交信息附加说明
	CommitMessageModel           ModelSelectionConfig `json:"commit_message_model,omitempty"`             // 提交信息与会话标题生成模型；Provider 为空时沿用 utility 角色模型
}

// AutoCleanupMergedWorktreesEnabled 返回合并后是否自动清理 worktree，nil 视为开启。
func (s SourceControlConfig) AutoCleanupMergedWorktreesEnabled() bool {
	if s.AutoCleanupMergedWorktrees == nil {
		return true
	}
	return *s.AutoCleanupMergedWorktrees
}

// AutoCleanupWorktreesOnDeleteEnabled 返回删除会话时是否清理 worktree，nil 视为开启。
func (s SourceControlConfig) AutoCleanupWorktreesOnDeleteEnabled() bool {
	if s.AutoCleanupWorktreesOnDelete == nil {
		return true
	}
	return *s.AutoCleanupWorktreesOnDelete
}

// InitSubmodulesEnabled 返回新 worktree 是否初始化子模块，nil 视为开启。
func (s SourceControlConfig) InitSubmodulesEnabled() bool {
	if s.InitSubmodules == nil {
		return true
	}
	return *s.InitSubmodules
}

// CloneIgnoredOnWorktreeEnabled 返回新 worktree 是否写时复制被忽略的依赖目录，nil 视为开启。
func (s SourceControlConfig) CloneIgnoredOnWorktreeEnabled() bool {
	if s.CloneIgnoredOnWorktree == nil {
		return true
	}
	return *s.CloneIgnoredOnWorktree
}

// CloneIgnoredPatternsText 返回克隆名单的原始文本，nil 时用默认名单。
func (s SourceControlConfig) CloneIgnoredPatternsText() string {
	if s.CloneIgnoredPatterns == nil {
		return DefaultCloneIgnoredPatterns
	}
	return *s.CloneIgnoredPatterns
}

// CloneIgnoredPatternList 把克隆名单解析成模式列表：按英文逗号切分、去空白、丢空串并去重（保持原顺序）。
func (s SourceControlConfig) CloneIgnoredPatternList() []string {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(s.CloneIgnoredPatternsText(), ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// GenerateCommitMessageEnabled 返回提交信息是否由 AI 生成，nil 视为开启。
func (s SourceControlConfig) GenerateCommitMessageEnabled() bool {
	if s.GenerateCommitMessage == nil {
		return true
	}
	return *s.GenerateCommitMessage
}

// MemoryConfig Nowledge Mem 记忆集成设置。
const (
	MemoryBackendAuto  = "auto"
	MemoryBackendNmem  = "nmem"
	MemoryBackendLocal = "local"
)

type MemoryConfig struct {
	Backend                    string            `json:"backend,omitempty"`
	InjectWorkingMemory        *bool             `json:"inject_working_memory,omitempty"`    // 新会话首轮注入当前项目 space 的 Working Memory，nil 视为开启
	InjectMemoryToolsNote      *bool             `json:"inject_memory_tools_note,omitempty"` // 向新会话说明 Nowledge 记忆工具用法，nil 视为开启
	WorkingMemoryMaxCharacters int               `json:"working_memory_max_characters"`      // Working Memory 注入字符上限，默认 8000，范围 1000..32000
	SyncThreads                *bool             `json:"sync_threads,omitempty"`             // 将 mcode 会话同步到 Nowledge Threads，nil 视为开启
	SyncHelperThreads          *bool             `json:"sync_helper_threads,omitempty"`      // 同步编排 bee 子会话，nil 视为关闭
	SyncAutomationThreads      *bool             `json:"sync_automation_threads,omitempty"`  // 同步自动化运行会话，nil 视为关闭
	BeeRecall                  *bool             `json:"bee_recall,omitempty"`               // 派发 bee 时附加相关记忆，nil 视为开启
	SpaceOverrides             map[string]string `json:"space_overrides,omitempty"`          // 项目路径到 space 名的映射，为空时按仓库目录同名匹配
}

func (m MemoryConfig) BackendMode() string {
	switch m.Backend {
	case MemoryBackendNmem, MemoryBackendLocal:
		return m.Backend
	default:
		return MemoryBackendAuto
	}
}

// WorkingMemoryEnabled 返回新会话是否注入 Working Memory，nil 视为开启。
func (m MemoryConfig) WorkingMemoryEnabled() bool {
	if m.InjectWorkingMemory == nil {
		return true
	}
	return *m.InjectWorkingMemory
}

// MemoryToolsNoteEnabled 返回新会话是否说明记忆工具用法，nil 视为开启。
func (m MemoryConfig) MemoryToolsNoteEnabled() bool {
	if m.InjectMemoryToolsNote == nil {
		return true
	}
	return *m.InjectMemoryToolsNote
}

// ThreadSyncEnabled 返回是否把 mcode 会话同步到 Nowledge Threads，nil 视为开启。
func (m MemoryConfig) ThreadSyncEnabled() bool {
	if m.SyncThreads == nil {
		return true
	}
	return *m.SyncThreads
}

// HelperThreadSyncEnabled 返回是否把编排 bee 子会话同步到 Nowledge Threads，nil 视为关闭。
func (m MemoryConfig) HelperThreadSyncEnabled() bool {
	if m.SyncHelperThreads == nil {
		return false
	}
	return *m.SyncHelperThreads
}

// AutomationThreadSyncEnabled 返回是否把自动化运行会话同步到 Nowledge Threads，nil 视为关闭。
func (m MemoryConfig) AutomationThreadSyncEnabled() bool {
	if m.SyncAutomationThreads == nil {
		return false
	}
	return *m.SyncAutomationThreads
}

// BeeRecallEnabled 返回派发 bee 时是否附加相关记忆，nil 视为开启。
func (m MemoryConfig) BeeRecallEnabled() bool {
	if m.BeeRecall == nil {
		return true
	}
	return *m.BeeRecall
}

// WorkingMemoryBudget 返回 Working Memory 注入字符上限：为 0 时用默认值，越界时夹到范围内。
func (m MemoryConfig) WorkingMemoryBudget() int {
	if m.WorkingMemoryMaxCharacters <= 0 {
		return DefaultWorkingMemoryMaxCharacters
	}
	if m.WorkingMemoryMaxCharacters < MinWorkingMemoryMaxCharacters {
		return MinWorkingMemoryMaxCharacters
	}
	if m.WorkingMemoryMaxCharacters > MaxWorkingMemoryMaxCharacters {
		return MaxWorkingMemoryMaxCharacters
	}
	return m.WorkingMemoryMaxCharacters
}

// SpaceFor 返回项目路径对应的人工覆盖 space 名；没有覆盖时返回空串，由调用方按目录同名匹配。
func (m MemoryConfig) SpaceFor(projectPath string) string {
	if len(m.SpaceOverrides) == 0 {
		return ""
	}
	return strings.TrimSpace(m.SpaceOverrides[projectPath])
}

// StewardConfig 总管（常驻驻留会话）配置。
// 总管在会话卡住、出错、额度用尽、看板任务失败、自动化失败等事件发生时被唤醒，
// 先看一遍再向用户汇报；它只用这里指定的模型，每次唤醒都会消耗额度。
type StewardConfig struct {
	Enabled        bool   `json:"enabled"`                   // 是否启用总管，默认 true
	EnabledChoice  bool   `json:"enabled_choice,omitempty"`  // 用户在设置页亲手切换过启用开关后置 true，此后迁移不再改 enabled
	Name           string `json:"name"`                      // 总管的昵称，可留空；留空时界面只显示「总管」
	Avatar         string `json:"avatar"`                    // 头像：一个 emoji 或一个字，空表示用昵称首字，无昵称用「总」
	Provider       string `json:"provider"`                  // 总管使用的提供方，空表示跟随 Models.Provider
	Model          string `json:"model"`                     // 总管使用的模型，空表示该提供方的默认模型
	Effort         string `json:"effort,omitempty"`          // 推理强度，空表示模型默认
	PlanProvider   string `json:"plan_provider,omitempty"`   // 出方案用的提供方，空表示沿用 deep 档的模型
	PlanModel      string `json:"plan_model,omitempty"`      // 出方案用的模型，空表示沿用 deep 档的模型
	PlanEffort     string `json:"plan_effort,omitempty"`     // 出方案用的推理强度，空表示沿用 deep 档的模型
	DailyWakeLimit int    `json:"daily_wake_limit"`          // 每天最多唤醒次数，0 或负数视为默认 30
	HomeDir        string `json:"home_dir,omitempty"`        // 总管主目录，空表示数据目录下的 steward 子目录
	QuietStart     string `json:"quiet_start"`               // 安静时段开始，HH:MM 本地时间，空表示关闭
	QuietEnd       string `json:"quiet_end"`                 // 安静时段结束，HH:MM 本地时间，空表示关闭；可早于开始表示跨午夜
	DailyReport    bool   `json:"daily_report,omitempty"`    // 是否每天发一份今日要点，默认关
	DailyReportAt  string `json:"daily_report_at,omitempty"` // 日报时间，HH:MM 本地时间，空表示 18:00
	// EventPolicies 按事件类型覆盖唤醒策略，取值 wake（立刻唤醒）、patrol（不立刻唤醒，留给兜底巡检）；
	// 为空或其他取值一律回退默认。
	EventPolicies map[string]string `json:"event_policies,omitempty"`

	// 主会话换新的两个阈值：空闲够久或上下文够大时，下一轮丢掉旧会话另开一条。
	RotateIdleMinutes   int   `json:"rotate_idle_minutes,omitempty"`   // 主会话空闲多少分钟后换新会话，0 或负数表示关闭空闲换新
	RotateContextTokens int64 `json:"rotate_context_tokens,omitempty"` // 上下文达到多少 token 时换新会话，0 或负数表示默认 120000

	// NoProgressMinutes 是无进展看门狗阈值（分钟），0 或负数视为默认 45。
	NoProgressMinutes int `json:"no_progress_minutes,omitempty"`
	// NoProgressOff 关闭无进展看门狗，零值 false 表示开启。
	NoProgressOff bool `json:"no_progress_off,omitempty"`

	// PassthroughOff 关闭「属于话题的事件直通给话题会话」，零值 false 表示默认直通。
	PassthroughOff bool `json:"passthrough_off,omitempty"`

	// AutoCollapseReasoningOff 关闭总管会话的「自动折叠思考过程」，零值 false 表示默认折叠。
	// 它只管总管会话，与其他会话的 Session.AutoCollapseReasoning 互不影响。
	AutoCollapseReasoningOff bool `json:"auto_collapse_reasoning_off,omitempty"`
}

// Nickname 返回用户给总管起的昵称：去掉首尾空白后可能为空。
func (s StewardConfig) Nickname() string {
	return strings.TrimSpace(s.Name)
}

// DisplayName 返回总管当前使用的名字：昵称为空时用默认名字「总管」。
func (s StewardConfig) DisplayName() string {
	if name := s.Nickname(); name != "" {
		return name
	}
	return DefaultStewardName
}

// Label 返回界面上的完整称呼：没有昵称只显示「总管」，有昵称显示「昵称 · 总管」。
func (s StewardConfig) Label() string {
	if name := s.Nickname(); name != "" {
		return name + " · " + DefaultStewardName
	}
	return DefaultStewardName
}

// AvatarChar 返回界面头像用的字符：配置了头像就用它，否则取 DisplayName() 的首个 rune（没有昵称时是「总」）。
func (s StewardConfig) AvatarChar() string {
	if avatar := strings.TrimSpace(s.Avatar); avatar != "" {
		return avatar
	}
	for _, r := range s.DisplayName() {
		return string(r)
	}
	return ""
}

// ParseClock 解析 HH:MM 形式的钟点（去首尾空白后按 15:04 严格解析，00:00 到 23:59），
// 返回当天零点起的分钟数；格式非法时 ok 为 false。
func ParseClock(s string) (minutes int, ok bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// QuietWindow 判断 now 是否处于安静时段：QuietStart 与 QuietEnd 都合法且不相等时才生效，
// 区间为 [开始, 结束)，允许跨午夜。处于时段内时返回 true 与本次时段在 now 所在时区的结束时刻
// （跨午夜的夜里一段结束于次日）；不生效或不在时段内返回 false 与零值。
func (s StewardConfig) QuietWindow(now time.Time) (inQuiet bool, end time.Time) {
	start, ok1 := ParseClock(s.QuietStart)
	stop, ok2 := ParseClock(s.QuietEnd)
	if !ok1 || !ok2 || start == stop {
		return false, time.Time{}
	}
	cur := now.Hour()*60 + now.Minute()
	y, m, d := now.Date()
	endOn := func(dayOffset int) time.Time {
		return time.Date(y, m, d+dayOffset, stop/60, stop%60, 0, 0, now.Location())
	}
	if start < stop {
		if cur >= start && cur < stop {
			return true, endOn(0)
		}
		return false, time.Time{}
	}
	// 跨午夜：夜里一段 [start, 24:00) 结束于次日，凌晨一段 [00:00, stop) 结束于当天。
	if cur >= start {
		return true, endOn(1)
	}
	if cur < stop {
		return true, endOn(0)
	}
	return false, time.Time{}
}

// DailyReportClock 返回日报的钟点：DailyReportAt 为空或非法时回落到 18:00。
func (s StewardConfig) DailyReportClock() (hour, minute int) {
	if minutes, ok := ParseClock(s.DailyReportAt); ok {
		return minutes / 60, minutes % 60
	}
	return 18, 0
}

// WakeLimit 返回每天最多唤醒次数；0 或负数返回默认 30。
func (s StewardConfig) WakeLimit() int {
	if s.DailyWakeLimit <= 0 {
		return DefaultStewardDailyWakeLimit
	}
	return s.DailyWakeLimit
}

// Passthrough 报告是否把属于话题的事件直通给话题会话；默认开，只有显式关掉才为 false。
func (s StewardConfig) Passthrough() bool { return !s.PassthroughOff }

// AutoCollapseReasoningEnabled 报告总管会话是否自动折叠思考过程；默认开，只有显式关掉才为 false。
// 它只看总管自己的开关，与 Session.AutoCollapseReasoning 无关。
func (s StewardConfig) AutoCollapseReasoningEnabled() bool {
	return !s.AutoCollapseReasoningOff
}

// 总管按事件类型唤醒的策略取值。
const (
	StewardEventWake   = "wake"   // 立刻唤醒总管
	StewardEventPatrol = "patrol" // 不立刻唤醒，留给兜底巡检
)

// defaultStewardEventPolicies 是未配置 event_policies 时的默认唤醒策略：
// 自动化正常完成不需要立刻处理，留给兜底巡检；其余类型全部立刻唤醒。
var defaultStewardEventPolicies = map[string]string{
	"automation_finished": StewardEventPatrol,
	"thread_no_progress":  StewardEventWake,
}

// EventPolicy 返回某类事件的唤醒策略：EventPolicies 里的合法配置（去首尾空白并转小写后）优先，
// 配置为空或取值非法时查默认表，默认表未列出的类型一律 wake。
func (s StewardConfig) EventPolicy(kind string) string {
	policy := strings.ToLower(strings.TrimSpace(s.EventPolicies[kind]))
	if policy == StewardEventWake || policy == StewardEventPatrol {
		return policy
	}
	if policy, ok := defaultStewardEventPolicies[kind]; ok {
		return policy
	}
	return StewardEventWake
}

// NoProgressAfter 返回无进展看门狗阈值；NoProgressOff 为真时返回 0 表示关闭。
func (s StewardConfig) NoProgressAfter() time.Duration {
	if s.NoProgressOff {
		return 0
	}
	if s.NoProgressMinutes <= 0 {
		return DefaultNoProgressMinutes * time.Minute
	}
	return time.Duration(s.NoProgressMinutes) * time.Minute
}

// ResolvedHomeDir 返回总管的主目录：HomeDir 为空时落在 <dataDir>/steward。
func (s StewardConfig) ResolvedHomeDir(dataDir string) string {
	if dir := strings.TrimSpace(s.HomeDir); dir != "" {
		return dir
	}
	return filepath.Join(dataDir, "steward")
}

// ProvidersConfig 提供方配置集合。
type ProvidersConfig struct {
	Claude      ProviderConfig                  `json:"claude"`
	Codex       ProviderConfig                  `json:"codex"`
	Agy         ProviderConfig                  `json:"agy"`
	Copilot     ProviderConfig                  `json:"copilot"`
	Pi          ProviderConfig                  `json:"pi"`
	Omp         ProviderConfig                  `json:"omp"`
	CommandCode ProviderConfig                  `json:"commandcode"`
	ACP         map[string]ProviderConfig       `json:"acp,omitempty"`
	OpenAI      OpenAIConfig                    `json:"openai"`
	Presets     map[string]PresetProviderConfig `json:"presets,omitempty"`
	// MagpieAgents 按 agent id 存 magpie 的设置：模式与每个端点勾选的模型；
	// 旧的 CustomEndpointConfig.PiModels 由 Normalize 双向镜像进 agents["pi"]。
	MagpieAgents             map[string]MagpieAgentConfig `json:"magpieAgents,omitempty"`
	HideMagpieModelsInPicker bool                         `json:"hideMagpieModelsInPicker"`
	// MagpieRoutingVersion 是 Magpie 路由一次性迁移的版本号。
	MagpieRoutingVersion int                    `json:"magpieRoutingVersion,omitempty"`
	Custom               []CustomEndpointConfig `json:"custom,omitempty"`
}

// PresetProviderConfig 单个 OpenAI 兼容预设（DeepSeek、Z.ai、Meta、Magpie）的设置；
// key 为 internal/providers/openai 的预设 ID。零值表示未启用。
type PresetProviderConfig struct {
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	CodingPlan bool   `json:"codingPlan,omitempty"`
}

// CustomEndpointConfig 自定义 OpenAI 兼容端点配置。
// Magpie 为 true 表示该端点复用内置 Magpie 网关的语义（默认本地地址、允许空模型）。
type CustomEndpointConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
	Enabled bool   `json:"enabled"`
	Magpie  bool   `json:"magpie,omitempty"`
	// PiModels 是勾选后注入 pi 的模型 ID（magpie 的模型 id），为空表示不注入 pi。
	PiModels []string `json:"piModels,omitempty"`
	// QuotaProviders 控制菜单栏额度面板展示的 Magpie 供应商列表：
	// nil 表示旧配置未设置，按只选 codex 处理；
	// 空切片表示一个都不展示；
	// 否则只展示列出的供应商 id（小写，等于 magpie.AccountProvider 的结果）。
	// 只对 Magpie 端点生效。
	QuotaProviders []string `json:"quotaProviders"`
	// PersonaWebURL 是 magpie web 管理地址（含端口，如 https://host:3430），
	// 配置后 mcode 经它的插件接口读 persona-rules 的人设；仅 Magpie 端点使用。
	PersonaWebURL string `json:"personaWebURL,omitempty"`
	// PersonasJSON 是用户手动粘贴的人设列表 JSON；同样只用于 Magpie 端点。
	PersonasJSON string `json:"personasJSON,omitempty"`
	// PersonaRules 表示用户声明已在 magpie 部署 persona-rules 插件，
	// 只有它为 true 时手动粘贴的人设列表才生效。
	PersonaRules bool `json:"personaRules,omitempty"`
}

// DefaultMagpieBaseURL 是内置 Magpie 网关的本地默认地址。
const DefaultMagpieBaseURL = "http://127.0.0.1:3425/v1"

// CustomEndpointKind 返回自定义端点在 harness 中注册的 Provider Kind。
func CustomEndpointKind(id string) string {
	return "custom-" + id
}

// CustomEndpointProviderKind 返回自定义端点实际注册的 Provider Kind。
// 旧的 Magpie 槽位沿用原 ID，保证已有会话与模型 pin 继续可用。
func CustomEndpointProviderKind(ep CustomEndpointConfig) string {
	if ep.Magpie {
		switch ep.ID {
		case "magpie", "magpie2", "magpie3", "magpie4":
			return ep.ID
		}
	}
	return CustomEndpointKind(ep.ID)
}

// CustomEndpointSecretAccount 返回存储在 SecretStore 中的账号名。
func CustomEndpointSecretAccount(id string) string {
	return "providers.custom." + id + ".apiKey"
}

// CustomEndpointWebKeySecretAccount 返回 magpie web 管理密钥在 SecretStore 中的账号名。
func CustomEndpointWebKeySecretAccount(id string) string {
	return "providers.custom." + id + ".webKey"
}

// NormalizeCustomEndpoints 对自定义端点列表做规范化：
// 对各字段 TrimSpace、丢弃 ID 为空的条目、按 ID 去重保留第一个。
func NormalizeCustomEndpoints(custom []CustomEndpointConfig) []CustomEndpointConfig {
	if len(custom) == 0 {
		return nil
	}
	res := make([]CustomEndpointConfig, 0, len(custom))
	seen := make(map[string]struct{}, len(custom))
	for _, ep := range custom {
		id := strings.TrimSpace(ep.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ep.ID = id
		ep.Name = strings.TrimSpace(ep.Name)
		ep.BaseURL = strings.TrimSpace(ep.BaseURL)
		if ep.Magpie && ep.BaseURL == "" {
			ep.BaseURL = DefaultMagpieBaseURL
		}
		ep.Model = strings.TrimSpace(ep.Model)
		if ep.Magpie {
			ep.PiModels = normalizePiModels(ep.PiModels)
			ep.QuotaProviders = normalizeQuotaProviders(ep.QuotaProviders)
			// web 管理地址去掉首尾空白与末尾斜杠，人设 JSON 只去首尾空白。
			ep.PersonaWebURL = strings.TrimRight(strings.TrimSpace(ep.PersonaWebURL), "/")
			ep.PersonasJSON = strings.TrimSpace(ep.PersonasJSON)
		} else {
			ep.PiModels = nil
			ep.QuotaProviders = nil
			ep.PersonaWebURL = ""
			ep.PersonasJSON = ""
			ep.PersonaRules = false
		}
		res = append(res, ep)
	}
	return res
}

// normalizePiModels 对注入 pi 的模型 ID 列表做 TrimSpace、去空、去重（保持原顺序）。
func normalizePiModels(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	res := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		res = append(res, id)
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

// normalizeQuotaProviders 对 Magpie 端点额度展示的供应商 ID 列表做 TrimSpace、转小写、去重（保持原顺序）。
// nil 表示未设置，保留为 nil；空切片表示都不展示，保留为空切片。
func normalizeQuotaProviders(providers []string) []string {
	if providers == nil {
		return nil
	}
	if len(providers) == 0 {
		return []string{}
	}
	res := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		res = append(res, p)
	}
	return res
}

// Normalize 对配置执行全局规范化（包括权限模式与自定义端点列表）。
func Normalize(cfg Config) Config {
	switch cfg.Permissions.Mode {
	case "agent", ModeAutoAcceptEdits:
		cfg.Permissions.Mode = ModeAuto
	case "full_access":
		cfg.Permissions.Mode = ModeFullAccess
	}

	switch cfg.Session.ThreadFinishAction {
	case FinishActionDormant, FinishActionArchive:
	default:
		cfg.Session.ThreadFinishAction = FinishActionDormant
	}

	switch cfg.Session.ComposerSendKey {
	case ComposerSendKeyEnter, ComposerSendKeyShiftEnter:
	default:
		cfg.Session.ComposerSendKey = ComposerSendKeyEnter
	}

	switch cfg.Startup.QuitAction {
	case QuitActionQuit, QuitActionMinimizeToTray:
	default:
		cfg.Startup.QuitAction = QuitActionQuit
	}
	cfg.Workbench.TerminalShell = strings.TrimSpace(cfg.Workbench.TerminalShell)

	cfg.Appearance.CodeFont = strings.TrimSpace(cfg.Appearance.CodeFont)
	if cfg.Appearance.CodeFontSize <= 0 {
		cfg.Appearance.CodeFontSize = DefaultCodeFontSize
	} else if cfg.Appearance.CodeFontSize < MinCodeFontSize {
		cfg.Appearance.CodeFontSize = MinCodeFontSize
	} else if cfg.Appearance.CodeFontSize > MaxCodeFontSize {
		cfg.Appearance.CodeFontSize = MaxCodeFontSize
	}
	switch cfg.Appearance.ConversationDensity {
	case DensityCompact, DensityComfortable:
	default:
		cfg.Appearance.ConversationDensity = DensityComfortable
	}
	switch cfg.Appearance.ContentWidth {
	case ContentWidthStandard, ContentWidthWide, ContentWidthFull:
	default:
		cfg.Appearance.ContentWidth = ContentWidthStandard
	}
	cfg.Appearance.ContentWidthPercent = cfg.Appearance.EffectiveContentWidthPercent()
	cfg.Appearance.TextScale = cfg.Appearance.EffectiveTextScale()

	if cfg.Orchestration.MaxBees < MinSwarmMaxBees {
		cfg.Orchestration.MaxBees = MinSwarmMaxBees
	} else if cfg.Orchestration.MaxBees > MaxSwarmMaxBees {
		cfg.Orchestration.MaxBees = MaxSwarmMaxBees
	}
	if cfg.Orchestration.AutoForwardQuestionSeconds < 0 {
		cfg.Orchestration.AutoForwardQuestionSeconds = 0
	} else if cfg.Orchestration.AutoForwardQuestionSeconds > MaxSwarmAutoForwardQuestionSeconds {
		cfg.Orchestration.AutoForwardQuestionSeconds = MaxSwarmAutoForwardQuestionSeconds
	}
	if cfg.Orchestration.MaxRounds < MinSwarmMaxRounds {
		cfg.Orchestration.MaxRounds = MinSwarmMaxRounds
	} else if cfg.Orchestration.MaxRounds > MaxSwarmMaxRounds {
		cfg.Orchestration.MaxRounds = MaxSwarmMaxRounds
	}
	if cfg.Orchestration.BeeTimeoutMinutes <= 0 {
		cfg.Orchestration.BeeTimeoutMinutes = DefaultSwarmBeeTimeoutMinutes
	} else if cfg.Orchestration.BeeTimeoutMinutes < MinSwarmBeeTimeoutMinutes {
		cfg.Orchestration.BeeTimeoutMinutes = MinSwarmBeeTimeoutMinutes
	} else if cfg.Orchestration.BeeTimeoutMinutes > MaxSwarmBeeTimeoutMinutes {
		cfg.Orchestration.BeeTimeoutMinutes = MaxSwarmBeeTimeoutMinutes
	}
	if cfg.Orchestration.RequestTimeoutMinutes <= 0 {
		cfg.Orchestration.RequestTimeoutMinutes = DefaultSwarmRequestTimeoutMinutes
	} else if cfg.Orchestration.RequestTimeoutMinutes < MinSwarmRequestTimeoutMinutes {
		cfg.Orchestration.RequestTimeoutMinutes = MinSwarmRequestTimeoutMinutes
	} else if cfg.Orchestration.RequestTimeoutMinutes > MaxSwarmRequestTimeoutMinutes {
		cfg.Orchestration.RequestTimeoutMinutes = MaxSwarmRequestTimeoutMinutes
	}
	switch cfg.Orchestration.WorkspaceMode {
	case WorkspaceModeWorktree, WorkspaceModeShared:
	default:
		cfg.Orchestration.WorkspaceMode = WorkspaceModeWorktree
	}
	// 审查模式：空串与未知值一律回落 off；审查模型三件套只去空白，空串表示自动选择最便宜的可用审查模型。
	cfg.Orchestration.ReviewMode = cfg.Orchestration.ReviewModeValue()
	cfg.Orchestration.ReviewProvider = strings.TrimSpace(cfg.Orchestration.ReviewProvider)
	cfg.Orchestration.ReviewModel = strings.TrimSpace(cfg.Orchestration.ReviewModel)
	cfg.Orchestration.ReviewEffort = strings.TrimSpace(cfg.Orchestration.ReviewEffort)
	// 检查命令去空白、按字节截断到上限；行数上限夹到 0..500，0 表示关闭行数规则。
	cfg.Orchestration.ReviewCheckCommand = truncateUTF8(strings.TrimSpace(cfg.Orchestration.ReviewCheckCommand), MaxReviewCheckCommandLen)
	if cfg.Orchestration.ReviewMaxChangedLines < MinReviewMaxChangedLines {
		cfg.Orchestration.ReviewMaxChangedLines = MinReviewMaxChangedLines
	} else if cfg.Orchestration.ReviewMaxChangedLines > MaxReviewMaxChangedLines {
		cfg.Orchestration.ReviewMaxChangedLines = MaxReviewMaxChangedLines
	}
	cfg.Orchestration.DefaultProvider = strings.TrimSpace(cfg.Orchestration.DefaultProvider)
	cfg.Orchestration.DefaultModel = strings.TrimSpace(cfg.Orchestration.DefaultModel)
	cfg.Orchestration.DefaultEffort = strings.TrimSpace(cfg.Orchestration.DefaultEffort)

	// 总管：名字去空白、头像只留前两个 rune；provider/model/effort 空串即继承，
	// 权限档归一，唤醒上限夹到范围内。
	cfg.Orchestration.Steward.Name = strings.TrimSpace(cfg.Orchestration.Steward.Name)
	// 头像允许「一个 emoji 或一个字」：组合 emoji 由两个 rune 组成，所以保留前 2 个 rune。
	cfg.Orchestration.Steward.Avatar = strings.TrimSpace(cfg.Orchestration.Steward.Avatar)
	if avatar := []rune(cfg.Orchestration.Steward.Avatar); len(avatar) > 2 {
		cfg.Orchestration.Steward.Avatar = string(avatar[:2])
	}
	cfg.Orchestration.Steward.Provider = strings.TrimSpace(cfg.Orchestration.Steward.Provider)
	cfg.Orchestration.Steward.Model = strings.TrimSpace(cfg.Orchestration.Steward.Model)
	cfg.Orchestration.Steward.Effort = strings.TrimSpace(cfg.Orchestration.Steward.Effort)
	cfg.Orchestration.Steward.PlanProvider = strings.TrimSpace(cfg.Orchestration.Steward.PlanProvider)
	cfg.Orchestration.Steward.PlanModel = strings.TrimSpace(cfg.Orchestration.Steward.PlanModel)
	cfg.Orchestration.Steward.PlanEffort = strings.TrimSpace(cfg.Orchestration.Steward.PlanEffort)
	cfg.Orchestration.Steward.HomeDir = strings.TrimSpace(cfg.Orchestration.Steward.HomeDir)
	if cfg.Orchestration.Steward.DailyWakeLimit <= 0 {
		cfg.Orchestration.Steward.DailyWakeLimit = DefaultStewardDailyWakeLimit
	} else if cfg.Orchestration.Steward.DailyWakeLimit > MaxStewardDailyWakeLimit {
		cfg.Orchestration.Steward.DailyWakeLimit = MaxStewardDailyWakeLimit
	}
	// 安静时段：任一非法或首尾相等即视为关闭并清空，合法时规范成 HH:MM。
	qs, okStart := ParseClock(cfg.Orchestration.Steward.QuietStart)
	qe, okEnd := ParseClock(cfg.Orchestration.Steward.QuietEnd)
	if !okStart || !okEnd || qs == qe {
		cfg.Orchestration.Steward.QuietStart, cfg.Orchestration.Steward.QuietEnd = "", ""
	} else {
		cfg.Orchestration.Steward.QuietStart = fmt.Sprintf("%02d:%02d", qs/60, qs%60)
		cfg.Orchestration.Steward.QuietEnd = fmt.Sprintf("%02d:%02d", qe/60, qe%60)
	}
	cfg.Orchestration.Steward.DailyReportAt = strings.TrimSpace(cfg.Orchestration.Steward.DailyReportAt)

	if cfg.Workbench.StuckNudgeTimeout <= 0 {
		cfg.Workbench.StuckNudgeTimeout = DefaultStuckNudgeTimeout
	} else if cfg.Workbench.StuckNudgeTimeout < MinStuckNudgeTimeout {
		cfg.Workbench.StuckNudgeTimeout = MinStuckNudgeTimeout
	} else if cfg.Workbench.StuckNudgeTimeout > MaxStuckNudgeTimeout {
		cfg.Workbench.StuckNudgeTimeout = MaxStuckNudgeTimeout
	}

	if cfg.Workbench.StuckInterruptTimeout <= 0 {
		cfg.Workbench.StuckInterruptTimeout = DefaultStuckInterruptTimeout
	} else if cfg.Workbench.StuckInterruptTimeout > MaxStuckInterruptTimeout {
		cfg.Workbench.StuckInterruptTimeout = MaxStuckInterruptTimeout
	}
	if cfg.Workbench.StuckInterruptTimeout <= cfg.Workbench.StuckNudgeTimeout {
		cfg.Workbench.StuckInterruptTimeout = cfg.Workbench.StuckNudgeTimeout + 60
		if cfg.Workbench.StuckInterruptTimeout > MaxStuckInterruptTimeout {
			cfg.Workbench.StuckInterruptTimeout = MaxStuckInterruptTimeout
		}
	}

	if cfg.Workbench.TerminalScrollbackLines <= 0 {
		cfg.Workbench.TerminalScrollbackLines = DefaultTerminalScrollback
	} else if cfg.Workbench.TerminalScrollbackLines < MinTerminalScrollback {
		cfg.Workbench.TerminalScrollbackLines = MinTerminalScrollback
	} else if cfg.Workbench.TerminalScrollbackLines > MaxTerminalScrollback {
		cfg.Workbench.TerminalScrollbackLines = MaxTerminalScrollback
	}

	if cfg.Workbench.TerminalFontSize <= 0 {
		cfg.Workbench.TerminalFontSize = DefaultTerminalFontSize
	} else if cfg.Workbench.TerminalFontSize < MinTerminalFontSize {
		cfg.Workbench.TerminalFontSize = MinTerminalFontSize
	} else if cfg.Workbench.TerminalFontSize > MaxTerminalFontSize {
		cfg.Workbench.TerminalFontSize = MaxTerminalFontSize
	}

	cfg.Workbench.BrowserQuickPorts = strings.TrimSpace(cfg.Workbench.BrowserQuickPorts)
	if cfg.Workbench.BrowserQuickPorts == "" {
		cfg.Workbench.BrowserQuickPorts = "3000, 5173, 8080, 4321"
	}

	if cfg.Workbench.ContextInjectMaxCharacters <= 0 {
		cfg.Workbench.ContextInjectMaxCharacters = DefaultContextInjectMaxCharacters
	} else if cfg.Workbench.ContextInjectMaxCharacters < MinContextInjectMaxCharacters {
		cfg.Workbench.ContextInjectMaxCharacters = MinContextInjectMaxCharacters
	} else if cfg.Workbench.ContextInjectMaxCharacters > MaxContextInjectMaxCharacters {
		cfg.Workbench.ContextInjectMaxCharacters = MaxContextInjectMaxCharacters
	}

	switch cfg.Workbench.KanbanDefaultView {
	case KanbanViewKanban, KanbanViewList:
	default:
		cfg.Workbench.KanbanDefaultView = KanbanViewKanban
	}

	cfg.Workbench.BrowserStartURL = strings.TrimSpace(cfg.Workbench.BrowserStartURL)
	if cfg.Workbench.BrowserStartURL == "" {
		cfg.Workbench.BrowserStartURL = DefaultBrowserStartURL
	}
	// 打开方式：不预填默认列表，保持“未配置即用默认”的语义；只清理空项与两端空白。
	cfg.Workbench.OpenInApps = normalizeOpenInApps(cfg.Workbench.OpenInApps)

	if cfg.Workbench.EditorFontSize <= 0 {
		cfg.Workbench.EditorFontSize = DefaultEditorFontSize
	} else if cfg.Workbench.EditorFontSize < MinEditorFontSize {
		cfg.Workbench.EditorFontSize = MinEditorFontSize
	} else if cfg.Workbench.EditorFontSize > MaxEditorFontSize {
		cfg.Workbench.EditorFontSize = MaxEditorFontSize
	}

	switch cfg.SourceControl.DefaultWorkspaceMode {
	case WorkspaceModeLocal, WorkspaceModeWorktree:
	default:
		cfg.SourceControl.DefaultWorkspaceMode = WorkspaceModeLocal
	}
	cfg.SourceControl.CommitInstructions = strings.TrimSpace(cfg.SourceControl.CommitInstructions)
	cfg.SourceControl.CommitMessageModel.Provider = strings.TrimSpace(cfg.SourceControl.CommitMessageModel.Provider)
	cfg.SourceControl.CommitMessageModel.Model = strings.TrimSpace(cfg.SourceControl.CommitMessageModel.Model)
	cfg.SourceControl.CommitMessageModel.Effort = strings.TrimSpace(cfg.SourceControl.CommitMessageModel.Effort)

	if cfg.Memory.WorkingMemoryMaxCharacters <= 0 {
		cfg.Memory.WorkingMemoryMaxCharacters = DefaultWorkingMemoryMaxCharacters
	} else if cfg.Memory.WorkingMemoryMaxCharacters < MinWorkingMemoryMaxCharacters {
		cfg.Memory.WorkingMemoryMaxCharacters = MinWorkingMemoryMaxCharacters
	} else if cfg.Memory.WorkingMemoryMaxCharacters > MaxWorkingMemoryMaxCharacters {
		cfg.Memory.WorkingMemoryMaxCharacters = MaxWorkingMemoryMaxCharacters
	}

	cfg.Providers.Custom = NormalizeCustomEndpoints(cfg.Providers.Custom)
	normalizeMagpieAgents(&cfg)

	if cfg.Models.PinLimit < MinModelListLimit || cfg.Models.PinLimit > MaxModelListLimit {
		cfg.Models.PinLimit = DefaultModelListLimit
	}
	cfg.Models.ReorderModelList(cfg.Models.Pins)

	if len(cfg.Orchestration.Pairs) > 0 {
		filtered := make([]SwarmPair, 0, len(cfg.Orchestration.Pairs))
		for _, pair := range cfg.Orchestration.Pairs {
			if strings.TrimSpace(pair.ID) == "" || (strings.TrimSpace(pair.LeadProvider) == "" && strings.TrimSpace(pair.WorkerProvider) == "") {
				continue
			}
			filtered = append(filtered, pair)
		}
		if len(filtered) == 0 {
			cfg.Orchestration.Pairs = nil
		} else {
			cfg.Orchestration.Pairs = filtered
		}
	}

	if cfg.Orchestration.Tiers == nil {
		cfg.Orchestration.Tiers = DefaultSwarmTiers()
	}
	tierIDs := make(map[string]struct{}, len(cfg.Orchestration.Tiers))
	for i := range cfg.Orchestration.Tiers {
		cfg.Orchestration.Tiers[i].ID = strings.ToLower(strings.TrimSpace(cfg.Orchestration.Tiers[i].ID))
		cfg.Orchestration.Tiers[i].Name = strings.TrimSpace(cfg.Orchestration.Tiers[i].Name)
		if cfg.Orchestration.Tiers[i].Name == "" {
			cfg.Orchestration.Tiers[i].Name = cfg.Orchestration.Tiers[i].ID
		}
		if cfg.Orchestration.Tiers[i].ID != "" {
			tierIDs[cfg.Orchestration.Tiers[i].ID] = struct{}{}
		}
	}

	cfg.Orchestration.DefaultTier = strings.ToLower(strings.TrimSpace(cfg.Orchestration.DefaultTier))
	if _, ok := tierIDs[cfg.Orchestration.DefaultTier]; !ok || cfg.Orchestration.DefaultTier == "" {
		if _, ok := tierIDs[DefaultSwarmTierID]; ok {
			cfg.Orchestration.DefaultTier = DefaultSwarmTierID
		} else {
			firstNonReadOnly := ""
			for _, t := range cfg.Orchestration.Tiers {
				if !t.ReadOnly {
					firstNonReadOnly = t.ID
					break
				}
			}
			cfg.Orchestration.DefaultTier = firstNonReadOnly
		}
	}

	for i := range cfg.Orchestration.Pairs {
		if cfg.Orchestration.Pairs[i].BeeTiers != nil {
			cleaned := make(map[string]SwarmTierOverride, len(cfg.Orchestration.Pairs[i].BeeTiers))
			for k, v := range cfg.Orchestration.Pairs[i].BeeTiers {
				lk := strings.ToLower(strings.TrimSpace(k))
				if _, ok := tierIDs[lk]; ok {
					cleaned[lk] = v
				}
			}
			cfg.Orchestration.Pairs[i].BeeTiers = cleaned
		}
	}

	if cfg.Models.Roles.Utility.Provider == "" {
		cfg.Models.Roles.Utility.Provider = cfg.Models.Provider
	}

	cfg.QuickReplies = NormalizeQuickReplies(cfg.QuickReplies)
	return cfg
}

// OpenAIConfig 单个 OpenAI Provider 的设置。
type OpenAIConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
}

// ProviderConfig 单个 Provider 的设置。
type ProviderConfig struct {
	Enabled bool   `json:"enabled"`
	Bin     string `json:"bin"` // 可执行文件路径，空表示从 PATH 查找
	// UpdateMonitor 是否检查该 Provider 的版本更新，nil 视为开启。
	UpdateMonitor *bool `json:"updateMonitor,omitempty"`
	// Auth 仅 ACP droid 使用，取值 "apiKey" 或 "browser"，空视为 "browser"。
	Auth string `json:"auth,omitempty"`
	// ResidentIdleMinutes 仅 claude 使用：常驻进程空闲多少分钟后回收。nil 表示默认 10，0 表示关闭常驻
	// （每轮一个进程，用 --resume 续会话）。
	ResidentIdleMinutes *int `json:"residentIdleMinutes,omitempty"`
	// ResidentMax 仅 claude 使用：同时驻留的进程数上限，0 表示默认 4。
	ResidentMax int `json:"residentMax,omitempty"`
	// PiCacheLong 仅 pi 使用：开启后给 pi 子进程设 PI_CACHE_RETENTION=long，
	// 让支持的服务商（如 Anthropic）使用更长的 prompt 缓存，停顿较久的对话可以少付重新写缓存的费用，
	// 代价是缓存写入单价更高。默认关闭。
	PiCacheLong bool `json:"piCacheLong,omitempty"`
	// PiVisionModel 仅 pi 使用：格式为 provider/model。填了以后 pi 会提供 vision 工具，
	// 当前模型看不了图片时用这个模型描述图片；留空则不提供。
	PiVisionModel string `json:"piVisionModel,omitempty"`
}

// DefaultResidentIdleMinutes 是 claude 常驻进程空闲回收的默认分钟数。
const DefaultResidentIdleMinutes = 10

// ResidentIdleTTL 返回 claude 常驻进程的空闲回收时长；返回 0 表示关闭常驻。
func (p ProviderConfig) ResidentIdleTTL() time.Duration {
	minutes := DefaultResidentIdleMinutes
	if p.ResidentIdleMinutes != nil {
		minutes = *p.ResidentIdleMinutes
	}
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

// UpdateMonitorEnabled 返回是否检查该 Provider 的版本更新，nil 视为开启。
func (p ProviderConfig) UpdateMonitorEnabled() bool {
	if p.UpdateMonitor == nil {
		return true
	}
	return *p.UpdateMonitor
}

// RemoteConfig 远程控制服务配置。
type RemoteConfig struct {
	Enabled       bool     `json:"enabled"`     // 桌面端是否启动远程控制服务，默认 false
	Listen        string   `json:"listen"`      // host:port，默认 0.0.0.0:43117
	ExtraHosts    []string `json:"extra_hosts"` // 额外允许的精确 Host 域名列表
	TLSListen     string   `json:"tls_listen"`  // 空表示不启用 HTTPS，启用时默认建议 0.0.0.0:8443
	TLSDomain     string   `json:"tls_domain"`
	ACMEEmail     string   `json:"acme_email"`
	ACMEDirectory string   `json:"acme_directory"`
	// PushEnabled 为手机 Web Push 离线推送开关，nil 视为开启。仅在远程控制运行且登记了订阅时生效。
	PushOn *bool `json:"push_enabled,omitempty"`
	// TailscaleOn 为内嵌 Tailscale 入口开关，nil 视为关闭。仅在远程控制运行且本机已登录 tailnet 时生效。
	TailscaleOn *bool `json:"tailscale_enabled,omitempty"`
	// TailscaleHostname 是内嵌节点在 tailnet 中的主机名，空或不合法（见 ValidTailscaleHostname）时回落为 mcode。
	TailscaleHostname string `json:"tailscale_hostname"`
}

// PushEnabled 返回离线推送是否开启，nil 视为开启。
func (r RemoteConfig) PushEnabled() bool {
	if r.PushOn == nil {
		return true
	}
	return *r.PushOn
}

// TailscaleEnabled 返回内嵌 Tailscale 入口是否开启，nil 视为关闭。
func (r RemoteConfig) TailscaleEnabled() bool {
	if r.TailscaleOn == nil {
		return false
	}
	return *r.TailscaleOn
}

// ValidTailscaleHostname 判断 name 是否是合法的 Tailscale 节点名：按 DNS label 规则，
// 长度 1 到 63，只含 ASCII 字母、数字和连字符，且不以连字符开头或结尾。
func ValidTailscaleHostname(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-':
			// 连字符只能出现在中间，开头或结尾都不合法。
			if i == 0 || i == len(name)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// TailscaleHost 返回内嵌 Tailscale 节点的主机名：为空或不合法（见 ValidTailscaleHostname）时
// 返回 mcode，配置文件里残留的非法值不会写进节点。
func (r RemoteConfig) TailscaleHost() string {
	host := strings.TrimSpace(r.TailscaleHostname)
	if !ValidTailscaleHostname(host) {
		return "mcode"
	}
	return host
}

// ModelRoleConfig 单个模型角色的提供方与模型配置。两者为空表示跟随默认。
type ModelRoleConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelRolesConfig 各模型角色的配置集合。
// 已弃用：运行时不再读取，仅为兼容旧配置文件保留。
type ModelRolesConfig struct {
	Lead     ModelRoleConfig `json:"lead"`
	Coder    ModelRoleConfig `json:"coder"`
	Research ModelRoleConfig `json:"research"`
	Vision   ModelRoleConfig `json:"vision"`
	Utility  ModelRoleConfig `json:"utility"`
}

// ModelsConfig 模型段全局参数与角色配置。
type ModelsConfig struct {
	Roles            ModelRolesConfig           `json:"roles"`
	Provider         string                     `json:"provider"`                    // claude | codex | memory | agy：最近一次使用的 Agent，新会话默认沿用它
	ProviderSelected bool                       `json:"provider_selected,omitempty"` // 是否已记录过用户用过的 Agent；false 表示首次启动，默认取第一个可用的 Agent
	Pins             []ModelPin                 `json:"pins,omitempty"`              // 固定的模型列表
	PinLimit         int                        `json:"pin_limit"`                   // 模型选择器上限，默认 30，范围 1..30
	Preferences      map[string]ModelPreference `json:"preferences,omitempty"`       // 各模型偏好设置（以 provider/model_id 为键）
}

// ModelPreferenceFor 返回指定固定模型的偏好设置，缺失时返回零值。
func (m ModelsConfig) ModelPreferenceFor(pin ModelPin) ModelPreference {
	pref := m.Preferences[pin.Key()]
	pref.Effort = copyStringPtr(pref.Effort)
	return pref
}

// SetModelPreference 设置指定固定模型的偏好；若 Provider 或 ModelID 为空则忽略；
// 若 pref 为零值（Effort==nil 且 !FastMode）则删除该键；
// Effort 存储时复制一份字符串指针，避免外部别名。
func (m *ModelsConfig) SetModelPreference(pin ModelPin, pref ModelPreference) {
	if pin.Provider == "" || pin.ModelID == "" {
		return
	}
	key := pin.Key()
	if pref.Effort == nil && !pref.FastMode {
		if m.Preferences != nil {
			delete(m.Preferences, key)
			if len(m.Preferences) == 0 {
				m.Preferences = nil
			}
		}
		return
	}
	if m.Preferences == nil {
		m.Preferences = make(map[string]ModelPreference)
	}
	m.Preferences[key] = ModelPreference{
		Effort:   copyStringPtr(pref.Effort),
		FastMode: pref.FastMode,
	}
}

// EffectiveModelListLimit 返回有效的模型列表上限，不在 [1,30] 范围内时返回默认值 30。
func (m ModelsConfig) EffectiveModelListLimit() int {
	if m.PinLimit < MinModelListLimit || m.PinLimit > MaxModelListLimit {
		return DefaultModelListLimit
	}
	return m.PinLimit
}

// ReorderModelList 按传入顺序重建 Pins，丢弃 Provider/ModelID 为空的项，
// 按 Key() 去重（保留首次出现），截断到 EffectiveModelListLimit()。
func (m *ModelsConfig) ReorderModelList(pins []ModelPin) {
	limit := m.EffectiveModelListLimit()
	res := make([]ModelPin, 0, len(pins))
	seen := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		if pin.Provider == "" || pin.ModelID == "" {
			continue
		}
		key := pin.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		res = append(res, pin)
		if len(res) >= limit {
			break
		}
	}
	if len(res) == 0 {
		m.Pins = nil
	} else {
		m.Pins = res
	}
}

// AddModelPin 向固定模型列表追加一项；若 pin 无效、已存在或列表已达上限则返回 false 不做修改，否则追加并返回 true。
func (m *ModelsConfig) AddModelPin(pin ModelPin) bool {
	if pin.Provider == "" || pin.ModelID == "" {
		return false
	}
	limit := m.EffectiveModelListLimit()
	if len(m.Pins) >= limit {
		return false
	}
	targetKey := pin.Key()
	for _, p := range m.Pins {
		if p.Key() == targetKey {
			return false
		}
	}
	m.Pins = append(m.Pins, pin)
	return true
}

// HasModelPin 判断固定模型列表里是否已有该 provider/model 组合。
func (m ModelsConfig) HasModelPin(provider, model string) bool {
	if provider == "" || model == "" {
		return false
	}
	for _, pin := range m.Pins {
		if pin.Provider == provider && pin.ModelID == model {
			return true
		}
	}
	return false
}

// RemoveModelPin 按 Key() 从固定模型列表中删除指定项，返回是否成功删除。
func (m *ModelsConfig) RemoveModelPin(pin ModelPin) bool {
	targetKey := pin.Key()
	for i, p := range m.Pins {
		if p.Key() == targetKey {
			m.Pins = append(m.Pins[:i], m.Pins[i+1:]...)
			return true
		}
	}
	return false
}

// Role 按角色常量返回对应字段的指针，未知角色返回 nil。
func (m *ModelsConfig) Role(role string) *ModelRoleConfig {
	switch role {
	case RoleLead:
		return &m.Roles.Lead
	case RoleCoder:
		return &m.Roles.Coder
	case RoleResearch:
		return &m.Roles.Research
	case RoleVision:
		return &m.Roles.Vision
	case RoleUtility:
		return &m.Roles.Utility
	default:
		return nil
	}
}

// State 对应 <data>/state.json，由程序写入运行时状态。
type State struct {
	Version   int               `json:"version"`
	LastModel map[string]string `json:"last_model"` // provider -> model
}

// Defaults 返回内置默认值。Load 先用它填充，再解码文件，缺失的字段保持默认。
func Defaults() Config {
	return Config{
		Version: Version,
		Session: SessionConfig{
			ShowReasoning:          boolPtr(true),
			AutoCollapseReasoning:  boolPtr(true),
			PinSentMessage:         boolPtr(false),
			ConciseRepliesEnabled:  boolPtr(false),
			QueueWhileRunning:      true,
			InsertIntoRunningTurn:  true,
			AutoContinueAfterLimit: false,
			AskCompactBeforeResume: boolPtr(true),
			ThreadFinishAction:     FinishActionDormant,
			ComposerSendKey:        ComposerSendKeyEnter,
		},
		Notifications: NotificationsConfig{
			NotifyWhenFinished:       true,
			NotifyOnApproval:         true,
			PlayFinishSound:          true,
			NotifyWhenForeground:     false,
			ChimeWhenAttentionNeeded: boolPtr(true),
			DormantSound:             boolPtr(true),
		},
		Startup: StartupConfig{
			ShowTrayIcon:  true,
			TrayShowQuota: false,
			QuitAction:    QuitActionQuit,
		},
		Shortcuts: ShortcutsConfig{
			GlobalShortcutEnabled: false,
			GlobalShortcut:        "CmdOrCtrl+Shift+Space",
		},
		Permissions: PermissionsConfig{
			Mode: ModeFullAccess,
		},
		Archive: ArchiveConfig{
			ConfirmBeforeDeleting: true,
		},
		Appearance: AppearanceConfig{
			Theme:               ThemeSystem,
			CodeFont:            "",
			CodeFontSize:        DefaultCodeFontSize,
			ConversationDensity: DensityComfortable,
			ContentWidth:        ContentWidthStandard,
			ContentWidthPercent: DefaultContentWidthPercent,
			SidebarCompact:      false,
			TextScale:           DefaultTextScale,
		},
		Providers: ProvidersConfig{
			Claude: ProviderConfig{Enabled: true, Bin: ""},
			Codex:  ProviderConfig{Enabled: true, Bin: ""},
			Agy:    ProviderConfig{Enabled: true, Bin: ""},
			OpenAI: OpenAIConfig{Enabled: true},
			Pi: ProviderConfig{Enabled: true, Bin: ""},
			HideMagpieModelsInPicker: true,
		},
		Remote: RemoteConfig{
			Enabled:       false,
			Listen:        DefaultRemoteListen,
			ExtraHosts:    []string{},
			TLSListen:     "",
			TLSDomain:     "",
			ACMEEmail:     "",
			ACMEDirectory: "",
		},
		Models: ModelsConfig{
			Roles: ModelRolesConfig{
				Utility: ModelRoleConfig{
					Provider: ProviderClaude,
					Model:    "",
				},
			},
			Provider: ProviderClaude,
			PinLimit: DefaultModelListLimit,
		},
		Orchestration: OrchestrationConfig{
			Enabled:               boolPtr(true),
			AutoDelegate:          boolPtr(true),
			MaxBees:               4,
			MaxRounds:             10,
			BeeTimeoutMinutes:     DefaultSwarmBeeTimeoutMinutes,
			RequestTimeoutMinutes: DefaultSwarmRequestTimeoutMinutes,
			WorkspaceMode:         WorkspaceModeWorktree,
			RetryFailed:           true,
			AutoMerge:             true,
			LeadPlansOnly:         true,
			AutoClearFinished:     true,
			AutoHidesIdleBees:     true,
			ShowsBeeDetails:       true,
			AutoPopsBees:          true,
			AutoForwardQuestionSeconds: 180,
			ReviewMode:            ReviewModeOff,
			ReviewCheckCommand:    "",
			ReviewMaxChangedLines: DefaultReviewMaxChangedLines,
			DefaultProvider:       "",
			DefaultModel:          "",
			Tiers:                 DefaultSwarmTiers(),
			DefaultTier:           DefaultSwarmTierID,
			Steward: StewardConfig{
				Enabled:        true,
				Name:           "MoCo",
				Avatar:         "🤖",
				QuietStart:     "00:00",
				QuietEnd:       "06:00",
				Provider:       "",
				Model:          "",
				Effort:         "",
				DailyWakeLimit: DefaultStewardDailyWakeLimit,
				HomeDir:        "",
			},
		},
		Workbench: WorkbenchConfig{
			AutoRescueStuck:            boolPtr(true),
			StuckNudgeTimeout:          DefaultStuckNudgeTimeout,
			StuckInterruptTimeout:      DefaultStuckInterruptTimeout,
			EditorAutoSave:             boolPtr(true),
			TerminalScrollbackLines:    DefaultTerminalScrollback,
			TerminalFontSize:           DefaultTerminalFontSize,
			KanbanAutoReview:           boolPtr(true),
			KanbanAutoStartOnCreate:    false,
			BrowserQuickPorts:          "3000, 5173, 8080, 4321",
			ContextInjectsGuidance:     boolPtr(true),
			ContextInjectMaxCharacters: DefaultContextInjectMaxCharacters,
			KanbanDefaultView:          KanbanViewKanban,
			KanbanRunInWorktree:        boolPtr(true),
			KanbanMCPEnabled:           boolPtr(true),
			KanbanEntryOrdinary:        boolPtr(true),
			BrowserStartURL:            DefaultBrowserStartURL,
			BrowserConsoleCapture:      boolPtr(true),
			EditorFontSize:             DefaultEditorFontSize,
		},
		SourceControl: SourceControlConfig{
			AutoCleanupMergedWorktrees:   boolPtr(true),
			AutoCleanupWorktreesOnDelete: boolPtr(true),
			InitSubmodules:               boolPtr(true),
			CloneIgnoredOnWorktree:       boolPtr(true),
			DefaultWorkspaceMode:         WorkspaceModeLocal,
			GenerateCommitMessage:        boolPtr(true),
			CommitInstructions:           "",
		},
		Memory: MemoryConfig{
			InjectWorkingMemory:        boolPtr(true),
			InjectMemoryToolsNote:      boolPtr(true),
			WorkingMemoryMaxCharacters: DefaultWorkingMemoryMaxCharacters,
			SyncThreads:                boolPtr(true),
		},
	}
}

// tierIDRegex 校验档位 ID：以小写字母或数字开头，可包含小写字母、数字、下划线与中划线，长度 1..32。
var tierIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// builtinProviderIDs 是仓库内置 provider 的 ID：CLI 家族、ACP Agent 与 OpenAI 兼容预设。
// ACP Agent ID 与 internal/providers/acp.Agents() 保持一致，预设 ID 与 internal/providers/openai.PresetByID 一致。
var builtinProviderIDs = []string{
	ProviderClaude, ProviderCodex, ProviderAgy, ProviderMemory,
	ProviderCopilot, ProviderPi, ProviderOmp, ProviderCommandCode,
	"cline", "cursor", "opencode", "grok", "devin", "droid",
	"openai", "deepseek", "zai", "meta",
}

// IsKnownProvider 判断 name 是否是内置 provider，或当前配置里登记的 ACP Agent、预设、自定义端点 ID。
// 模型选择器会把选中的 provider ID 原样写回 general.provider，故校验必须覆盖整个可选集合，
// 否则选中 Copilot、预设或自定义端点后整个设置保存都会被拒。
func IsKnownProvider(cfg Config, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, id := range builtinProviderIDs {
		if name == id {
			return true
		}
	}
	if _, ok := cfg.Providers.ACP[name]; ok {
		return true
	}
	if _, ok := cfg.Providers.Presets[name]; ok {
		return true
	}
	for _, ep := range cfg.Providers.Custom {
		if CustomEndpointProviderKind(ep) == name {
			return true
		}
	}
	return false
}

// Validate 校验配置结构体字段的有效性。若存在非法值，返回包含字段名的明确错误。
func Validate(cfg Config) error {
	if !IsKnownProvider(cfg, cfg.Models.Provider) {
		return fmt.Errorf("invalid models.provider %q: must be a built-in provider, ACP agent, preset, or custom endpoint ID", cfg.Models.Provider)
	}

	// 接受与 internal/harness.ParseMode 相同的取值（含兼容旧值）
	switch cfg.Permissions.Mode {
	case ModeReadOnly, ModeSupervised, ModeAutoAcceptEdits, ModeAuto, ModeFullAccess, "agent", "full_access":
	default:
		return fmt.Errorf("invalid permissions.mode %q: must be 'readonly', 'supervised', 'autoAcceptEdits', 'auto', or 'fullAccess'", cfg.Permissions.Mode)
	}

	switch cfg.Session.ThreadFinishAction {
	case FinishActionDormant, FinishActionArchive:
	default:
		return fmt.Errorf("invalid session.thread_finish_action %q: must be 'dormant' or 'archive'", cfg.Session.ThreadFinishAction)
	}

	switch cfg.Startup.QuitAction {
	case QuitActionQuit, QuitActionMinimizeToTray:
	default:
		return fmt.Errorf("invalid startup.quit_action %q: must be 'quit' or 'minimize_to_tray'", cfg.Startup.QuitAction)
	}

	switch cfg.Appearance.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		return fmt.Errorf("invalid appearance.theme %q: must be 'system', 'light', or 'dark'", cfg.Appearance.Theme)
	}

	if cfg.Appearance.CodeFontSize < MinCodeFontSize || cfg.Appearance.CodeFontSize > MaxCodeFontSize {
		return fmt.Errorf("appearance.code_font_size %d must be between %d and %d", cfg.Appearance.CodeFontSize, MinCodeFontSize, MaxCodeFontSize)
	}

	switch cfg.Appearance.ConversationDensity {
	case DensityCompact, DensityComfortable:
	default:
		return fmt.Errorf("invalid appearance.conversation_density %q: must be 'compact' or 'comfortable'", cfg.Appearance.ConversationDensity)
	}

	// 空值表示旧配置没有这一项，由归一化补成默认档，这里直接放行。
	switch cfg.Appearance.ContentWidth {
	case ContentWidthStandard, ContentWidthWide, ContentWidthFull, "":
	default:
		return fmt.Errorf("invalid appearance.content_width %q: must be 'standard', 'wide' or 'full'", cfg.Appearance.ContentWidth)
	}

	// 0 表示沿用默认值（旧配置没有这一项），其余必须在 60..100 之间。
	if cfg.Appearance.ContentWidthPercent != 0 && (cfg.Appearance.ContentWidthPercent < MinContentWidthPercent || cfg.Appearance.ContentWidthPercent > MaxContentWidthPercent) {
		return fmt.Errorf("appearance.content_width_percent %d must be between %d and %d", cfg.Appearance.ContentWidthPercent, MinContentWidthPercent, MaxContentWidthPercent)
	}

	// 0 表示沿用默认值（旧配置没有这一项），其余必须是 TextScaleLevels 里的档位。
	if cfg.Appearance.TextScale != 0 && cfg.Appearance.EffectiveTextScale() != cfg.Appearance.TextScale {
		return fmt.Errorf("appearance.text_scale %d must be one of %v", cfg.Appearance.TextScale, TextScaleLevels)
	}

	if cfg.Orchestration.MaxBees < MinSwarmMaxBees || cfg.Orchestration.MaxBees > MaxSwarmMaxBees {
		return fmt.Errorf("orchestration.max_bees %d must be between %d and %d", cfg.Orchestration.MaxBees, MinSwarmMaxBees, MaxSwarmMaxBees)
	}

	if cfg.Orchestration.PanelWidth < 0 || cfg.Orchestration.PanelHeight < 0 {
		return fmt.Errorf("orchestration.panel_width/panel_height must not be negative")
	}

	if cfg.Orchestration.BeeTimeoutMinutes != 0 && (cfg.Orchestration.BeeTimeoutMinutes < MinSwarmBeeTimeoutMinutes || cfg.Orchestration.BeeTimeoutMinutes > MaxSwarmBeeTimeoutMinutes) {
		return fmt.Errorf("orchestration.bee_timeout_minutes %d must be between %d and %d", cfg.Orchestration.BeeTimeoutMinutes, MinSwarmBeeTimeoutMinutes, MaxSwarmBeeTimeoutMinutes)
	}

	if cfg.Orchestration.RequestTimeoutMinutes != 0 && (cfg.Orchestration.RequestTimeoutMinutes < MinSwarmRequestTimeoutMinutes || cfg.Orchestration.RequestTimeoutMinutes > MaxSwarmRequestTimeoutMinutes) {
		return fmt.Errorf("orchestration.request_timeout_minutes %d must be between %d and %d", cfg.Orchestration.RequestTimeoutMinutes, MinSwarmRequestTimeoutMinutes, MaxSwarmRequestTimeoutMinutes)
	}

	if cfg.Orchestration.AutoForwardQuestionSeconds < 0 || cfg.Orchestration.AutoForwardQuestionSeconds > MaxSwarmAutoForwardQuestionSeconds {
		return fmt.Errorf("orchestration.auto_forward_question_seconds %d must be between 0 and %d", cfg.Orchestration.AutoForwardQuestionSeconds, MaxSwarmAutoForwardQuestionSeconds)
	}

	switch cfg.Orchestration.WorkspaceMode {
	case WorkspaceModeWorktree, WorkspaceModeShared:
	default:
		return fmt.Errorf("invalid orchestration.workspace_mode %q: must be 'worktree' or 'shared'", cfg.Orchestration.WorkspaceMode)
	}

	seenTierIDs := make(map[string]bool, len(cfg.Orchestration.Tiers))
	hasWritableTier := false
	for _, tier := range cfg.Orchestration.Tiers {
		if tier.ID == "" || !tierIDRegex.MatchString(tier.ID) {
			return fmt.Errorf("invalid orchestration.tier id %q: must match ^[a-z0-9][a-z0-9_-]{0,31}$", tier.ID)
		}
		if seenTierIDs[tier.ID] {
			return fmt.Errorf("duplicate orchestration.tier id %q", tier.ID)
		}
		seenTierIDs[tier.ID] = true
		if !tier.ReadOnly {
			hasWritableTier = true
		}
	}
	if len(cfg.Orchestration.Tiers) > 0 {
		if !hasWritableTier {
			return fmt.Errorf("at least one orchestration tier must not be read-only")
		}
		if !seenTierIDs[cfg.Orchestration.DefaultTier] {
			return fmt.Errorf("invalid orchestration.default_tier %q: tier not found", cfg.Orchestration.DefaultTier)
		}
	}

	if cfg.Workbench.StuckNudgeTimeout < MinStuckNudgeTimeout || cfg.Workbench.StuckNudgeTimeout > MaxStuckNudgeTimeout {
		return fmt.Errorf("workbench.stuck_nudge_timeout %d must be between %d and %d", cfg.Workbench.StuckNudgeTimeout, MinStuckNudgeTimeout, MaxStuckNudgeTimeout)
	}

	if cfg.Workbench.StuckInterruptTimeout <= cfg.Workbench.StuckNudgeTimeout || cfg.Workbench.StuckInterruptTimeout > MaxStuckInterruptTimeout {
		return fmt.Errorf("workbench.stuck_interrupt_timeout %d must be greater than stuck_nudge_timeout %d and <= %d", cfg.Workbench.StuckInterruptTimeout, cfg.Workbench.StuckNudgeTimeout, MaxStuckInterruptTimeout)
	}

	if cfg.Workbench.TerminalScrollbackLines < MinTerminalScrollback || cfg.Workbench.TerminalScrollbackLines > MaxTerminalScrollback {
		return fmt.Errorf("workbench.terminal_scrollback_lines %d must be between %d and %d", cfg.Workbench.TerminalScrollbackLines, MinTerminalScrollback, MaxTerminalScrollback)
	}

	if cfg.Workbench.ContextInjectMaxCharacters < MinContextInjectMaxCharacters || cfg.Workbench.ContextInjectMaxCharacters > MaxContextInjectMaxCharacters {
		return fmt.Errorf("workbench.context_inject_max_characters %d must be between %d and %d", cfg.Workbench.ContextInjectMaxCharacters, MinContextInjectMaxCharacters, MaxContextInjectMaxCharacters)
	}

	switch cfg.Workbench.KanbanDefaultView {
	case KanbanViewKanban, KanbanViewList:
	default:
		return fmt.Errorf("invalid workbench.kanban_default_view %q: must be 'kanban' or 'list'", cfg.Workbench.KanbanDefaultView)
	}

	if cfg.Workbench.EditorFontSize < MinEditorFontSize || cfg.Workbench.EditorFontSize > MaxEditorFontSize {
		return fmt.Errorf("workbench.editor_font_size %d must be between %d and %d", cfg.Workbench.EditorFontSize, MinEditorFontSize, MaxEditorFontSize)
	}

	switch cfg.SourceControl.DefaultWorkspaceMode {
	case WorkspaceModeLocal, WorkspaceModeWorktree:
	default:
		return fmt.Errorf("invalid source_control.default_workspace_mode %q: must be 'local' or 'worktree'", cfg.SourceControl.DefaultWorkspaceMode)
	}

	for _, role := range ModelRoles {
		r := cfg.Models.Role(role)
		if r != nil && r.Model != "" && r.Provider == "" {
			return fmt.Errorf("models.roles.%s.model requires provider", role)
		}
	}

	return nil
}
