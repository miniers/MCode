package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

type reviewHunkReverter interface {
	ReviewRevertHunk(context.Context, wire.ThreadsReviewRevertHunkParams) (wire.ThreadsReviewRevertHunkResult, error)
}

var reviewWorkspaceLocks sync.Map

func (b *MemoryBackend) ReviewRevertHunk(ctx context.Context, p wire.ThreadsReviewRevertHunkParams) (wire.ThreadsReviewRevertHunkResult, error) {
	var result wire.ThreadsReviewRevertHunkResult
	if !reviewScopeValid(p.Scope) || p.Path == "" || p.BaseRevision == "" || p.HeadRevision == "" || p.StartLine < 1 || p.LineCount < 0 || p.HunkHash == "" || p.ExpectedFileHash == "" {
		return result, errors.New("参数无效")
	}
	state, err := b.diffThreadState(p.ThreadID)
	if err != nil {
		return result, err
	}
	if state.running {
		return result, errors.New("会话正在运行，无法撤销改动块")
	}
	if state.git == nil || state.workDir == "" {
		return result, errors.New("git 不可用，无法撤销改动块")
	}
	if p.Scope != wire.ReviewScopeTurn && p.TurnID != "" {
		return result, errors.New("turnID 只适用于 turn 作用域")
	}
	root, err := state.git.RepoRoot(ctx, state.workDir)
	if err != nil {
		return result, err
	}
	lockValue, _ := reviewWorkspaceLocks.LoadOrStore(root, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	b.mu.Lock()
	busy := b.revertWorkspaceBusyLocked(p.ThreadID, root)
	busyCheck := b.revertBusyCheck
	b.mu.Unlock()
	if busy != "" {
		return result, errors.New(busy)
	}
	if busyCheck != nil {
		if reason := busyCheck(p.ThreadID, root); reason != "" {
			return result, errors.New(reason)
		}
	}
	snapshot, err := b.ReviewGet(ctx, wire.ThreadsReviewGetParams{ThreadID: p.ThreadID, Scope: p.Scope, TurnID: p.TurnID, Path: p.Path})
	if err != nil {
		return result, err
	}
	if snapshot.Unavailable != "" || snapshot.File == nil {
		return result, errors.New("审阅快照不可用")
	}
	if snapshot.BaseRevision != p.BaseRevision || snapshot.HeadRevision != p.HeadRevision || snapshot.Running {
		return result, errors.New("审阅快照已过期")
	}
	file := snapshot.File
	if file.Binary || file.TooLarge || file.Missing || file.OldText == "" || file.NewText == "" {
		return result, errors.New("该文件不支持撤销改动块")
	}
	for _, f := range snapshot.Files {
		if f.Path == p.Path && (f.Status == "A" || f.Status == "D" || f.Status == "R" || f.OrigPath != "") {
			return result, errors.New("新增、删除或重命名文件不支持撤销改动块")
		}
	}
	if got, e := git.RevertHunkHash(file.NewText, p.StartLine, p.LineCount); e != nil || got != p.HunkHash {
		return result, errors.New("审阅块已过期")
	}

	full := filepath.Join(root, filepath.FromSlash(p.Path))
	before, err := os.ReadFile(full)
	if err != nil {
		return result, err
	}
	if reviewHash(before) != p.ExpectedFileHash {
		return result, errors.New("工作区文件已改变")
	}
	if err := saveReviewRevertSnapshot(b, p.ThreadID, p.Path, before); err != nil {
		return result, err
	}
	newHash, err := git.RevertHunk(root, p.Path, file.OldText, file.NewText, p.StartLine, p.LineCount, p.HunkHash, p.ExpectedFileHash)
	if err != nil {
		return result, err
	}
	result.FileHash = newHash
	return result, nil
}

func reviewHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func saveReviewRevertSnapshot(b *MemoryBackend, threadID, path string, data []byte) error {
	dir := b.dataDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "mcode-review-snapshots")
	}
	dir = filepath.Join(dir, "review-revert", threadID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%x-%s", time.Now().UnixNano(), sha256.Sum256([]byte(path)), filepath.Base(path))
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 20 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries[:len(entries)-20] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

func reviewRevertParams(raw json.RawMessage) (wire.ThreadsReviewRevertHunkParams, error) {
	var p wire.ThreadsReviewRevertHunkParams
	if err := decodeExtParams(raw, &p); err != nil {
		return p, err
	}
	if !isValidUUID(p.ThreadID) {
		return p, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if p.Scope != wire.ReviewScopeTurn && p.Scope != wire.ReviewScopeSession && p.Scope != wire.ReviewScopeUncommitted {
		return p, ExtInvalidParams("Invalid params: 'scope' is invalid")
	}
	if p.Scope == wire.ReviewScopeTurn && p.TurnID != "" && !isValidUUID(p.TurnID) {
		return p, ExtInvalidParams("Invalid params: 'turnID' must be a valid UUID")
	}
	if p.Scope != wire.ReviewScopeTurn && p.TurnID != "" {
		return p, ExtInvalidParams("Invalid params: 'turnID' is only valid in turn scope")
	}
	if filepath.IsAbs(p.Path) || p.Path == "" || strings.HasPrefix(filepath.Clean(filepath.FromSlash(p.Path)), "..") {
		return p, ExtInvalidParams("Invalid params: 'path' is invalid")
	}
	if p.StartLine < 1 || p.LineCount < 0 {
		return p, ExtInvalidParams("Invalid params: hunk range is invalid")
	}
	return p, nil
}

func handleThreadsReviewRevertHunk(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	p, err := reviewRevertParams(raw)
	if err != nil {
		return nil, err
	}
	backend, ok := d.backend.(reviewHunkReverter)
	if !ok {
		return nil, ExtNotImplemented()
	}
	result, err := backend.ReviewRevertHunk(ctx, p)
	if err != nil {
		return nil, revertExtWrapError(err)
	}
	return result, nil
}
