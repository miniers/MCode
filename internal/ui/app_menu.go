package mcodeui

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 应用菜单里「记忆」入口的文案：本地记忆后端只提供文档与规则，入口就叫「记忆」；
// 接入 Nowledge 时保留原名，便于和 Nowledge Mem 对应。
const (
	memoryEntryLabelDefault = "记忆 (Nowledge)"
	memoryEntryLabelLocal   = "记忆"
)

// memoryEntryLabel 返回记忆入口在当前后端下的文案；没有 client 时保持原文案。
func memoryEntryLabel(client Client) string {
	if client != nil && client.MemoryBackend() == config.MemoryBackendLocal {
		return memoryEntryLabelLocal
	}
	return memoryEntryLabelDefault
}

// memoryMenuClient 返回构建原生菜单时可用于判断记忆后端的 client：
// 菜单在窗口创建前安装，那时还没有 client；改键重建菜单时取当前窗口的 client。
func memoryMenuClient() Client {
	if _, state := activeChatWindow(); state != nil {
		return state.client
	}
	return nil
}

// memoryMenuLabelBuilt 保存最近一次构建原生菜单时记忆入口的文案。
var memoryMenuLabelBuilt atomic.Value

// syncMemoryMenuLabel 在拿到 client 后按实际后端把已安装菜单里的记忆入口改为对应文案。
// 菜单在窗口创建前安装，那时还拿不到 client，只能在首帧之后补一次；文案相同时不做任何事。
func syncMemoryMenuLabel() {
	if mygo.App.Menu() == nil {
		return
	}
	client := memoryMenuClient()
	if client == nil {
		return
	}
	label := memoryEntryLabel(client)
	if built, _ := memoryMenuLabelBuilt.Load().(string); built == label {
		return
	}
	InstallAppMenu()
}

// appMenuGOOS 是菜单平台判断的注入点，测试可临时改写它来验证各平台的菜单结构。
var appMenuGOOS = runtime.GOOS

// menuAccelerator 返回菜单项的当前 Accelerator：优先取快捷键表的有效绑定（含用户自定义），
// 取不到或无法表示时回退到 fallback（默认写死的写法）。
func menuAccelerator(id, fallback string) string {
	mods, key, ok := shortcutBindingFor(id)
	if !ok {
		return fallback
	}
	if accel := formatMenuAccelerator(mods, key); accel != "" {
		return accel
	}
	return fallback
}

// formatMenuAccelerator 把修饰位与按键格式化成 mygo 的 Accelerator 写法（如 Ctrl+CmdOrCtrl+A）。
// 非 macOS 上 Cmd 与 Ctrl 是同一个修饰位，只输出 CmdOrCtrl 避免重复。
func formatMenuAccelerator(mods ui.Modifiers, key ui.Key) string {
	parts := make([]string, 0, 4)
	if mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	if ui.Cmd != ui.Ctrl && mods&ui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if mods&ui.Cmd != 0 {
		parts = append(parts, "CmdOrCtrl")
	}
	keyName := menuAcceleratorKey(key)
	if keyName == "" {
		return ""
	}
	return strings.Join(append(parts, keyName), "+")
}

// menuAcceleratorKey 返回单个按键在 mygo Accelerator 里的写法；无法表示时返回空串。
func menuAcceleratorKey(key ui.Key) string {
	switch key {
	case ui.KeyComma:
		return ","
	case ui.KeyPeriod:
		return "."
	case ui.KeyBackquote:
		return "`"
	case ui.KeySlash:
		return "/"
	case ui.KeyUp:
		return "Up"
	case ui.KeyDown:
		return "Down"
	case ui.KeyLeft:
		return "Left"
	case ui.KeyRight:
		return "Right"
	case ui.KeyEnter:
		return "Enter"
	case ui.KeyEscape:
		return "Escape"
	case ui.KeySpace:
		return "Space"
	case ui.KeyTab:
		return "Tab"
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		return string(rune('A' + key - ui.KeyA))
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		return string(rune('0' + key - ui.Key0))
	}
	return ""
}

