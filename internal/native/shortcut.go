package native

import (
	"github.com/egoist/mygo"
)

// RegisterGlobalShortcut 注册系统全局快捷键。
// 当 accelerator 为空时直接返回空操作的 unregister 函数与 nil 错误；
// 内部调用 mygo.GlobalShortcut.Register 与 mygo.GlobalShortcut.Unregister。
func RegisterGlobalShortcut(accelerator string, fn func()) (unregister func(), err error) {
	if accelerator == "" {
		return func() {}, nil
	}
	if err := mygo.GlobalShortcut.Register(accelerator, fn); err != nil {
		return nil, err
	}
	return func() {
		mygo.GlobalShortcut.Unregister(accelerator)
	}, nil
}
