package mcodeui

const HeaderHeight = ControlMD + 2*Space3 + 1

type shellPane int

const (
	paneSidebar shellPane = iota
	paneMain
	paneSettings
)

func headerInsetFor(_ shellPane) float32 {
	return HeaderHeight
}

func headerCollapseWidth(windowWidth, sidebarWidth, sidebarProgress float32) float32 {
	return max(windowWidth-sidebarWidth*sidebarProgress, 0)
}

func headerCollapseLevel(width float32) int {
	if width > 0 && width < 760 {
		return 1
	}
	return 0
}
