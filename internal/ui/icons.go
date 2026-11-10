package mcodeui

// Lucide Icons
// ISC License
//
// Copyright (c) for portions of Lucide are held by Cole Bemis 2013-2022 as part of Feather (MIT).
// All other portions Copyright (c) Lucide Contributors 2022.
//
// Permission to use, copy, modify, and/or distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
// ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
// ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
// OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

var (
	iconCacheMu sync.RWMutex
	iconCache   = make(map[string]*ui.SVG)
)

// icons_assets 收录 provider 品牌图标：主体来自 Simple Icons（CC0-1.0），部分取自厂商官方公开的
// 品牌资源，无公开矢量源者用 mcode 原创字母 monogram；逐项来源见 docs/THIRD_PARTY_NOTICES.md。
//
//go:embed icons_assets/*.svg
var bundledAssets embed.FS

// bundledIcon 解析 icons_assets 下的完整 SVG；非 24×24 viewBox 按原样保留，与 IconSwarm 一致。
func bundledIcon(name string) *ui.SVG {
	data, err := bundledAssets.ReadFile("icons_assets/" + name)
	if err != nil {
		panic("ui: 缺少内置图标资源 " + name)
	}
	return ui.MustParseSVG(data)
}

// bundledColorAssets 收录同一批品牌图标的彩色版本 SVG 原文。
//
//go:embed icons_assets/color/*.svg
var bundledColorAssets embed.FS

// bundledColorIcon 解析 icons_assets/color 下的原色 SVG；缺失时返回 nil。
func bundledColorIcon(name string) *ui.SVG {
	data, err := bundledColorAssets.ReadFile("icons_assets/color/" + name)
	if err != nil {
		return nil
	}
	return ui.MustParseSVG(data)
}

func parseLucideSVG(content string) *ui.SVG {
	data := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">%s</svg>`, content)
	return ui.MustParseSVG([]byte(data))
}

// 预定义 Lucide 图标的 path/element 内容，均来自 lucide-static 原文（只做颜色/属性兼容处理）
const (
	svgPathSearch          = `<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`
	svgPathBell            = `<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`
	svgPathFolder          = `<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`
	svgPathFolderOpen      = `<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>`
	svgPathPlus            = `<path d="M5 12h14"/><path d="M12 5v14"/>`
	svgPathSquarePen       = `<path d="M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.375 2.625a1 1 0 0 1 3 3l-9.013 9.014a2 2 0 0 1-.853.505l-2.873.84a.5.5 0 0 1-.62-.62l.84-2.873a2 2 0 0 1 .506-.852z"/>`
	svgPathSettings        = `<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`
	svgPathMoreHorizontal  = `<circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/><circle cx="5" cy="12" r="1"/>`
	svgPathCircleEllipsis  = `<circle cx="12" cy="12" r="10"/><path d="M17 12h.01"/><path d="M12 12h.01"/><path d="M7 12h.01"/>`
	svgPathChevronRight    = `<path d="m9 18 6-6-6-6"/>`
	svgPathChevronDown     = `<path d="m6 9 6 6 6-6"/>`
	svgPathX               = `<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`
	svgPathArrowLeft       = `<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>`
	svgPathPaperclip       = `<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/>`
	svgPathArrowUp         = `<path d="M12 19L12 5M5 12L12 5L19 12"/>`
	svgPathSquare          = `<rect width="18" height="18" x="3" y="3" rx="2"/>`
	svgPathCheck           = `<path d="M20 6 9 17l-5-5"/>`
	svgPathAlertTriangle   = `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><line x1="12" x2="12" y1="9" y2="13"/><line x1="12" x2="12.01" y1="17" y2="17"/>`
	svgPathTerminal        = `<path d="M12 19h8"/><path d="m4 17 6-6-6-6"/>`
	svgPathGlobe           = `<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>`
	svgPathFile            = `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/>`
	svgPathBrain           = `<path d="M12 5a3 3 0 1 0-5.997.125 4 4 0 0 0-2.526 5.77 4 4 0 0 0 .556 6.588A4 4 0 1 0 12 18Z"/><path d="M12 5a3 3 0 1 1 5.997.125 4 4 0 0 1 2.526 5.77 4 4 0 0 1-.556 6.588A4 4 0 1 1 12 18Z"/><path d="M15 13a4.5 4.5 0 0 1-3-4 4.5 4.5 0 0 1-3 4"/><path d="M12 18v4"/>`
	svgPathWrench          = `<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>`
	svgPathHammer          = `<path d="m15 12-8.373 8.373a1 1 0 1 1-3-3L12 9"/><path d="m18 15 4-4"/><path d="m21.5 11.5-1.914-1.914A2 2 0 0 1 19 8.172V7l-2.26-2.26a6 6 0 0 0-4.202-1.756L9 2.96l.92.82A6.18 6.18 0 0 1 12 8.4V10l2 2h1.172a2 2 0 0 1 1.414.586L18.5 14.5"/>`
	svgPathGitBranch       = `<path d="M15 6a9 9 0 0 0-9 9V3"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/>`
	svgPathCopy            = `<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`
	svgPathArrowDownCircle = `<circle cx="12" cy="12" r="10"/><path d="M12 8v8"/><path d="m8 12 4 4 4-4"/>`
)

var (
	iconSearch          = parseLucideSVG(svgPathSearch)
	iconBell            = parseLucideSVG(svgPathBell)
	iconFolder          = parseLucideSVG(svgPathFolder)
	iconFolderOpen      = parseLucideSVG(svgPathFolderOpen)
	iconPlus            = parseLucideSVG(svgPathPlus)
	iconSquarePen       = parseLucideSVG(svgPathSquarePen)
	iconSettings        = parseLucideSVG(svgPathSettings)
	iconMoreHorizontal  = parseLucideSVG(svgPathMoreHorizontal)
	iconChevronRight    = parseLucideSVG(svgPathChevronRight)
	iconChevronDown     = parseLucideSVG(svgPathChevronDown)
	iconX               = parseLucideSVG(svgPathX)
	iconArrowLeft       = parseLucideSVG(svgPathArrowLeft)
	iconPaperclip       = parseLucideSVG(svgPathPaperclip)
	iconArrowUp         = parseLucideSVG(svgPathArrowUp)
	iconSquare          = parseLucideSVG(svgPathSquare)
	iconCheck           = parseLucideSVG(svgPathCheck)
	iconAlertTriangle   = parseLucideSVG(svgPathAlertTriangle)
	iconTerminal        = parseLucideSVG(svgPathTerminal)
	iconGlobe           = parseLucideSVG(svgPathGlobe)
	iconFile            = parseLucideSVG(svgPathFile)
	iconBrain           = parseLucideSVG(svgPathBrain)
	iconWrench          = parseLucideSVG(svgPathWrench)
	iconHammer          = parseLucideSVG(svgPathHammer)
	iconGitBranch       = parseLucideSVG(svgPathGitBranch)
	iconCopy            = parseLucideSVG(svgPathCopy)
	iconArrowDownCircle = parseLucideSVG(svgPathArrowDownCircle)
)

