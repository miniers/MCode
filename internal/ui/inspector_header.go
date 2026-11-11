package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func toggleInspector(s *chatState) {
	st := inspectorSession(s)
	if st == nil {
		return
	}
	if st.Open {
		inspectorClose(s)
	} else {
		inspectorSelect(s, st.Active, false)
	}
}

func renderInspectorHeader(c *ui.Context, s *chatState, st *inspectorSessionState, topAligned bool) {
	inspectorFollowTick(s, st)
	height := diffHeaderHeight
	header := ui.Row(c.Key("inspector-header")).Shrink(0).FillWidth().Padding(0, Space3, 0, 0).AlignItems(ui.Center)
	if topAligned {
		height = HeaderHeight
		header.DragWindow()
	}
	header.Height(height)
	type selection struct {
		index  int
		active string
	}
	selectionState := ui.Local(header, "inspector-selection", func() selection { return selection{} })
	if selectionState.active != st.Active {
		selectionState.active = st.Active
		for i := range sidePanes {
			if sidePanes[i].id == st.Active {
				selectionState.index = i
				break
			}
		}
	}
	header.Children(func() {
		tabs := ui.TabsBase(c.Key("inspector-tabs"), &selectionState.index, len(sidePanes))
		tabs.List.Grow(1).Children(func() {
			for i, pane := range sidePanes {
				tab := tabs.Tab(i).Height(height).Padding(0, Space4).Gap(Space2).AlignItems(ui.Center).Label(pane.title)
				s.mu.Lock()
				count := 0
				if st.follow != nil {
					count = st.follow.badges[pane.id]
				}
				s.mu.Unlock()
				if count > 0 && tab.Clicked() {
					selectionState.index, selectionState.active = i, pane.id
					inspectorFollowResume(s, st, pane.id)
					count = 0
				}
				target := float32(0)
				if selectionState.index == i {
					target = 1
				}
				if reduceMotion(c) {
					tab.Jump("indicator", target)
				}
				ink := tab.AnimateTo("indicator", target, springSnappy)
				tab.Children(func() {
					ui.Text(c, pane.title).FontSize(TypeBody)
					if count > 0 {
						rollingNumber(c, fmt.Sprintf("inspector-follow-%p-%p-%s", s, st, pane.id), count).
							Padding(0, Space1).Radius(RadiusSM).Background(ThemeAccentSubtle(c)).TextColor(ThemeAccent(c))
					}
					ui.Box(c).Absolute().Left(Space4).Right(Space4).Bottom(0).Height(Space0).Background(ThemeAccent(c).Alpha(ink))
				})
			}
		})
		if selectionState.index >= 0 && selectionState.index < len(sidePanes) && sidePanes[selectionState.index].id != st.Active {
			selectionState.active = sidePanes[selectionState.index].id
			inspectorSelect(s, selectionState.active, false)
		}
		IconButton(c, IconButtonProps{Key: "inspector-close", Icon: IconX(), Label: "关闭右栏", Tooltip: shortcutTitleWithKey("toggle-inspector", "关闭"), Size: ControlXS, Variant: ButtonGhost, OnClick: func() { inspectorClose(s) }})
		if topAligned {
			ui.Box(c.Key("inspector-header-border")).Absolute().Left(0).Right(0).Bottom(0).Height(Hairline).Background(ThemeBorder(c))
		}
	})
}
