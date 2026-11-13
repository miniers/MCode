package git

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

// Watcher 提供工作区变化代次（generation）的查询能力：
// 代次不变表示自上次查询以来工作区文件没有变化，上一次的快照结果仍然有效。
type Watcher interface {
	// Generation 返回当前工作区变化代次；实现会尽力确认此前发生的写入都已被计入。
	Generation(ctx context.Context) (uint64, error)
	// Close 释放监听资源，可重复调用。
	Close() error
}

// NewWatcher 为 root 创建工作区监听器：优先使用平台原生实现（macOS 为 FSEvents），
// 平台不支持或初始化失败时退回基于 mtime 哈希的轮询实现。
func NewWatcher(root string) Watcher {
	if w, err := newPlatformWatcher(root); err == nil && w != nil {
		return w
	}
	return newPollWatcher(root)
}

// pollWatcher 是 mtime 哈希退化实现：每次 Generation 遍历 root（跳过 .git 目录），
// 对「相对路径 + 文件大小 + 修改时间」做 FNV 哈希，与上次结果不同就让 generation 加一。
type pollWatcher struct {
	root string

	mu     sync.Mutex
	gen    uint64
	last   uint64
	hashed bool
	closed bool
}

// newPollWatcher 创建轮询监听器，初始 generation 为 1。
func newPollWatcher(root string) Watcher {
	return &pollWatcher{root: filepath.Clean(root), gen: 1}
}

// Generation 遍历工作区并比对 mtime 哈希，返回当前代次。
func (w *pollWatcher) Generation(ctx context.Context) (uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("watcher 已关闭")
	}
	sum, err := w.hashTree(ctx)
	if err != nil {
		return 0, err
	}
	if !w.hashed {
		w.last = sum
		w.hashed = true
	} else if sum != w.last {
		w.last = sum
		w.gen++
	}
	return w.gen, nil
}

// hashTree 计算工作区当前状态的 FNV-1a 哈希；跳过 .git 目录与不可访问的条目。
func (w *pollWatcher) hashTree(ctx context.Context) (uint64, error) {
	h := fnv.New64a()
	err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if path == w.root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || (d.Name() == "worktrees" && filepath.Base(filepath.Dir(path)) == ".mcode") {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(w.root, path)
		if rerr != nil {
			rel = path
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

// Close 关闭轮询监听器，可重复调用。
func (w *pollWatcher) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

// watchedSnapshot 记录某个工作区根目录最近一次快照的监听器、监听代次、HEAD 与 tree。
type watchedSnapshot struct {
	w    Watcher // 产生该代次的监听器：换用新监听器时旧缓存不可信
	gen  uint64
	head string
	tree string
}

var (
	watchedMu     sync.Mutex
	watchedByRoot = map[string]watchedSnapshot{}
)

// checkpointCommitEnv 是检查点提交使用的作者与提交者环境变量，与 Checkpoint 保持一致。
var checkpointCommitEnv = []string{
	"GIT_AUTHOR_NAME=mcode",
	"GIT_AUTHOR_EMAIL=mcode@localhost",
	"GIT_COMMITTER_NAME=mcode",
	"GIT_COMMITTER_EMAIL=mcode@localhost",
}

// CheckpointWatched 在监听器确认工作区未变化时复用上一次快照的 tree，
// 只做 commit-tree 与 update-ref；否则退回完整快照 r.Checkpoint。
// w 为 nil 或取代次失败时同样退回完整快照，保证调用方总能拿到提交。
func (r *Runner) CheckpointWatched(ctx context.Context, w Watcher, root, ref, message string) (string, error) {
	if w == nil {
		return r.Checkpoint(ctx, root, ref, message)
	}
	gen, err := w.Generation(ctx)
	if err != nil {
		return r.Checkpoint(ctx, root, ref, message)
	}

	// 当前 HEAD：空仓库时为空串，提交不再带父提交
	head := ""
	if out, herr := r.run(ctx, root, nil, "rev-parse", "-q", "--verify", "HEAD"); herr == nil {
		head = strings.TrimSpace(string(out))
	}

	key := filepath.Clean(root)
	watchedMu.Lock()
	cached, ok := watchedByRoot[key]
	watchedMu.Unlock()

	if ok && cached.w == w && cached.gen == gen && cached.head == head && cached.tree != "" {
		if commit, cerr := r.commitTree(ctx, root, ref, cached.tree, head, message); cerr == nil {
			return commit, nil
		}
		// 复用失败时退回完整快照，避免把错误暴露给调用方
	}

	commit, err := r.Checkpoint(ctx, root, ref, message)
	if err != nil {
		return "", err
	}
	if out, terr := r.run(ctx, root, nil, "rev-parse", commit+"^{tree}"); terr == nil {
		tree := strings.TrimSpace(string(out))
		watchedMu.Lock()
		watchedByRoot[key] = watchedSnapshot{w: w, gen: gen, head: head, tree: tree}
		watchedMu.Unlock()
	}
	return commit, nil
}

// commitTree 基于已有 tree 直接生成提交并更新引用，不触碰工作区与暂存区。
func (r *Runner) commitTree(ctx context.Context, root, ref, tree, head, message string) (string, error) {
	if message == "" {
		message = "mcode checkpoint"
	}
	args := []string{"commit-tree", tree, "-m", message}
	if head != "" {
		args = append(args, "-p", head)
	}
	out, err := r.run(ctx, root, checkpointCommitEnv, args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if _, err := r.run(ctx, root, checkpointCommitEnv, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}
