package mcodeui

import (
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// inspectorFileRef 是发起引用时的来源快照，行号从 1 开始，0 表示整个文件或目录。
type inspectorFileRef struct {
	ThreadID string
	Identity string
	Path     string
	First    int
	Last     int
}

func inspectorFileReference(s *chatState, threadID, path string, first, last int) inspectorFileRef {
	_, root, main := getSelectedThreadDirs(s)
	return inspectorFileRef{ThreadID: threadID, Identity: root + "\x00" + main, Path: path, First: first, Last: last}
}

func (s *chatState) addInspectorFileRefs(refs []inspectorFileRef) {
	threadID, root, main := getSelectedThreadDirs(s)
	identity := root + "\x00" + main
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	panelThreadID := st.threadID
	fileManagerMu.Unlock()
	if panelThreadID != threadID {
		return
	}
	for _, ref := range refs {
		if ref.ThreadID == "" || ref.ThreadID != threadID || ref.Identity != identity || root == "" ||
			!utf8.ValidString(ref.Path) || !fs.ValidPath(ref.Path) || ref.Path == "." || strings.ContainsAny(ref.Path, "\\\x00\r\n") ||
			ref.First < 0 || ref.Last < 0 || (ref.First == 0 && ref.Last != 0) || (ref.First > 0 && ref.Last < ref.First) {
			continue
		}
		protected := false
		for _, part := range strings.Split(ref.Path, "/") {
			protected = protected || part == ".git"
		}
		fileManagerMu.Lock()
		if st.tree != nil {
			for path := ref.Path; path != ""; path = fileManagerParentPath(path) {
				protected = protected || st.tree.entries[path].Protected
			}
			protected = protected || (st.tree.onlyChanged && st.tree.git[ref.Path].Status == "D")
		}
		fileManagerMu.Unlock()
		if protected {
			continue
		}
		name := ref.Path
		content := fmt.Sprintf("工作区相对路径：%q\n", ref.Path)
		if ref.First > 0 {
			name += fmt.Sprintf(":%d-%d", ref.First, ref.Last)
			content += fmt.Sprintf("行范围：%d-%d（从 1 开始，含首尾）\n", ref.First, ref.Last)
		}
		s.addAttachmentToThread(ref.ThreadID, name, "text/plain", []byte(content), &ref)
	}
}