// DispatchMenuAction 把原生菜单点击派发给当前活动窗口的会话状态。
// 菜单点击由原生主线程触发，直接改渲染状态会与正在进行的帧交错，
// 因此统一入队，由该窗口在下一帧按顺序消费。
// 全部窗口关闭后菜单栏仍然可用：此时先开一个聊天窗口，再在其中执行动作。
func DispatchMenuAction(id string, index int) {
	if id == "open-settings" || id == "open-about" || id == "check-update" {
		settingsWindowMu.Lock()
		entry := settingsWindow
		settingsWindowMu.Unlock()
		if entry != nil {
			switch id {
			case "open-settings":
				openSettingsWindow(entry.owner, "")
			case "open-about":
				openSettingsWindow(entry.owner, "about")
			case "check-update":
				openAboutAndCheckUpdate(entry.owner)
			}
			return
		}
	}
	win, state := activeChatWindow()
	if state == nil {
		win, state = ensureChatWindow()
		if state == nil {
			return
		}
	}
	needsProjects := actionNeedsLoadedProjects(id)
	if needsProjects && win != nil {
		// 托盘隐藏或最小化时先把窗口显出来，否则动作执行了用户也看不见。
		mygo.RunOnMain(func() {
			if win.IsMinimized() {
				win.Restore()
			}
			win.Show()
			win.Focus()
		})
	}
	if needsProjects && !state.hasLoadedProjects() {
		state.enqueueActionWhenReady(id, index)
		return
	}
	state.enqueueAction(id, index)
}

// actionNeedsLoadedProjects 判断菜单动作是否依赖已加载的项目列表。
// 新建会话要落到某个项目上，而窗口刚打开时项目还在后台加载。
func actionNeedsLoadedProjects(id string) bool {
	switch id {
	case "new-thread", "new-worktree-thread":
		return true
	}
	return false
}

// BuildAppMenu 构建应用原生菜单，菜单项只负责把动作 ID 交给 DispatchMenuAction，
// 具体行为仍由各窗口的渲染循环执行。
func BuildAppMenu() *mygo.Menu {
	if appMenuGOOS == "windows" {
		return buildWindowsAppMenu()
	}
	return buildDarwinAppMenu()
}

// buildDarwinAppMenu 构建 macOS 原生顶栏菜单：应用、文件、编辑、视图、会话、窗口。
func buildDarwinAppMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{
			Role:  mygo.RoleAppMenu,
			Label: "MCode",
			Submenu: []*mygo.MenuItem{
				{Label: "关于 MCode", Click: func(*mygo.MenuItem, *mygo.Window) { DispatchMenuAction("open-about", 0) }},
				{Label: "检查更新…", Click: func(*mygo.MenuItem, *mygo.Window) { DispatchMenuAction("check-update", 0) }},
				mygo.Separator(),
				settingsMenuItem(),
				mygo.Separator(),
				{Role: mygo.RoleServices, Label: "服务"},
				mygo.Separator(),
				{Role: mygo.RoleHide, Label: "隐藏 MCode"},
				{Role: mygo.RoleHideOthers, Label: "隐藏其他"},
				{Role: mygo.RoleUnhide, Label: "全部显示"},
				mygo.Separator(),
				{Role: mygo.RoleQuit, Label: "退出 MCode"},
			},
		},
		{
			Label: "文件",
			Submenu: append(buildFileActionMenuItems(),
				mygo.Separator(),
				closeMenuItem(),
			),
		},
		{Role: mygo.RoleEditMenu},
		{
			Label:   "视图",
			Submenu: buildViewMenuItems(),
		},
		{
			Label:   "会话",
			Submenu: buildThreadMenuItems(),
		},
		{Role: mygo.RoleWindowMenu},
	})
}

// buildWindowsAppMenu 构建 Windows 菜单：文件、编辑、视图、会话、窗口、帮助。
//
// Windows 上 mygo 会把 macOS 专用的角色标成隐藏（mygo menu.go 的 prepareItem 用 macOnlyRoles
// 过滤），应用菜单本身（RoleAppMenu）、关于、服务、隐藏、全部显示、帮助都在其中，所以这里不写这些项：
// 原应用菜单里仍有意义的偏好设置并进「文件」，退出用 RoleQuit 放在文件末尾，
// 关于 MCode 和检查更新放进最后的「帮助」。
//
// 主窗口用 TitleBarHiddenInset 创建（internal/ui/windows.go），标题栏隐藏的窗口不显示菜单栏
// （mygo internal/windows/menu.go 的 menuShown 与 barless），但菜单仍然装进窗口：
// 快捷键来自菜单的 Accelerator（同上 installMenu 收集 accels，由 surface 消费），
// Alt/F10 会把整条菜单栏以弹出菜单打开（同上 popupMenuBar），所以 Windows 上照旧安装菜单。
func buildWindowsAppMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{
			Label: "文件",
			Submenu: append(buildFileActionMenuItems(),
				mygo.Separator(),
				settingsMenuItem(),
				mygo.Separator(),
				closeMenuItem(),
				mygo.Separator(),
				&mygo.MenuItem{Role: mygo.RoleQuit, Label: "退出"},
			),
		},
		{Role: mygo.RoleEditMenu},
		{
			Label:   "视图",
			Submenu: buildViewMenuItems(),
		},
		{
			Label:   "会话",
			Submenu: buildThreadMenuItems(),
		},
		{Role: mygo.RoleWindowMenu},
		{
			Label: "帮助",
			Submenu: []*mygo.MenuItem{
				{
					Label: "关于 MCode",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						DispatchMenuAction("open-about", 0)
					},
				},
				{Label: "检查更新…", Click: func(*mygo.MenuItem, *mygo.Window) { DispatchMenuAction("check-update", 0) }},
			},
		},
	})
}

