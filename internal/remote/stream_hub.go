package remote

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

// maxThreadEventFrameBytes 是单帧上限，单帧超过 512KB 时改发 resync。
const maxThreadEventFrameBytes = 512 * 1024

// StreamHubOptions 配置事件发布中心的聚合参数。
type StreamHubOptions struct {
	Interval time.Duration
}

type threadState struct {
	runID       string
	sequence    int
	subscribers map[Conn]struct{}

	// 待发缓冲状态
	hasPending    bool
	msgOrder      []string
	messages      map[string]wire.ThreadMessage
	removedOrder  []string
	removedSet    map[string]struct{}
	pendingThread wire.ThreadSummary

	// timer/observed* 记录送达与合并状态；observedSwarmBusy 让 SwarmBusy 的切换也立即送达观察者，
	// observedBackgroundTasks 让后台任务计数在 0 与非 0 之间切换时立即送达，侧边栏的「后台」标记才能跟上。
	timer                   *time.Timer
	observed                bool
	observedRunning         bool
	observedSwarmBusy       bool
	observedBackgroundTasks int
	observedSummary         wire.ThreadSummary
}

// threadSummaryEqual 判断两份线程摘要是否等价。ThreadSummary 含切片字段（MCPDisabled）
// 与多个指针字段（Model、Effort、TaskID 等），结构体已不能直接用 == 比较，这里用
// reflect.DeepEqual 逐字段比较；指针按指向的值比较，即两份摘要语义相同就判为相等。
func threadSummaryEqual(a, b wire.ThreadSummary) bool {
	return reflect.DeepEqual(a, b)
}

func (s *threadState) merge(messages []wire.ThreadMessage, removedIDs []string, thread wire.ThreadSummary) {
	if s.messages == nil {
		s.messages = make(map[string]wire.ThreadMessage)
	}
	if s.removedSet == nil {
		s.removedSet = make(map[string]struct{})
	}

	for _, m := range messages {
		// 先 remove 后 upsert：从 removed 中删除
		if _, ok := s.removedSet[m.ID]; ok {
			delete(s.removedSet, m.ID)
			for i, id := range s.removedOrder {
				if id == m.ID {
					s.removedOrder = append(s.removedOrder[:i], s.removedOrder[i+1:]...)
					break
				}
			}
		}
		// 后到的覆盖先到的，保留首次出现的顺序
		if _, ok := s.messages[m.ID]; !ok {
			s.msgOrder = append(s.msgOrder, m.ID)
		}
		s.messages[m.ID] = m
	}

	for _, id := range removedIDs {
		// 先 upsert 后 remove：从 messages 中删除，保留在 removed 中
		if _, ok := s.messages[id]; ok {
			delete(s.messages, id)
			for i, mid := range s.msgOrder {
				if mid == id {
					s.msgOrder = append(s.msgOrder[:i], s.msgOrder[i+1:]...)
					break
				}
			}
		}
		// 去重加入 removedOrder
		if _, ok := s.removedSet[id]; !ok {
			s.removedSet[id] = struct{}{}
			s.removedOrder = append(s.removedOrder, id)
		}
	}

	s.pendingThread = thread
	s.hasPending = true
}

