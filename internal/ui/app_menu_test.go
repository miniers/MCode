package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// TestMemoryEntryLabelFollowsBackend 验证记忆入口文案：本地后端叫「记忆」，
// Nowledge 后端保留「记忆 (Nowledge)」，没有 client 时也保持原文案。
func TestMemoryEntryLabelFollowsBackend(t *testing.T) {
	fake := NewFakeClient()
	if got := memoryEntryLabel(fake); got != memoryEntryLabelDefault {
		t.Fatalf("Nowledge 后端的记忆入口文案为 %q，期望 %q", got, memoryEntryLabelDefault)
	}
	fake.memoryBackend = "local"
	if got := memoryEntryLabel(fake); got != memoryEntryLabelLocal {
		t.Fatalf("本地后端的记忆入口文案为 %q，期望 %q", got, memoryEntryLabelLocal)
	}
	if got := memoryEntryLabel(nil); got != memoryEntryLabelDefault {
		t.Fatalf("没有 client 时应保持原文案，得到 %q", got)
	}
}

// TestBuildAppMenu 验证原生顶栏菜单的结构、顺序与快捷键绑定。
func TestBuildAppMenu(t *testing.T) {
	menu := BuildAppMenu()
	if menu == nil {
		t.Fatal("BuildAppMenu 不应返回 nil")
	}

	items := menu.Items()
	if len(items) != 6 {
		t.Fatalf("顶级菜单应有 6 项，得到 %d", len(items))
	}
	if items[0].Role != mygo.RoleAppMenu {
		t.Errorf("第 1 个顶级菜单应为应用菜单，得到 Role=%q", items[0].Role)
	}
	if items[2].Role != mygo.RoleEditMenu {
		t.Errorf("第 3 个顶级菜单应为编辑菜单，得到 Role=%q", items[2].Role)
	}
	if items[5].Role != mygo.RoleWindowMenu {
		t.Errorf("第 6 个顶级菜单应为窗口菜单，得到 Role=%q", items[5].Role)
	}
	if items[1].Label != "文件" || items[3].Label != "视图" || items[4].Label != "会话" {
		t.Errorf("文件/视图/会话菜单标签不符：%q / %q / %q", items[1].Label, items[3].Label, items[4].Label)
	}

	appMenu := items[0].Submenu
	requireMenuItem(t, appMenu, "偏好设置…", "CmdOrCtrl+,")
	requireMenuRole(t, appMenu, mygo.RoleAbout)
	requireMenuRole(t, appMenu, mygo.RoleServices)
	requireMenuRole(t, appMenu, mygo.RoleHide)
	requireMenuRole(t, appMenu, mygo.RoleHideOthers)
	requireMenuRole(t, appMenu, mygo.RoleUnhide)
	requireMenuRole(t, appMenu, mygo.RoleQuit)

	fileMenu := items[1].Submenu
	requireMenuItem(t, fileMenu, "新建会话", "CmdOrCtrl+N")
	requireMenuItem(t, fileMenu, "在 Worktree 新建会话", "Alt+CmdOrCtrl+N")
	requireMenuItem(t, fileMenu, "添加项目…", "Shift+CmdOrCtrl+O")
	// ⌘W 改为普通菜单项：预览文件时先关预览，没有预览才关窗口，所以不能再设置 RoleClose。
	requireMenuItem(t, fileMenu, "关闭", "CmdOrCtrl+W")
	closeItem := fileMenu[len(fileMenu)-1]
	if closeItem.Label != "关闭" {
		t.Errorf("「文件」菜单末尾应为「关闭」，得到 %q", closeItem.Label)
	}
	if closeItem.Role != "" {
		t.Errorf("「关闭」不应设置 Role，得到 %q", closeItem.Role)
	}

	viewMenu := items[3].Submenu
	requireMenuItem(t, viewMenu, "显示/隐藏侧边栏", "CmdOrCtrl+B")
	requireMenuItem(t, viewMenu, "命令面板…", "CmdOrCtrl+K")
	requireMenuItem(t, viewMenu, "显示/隐藏终端", "CmdOrCtrl+J")
	requireMenuItem(t, viewMenu, "显示/隐藏改动", "CmdOrCtrl+D")
	requireMenuItem(t, viewMenu, "显示/隐藏网页预览", "Shift+CmdOrCtrl+B")
	requireMenuItem(t, viewMenu, "项目文档 (.mcode/context)", "Shift+CmdOrCtrl+K")
	requireMenuItem(t, viewMenu, "源码编辑器…", "Shift+CmdOrCtrl+E")
	requireMenuItem(t, viewMenu, "动态视图", "Alt+CmdOrCtrl+U")
	requireMenuRole(t, viewMenu, mygo.RoleToggleFullScreen)

	threadMenu := items[4].Submenu
	requireMenuItem(t, threadMenu, "停止当前回复", "CmdOrCtrl+.")
	requireMenuItem(t, threadMenu, "切换 Build/Plan 模式", "Shift+CmdOrCtrl+P")
	requireMenuItem(t, threadMenu, "上一会话", "Alt+CmdOrCtrl+Up")
	requireMenuItem(t, threadMenu, "下一会话", "Alt+CmdOrCtrl+Down")
	requireMenuItem(t, threadMenu, "运行项目脚本", "Alt+CmdOrCtrl+R")
	for i := 1; i <= 9; i++ {
		requireMenuItem(t, threadMenu,
			fmt.Sprintf("会话 %d", i),
			fmt.Sprintf("CmdOrCtrl+%d", i))
	}
	requireMenuItem(t, threadMenu, "休眠/重新激活会话", "Shift+CmdOrCtrl+S")
	requireMenuItem(t, threadMenu, "转发到新会话", "Shift+CmdOrCtrl+D")
	// 归档默认键位 ⌃⌘A（U-043）；非 macOS 上 Cmd 与 Ctrl 是同一修饰位，只显示 CmdOrCtrl。
	archiveAccel := "Ctrl+CmdOrCtrl+A"
	if ui.Cmd == ui.Ctrl {
		archiveAccel = "CmdOrCtrl+A"
	}
	requireMenuItem(t, threadMenu, "归档会话", archiveAccel)
}

