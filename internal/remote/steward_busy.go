package remote

// SetStewardBusy 发布前台会话的处理状态。
func (b *MemoryBackend) SetStewardBusy(threadID string, busy bool) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || !b.stewardLocked(td) {
		b.mu.Unlock()
		return
	}
	if td.stewardBusy == busy {
		b.mu.Unlock()
		return
	}
	td.stewardBusy = busy
	summary := b.summaryForPublishLocked(td)
	td.summary.IsRunning = summary.IsRunning
	hub := b.hub
	b.mu.Unlock()
	if hub != nil {
		hub.Publish(threadID, nil, nil, summary)
	}
}