// 各图标快捷导出函数
func IconArrowDownCircle() *ui.SVG { return iconArrowDownCircle }
func IconSearch() *ui.SVG          { return iconSearch }
func IconBell() *ui.SVG            { return iconBell }
func IconFolder() *ui.SVG          { return iconFolder }
func IconFolderOpen() *ui.SVG      { return iconFolderOpen }
func IconPlus() *ui.SVG            { return iconPlus }
func IconSquarePen() *ui.SVG       { return iconSquarePen }
func IconSettings() *ui.SVG        { return iconSettings }
func IconMoreHorizontal() *ui.SVG  { return iconMoreHorizontal }
func IconChevronRight() *ui.SVG    { return iconChevronRight }
func IconChevronDown() *ui.SVG     { return iconChevronDown }
func IconX() *ui.SVG               { return iconX }
func IconArrowLeft() *ui.SVG       { return iconArrowLeft }
func IconPaperclip() *ui.SVG       { return iconPaperclip }
func IconArrowUp() *ui.SVG         { return iconArrowUp }
func IconSend() *ui.SVG            { return iconSend }
func IconSquare() *ui.SVG          { return iconSquare }
func IconCheck() *ui.SVG           { return iconCheck }
func IconAlertTriangle() *ui.SVG   { return iconAlertTriangle }
func IconTerminal() *ui.SVG        { return iconTerminal }
func IconGlobe() *ui.SVG           { return iconGlobe }
func IconFile() *ui.SVG            { return iconFile }
func IconBrain() *ui.SVG           { return iconBrain }
func IconWrench() *ui.SVG          { return iconWrench }
func IconHammer() *ui.SVG          { return iconHammer }
func IconGitBranch() *ui.SVG       { return iconGitBranch }
func IconCopy() *ui.SVG            { return iconCopy }

// IconByName 按名称动态查找或获取图标（支持连字符与下划线命名，如 "square-pen" 或 "square_pen"）。
func IconByName(name string) *ui.SVG {
	canonical := strings.ToLower(strings.TrimSpace(name))
	canonical = strings.ReplaceAll(canonical, "_", "-")

	switch canonical {
	case "search":
		return iconSearch
	case "bell":
		return iconBell
	case "folder":
		return iconFolder
	case "folder-open":
		return iconFolderOpen
	case "plus":
		return iconPlus
	case "square-pen", "new-thread", "edit":
		return iconSquarePen
	case "settings", "gear":
		return iconSettings
	case "more-horizontal", "ellipsis", "more":
		return iconMoreHorizontal
	case "chevron-right":
		return iconChevronRight
	case "chevron-down":
		return iconChevronDown
	case "x", "close":
		return iconX
	case "arrow-left", "back":
		return iconArrowLeft
	case "paperclip", "attach":
		return iconPaperclip
	case "arrow-up":
		return iconArrowUp
	case "square", "stop":
		return iconSquare
	case "check":
		return iconCheck
	case "alert-triangle", "warning":
		return iconAlertTriangle
	case "terminal":
		return iconTerminal
	case "globe":
		return iconGlobe
	case "file":
		return iconFile
	case "brain":
		return iconBrain
	case "wrench":
		return iconWrench
	case "hammer":
		return iconHammer
	case "list-checks":
		return iconListChecks
	case "git-branch", "branch":
		return iconGitBranch
	case "copy":
		return iconCopy
	case "asterisk":
		return iconAsterisk
	case "hexagon":
		return iconHexagon
	case "sun":
		return iconSun
	case "moon":
		return iconMoon
	case "monitor":
		return iconMonitor
	case "shield":
		return iconShield
	case "lock":
		return iconLock
	case "lock-open", "lock_open":
		return iconLockOpen
	case "sparkles":
		return iconSparkles
	case "kanban", "tasks":
		return iconKanban
	case "automation", "automations":
		return iconCalendarClock
	case "zap":
		return iconZap
	case "refresh-cw", "refresh":
		return iconRefreshCw
	case "play":
		return iconPlay
	case "pause":
		return iconPause
	case "trash", "trash-2":
		return iconTrash2
	case "swarm", "hydra":
		return iconSwarm
	case "pin":
		return iconPin
	case "star":
		return iconStar
	case "star-filled", "star_filled":
		return iconStarFilled
	case "panel-left", "panel_left", "sidebar":
		return iconPanelLeft
	case "palette":
		return iconPalette
	case "smartphone", "phone":
		return iconSmartphone
	case "cpu":
		return iconCpu
	case "puzzle":
		return iconPuzzle
	case "sliders-horizontal", "sliders_horizontal":
		return iconSlidersHorizontal
	case "command", "cmd":
		return iconCommand
	case "archive":
		return iconArchive
	case "info":
		return iconInfo
	case "circle-ellipsis", "ellipsis-circle":
		return iconCircleEllipsis
	case "circle-check", "check-circle", "checkmark-circle":
		return iconCircleCheck
	case "hand-raised":
		return iconHandRaised
	case "pencil-line":
		return iconPencilLine
	case "wand-sparkles":
		return iconWandSparkles
	case "book-open":
		return iconBookOpen
	case "plus-minus", "diff":
		return iconPlusMinus
	case "external-link":
		return iconExternalLink
	case "chart-bar", "chart-no-axes-column":
		return iconChartBar
	case "stop-filled":
		return iconStopFilled
	case "tray", "inbox":
		return iconTray
	case "map":
		return iconMap
	case "seal-check", "badge-check":
		return iconSealCheck
	case "note", "notebook-pen":
		return iconNote
	case "circle-small":
		return iconCircleSmall
	case "list-todo":
		return iconListTodo
	case "merge":
		return iconMerge
	case "network":
		return iconNetwork
	case "git-pull-request-create":
		return iconGitPullRequestCreate
	case "timer":
		return iconTimer
	case "notebook":
		return iconNotebook
	case "square-arrow-out-up-right":
		return iconSquareArrowOutUpRight
	case "message-square-plus":
		return iconMessageSquarePlus
	case "log-in":
		return iconLogIn
	case "equal":
		return iconEqual
	case "move-down":
		return iconMoveDown
	case "circle-stop":
		return iconCircleStop
	case "circle-x":
		return iconCircleX
	case "power":
		return iconPower
	case "layout-dashboard":
		return iconLayoutDashboard
	case "arrow-up-to-line":
		return iconArrowUpToLine
	case "reply":
		return iconReply
	case "chevrons-up":
		return iconChevronsUp
	case "git-commit-vertical":
		return iconGitCommitVertical
	case "octagon-alert":
		return iconOctagonAlert
	case "flame":
		return iconFlame
	case "file-diff":
		return iconFileDiff
	case "clipboard-list":
		return iconClipboardList
	case "list-ordered":
		return iconListOrdered
	case "git-fork":
		return iconGitFork
	case "circle-dashed":
		return iconCircleDashed
	case "scale":
		return iconScale
	case "scroll-text":
		return iconScrollText
	case "anchor":
		return iconAnchor
	case "magnet":
		return iconMagnet
	case "bot":
		return iconBot
	case "circle-dot":
		return iconCircleDot
	case "columns-3":
		return iconColumns3
	case "calendar-clock":
		return iconCalendarClock
	case "battery-low":
		return iconBatteryLow
	case "loader":
		return iconLoader
	case "clock":
		return iconClock
	case "repeat":
		return iconRepeat
	case "shuffle":
		return iconShuffle
	case "circle-pause":
		return iconCirclePause
	case "boxes":
		return iconBoxes
	case "minimize-2":
		return iconMinimize2
	case "package":
		return iconPackage
	case "fold-vertical":
		return iconFoldVertical
	case "user-round-check":
		return iconUserRoundCheck
	case "highlighter":
		return iconHighlighter
	case "arrow-up-right":
		return iconArrowUpRight
	case "chart-column":
		return iconChartColumn
	case "book-open-text":
		return iconBookOpenText
	case "send":
		return iconSend

	default:
		// "provider-<id>" 转交 ProviderIcon 解析
		if strings.HasPrefix(canonical, "provider-") {
			return ProviderIcon(strings.TrimPrefix(canonical, "provider-"))
		}
		return nil
	}
}

