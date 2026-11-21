package mcodeui

import "github.com/egoist/mygo/ui"

// renderMessageMoreButton 渲染消息操作栏末尾的「⋯」更多按钮。
//
// 方案一：mygo 的菜单按钮 API（ui.Element.Menu）直接接收 func(*ui.Menu)，
// 所以这里把与右键同一个 messageContextMenu 交给它，条目、可用条件与行为天然一致，
// 不存在两套菜单定义走偏的可能；弹层由系统原生菜单呈现，与右键菜单同一套外观。
// 与侧边栏会话行的「更多操作」按钮（internal/ui/sidebar.go:1084）同一写法。
func renderMessageMoreButton(c *ui.Context, s *chatState, m Message) {
	btn := IconButton(c.Key("msg-more-"+m.ID), IconButtonProps{
		Icon:    IconMoreHorizontal(),
		Label:   "更多",
		Tooltip: "更多",
		Variant: ButtonGhost,
		Size:    ControlSM,
	})
	btn.Menu(messageContextMenu(c, s, m))
}
