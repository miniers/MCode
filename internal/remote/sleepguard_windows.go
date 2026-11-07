//go:build windows

package remote

import (
	"errors"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	// esContinuous 让执行状态设置持续生效，直到下一次显式修改。
	esContinuous = 0x80000000
	// esSystemRequired 阻止系统进入空闲休眠。
	esSystemRequired = 0x00000001
)

// setThreadExecutionState 对应 kernel32!SetThreadExecutionState（纯 Go 调用，不引入 cgo）。
var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// launchNoSleep 在锁定到独立 OS 线程的协程里声明「系统必须保持唤醒」。
// SetThreadExecutionState 是线程级状态，所以必须 LockOSThread 并在同一线程上释放；
// 参数 pid 仅为与 darwin 版签名一致，Windows 实现不使用。
func launchNoSleep(int) (func(), error) {
	if err := setThreadExecutionState.Find(); err != nil {
		return nil, err
	}

	ready := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if r1, _, callErr := setThreadExecutionState.Call(esContinuous | esSystemRequired); r1 == 0 {
			if callErr != nil {
				ready <- callErr
			} else {
				ready <- errors.New("remote: SetThreadExecutionState 调用失败")
			}
			return
		}
		ready <- nil

		<-release
		// ES_CONTINUOUS：恢复默认电源行为。
		_, _, _ = setThreadExecutionState.Call(esContinuous)
	}()

	if err := <-ready; err != nil {
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() { close(release) })
		<-done
	}, nil
}