// 追加的 Lucide 图标常量
const (
	svgPathAsterisk          = `<path d="M12 6v12"/><path d="M17.196 9 6.804 15"/><path d="m6.804 9 10.392 6"/>`
	svgPathHexagon           = `<path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>`
	svgPathSun               = `<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>`
	svgPathMoon              = `<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>`
	svgPathMonitor           = `<rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/>`
	svgPathShield            = `<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>`
	svgPathLock              = `<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`
	svgPathLockOpen          = `<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 9.9-1"/>`
	svgPathSparkles          = `<path d="m12 3-1.912 5.813a2 2 0 0 1-1.275 1.275L3 12l5.813 1.912a2 2 0 0 1 1.275 1.275L12 21l1.912-5.813a2 2 0 0 1 1.275-1.275L21 12l-5.813-1.912a2 2 0 0 1-1.275-1.275L12 3Z"/><path d="M5 3v4"/><path d="M19 17v4"/><path d="M3 5h4"/><path d="M17 19h4"/>`
	svgPathKanban            = `<path d="M5 3v14"/><path d="M12 3v8"/><path d="M19 3v18"/>`
	svgPathZap               = `<polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/>`
	svgPathRefreshCw         = `<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`
	svgPathPlay              = `<path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z"/>`
	svgPathPause             = `<rect width="4" height="16" x="6" y="4"/><rect width="4" height="16" x="14" y="4"/>`
	svgPathTrash2            = `<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/>`
	svgPathPin               = `<line x1="12" x2="12" y1="17" y2="22"/><path d="M5 17h14v-1.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V6h1a2 2 0 0 0 0-4H8a2 2 0 0 0 0 4h1v4.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24Z"/>`
	svgPathPanelLeft         = `<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>`
	svgPathPalette           = `<circle cx="13.5" cy="6.5" r=".5" fill="currentColor"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z"/>`
	svgPathSmartphone        = `<rect width="14" height="20" x="5" y="2" rx="2" ry="2"/><path d="M12 18h.01"/>`
	svgPathCpu               = `<rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9" rx="1"/><path d="M15 2v2"/><path d="M15 20v2"/><path d="M2 15h2"/><path d="M2 9h2"/><path d="M20 15h2"/><path d="M20 9h2"/><path d="M9 2v2"/><path d="M9 20v2"/>`
	svgPathPuzzle            = `<path d="M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 15.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0 .474-1.68l-1.683-1.682a2.414 2.414 0 0 1 0-3.414L4.39 8.61a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1-.474-1.68l1.683-1.682a2.414 2.414 0 0 1 3.414 0z"/>`
	svgPathSlidersHorizontal = `<line x1="21" x2="14" y1="4" y2="4"/><line x1="10" x2="3" y1="4" y2="4"/><line x1="21" x2="12" y1="12" y2="12"/><line x1="8" x2="3" y1="12" y2="12"/><line x1="21" x2="16" y1="20" y2="20"/><line x1="12" x2="3" y1="20" y2="20"/><line x1="14" x2="14" y1="2" y2="6"/><line x1="12" x2="12" y1="10" y2="14"/><line x1="16" x2="16" y1="18" y2="22"/>`
	svgPathCommand           = `<path d="M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3"/>`
	svgPathArchive           = `<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>`
	svgPathInfo              = `<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>`
	svgPathCircleCheck       = `<circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/>`
)

// 追加图标常量（Lucide 与品牌标识）
const (
	svgPathEye          = `<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>`
	svgPathHandRaised   = `<path d="M18 11V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2"/><path d="M14 10V4a2 2 0 0 0-2-2a2 2 0 0 0-2 2v2"/><path d="M10 10.5V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15"/>`
	svgPathPencilLine   = `<path d="M13 21h8"/><path d="m15 5 4 4"/><path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/>`
	svgPathWandSparkles = `<path d="m21.64 3.64-1.28-1.28a1.21 1.21 0 0 0-1.72 0L2.36 18.64a1.21 1.21 0 0 0 0 1.72l1.28 1.28a1.2 1.2 0 0 0 1.72 0L21.64 5.36a1.2 1.2 0 0 0 0-1.72"/><path d="m14 7 3 3"/><path d="M5 6v4"/><path d="M19 14v4"/><path d="M10 2v2"/><path d="M7 8H3"/><path d="M21 16h-4"/><path d="M11 3H9"/>`
	// 按 SF Symbol book.pages 重画：右侧书页有厚度与前封面折边，左侧书页无折边。
	svgPathBookOpen     = `<path d="M11 5.5C9.6 4.3 7.7 3.75 5.5 3.75c-.7 0-1.7.1-2.4.3a1 1 0 0 0-.35 1.85V17.2a1 1 0 0 0 1.3.95c.6-.15 1.3-.2 1.95-.2 1.9 0 3.6.5 5 1.55"/><path d="M13 5.5c1.4-1.2 3.3-1.75 5.5-1.75.7 0 1.7.1 2.4.3a1 1 0 0 1 .35 1.85V17.2a1 1 0 0 1-1.3.95 9.6 9.6 0 0 0-1.95-.2c-1.9 0-3.6.5-5 1.55"/><path d="M11 22.25v-7.5"/><path d="M13 22.25v-7.5"/><path d="M17.5 3.9V23"/>`
	svgPathDiff         = `<path d="M12 3v14"/><path d="M5 10h14"/><path d="M5 21h14"/>`
	svgPathExternalLink = `<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`
	svgPathChartBar     = `<line x1="18" x2="18" y1="20" y2="10"/><line x1="12" x2="12" y1="20" y2="4"/><line x1="6" x2="6" y1="20" y2="16"/>`
	svgPathStopFilled   = `<rect width="18" height="18" x="3" y="3" rx="2" fill="currentColor"/>`
	svgPathStar         = `<polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/>`
	svgPathStarFilled   = `<polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" fill="currentColor" stroke="none"/>`
	svgPathTray         = `<polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>`
	svgPathMap          = `<path d="M14.106 5.553a2 2 0 0 0 1.788 0l3.659-1.83A1 1 0 0 1 21 4.619v12.764a1 1 0 0 1-.553.894l-4.553 2.277a2 2 0 0 1-1.788 0l-4.212-2.106a2 2 0 0 0-1.788 0l-3.659 1.83A1 1 0 0 1 3 19.381V6.618a1 1 0 0 1 .553-.894l4.553-2.277a2 2 0 0 1 1.788 0z"/><path d="M15 5.764v15"/><path d="M9 3.236v15"/>`
	svgPathSealCheck    = `<path d="M3.85 8.62a4 4 0 0 1 4.78-4.77 4 4 0 0 1 6.74 0 4 4 0 0 1 4.78 4.78 4 4 0 0 1 0 6.74 4 4 0 0 1-4.77 4.78 4 4 0 0 1-6.75 0 4 4 0 0 1-4.78-4.77 4 4 0 0 1 0-6.76Z"/><path d="m9 12 2 2 4-4"/>`
	svgPathNote         = `<path d="M13.4 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7.4"/><path d="M2 6h4"/><path d="M2 10h4"/><path d="M2 14h4"/><path d="M2 18h4"/><path d="M21.378 5.626a1 1 0 1 0-3.004-3.004l-5.01 5.012a2 2 0 0 0-.506.854l-.837 2.87a.5.5 0 0 0 .62.62l2.87-.837a2 2 0 0 0 .854-.506z"/>`

	svgSwarmMark = `<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>`
)

