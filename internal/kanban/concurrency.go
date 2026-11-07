package kanban

import (
	"errors"
	"sync"
)

// maxConcurrentWritesPerSession 限制每个调用方会话允许的最大并发写操作数。
const maxConcurrentWritesPerSession = 4

var (
	// ErrTaskAlreadyInFlight 表示同一任务已有写操作正在执行。
	ErrTaskAlreadyInFlight = errors.New("kanban: task write already in flight")
	// ErrRequestAlreadyInFlight 表示同一会话中相同 request_id 的写操作正在执行。
	ErrRequestAlreadyInFlight = errors.New("kanban: request write already in flight")
	// ErrRateLimitExceeded 表示当前会话并发写操作数已达上限。
	ErrRateLimitExceeded = errors.New("kanban: write rate limit exceeded")
)

// WriteConcurrencyManager 管理看板写操作的并发度、任务排他锁及请求级防重锁。
type WriteConcurrencyManager struct {
	mu            sync.Mutex
	taskKeys      map[string]bool
	requestKeys   map[string]map[string]bool // sessionID -> requestID -> true
	sessionCounts map[string]int
}

// NewWriteConcurrencyManager 创建写并发锁管理器实例。
func NewWriteConcurrencyManager() *WriteConcurrencyManager {
	return &WriteConcurrencyManager{
		taskKeys:      make(map[string]bool),
		requestKeys:   make(map[string]map[string]bool),
		sessionCounts: make(map[string]int),
	}
}

// Acquire 尝试获取写操作锁。空 taskID/requestID 不参与检查；匿名调用方共享会话限额。
// 返回的 release 函数保证幂等释放。
func (m *WriteConcurrencyManager) Acquire(sessionID, taskID, requestID string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 同一 taskID 已有写操作在途时直接拒绝
	if taskID != "" && m.taskKeys[taskID] {
		return nil, ErrTaskAlreadyInFlight
	}

	// 2. 同一 requestID 已有写操作在途时直接拒绝
	if requestID != "" {
		if reqs, ok := m.requestKeys[sessionID]; ok && reqs[requestID] {
			return nil, ErrRequestAlreadyInFlight
		}
	}

	// 3. 每个 caller session 最多同时 4 个写操作
	if m.sessionCounts[sessionID] >= maxConcurrentWritesPerSession {
		return nil, ErrRateLimitExceeded
	}

	// 登记占用
	if taskID != "" {
		m.taskKeys[taskID] = true
	}
	if requestID != "" {
		reqs, ok := m.requestKeys[sessionID]
		if !ok {
			reqs = make(map[string]bool)
			m.requestKeys[sessionID] = reqs
		}
		reqs[requestID] = true
	}
	m.sessionCounts[sessionID]++

	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()

			if taskID != "" {
				delete(m.taskKeys, taskID)
			}
			if requestID != "" {
				if reqs, ok := m.requestKeys[sessionID]; ok {
					delete(reqs, requestID)
					if len(reqs) == 0 {
						delete(m.requestKeys, sessionID)
					}
				}
			}
			if count, ok := m.sessionCounts[sessionID]; ok {
				if count <= 1 {
					delete(m.sessionCounts, sessionID)
				} else {
					m.sessionCounts[sessionID] = count - 1
				}
			}
		})
	}

	return release, nil
}
