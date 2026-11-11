package mcodeui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo/ui"
)

// fileManagerOpenGOOS 是外部打开使用的平台标识，测试中可替换；
// 「在访达中显示」和「用默认应用打开」依赖 macOS 的 open 命令。
var fileManagerOpenGOOS = runtime.GOOS

// fileManagerOpenRunner 执行 macOS 的 open 命令（在访达中显示 / 用默认应用打开），
// 只在 fileManagerOpenGOOS 为 darwin 时被调用。包级变量方便测试替换。
var fileManagerOpenRunner = func(args ...string) error {
	cmd := exec.Command("open", args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

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

// fileManagerLocalPath 判断绝对路径在本机是否存在（远程工作区的文件本机没有）。
func fileManagerLocalPath(abs string) bool {
	if abs == "" {
		return false
	}
	_, err := os.Stat(abs)
	return err == nil
}

// fileManagerCanRevealLocally 判断条目能否用系统文件管理器打开：
// 只有 macOS 且路径在本机存在时才支持。
func fileManagerCanRevealLocally(abs string) bool {
	return fileManagerOpenGOOS == "darwin" && fileManagerLocalPath(abs)
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
func fileManagerEntryMenu(c *ui.Context, s *chatState, threadID string, entry FileEntry) func(*ui.Menu) {
	payload, canReference := fileTreeDragReference(s, threadID, entry)
	return func(m *ui.Menu) {
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
		}
		if m.Item("引用到输入框").Disabled(!canReference).Chosen() {
			s.addInspectorFileRefs(payload.refs())
		}

		abs := fileManagerWorkspacePath(s, threadID, entry.Path)
		if m.Item("复制路径").Chosen() && abs != "" {
			c.WriteClipboard(abs)
			s.setToast("已复制路径")
		}
		if m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(entry.Path)
			s.setToast("已复制相对路径")
		}
		// 远程工作区里的文件本机不存在、非 macOS 没有 open 命令，不提供外部打开。
		if fileManagerCanRevealLocally(abs) {
			if m.Item("在访达中显示").Chosen() {
				_ = fileManagerOpenRunner("-R", abs)
			}
			if !entry.IsDir {
				if m.Item("用默认应用打开").Chosen() {
					_ = fileManagerOpenRunner(abs)
				}
			}
		}

		// B 档写操作：客户端不支持或条目受保护时整组不显示。
		if !fileManagerEntryEditable(s, entry) {
			return
		}
		m.Separator()
		if entry.IsDir {
			if m.Item("在此新建文件…").Chosen() {
				openFileManagerNewDialog(s, entry.Path, false)
			}
			if m.Item("在此新建文件夹…").Chosen() {
				openFileManagerNewDialog(s, entry.Path, true)
			}
		}
		if m.Item("重命名…").Chosen() {
			openFileManagerRenameDialog(s, entry.Path)
		}
		if !entry.IsDir {
			if m.Item("复制一份").Chosen() {
				s.duplicateFileManagerEntry(entry.Path)
			}
		}
		if m.Item("移到废纸篓…").Danger().Chosen() {
			openFileManagerTrashDialog(s, entry.Path)
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
		if m.Item("复制路径").Chosen() && abs != "" {
			c.WriteClipboard(abs)
			s.setToast("已复制路径")
		}
		if m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(tab.path)
			s.setToast("已复制相对路径")
		}
		if fileManagerCanRevealLocally(abs) {
			if m.Item("在访达中显示").Chosen() {
				_ = fileManagerOpenRunner("-R", abs)
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
