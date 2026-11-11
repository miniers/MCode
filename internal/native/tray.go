package native

import (
	_ "embed"
	"fmt"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
)

// TrayOptions 配置系统托盘行为。
type TrayOptions struct {
	OnShowWindow      func()
	OnOpenSettings    func()
	OnCheckForUpdates func()
	// OnLeftClick 非 nil 时左键点击图标调用它，原生菜单改为右键弹出。
	OnLeftClick func()
	OnQuit      func()
}

// Tray 封装系统托盘/菜单栏图标及菜单。
type Tray struct {
	mu       sync.Mutex
	tray     *mygo.Tray
	opts     TrayOptions
	count    int
	menuless bool
}

// NewTray 创建菜单栏托盘图标。
// 图标为 macOS 模板图（IconIsTemplate）。
// 菜单项包含："显示窗口"、可选的"设置…"和"检查更新…"、禁用的状态项、分隔线、"退出 MCode"。
func NewTray(opts TrayOptions) (*Tray, error) {
	icon := trayIconPNG
	tip := "正在运行 0 个会话"
	menuless := opts.OnLeftClick != nil && runtime.GOOS != "linux"

	trayOpts := mygo.TrayOptions{
		Icon:           icon,
		IconIsTemplate: true,
		ToolTip:        tip,
	}
	if !menuless {
		trayOpts.Menu = makeTrayMenu(opts, 0)
	}

	rawTray, err := mygo.NewTray(trayOpts)
	if err != nil {
		return nil, err
	}

	t := &Tray{
		tray:     rawTray,
		opts:     opts,
		count:    0,
		menuless: menuless,
	}

	if menuless {
		rawTray.OnClick(opts.OnLeftClick)
		rawTray.OnRightClick(func() {
			mygo.RunOnMain(func() {
				t.mu.Lock()
				if t.tray == nil {
					t.mu.Unlock()
					return
				}
				menu := makeTrayMenu(t.opts, t.count)
				raw := t.tray
				t.mu.Unlock()

				raw.SetMenu(menu)
				defer raw.SetMenu(nil)
				raw.PopUpMenu()
			})
		})
	}

	return t, nil
}

// SetRunningCount 更新正在运行的会话计数。
// 可从任意 goroutine 调用，内部切到主线程更新 ToolTip，非 menuless 模式下同时重建菜单。
func (t *Tray) SetRunningCount(n int) {
	mygo.RunOnMain(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.tray == nil {
			return
		}
		t.count = n
		if !t.menuless {
			menu := makeTrayMenu(t.opts, n)
			t.tray.SetMenu(menu)
		}
		t.tray.SetToolTip(fmt.Sprintf("正在运行 %d 个会话", n))
	})
}

// SetTitle 设置托盘标题（文本）。
// 通过 mygo.RunOnMain 调用底层 SetTitle，t.tray 为 nil 时直接返回。
func (t *Tray) SetTitle(title string) {
	mygo.RunOnMain(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.tray == nil {
			return
		}
		t.tray.SetTitle(title)
	})
}

// Raw 返回底层托盘（加锁读取），供浮动面板锚定。
func (t *Tray) Raw() *mygo.Tray {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tray
}

// Destroy 销毁托盘图标。
func (t *Tray) Destroy() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tray != nil {
		t.tray.Destroy()
		t.tray = nil
	}
}

func makeTrayMenu(opts TrayOptions, n int) *mygo.Menu {
	items := []*mygo.MenuItem{
		{
			Label: "显示窗口",
			Click: func(item *mygo.MenuItem, win *mygo.Window) {
				if opts.OnShowWindow != nil {
					opts.OnShowWindow()
				}
			},
		},
	}
	if opts.OnOpenSettings != nil {
		items = append(items, &mygo.MenuItem{
			Label: "设置…",
			Click: func(item *mygo.MenuItem, win *mygo.Window) {
				opts.OnOpenSettings()
			},
		})
	}
	if opts.OnCheckForUpdates != nil {
		items = append(items, &mygo.MenuItem{
			Label: "检查更新…",
			Click: func(item *mygo.MenuItem, win *mygo.Window) {
				opts.OnCheckForUpdates()
			},
		})
	}
	items = append(items,
		&mygo.MenuItem{
			Label:    fmt.Sprintf("正在运行 %d 个会话", n),
			Disabled: true,
		},
		mygo.Separator(),
		&mygo.MenuItem{
			Label: "退出 MCode",
			Click: func(item *mygo.MenuItem, win *mygo.Window) {
				if opts.OnQuit != nil {
					opts.OnQuit()
				}
			},
		},
	)
	return mygo.NewMenu(items)
}

// MyGo 会把托盘图缩放到 18pt 高，因此用 36x36 的 @2x 图。
//
//go:embed trayicon.png
var trayIconPNG []byte
