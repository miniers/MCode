package remote

import (
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"mcode/internal/remote/wire"
)

const (
	// maxLogHubThreadBytes 是单个线程日志环缓冲的总文本字节上限（256 KiB）。
	maxLogHubThreadBytes = 256 * 1024
	// maxLogHubThreadChunks 是单个线程日志环缓冲保留的最大 chunk 条数。
	maxLogHubThreadChunks = 500
	// maxLogHubStreams 是单个线程保留的最近日志流元信息条数上限。
	maxLogHubStreams = 20
	// logHubFlushInterval 是日志增量批量推送的节流窗口。
	logHubFlushInterval = 50 * time.Millisecond
	// logHubFlushBytesThreshold 是累计待发日志字节达到该阈值时立即 flush 的水位（4 KiB）。
	logHubFlushBytesThreshold = 4 * 1024
	// maxLogHubEventPayloadBytes 是单次 logs.event 推送载荷字节上限（64 KiB），避免撑爆连接 sendCh。
	maxLogHubEventPayloadBytes = 64 * 1024
)

type logThreadState struct {
	seq           int
	chunkSeq      int
	streams       []wire.RemoteLogStreamInfo
	chunks        []wire.RemoteLogChunk
	chunkBytes    int
	truncated     bool
	subscribers   map[Conn]struct{}
	pendingStream *wire.RemoteLogStreamInfo
	pendingChunks []wire.RemoteLogChunk
	pendingBytes  int
	timer         *time.Timer
}

// LogStreamHub 管理各会话的工具/脚本实时日志环缓冲、订阅连接与节流推送。
type LogStreamHub struct {
	mu      sync.Mutex
	threads map[string]*logThreadState
	closed  bool
	now     func() time.Time
}

// NewLogStreamHub 构建并初始化一个 LogStreamHub。
func NewLogStreamHub() *LogStreamHub {
	return &LogStreamHub{
		threads: make(map[string]*logThreadState),
		now:     time.Now,
	}
}

func (h *LogStreamHub) getOrCreateThreadLocked(threadID string) *logThreadState {
	st, ok := h.threads[threadID]
	if !ok {
		st = &logThreadState{
			subscribers: make(map[Conn]struct{}),
		}
		h.threads[threadID] = st
	}
	return st
}

func (h *LogStreamHub) upsertStreamLocked(st *logThreadState, info wire.RemoteLogStreamInfo) wire.RemoteLogStreamInfo {
	for i := range st.streams {
		if st.streams[i].StreamID == info.StreamID {
			if info.StartedAt == "" {
				info.StartedAt = st.streams[i].StartedAt
			}
			if info.Title == "" {
				info.Title = st.streams[i].Title
			}
			if info.Source == "" {
				info.Source = st.streams[i].Source
			}
			st.streams[i] = info
			return st.streams[i]
		}
	}
	st.streams = append(st.streams, info)
	for len(st.streams) > maxLogHubStreams {
		evictIdx := 0
		for i := range st.streams {
			if st.streams[i].Status != "running" {
				evictIdx = i
				break
			}
		}
		st.streams = append(st.streams[:evictIdx], st.streams[evictIdx+1:]...)
	}
	return info
}

// Start 记录一条新的工具或脚本日志流开始；若已有订阅者则立即推送 logs.event。
func (h *LogStreamHub) Start(threadID, streamID, source, title string) {
	if threadID == "" || streamID == "" {
		return
	}
	if source == "" {
		source = "tool"
	}
	title = RedactSecrets(title)

	var (
		params      wire.RemoteLogsEventNotificationParams
		subscribers []Conn
		shouldSend  bool
	)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	st := h.getOrCreateThreadLocked(threadID)
	nowStr := h.now().UTC().Format(time.RFC3339)
	info := h.upsertStreamLocked(st, wire.RemoteLogStreamInfo{
		StreamID:  streamID,
		Source:    source,
		Title:     title,
		Status:    "running",
		StartedAt: nowStr,
	})
	if len(st.subscribers) == 0 {
		st.seq++
		h.mu.Unlock()
		return
	}
	streamCopy := info
	st.pendingStream = &streamCopy
	params, subscribers, shouldSend = h.takePendingLocked(threadID, st)
	h.mu.Unlock()

	if shouldSend {
		h.sendEvent(subscribers, params)
	}
}

