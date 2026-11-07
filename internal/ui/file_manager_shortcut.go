package mcodeui

// fileManagerHasClosableTab 报告文件面板当前是否有可关闭的预览标签页。
// 面板没打开、停在目录列表或没有激活的 tab 时没有可关的内容。
func fileManagerHasClosableTab(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.open && !st.showList && st.active != ""
}

// fileManagerCloseActiveTab 关闭文件面板里当前激活的标签页，并切到相邻标签页。
// 关掉的若是外部入口（聊天文件卡片、终端路径）打开的最后一个标签页，整个侧栏一起收起；
// 否则关完最后一个回到目录列表，面板本身仍然打开。
// 没有可关闭的标签页时返回 false，关闭成功返回 true。
//
// 调用方（关闭菜单项与 root.go 的动作分发）不需要改动。
func fileManagerCloseActiveTab(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	if !st.open || st.showList || st.active == "" {
		fileManagerMu.Unlock()
		return false
	}
	path := st.active
	fileManagerMu.Unlock()

	s.closeFileManagerTab(path)
	return true
}