// 选定的 Lucide 补充图标常量
const (
	svgPathCircleSmall           = `<circle cx="12" cy="12" r="6"/>`
	svgPathListTodo              = `<path d="M13 5h8"/><path d="M13 12h8"/><path d="M13 19h8"/><path d="m3 17 2 2 4-4"/><rect x="3" y="4" width="6" height="6" rx="1"/>`
	svgPathMerge                 = `<path d="m8 6 4-4 4 4"/><path d="M12 2v10.3a4 4 0 0 1-1.172 2.872L4 22"/><path d="m20 22-5-5"/>`
	svgPathNetwork               = `<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>`
	svgPathGitPullRequestCreate  = `<circle cx="6" cy="6" r="3"/><path d="M6 9v12"/><path d="M13 6h3a2 2 0 0 1 2 2v3"/><path d="M18 15v6"/><path d="M21 18h-6"/>`
	svgPathTimer                 = `<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>`
	svgPathNotebook              = `<path d="M2 6h4"/><path d="M2 10h4"/><path d="M2 14h4"/><path d="M2 18h4"/><rect width="16" height="20" x="4" y="2" rx="2"/><path d="M16 2v20"/>`
	svgPathSquareArrowOutUpRight = `<path d="M21 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h6"/><path d="m21 3-9 9"/><path d="M15 3h6v6"/>`
	svgPathMessageSquarePlus     = `<path d="M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z"/><path d="M12 8v6"/><path d="M9 11h6"/>`
	svgPathLogIn                 = `<path d="m10 17 5-5-5-5"/><path d="M15 12H3"/><path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/>`
	svgPathEqual                 = `<line x1="5" x2="19" y1="9" y2="9"/><line x1="5" x2="19" y1="15" y2="15"/>`
	svgPathMoveDown              = `<path d="M8 18L12 22L16 18"/><path d="M12 2V22"/>`
	svgPathCircleStop            = `<circle cx="12" cy="12" r="10"/><rect x="9" y="9" width="6" height="6" rx="1"/>`
	svgPathCircleX               = `<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6"/><path d="m9 9 6 6"/>`
	svgPathPower                 = `<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/>`
	svgPathLayoutDashboard       = `<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>`
	svgPathArrowUpToLine         = `<path d="M5 3h14"/><path d="m18 13-6-6-6 6"/><path d="M12 7v14"/>`
	svgPathReply                 = `<path d="M20 18v-2a4 4 0 0 0-4-4H4"/><path d="m9 17-5-5 5-5"/>`
	svgPathChevronsUp            = `<path d="m17 11-5-5-5 5"/><path d="m17 18-5-5-5 5"/>`
	svgPathGitCommitVertical     = `<path d="M12 3v6"/><circle cx="12" cy="12" r="3"/><path d="M12 15v6"/>`
	svgPathOctagonAlert          = `<path d="M12 16h.01"/><path d="M12 8v4"/><path d="M15.312 2a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586l-4.688-4.688A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2z"/>`
	svgPathFlame                 = `<path d="M12 3q1 4 4 6.5t3 5.5a1 1 0 0 1-14 0 5 5 0 0 1 1-3 1 1 0 0 0 5 0c0-2-1.5-3-1.5-5q0-2 2.5-4"/>`
	svgPathFileDiff              = `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M9 10h6"/><path d="M12 13V7"/><path d="M9 17h6"/>`
	svgPathClipboardList         = `<rect width="8" height="4" x="8" y="2" rx="1" ry="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="M12 11h4"/><path d="M12 16h4"/><path d="M8 11h.01"/><path d="M8 16h.01"/>`
	svgPathListOrdered           = `<path d="M11 5h10"/><path d="M11 12h10"/><path d="M11 19h10"/><path d="M4 4h1v5"/><path d="M4 9h2"/><path d="M6.5 20H3.4c0-1 2.6-1.925 2.6-3.5a1.5 1.5 0 0 0-2.6-1.02"/>`
	svgPathGitFork               = `<circle cx="12" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9v2c0 .6-.4 1-1 1H7c-.6 0-1-.4-1-1V9"/><path d="M12 12v3"/>`
	svgPathCircleDashed          = `<path d="M10.1 2.182a10 10 0 0 1 3.8 0"/><path d="M13.9 21.818a10 10 0 0 1-3.8 0"/><path d="M17.609 3.721a10 10 0 0 1 2.69 2.7"/><path d="M2.182 13.9a10 10 0 0 1 0-3.8"/><path d="M20.279 17.609a10 10 0 0 1-2.7 2.69"/><path d="M21.818 10.1a10 10 0 0 1 0 3.8"/><path d="M3.721 6.391a10 10 0 0 1 2.7-2.69"/><path d="M6.391 20.279a10 10 0 0 1-2.69-2.7"/>`
	svgPathScale                 = `<path d="M12 3v18"/><path d="m19 8 3 8a5 5 0 0 1-6 0zV7"/><path d="M3 7h1a17 17 0 0 0 8-2 17 17 0 0 0 8 2h1"/><path d="m5 8 3 8a5 5 0 0 1-6 0zV7"/><path d="M7 21h10"/>`
	svgPathScrollText            = `<path d="M15 12h-5"/><path d="M15 8h-5"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3"/>`
	svgPathAnchor                = `<path d="M12 6v16"/><path d="m19 13 2-1a9 9 0 0 1-18 0l2 1"/><path d="M9 11h6"/><circle cx="12" cy="4" r="2"/>`
	svgPathMagnet                = `<path d="m12 15 4 4"/><path d="M2.352 10.648a1.205 1.205 0 0 0 0 1.704l2.296 2.296a1.205 1.205 0 0 0 1.704 0l6.029-6.029a1 1 0 1 1 3 3l-6.029 6.029a1.205 1.205 0 0 0 0 1.704l2.296 2.296a1.205 1.205 0 0 0 1.704 0l6.365-6.367A1 1 0 0 0 8.716 4.282z"/><path d="m5 8 4 4"/>`
	svgPathBot                   = `<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/>`
	svgPathCircleDot             = `<circle cx="12" cy="12" r="1"/><circle cx="12" cy="12" r="10"/>`
	svgPathColumns3              = `<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/><path d="M15 3v18"/>`
	svgPathCalendarClock         = `<path d="M16 14v2.2l1.6 1"/><path d="M16 2v3"/><path d="M21 7.338V5a2 2 0 00-2-2H5a2 2 0 00-2 2v14a2 2 0 002 2h2.338"/><path d="M3 9h5.859"/><path d="M8 2v3"/><circle cx="16" cy="16" r="6"/>`
	svgPathBatteryLow            = `<path d="M22 14v-4"/><path d="M6 14v-4"/><rect x="2" y="6" width="16" height="12" rx="2"/>`
	svgPathLoader                = `<path d="M12 2v4"/><path d="m16.2 7.8 2.9-2.9"/><path d="M18 12h4"/><path d="m16.2 16.2 2.9 2.9"/><path d="M12 18v4"/><path d="m4.9 19.1 2.9-2.9"/><path d="M2 12h4"/><path d="m4.9 4.9 2.9 2.9"/>`
	svgPathClock                 = `<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`
	svgPathRepeat                = `<path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/>`
	svgPathShuffle               = `<path d="m18 14 4 4-4 4"/><path d="m18 2 4 4-4 4"/><path d="M2 18h1.973a4 4 0 0 0 3.3-1.7l5.454-8.6a4 4 0 0 1 3.3-1.7H22"/><path d="M2 6h1.972a4 4 0 0 1 3.6 2.2"/><path d="M22 18h-6.041a4 4 0 0 1-3.3-1.8l-.359-.45"/>`
	svgPathCirclePause           = `<circle cx="12" cy="12" r="10"/><line x1="10" x2="10" y1="15" y2="9"/><line x1="14" x2="14" y1="15" y2="9"/>`
	svgPathBoxes                 = `<path d="M2.97 12.92A2 2 0 0 0 2 14.63v3.24a2 2 0 0 0 .97 1.71l3 1.8a2 2 0 0 0 2.06 0L12 19v-5.5l-5-3-4.03 2.42Z"/><path d="m7 16.5-4.74-2.85"/><path d="m7 16.5 5-3"/><path d="M7 16.5v5.17"/><path d="M12 13.5V19l3.97 2.38a2 2 0 0 0 2.06 0l3-1.8a2 2 0 0 0 .97-1.71v-3.24a2 2 0 0 0-.97-1.71L17 10.5l-5 3Z"/><path d="m17 16.5-5-3"/><path d="m17 16.5 4.74-2.85"/><path d="M17 16.5v5.17"/><path d="M7.97 4.42A2 2 0 0 0 7 6.13v4.37l5 3 5-3V6.13a2 2 0 0 0-.97-1.71l-3-1.8a2 2 0 0 0-2.06 0l-3 1.8Z"/><path d="M12 8 7.26 5.15"/><path d="m12 8 4.74-2.85"/><path d="M12 13.5V8"/>`
	svgPathMinimize2             = `<path d="m14 10 7-7"/><path d="M20 10h-6V4"/><path d="m3 21 7-7"/><path d="M4 14h6v6"/>`
	svgPathPackage               = `<path d="M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z"/><path d="M12 22V12"/><polyline points="3.29 7 12 12 20.71 7"/><path d="m7.5 4.27 9 5.15"/>`
	svgPathFoldVertical          = `<path d="M12 22v-6"/><path d="M12 8V2"/><path d="M4 12H2"/><path d="M10 12H8"/><path d="M16 12h-2"/><path d="M22 12h-2"/><path d="m15 19-3-3-3 3"/><path d="m15 5-3 3-3-3"/>`
	svgPathListChecks            = `<path d="M13 5h8"/><path d="M13 12h8"/><path d="M13 19h8"/><path d="m3 17 2 2 4-4"/><path d="m3 7 2 2 4-4"/>`
	svgPathUserRoundCheck        = `<path d="M2 21a8 8 0 0 1 13.292-6"/><circle cx="10" cy="8" r="5"/><path d="m16 19 2 2 4-4"/>`
	svgPathHighlighter           = `<path d="m9 11-6 6v3h9l3-3"/><path d="m22 12-4.6 4.6a2 2 0 0 1-2.8 0l-5.2-5.2a2 2 0 0 1 0-2.8L14 4"/>`
	svgPathArrowUpRight          = `<path d="M7 7h10v10"/><path d="M7 17 17 7"/>`
	svgPathChartColumn           = `<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/>`
	svgPathBookOpenText          = `<path d="M12 5v16"/><path d="M16 13h2"/><path d="M16 9h2"/><path d="M20.001 19A2 2 0 0022 17V5a2 2 0 00-1.999-2L16 3.002A5 5 0 0012 5a5 5 0 00-4-2H4a2 2 0 00-2 2v12a2 2 0 001.999 2H8a5 5 0 014 2 5 5 0 014-2z"/><path d="M6 13h2"/><path d="M6 9h2"/>`
	svgPathSend                  = `<path d="M14.536 21.686a.5.5 0 0 0 .937-.024l6.5-19a.496.496 0 0 0-.635-.635l-19 6.5a.5.5 0 0 0-.024.937l7.93 3.18a2 2 0 0 1 1.112 1.11z"/><path d="m21.854 2.147-10.94 10.939"/>`
)

