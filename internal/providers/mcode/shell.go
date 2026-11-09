package mcode

import (
	"context"
	"sort"
	"strings"

	"mcode/internal/agent/session"
	"mcode/internal/agent/shell"
	"mcode/internal/harness"
)

// DisplayName 返回 provider 展示名称；未配置时回落到 Kind。
func (h *Harness) DisplayName() string {
	if h.opts.DisplayName != "" {
		return h.opts.DisplayName
	}
	if h.opts.Kind != "" {
		return string(h.opts.Kind)
	}
	return "mcode"
}

// ModelIDs 返回该来源当前可选的模型；未配置回调时返回 nil。
func (h *Harness) ModelIDs() []string {
	if h.opts.ModelIDs == nil {
		return nil
	}
	return h.opts.ModelIDs()
}

// RefreshModels 刷新模型目录；未配置回调时不做任何事。
func (h *Harness) RefreshModels(ctx context.Context) error {
	if h.opts.RefreshModels == nil {
		return nil
	}
	return h.opts.RefreshModels(ctx)
}

// Available 报告该来源是否可用；未配置回调时视为始终可用。
func (h *Harness) Available() (bool, string) {
	if h.opts.Available == nil {
		return true, ""
	}
	return h.opts.Available()
}

// PreserveSessionsFrom 在注册新实例时共享状态，旧实例进行中的轮次继续使用同一把锁和同一份 map。
func (h *Harness) PreserveSessionsFrom(previous harness.Harness) {
	if old, ok := previous.(*Harness); ok && old != nil && old != h {
		h.mu = old.mu
		h.gates = old.gates
		h.runs = old.runs
		h.pids = old.pids
	}
}

// ConfirmsApprovalDelivery 报告是否确认审批答复送达，默认开启。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return h.opts.ConfirmsApprovalDelivery == nil || *h.opts.ConfirmsApprovalDelivery
}

// SessionExists 判断某个会话的上下文是否还在。会话目录未配置时无法判断，按仍在处理。
func (h *Harness) SessionExists(sessionID string) bool {
	if !h.sessionKindMatches(sessionID) {
		return false
	}
	if sessionID == "" || h.opts.StoreDir == "" {
		return true
	}
	return session.NewStore(h.opts.StoreDir).Exists(sessionID)
}

// sessionKindMatches 保留旧无前缀 ID，同时拒绝其他已知来源的前缀。
func (h *Harness) sessionKindMatches(id string) bool {
	if strings.HasPrefix(id, string(h.Kind())+"_") {
		return true
	}
	prefix, _, found := strings.Cut(id, "_")
	if !found {
		return true
	}
	switch prefix {
	case "mcode", "openai", "anthropic", "deepseek", "zai", "meta", "magpie", "magpie2", "magpie3", "magpie4":
		return false
	}
	return !strings.HasPrefix(prefix, "custom-")
}

var _ harness.SessionChecker = (*Harness)(nil)

// withProcessTracker 让 ctx 携带该线程的 shell 进程登记回调，threadID 为空时原样返回。
func (h *Harness) withProcessTracker(ctx context.Context, threadID string) context.Context {
	if threadID == "" {
		return ctx
	}
	return shell.WithProcessTracker(shell.WithOwner(ctx, threadID),
		func(pid int) { h.trackProcess(threadID, pid) },
		func(pid int) { h.untrackProcess(threadID, pid) },
	)
}

// trackProcess 登记线程当前正在运行的命令进程。
func (h *Harness) trackProcess(threadID string, pid int) {
	if threadID == "" || pid <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pids == nil {
		h.pids = make(map[string]map[int]bool)
	}
	set := h.pids[threadID]
	if set == nil {
		set = make(map[int]bool)
		h.pids[threadID] = set
	}
	set[pid] = true
}

// untrackProcess 注销已退出的命令进程；线程记录已被回收时静默忽略。
func (h *Harness) untrackProcess(threadID string, pid int) {
	if threadID == "" || pid <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.pids[threadID]
	delete(set, pid)
	if len(set) == 0 {
		delete(h.pids, threadID)
	}
}

// ProcessRootPIDs 返回该线程当前正在运行的命令进程 PID 副本，没有时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	if threadID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.pids[threadID]
	if len(set) == 0 {
		return nil
	}
	pids := make([]int, 0, len(set))
	for pid := range set {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// EvictThread 释放线程持有的会话门禁与进程记录。
// 正在执行的回合已经持有自己的 gate 对象，删除表项不会影响它；
// 迟到的进程退出回调也会因为找不到记录而静默忽略。
func (h *Harness) EvictThread(threadID string) {
	if threadID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.gates, threadID)
	delete(h.pids, threadID)
}

var _ harness.ProcessRooter = (*Harness)(nil)
var _ harness.ThreadEvictor = (*Harness)(nil)
