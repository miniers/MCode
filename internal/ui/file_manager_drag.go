package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const fileTreeHoverExpandDelay = 600 * time.Millisecond

// fileTreeDragPayload 的切片独立于树缓存，拖动开始后不再修改。
type fileTreeDragPayload struct {
	ThreadID string
	Identity string
	Paths    []string
}

func fileTreeDragReference(s *chatState, threadID string, entry FileEntry) (fileTreeDragPayload, bool) {
	ref := inspectorFileReference(s, threadID, entry.Path, 0, 0)
	payload := fileTreeDragPayload{ThreadID: threadID, Identity: ref.Identity}
	st := getFileManagerState(s)
	if st == nil || entry.Protected {
		return payload, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.threadID != threadID {
		return payload, false
	}
	tree := ensureFileTreeLocked(st)
	outline := &tree.outline
	selection := &tree.selection
	if tree.onlyChanged {
		outline = fileTreeChangedOutlineLocked(tree)
		selection = &tree.changedSelection
	}
	allowed := func(path string) bool {
		if tree.onlyChanged && tree.git[path].Status == "D" {
			return false
		}
		for ancestor := path; ancestor != ""; ancestor = fileManagerParentPath(ancestor) {
			if tree.entries[ancestor].Protected {
				return false
			}
		}
		return true
	}
	if !allowed(entry.Path) {
		return payload, false
	}
	if selection.Has(entry.Path) {
		for row := 0; row < outline.Rows(); row++ {
			path := outline.Item(row)
			if selection.Has(path) && allowed(path) {
				payload.Paths = append(payload.Paths, path)
			}
		}
	} else {
		payload.Paths = []string{entry.Path}
	}
	return payload, len(payload.Paths) > 0
}

func (p fileTreeDragPayload) refs() []inspectorFileRef {
	refs := make([]inspectorFileRef, 0, len(p.Paths))
	for _, path := range p.Paths {
		refs = append(refs, inspectorFileRef{ThreadID: p.ThreadID, Identity: p.Identity, Path: path})
	}
	return refs
}

func composerInspectorDrop(s *chatState, target ui.Element) bool {
	payload, over := ui.DragOver[fileTreeDragPayload](target)
	if over {
		threadID, root, main := getSelectedThreadDirs(s)
		over = payload.ThreadID == threadID && payload.Identity == root+"\x00"+main
	}
	if payload, ok := ui.Drop[fileTreeDragPayload](target); ok {
		s.addInspectorFileRefs(payload.refs())
	}
	return over
}

// fileTreeDragPayloadMatchesTree 校验拖拽载荷的会话与工作区身份是否与当前树一致。
func fileTreeDragPayloadMatchesTree(s *chatState, threadID string, payload fileTreeDragPayload) bool {
	if s == nil || threadID == "" {
		return false
	}
	curThreadID, root, main := getSelectedThreadDirs(s)
	return payload.ThreadID == threadID &&
		payload.ThreadID == curThreadID &&
		payload.Identity == root+"\x00"+main
}

// fileTreePlanMove 纯函数校验并计算树内拖放移动计划：
// 返回最终要移动的路径列表；若不合法，返回空切片与原因；
// 若所有路径的父目录都已是目标目录，则返回空切片与空原因（不移动、不高亮、不提示）。
func fileTreePlanMove(paths []string, targetDir string, entries map[string]FileEntry, children map[string][]string) ([]string, string) {
	targetDir = strings.Trim(strings.TrimSpace(targetDir), "/")
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p != "" {
			normalized = append(normalized, p)
		}
	}
	collapsed := fileManagerCollapseNestedPaths(normalized)
	if len(collapsed) == 0 {
		return nil, ""
	}

	for anc := targetDir; anc != ""; anc = fileManagerParentPath(anc) {
		if entries[anc].Protected {
			return nil, "不能移动到受保护的文件夹"
		}
	}
	if targetDir != "" {
		if entry, ok := entries[targetDir]; ok && !entry.IsDir {
			return nil, "目标不是文件夹"
		}
	}

	for _, p := range collapsed {
		if fileManagerPathUnder(targetDir, p) {
			return nil, "不可移入自身或子目录"
		}
	}

	movePaths := make([]string, 0, len(collapsed))
	for _, p := range collapsed {
		if fileManagerParentPath(p) == targetDir {
			continue
		}
		movePaths = append(movePaths, p)
	}
	if len(movePaths) == 0 {
		return nil, ""
	}

	existing := make(map[string]bool, len(children[targetDir]))
	for _, child := range children[targetDir] {
		existing[fileManagerTabName(child)] = true
	}
	seenNames := make(map[string]bool, len(movePaths))
	for _, p := range movePaths {
		name := fileManagerTabName(p)
		if existing[name] || seenNames[name] {
			return nil, "目标文件夹已有同名项：" + name
		}
		seenNames[name] = true
	}
	return movePaths, ""
}

