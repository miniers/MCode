package mcodeui

import (
	"slices"
	"sync"
	"time"
)

// 浮动面板里的子代理输入框复用主 composer：每个子代理持有一个 embeddedBee 模式的轻量 chatState，
// 自己订阅该会话的历史与队列，输入框、模型选择、发送与排队都走与主会话相同的代码路径。

// embeddedBeeIdleTTL 是子代理面板不再显示后，嵌入状态保留多久才回收：
// 切换所选子代理时不丢失输入到一半的草稿。
const embeddedBeeIdleTTL = 30 * time.Second

type embeddedBeeKey struct {
	parent   *chatState
	threadID string
}

type embeddedBeeEntry struct {
	state    *chatState
	lastUsed time.Time
}

var (
	embeddedMu sync.Mutex
	// embeddedBees 是各主会话状态下已创建的嵌入状态。
	embeddedBees = map[embeddedBeeKey]*embeddedBeeEntry{}
	// embeddedParent 记录嵌入状态所属的主会话状态。
	embeddedParent = map[*chatState]*chatState{}
	// embeddedFocused 是当前输入框聚焦的嵌入状态。
	embeddedFocused = map[*chatState]bool{}
	// embeddedStatusSeen 记录各嵌入状态上次同步时看到的 bee 状态，只在状态变化时改 IsRunning。
	embeddedStatusSeen = map[*chatState]string{}
)

// composerOwnsShortcuts 表示 s 的 composer 是否处理窗口级快捷键（⌘. 中断、⌘⇧M 切换模式）：
// 嵌入的子代理输入框聚焦时归它，否则归主 composer，避免两个 composer 同时响应。
func composerOwnsShortcuts(s *chatState) bool {
	embeddedMu.Lock()
	defer embeddedMu.Unlock()
	if s.embeddedBee {
		return embeddedFocused[s]
	}
	for e, parent := range embeddedParent {
		if parent == s && embeddedFocused[e] {
			return false
		}
	}
	return true
}

// noteEmbeddedComposerFocus 记录嵌入 composer 的聚焦变化；主 composer 调用时无事发生。
func noteEmbeddedComposerFocus(s *chatState, focused bool) {
	if !s.embeddedBee {
		return
	}
	embeddedMu.Lock()
	defer embeddedMu.Unlock()
	if focused {
		embeddedFocused[s] = true
	} else {
		delete(embeddedFocused, s)
	}
}

// toastOwner 返回嵌入状态的 Toast 所属状态；独立窗口状态未登记时仍由自己显示。
func toastOwner(s *chatState) *chatState {
	embeddedMu.Lock()
	parent := embeddedParent[s]
	embeddedMu.Unlock()
	if parent != nil {
		return parent
	}
	return s
}

// newEmbeddedBeeState 构造一个 embeddedBee 模式的子会话状态：复用 composer，但不含主窗口独有的组件。
// 状态不登记进嵌入表，生命周期由调用方负责（浮窗缓存或独立窗口各持一份）。
func newEmbeddedBeeState(parent *chatState) *chatState {
	child := newChatState(parent.client)
	child.embeddedBee = true
	child.embeddedPicker = &ModelPickerState{}
	return child
}