func (s *threadState) clearBuffer() {
	s.hasPending = false
	s.msgOrder = nil
	s.messages = nil
	s.removedOrder = nil
	s.removedSet = nil
	s.pendingThread = wire.ThreadSummary{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

type hubObserverItem struct {
	id int
	fn func(threadID, method string, params any)
}

// StreamHub 管理所有会话的实时流式事件发布、聚合与订阅分发。
type StreamHub struct {
	mu        sync.Mutex
	interval  time.Duration
	threads   map[string]*threadState
	closed    bool
	observer  func(threadID, method string, params any)
	observers []hubObserverItem
	nextObsID int
}

// AddObserver 注册一个事件观察者，返回注销函数。
func (h *StreamHub) AddObserver(fn func(threadID, method string, params any)) (remove func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.nextObsID
	h.nextObsID++
	h.observers = append(h.observers, hubObserverItem{id: id, fn: fn})

	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for i, item := range h.observers {
			if item.id == id {
				h.observers = append(h.observers[:i], h.observers[i+1:]...)
				break
			}
		}
	}
}

// SetObserver 设置事件中心的全局观察者回调函数。
func (h *StreamHub) SetObserver(fn func(threadID, method string, params any)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observer = fn
}

// NewStreamHub 构建并初始化 StreamHub。
func NewStreamHub(opts StreamHubOptions) *StreamHub {
	interval := opts.Interval
	if interval <= 0 {
		interval = 16 * time.Millisecond
	}
	return &StreamHub{
		interval: interval,
		threads:  make(map[string]*threadState),
	}
}

func (h *StreamHub) getOrCreateThreadLocked(threadID string) *threadState {
	st, ok := h.threads[threadID]
	if !ok {
		st = &threadState{
			subscribers: make(map[Conn]struct{}),
		}
		h.threads[threadID] = st
	}
	return st
}

// Subscribe 登记连接对指定会话的订阅，并在同一把锁内返回当前水位。
// 若会话从没开始过 run，返回空 runID 与 0。
func (h *StreamHub) Subscribe(c Conn, threadID string) (runID string, sequence int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st := h.getOrCreateThreadLocked(threadID)
	st.subscribers[c] = struct{}{}
	return st.runID, st.sequence
}

// Unsubscribe 取消连接对指定会话的订阅。
func (h *StreamHub) Unsubscribe(c Conn, threadID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if st, ok := h.threads[threadID]; ok {
		delete(st.subscribers, c)
	}
}

// IsSubscribed 返回连接当前是否订阅了至少一个会话。
func (h *StreamHub) IsSubscribed(c Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, st := range h.threads {
		if _, ok := st.subscribers[c]; ok {
			return true
		}
	}
	return false
}

// RemoveConn 从所有会话中移除该连接。
func (h *StreamHub) RemoveConn(c Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, st := range h.threads {
		delete(st.subscribers, c)
	}
}

// BeginRun 生成新 runID（UUID v4 格式），把 sequence 置 0，丢弃旧 run 未发出的缓冲，并返回新 runID。
func (h *StreamHub) BeginRun(threadID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.beginRunLocked(threadID)
}

func (h *StreamHub) beginRunLocked(threadID string) string {
	st := h.getOrCreateThreadLocked(threadID)
	st.clearBuffer()
	st.runID = newUUID()
	st.sequence = 0
	return st.runID
}

// Publish 并入待发缓冲。缓冲从空变为非空时，用 time.AfterFunc 安排一次聚合 flush。
// 若会话还没有 runID，先隐式调用 BeginRun。
func (h *StreamHub) Publish(threadID string, messages []wire.ThreadMessage, removedIDs []string, thread wire.ThreadSummary) {
	var observers []func(string, string, any)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}

	st := h.getOrCreateThreadLocked(threadID)
	if st.runID == "" {
		h.beginRunLocked(threadID)
	}

	wasPending := st.hasPending
	st.merge(messages, removedIDs, thread)
	// 生命周期切换立即送达，只有同一运行态内的文本增量按帧合并。
	// SwarmBusy 的切换（如子线程全部落定）同样立即送达，否则提醒检测会错过
	// “主线程空闲且名下不再有子线程活动”的那一刻。
	// 后台任务计数只在 0 与非 0 之间切换时立即送达；同一区间内的计数变化只影响行尾文案，按帧合并即可。
	backgroundTasksToggle := (st.observedBackgroundTasks == 0) != (thread.BackgroundTasks == 0)
	if !st.observed || st.observedRunning != thread.IsRunning || st.observedSwarmBusy != thread.SwarmBusy || backgroundTasksToggle {
		st.observed, st.observedRunning, st.observedSwarmBusy = true, thread.IsRunning, thread.SwarmBusy
		st.observedBackgroundTasks = thread.BackgroundTasks
		st.observedSummary = thread
		if h.observer != nil {
			observers = append(observers, h.observer)
		}
		for _, observer := range h.observers {
			observers = append(observers, observer.fn)
		}
	}

	if !wasPending && !h.closed {
		st.timer = time.AfterFunc(h.interval, func() {
			h.Flush(threadID)
		})
	}
	h.mu.Unlock()
	for _, observer := range observers {
		observer(threadID, "thread.summary", thread)
	}
}