// Append 追加一条日志增量（自动脱敏）；若存在订阅者则按 50ms/4KB 规则节流推送。
func (h *LogStreamHub) Append(threadID, streamID, source, title, text string) {
	if threadID == "" || streamID == "" || text == "" {
		return
	}
	if source == "" {
		source = "tool"
	}
	title = RedactSecrets(title)
	text = RedactSecrets(text)
	if text == "" {
		return
	}

	var (
		params      wire.RemoteLogsEventNotificationParams
		subscribers []Conn
		shouldSend  bool
	)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	st := h.getOrCreateThreadLocked(threadID)
	now := h.now().UTC()
	nowStr := now.Format(time.RFC3339)

	found := false
	for i := range st.streams {
		if st.streams[i].StreamID == streamID {
			if title != "" && st.streams[i].Title == "" {
				st.streams[i].Title = title
			}
			found = true
			break
		}
	}
	if !found {
		h.upsertStreamLocked(st, wire.RemoteLogStreamInfo{
			StreamID:  streamID,
			Source:    source,
			Title:     title,
			Status:    "running",
			StartedAt: nowStr,
		})
	}

	if len(text) > maxLogHubThreadBytes {
		text = utf8Tail(text, maxLogHubThreadBytes)
		st.truncated = true
	}

	st.chunkSeq++
	chunk := wire.RemoteLogChunk{
		Seq:       st.chunkSeq,
		StreamID:  streamID,
		Source:    source,
		Text:      text,
		Timestamp: nowStr,
	}
	st.chunks = append(st.chunks, chunk)
	st.chunkBytes += len(chunk.Text)
	for len(st.chunks) > maxLogHubThreadChunks || st.chunkBytes > maxLogHubThreadBytes {
		st.chunkBytes -= len(st.chunks[0].Text)
		st.chunks = st.chunks[1:]
		st.truncated = true
	}

	if len(st.subscribers) == 0 {
		st.seq++
		h.mu.Unlock()
		return
	}

	st.pendingChunks = append(st.pendingChunks, chunk)
	st.pendingBytes += len(chunk.Text)

	if st.pendingBytes >= logHubFlushBytesThreshold {
		params, subscribers, shouldSend = h.takePendingLocked(threadID, st)
		h.mu.Unlock()
		if shouldSend {
			h.sendEvent(subscribers, params)
		}
		return
	}

	if st.timer == nil {
		st.timer = time.AfterFunc(logHubFlushInterval, func() {
			h.flush(threadID)
		})
	}
	h.mu.Unlock()
}

// Finish 标记指定日志流结束（"done" 或 "error"）；若有订阅者则立即 flush 增量与终态。
func (h *LogStreamHub) Finish(threadID, streamID, status string) {
	if threadID == "" || streamID == "" {
		return
	}
	if status == "" {
		status = "done"
	}

	var (
		params      wire.RemoteLogsEventNotificationParams
		subscribers []Conn
		shouldSend  bool
	)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	st, ok := h.threads[threadID]
	if !ok {
		h.mu.Unlock()
		return
	}
	idx := -1
	for i := range st.streams {
		if st.streams[i].StreamID == streamID {
			idx = i
			break
		}
	}
	if idx < 0 {
		h.mu.Unlock()
		return
	}
	st.streams[idx].Status = status
	st.streams[idx].FinishedAt = h.now().UTC().Format(time.RFC3339)

	if len(st.subscribers) == 0 {
		st.seq++
		h.mu.Unlock()
		return
	}
	streamCopy := st.streams[idx]
	st.pendingStream = &streamCopy
	params, subscribers, shouldSend = h.takePendingLocked(threadID, st)
	h.mu.Unlock()

	if shouldSend {
		h.sendEvent(subscribers, params)
	}
}

// Subscribe 登记连接订阅指定会话的实时日志，并返回当前流与环缓冲快照。
func (h *LogStreamHub) Subscribe(c Conn, threadID string) wire.RemoteLogsSubscribeResult {
	h.mu.Lock()
	defer h.mu.Unlock()

	st := h.getOrCreateThreadLocked(threadID)
	if !h.closed && c != nil {
		if st.subscribers == nil {
			st.subscribers = make(map[Conn]struct{})
		}
		st.subscribers[c] = struct{}{}
	}

	streams := make([]wire.RemoteLogStreamInfo, len(st.streams))
	copy(streams, st.streams)
	chunks := make([]wire.RemoteLogChunk, len(st.chunks))
	copy(chunks, st.chunks)

	return wire.RemoteLogsSubscribeResult{
		ThreadID:  threadID,
		Sequence:  st.seq,
		Streams:   streams,
		Chunks:    chunks,
		Truncated: st.truncated,
	}
}

// Unsubscribe 取消连接对指定会话实时日志的订阅；无剩余订阅者时停止未触发定时器。
func (h *LogStreamHub) Unsubscribe(c Conn, threadID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st, ok := h.threads[threadID]
	if !ok || c == nil {
		return
	}
	delete(st.subscribers, c)
	if len(st.subscribers) == 0 {
		clearPendingLogLocked(st)
	}
}

