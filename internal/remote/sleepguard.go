package remote

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"mcode/internal/procattr"
)

// SleepGuard 在支持防休眠断言的平台（darwin / windows）上运行远程控制且有活跃会话时
// 阻止系统进入空闲休眠。遵循无 cgo 设计原则：darwin 管理 caffeinate 子进程，
// windows 通过 SetThreadExecutionState 在线程上持断言。
type SleepGuard struct {
	mu       sync.Mutex
	busy     func() bool
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	release  func() // 非 nil 表示当前持有防休眠断言

	// 以下字段仅供测试替换。
	goos   string
	launch func(pid int) (release func(), err error)
}

// launchCaffeinate 启动 caffeinate -i -w <pid>：-i 阻止空闲休眠，-w 绑定本进程，崩溃后断言随之释放。
func launchCaffeinate(pid int) (func(), error) {
	cmd := procattr.HideWindow(exec.Command("caffeinate", "-i", "-w", strconv.Itoa(pid)))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}, nil
}

// supportsSleepGuard 报告该平台是否有可用的防休眠实现。
func supportsSleepGuard(goos string) bool {
	return goos == "darwin" || goos == "windows"
}

// defaultLaunch 按平台选择防休眠实现：darwin 用 caffeinate，windows 用 SetThreadExecutionState。
func defaultLaunch(goos string) func(pid int) (release func(), err error) {
	switch goos {
	case "darwin":
		return launchCaffeinate
	case "windows":
		return launchNoSleep
	default:
		return func(int) (func(), error) {
			return nil, errors.New("remote: 当前平台没有防休眠实现")
		}
	}
}

// NewSleepGuard 创建并返回 SleepGuard 实例。
// busy 函数返回当前是否有正在运行的会话任务；interval 为轮询检查间隔（默认 20 秒）。
func NewSleepGuard(busy func() bool, interval time.Duration) *SleepGuard {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	goos := runtime.GOOS
	return &SleepGuard{
		busy:     busy,
		interval: interval,
		goos:     goos,
		launch:   defaultLaunch(goos),
	}
}

// Start 启动后台轮询协程。在不支持防休眠的平台上直接返回，不执行任何操作。
func (g *SleepGuard) Start() {
	if !supportsSleepGuard(g.goos) {
		return
	}

	g.mu.Lock()
	if g.stopCh != nil {
		g.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	g.stopCh = stopCh
	g.doneCh = doneCh
	g.mu.Unlock()

	go func() {
		defer close(doneCh)
		// 启动后在后台协程中先立即执行一次检查
		g.refresh()

		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				g.refresh()
			}
		}
	}()
}

// Stop 停止后台轮询协程并确保释放已持有的防休眠断言。不支持防休眠的平台直接返回。
func (g *SleepGuard) Stop() {
	if !supportsSleepGuard(g.goos) {
		return
	}

	g.mu.Lock()
	stopCh := g.stopCh
	doneCh := g.doneCh
	g.stopCh = nil
	g.doneCh = nil
	g.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
		<-doneCh
	}

	g.mu.Lock()
	g.releaseLocked()
	g.mu.Unlock()
}

func (g *SleepGuard) refresh() {
	var isBusy bool
	if g.busy != nil {
		isBusy = g.busy()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if isBusy {
		if g.release == nil {
			if release, err := g.launch(os.Getpid()); err == nil {
				g.release = release
			}
		}
	} else {
		g.releaseLocked()
	}
}

func (g *SleepGuard) releaseLocked() {
	if g.release != nil {
		g.release()
		g.release = nil
	}
}
