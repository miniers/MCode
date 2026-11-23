package mcodeui

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	inspectorRefPathPrefix  = "工作区相对路径："
	inspectorRefRangePrefix = "行范围："
	inspectorRefRangeSuffix = "（从 1 开始，含首尾）"
)

// isValidInspectorRefPath 校验工作区相对路径是否安全合法。
func isValidInspectorRefPath(path string) bool {
	return utf8.ValidString(path) && fs.ValidPath(path) && path != "." && !strings.ContainsAny(path, "\\\x00\r\n")
}

// inspectorRefContent 生成工作区文件引用的标准化文本。
func inspectorRefContent(path string, first, last int) string {
	content := fmt.Sprintf("%s%q\n", inspectorRefPathPrefix, path)
	if first > 0 {
		content += fmt.Sprintf("%s%d-%d%s\n", inspectorRefRangePrefix, first, last, inspectorRefRangeSuffix)
	}
	return content
}

// parseInspectorRefContent 解析引用文本中的工作区相对路径与可选行范围，第二行之后的内容忽略。
func parseInspectorRefContent(data []byte) (path string, first, last int, ok bool) {
	line1, rest, hasRest := strings.Cut(string(data), "\n")
	line1 = strings.TrimSuffix(line1, "\r")
	quoted, hasPrefix := strings.CutPrefix(line1, inspectorRefPathPrefix)
	if !hasPrefix {
		return "", 0, 0, false
	}
	unquoted, err := strconv.Unquote(quoted)
	if err != nil || !isValidInspectorRefPath(unquoted) {
		return "", 0, 0, false
	}
	if hasRest {
		line2, _, _ := strings.Cut(rest, "\n")
		line2 = strings.TrimSuffix(line2, "\r")
		if rangeSpec, hasRange := strings.CutPrefix(line2, inspectorRefRangePrefix); hasRange {
			var f, l int
			if n, err := fmt.Sscanf(rangeSpec, "%d-%d", &f, &l); err == nil && n == 2 && f > 0 && l >= f {
				first, last = f, l
			}
		}
	}
	return unquoted, first, last, true
}

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
			!isValidInspectorRefPath(ref.Path) ||
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
		if ref.First > 0 {
			name += fmt.Sprintf(":%d-%d", ref.First, ref.Last)
		}
		content := inspectorRefContent(ref.Path, ref.First, ref.Last)
		s.addAttachmentToThread(ref.ThreadID, name, "text/plain", []byte(content), &ref)
	}
}