// buildFileActionMenuItems 构建「文件」菜单里与平台无关的动作项：新建会话、添加项目。
func buildFileActionMenuItems() []*mygo.MenuItem {
	return []*mygo.MenuItem{
		{
			Label:       "新建会话",
			Accelerator: menuAccelerator("new-thread", "CmdOrCtrl+N"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("new-thread", 0)
			},
		},
		{
			Label:       "在 Worktree 新建会话",
			Accelerator: menuAccelerator("new-worktree-thread", "Alt+CmdOrCtrl+N"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("new-worktree-thread", 0)
			},
		},
		mygo.Separator(),
		{
			Label:       "添加项目…",
			Accelerator: menuAccelerator("add-project", "Shift+CmdOrCtrl+O"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("add-project", 0)
			},
		},
	}
}

// closeMenuItem 返回「文件」菜单末尾的关闭项：⌘W 先关掉文件面板里正在预览的文件，
// 没有预览时才照常关闭窗口。这里不能再设置 Role：RoleClose 的点击会先执行系统角色
// 直接关窗口，Click 回调不会生效。
func closeMenuItem() *mygo.MenuItem {
	return &mygo.MenuItem{
		Label:       "关闭",
		Accelerator: "CmdOrCtrl+W",
		Click: func(_ *mygo.MenuItem, win *mygo.Window) {
			if win == nil {
				return
			}
			chatWindowsMu.RLock()
			state := chatWindows[win]
			chatWindowsMu.RUnlock()
			// 设置等非聊天窗口查不到 state，照常关闭窗口。
			if state != nil && browserCloseFromMenu(state) {
				return
			}
			if state != nil && fileManagerHasClosableTab(state) {
				state.enqueueAction("close-file-tab", 0)
				return
			}
			win.Close()
		},
	}
}

// settingsMenuItem 返回「偏好设置…」菜单项：macOS 放在应用菜单，Windows 放在「文件」菜单。
func settingsMenuItem() *mygo.MenuItem {
	return &mygo.MenuItem{
		Label:       "偏好设置…",
		Accelerator: menuAccelerator("open-settings", "CmdOrCtrl+,"),
		Click: func(*mygo.MenuItem, *mygo.Window) {
			DispatchMenuAction("open-settings", 0)
		},
	}
}

// buildViewMenuItems 构建“视图”菜单项：右侧分栏面板的三项由注册表生成，
// 其余项仍手写；生成项放在终端开关之后，顺序为文件管理、网页预览、改动。
func buildViewMenuItems() []*mygo.MenuItem {
	// 记忆入口的文案取决于记忆后端；构建时记下实际用的文案，供 syncMemoryMenuLabel 判断是否需要重建。
	memoryLabel := memoryEntryLabel(memoryMenuClient())
	memoryMenuLabelBuilt.Store(memoryLabel)
	items := []*mygo.MenuItem{
		{
			Label:       "显示/隐藏侧边栏",
			Accelerator: menuAccelerator("toggle-sidebar", "CmdOrCtrl+B"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-sidebar", 0)
			},
		},
		{
			Label:       "命令面板…",
			Accelerator: menuAccelerator("command-palette", "CmdOrCtrl+K"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("command-palette", 0)
			},
		},
		{
			Label:       "会话内查找",
			Accelerator: menuAccelerator("find-in-thread", "CmdOrCtrl+F"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("find-in-thread", 0)
			},
		},
		{
			Label:       "键盘快捷键…",
			Accelerator: menuAccelerator("show-shortcuts", "CmdOrCtrl+/"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("show-shortcuts", 0)
			},
		},
		mygo.Separator(),
		{
			Label:       "显示/隐藏终端",
			Accelerator: menuAccelerator("toggle-terminal", "CmdOrCtrl+J"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-terminal", 0)
			},
		},
	}
	items = append(items, sidePaneMenuItems()...)
	items = append(items,
		&mygo.MenuItem{Label: "显示/隐藏右栏", Accelerator: menuAccelerator("toggle-inspector", ""), Click: func(*mygo.MenuItem, *mygo.Window) {
			DispatchMenuAction("toggle-inspector", 0)
		}},
		&mygo.MenuItem{
			Label:       "项目文档 (.mcode/context)",
			Accelerator: menuAccelerator("toggle-context", "Shift+CmdOrCtrl+K"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-context", 0)
			},
		},
		&mygo.MenuItem{
			Label: memoryLabel,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-memory", 0)
			},
		},
		&mygo.MenuItem{
			Label:       "源码编辑器…",
			Accelerator: menuAccelerator("open-editor", "Shift+CmdOrCtrl+E"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("open-editor", 0)
			},
		},
		&mygo.MenuItem{
			Label:       "动态视图",
			Accelerator: menuAccelerator("toggle-activity", "Alt+CmdOrCtrl+U"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-activity", 0)
			},
		},
		mygo.Separator(),
		&mygo.MenuItem{Role: mygo.RoleToggleFullScreen},
	)
	return items
}

