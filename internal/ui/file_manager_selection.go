package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// fileTreeActiveViewsLocked 返回当前视图（普通树或只看改动）的 Outline、选区、焦点下标与条目映射。
// 调用方持 fileManagerMu。
func fileTreeActiveViewsLocked(tree *fileTreeState) (*ui.OutlineState[string], *ui.Selection[string], *int, map[string]FileEntry) {
	if tree.onlyChanged {
		_, _, entries := fileTreeChangedRowsLocked(tree)
		return fileTreeChangedOutlineLocked(tree), &tree.changedSelection, &tree.changedSelected, entries
	}
	return &tree.outline, &tree.selection, &tree.selected, tree.entries
}

// fileTreeEntryProtectedLocked 判断 path 本身或其任一祖先目录是否受保护。
// 调用方持 fileManagerMu。
func fileTreeEntryProtectedLocked(tree *fileTreeState, entries map[string]FileEntry, path string) bool {
	for ancestor := path; ancestor != ""; ancestor = fileManagerParentPath(ancestor) {
		if entries[ancestor].Protected || tree.entries[ancestor].Protected {
			return true
		}
	}
	return false
}

// fileTreeEntrySelectableLocked 判断条目是否可参与批量操作（非受保护、非只看改动中的已删除项）。
// 调用方持 fileManagerMu。
func fileTreeEntrySelectableLocked(tree *fileTreeState, entries map[string]FileEntry, path string) bool {
	if path == "" {
		return false
	}
	if fileTreeEntryProtectedLocked(tree, entries, path) {
		return false
	}
	if tree.onlyChanged && tree.git[path].Status == "D" {
		return false
	}
	return true
}

// fileTreeSelectionSnapshotLocked 按树中当前显示顺序返回选区条目、是否含受保护项与批量菜单判定所需信息。
// 调用方持 fileManagerMu。
func fileTreeSelectionSnapshotLocked(tree *fileTreeState) (entries []FileEntry, hasProtected bool) {
	if tree == nil {
		return nil, false
	}
	outline, selection, _, entryMap := fileTreeActiveViewsLocked(tree)
	if selection.Len() == 0 {
		return nil, false
	}
	for row := 0; row < outline.Rows(); row++ {
		path := outline.Item(row)
		if !selection.Has(path) {
			continue
		}
		entry, ok := entryMap[path]
		if !ok {
			continue
		}
		if fileTreeEntryProtectedLocked(tree, entryMap, path) {
			hasProtected = true
			continue
		}
		entries = append(entries, entry)
	}
	return entries, hasProtected
}

// fileTreeSelectionEntries 按当前视图树中的显示顺序返回选中的非受保护条目列表。
func fileTreeSelectionEntries(s *chatState) (entries []FileEntry, hasProtected bool) {
	st := getFileManagerState(s)
	if st == nil {
		return nil, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return nil, false
	}
	return fileTreeSelectionSnapshotLocked(st.tree)
}

// fileTreeSelectionBarStatsLocked 只遍历当前选区与已加载缓存统计项数与文件总大小，不遍历整棵树。
// 调用方持 fileManagerMu。
func fileTreeSelectionBarStatsLocked(tree *fileTreeState) (count int, totalSize int64, hasDir bool, hasProtected bool) {
	if tree == nil {
		return 0, 0, false, false
	}
	selection := &tree.selection
	entries := tree.entries
	if tree.onlyChanged {
		selection = &tree.changedSelection
		if tree.changedCacheOK {
			entries = tree.changedEntries
		} else {
			_, _, entries = fileTreeChangedRowsLocked(tree)
		}
	}
	for path := range selection.All() {
		entry, ok := entries[path]
		if !ok {
			continue
		}
		count++
		if fileTreeEntryProtectedLocked(tree, entries, path) {
			hasProtected = true
		}
		if entry.IsDir {
			hasDir = true
		} else {
			totalSize += entry.Size
		}
	}
	return count, totalSize, hasDir, hasProtected
}