var (
	iconAsterisk          = parseLucideSVG(svgPathAsterisk)
	iconHexagon           = parseLucideSVG(svgPathHexagon)
	iconSun               = parseLucideSVG(svgPathSun)
	iconMoon              = parseLucideSVG(svgPathMoon)
	iconMonitor           = parseLucideSVG(svgPathMonitor)
	iconShield            = parseLucideSVG(svgPathShield)
	iconLock              = parseLucideSVG(svgPathLock)
	iconLockOpen          = parseLucideSVG(svgPathLockOpen)
	iconSparkles          = parseLucideSVG(svgPathSparkles)
	iconKanban            = parseLucideSVG(svgPathKanban)
	iconZap               = parseLucideSVG(svgPathZap)
	iconRefreshCw         = parseLucideSVG(svgPathRefreshCw)
	iconPlay              = parseLucideSVG(svgPathPlay)
	iconPause             = parseLucideSVG(svgPathPause)
	iconTrash2            = parseLucideSVG(svgPathTrash2)
	iconSwarm             = parseLucideSVG(svgSwarmMark)
	iconPin               = parseLucideSVG(svgPathPin)
	iconPanelLeft         = parseLucideSVG(svgPathPanelLeft)
	iconPalette           = parseLucideSVG(svgPathPalette)
	iconSmartphone        = parseLucideSVG(svgPathSmartphone)
	iconCpu               = parseLucideSVG(svgPathCpu)
	iconPuzzle            = parseLucideSVG(svgPathPuzzle)
	iconSlidersHorizontal = parseLucideSVG(svgPathSlidersHorizontal)
	iconCommand           = parseLucideSVG(svgPathCommand)
	iconArchive           = parseLucideSVG(svgPathArchive)
	iconInfo              = parseLucideSVG(svgPathInfo)
	iconCircleEllipsis    = parseLucideSVG(svgPathCircleEllipsis)
	iconCircleCheck       = parseLucideSVG(svgPathCircleCheck)

	iconEye          = parseLucideSVG(svgPathEye)
	iconHandRaised   = parseLucideSVG(svgPathHandRaised)
	iconPencilLine   = parseLucideSVG(svgPathPencilLine)
	iconWandSparkles = parseLucideSVG(svgPathWandSparkles)
	iconBookOpen     = parseLucideSVG(svgPathBookOpen)
	iconPlusMinus    = parseLucideSVG(svgPathDiff)
	iconExternalLink = parseLucideSVG(svgPathExternalLink)
	iconChartBar     = parseLucideSVG(svgPathChartBar)
	iconStopFilled   = parseLucideSVG(svgPathStopFilled)
	iconStar         = parseLucideSVG(svgPathStar)
	iconStarFilled   = parseLucideSVG(svgPathStarFilled)
	iconTray         = parseLucideSVG(svgPathTray)
	iconMap          = parseLucideSVG(svgPathMap)
	iconSealCheck    = parseLucideSVG(svgPathSealCheck)
	iconNote         = parseLucideSVG(svgPathNote)

	iconProviderAntigravity = bundledIcon("provider-antigravity.svg")
	iconProviderCommandCode = bundledIcon("provider-commandcode.svg")
	iconProviderDroid       = bundledIcon("provider-droid.svg")
	iconProviderPi          = bundledIcon("provider-pi.svg")
	iconProviderCline       = bundledIcon("provider-cline.svg")
	iconProviderCodex       = bundledIcon("provider-codex.svg")
	iconProviderClaude      = bundledIcon("provider-claude.svg")
	iconProviderCopilot     = bundledIcon("provider-copilot.svg")
	iconProviderOmp         = bundledIcon("provider-omp.svg")
	iconProviderDeepseek    = bundledIcon("provider-deepseek.svg")
	iconProviderZai         = bundledIcon("provider-zai.svg")
	iconProviderMeta        = bundledIcon("provider-meta.svg")
	iconProviderCursor      = bundledIcon("provider-cursor.svg")
	iconProviderDevin       = bundledIcon("provider-devin.svg")
	iconProviderGrok        = bundledIcon("provider-grok.svg")
	iconProviderOpencode    = bundledIcon("provider-opencode.svg")

	// Magpie 没有对应品牌图标，保留原绘制。
	iconProviderMagpie = parseLucideSVG(`<rect x="3" y="3" width="18" height="18" rx="4"/><path d="M8.5 15.5V8.5l3.5 4 3.5-4v7"/>`)

	// 原色版本：与上面同名的内置资源，保留品牌配色与渐变。
	iconProviderAntigravityColor = bundledColorIcon("provider-antigravity.svg")
	iconProviderCommandCodeColor = bundledColorIcon("provider-commandcode.svg")
	iconProviderDroidColor       = bundledColorIcon("provider-droid.svg")
	iconProviderPiColor          = bundledColorIcon("provider-pi.svg")
	iconProviderClineColor       = bundledColorIcon("provider-cline.svg")
	iconProviderCodexColor       = bundledColorIcon("provider-codex.svg")
	iconProviderClaudeColor      = bundledColorIcon("provider-claude.svg")
	iconProviderCopilotColor     = bundledColorIcon("provider-copilot.svg")
	iconProviderOmpColor         = bundledColorIcon("provider-omp.svg")
	iconProviderDeepseekColor    = bundledColorIcon("provider-deepseek.svg")
	iconProviderZaiColor         = bundledColorIcon("provider-zai.svg")
	iconProviderMetaColor        = bundledColorIcon("provider-meta.svg")
	iconProviderCursorColor      = bundledColorIcon("provider-cursor.svg")
	iconProviderDevinColor       = bundledColorIcon("provider-devin.svg")
	iconProviderGrokColor        = bundledColorIcon("provider-grok.svg")
	iconProviderOpencodeColor    = bundledColorIcon("provider-opencode.svg")
)

