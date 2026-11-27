package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var (
	fileManagerGOOS        = runtime.GOOS
	fileManagerPreviewItem = func(abs string) error { return mygo.Shell.PreviewItem(abs) }
	fileManagerOpenDefault = func(abs string) error { return mygo.Shell.OpenPath(abs) }
)

// fileManagerEntryEditable 判断条目能否做写操作：受保护条目和服务端不支持文件操作时都不行。
func fileManagerEntryEditable(s *chatState, entry FileEntry) bool {
	if entry.Protected {
		return false
	}
	_, ok := fileOpsClient(s)
	return ok
}

// fileManagerWorkspacePath 把相对路径拼成工作区绝对路径；拿不到工作目录时返回空串。
func fileManagerWorkspacePath(s *chatState, threadID, relPath string) string {
	workDir := turnFilesWorkDir(s, threadID)
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, filepath.FromSlash(relPath))
}

// gitRelativeFilePath 将绝对文件路径限制在仓库根目录内。
func gitRelativeFilePath(root, file string) (string, bool) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(file) {
		return "", false
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// fileManagerLocalPath 判断绝对路径在本机是否存在（远程工作区的文件本机没有）。
func fileManagerLocalPath(abs string) bool {
	if abs == "" {
		return false
	}
	_, err := os.Stat(abs)
	return err == nil
}

// fileManagerLocation 返回面板当前绑定的会话与目录。
func fileManagerLocation(s *chatState) (threadID, dir string) {
	st := getFileManagerState(s)
	if st == nil {
		return "", ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.threadID, st.dir
}

// openFileManagerInSplit 在右侧分栏打开文件：已经有打开的 tab 且没拆分时先拆一栏，
// 还没有任何 tab 时直接打开。
func openFileManagerInSplit(s *chatState, threadID, path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	needSplit := st.active != "" && len(st.tabs) > 0 && st.splitAnchor == ""
	fileManagerMu.Unlock()
	if needSplit {
		s.splitFileManager()
	}
	s.openFileManagerFile(threadID, path, true)
}

// fileManagerEntryMenu 返回文件 / 目录行的菜单：行右键与行尾「更多操作」按钮共用。
// 若目标条目位于多选选区内且选区达到 2 项或更多，则展示批量菜单；否则按单条菜单展示。
func fileManagerEntryMenu(c *ui.Context, s *chatState, threadID string, entry FileEntry) func(*ui.Menu) {
	return func(m *ui.Menu) {
		st := getFileManagerState(s)
		isBatch := false
		if st != nil {
			fileManagerMu.Lock()
			isBatch = fileTreeSelectionContainsLocked(st, entry.Path)
			fileManagerMu.Unlock()
		}
		if isBatch {
			batchPayload, canBatchRef := fileTreeSelectionDragReference(s, threadID)
			allPaths, actionablePaths, hasProtected := fileTreeSelectionPaths(s)
			absPaths := make([]string, 0, len(allPaths))
			for _, rel := range allPaths {
				if abs := fileManagerWorkspacePath(s, threadID, rel); abs != "" {
					absPaths = append(absPaths, abs)
				}
			}

			m.Item("打开").Disabled(true).Trailing("仅单选")
			m.Item("重命名…").Disabled(true).Trailing("仅单选")
			m.Separator()
			if m.Item("引用到输入框").Disabled(!canBatchRef).Chosen() {
				s.addInspectorFileRefs(batchPayload.refs())
			}
			if m.Item("复制路径").Disabled(len(absPaths) == 0).Chosen() {
				c.WriteClipboard(fileManagerJoinClipboardPaths(absPaths))
				s.setToast(fmt.Sprintf("已复制 %d 条路径", len(absPaths)))
			}
			if m.Item("复制相对路径").Disabled(len(allPaths) == 0).Chosen() {
				c.WriteClipboard(fileManagerJoinClipboardPaths(allPaths))
				s.setToast(fmt.Sprintf("已复制 %d 条相对路径", len(allPaths)))
			}
			if _, ok := fileOpsClient(s); ok {
				m.Separator()
				if m.Item(trashActionLabel()+"…").Danger().Disabled(hasProtected || len(actionablePaths) == 0).Shortcut(ui.Cmd, ui.KeyBackspace).Chosen() {
					openFileManagerBatchTrashDialog(s, actionablePaths)
				}
			}
			return
		}

		payload, canReference := fileTreeDragReference(s, threadID, entry)
		abs := fileManagerWorkspacePath(s, threadID, entry.Path)
		root := turnFilesWorkDir(s, threadID)
		if ps := getGitPaneState(s); ps != nil && ps.threadID == threadID {
			ps.mu.Lock()
			if ps.state.WorkDir != "" {
				root = ps.state.WorkDir
			}
			ps.mu.Unlock()
		}
		rel, inGitRepo := gitRelativeFilePath(root, abs)
		local := fileManagerLocalPath(abs)
		editable := fileManagerEntryEditable(s, entry)
		sep := false

		// 打开
		if entry.IsDir {
			if m.Item("打开").Chosen() {
				s.loadFileManagerDir(threadID, entry.Path)
			}
		} else {
			if m.Item("打开").Chosen() {
				s.openFileManagerFile(threadID, entry.Path, true)
			}
			if m.Item("在右侧分栏打开").Chosen() {
				openFileManagerInSplit(s, threadID, entry.Path)
			}
			if local && fileManagerGOOS == "darwin" && m.Item("快速查看").Chosen() {
				_ = fileManagerPreviewItem(abs)
			}
		}
		sep = true

		// 外部：远程工作区里的文件本机不存在，不提供外部打开。
		if local {
			if sep {
				m.Separator()
			}
			sep = true
			if !entry.IsDir {
				if m.Item("在外部编辑器打开").Chosen() {
					s.openFileAtLocation(fileJumpRequest{
						Path:     abs,
						ThreadID: threadID,
						Target:   fileJumpExternal,
					})
				}
				if m.Item("用默认应用打开").Chosen() {
					_ = fileManagerOpenDefault(abs)
				}
			}
			if m.Item(revealInFileManagerLabel()).Chosen() {
				openItemInFinder(abs)
			}
		}

		// 引用与复制
		if sep {
			m.Separator()
		}
		sep = true
		if m.Item("引用到输入框").Disabled(!canReference).Chosen() {
			s.addInspectorFileRefs(payload.refs())
		}
		if m.Item("复制路径").Disabled(abs == "").Chosen() {
			c.WriteClipboard(abs)
			s.setToast("已复制路径")
		}
		if m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(entry.Path)
			s.setToast("已复制相对路径")
		}

		// Git
		hasGitHistory := !entry.IsDir && inGitRepo
		hasGitIgnore := inGitRepo && local && editable
		if hasGitHistory || hasGitIgnore {
			if sep {
				m.Separator()
			}
			sep = true
			if hasGitHistory {
				if m.Item("文件历史").Chosen() {
					inspectorSelect(s, sidePaneGitID, false)
					if ps := getGitPaneState(s); ps != nil {
						openGitFileHistory(ps, rel)
					}
				}
				if m.Item("逐行追溯").Chosen() {
					inspectorSelect(s, sidePaneGitID, false)
					if ps := getGitPaneState(s); ps != nil {
						openGitBlame(ps, rel)
					}
				}
			}
			if hasGitIgnore {
				if m.Item("从 Git 中忽略").Chosen() {
					if err := gitChangesIgnore(root, rel); err != nil {
						s.setToast("加入 .gitignore 失败: " + err.Error())
					} else {
						s.setToast("已加入 .gitignore")
					}
				}
			}
		}

		// 编辑（仅可编辑时显示）
		if editable {
			if sep {
				m.Separator()
			}
			sep = true
			if entry.IsDir {
				if m.Item("在此新建文件…").Chosen() {
					openFileManagerNewDialog(s, entry.Path, false)
				}
				if m.Item("在此新建文件夹…").Chosen() {
					openFileManagerNewDialog(s, entry.Path, true)
				}
			}
			if m.Item("重命名…").Chosen() {
				s.startFileManagerInlineRename(entry.Path)
			}
			if !entry.IsDir {
				if m.Item("复制一份").Chosen() {
					s.duplicateFileManagerEntry(entry.Path)
				}
			}

			// 危险
			m.Separator()
			if m.Item(trashActionLabel() + "…").Danger().Chosen() {
				openFileManagerTrashDialog(s, entry.Path)
			}
		}
	}
}

// fileManagerTabMenu 返回预览 tab 的菜单：关闭、固定与复制路径。
func fileManagerTabMenu(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot) func(*ui.Menu) {
	return func(m *ui.Menu) {
		// 关闭右侧只在不是最后一个 tab 时可用。
		lastIndex := -1
		for i, other := range snap.tabs {
			if other.path == tab.path {
				lastIndex = i
			}
		}
		if m.Item("关闭").Chosen() {
			s.closeFileManagerTab(tab.path)
		}
		if m.Item("关闭其他").Chosen() {
			s.closeOtherFileManagerTabs(tab.path)
		}
		if m.Item("关闭右侧").Disabled(lastIndex < 0 || lastIndex == len(snap.tabs)-1).Chosen() {
			s.closeFileManagerTabsRightOf(tab.path)
		}
		if m.Item("全部关闭").Chosen() {
			s.closeAllFileManagerTabs()
		}
		m.Separator()
		if tab.temporary {
			if m.Item("固定").Chosen() {
				s.pinFileManagerTab(tab.path)
			}
		}

		abs := fileManagerWorkspacePath(s, fileManagerThreadID(s), tab.path)
		if m.Item("复制路径").Disabled(abs == "").Chosen() {
			c.WriteClipboard(abs)
			s.setToast("已复制路径")
		}
		if m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(tab.path)
			s.setToast("已复制相对路径")
		}
		if fileManagerLocalPath(abs) {
			if m.Item(revealInFileManagerLabel()).Chosen() {
				openItemInFinder(abs)
			}
		}
	}
}

// fileManagerDirMenu 是列表头部「新建」按钮与列表空白处的菜单：新建与刷新。
// 客户端不支持文件操作时只留刷新。
func fileManagerDirMenu(c *ui.Context, s *chatState) func(*ui.Menu) {
	return func(m *ui.Menu) {
		threadID, dir := fileManagerLocation(s)
		if _, ok := fileOpsClient(s); ok {
			if m.Item("新建文件…").Chosen() {
				openFileManagerNewDialog(s, dir, false)
			}
			if m.Item("新建文件夹…").Chosen() {
				openFileManagerNewDialog(s, dir, true)
			}
			m.Separator()
		}
		if m.Item("刷新").Chosen() {
			s.loadFileManagerDir(threadID, dir)
		}
	}
}
