package mcodeui

import "github.com/egoist/mygo/ui"

func renderStewardReply(c *ui.Context, s *chatState, m Message) ui.Element {
	return ui.Row(c.Key("steward-reply-" + m.ID)).
		FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).
		Children(func() {
			ui.Text(c, m.Text).FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).
				Selectable().Shrink(1).MinWidth(0)
		})
}