// fileTreeEvaluateDrop 校验树内放置载荷并返回移动计划；matched 为 false 表示不属于当前树。
func fileTreeEvaluateDrop(s *chatState, threadID string, payload fileTreeDragPayload, targetDir string) (movePaths []string, reason string, valid bool, matched bool) {
	if !fileTreeDragPayloadMatchesTree(s, threadID, payload) {
		return nil, "", false, false
	}
	st := getFileManagerState(s)
	if st == nil {
		return nil, "", false, false
	}
	fileManagerMu.Lock()
	if st.threadID != threadID || st.tree == nil || st.tree.onlyChanged || st.rename.active {
		fileManagerMu.Unlock()
		return nil, "", false, false
	}
	kids := st.tree.rawChildren[targetDir]
	if kids == nil {
		kids = st.tree.children[targetDir]
	}
	targetChildren := map[string][]string{targetDir: kids}
	movePaths, reason = fileTreePlanMove(payload.Paths, targetDir, st.tree.entries, targetChildren)
	fileManagerMu.Unlock()
	return movePaths, reason, len(movePaths) > 0 && reason == "", true
}

// moveFileTreePaths 在后台按顺序把 paths 移动到 targetDir，复用重命名后处理并同步展开与选区。
func (s *chatState) moveFileTreePaths(threadID string, paths []string, targetDir string) {
	if s == nil || threadID == "" || len(paths) == 0 {
		return
	}
	client, ok := fileOpsClient(s)
	if !ok {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	sessionGen := st.gen
	fileManagerMu.Unlock()

	moveList := append([]string(nil), paths...)
	go func(tid string, gen int, items []string, dst string) {
		ctx := context.Background()
		moved := make([]string, 0, len(items))
		for _, oldPath := range items {
			newPath := fileManagerJoinPath(dst, fileManagerTabName(oldPath))
			renamed, err := performFileManagerRename(ctx, client, tid, oldPath, newPath)
			if err != nil {
				s.setToast(fmt.Sprintf("已移动 %d 项，%s 失败：%s", len(moved), oldPath, err.Error()))
				return
			}
			fileManagerMu.Lock()
			sameSession := st.threadID == tid && st.gen == gen
			fileManagerMu.Unlock()
			if !sameSession {
				return
			}
			moved = append(moved, renamed)
			s.applyFileManagerRename(tid, oldPath, renamed)
		}

		needLoadTarget := false
		fileManagerMu.Lock()
		if st.threadID == tid && st.gen == gen {
			tree := ensureFileTreeLocked(st)
			if dst != "" {
				for _, anc := range fileTreeAncestors(dst) {
					if anc != "" {
						tree.outline.Open.Add(anc)
					}
				}
				st.dir = dst
				needLoadTarget = tree.children[dst] == nil && !tree.loading[dst]
			}
			tree.selection.Clear()
			for _, p := range moved {
				tree.selection.Add(p)
			}
			if len(moved) > 0 {
				tree.selected = fileTreeRowIndexLocked(tree, moved[0])
				if len(moved) == 1 {
					tree.revealPath = moved[0]
				}
			}
			fileTreeBumpOutlineGenLocked(tree)
		}
		fileManagerMu.Unlock()

		if needLoadTarget {
			s.fileTreeLoadDir(tid, dst)
		}
		inspectorRemember(s)
		s.invalidate()
	}(threadID, sessionGen, moveList, targetDir)
}
