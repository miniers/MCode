package mcodeui

// 项目激活状态：按项目 ID 记录覆盖值，缺省跟随全局默认。
// mcode 没有全局开关，因此默认视为激活（active=true）；覆盖表随 ui-state.json 持久化，
// 存储与恢复方式同项目顺序（ui_state.go 的 sidebarProjectActivation / storeSidebarProjectActivation）。

// isProjectActive 返回项目是否处于激活状态；没有覆盖条目时默认激活。
func isProjectActive(s *chatState, projectID string) bool {
	if s == nil || projectID == "" {
		return true
	}
	if active, ok := sidebarProjectActivation(s)[projectID]; ok {
		return active
	}
	return true
}

// setProjectActive 设置项目的激活状态覆盖并持久化（同项目重排的保存路径）。
func setProjectActive(s *chatState, projectID string, active bool) {
	if s == nil || projectID == "" {
		return
	}
	m := sidebarProjectActivation(s)
	if m == nil {
		m = make(map[string]bool)
	}
	m[projectID] = active
	storeSidebarProjectActivation(s, m)
	persistUIState(s)
	s.invalidate()
}

// clearProjectActivationOverride 清除项目的激活状态覆盖，使其重新跟随全局默认。
func clearProjectActivationOverride(s *chatState, projectID string) {
	if s == nil || projectID == "" {
		return
	}
	m := sidebarProjectActivation(s)
	if _, ok := m[projectID]; !ok {
		return
	}
	delete(m, projectID)
	storeSidebarProjectActivation(s, m)
	persistUIState(s)
	s.invalidate()
}

// hasProjectActivationOverride 返回项目是否存在激活状态覆盖（用于决定是否显示「遵循全局设置」）。
func hasProjectActivationOverride(s *chatState, projectID string) bool {
	if s == nil || projectID == "" {
		return false
	}
	_, ok := sidebarProjectActivation(s)[projectID]
	return ok
}
