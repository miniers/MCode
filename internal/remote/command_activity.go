package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procs"
	"mcode/internal/remote/wire"
)

var procSnapshot = procs.Snapshot

// RunningCommandActivity 按工具消息归集运行中命令的进程快照，每次只读取一次整机进程表。后台任务也会参与采样。
func (b *MemoryBackend) RunningCommandActivity(ctx context.Context, threadID string) (map[string][]procs.Proc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	activity := make(map[string][]procs.Proc)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("thread %s not found", threadID)}
	}
	b.ensureMessagesLoadedLocked(td)
	type command struct {
		id      string
		started time.Time
	}
	var commands []command
	for _, msg := range td.messages {
		if msg.ToolStatus == nil || !activityCommandTool(msg) {
			continue
		}
		if *msg.ToolStatus != "background" && (*msg.ToolStatus != "running" || td.runTurnID == "" || (msg.TurnID != nil && *msg.TurnID != td.runTurnID)) {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, msg.CreatedAt)
		if err == nil && !started.IsZero() {
			commands = append(commands, command{id: msg.ID, started: started})
			activity[msg.ID] = nil
		}
	}
	rooter, _ := b.threadHarnessLocked(td.summary.Provider).(harness.ProcessRooter)
	b.mu.Unlock()
	if rooter == nil || len(commands) == 0 {
		return activity, nil
	}
	sort.SliceStable(commands, func(i, j int) bool { return commands[i].started.Before(commands[j].started) })
	roots := rooter.ProcessRootPIDs(threadID)
	if len(roots) == 0 {
		return activity, nil
	}
	table, err := procSnapshot()
	if errors.Is(err, procs.ErrNotSupported) {
		return map[string][]procs.Proc{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	for _, root := range roots {
		procs.PopulateTreeTimes(table, root)
		processes := procs.TreesStartedAfterIn(table, root, commands[0].started)
		byPID := make(map[int]procs.Proc, len(processes))
		for _, p := range processes {
			byPID[p.PID] = p
		}
		owners := make(map[int]string)
		for _, p := range processes {
			if _, hasParent := byPID[p.PPID]; hasParent {
				continue
			}
			// ps 的启动时间只精确到秒，允许消息时间晚于根启动时间最多两秒。
			i := sort.Search(len(commands), func(i int) bool { return commands[i].started.After(p.StartTime.Add(2 * time.Second)) })
			if i > 0 {
				owners[p.PID] = commands[i-1].id
			}
		}
		for _, p := range processes {
			if seen[p.PID] {
				continue
			}
			ancestor := p
			visited := make(map[int]bool)
			for owners[ancestor.PID] == "" && !visited[ancestor.PID] {
				visited[ancestor.PID] = true
				parent, ok := byPID[ancestor.PPID]
				if !ok {
					break
				}
				ancestor = parent
			}
			if id := owners[ancestor.PID]; id != "" {
				activity[id] = append(activity[id], p)
				seen[p.PID] = true
			}
		}
	}
	return activity, nil
}

// activityCommandTool 与桌面端命令工具分类保持一致，包括带 MCP 命名空间的工具名。
func activityCommandTool(msg wire.ThreadMessage) bool {
	if msg.Role != "" && msg.Role != "tool" {
		return false
	}
	if msg.Kind != nil && strings.EqualFold(*msg.Kind, "command") {
		return true
	}
	name := ""
	if msg.ToolName != nil {
		name = *msg.ToolName
	}
	if name == "" {
		name = strings.TrimSpace(msg.Text)
		if i := strings.IndexAny(name, " \t\n{"); i >= 0 {
			name = name[:i]
		}
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if i := strings.LastIndexAny(name, "./:"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.NewReplacer("_", "", "-", "", " ", "").Replace(name)
	switch name {
	case "bash", "shell", "execcommand", "command", "runcommand", "terminal", "localshell", "exec", "execute":
		return true
	}
	return false
}