// Flush 同步触发指定会话的事件发送。
// 若缓冲为空则无动作；否则 sequence 加 1，组装 projection 事件并下发给所有订阅者。
func (h *StreamHub) Flush(threadID string) {
	var (
		ev           wire.ThreadEvent
		subscribers  []Conn
		shouldNotify bool
		observers    []func(string, string, any)
	)

	h.mu.Lock()
	st, ok := h.threads[threadID]
	if ok && st.hasPending {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		st.sequence++

		msgs := make([]wire.ThreadMessage, 0, len(st.msgOrder))
		for _, id := range st.msgOrder {
			msgs = append(msgs, st.messages[id])
		}

		removed := make([]string, len(st.removedOrder))
		copy(removed, st.removedOrder)

		ev = wire.ThreadEvent{
			ThreadID: threadID,
			RunID:    st.runID,
			Sequence: st.sequence,
			Kind:     "projection",
			Payload: wire.ThreadEventPayload{
				Messages:          msgs,
				RemovedMessageIDs: removed,
				Thread:            st.pendingThread,
			},
		}

		// 清空待发缓冲
		st.hasPending = false
		st.msgOrder = nil
		st.messages = nil
		st.removedOrder = nil
		st.removedSet = nil
		st.pendingThread = wire.ThreadSummary{}

		// 拷贝订阅者列表
		subscribers = make([]Conn, 0, len(st.subscribers))
		for c := range st.subscribers {
			subscribers = append(subscribers, c)
		}
		shouldNotify = true
		if !threadSummaryEqual(st.observedSummary, ev.Payload.Thread) {
			st.observedSummary = ev.Payload.Thread
			if h.observer != nil {
				observers = append(observers, h.observer)
			}
			for _, observer := range h.observers {
				observers = append(observers, observer.fn)
			}
		}
	}
	h.mu.Unlock()

	if !shouldNotify {
		return
	}
	for _, observer := range observers {
		observer(threadID, "thread.summary", ev.Payload.Thread)
	}

	frameBytes, err := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "threads.event", Params: ev})
	if err == nil && len(frameBytes) > maxThreadEventFrameBytes {
		ev = wire.ThreadEvent{
			ThreadID: ev.ThreadID,
			RunID:    ev.RunID,
			Sequence: ev.Sequence,
			Kind:     "resync",
			Payload: wire.ThreadEventPayload{
				Thread: ev.Payload.Thread,
			},
		}
		frameBytes, err = json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "threads.event", Params: ev})
	}
	if err != nil || len(frameBytes) > maxThreadEventFrameBytes {
		// 摘要本身也超限时通过断连强制重新同步，绝不发送超大帧。
		for _, c := range subscribers {
			_ = c.Close()
			h.RemoveConn(c)
		}
		return
	}

	var failed []Conn
	for _, c := range subscribers {
		if err := c.Notify("threads.event", ev); err != nil {
			failed = append(failed, c)
		}
	}

	if len(failed) > 0 {
		for _, c := range failed {
			h.RemoveConn(c)
		}
	}
}

// Notify 先 Flush 该线程待发的 threads.event，然后向该线程的所有订阅连接调用 Conn.Notify(method, params)。
func (h *StreamHub) Notify(threadID, method string, params any) {
	h.Flush(threadID)

	var subscribers []Conn
	var obsList []func(threadID, method string, params any)
	h.mu.Lock()
	if st, ok := h.threads[threadID]; ok {
		subscribers = make([]Conn, 0, len(st.subscribers))
		for c := range st.subscribers {
			subscribers = append(subscribers, c)
		}
	}
	if h.observer != nil {
		obsList = append(obsList, h.observer)
	}
	for _, it := range h.observers {
		obsList = append(obsList, it.fn)
	}
	h.mu.Unlock()

	for _, obs := range obsList {
		obs(threadID, method, params)
	}

	var failed []Conn
	for _, c := range subscribers {
		if err := c.Notify(method, params); err != nil {
			failed = append(failed, c)
		}
	}

	if len(failed) > 0 {
		for _, c := range failed {
			h.RemoveConn(c)
		}
	}
}

// CurrentRunID 返回指定线程当前的 runID，不存在或尚未开始则返回空字符串。
func (h *StreamHub) CurrentRunID(threadID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if st, ok := h.threads[threadID]; ok {
		return st.runID
	}
	return ""
}

// Close 停止所有未到期定时器，且之后的 Publish 不再安排 flush。
func (h *StreamHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true
	for _, st := range h.threads {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
	}
}

// newUUID 使用 crypto/rand 生成标准 UUID v4 字符串。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x-%04x-4000-8000-%012x", time.Now().UnixNano(), 0, 0)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}
