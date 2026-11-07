package mcodeui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/git"
	"mcode/internal/projctx"
)

// aiWorktreeCallLimit 限制回放的最近工具调用次数，避免超长会话把时间线扫一遍。
const aiWorktreeCallLimit = 300

// aiWorktreeMaxActive 限制同时进行的识别数量；识别要读会话历史与 Git 状态，不宜并发过多。
const aiWorktreeMaxActive = 2

// aiWorktreeRedetectInterval 是已识别会话重新识别自建 worktree 的最小间隔。
// 选中会话的 messagesRev 会随流式增量不断变化，若变化即重新识别，几乎每帧都会起一个
// 识别 goroutine；会话已识别过时，流式期间最多按这个间隔重新识别一次。
// 测试里设为 0，此时行为与不限频完全一致，且不安排延后回调。
var aiWorktreeRedetectInterval = 5 * time.Second

// aiWorktreeInfo 描述会话实际在哪个自建 worktree 里干活：路径与它检出的分支。
// 空值表示会话没在自建 worktree 里干活。
type aiWorktreeInfo struct {
	Path   string
	Branch string
}

// aiWorktreeState 按 threadID 缓存识别结果；识别只用于展示与 Diff 归属，
// 不改变会话的工作目录。按 chatState 附加，同 worktreeUIState 模式。
type aiWorktreeState struct {
	mu      sync.Mutex
	found   map[string]aiWorktreeInfo // threadID → 识别结果；存在且 Path 为空表示已识别且没有自建 worktree
	revs    map[string]uint64         // 识别时 s.messagesRev 的取值，用于判断缓存是否过期
	gens    map[string]uint64         // 每次排队的序号，避免慢识别覆盖较新的结果
	pending map[string]bool           // 已在排队或识别中的 threadID
	// lastDetect/redetectArmed 用于限频重新识别：前者记上次排队识别的时刻，
	// 后者记该会话是否已安排延后重绘，避免同一会话堆积多个定时器。
	lastDetect    map[string]time.Time
	redetectArmed map[string]bool
	queue         []string
	active        int
}

var (
	aiWorktreeMu     sync.Mutex
	aiWorktreeStates = make(map[*chatState]*aiWorktreeState)
)

// getAIWorktreeState 返回 chatState 对应的识别缓存，首次访问时创建。
func getAIWorktreeState(s *chatState) *aiWorktreeState {
	aiWorktreeMu.Lock()
	defer aiWorktreeMu.Unlock()
	st, ok := aiWorktreeStates[s]
	if !ok {
		st = &aiWorktreeState{
			found:         make(map[string]aiWorktreeInfo),
			revs:          make(map[string]uint64),
			gens:          make(map[string]uint64),
			pending:       make(map[string]bool),
			lastDetect:    make(map[string]time.Time),
			redetectArmed: make(map[string]bool),
		}
		aiWorktreeStates[s] = st
	}
	return st
}

// selectionAndRevision 一次性读出当前选中会话与消息版本，供判断缓存是否过期。
func (s *chatState) selectionAndRevision() (string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID, s.messagesRev
}

// aiWorktreeRevision 返回会话的识别版本：只有选中会话的内存消息会变，其余会话记为 0。
func (s *chatState) aiWorktreeRevision(threadID string) uint64 {
	selected, rev := s.selectionAndRevision()
	if selected != threadID {
		return 0
	}
	return rev
}

// aiWorktreePath 返回会话自建 worktree 的路径（空串表示没有），
// 并在尚未识别或选中会话的消息已变化时排队重新识别。
func (s *chatState) aiWorktreePath(threadID string) string {
	return s.aiWorktree(threadID).Path
}

