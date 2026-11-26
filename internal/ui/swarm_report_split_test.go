package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSplitSwarmReport(t *testing.T) {
	cases := []struct {
		name           string
		summary        string
		wantConclusion string
		wantDetail     string
		wantHeadline   string
	}{
		{
			name:           "有结论有详情",
			summary:        "## 结论\n第一行结论\n第二行补充\n\n## 详情\n详细分析段落\n\n- 明细一",
			wantConclusion: "第一行结论\n第二行补充",
			wantDetail:     "详细分析段落\n\n- 明细一",
			wantHeadline:   "第一行结论",
		},
		{
			name:           "标题容忍前后空白与CRLF",
			summary:        "  ## 结论  \r\n总结首行\r\n  ## 详情 \r\n详情正文\r\n",
			wantConclusion: "总结首行",
			wantDetail:     "详情正文",
			wantHeadline:   "总结首行",
		},
		{
			name:           "无结论标题按旧汇报进详情",
			summary:        "  解析器已修复，测试通过。\n补充信息  ",
			wantConclusion: "",
			wantDetail:     "解析器已修复，测试通过。\n补充信息",
			wantHeadline:   "",
		},
		{
			name:           "失败原因无标题进详情",
			summary:        "执行超时：exit code 1",
			wantConclusion: "",
			wantDetail:     "执行超时：exit code 1",
			wantHeadline:   "",
		},
		{
			name:           "只有结论无详情",
			summary:        "## 结论\n只写了结论内容",
			wantConclusion: "只写了结论内容",
			wantDetail:     "",
			wantHeadline:   "只写了结论内容",
		},
		{
			name:           "标题非独占一行不匹配",
			summary:        "## 结论摘要\n内容",
			wantConclusion: "",
			wantDetail:     "## 结论摘要\n内容",
			wantHeadline:   "",
		},
		{
			name:           "结论首行去掉无序列表与粗体标记",
			summary:        "## 结论\n\n- **x**\n第二行",
			wantConclusion: "- **x**\n第二行",
			wantDetail:     "",
			wantHeadline:   "x",
		},
		{
			name:           "结论首行去掉星号序号反引号与井号",
			summary:        "## 结论\n1. ### 修复了 `splitSwarmReport` 的 **边界**\n\n## 详情\n详细内容",
			wantConclusion: "1. ### 修复了 `splitSwarmReport` 的 **边界**",
			wantDetail:     "详细内容",
			wantHeadline:   "修复了 splitSwarmReport 的 边界",
		},
		{
			name:           "结论为空时headline为空",
			summary:        "## 结论\n\n## 详情\n只有详情",
			wantConclusion: "",
			wantDetail:     "只有详情",
			wantHeadline:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotConclusion, gotDetail := splitSwarmReport(tc.summary)
			if gotConclusion != tc.wantConclusion || gotDetail != tc.wantDetail {
				t.Fatalf("splitSwarmReport() = (%q, %q), want (%q, %q)",
					gotConclusion, gotDetail, tc.wantConclusion, tc.wantDetail)
			}
			if gotHeadline := swarmReportHeadline(tc.summary); gotHeadline != tc.wantHeadline {
				t.Fatalf("swarmReportHeadline() = %q, want %q", gotHeadline, tc.wantHeadline)
			}
		})
	}
}

func TestSwarmReportDetailFold(t *testing.T) {
	setAppReduceMotion(t, false)
	entry, _ := setupPopoverFlyoutTest(t)
	const leadID = "split-fold-lead"
	const beeID = "split-fold-bee"
	key := leadID + "|" + beeID
	detailKey := "capsule|" + key
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmCapsuleOpen, key)
		delete(swarmCapsuleDismissed, key)
		delete(swarmReportDetailOpen, detailKey)
		swarmFloatMu.Unlock()
	})

	report := "## 结论\n第一行结论摘要\n\n## 详情\n这里是默认折叠的详情正文。"
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderSwarmTimelineCapsules(c, &chatState{}, swarmCapsuleRowID, leadID, []SwarmBee{{
			ThreadID: beeID,
			Name:     "Aspen",
			Status:   "completed",
			Summary:  report,
		}})
	}, 800, 600)
	if err := host.Click("子线程 Aspen 已完成"); err != nil {
		t.Fatal(err)
	}

	menu := ui.NewTester(popoverTestView(t, entry.services, "swarm-capsule-popover-"+key), int(swarmCapsulePopoverWidth), 800)
	menu.Frame()
	if !menu.HasText("第一行结论摘要") {
		t.Fatalf("浮窗应先显示结论文字: %v", menu.Texts())
	}
	if menu.HasText("这里是默认折叠的详情正文。") {
		t.Fatalf("详情文字默认应折叠不可见: %v", menu.Texts())
	}

	if err := menu.Click("详情"); err != nil {
		t.Fatalf("点击详情开关失败: %v", err)
	}
	menu.Advance(motionUnfold + 40*time.Millisecond)
	if !menu.HasText("这里是默认折叠的详情正文。") {
		t.Fatalf("点击详情后应展开显示详情文字: %v", menu.Texts())
	}
	if menu.FramePending() {
		t.Fatal("展开动画结束后不应继续请求下一帧")
	}
}
