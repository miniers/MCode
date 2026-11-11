package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type diffThreadSnapshot struct {
	workDir       string
	git           *git.Runner
	messages      []wire.ThreadMessage
	running       bool
	runningTurnID string
	takenBack     map[string]bool
	store         *store.Store
}

// diffThreadState 复用旧 Diff 的会话与 worktree 解析，并在锁内复制可变状态。
func (b *MemoryBackend) diffThreadState(threadID string) (diffThreadSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return diffThreadSnapshot{}, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("thread %s not found", threadID)}
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return diffThreadSnapshot{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	s := diffThreadSnapshot{workDir: b.harnessWorkDir, git: b.git, messages: make([]wire.ThreadMessage, len(td.messages)), running: td.summary.IsRunning, runningTurnID: td.runTurnID, takenBack: make(map[string]bool, len(td.takenBack)), store: b.store}
	if td.workDir != "" {
		s.workDir = td.workDir
	} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		s.workDir = proj.Path
	}
	copy(s.messages, td.messages)
	for id := range td.takenBack {
		s.takenBack[id] = true
	}
	return s, nil
}

type reviewTurn struct {
	id, start, end string
	index          int
	ordered        bool
}

func reviewScopeValid(scope wire.ReviewScope) bool {
	return scope == wire.ReviewScopeTurn || scope == wire.ReviewScopeSession || scope == wire.ReviewScopeUncommitted
}