// syncEmbeddedBeeState 把父会话的服务、供应商与 bee 的会话信息同步到子会话状态。
// 首次同步（尚未初始化）时启动队列监听并选中该 bee 的会话。
func syncEmbeddedBeeState(parent, child *chatState, bee SwarmBee) {
	parent.mu.Lock()
	services := parent.uiServices()
	providers := slices.Clone(parent.providers)
	newProvider := parent.newThreadProvider
	var known *Thread
	for i := range parent.threads {
		if parent.threads[i].ID == bee.ThreadID {
			t := parent.threads[i]
			known = &t
			break
		}
	}
	parent.mu.Unlock()

	child.mu.Lock()
	first := !child.initialized
	if first {
		child.setUIServices(services)
		child.initialized = true
	}
	child.providers = providers
	child.newThreadProvider = newProvider
	have := false
	for _, th := range child.threads {
		if th.ID == bee.ThreadID {
			have = true
			break
		}
	}
	if !have {
		th := Thread{ID: bee.ThreadID, Title: bee.Name, IsHelper: true, IsRunning: bee.Status == "running"}
		if known != nil {
			th = *known
		}
		child.threads = append(child.threads, th)
		// 新挂入的 bee 会话带自己的待处理请求标记，子会话的缓存必须失效。
		child.bumpThreadsRuntimeLocked()
	}
	child.mu.Unlock()

	// 只在 bee 状态变化时覆盖 IsRunning：子代理结束后用户仍可在它的输入框追问，
	// 那种情况下 child 自己乐观置上的运行中不能被每帧同步抹掉。
	embeddedMu.Lock()
	prev, seen := embeddedStatusSeen[child]
	embeddedMu.Unlock()
	if !seen || prev != bee.Status {
		changed := false
		child.mu.Lock()
		for i := range child.threads {
			if child.threads[i].ID != bee.ThreadID {
				continue
			}
			running := bee.Status == "running"
			if child.threads[i].IsRunning != running {
				child.threads[i].IsRunning = running
				changed = true
			}
			break
		}
		child.mu.Unlock()
		embeddedMu.Lock()
		embeddedStatusSeen[child] = bee.Status
		embeddedMu.Unlock()
		if changed {
			child.invalidate()
		}
	}

	if !first {
		return
	}
	child.startQueueWatch()
	child.selectThread(bee.ThreadID)
}

// beeChatState 返回 parent 面板里某个子代理的嵌入状态，必要时创建，并同步供应商与会话信息。
func beeChatState(parent *chatState, bee SwarmBee) *chatState {
	key := embeddedBeeKey{parent, bee.ThreadID}
	embeddedMu.Lock()
	entry := embeddedBees[key]
	if entry == nil {
		child := newEmbeddedBeeState(parent)
		entry = &embeddedBeeEntry{state: child}
		embeddedBees[key] = entry
		embeddedParent[child] = parent
	}
	entry.lastUsed = time.Now()
	child := entry.state
	embeddedMu.Unlock()

	syncEmbeddedBeeState(parent, child, bee)
	return child
}

// releaseEmbeddedBeeState 回收一个不在嵌入表里的子会话状态（独立窗口用）：
// 清掉它的聚焦记录再关闭，避免映射里残留已关闭状态的指针。
func releaseEmbeddedBeeState(child *chatState) {
	if child == nil {
		return
	}
	embeddedMu.Lock()
	delete(embeddedFocused, child)
	delete(embeddedStatusSeen, child)
	embeddedMu.Unlock()
	child.close()
}

// sweepEmbeddedBees 回收 parent 名下闲置超过 embeddedBeeIdleTTL 的嵌入状态。
func sweepEmbeddedBees(parent *chatState) {
	now := time.Now()
	var stale []*chatState
	embeddedMu.Lock()
	for key, entry := range embeddedBees {
		if key.parent == parent && now.Sub(entry.lastUsed) > embeddedBeeIdleTTL {
			stale = append(stale, entry.state)
			delete(embeddedBees, key)
			delete(embeddedParent, entry.state)
			delete(embeddedFocused, entry.state)
			delete(embeddedStatusSeen, entry.state)
		}
	}
	embeddedMu.Unlock()
	for _, child := range stale {
		child.close()
	}
}

// closeEmbeddedBees 在主会话窗口关闭时回收它名下所有嵌入状态。
func closeEmbeddedBees(parent *chatState) {
	var all []*chatState
	embeddedMu.Lock()
	for key, entry := range embeddedBees {
		if key.parent == parent {
			all = append(all, entry.state)
			delete(embeddedBees, key)
			delete(embeddedParent, entry.state)
			delete(embeddedFocused, entry.state)
			delete(embeddedStatusSeen, entry.state)
		}
	}
	embeddedMu.Unlock()
	for _, child := range all {
		child.close()
	}
}