// RemoveConn 从所有会话的日志订阅集合中移除指定连接。
func (h *LogStreamHub) RemoveConn(c Conn) {
	if c == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, st := range h.threads {
		delete(st.subscribers, c)
		if len(st.subscribers) == 0 {
			clearPendingLogLocked(st)
		}
	}
}

// Close 停止所有未触发定时器并清空订阅集合。
func (h *LogStreamHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true
	for _, st := range h.threads {
		clearPendingLogLocked(st)
		st.subscribers = nil
	}
}

func clearPendingLogLocked(st *logThreadState) {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.pendingStream = nil
	st.pendingChunks = nil
	st.pendingBytes = 0
}

func (h *LogStreamHub) takePendingLocked(threadID string, st *logThreadState) (wire.RemoteLogsEventNotificationParams, []Conn, bool) {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	if st.pendingStream == nil && len(st.pendingChunks) == 0 {
		return wire.RemoteLogsEventNotificationParams{}, nil, false
	}
	if len(st.subscribers) == 0 {
		st.pendingStream = nil
		st.pendingChunks = nil
		st.pendingBytes = 0
		return wire.RemoteLogsEventNotificationParams{}, nil, false
	}

	st.seq++
	chunks := st.pendingChunks
	if chunks == nil {
		chunks = []wire.RemoteLogChunk{}
	}
	params := wire.RemoteLogsEventNotificationParams{
		ThreadID: threadID,
		Sequence: st.seq,
		Stream:   st.pendingStream,
		Chunks:   chunks,
	}

	st.pendingStream = nil
	st.pendingChunks = nil
	st.pendingBytes = 0

	subscribers := make([]Conn, 0, len(st.subscribers))
	for c := range st.subscribers {
		subscribers = append(subscribers, c)
	}
	return params, subscribers, true
}

func (h *LogStreamHub) flush(threadID string) {
	var (
		params      wire.RemoteLogsEventNotificationParams
		subscribers []Conn
		shouldSend  bool
	)

	h.mu.Lock()
	if !h.closed {
		if st, ok := h.threads[threadID]; ok {
			params, subscribers, shouldSend = h.takePendingLocked(threadID, st)
		}
	}
	h.mu.Unlock()

	if shouldSend {
		h.sendEvent(subscribers, params)
	}
}

func (h *LogStreamHub) sendEvent(subscribers []Conn, params wire.RemoteLogsEventNotificationParams) {
	capLogEventPayload(&params)

	var failed []Conn
	for _, c := range subscribers {
		if err := c.Notify("logs.event", params); err != nil {
			failed = append(failed, c)
		}
	}
	for _, c := range failed {
		h.RemoveConn(c)
	}
}

// capLogEventPayload 把单次 logs.event 推送载荷限制在 64 KiB 以内（丢弃前序旧片段或保留末尾合法 UTF-8 内容）。
func capLogEventPayload(params *wire.RemoteLogsEventNotificationParams) {
	if params.Chunks == nil {
		params.Chunks = []wire.RemoteLogChunk{}
	}
	raw, err := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "logs.event", Params: params})
	if err == nil && len(raw) <= maxLogHubEventPayloadBytes {
		return
	}

	for len(params.Chunks) > 1 {
		params.Chunks = params.Chunks[1:]
		raw, err = json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "logs.event", Params: params})
		if err == nil && len(raw) <= maxLogHubEventPayloadBytes {
			return
		}
	}

	if len(params.Chunks) == 1 {
		for len(params.Chunks[0].Text) > 0 {
			excess := len(raw) - maxLogHubEventPayloadBytes
			if excess <= 0 {
				break
			}
			keep := len(params.Chunks[0].Text) - excess - 256
			if keep <= 0 {
				params.Chunks[0].Text = ""
				break
			}
			params.Chunks[0].Text = utf8Tail(params.Chunks[0].Text, keep)
			raw, err = json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "logs.event", Params: params})
			if err == nil && len(raw) <= maxLogHubEventPayloadBytes {
				return
			}
		}
	}

	if params.Stream != nil && len(raw) > maxLogHubEventPayloadBytes {
		params.Stream.Title = utf8Head(params.Stream.Title, 512)
	}
}

// utf8Tail 截取字符串末尾不超过 maxBytes 的部分，并前移起点至合法 UTF-8 字符边界。
func utf8Tail(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// utf8Head 截取字符串开头不超过 maxBytes 的合法 UTF-8 前缀。
func utf8Head(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