// sidePaneMenuItems 由 side_pane.go 的注册表生成右侧分栏面板的菜单开关项。
// 循环变量先拷贝到局部变量，避免所有闭包捕获同一个值。
func sidePaneMenuItems() []*mygo.MenuItem {
	items := make([]*mygo.MenuItem, 0, len(sidePanes))
	for i := range sidePanes {
		p := &sidePanes[i]
		id := p.actionID
		label := "显示/隐藏" + p.title
		if p.id == sidePaneGitID {
			label = p.menuLabel
		}
		items = append(items, &mygo.MenuItem{
			Label:       label,
			Accelerator: menuAccelerator(id, p.menuAccel),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction(id, 0)
			},
		})
	}
	return items
}

// buildThreadMenuItems 构建“会话”菜单项，其中 ⌘1~⌘9 由循环生成。
func buildThreadMenuItems() []*mygo.MenuItem {
	items := []*mygo.MenuItem{
		{
			Label:       "停止当前回复",
			Accelerator: menuAccelerator("stop-reply", "CmdOrCtrl+."),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("stop-reply", 0)
			},
		},
		{
			Label:       "切换 Build/Plan 模式",
			Accelerator: menuAccelerator("toggle-plan-mode", "Shift+CmdOrCtrl+P"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("toggle-plan-mode", 0)
			},
		},
		mygo.Separator(),
		{
			Label:       "上一会话",
			Accelerator: menuAccelerator("previous-thread", "Alt+CmdOrCtrl+Up"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("previous-thread", 0)
			},
		},
		{
			Label:       "下一会话",
			Accelerator: menuAccelerator("next-thread", "Alt+CmdOrCtrl+Down"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("next-thread", 0)
			},
		},
		{
			Label:       "运行项目脚本",
			Accelerator: menuAccelerator("run-script", "Alt+CmdOrCtrl+R"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("run-script", 0)
			},
		},
		mygo.Separator(),
	}
	// ⌘1~⌘9 是固定绑定（不受覆盖影响），直接由绑定表生成菜单项。
	for i, binding := range selectThreadBindings() {
		index := i + 1
		items = append(items, &mygo.MenuItem{
			Label:       fmt.Sprintf("会话 %d", index),
			Accelerator: formatMenuAccelerator(binding.mods, binding.key),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("select-thread-n", index)
			},
		})
	}
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			Label:       "转发到新会话",
			Accelerator: menuAccelerator("forward-thread", "Shift+CmdOrCtrl+D"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("forward-thread", 0)
			},
		},
		&mygo.MenuItem{
			Label:       "休眠/重新激活会话",
			Accelerator: menuAccelerator("dormant-thread", "Shift+CmdOrCtrl+S"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("dormant-thread", 0)
			},
		},
		&mygo.MenuItem{
			Label:       "归档会话",
			Accelerator: menuAccelerator("archive-thread", "Ctrl+CmdOrCtrl+A"),
			Click: func(*mygo.MenuItem, *mygo.Window) {
				DispatchMenuAction("archive-thread", 0)
			},
		},
	)
	return items
}

// InstallAppMenu 把构建好的菜单安装为应用主菜单。
// Windows 上菜单装到窗口菜单栏，标题栏隐藏的主窗口不显示它，但快捷键与 Alt/F10 弹出菜单仍然可用。
func InstallAppMenu() {
	mygo.App.SetMenu(BuildAppMenu())
}

// refreshAppMenu 在快捷键键位变化后重建原生菜单，让 Accelerator 跟随新键位。
// SetMenu 内部会把后端更新派发到主线程，与 InstallAppMenu 的调用方式一致；
// 菜单尚未安装时（启动早期、测试）无需重建。
func refreshAppMenu() {
	if mygo.App.Menu() == nil {
		return
	}
	InstallAppMenu()
}
