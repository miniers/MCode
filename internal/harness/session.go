package harness

// SessionChecker 由 Harness 可选实现：判断某个会话的上下文是否还在。
// 返回 false 表示会话已丢失，remote 会把线程的可见历史交接进下一轮提示词。
type SessionChecker interface {
	SessionExists(sessionID string) bool
}
