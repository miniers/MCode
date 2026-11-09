package remote

// stewardBusyTurn 表示专用前台正在处理消息，不对应普通执行轮次。
const stewardBusyTurn = "steward:frontdesk"

// SetStewardBusy 发布前台会话的处理状态。
func (b *MemoryBackend) SetStewardBusy(threadID string, busy bool) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || !b.stewardLocked(td) {
		b.mu.Unlock()
		return
	}
	if busy {
		if td.runTurnID != "" || td.runCancel != nil {
			b.mu.Unlock()
			return
		}
		td.runTurnID = stewardBusyTurn
	} else if td.runTurnID == stewardBusyTurn {
		td.runTurnID = ""
	} else {
		b.mu.Unlock()
		return
	}
	td.summary.IsRunning = busy
	summary := b.summaryForPublishLocked(td)
	hub := b.hub
	b.mu.Unlock()
	if hub != nil {
		hub.Publish(threadID, nil, nil, summary)
	}
}