// TestBuildAppMenuFollowsShortcutOverrides 覆盖 U-044：改键后菜单构建读取当前有效绑定，
// Accelerator 跟着变化（不再显示旧键位）。
func TestBuildAppMenuFollowsShortcutOverrides(t *testing.T) {
	setShortcutOverrides(map[string]string{
		"archive-thread": "Cmd+Shift+Y",
		"toggle-diff":    "Alt+Cmd+D",
		"open-settings":  "Cmd+Shift+Comma",
	})
	t.Cleanup(func() { setShortcutOverrides(nil) })

	items := BuildAppMenu().Items()
	requireMenuItem(t, items[0].Submenu, "偏好设置…", "Shift+CmdOrCtrl+,")
	requireMenuItem(t, items[3].Submenu, "显示/隐藏改动", "Alt+CmdOrCtrl+D")
	requireMenuItem(t, items[4].Submenu, "归档会话", "Shift+CmdOrCtrl+Y")

	// 未改键的菜单项保持默认键位。
	requireMenuItem(t, items[3].Submenu, "显示/隐藏侧边栏", "CmdOrCtrl+B")
}

// TestBuildWindowsAppMenu 验证 Windows 菜单：不含 macOS 专用角色，第一个菜单是文件且以退出收尾，
// 关于 MCode 放在最后的帮助菜单。
func TestBuildWindowsAppMenu(t *testing.T) {
	prev := appMenuGOOS
	appMenuGOOS = "windows"
	t.Cleanup(func() { appMenuGOOS = prev })

	items := BuildAppMenu().Items()
	if len(items) != 6 {
		t.Fatalf("Windows 顶级菜单应有 6 项，得到 %d", len(items))
	}
	if items[0].Label != "文件" {
		t.Errorf("第 1 个顶级菜单应为文件，得到 %q", items[0].Label)
	}
	if items[1].Role != mygo.RoleEditMenu {
		t.Errorf("第 2 个顶级菜单应为编辑菜单，得到 Role=%q", items[1].Role)
	}
	if items[2].Label != "视图" || items[3].Label != "会话" {
		t.Errorf("视图/会话菜单标签不符：%q / %q", items[2].Label, items[3].Label)
	}
	if items[4].Role != mygo.RoleWindowMenu {
		t.Errorf("第 5 个顶级菜单应为窗口菜单，得到 Role=%q", items[4].Role)
	}
	if items[5].Label != "帮助" {
		t.Errorf("第 6 个顶级菜单应为帮助，得到 %q", items[5].Label)
	}

	// Windows 上 mygo 会隐藏 macOS 专用角色，写进菜单只会留下空位。
	for _, role := range []mygo.MenuRole{
		mygo.RoleAppMenu, mygo.RoleAbout, mygo.RoleServices,
		mygo.RoleHide, mygo.RoleHideOthers, mygo.RoleUnhide, mygo.RoleHelp,
	} {
		forbidMenuRole(t, items, role)
	}

	fileMenu := items[0].Submenu
	requireMenuItem(t, fileMenu, "新建会话", "CmdOrCtrl+N")
	requireMenuItem(t, fileMenu, "在 Worktree 新建会话", "Alt+CmdOrCtrl+N")
	requireMenuItem(t, fileMenu, "添加项目…", "Shift+CmdOrCtrl+O")
	requireMenuItem(t, fileMenu, "偏好设置…", "CmdOrCtrl+,")
	requireMenuRole(t, fileMenu, mygo.RoleQuit)
	if last := fileMenu[len(fileMenu)-1]; last.Role != mygo.RoleQuit {
		t.Errorf("文件菜单最后一项应为退出，得到 Label=%q Role=%q", last.Label, last.Role)
	}

	// RoleAbout 在 Windows 上会被隐藏，关于项是普通菜单项。
	requireMenuItem(t, items[5].Submenu, "关于 MCode", "")

	for i, item := range items {
		if !item.IsVisible() {
			t.Errorf("第 %d 个顶级菜单不应被隐藏：%q", i+1, item.Label)
		}
	}
}

