package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// splitSessionNotice 是分割线的文案，也是后端写入的消息正文。
const splitSessionNotice = "已分割 · 以上历史不再进入上下文"

// splitDividerText 把分割线正文拆成标题与说明：正文为空时退回常量文案；
// 说明取第一个换行之后的剩余内容，并压成单行，避免长文案把这条线撑高。
func splitDividerText(text string) (title, detail string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return splitSessionNotice, ""
	}
	title, rest, found := strings.Cut(text, "\n")
	if !found {
		return strings.TrimSpace(title), ""
	}
	return strings.TrimSpace(title), strings.Join(strings.Fields(rest), " ")
}

// splitDividerRow 是左右两条淡色横线夹一段小字的那一行；单行与带说明两种排版共用。
func splitDividerRow(c *ui.Context, key, id, text string, marginBottom float32) ui.Element {
	return ui.Row(c.Key(key)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(0, 0, marginBottom, 0).
		Children(func() {
			ui.Box(c.Key("split-divider-left-" + id)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
			ui.Text(c, text).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
			ui.Box(c.Key("split-divider-right-" + id)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
		})
}

// renderSplitDivider 渲染时间线里的分割线：左右两条淡色横线夹一段小字。
// 分割线只是界面上的界线，上面的历史照常显示、可以滚动回看。
// 带计数时正文有两行：第一行仍是横线夹小字，下面另起一行居中写「已带走」的条数，
// 同样只占一行（窄窗口下截断，与同族的换新/压缩线一致）。
func renderSplitDivider(c *ui.Context, m Message) ui.Element {
	title, detail := splitDividerText(m.Text)
	if detail == "" {
		return splitDividerRow(c, "split-divider-"+m.ID, m.ID, title, 10)
	}
	return ui.Column(c.Key("split-divider-"+m.ID)).
		FillWidth().
		AlignItems(ui.Center).
		Margin(0, 0, Space3, 0).
		Children(func() {
			splitDividerRow(c, "split-divider-row-"+m.ID, m.ID, title, 0)
			ui.Text(c, detail).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}
