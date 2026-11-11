package mcodeui

// fileManagerHasClosableTab 报告文件面板当前是否有可关闭的预览标签页。
// 面板没打开或没有激活的 tab 时没有可关的内容。
func fileManagerHasClosableTab(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	// 打开状态由右栏容器判断，需在拿 fileManagerMu 之前查询。
	open := isFileManagerOpen(s)
	if !open {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.active != ""
}

// fileManagerCloseActiveTab 关闭文件面板里当前激活的标签页，并切到相邻标签页。
// 关完最后一个标签页时回到文件树，面板本身仍然打开。
// 没有可关闭的标签页时返回 false，关闭成功返回 true。
//
// 调用方（关闭菜单项与 root.go 的动作分发）不需要改动。
func fileManagerCloseActiveTab(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	// 打开状态由右栏容器判断，需在拿 fileManagerMu 之前查询。
	if !isFileManagerOpen(s) {
		return false
	}
	fileManagerMu.Lock()
	if st.active == "" {
		fileManagerMu.Unlock()
		return false
	}
	path := st.active
	fileManagerMu.Unlock()

	s.closeFileManagerTab(path)
	return true
}