// forbidMenuRole 断言菜单（含子菜单）中不存在指定系统角色的菜单项。
func forbidMenuRole(t *testing.T, items []*mygo.MenuItem, role mygo.MenuRole) {
	t.Helper()
	for _, item := range items {
		if item.Role == role {
			t.Errorf("Windows 菜单不应包含角色为 %q 的菜单项", role)
		}
		forbidMenuRole(t, item.Submenu, role)
	}
}

// requireMenuItem 断言菜单中存在指定标签的动作项，且快捷键与点击回调均已绑定。
func requireMenuItem(t *testing.T, items []*mygo.MenuItem, label, accelerator string) {
	t.Helper()
	for _, item := range items {
		if item.Label != label {
			continue
		}
		if item.Accelerator != accelerator {
			t.Errorf("菜单项 %q 的快捷键为 %q，期望 %q", label, item.Accelerator, accelerator)
		}
		if item.Click == nil {
			t.Errorf("菜单项 %q 缺少点击回调", label)
		}
		return
	}
	t.Errorf("缺少菜单项 %q", label)
}

// requireMenuRole 断言菜单中存在指定系统角色的菜单项。
func requireMenuRole(t *testing.T, items []*mygo.MenuItem, role mygo.MenuRole) {
	t.Helper()
	for _, item := range items {
		if item.Role == role {
			return
		}
	}
	t.Errorf("缺少角色为 %q 的菜单项", role)
}
