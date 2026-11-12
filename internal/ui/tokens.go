package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 间距（DIP）。
// Space0 是半格间距，用于徽章和分段轨道的内边距。
const (
	Space0 float32 = 2
	Space1 float32 = 4
	Space2 float32 = 6
	Space3 float32 = 8
	Space4 float32 = 12
	Space5 float32 = 16
	Space6 float32 = 20
	Space7 float32 = 24
	Space8 float32 = 32
)

// 设置界面的间距与尺寸（DIP）。
const (
	TasksSidebarWidth         float32 = 220
	TasksToolbarHeight        float32 = 52
	TasksSearchWidth          float32 = 140
	TasksSearchMinWidth       float32 = 120
	KanbanColumnMinWidth      float32 = 270
	KanbanDoneRailWidth       float32 = 44
	KanbanEmptyDropMinHeight  float32 = 64
	KanbanCardActionSlotWidth float32 = 80
	KanbanFocusRingWidth      float32 = 2
	KanbanDragSourceOpacity   float32 = .4
	KanbanBoardPaneMinWidth   float32 = 400
	KanbanDetailMinWidth      float32 = 360
	KanbanDetailDefaultWidth  float32 = 480
	KanbanDetailMaxWidth      float32 = 640
)

const (
	settingsBadgePadY       float32 = 3
	settingsPillPadX        float32 = 5
	settingsCompactPadX     float32 = 10
	settingsCardPad         float32 = 14
	settingsContentMaxWidth float32 = 720
	settingsSheetWidth      float32 = 520
	settingsSheetWideWidth  float32 = 720
	settingsRowMinHeight    float32 = 36
	settingsRowPadX                 = Space5
	settingsRowPadY                 = Space4
	settingsSectionGap              = Space7
	settingsSectionTitleGap         = Space3
	settingsDividerInset            = settingsRowPadX
)

// 供应商面板尺寸（DIP）：卡片最小宽度 720。
const (
	settingsProviderSheetMinWidth float32 = 720
	settingsSheetMinWidth         float32 = 360
)

// 圆角。
const (
	RadiusXS   float32 = 4
	RadiusSM   float32 = 6
	RadiusMD   float32 = 8
	RadiusLG   float32 = 12
	RadiusXL   float32 = 14
	RadiusPill float32 = 999
)

// 控件、图标、行高。
const (
	ControlXS        float32 = 20
	ControlKbd       float32 = 18 // 键帽高度
	ControlSM        float32 = 24
	ControlMD        float32 = 28
	ControlLG        float32 = 32
	IconSM           float32 = 14
	IconMD           float32 = 16
	IconLG           float32 = 20
	RowHeight        float32 = 28
	RowHeightCompact float32 = 24
	Hairline         float32 = 1 // 发丝线粗细
)

const gitCommitMoreHoverOpacity float32 = 0.85

// 字号：一律经 scaledFont 使用。
const (
	TypeCaption   float32 = 11
	TypeFootnote  float32 = 12
	TypeBody      float32 = 13
	TypeProse     float32 = 14
	TypeHeading   float32 = 13
	TypeTitle     float32 = 15
	TypePageTitle float32 = 17
	TypeCode      float32 = 12
)

// 叠加底色透明度。
const (
	OverlaySubtle float32 = 0.06
	OverlayFaint  float32 = 0.04
)

// 动效。
const (
	DurFast  = 90 * time.Millisecond
	DurSnap  = 140 * time.Millisecond
	DurBase  = 200 * time.Millisecond
	DurSheet = 280 * time.Millisecond
	DurSlow  = 420 * time.Millisecond

	staggerBase  = 50 * time.Millisecond
	staggerStep  = 30 * time.Millisecond
	staggerLimit = 8
)

var (
	EaseOut       = ui.CubicBezier(0.165, 0.84, 0.44, 1)
	EaseSnap      = ui.CubicBezier(0.32, 0.72, 0, 1)
	EaseOvershoot = ui.CubicBezier(0.34, 1.3, 0.64, 1)
)

// 层级仅用于文档和断言，mygo 不支持 z-index。
const (
	ZBase = iota
	ZSticky
	ZPanel
	ZPopover
	ZDialog
	ZToast
	ZDrag
)

// 状态枚举。
type Status int

const (
	StatusIdle Status = iota
	StatusRunning
	StatusAttention
	StatusError
)

type ToolState int

const (
	ToolPending ToolState = iota
	ToolRunning
	ToolDone
	ToolError
)

// Density 定义界面密度。
type Density struct {
	ControlH float32
	RowH     float32
	Gap      float32
	PadX     float32
	PadY     float32
	Icon     float32
}

var (
	DensityComfortable = Density{ControlH: ControlMD, RowH: RowHeight, Gap: Space4, PadX: Space4, PadY: Space3, Icon: IconMD}
	DensityCompact     = Density{ControlH: ControlSM, RowH: RowHeightCompact, Gap: Space3, PadX: Space3, PadY: Space2, Icon: IconSM}
)

func densityOf(cfg config.AppearanceConfig) Density {
	if cfg.ConversationDensity == config.DensityCompact {
		return DensityCompact
	}
	return DensityComfortable
}

func densityFor(s *chatState) Density {
	if st := getChatSettings(s); st != nil {
		return densityOf(st.cfg.Appearance)
	}
	return DensityComfortable
}

func weightFor(c *ui.Context, w int) int {
	return weightForTheme(w, isDark(c))
}

func weightForTheme(w int, dark bool) int {
	if dark {
		switch w {
		case 600:
			return 500
		case 500:
			return 400
		}
	}
	return w
}

func staggerDelay(i int) time.Duration {
	return staggerBase + time.Duration(min(max(i, 0), staggerLimit))*staggerStep
}
