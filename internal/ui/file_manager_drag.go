package mcodeui

import "github.com/egoist/mygo/ui"

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