// 图标审计页选定的补充图标变量
var (
	iconCircleSmall           = parseLucideSVG(svgPathCircleSmall)
	iconListTodo              = parseLucideSVG(svgPathListTodo)
	iconMerge                 = parseLucideSVG(svgPathMerge)
	iconNetwork               = parseLucideSVG(svgPathNetwork)
	iconGitPullRequestCreate  = parseLucideSVG(svgPathGitPullRequestCreate)
	iconTimer                 = parseLucideSVG(svgPathTimer)
	iconNotebook              = parseLucideSVG(svgPathNotebook)
	iconSquareArrowOutUpRight = parseLucideSVG(svgPathSquareArrowOutUpRight)
	iconMessageSquarePlus     = parseLucideSVG(svgPathMessageSquarePlus)
	iconLogIn                 = parseLucideSVG(svgPathLogIn)
	iconEqual                 = parseLucideSVG(svgPathEqual)
	iconMoveDown              = parseLucideSVG(svgPathMoveDown)
	iconCircleStop            = parseLucideSVG(svgPathCircleStop)
	iconCircleX               = parseLucideSVG(svgPathCircleX)
	iconPower                 = parseLucideSVG(svgPathPower)
	iconLayoutDashboard       = parseLucideSVG(svgPathLayoutDashboard)
	iconArrowUpToLine         = parseLucideSVG(svgPathArrowUpToLine)
	iconReply                 = parseLucideSVG(svgPathReply)
	iconChevronsUp            = parseLucideSVG(svgPathChevronsUp)
	iconGitCommitVertical     = parseLucideSVG(svgPathGitCommitVertical)
	iconOctagonAlert          = parseLucideSVG(svgPathOctagonAlert)
	iconFlame                 = parseLucideSVG(svgPathFlame)
	iconFileDiff              = parseLucideSVG(svgPathFileDiff)
	iconClipboardList         = parseLucideSVG(svgPathClipboardList)
	iconListOrdered           = parseLucideSVG(svgPathListOrdered)
	iconGitFork               = parseLucideSVG(svgPathGitFork)
	iconCircleDashed          = parseLucideSVG(svgPathCircleDashed)
	iconScale                 = parseLucideSVG(svgPathScale)
	iconScrollText            = parseLucideSVG(svgPathScrollText)
	iconAnchor                = parseLucideSVG(svgPathAnchor)
	iconMagnet                = parseLucideSVG(svgPathMagnet)
	iconBot                   = parseLucideSVG(svgPathBot)
	iconCircleDot             = parseLucideSVG(svgPathCircleDot)
	iconColumns3              = parseLucideSVG(svgPathColumns3)
	iconCalendarClock         = parseLucideSVG(svgPathCalendarClock)
	iconBatteryLow            = parseLucideSVG(svgPathBatteryLow)
	iconLoader                = parseLucideSVG(svgPathLoader)
	iconClock                 = parseLucideSVG(svgPathClock)
	iconRepeat                = parseLucideSVG(svgPathRepeat)
	iconShuffle               = parseLucideSVG(svgPathShuffle)
	iconCirclePause           = parseLucideSVG(svgPathCirclePause)
	iconBoxes                 = parseLucideSVG(svgPathBoxes)
	iconMinimize2             = parseLucideSVG(svgPathMinimize2)
	iconPackage               = parseLucideSVG(svgPathPackage)
	iconFoldVertical          = parseLucideSVG(svgPathFoldVertical)
	iconListChecks            = parseLucideSVG(svgPathListChecks)
	iconUserRoundCheck        = parseLucideSVG(svgPathUserRoundCheck)
	iconHighlighter           = parseLucideSVG(svgPathHighlighter)
	iconArrowUpRight          = parseLucideSVG(svgPathArrowUpRight)
	iconChartColumn           = parseLucideSVG(svgPathChartColumn)
	iconBookOpenText          = parseLucideSVG(svgPathBookOpenText)
	iconSend                  = parseLucideSVG(svgPathSend)
)

