//go:build !darwin

package git

import "errors"

// newPlatformWatcher 在非 darwin 平台不可用：返回错误让 NewWatcher 退回轮询实现。
func newPlatformWatcher(root string) (Watcher, error) {
	return nil, errors.New("当前平台不支持 FSEvents 工作区监听")
}
