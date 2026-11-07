package mcodeui

import "strings"

// continuePillKind 表示输入框上方「继续」胶囊的文案种类。
type continuePillKind int

const (
	// continuePillNone 表示当前没有可显示的继续入口。
	continuePillNone continuePillKind = iota
	// continuePillInterrupted 表示上一轮被中断（用户停止、取消或服务端标记中断）。
	continuePillInterrupted
	// continuePillFailed 表示上一轮失败结束，可以接着往下跑。
	continuePillFailed
)

// continuePillState 是判定继续入口所需的纯数据快照，便于表驱动测试。
// 所有字段都由 continuePillSnapshotLocked 在持锁状态下填充。
type continuePillState struct {
	// locallyInterrupted 是本次运行中用户停止成功后置位的标记（s.interrupted）。
	locallyInterrupted bool
	// lastStatus 是后端给出的线程最近一轮结束状态（Thread.LastStatus）。
	lastStatus string
	// isRunning 表示线程本体或名下子线程还在运行。
	isRunning bool
	// dismissed 表示这一次异常结束已经被用户忽略过。
	dismissed bool
	// lastMessageID 是线程最后一条消息的 ID，没有消息时为空串。
	lastMessageID string
	// lastMessageIsError 表示最后一条消息是错误消息（时间线里已有「重试」卡片）。
	lastMessageIsError bool
}

// continuePillStatusKind 把线程最近一轮终态映射成胶囊文案种类；非异常结束返回 continuePillNone。
func continuePillStatusKind(status string) continuePillKind {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "interrupted", "cancelled", "canceled", "stopped":
		return continuePillInterrupted
	case "failed":
		return continuePillFailed
	}
	return continuePillNone
}

// shouldShowContinuePill 纯函数：判断当前会话是否显示继续胶囊，以及胶囊的文案种类。
// 显示条件：本次运行里用户停止过，或者后端最近一轮属于异常结束且这一轮没有被忽略。
func shouldShowContinuePill(st continuePillState) (bool, continuePillKind) {
	if st.isRunning {
		return false, continuePillNone
	}
	if st.locallyInterrupted {
		return true, continuePillInterrupted
	}
	kind := continuePillStatusKind(st.lastStatus)
	if kind == continuePillNone {
		return false, continuePillNone
	}
	// 失败且时间线末尾就是错误卡片时不再提供胶囊：错误卡片自己已经带「重试」入口。
	if kind == continuePillFailed && st.lastMessageIsError {
		return false, continuePillNone
	}
	// 这一次异常结束被忽略过就不再显示；出现新一轮（最后一条消息 ID 变化）后自动恢复。
	if st.dismissed {
		return false, continuePillNone
	}
	return true, kind
}

// continuePillLabels 返回胶囊的状态文案与继续按钮的操作提示；按钮文字统一为「继续」。
func continuePillLabels(kind continuePillKind) (status, tooltip string) {
	if kind == continuePillFailed {
		return "上一轮失败", "从失败处继续"
	}
	return "上一轮已中断", "继续上一轮"
}

// continueMessageIsError 判断消息是不是时间线里的错误卡片，识别规则与时间线渲染保持一致。
func continueMessageIsError(m Message) bool {
	return m.Role == "error" || m.Kind == "error" || strings.HasPrefix(m.Role, "error")
}

// continuePillSnapshotLocked 在持锁状态下汇总判定继续胶囊所需的状态；调用方必须持有 s.mu。
// 这里只读内存字段，不调用任何会再次加 s.mu 的函数（例如 isThreadRunning）。
func (s *chatState) continuePillSnapshotLocked(threadID string) continuePillState {
	var st continuePillState
	if threadID == "" {
		return st
	}
	st.locallyInterrupted = s.interrupted[threadID]
	for _, th := range s.threads {
		if th.ID == threadID {
			st.lastStatus = th.LastStatus
			st.isRunning = th.IsRunning || th.SwarmBusy
			break
		}
	}
	// s.messages 只含当前会话，其它会话拿不到最后一条消息。
	if threadID == s.selectedThreadID && len(s.messages) > 0 {
		last := s.messages[len(s.messages)-1]
		st.lastMessageID = last.ID
		st.lastMessageIsError = continueMessageIsError(last)
	}
	// 忽略记录按「线程 ID -> 当时最后一条消息 ID」保存：最后一条消息变了说明开了新一轮，记录自动失效。
	if recorded, ok := s.continueDismissed[threadID]; ok {
		st.dismissed = recorded == st.lastMessageID
	}
	return st
}

// dismissContinuePill 收起继续胶囊：清掉本地中断标记，记录这一次异常结束已被忽略，并在开启持久化时写盘。
func (s *chatState) dismissContinuePill(threadID, lastMessageID string) {
	if s == nil || threadID == "" {
		return
	}
	s.mu.Lock()
	if s.interrupted != nil {
		delete(s.interrupted, threadID)
	}
	// 原有的本地中断标记保留原地删除，忽略记录按线程分开写，互不影响。
	if s.continueDismissed == nil {
		s.continueDismissed = make(map[string]string)
	}
	s.continueDismissed[threadID] = lastMessageID
	records := copyStringMap(s.continueDismissed)
	s.mu.Unlock()
	persistContinueDismissed(s, records)
	s.invalidate()
}