// aiWorktree 返回会话自建 worktree 的识别结果（缓存）。
// 标题栏展示分支时可以用返回的 Branch；未识别出来时两个字段都为空。
func (s *chatState) aiWorktree(threadID string) aiWorktreeInfo {
	if s == nil || threadID == "" {
		return aiWorktreeInfo{}
	}
	selected, rev := s.selectionAndRevision()
	st := getAIWorktreeState(s)
	st.mu.Lock()
	info, known := st.found[threadID]
	// !known：还没识别过，必须立即排队，首次识别不受限频影响。
	if !known {
		st.mu.Unlock()
		s.ensureAIWorktree(threadID)
		return info
	}
	if !(threadID == selected && st.revs[threadID] != rev) {
		st.mu.Unlock()
		return info
	}
	// 已识别过但消息变了：距上次排队不足一个间隔就先不识别，改为安排一次延后重绘，
	// 这样流式结束后即使没有新帧，标识也能在一个间隔内刷新。
	wait := aiWorktreeRedetectInterval - time.Since(st.lastDetect[threadID])
	if wait <= 0 {
		st.mu.Unlock()
		s.ensureAIWorktree(threadID)
		return info
	}
	arm := !st.redetectArmed[threadID]
	st.redetectArmed[threadID] = true
	st.mu.Unlock()
	if arm {
		time.AfterFunc(wait, func() {
			st.mu.Lock()
			st.redetectArmed[threadID] = false
			st.mu.Unlock()
			s.invalidate()
		})
	}
	return info
}

// aiWorktreeFor 返回会话的自建 worktree；缓存缺失或过期时同步识别。
// 识别要读会话历史与 Git 状态，调用方应在后台 goroutine 中调用。
func (s *chatState) aiWorktreeFor(ctx context.Context, threadID string) aiWorktreeInfo {
	if s == nil || threadID == "" || s.client == nil {
		return aiWorktreeInfo{}
	}
	selected, rev := s.selectionAndRevision()
	st := getAIWorktreeState(s)
	st.mu.Lock()
	info, known := st.found[threadID]
	fresh := known && !(threadID == selected && st.revs[threadID] != rev)
	st.mu.Unlock()
	if fresh {
		return info
	}
	info = s.detectAIWorktree(ctx, threadID)
	st.mu.Lock()
	st.found[threadID] = info
	st.revs[threadID] = rev
	st.mu.Unlock()
	return info
}

// ensureAIWorktree 把会话排进识别队列；并发受限，队列由 runAIWorktreeQueue 消费。
func (s *chatState) ensureAIWorktree(threadID string) {
	if s == nil || threadID == "" || s.client == nil {
		return
	}
	st := getAIWorktreeState(s)
	st.mu.Lock()
	if st.pending[threadID] {
		st.mu.Unlock()
		return
	}
	st.pending[threadID] = true
	st.lastDetect[threadID] = time.Now()
	st.gens[threadID]++
	st.queue = append(st.queue, threadID)
	start := st.active < aiWorktreeMaxActive
	if start {
		st.active++
	}
	st.mu.Unlock()
	if start {
		go s.runAIWorktreeQueue()
	}
}

// runAIWorktreeQueue 串行消费识别队列，队列取空后释放占用的并发位。
func (s *chatState) runAIWorktreeQueue() {
	st := getAIWorktreeState(s)
	for {
		st.mu.Lock()
		if len(st.queue) == 0 {
			st.active--
			st.mu.Unlock()
			return
		}
		threadID := st.queue[0]
		st.queue = st.queue[1:]
		gen := st.gens[threadID]
		st.mu.Unlock()

		rev := s.aiWorktreeRevision(threadID)
		info := s.detectAIWorktree(context.Background(), threadID)

		st.mu.Lock()
		if st.gens[threadID] == gen {
			st.found[threadID] = info
			st.revs[threadID] = rev
		}
		delete(st.pending, threadID)
		st.mu.Unlock()
		s.invalidate()
	}
}

