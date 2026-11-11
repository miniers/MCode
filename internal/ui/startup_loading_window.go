package mcodeui

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 启动时的加载小窗：首次升级会整理数据库（几秒到几十秒），主窗口出来之前先给用户
// 一个带提示文字与进度的窗口，整理完由调用方关闭。
const (
	startupLoadingWindowWidth  = 360
	startupLoadingWindowHeight = 150
	// startupCardPaddingX 是启动小窗卡片的横向内边距。
	startupCardPaddingX float32 = 28
)

// StartupLoadingWindow 是启动加载窗口的句柄。进度由 app.Open 所在的后台 goroutine
// 写入，因此状态用 mu 保护，重绘通过 ui.Services 请求（Invalidate 允许跨 goroutine 调用）。
type StartupLoadingWindow struct {
	mu       sync.Mutex
	win      *mygo.Window
	services ui.Services
	done     int64
	total    int64
	closed   bool
}

// OpenStartupLoadingWindow 在主线程创建并显示加载窗口，返回可跨 goroutine 更新进度、
// 关闭窗口的句柄。窗口尺寸固定、居中显示（X、Y 为 0 时 mygo 自动居中）。
func OpenStartupLoadingWindow() *StartupLoadingWindow {
	w := &StartupLoadingWindow{}
	mygo.RunOnMain(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "MCode",
			Width:           startupLoadingWindowWidth,
			Height:          startupLoadingWindowHeight,
			DisableResize:   true,
			DisableMaximize: true,
			Content:         ui.View(func(c *ui.Context) { w.render(c) }),
		})
		w.mu.Lock()
		w.win = win
		w.mu.Unlock()
		win.OnClosed(func() {
			w.mu.Lock()
			w.closed = true
			if w.win == win {
				w.win = nil
			}
			w.mu.Unlock()
		})
	})
	return w
}

// SetProgress 记录最新进度并请求重绘；任意 goroutine 都可以调用。
func (w *StartupLoadingWindow) SetProgress(done, total int64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.done, w.total = done, total
	services, closed := w.services, w.closed
	w.mu.Unlock()
	if closed || services == (ui.Services{}) {
		return
	}
	services.Invalidate()
}

// Close 关闭加载窗口；窗口已经关掉或还没创建时什么也不做。任意 goroutine 都可以调用
// （mygo.Window.Close 内部会转到主线程）。
func (w *StartupLoadingWindow) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	win := w.win
	w.win = nil
	w.mu.Unlock()
	if win != nil {
		win.Close()
	}
}

// render 是窗口每帧的内容：记下本窗口的 ui.Services 供 SetProgress 请求重绘，并按最新进度渲染。
func (w *StartupLoadingWindow) render(c *ui.Context) {
	applyAppTheme(c)
	w.mu.Lock()
	if w.services == (ui.Services{}) {
		w.services = c.Services()
	}
	done, total := w.done, w.total
	w.mu.Unlock()

	renderStartupLoading(c, done, total)
}

// startupMB 把字节换成 MB 并四舍五入到整数：进度文字里的用量只保留到 MB。
func startupMB(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	const mb = int64(1024 * 1024)
	return (bytes + mb/2) / mb
}

// startupProgressLabel 生成进度条下面一行的文字：total>0 时显示百分比与已用/总量，
// 拿不到总量（total<=0）时只提示稍候。
func startupProgressLabel(done, total int64) string {
	if total <= 0 {
		return "请稍候…"
	}
	if done < 0 {
		done = 0
	}
	percent := int(float64(done) / float64(total) * 100)
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("已完成 %d%%（%d MB / %d MB）", percent, startupMB(done), startupMB(total))
}

// renderStartupLoading 渲染加载窗口内容：主文字、副文字、进度条与进度文字。
// 抽成不依赖窗口对象的函数，方便测试直接构建内容树。
func renderStartupLoading(c *ui.Context, done, total int64) {
	// total 未知时进度条保持空：不确定状态靠「请稍候…」说明，不做动画，避免额外定时器。
	ratio := float32(0)
	if total > 0 && done > 0 {
		ratio = float32(float64(done) / float64(total))
		if ratio > 1 {
			ratio = 1
		}
	}

	ui.Column(c.Key("startup-loading-root")).
		Fill().
		Background(ThemeWindowBg(c)).
		Padding(Space6, startupCardPaddingX).
		Justify(ui.Center).
		Children(func() {
			ui.Column(c.Key("startup-loading-text")).
				FillWidth().
				Gap(Space1).
				Children(func() {
					ui.Text(c, "正在整理数据库…").FontSize(uiFont(TypeProse)).FontWeight(600).TextColor(ThemePrimaryText(c))
					ui.Text(c, "仅首次升级时进行一次，请勿关闭").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				})

			// 进度条：高 6、圆角 3，轨道是低透明度主文本色，填充用强调色。
			ProgressBar(c, ProgressBarProps{
				Key: "startup-loading-bar", Label: "启动进度", Ratio: ratio, Height: Space2,
				Track: ThemeOverlay(c, 0.1), Fill: ThemeAccent(c),
			}).MarginY(12).Clip()

			ui.Text(c, startupProgressLabel(done, total)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
		})
}
