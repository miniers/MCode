package app

import (
	mcodeui "mcode/internal/ui"
)

// Client 实现界面的前台会话上报接口：界面把「用户当前正在看的会话」写进来，
// 总管之类的后台逻辑读取它，判断该会话是否正被人看着、暂不打扰。
var _ mcodeui.ForegroundThreadReporter = (*Client)(nil)

// SetForegroundThread 记录界面当前正在看的会话；空串表示用户没有在看任何会话
// （窗口失焦、隐藏，或没选中会话）。可能来自界面的任意 goroutine。
func (c *Client) SetForegroundThread(threadID string) {
	c.foregroundMu.Lock()
	defer c.foregroundMu.Unlock()
	c.foregroundThread = threadID
}

// ForegroundThread 返回最近一次上报的前台会话 ID，空串表示没有或还没上报过。
func (c *Client) ForegroundThread() string {
	c.foregroundMu.Lock()
	defer c.foregroundMu.Unlock()
	return c.foregroundThread
}