// 追加各图标快捷导出函数
func IconLock() *ui.SVG              { return iconLock }
func IconLockOpen() *ui.SVG          { return iconLockOpen }
func IconSparkles() *ui.SVG          { return iconSparkles }
func IconRefreshCw() *ui.SVG         { return iconRefreshCw }
func IconPlay() *ui.SVG              { return iconPlay }
func IconTrash2() *ui.SVG            { return iconTrash2 }
func IconSwarm() *ui.SVG             { return iconSwarm }
func IconPin() *ui.SVG               { return iconPin }
func IconPanelLeft() *ui.SVG         { return iconPanelLeft }
func IconPalette() *ui.SVG           { return iconPalette }
func IconSmartphone() *ui.SVG        { return iconSmartphone }
func IconCpu() *ui.SVG               { return iconCpu }
func IconSlidersHorizontal() *ui.SVG { return iconSlidersHorizontal }
func IconArchive() *ui.SVG           { return iconArchive }
func IconInfo() *ui.SVG              { return iconInfo }
func IconCircleEllipsis() *ui.SVG    { return iconCircleEllipsis }
func IconCircleCheck() *ui.SVG       { return iconCircleCheck }

// IconStar 是收藏用的线框星（未收藏态）；IconStarFilled 为同一多边形的填充版（已收藏态）。
func IconStar() *ui.SVG       { return iconStar }
func IconStarFilled() *ui.SVG { return iconStarFilled }

func IconEye() *ui.SVG          { return iconEye }
func IconHandRaised() *ui.SVG   { return iconHandRaised }
func IconPencilLine() *ui.SVG   { return iconPencilLine }
func IconWandSparkles() *ui.SVG { return iconWandSparkles }
func IconBookOpen() *ui.SVG     { return iconBookOpen }
func IconExternalLink() *ui.SVG { return iconExternalLink }
func IconChartBar() *ui.SVG     { return iconChartBar }
func IconStopFilled() *ui.SVG   { return iconStopFilled }
func IconTray() *ui.SVG         { return iconTray }
func IconSealCheck() *ui.SVG    { return iconSealCheck }
func IconNote() *ui.SVG         { return iconNote }

// 图标审计页选定的补充图标快捷导出函数
func IconCircleSmall() *ui.SVG           { return iconCircleSmall }
func IconListTodo() *ui.SVG              { return iconListTodo }
func IconMerge() *ui.SVG                 { return iconMerge }
func IconNetwork() *ui.SVG               { return iconNetwork }
func IconGitPullRequestCreate() *ui.SVG  { return iconGitPullRequestCreate }
func IconTimer() *ui.SVG                 { return iconTimer }
func IconNotebook() *ui.SVG              { return iconNotebook }
func IconSquareArrowOutUpRight() *ui.SVG { return iconSquareArrowOutUpRight }
func IconMessageSquarePlus() *ui.SVG     { return iconMessageSquarePlus }
func IconLogIn() *ui.SVG                 { return iconLogIn }
func IconEqual() *ui.SVG                 { return iconEqual }
func IconMoveDown() *ui.SVG              { return iconMoveDown }
func IconCircleStop() *ui.SVG            { return iconCircleStop }
func IconCircleX() *ui.SVG               { return iconCircleX }
func IconPower() *ui.SVG                 { return iconPower }
func IconLayoutDashboard() *ui.SVG       { return iconLayoutDashboard }
func IconArrowUpToLine() *ui.SVG         { return iconArrowUpToLine }
func IconReply() *ui.SVG                 { return iconReply }
func IconChevronsUp() *ui.SVG            { return iconChevronsUp }
func IconGitCommitVertical() *ui.SVG     { return iconGitCommitVertical }
func IconOctagonAlert() *ui.SVG          { return iconOctagonAlert }
func IconFlame() *ui.SVG                 { return iconFlame }
func IconFileDiff() *ui.SVG              { return iconFileDiff }
func IconClipboardList() *ui.SVG         { return iconClipboardList }
func IconListOrdered() *ui.SVG           { return iconListOrdered }
func IconGitFork() *ui.SVG               { return iconGitFork }
func IconScale() *ui.SVG                 { return iconScale }
func IconScrollText() *ui.SVG            { return iconScrollText }
func IconAnchor() *ui.SVG                { return iconAnchor }
func IconMagnet() *ui.SVG                { return iconMagnet }
func IconBot() *ui.SVG                   { return iconBot }
func IconColumns3() *ui.SVG              { return iconColumns3 }
func IconCalendarClock() *ui.SVG         { return iconCalendarClock }
func IconBatteryLow() *ui.SVG            { return iconBatteryLow }
func IconLoader() *ui.SVG                { return iconLoader }
func IconClock() *ui.SVG                 { return iconClock }
func IconRepeat() *ui.SVG                { return iconRepeat }
func IconShuffle() *ui.SVG               { return iconShuffle }
func IconBoxes() *ui.SVG                 { return iconBoxes }
func IconPackage() *ui.SVG               { return iconPackage }
func IconFoldVertical() *ui.SVG          { return iconFoldVertical }
func IconListChecks() *ui.SVG            { return iconListChecks }
func IconUserRoundCheck() *ui.SVG        { return iconUserRoundCheck }
func IconHighlighter() *ui.SVG           { return iconHighlighter }
func IconArrowUpRight() *ui.SVG          { return iconArrowUpRight }
func IconBookOpenText() *ui.SVG          { return iconBookOpenText }

