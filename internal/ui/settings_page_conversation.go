package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// advancedConversation 是会话页的高级设置。
var advancedConversation = settingsSection{
	ID:    "advanced.conversation",
	Title: "高级",
	Rows: []setting{
		{
			ID:      "row-ask-compact-before-resume",
			Title:   "恢复旧会话前询问压缩",
			Aliases: []string{"压缩", "上下文", "旧会话", "继续", "恢复", "长时间未活动"},
			Control: toggle(ptr(func(c *config.Config) **bool { return &c.Session.AskCompactBeforeResume }, true)),
		},
		{
			ID:      "row-bee-message-retention",
			Title:   "压缩 30 天前的子代理消息",
			Aliases: []string{"保留", "压缩", "子代理", "bee", "30 天", "工具输出", "思考", "不可恢复", "主会话"},
			Control: toggle(ptr(func(c *config.Config) **bool { return &c.Retention.BeeMessages }, true)),
		},
	},
}

// pageConversation 是「会话」页：回复与思考、消息与排队、完成与提醒、保留与归档。
var pageConversation = settingsPage{
	ID:    "conversation",
	Title: "会话",
	Icon:  IconMessageSquarePlus(),
	Keywords: []string{
		"conversation", "session", "chat", "thread", "reasoning", "thinking",
		"compact", "concise", "concise-replies", "verbose", "comments", "replies",
		"queue", "insert", "finish", "finished", "done", "dormant",
		"confirm", "会话", "对话", "思考", "排队", "压缩", "限额", "完成", "删除",
	},
	Sections: []settingsSection{
		{
			ID:    "conversation.reply",
			Title: "回复与思考",
			Rows: []setting{
				{
					ID:      "row-show-reasoning",
					Title:   "显示思考过程",
					Aliases: []string{"思考", "思考过程", "步骤", "展开", "推理", "thinking"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Session.ShowReasoning }, true)),
				},
				{
					ID:      "row-auto-collapse-reasoning",
					Title:   "自动折叠思考过程",
					Aliases: []string{"折叠", "思考", "收起", "展开", "自动", "thinking"},
					// 字段是 *bool，choice 只绑定字符串；不新增控件能力前保留开关。
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Session.AutoCollapseReasoning }, true)),
				},
				{
					ID:      "row-concise-replies",
					Title:   "简洁 AI 回复",
					Aliases: []string{"简洁", "精简", "回复", "提示词", "注释", "concise", "verbose"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Session.ConciseRepliesEnabled }, true)),
				},
			},
		},
		{
			ID:    "conversation.messages",
			Title: "消息与排队",
			Rows: []setting{
				{
					ID:      "row-quick-replies",
					Title:   "快捷回复",
					Aliases: []string{"快捷回复", "常用回复"},
					Control: link(func(st *SettingsState) string {
						return fmt.Sprintf("%d 个", len(st.cfg.QuickReplies))
					}, "quick-replies"),
				},
				{
					ID:      "row-queue-while-running",
					Title:   "运行中排队消息",
					Aliases: []string{"排队", "队列", "回车", "中断", "轮次", "queue"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Session.QueueWhileRunning }, false)),
				},
				{
					ID:      "row-insert-into-running-turn",
					Title:   "新消息插入当前回合",
					Aliases: []string{"排队", "插入", "回合", "注入", "服务商", "insert", "turn"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Session.InsertIntoRunningTurn }, false)),
				},
				{
					ID:      "row-composer-send-key",
					Title:   "发送消息快捷键",
					Aliases: []string{"回车", "换行", "快捷键", "Shift", "Enter", "发送"},
					Control: choice(
						val(func(c *config.Config) *string { return &c.Session.ComposerSendKey }, config.ComposerSendKeyEnter),
						[]choiceOption{
							{Value: config.ComposerSendKeyEnter, Label: "Enter"},
							{Value: config.ComposerSendKeyShiftEnter, Label: "Shift+Enter"},
						},
					),
				},
				{
					ID:      "row-pin-sent-message",
					Title:   "发送后置顶消息",
					Aliases: []string{"置顶", "滚动", "发送", "时间线", "跟随", "pin"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Session.PinSentMessage }, true)),
				},
				{
					ID:      "row-auto-continue-after-limit",
					Title:   "用尽限额后自动继续",
					Aliases: []string{"额度", "限额", "配额", "续跑", "自动", "limit"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Session.AutoContinueAfterLimit }, false)),
				},
			},
		},
		{
			ID:    "conversation.finish",
			Title: "完成与提醒",
			Rows: []setting{
				{
					ID:      "row-thread-finish-action",
					Title:   "会话完成动作",
					Aliases: []string{"完成", "归档", "休眠", "结束", "轮次", "dormant", "archive"},
					Control: choice(
						val(func(c *config.Config) *string { return &c.Session.ThreadFinishAction }, config.FinishActionDormant),
						[]choiceOption{
							{Value: config.FinishActionDormant, Label: "休眠"},
							{Value: config.FinishActionArchive, Label: "归档"},
						},
					),
				},
			},
		},
		{
			ID:    "conversation.retention",
			Title: "保留与归档",
			Rows: []setting{
				{
					ID:      "row-auto-dormant-days",
					Title:   "无消息多少天后自动休眠",
					Aliases: []string{"自动休眠", "天数", "归档"},
					Control: autoDormantControl{},
				},
				{
					ID:      "row-archive",
					Title:   "已归档会话",
					Aliases: []string{"归档", "恢复", "删除"},
					Control: link(archiveCountLabel, "archive"),
				},
				{
					ID:      "row-confirm-delete",
					Title:   "删除会话前确认",
					Aliases: []string{"删除", "确认", "弹窗", "删除会话"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Archive.ConfirmBeforeDeleting }, true)),
				},
			},
		},
		advancedConversation,
	},
}

// 注册到页面树放在 init 里：包级变量不能引用页面的初始值，否则会和渲染链形成初始化循环。
func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageConversation) }

// renderPageConversation 渲染会话页；渲染器只认页面树，保留这个入口给既有调用方。
func renderPageConversation(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageConversation)
}

// renderConversationAdvancedSections 只为 settings.go 里尚未删除的旧高级区分支保活：
// 行已经并入页面树，这里按「会话」节渲染，实际分发不会再走到。
func renderConversationAdvancedSections(c *ui.Context, st *SettingsState) {
	renderSettingsTreeSection(c, st, advancedConversation, false)
}
