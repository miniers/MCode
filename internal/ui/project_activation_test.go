package mcodeui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestProjectActivation_DefaultActive 验证没有覆盖条目时项目默认激活。
func TestProjectActivation_DefaultActive(t *testing.T) {
	s := newChatState(NewFakeClient())

	if !isProjectActive(s, "p1") {
		t.Fatal("没有覆盖时项目应默认激活")
	}
	if hasProjectActivationOverride(s, "p1") {
		t.Fatal("未设置过覆盖时不应存在覆盖条目")
	}
	// 空参数同样按默认激活处理
	if !isProjectActive(nil, "p1") || !isProjectActive(s, "") {
		t.Fatal("空参数应默认激活")
	}
}

// TestProjectActivation_SetAndClear 验证设置覆盖、读取覆盖与清除覆盖。
func TestProjectActivation_SetAndClear(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	s := newChatState(NewFakeClient())

	setProjectActive(s, "p1", false)
	if isProjectActive(s, "p1") {
		t.Fatal("设置为停用后项目应为非激活")
	}
	if !hasProjectActivationOverride(s, "p1") {
		t.Fatal("设置后应存在覆盖条目")
	}
	if !isProjectActive(s, "p2") {
		t.Fatal("其他项目应保持默认激活")
	}

	// 重新激活同样写入覆盖（而不是清除覆盖）
	setProjectActive(s, "p1", true)
	if !isProjectActive(s, "p1") {
		t.Fatal("重新激活后项目应为激活")
	}
	if !hasProjectActivationOverride(s, "p1") {
		t.Fatal("重新激活后覆盖条目仍应存在")
	}

	clearProjectActivationOverride(s, "p1")
	if !isProjectActive(s, "p1") {
		t.Fatal("清除覆盖后项目应回到默认激活")
	}
	if hasProjectActivationOverride(s, "p1") {
		t.Fatal("清除后不应存在覆盖条目")
	}
}

// TestProjectActivation_UIStatePersistence 验证激活覆盖随 ui-state.json 往返。
func TestProjectActivation_UIStatePersistence(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)

	file := uiStateFile{
		UIState:           UIState{SidebarWidth: 300, SidebarHidden: false},
		ProjectActivation: map[string]bool{"p2": false, "p3": true},
	}
	if err := saveUIStateFile(file); err != nil {
		t.Fatalf("saveUIStateFile failed: %v", err)
	}

	want := map[string]bool{"p2": false, "p3": true}
	if got := loadProjectActivation(); !reflect.DeepEqual(got, want) {
		t.Fatalf("激活覆盖未往返：%v, want %v", got, want)
	}

	// 启动恢复：applyUIState 一并恢复激活覆盖
	s := newChatState(NewFakeClient())
	applyUIState(s, loadUIState())
	if isProjectActive(s, "p2") {
		t.Fatal("applyUIState 后 p2 应为停用")
	}
	if !isProjectActive(s, "p3") || !isProjectActive(s, "p1") {
		t.Fatal("applyUIState 后 p3 与未覆盖项目应保持激活")
	}
	if !hasProjectActivationOverride(s, "p2") {
		t.Fatal("applyUIState 后应保留覆盖条目")
	}

	// 旧文件（无 project_activation 字段）仍可读取，覆盖为空
	old := []byte(`{"sidebar_width": 320, "sidebar_hidden": false}`)
	if err := os.WriteFile(filepath.Join(dataDir, uiStateFileName), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadProjectActivation(); len(got) != 0 {
		t.Fatalf("旧文件的激活覆盖应为空，got %v", got)
	}
}
