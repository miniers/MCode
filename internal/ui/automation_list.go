package mcodeui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type automationListProps struct {
	Visible         []Automation
	SelectedID      string
	Unread          map[string]int
	RunningIDs      map[string]bool
	ScopeID         string
	ProjectNames    map[string]string
	Scroll          *ui.ScrollState
	KeyboardFocusID *string
	EditorOpen      bool
	Now             time.Time
	OnSelect        func(Automation)
	OnEdit          func(Automation)
}

func renderAutomationList(c *ui.Context, p automationListProps) {
	scroll := ui.Scroll(c.Key("automation-scroll-list")).Grow(1).FillWidth()
	if p.Scroll != nil {
		scroll.TrackScroll(p.Scroll)
	}
	scroll.Children(func() {
		ui.Column(c.Key("automation-items-col")).FillWidth().Padding(0, Space3).Children(func() {
			for index, a := range p.Visible {
				fields := automationVisualRowFields(a, p.ProjectNames[a.ProjectID], p.ScopeID, p.RunningIDs[a.ID], p.Now)
				label := "自动化项 " + a.Name
				if p.Unread[a.ID] > 0 {
					label += fmt.Sprintf("，未读 %d", p.Unread[a.ID])
				}
				for _, part := range []string{fields.Schedule, fields.Next, fields.Plan, fields.Result} {
					if part != "" {
						label += "，" + part
					}
				}
				row := ui.Column(c.Key("automation-item-"+a.ID)).Focusable().Label(label).Transition(motionListItem(c)).FillWidth().MinWidth(0).Padding(Space4, Space4).Radius(RadiusSM).Gap(Space1)
				if p.KeyboardFocusID != nil && *p.KeyboardFocusID == a.ID {
					row.Focus()
					*p.KeyboardFocusID = ""
				}
				if row.Focused() && !p.EditorOpen {
					next := index
					if row.Shortcut(0, ui.KeyUp) {
						next--
					} else if row.Shortcut(0, ui.KeyDown) {
						next++
					}
					if next >= 0 && next < len(p.Visible) && next != index {
						if p.KeyboardFocusID != nil {
							*p.KeyboardFocusID = p.Visible[next].ID
						}
						if p.OnSelect != nil {
							p.OnSelect(p.Visible[next])
						}
					}
					if row.Shortcut(0, ui.KeyEnter) && p.OnEdit != nil {
						p.OnEdit(a)
					}
				}
				if a.ID == p.SelectedID {
					row.Background(ThemeSelected(c))
				} else if row.Hovered() {
					row.Background(ThemeHover(c))
				}
				if row.Clicked() && p.OnSelect != nil {
					p.OnSelect(a)
				}
				row.Children(func() {
					ui.Row(c.Key("automation-item-row1-" + a.ID)).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
						icon, color := IconCalendarClock(), ThemeTertiaryText(c)
						switch {
						case fields.Kind == "running":
							icon, color = IconLoader(), ThemeAccent(c)
						case fields.Plan == "已完成":
							icon, color = IconCheck(), ThemeSecondaryText(c)
						case fields.Plan != "":
							icon, color = IconCircleSmall(), ThemeSecondaryText(c)
						case fields.Kind == "failed":
							icon, color = IconCircleX(), ThemeDanger(c)
						case fields.Kind == "succeeded":
							icon, color = IconCircleCheck(), ThemeSuccess(c)
						case fields.Kind == "stopped":
							icon, color = IconCircleStop(), ThemeSecondaryText(c)
						}
						ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color).Shrink(0)
						weight := 400
						if a.ID == p.SelectedID || p.Unread[a.ID] > 0 {
							weight = weightFor(c, 500)
						}
						ui.Text(c, fields.Name).FontSize(TypeBody).FontWeight(weight).TextColor(ThemePrimaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
						if p.Unread[a.ID] > 0 {
							ui.Box(c.Key("automation-unread-dot-"+a.ID)).Size(Space2, Space2).Radius(RadiusPill).Background(ThemeAccent(c)).Label("未读").Shrink(0)
						}
					})
					ui.Text(c, fields.Schedule).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).FillWidth().MinWidth(0).SingleLine().Ellipsis("…")
					parts := []string{}
					for _, part := range []string{fields.Next, fields.Plan, fields.Result} {
						if part != "" {
							parts = append(parts, part)
						}
					}
					if len(parts) > 0 {
						ui.Text(c, strings.Join(parts, " · ")).FontSize(TypeBody).TextColor(ThemeSecondaryText(c)).FillWidth().MinWidth(0).SingleLine().Ellipsis("…")
					}
				})
			}
		})
	})
}
