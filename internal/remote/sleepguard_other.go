//go:build !windows

package remote

import "errors"

// launchNoSleep 只在 windows 上实现；其他平台由 launchCaffeinate 或空实现负责，
// 这里保留同名桩函数，保证 darwin 与 linux 都能编译。
func launchNoSleep(int) (func(), error) {
	return nil, errors.New("remote: 当前平台没有防休眠实现")
}
