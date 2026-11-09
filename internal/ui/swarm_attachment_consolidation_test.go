package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSwarmAvatarStackConsolidated(t *testing.T) {
	entries := []swarmReportEntry{
		{Name: " 中文名 "}, {Name: "Birch"}, {Name: "Cove"}, {Name: "Dawn"}, {Name: "Elm"},
	}
	for _, count := range []int{0, 1, 4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				renderSwarmAvatarStack(c, "test-stack", entries[:count])
			}, 300, 100)
			for i, initial := range []string{"中", "B", "C", "D", "E"} {
				want := i < count && i < 4
				if got := tester.HasText(initial); got != want {
					t.Errorf("头像 %q 显示 = %v，期望 %v", initial, got, want)
				}
			}
			if got := tester.HasText("+1"); got != (count == 5) {
				t.Errorf("+1 显示 = %v，条目数 = %d", got, count)
			}
		})
	}
	tester := ui.NewTester(func(c *ui.Context) {
		renderSwarmReportAvatar(c, "test-report-avatar", " 中文名 ", ControlSM, TypeCaption)
	}, 100, 100)
	if !tester.HasText("中") || tester.HasText("中文名") {
		t.Fatal("汇报头像应显示中文名首字")
	}
}

func TestSwarmReportCopyConsolidated(t *testing.T) {
	const key = "consolidated-swarm-report"
	t.Cleanup(func() {
		copyFeedbackMu.Lock()
		delete(copyFeedbackAt, key+"-copy")
		copyFeedbackMu.Unlock()
	})
	tester := ui.NewTester(func(c *ui.Context) {
		renderSwarmReportCopyButton(c, nil, key, "汇报正文")
	}, 200, 100)
	if err := tester.Click("复制汇报"); err != nil {
		t.Fatal(err)
	}
	if got := tester.Clipboard(); got != "汇报正文" {
		t.Fatalf("剪贴板 = %q", got)
	}
	if !copyFeedbackActive(key+"-copy", time.Now()) {
		t.Fatal("复制后没有记录反馈")
	}
	tester.Frame()
	if _, ok := tester.Find("复制成功"); !ok {
		t.Fatal("复制后没有显示对勾")
	}
}

func TestSwarmEmptyPanelConsolidated(t *testing.T) {
	const leadID = "consolidated-empty-panel"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		swarmFloatMu.Unlock()
	})
	s := &chatState{}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(300, 200).Children(func() {
			renderSwarmEmptyTeamPanel(c, s, swarmFloatFrame{leadID: leadID})
		})
	}, 300, 200)
	if !tester.HasText("暂无子代理") {
		t.Fatal("没有显示空团队提示")
	}
	if err := tester.Click("关闭编排面板"); err != nil {
		t.Fatal(err)
	}
	if !isSwarmFloatPanelDismissed(leadID, nil) {
		t.Fatal("点击关闭后面板没有关闭")
	}
}

func TestAttachmentProgressConsolidated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress float32
		label    string
	}{
		{"负数", -1, ""}, {"未知", 0, ""}, {"一半", 0.5, "上传进度 50%"},
		{"完成", 1, "上传进度 100%"}, {"超出", 2, "上传进度 100%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Size(200, 100).Children(func() {
					renderAttachmentUploadBar(c, composerAttachment{LocalID: "progress", Progress: tc.progress})
				})
			}, 200, 100)
			if tc.label == "" {
				if _, ok := tester.Find("加载中"); !ok {
					t.Fatal("未知进度没有显示加载状态")
				}
				if _, ok := tester.Find("上传进度 0%"); ok {
					t.Fatal("未知进度不应显示确定的零进度")
				}
				return
			}
			if _, ok := tester.Find(tc.label); !ok {
				t.Fatalf("没有显示进度轨道 %q", tc.label)
			}
			if _, ok := tester.Find("加载中"); ok {
				t.Fatal("已知进度不应显示未知进度状态")
			}
		})
	}
}

func TestAttachmentStoppedDoesNotShowProgress(t *testing.T) {
	for _, tc := range []struct {
		name, err string
	}{
		{"取消", ""}, {"失败", "上传失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				renderImageAttachmentCard(c, &chatState{}, composerAttachment{
					LocalID: "stopped", Name: "图片", Err: tc.err, Progress: 1,
				}, false)
			}, 200, 100)
			if _, ok := tester.Find("上传进度 100%"); ok {
				t.Fatal("已停止的上传不应显示进度轨道")
			}
		})
	}
}