// detectAIWorktree 识别会话实际写入的自建 worktree：用会话工作目录、同仓库的其他 worktree
// （剔除应用自管的那些）和时间线里的写入线索做匹配，判断逻辑见 projctx.LocateWorktree。
func (s *chatState) detectAIWorktree(ctx context.Context, threadID string) aiWorktreeInfo {
	if s.client == nil {
		return aiWorktreeInfo{}
	}
	state, err := s.client.GitState(ctx, threadID)
	if err != nil {
		return aiWorktreeInfo{}
	}
	root := strings.TrimSpace(state.WorkDir)
	if root == "" {
		return aiWorktreeInfo{}
	}
	branches := make(map[string]string, len(state.Worktrees))
	candidates := make([]string, 0, len(state.Worktrees))
	// 其他会话的工作目录：它们自己的 worktree（看板任务、Swarm bee、应用自建）不是本会话 AI 自建的。
	otherWorkdirs := s.aiWorktreeOtherWorkdirs(threadID)
	for _, worktree := range state.Worktrees {
		path := strings.TrimSpace(worktree.Path)
		if path == "" || git.IsManagedWorktreePath(path) || otherWorkdirs[filepath.Clean(path)] {
			continue
		}
		candidates = append(candidates, path)
		branches[path] = strings.TrimSpace(worktree.Branch)
	}
	if len(candidates) == 0 {
		return aiWorktreeInfo{}
	}
	messages, err := s.aiWorktreeMessages(ctx, threadID)
	if err != nil {
		return aiWorktreeInfo{}
	}
	path := projctx.LocateWorktree(root, candidates, aiWorktreeWrites(messages))
	if path == "" {
		return aiWorktreeInfo{}
	}
	return aiWorktreeInfo{Path: path, Branch: branches[path]}
}

// aiWorktreeOtherWorkdirs 返回其他会话的工作目录集合，用于把它们的 worktree 排除在候选之外。
func (s *chatState) aiWorktreeOtherWorkdirs(threadID string) map[string]bool {
	if s.client == nil {
		return nil
	}
	s.mu.Lock()
	threads := make([]Thread, len(s.threads))
	copy(threads, s.threads)
	s.mu.Unlock()
	out := make(map[string]bool, len(threads))
	for _, thread := range threads {
		if thread.ID == threadID {
			continue
		}
		dir, ok := s.client.ThreadWorkDir(thread.ID)
		if !ok || strings.TrimSpace(dir) == "" {
			continue
		}
		out[filepath.Clean(dir)] = true
	}
	return out
}

// aiWorktreeMessages 取会话的时间线：已选中的会话直接用内存里的消息，其余从后端读一次。
func (s *chatState) aiWorktreeMessages(ctx context.Context, threadID string) ([]Message, error) {
	s.mu.Lock()
	selected := s.selectedThreadID
	messages := s.messages
	s.mu.Unlock()
	if selected == threadID && len(messages) > 0 {
		return messages, nil
	}
	return s.client.History(ctx, threadID)
}

// aiWorktreeWrites 把消息时间线归约为写入线索，由旧到新排列。
// 命令类工具给出命令原文（cwd 在其中随 cd 前进），编辑类与写文件的 MCP 工具给出路径。
func aiWorktreeWrites(messages []Message) []projctx.ToolWrite {
	var calls []Message
	for i := len(messages) - 1; i >= 0 && len(calls) < aiWorktreeCallLimit; i-- {
		message := messages[i]
		// 命令类工具的消息 Role 可能为空，只有普通对话消息才跳过。
		if message.Role != "tool" && !isCommandTool(message) {
			continue
		}
		calls = append(calls, message)
	}
	writes := make([]projctx.ToolWrite, 0, len(calls))
	for i := len(calls) - 1; i >= 0; i-- {
		call := calls[i]
		write := projctx.ToolWrite{}
		if isCommandTool(call) {
			write.Command = parseCommandToolInfo(call).Command
		}
		if isAIWorktreeWriteTool(call) {
			write.Paths = toolEditedPaths(call)
		}
		writes = append(writes, write)
	}
	return writes
}

// isAIWorktreeWriteTool 判断工具调用是否写文件：编辑类工具，或名字看起来像写文件的 MCP 工具。
func isAIWorktreeWriteTool(m Message) bool {
	if isFileEditTool(m) {
		return true
	}
	name := strings.ToLower(toolMessageName(m))
	for _, keyword := range []string{"write", "edit", "create", "move", "patch"} {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	return false
}

// aiWorktreeWorkingDiff 取 AI 自建 worktree 当前的未提交改动（含未跟踪文件）为 unified diff 文本。
func aiWorktreeWorkingDiff(ctx context.Context, path string) (string, error) {
	return git.NewRunner("", 2).WorkingTreeDiffText(ctx, path)
}