// ProviderIcon 按 Provider ID 返回对应的 Agent 图标。
// custom- 前缀的自定义端点回落到 globe，其余未知 ID 回落到 brain。
func ProviderIcon(id string) *ui.SVG {
	key := providerIconKey(id)
	if key == "" {
		return iconBrain
	}
	switch key {
	case "codex", "openai":
		return iconProviderCodex
	case "claude":
		return iconProviderClaude
	case "agy", "antigravity":
		return iconProviderAntigravity
	case "copilot":
		return iconProviderCopilot
	case "mcode":
		return iconBrain
	case "pi", "mu":
		return iconProviderPi
	case "omp":
		return iconProviderOmp
	case "commandcode", "cmd":
		return iconProviderCommandCode
	case "deepseek":
		return iconProviderDeepseek
	case "zai":
		return iconProviderZai
	case "meta":
		return iconProviderMeta
	case "magpie", "magpie1", "magpie2", "magpie3", "magpie4":
		return iconProviderMagpie
	case "droid":
		return iconProviderDroid
	case "cline", "acp":
		return iconProviderCline
	case "cursor":
		return iconProviderCursor
	case "devin":
		return iconProviderDevin
	case "grok":
		return iconProviderGrok
	case "opencode":
		return iconProviderOpencode
	}
	if strings.HasPrefix(key, "custom-") {
		return iconGlobe
	}
	return iconBrain
}

// ProviderIconColor 按 Provider ID 返回内置品牌资源里的彩色图标，映射与 ProviderIcon 一致。
// 没有彩色资源（Magpie、custom- 端点、未知 ID）时退回 ProviderIcon。
//
// 彩色图标要用 renderProviderIconColor 绘制：ui.Icon 会强制用文字色覆盖它的形状，
// 而 ui.Image 会按 SVG 自身颜色绘制，只在 SVG 使用 currentColor 的地方取文字色。
//
// Antigravity、Codex 等资源本身是纯黑，渲染时用 providerIconColorFor 把 currentColor
// 与纯黑换成主题文字色，确保在深色背景下清晰可见，其余品牌色与渐变原样保留。
func ProviderIconColor(id string) *ui.SVG {
	base := colorProviderIcons[providerIconKey(id)]
	if base == nil {
		return ProviderIcon(id)
	}
	return base
}

// providerIconColorFor 是渲染用的彩色图标：在 ProviderIconColor 基础上做两处主题适配并缓存结果：
//   - currentColor 换成当前主题的 secondary 文字色（Zai 本身就使用 currentColor）；
//   - 纯黑资源（Antigravity、Codex、CommandCode 等）在深浅主题下均适配为当前主题的主文字色，
//     避免在深色背景上因纯黑而不可见；其余品牌原色原样保留。
func providerIconColorFor(c *ui.Context, id string) *ui.SVG {
	key := providerIconKey(id)
	base := colorProviderIcons[key]
	if base == nil {
		return ProviderIcon(id)
	}
	dark := isDark(c)
	secondary := ThemeSecondaryText(c)
	primary := ThemePrimaryText(c)
	hex := fmt.Sprintf("%02X%02X%02X", secondary.R, secondary.G, secondary.B)
	iconColorTintMu.Lock()
	cacheKey := fmt.Sprintf("%s|%s|%t|%02X%02X%02X", key, hex, dark, primary.R, primary.G, primary.B)
	svg, ok := iconColorTintCache[cacheKey]
	iconColorTintMu.Unlock()
	if ok {
		return svg
	}
	data, err := bundledColorAssets.ReadFile("icons_assets/color/provider-" + key + ".svg")
	if err != nil {
		return base
	}
	src := strings.ReplaceAll(string(data), "currentColor", "#"+hex)
	// 纯黑单色图形在深浅主题下均替换为主文字色，保证在深色背景下清晰可见
	src = strings.ReplaceAll(src, "#000000",
		fmt.Sprintf("#%02X%02X%02X", primary.R, primary.G, primary.B))
	svg = ui.MustParseSVG([]byte(src))
	iconColorTintMu.Lock()
	iconColorTintCache[cacheKey] = svg
	iconColorTintMu.Unlock()
	return svg
}

var (
	iconColorTintMu    sync.Mutex
	iconColorTintCache = make(map[string]*ui.SVG)
)

// colorProviderIcons 是彩色资源的 Provider ID 映射，与 ProviderIcon 的 ID 保持一致。
var colorProviderIcons = map[string]*ui.SVG{
	"codex":       iconProviderCodexColor,
	"openai":      iconProviderCodexColor,
	"claude":      iconProviderClaudeColor,
	"agy":         iconProviderAntigravityColor,
	"antigravity": iconProviderAntigravityColor,
	"copilot":     iconProviderCopilotColor,
	"pi":          iconProviderPiColor,
	"mu":          iconProviderPiColor,
	"omp":         iconProviderOmpColor,
	"commandcode": iconProviderCommandCodeColor,
	"cmd":         iconProviderCommandCodeColor,
	"deepseek":    iconProviderDeepseekColor,
	"zai":         iconProviderZaiColor,
	"meta":        iconProviderMetaColor,
	"droid":       iconProviderDroidColor,
	"cline":       iconProviderClineColor,
	"acp":         iconProviderClineColor,
	"cursor":      iconProviderCursorColor,
	"devin":       iconProviderDevinColor,
	"grok":        iconProviderGrokColor,
	"opencode":    iconProviderOpencodeColor,
}

// providerIconKey 归一化 Provider ID，别名统一映射为主标识；custom- 端点保留前缀，空 ID 返回空串。
func providerIconKey(id string) string {
	k := strings.ToLower(strings.TrimSpace(id))
	switch k {
	case "agy":
		return "antigravity"
	case "cmd":
		return "commandcode"
	case "mu":
		return "pi"
	case "openai":
		return "codex"
	case "anthropic":
		return "claude"
	case "magpie", "magpie1", "magpie2", "magpie3", "magpie4":
		return "magpie"
	default:
		if strings.HasPrefix(k, "magpie-") {
			return "magpie"
		}
		if _, ok := colorProviderIcons[k]; !ok {
			if prefix, _, hasDash := strings.Cut(k, "-"); hasDash {
				if _, okPrefix := colorProviderIcons[prefix]; okPrefix {
					return prefix
				}
			}
		}
		return k
	}
}

// renderProviderIconColor 以 size×size 绘制供应商彩色图标：有原色资源时用 ui.Image
// 保留品牌颜色，否则退回 ui.Icon 并按 fallback 着色。找不到资源时回退到通用图标。
func renderProviderIconColor(c *ui.Context, id string, size float32, fallback ui.Color) {
	if size <= 0 {
		size = 14
	}
	key := providerIconKey(id)
	if base := colorProviderIcons[key]; base != nil {
		w, h := base.Size()
		var boxW, boxH float32 = size, size
		if w > 0 && h > 0 {
			if w > h {
				boxH = size * h / w
			} else if h > w {
				boxW = size * w / h
			}
		}
		ui.Image(c, providerIconColorFor(c, id)).
			Size(boxW, boxH).
			Fit(ui.Contain)
		return
	}
	ui.Icon(c, ProviderIcon(id)).
		Size(size, size).
		TextColor(fallback)
}
