package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestAutomationListVisualHierarchy(t *testing.T) {
	a := Automation{ID: "a", Name: "测试", ProjectID: "p", Schedule: AutomationSchedule{Type: "daily"}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderAutomationList(c, automationListProps{Visible: []Automation{a}, ProjectNames: map[string]string{"p": "项目"}, Now: time.Now()})
	}, 320, 400)
	for _, text := range []string{"测试", "项目 · 每天 09:00", "已暂停"} {
		if _, ok := tt.Find(text); !ok {
			t.Errorf("缺少 %s: %v", text, tt.Texts())
		}
	}
}

func TestAutomationListVisualBounds(t *testing.T) {
	for _, width := range []float32{320, 620} {
		name := strings.Repeat("很长的中文自动化名称", 20)
		a := Automation{ID: "a", Name: name, Enabled: true, Schedule: AutomationSchedule{Type: "cron", Expression: strings.Repeat("长", 100)}}
		tt := ui.NewTester(func(c *ui.Context) {
			renderAutomationList(c, automationListProps{Visible: []Automation{a}, Unread: map[string]int{"a": 2}, Now: time.Now()})
		}, int(width), 400)
		for _, label := range []string{name, "未读"} {
			r, ok := tt.Find(label)
			if !ok {
				t.Errorf("找不到 %q", label)
				continue
			}
			if r.X < 0 || r.X+r.W > width+1 {
				t.Errorf("宽度 %v 的 %s 越界: %+v", width, label, r)
			}
		}
	}
}

func TestAutomationListVisualStatusWithoutColor(t *testing.T) {
	a := Automation{ID: "a", Name: "暂停任务", LastRunStatus: "running"}
	clicks := 0
	tt := ui.NewTester(func(c *ui.Context) {
		renderAutomationList(c, automationListProps{Visible: []Automation{a}, Unread: map[string]int{"a": 1}, OnSelect: func(Automation) { clicks++ }, Now: time.Now()})
	}, 320, 400)
	label := "自动化项 暂停任务"
	if _, ok := tt.Find(label); !ok {
		t.Fatalf("缺少无障碍名 %s: %v", label, tt.Texts())
	}
	for _, text := range []string{"未读", "未设置调度", "已暂停 · 运行中"} {
		if _, ok := tt.Find(text); !ok {
			t.Errorf("缺少状态 %s", text)
		}
	}
	tt.Click(label)
	if clicks != 1 {
		t.Fatalf("回调次数 %d", clicks)
	}
}
