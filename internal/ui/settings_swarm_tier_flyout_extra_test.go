package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSwarmTierFlyoutSaveUpdatesHost(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("deep"); err != nil {
		t.Fatal(err)
	}
	state := getSwarmTiersUIState(st)
	if state.editor == nil {
		t.Fatal("没有打开编辑窗口")
	}
	editor := ui.NewTester(state.editor.view, 360, 1200)
	editor.Frame()
	if err := editor.Click("编辑档位名称"); err != nil {
		t.Fatal(err)
	}
	editor.Key(ui.Cmd, ui.KeyA)
	editor.Type("已更新档位")
	if err := editor.Click("保存"); err != nil {
		t.Fatal(err)
	}
	host.Frame()
	if !host.HasText("已更新档位") {
		t.Fatalf("宿主没有更新: %v", host.Texts())
	}
	if capture.destroyed != 1 {
		t.Fatal("保存后没有销毁窗口")
	}
}

func TestSwarmTierFlyoutUnexpectedCloseKeepsDraft(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	state := getSwarmTiersUIState(st)
	state.draft.Name = "未保存"
	capture.closed[0]()
	if state.editor != nil || state.draft.Name != "未保存" {
		t.Fatal("意外关闭丢失草稿")
	}
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 2 || state.draft.Name != "未保存" {
		t.Fatal("重开没有恢复草稿")
	}
	st.Close()
	if capture.destroyed != 1 {
		t.Fatal("Close 没有销毁窗口")
	}
}

func TestSwarmTierFlyoutScroll(t *testing.T) {
	st, host, _, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	state := getSwarmTiersUIState(st)
	editor := ui.NewTester(state.editor.view, 360, 260)
	editor.Frame()
	editor.Scroll(180, 130, 0, 2000)
	if err := editor.Click("保存"); err != nil {
		t.Fatalf("受限高度无法滚动到操作按钮: %v", err)
	}
}

func TestSwarmTierFlyoutDeleteAndFocus(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("deep"); err != nil {
		t.Fatal(err)
	}
	state := getSwarmTiersUIState(st)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	if capture.focused != 1 || state.draft.ID != "deep" {
		t.Fatal("点击其他入口应保留现有草稿")
	}
	if !deleteTier(st, "deep") {
		t.Fatal("删除档位失败")
	}
	if capture.destroyed != 1 || state.editor != nil {
		t.Fatal("删除后没有清理编辑窗口")
	}
}

func TestSwarmTierFlyoutValidationFailureKeepsWindow(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	state := getSwarmTiersUIState(st)
	editor := ui.NewTester(state.editor.view, 360, 1200)
	editor.Frame()
	if err := editor.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if state.saveError == "" || state.editor == nil || capture.destroyed != 0 {
		t.Fatal("校验失败应保留窗口和错误")
	}
}