// fileTreeSelectAll 全选当前视图的所有可见行（跳过受保护条目）。
func (s *chatState) fileTreeSelectAll() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	outline, selection, selected, entries := fileTreeActiveViewsLocked(tree)
	selection.Clear()
	firstIdx := -1
	for row := 0; row < outline.Rows(); row++ {
		path := outline.Item(row)
		if fileTreeEntryProtectedLocked(tree, entries, path) {
			continue
		}
		selection.Add(path)
		if firstIdx < 0 {
			firstIdx = row
		}
	}
	if (*selected < 0 || *selected >= outline.Rows()) && firstIdx >= 0 {
		*selected = firstIdx
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// fileTreeCollapseSelection 把选区收回到只剩当前焦点行；没有有效焦点行时清空选区。
func (s *chatState) fileTreeCollapseSelection() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	outline, selection, selected, _ := fileTreeActiveViewsLocked(tree)
	selection.Clear()
	if *selected >= 0 && *selected < outline.Rows() {
		selection.Add(outline.Item(*selected))
	} else {
		*selected = -1
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// fileTreeFocusedPathLocked 返回当前焦点行的路径和条目。调用方持 fileManagerMu。
func fileTreeFocusedPathLocked(tree *fileTreeState) (string, FileEntry, bool) {
	if tree == nil {
		return "", FileEntry{}, false
	}
	outline, selection, selected, entries := fileTreeActiveViewsLocked(tree)
	if *selected >= 0 && *selected < outline.Rows() {
		path := outline.Item(*selected)
		if entry, ok := entries[path]; ok {
			return path, entry, true
		}
	}
	if selection.Len() == 1 {
		for path := range selection.All() {
			if entry, ok := entries[path]; ok {
				return path, entry, true
			}
		}
	}
	return "", FileEntry{}, false
}

// fileTreeSelectionDragReference 按显示顺序为选区生成可引用到输入框的 payload（跳过受保护与已删除条目）。
func fileTreeSelectionDragReference(s *chatState, threadID string) (fileTreeDragPayload, bool) {
	ref := inspectorFileReference(s, threadID, "", 0, 0)
	payload := fileTreeDragPayload{ThreadID: threadID, Identity: ref.Identity}
	st := getFileManagerState(s)
	if st == nil {
		return payload, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.threadID != threadID {
		return payload, false
	}
	tree := ensureFileTreeLocked(st)
	outline, selection, _, entries := fileTreeActiveViewsLocked(tree)
	for row := 0; row < outline.Rows(); row++ {
		path := outline.Item(row)
		if selection.Has(path) && fileTreeEntrySelectableLocked(tree, entries, path) {
			payload.Paths = append(payload.Paths, path)
		}
	}
	return payload, len(payload.Paths) > 0
}

// fileTreeSelectionPaths 返回选区中用于复制路径或批量删除的路径列表（按树显示顺序）。
func fileTreeSelectionPaths(s *chatState) (allPaths []string, actionablePaths []string, hasProtected bool) {
	st := getFileManagerState(s)
	if st == nil {
		return nil, nil, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return nil, nil, false
	}
	outline, selection, _, entries := fileTreeActiveViewsLocked(st.tree)
	for row := 0; row < outline.Rows(); row++ {
		path := outline.Item(row)
		if !selection.Has(path) {
			continue
		}
		allPaths = append(allPaths, path)
		if fileTreeEntryProtectedLocked(st.tree, entries, path) {
			hasProtected = true
			continue
		}
		actionablePaths = append(actionablePaths, path)
	}
	return allPaths, actionablePaths, hasProtected
}

// fileTreeTrashSelection 对当前选区打开废纸篓确认弹窗；包含受保护条目或无可用写操作客户端时不触发。
func (s *chatState) fileTreeTrashSelection() bool {
	if _, ok := fileOpsClient(s); !ok {
		return false
	}
	_, actionable, hasProtected := fileTreeSelectionPaths(s)
	if hasProtected || len(actionable) == 0 {
		return false
	}
	openFileManagerBatchTrashDialog(s, actionable)
	return true
}

// fileTreeStartInlineRenameFocused 对当前焦点行进入行内重命名。
func (s *chatState) fileTreeStartInlineRenameFocused() bool {
	if _, ok := fileOpsClient(s); !ok {
		return false
	}
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	path, entry, ok := fileTreeFocusedPathLocked(tree)
	deleted := ok && tree.onlyChanged && tree.git[path].Status == "D"
	protected := ok && fileTreeEntryProtectedLocked(tree, tree.entries, path)
	if ok && tree.onlyChanged {
		_, _, changedEntries := fileTreeChangedRowsLocked(tree)
		protected = fileTreeEntryProtectedLocked(tree, changedEntries, path)
	}
	fileManagerMu.Unlock()
	if !ok || deleted || protected || entry.Protected {
		return false
	}
	s.startFileManagerInlineRename(path)
	return true
}

// fileTreeSelectionContainsLocked 判断 path 是否在当前视图的选区中，且选区至少有 2 项。
// 调用方持 fileManagerMu。
func fileTreeSelectionContainsLocked(st *fileManagerState, path string) bool {
	if st == nil || st.tree == nil || st.query != "" || path == "" {
		return false
	}
	_, selection, _, _ := fileTreeActiveViewsLocked(st.tree)
	return selection.Len() >= 2 && selection.Has(path)
}

// fileManagerJoinClipboardPaths 用换行符拼接多条路径。
func fileManagerJoinClipboardPaths(paths []string) string {
	return strings.Join(paths, "\n")
}
