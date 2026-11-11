package mcodeui

import "github.com/egoist/mygo/ui"

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

func renderInspectorHeader(c *ui.Context, s *chatState, st *inspectorSessionState) {
	height := diffHeaderHeight
	header := ui.Row(c.Key("inspector-header")).Shrink(0).FillWidth()
	if inspectorTopInset(c) == 0 {
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
				tab := tabs.Tab(i).Height(height).Padding(0, Space4).Label(pane.title)
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
					ui.Box(c).Absolute().Left(Space4).Right(Space4).Bottom(0).Height(Space0).Background(ThemeAccent(c).Alpha(ink))
				})
			}
		})
		if selectionState.index >= 0 && selectionState.index < len(sidePanes) && sidePanes[selectionState.index].id != st.Active {
			selectionState.active = sidePanes[selectionState.index].id
			inspectorSelect(s, selectionState.active, false)
		}
		IconButton(c, IconButtonProps{Key: "inspector-close", Icon: IconX(), Label: "关闭右栏", Tooltip: shortcutTitleWithKey("toggle-inspector", "关闭"), Size: ControlXS, Variant: ButtonGhost, OnClick: func() { inspectorClose(s) }})
		if inspectorTopInset(c) == 0 {
			ui.Box(c.Key("inspector-header-border")).Absolute().Left(0).Right(0).Bottom(0).Height(Hairline).Background(ThemeBorder(c))
		}
	})
}
