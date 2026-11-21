package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestWorkbenchDocsNoCountLabel(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("instructions.md", "# Rules")
	fake.SetContextDoc("api-design.md", "# API")

	s := newChatState(fake)
	project := newWorkbenchProject(t, map[string]string{"instructions": "# Rules"}, map[string]string{"api-design.md": "# API"})
	s.projects = []Project{{ID: "p1", Name: "demo", Path: project}}

	st := NewSettingsState(settingsTestDir(t), nil, s)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if workbenchContextManifest(st) != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	if !tt.HasText("Instructions") {
		t.Fatalf("工作台页应列出项目文档: %v", tt.Texts())
	}
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "标准 ") || strings.Contains(txt, " · 自定义 ") {
			t.Fatalf("工作台页不应再显示文档数量统计: %q", txt)
		}
	}
}

func TestOpenInAddFormVisibleFieldTitles(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageOpenIn(c, st) }, 800, 600)

	if err := tt.Click("添加应用…"); err != nil {
		t.Fatalf("点击添加应用失败: %v", err)
	}
	tt.Frame()

	for _, want := range []string{"应用名称", "应用路径"} {
		if !tt.HasText(want) {
			t.Fatalf("添加表单缺少可见字段标题 %q: %v", want, tt.Texts())
		}
	}
}

func TestOpenInRowButtonA11yLabelsUnique(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
		{Name: "Zed", AppPath: "/Applications/Zed.app", Enabled: true},
	}

	tt := ui.NewTester(func(c *ui.Context) { renderPageOpenIn(c, st) }, 800, 600)

	delCursor, ok := tt.Find("删除 Cursor")
	if !ok {
		t.Fatal("缺少读屏标签为「删除 Cursor」的删除按钮")
	}
	delZed, ok := tt.Find("删除 Zed")
	if !ok {
		t.Fatal("缺少读屏标签为「删除 Zed」的删除按钮")
	}
	if delCursor == delZed {
		t.Fatalf("两个删除按钮的位置不应重合: %+v", delCursor)
	}
}
