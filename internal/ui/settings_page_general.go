package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// permissionModeChoices 返回默认权限档位的选项，包含只读档位。
func permissionModeChoices() []choiceOption {
	return []choiceOption{
		{Value: config.ModeReadOnly, Label: "只读"},
		{Value: config.ModeSupervised, Label: "监督"},
		{Value: config.ModeAuto, Label: "自动"},
		{Value: config.ModeFullAccess, Label: "完全访问"},
	}
}

// permissionModeKnown 判断权限档位在界面上是否有对应选项。
func permissionModeKnown(mode string) bool {
	if mode == config.ModeAutoAcceptEdits {
		return true
	}
	for _, opt := range permissionModeChoices() {
		if opt.Value == mode {
			return true
		}
	}
	return false
}

// pageGeneral 是「通用」页：系统级配置。会话相关的开关在「会话」页，
// 终端与文件树在工作台页，全局快捷键的完整分组表在「快捷键」面板。
var pageGeneral = settingsPage{
	ID:       "general",
	Title:    "通用",
	Icon:     IconSettings(),
	Keywords: []string{"权限", "通知", "提示音", "启动", "托盘", "快捷键", "唤醒"},
	Sections: []settingsSection{
		{
			ID:    "general.permissions",
			Title: "权限",
			Rows: []setting{{
				ID:    "row-mode",
				Title: "默认权限",
				Aliases: []string{
					"新会话默认权限档位", "权限档位", "权限模式", "重启", "只读", "监督",
					"自动", "完全访问", "审批",
				},
				Lock: "mode",
				// 被启动参数锁定时选中项跟着真正生效的档位走：Open() 会重新从磁盘 Load，
				// 直接用内存里的配置会把锁定态显示成磁盘上的旧档（E-03）。
				Control: choice(
					val(func(c *config.Config) *string { return &c.Permissions.Mode }, config.ModeFullAccess),
					permissionModeChoices(),
				).withDisplay(func(st *SettingsState) string {
					mode := st.launchPermissionMode()
					if mode == "" {
						mode = st.cfg.Permissions.Mode
					}
					if mode == config.ModeAutoAcceptEdits {
						return config.ModeAuto
					}
					return mode
				}),
				Status: func(st *SettingsState, locked bool) string {
					if locked {
						if launch := st.launchPermissionMode(); launch != "" {
							return "由启动参数指定（当前：" + launch + "）"
						}
						return "由启动参数指定"
					}
					// 配置里是界面不认识的档位时没有一个选项能高亮，这里把当前值写出来。
					if mode := st.cfg.Permissions.Mode; !permissionModeKnown(mode) {
						return "当前：" + mode + "（未知档位）"
					}
					return ""
				},
			}},
		},
		{
			ID:    "general.notifications",
			Title: "通知与提示音",
			Rows: []setting{
				{
					ID:      "row-notify",
					Title:   "完成后通知",
					Aliases: []string{"完成", "轮次结束", "系统通知"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true)),
				},
				{
					ID:      "row-notify-foreground",
					Title:   "前台也发送通知",
					Aliases: []string{"前台", "系统通知"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenForeground }, false)),
				},
				{
					ID:      "row-notify-approval",
					Title:   "待审批时通知",
					Aliases: []string{"审批", "请求", "系统通知"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyOnApproval }, true)),
				},
				{
					ID:      "row-finish-sound",
					Title:   "完成提示音",
					Aliases: []string{"提示音", "声音", "结束"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.PlayFinishSound }, true)),
				},
				{
					ID:      "row-chime-attention",
					Title:   "需要介入时提示音",
					Aliases: []string{"提示音", "声音", "提问", "决策", "介入"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Notifications.ChimeWhenAttentionNeeded }, true)),
				},
				{
					ID:      "row-dormant-sound",
					Title:   "会话休眠提示音",
					Aliases: []string{"提示音", "声音", "休眠"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Notifications.DormantSound }, true)),
				},
				{
					ID:      "row-notify-stalled",
					Title:   "会话卡住时提醒",
					Aliases: []string{"卡住", "无进展", "提醒", "系统通知"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Notifications.NotifyWhenStalled }, true)),
				},
				{
					ID:      "row-stalled-minutes",
					Title:   "多久算卡住",
					Aliases: []string{"无进展多久算卡住", "卡住判定", "分钟", "默认", "阈值"},
					// 提醒关掉时这一行跟着禁用；0 表示没设置过，按默认 10 分钟显示。
					Enabled: func(st *SettingsState) bool { return st.cfg.Notifications.StalledAlertEnabled() },
					Control: number(val(func(c *config.Config) *int { return &c.Notifications.StalledAlertMinutes }, config.DefaultStalledAlertMinutes), "分钟", 1, 120),
				},
			},
		},
		{
			ID:    "general.startup",
			Title: "启动与托盘",
			Rows: []setting{
				{
					ID:      "row-tray",
					Title:   "菜单栏图标",
					Aliases: []string{"菜单栏", "托盘", "图标", "重启", "会话数"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Startup.ShowTrayIcon }, true)),
				},
				{
					ID:      "row-tray-quota",
					Title:   "菜单栏显示额度百分比",
					Aliases: []string{"菜单栏", "额度", "百分比", "账号"},
					Enabled: func(st *SettingsState) bool { return st.cfg.Startup.ShowTrayIcon },
					Control: toggle(val(func(c *config.Config) *bool { return &c.Startup.TrayShowQuota }, false)),
				},
				{
					ID:      "row-quit-action",
					Title:   "关闭窗口时",
					Aliases: []string{"关闭窗口", "退出", "托盘", "最小化"},
					Enabled: func(st *SettingsState) bool { return st.cfg.Startup.ShowTrayIcon },
					Control: choice(
						val(func(c *config.Config) *string { return &c.Startup.QuitAction }, config.QuitActionQuit),
						[]choiceOption{
							{Value: config.QuitActionQuit, Label: "退出应用"},
							{Value: config.QuitActionMinimizeToTray, Label: "最小化到托盘"},
						},
					).withDisplay(func(st *SettingsState) string {
						if !st.cfg.Startup.ShowTrayIcon {
							return config.QuitActionQuit
						}
						// 空值按「退出应用」处理，与旧选择器的回落一致。
						if v := st.cfg.Startup.QuitAction; v != "" {
							return v
						}
						return config.QuitActionQuit
					}),
				},
				{
					ID:      "row-automation-prevent-sleep",
					Title:   "保持唤醒",
					Aliases: []string{"定时任务前保持唤醒", "防休眠", "自动化", "休眠"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Automation.PreventSleep }, true)),
				},
			},
		},
		{
			ID:    "general.backup",
			Title: "备份与恢复",
			Rows: []setting{
				{ID: "row-general-backup", Title: "备份", Control: custom(renderBackupControl)},
				{ID: "row-general-restore", Title: "恢复", Control: custom(renderRestoreControl)},
			},
		},
		{
			ID:    "general.shortcuts",
			Title: "快捷键",
			Rows: []setting{
				{
					ID:      "row-global-shortcut",
					Title:   "全局快捷键",
					Aliases: []string{"快捷键组合", "唤起窗口", "全局热键", "重启"},
					// 开关与组合输入框是一个整体，输入框的防抖草稿在 settings_extra.go 里维护。
					Control: custom(renderGlobalShortcutControl),
				},
				{
					ID:      "row-shortcuts-all",
					Title:   "全部快捷键",
					Aliases: []string{"快捷键表", "快捷键分组"},
					Control: link(func(st *SettingsState) string {
						return fmt.Sprintf("%d 个", len(shortcutDefaults()))
					}, "shortcuts"),
				},
			},
		},
	},
}

// 注册到页面树放在 init 里：包级变量不能引用页面的初始值，否则会和渲染链形成初始化循环。
func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageGeneral) }

// renderPageGeneral 渲染通用页；渲染器只认页面树，保留这个入口给既有调用方。
func renderPageGeneral(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageGeneral)
}