// ReviewGet 返回固定版本的只读审阅快照，全文只按改动列表中的路径取用。
func (b *MemoryBackend) ReviewGet(ctx context.Context, p wire.ThreadsReviewGetParams) (wire.ThreadsReviewGetResult, error) {
	result := wire.ThreadsReviewGetResult{Scope: p.Scope, TurnID: p.TurnID, Files: []wire.ReviewFileMeta{}}
	if !reviewScopeValid(p.Scope) {
		return result, ExtInvalidParams("scope 必须为 turn、session 或 uncommitted")
	}
	if p.Scope != wire.ReviewScopeTurn && p.TurnID != "" {
		return result, ExtInvalidParams("turnID 只适用于 turn 作用域")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	state, err := b.diffThreadState(p.ThreadID)
	if err != nil {
		return result, err
	}
	unavailable := func(reason string) (wire.ThreadsReviewGetResult, error) {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Unavailable = reason
		return result, nil
	}
	if state.git == nil {
		return unavailable("git is not available")
	}
	if state.workDir == "" {
		return unavailable("work directory not specified")
	}
	root, err := state.git.RepoRoot(ctx, state.workDir)
	if err != nil {
		return unavailable("not a git repository: " + err.Error())
	}
	var base, head string
	var untracked map[string]bool
	if p.Scope == wire.ReviewScopeUncommitted {
		base, err = state.git.ReviewRevision(ctx, root, "HEAD")
		if err != nil {
			return unavailable("无法读取 HEAD: " + err.Error())
		}
	} else {
		turns, e := reviewTurns(ctx, state, root, p.ThreadID)
		if e != nil {
			return unavailable("无法读取 checkpoint: " + e.Error())
		}
		if p.Scope == wire.ReviewScopeTurn {
			id := p.TurnID
			if id == "" && state.running {
				id = state.runningTurnID
			}
			if id == "" {
				for i := len(turns) - 1; i >= 0; i-- {
					if turns[i].start != "" && turns[i].end != "" {
						id = turns[i].id
						break
					}
				}
				if id == "" {
					return unavailable("会话没有包含 start 和 end checkpoint 的可审轮")
				}
			}
			result.TurnID = id
			for _, turn := range turns {
				if turn.id == id {
					base, head = turn.start, turn.end
					break
				}
			}
			result.Running = state.running && id != "" && id == state.runningTurnID
			if base == "" {
				return unavailable("该轮缺少 start checkpoint")
			}
			if result.Running {
				head = ""
			} else if head == "" {
				return unavailable("该轮缺少 end checkpoint")
			}
		} else {
			first := -1
			for i, turn := range turns {
				if turn.start != "" {
					base = turn.start
					first = i
					break
				}
			}
			if first >= 0 {
				for _, turn := range turns[first:] {
					if turn.end != "" {
						head = turn.end
					}
				}
			}
			if base == "" {
				return unavailable("会话缺少 start checkpoint")
			}
			if head == "" {
				return unavailable("会话缺少 end checkpoint")
			}
		}
		base, err = state.git.ReviewRevision(ctx, root, base)
		if err != nil {
			return unavailable("start checkpoint 不可用: " + err.Error())
		}
	}
	if head == "" {
		head, untracked, err = state.git.ReviewWorkingTree(ctx, root)
	} else {
		head, err = state.git.ReviewRevision(ctx, root, head)
	}
	if err != nil {
		return unavailable("审阅终点不可用: " + err.Error())
	}
	result.BaseRevision, result.HeadRevision = base, head
	files, err := state.git.ReviewFiles(ctx, root, base, head, untracked)
	if err != nil {
		return unavailable("无法读取审阅改动: " + err.Error())
	}
	selected := -1
	for i, f := range files {
		result.Files = append(result.Files, wire.ReviewFileMeta{Path: f.Path, OrigPath: f.OrigPath, Status: f.Status, Language: reviewLanguage(f.Path), OldSize: f.OldSize, NewSize: f.NewSize, OldHash: f.OldHash, NewHash: f.NewHash, Binary: f.Binary, TooLarge: f.TooLarge, Missing: f.Missing})
		if f.Path == p.Path {
			selected = i
		}
	}
	if p.Path != "" {
		if selected < 0 {
			return result, ExtInvalidParams("path 不在该作用域的改动列表中")
		}
		f := files[selected]
		oldText, newText, binary, tooLarge, missing, e := state.git.ReviewTexts(ctx, root, f)
		if e != nil {
			return result, e
		}
		result.File = &wire.ReviewFileContent{Path: f.Path, OldText: oldText, NewText: newText, OldHash: f.OldHash, NewHash: f.NewHash, Binary: binary, TooLarge: tooLarge, Missing: missing}
		result.Files[selected].Binary = binary
		result.Files[selected].TooLarge = tooLarge
		result.Files[selected].Missing = missing
	}
	return result, nil
}

func reviewTurns(ctx context.Context, state diffThreadSnapshot, root, threadID string) ([]reviewTurn, error) {
	prefix := "refs/mcode/checkpoints/" + threadID + "/"
	refs, err := state.git.ListRefs(ctx, root, prefix)
	if err != nil {
		return nil, err
	}
	turns := make(map[string]*reviewTurn)
	get := func(id string) *reviewTurn {
		if turns[id] == nil {
			turns[id] = &reviewTurn{id: id}
		}
		return turns[id]
	}
	for _, ref := range refs {
		parts := strings.Split(strings.TrimPrefix(ref.Name, prefix), "/")
		if len(parts) != 2 || state.takenBack[parts[0]] {
			continue
		}
		t := get(parts[0])
		if parts[1] == "start" {
			t.start = ref.Commit
		} else if parts[1] == "end" {
			t.end = ref.Commit
		}
	}
	for i, msg := range state.messages {
		if msg.TurnID != nil && !state.takenBack[*msg.TurnID] {
			t := get(*msg.TurnID)
			if !t.ordered {
				t.index = i
				t.ordered = true
			}
		}
	}
	if state.store != nil {
		stored, err := state.store.ListTurns(ctx, threadID)
		if err != nil {
			return nil, err
		}
		for _, st := range stored {
			if st.Status == "taken_back" || state.takenBack[st.ID] {
				delete(turns, st.ID)
				continue
			}
			t := get(st.ID)
			t.index = st.TurnIndex
			t.ordered = true
			if st.BaseCheckpoint != "" {
				t.start = st.BaseCheckpoint
			}
			if st.FinishCheckpoint != "" {
				t.end = st.FinishCheckpoint
			}
		}
	}
	result := make([]reviewTurn, 0, len(turns))
	for _, t := range turns {
		result = append(result, *t)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.ordered != b.ordered {
			return a.ordered
		}
		if a.ordered && a.index != b.index {
			return a.index < b.index
		}
		return a.id < b.id
	})
	return result, nil
}

func reviewLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".json":
		return "json"
	case ".md", ".mdx":
		return "markdown"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".swift":
		return "swift"
	default:
		return ""
	}
}
