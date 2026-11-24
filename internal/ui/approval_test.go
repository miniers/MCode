package mcodeui

import (
	"context"
	"fmt"
	"image"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestApprovalBatchApprove(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	th := Thread{ID: "t1", ProjectID: "p1", Title: "ApprovalTest", Provider: "claude", UpdatedAt: now}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {th}},
	)

	appr1 := Approval{
		ThreadID:  "t1",
		RequestID: "req-1",
		Title:     "权限申请 1",
		Detail:    "执行命令 1",
		Options: []ApprovalOption{
			{ID: "opt-allow-1", Title: "允许", Role: "approve"},
			{ID: "opt-decline-1", Title: "拒绝", Role: "decline"},
		},
	}
	appr2 := Approval{
		ThreadID:  "t1",
		RequestID: "req-2",
		Title:     "权限申请 2",
		Detail:    "执行命令 2",
		Options: []ApprovalOption{
			{ID: "opt-allow-2", Title: "允许", Role: "approve"},
			{ID: "opt-decline-2", Title: "拒绝", Role: "decline"},
		},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	fake.TriggerApprovals("t1", []Approval{appr1, appr2})

	// 2 个待审批时出现批量行
	waitFor(t, tt, func() bool {
		return tt.HasText("2 个待审批") && tt.HasText("全部允许") && tt.HasText("全部拒绝")
	})

	// 点击“全部允许”
	if err := tt.Click("全部允许"); err != nil {
		t.Fatalf("click '全部允许' failed: %v", err)
	}

	// 假 Client 收到两次允许答复
	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		if len(calls) != 2 {
			return false
		}
		return (calls[0].OptionID == "opt-allow-1" && calls[1].OptionID == "opt-allow-2") ||
			(calls[0].OptionID == "opt-allow-2" && calls[1].OptionID == "opt-allow-1")
	})
}

func TestApprovalSingleNoBatchRow(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	th := Thread{ID: "t1", ProjectID: "p1", Title: "ApprovalTest", Provider: "claude", UpdatedAt: now}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {th}},
	)

	appr := Approval{
		ThreadID:  "t1",
		RequestID: "req-single",
		Title:     "单一权限申请",
		Detail:    "单一操作详情",
		Options: []ApprovalOption{
			{ID: "opt-allow", Title: "允许", Role: "approve"},
			{ID: "opt-decline", Title: "拒绝", Role: "decline"},
		},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	fake.TriggerApprovals("t1", []Approval{appr})

	// 等待审批卡片渲染出来
	waitFor(t, tt, func() bool {
		return tt.HasText("单一权限申请") && tt.HasText("允许")
	})

	// 1 个待审批时不应出现批量审批行
	if tt.HasText("1 个待审批") {
		t.Errorf("expected no batch row for single approval, but found '1 个待审批'")
	}
	if tt.HasText("全部允许") {
		t.Errorf("expected no '全部允许' button for single approval")
	}
	if tt.HasText("全部拒绝") {
		t.Errorf("expected no '全部拒绝' button for single approval")
	}
}

// newApprovalTester 创建带单个会话的 Tester，并推送给定审批列表。
func newApprovalTester(t *testing.T, approvals []Approval) (*FakeClient, *ui.Tester) {
	t.Helper()
	fake := NewFakeClient()
	th := Thread{ID: "t1", ProjectID: "p1", Title: "ApprovalTest", Provider: "claude", UpdatedAt: time.Now()}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {th}},
	)
	tt := ui.NewTester(Root(fake), 1000, 700)
	fake.TriggerApprovals("t1", approvals)
	return fake, tt
}

func allowDenyOptions() []ApprovalOption {
	return []ApprovalOption{
		{ID: "allow", Title: "允许", Role: "approve"},
		{ID: "deny", Title: "拒绝", Role: "decline"},
	}
}

func TestApprovalCardTitleByKind(t *testing.T) {
	cases := []struct {
		a    Approval
		want string
	}{
		{Approval{Kind: ApprovalKindCommand, Title: "bash", Reason: "credential"}, "读取凭证 · bash"},
		{Approval{Kind: ApprovalKindFileChange, Title: "write", Reason: "controlPlane"}, "改动 mcode 数据 · write"},
		{Approval{Kind: ApprovalKindCommand, Title: "Bash"}, "执行命令 · Bash"},
		{Approval{Kind: ApprovalKindCommand}, "执行命令"},
		{Approval{Kind: ApprovalKindFileChange, Title: "/a/b.go"}, "修改文件"},
		{Approval{Kind: ApprovalKindTool, Title: "mcp__github__create_issue"}, "调用工具 · github · create_issue"},
		{Approval{Kind: ApprovalKindPermissions, Title: "Read"}, "调用工具 · Read"},
		{Approval{Kind: ApprovalKindCommit}, "提交代码"},
		{Approval{Kind: ApprovalKindPlan}, "计划"},
		{Approval{Kind: ApprovalKindQuestion}, "提问"},
		{Approval{Title: "旧版标题"}, "旧版标题"},
		{Approval{Kind: "unknown", Title: "未知类型"}, "未知类型"},
	}
	for _, tc := range cases {
		if got := approvalCardTitle(tc.a); got != tc.want {
			t.Errorf("approvalCardTitle(%+v) = %q, want %q", tc.a, got, tc.want)
		}
	}
	if got := formatApprovalToolName("mcp__broken"); got != "mcp__broken" {
		t.Errorf("formatApprovalToolName 不完整 MCP 名应原样返回，got %q", got)
	}
}

func TestApprovalReasonText(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"credential", ""},
		{"controlPlane", ""},
		{"普通中文文字", "普通中文文字"},
		{"  普通中文文字  ", "普通中文文字"},
	}
	for _, tc := range cases {
		if got := approvalAuxText(Approval{Reason: tc.reason}); got != tc.want {
			t.Errorf("approvalAuxText(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	if got := approvalCardTitle(Approval{Kind: ApprovalKindCommand, Title: "bash", Reason: "credential"}); got != "读取凭证 · bash" {
		t.Errorf("approvalCardTitle = %q, want %q", got, "读取凭证 · bash")
	}
}

func TestApprovalKindRendering(t *testing.T) {
	_, tt := newApprovalTester(t, []Approval{
		{ThreadID: "t1", RequestID: "r-cmd", Kind: ApprovalKindCommand, Title: "Bash", Detail: "ls -la", Reason: "列出目录", Options: allowDenyOptions()},
		{ThreadID: "t1", RequestID: "r-commit", Kind: ApprovalKindCommit, Options: allowDenyOptions()},
		{ThreadID: "t1", RequestID: "r-tool", Kind: ApprovalKindTool, Title: "mcp__srv__run", Options: allowDenyOptions()},
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("执行命令 · Bash") && tt.HasText("列出目录") &&
			tt.HasText(commitApprovalPrompt) && tt.HasText("调用工具 · srv · run")
	})
}

func TestBatchApprovalsExcludesQuestionAndPlan(t *testing.T) {
	list := []Approval{
		{RequestID: "a", Kind: ApprovalKindCommand},
		{RequestID: "b", Kind: ApprovalKindQuestion},
		{RequestID: "c", Kind: ApprovalKindPlan},
		{RequestID: "d"},
	}
	got := batchApprovals(list)
	if len(got) != 2 || got[0].RequestID != "a" || got[1].RequestID != "d" {
		t.Fatalf("batchApprovals = %+v", got)
	}
}

func TestCmdReturnApprovalTarget(t *testing.T) {
	cmd := Approval{RequestID: "cmd", Kind: ApprovalKindCommand}
	plan := Approval{RequestID: "plan", Kind: ApprovalKindPlan}
	q := Approval{RequestID: "q", Kind: ApprovalKindQuestion}

	if _, ok := cmdReturnApprovalTarget(nil, false, false); ok {
		t.Error("无审批时不应返回目标")
	}
	if _, ok := cmdReturnApprovalTarget([]Approval{cmd}, true, false); ok {
		t.Error("responding 时不应返回目标")
	}
	if _, ok := cmdReturnApprovalTarget([]Approval{cmd}, false, true); ok {
		t.Error("composer 聚焦时不应返回目标")
	}
	if _, ok := cmdReturnApprovalTarget([]Approval{q}, false, false); ok {
		t.Error("只有提问时不应返回目标")
	}
	if got, ok := cmdReturnApprovalTarget([]Approval{q, plan, cmd}, false, false); !ok || got.RequestID != "plan" {
		t.Errorf("混合列表应取第一个非提问审批，got %q ok=%v", got.RequestID, ok)
	}
	if got, ok := cmdReturnApprovalTarget([]Approval{cmd, plan}, false, true); !ok || got.RequestID != "plan" {
		t.Errorf("输入框聚焦时应允许计划批准，got %q ok=%v", got.RequestID, ok)
	}
}

func TestCmdReturnApprovesFirstApproval(t *testing.T) {
	fake, tt := newApprovalTester(t, []Approval{
		{ThreadID: "t1", RequestID: "r-q", Kind: ApprovalKindQuestion, Questions: []ApprovalQuestion{{ID: "q1", Prompt: "选哪个？", Choices: []ApprovalQuestionChoice{{Label: "A"}}}}},
		{ThreadID: "t1", RequestID: "r-cmd", Kind: ApprovalKindCommand, Title: "Bash", Detail: "make", Options: allowDenyOptions()},
	})
	waitFor(t, tt, func() bool { return tt.HasText("执行命令 · Bash") })
	tt.Key(ui.Cmd, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		return len(calls) == 1 && calls[0].Approval.RequestID == "r-cmd" && calls[0].OptionID == "allow"
	})
}

func TestCmdReturnAfterComposerDisabled(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})
	fake.TriggerApprovals("t1", []Approval{{
		ThreadID: "t1", RequestID: "r-cmd", Kind: ApprovalKindCommand,
		Title: "Bash", Detail: "make", Options: allowDenyOptions(),
	}})
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.approvals) == 1 && !s.composerFocused
	})
	tt.Key(ui.Cmd, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		return len(calls) == 1 && calls[0].OptionID == "allow"
	})
}

func TestFileApprovalKindRendering(t *testing.T) {
	_, tt := newApprovalTester(t, []Approval{{
		ThreadID: "t1", RequestID: "r-file", Kind: ApprovalKindFileChange,
		Title: "/workspace/a.go", Detail: `{"new_string":"新内容"}`, Options: allowDenyOptions(),
	}})
	waitFor(t, tt, func() bool {
		return tt.HasText("修改文件") && tt.HasText("/workspace/a.go")
	})
}

func TestQuestionDefaultsAndAnswers(t *testing.T) {
	single := ApprovalQuestion{ID: "q1", Choices: []ApprovalQuestionChoice{{Label: "A"}, {Label: "B (Recommended)"}, {Label: "C 推荐"}}}
	multi := ApprovalQuestion{ID: "q2", AllowsMultiple: true, AllowsOther: true, Choices: []ApprovalQuestionChoice{{Label: "X 推荐"}, {Label: "Y"}, {Label: "Z RECOMMENDED"}}}
	free := ApprovalQuestion{ID: "q3", IsSecret: true}

	if got := defaultQuestionChoices(single); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("单选默认应只选第一个推荐项，got %v", got)
	}
	if got := defaultQuestionChoices(multi); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Errorf("多选默认应选全部推荐项，got %v", got)
	}
	if got := toggleQuestionChoice(single, []int{1}, 0); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("单选点击应替换，got %v", got)
	}
	if got := toggleQuestionChoice(multi, []int{0, 2}, 1); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("多选点击应追加并保持升序，got %v", got)
	}
	if got := toggleQuestionChoice(multi, []int{0, 1, 2}, 0); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("多选再次点击应取消，got %v", got)
	}

	qs := []ApprovalQuestion{single, multi, free}
	selected := [][]int{{1}, {1}, nil}
	others := []string{"忽略", " 其他 ", ""}
	if n := unansweredQuestionCount(qs, selected, others); n != 1 {
		t.Errorf("填空题未作答时应剩 1 题，got %d", n)
	}
	if questionAnswered(free, nil, "   ") {
		t.Error("仅空白的填空不算作答")
	}
	others[2] = "secret"
	if n := unansweredQuestionCount(qs, selected, others); n != 0 {
		t.Errorf("全部作答后应剩 0 题，got %d", n)
	}
	want := map[string][]string{
		"q1": {"B (Recommended)"},
		"q2": {"Y", "其他"},
		"q3": {"secret"},
	}
	if got := buildQuestionAnswers(qs, selected, others); !reflect.DeepEqual(got, want) {
		t.Errorf("buildQuestionAnswers = %v, want %v", got, want)
	}
}

func questionApproval() Approval {
	return Approval{
		ThreadID:  "t1",
		RequestID: "r-q",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{
			{ID: "q1", Header: "方案", Prompt: "选择实现方案", Choices: []ApprovalQuestionChoice{{Label: "方案甲 (recommended)"}, {Label: "方案乙"}}},
			{ID: "q2", Header: "范围", Prompt: "包含哪些模块", AllowsMultiple: true, Choices: []ApprovalQuestionChoice{{Label: "模块一"}, {Label: "模块二"}}},
		},
	}
}

func TestQuestionCardSubmit(t *testing.T) {
	fake, tt := newApprovalTester(t, []Approval{questionApproval()})
	waitFor(t, tt, func() bool {
		return tt.HasText("选择实现方案") && tt.HasText("1/2")
	})

	if tt.HasText("提交") {
		t.Fatal("第一题不应显示提交按钮")
	}

	if err := tt.Click("下一题"); err != nil {
		t.Fatalf("click '下一题' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("包含哪些模块") })
	if err := tt.Click("提交"); err != nil {
		t.Fatalf("click '提交' failed: %v", err)
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if _, ok := fake.LastRespondAnswers(); ok {
		t.Fatal("未答完时不应提交")
	}
	for _, label := range []string{"模块一", "模块二"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q failed: %v", label, err)
		}
		tt.Frame()
	}
	if err := tt.Click("提交"); err != nil {
		t.Fatalf("click '提交' failed: %v", err)
	}
	want := map[string][]string{"q1": {"方案甲 (recommended)"}, "q2": {"模块一", "模块二"}}
	waitFor(t, tt, func() bool {
		call, ok := fake.LastRespondAnswers()
		return ok && !call.Skip && call.OptionID == "" && reflect.DeepEqual(call.Answers, want)
	})
}

func TestQuestionCardActionChangesOnLastQuestion(t *testing.T) {
	a := questionApproval()
	s := newChatState(NewFakeClient())
	tt := ui.NewTester(func(c *ui.Context) {
		renderQuestionCard(c, s, a, false)
	}, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })
	if !tt.HasText("下一题") || tt.HasText("提交") {
		t.Fatal("第一题应显示下一题而非提交")
	}
	s.mu.Lock()
	s.questionDraftFor(a).current = 1
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("包含哪些模块") })
	if tt.HasText("下一题") {
		t.Fatal("最后一题不应显示下一题")
	}
	if !tt.HasText("提交") {
		t.Fatal("最后一题应显示提交")
	}
}

func TestQuestionCardSkip(t *testing.T) {
	fake, tt := newApprovalTester(t, []Approval{questionApproval()})
	waitFor(t, tt, func() bool { return tt.HasText("跳过") })
	if err := tt.Click("跳过"); err != nil {
		t.Fatalf("click '跳过' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		call, ok := fake.LastRespondAnswers()
		return ok && call.Skip && call.Answers == nil && call.Approval.RequestID == "r-q"
	})
}

// questionChoiceInkLeft 返回选项区域内非背景像素的最左横坐标，用来校验选项文字左对齐。
func questionChoiceInkLeft(img *image.RGBA, r ui.Rect) (int, bool) {
	x0, x1 := int(r.X)+2, int(r.X+r.W)-2
	y0, y1 := int(r.Y+r.H/4)+1, int(r.Y+r.H*3/4)
	if x1 > img.Rect.Dx()-1 {
		x1 = img.Rect.Dx() - 1
	}
	if y1 > img.Rect.Dy()-1 {
		y1 = img.Rect.Dy() - 1
	}
	if x1 <= x0 || y1 <= y0 {
		return 0, false
	}
	pixel := func(x, y int) [3]int {
		i := img.PixOffset(x, y)
		return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	}
	// 背景色取区域内出现最多的颜色，避免依赖具体主题色。
	counts := map[[3]int]int{}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			counts[pixel(x, y)]++
		}
	}
	var bg [3]int
	best := 0
	for col, n := range counts {
		if n > best {
			bg, best = col, n
		}
	}
	for x := x0; x < x1; x++ {
		for y := y0; y < y1; y++ {
			c := pixel(x, y)
			diff := c[0] - bg[0]
			if diff < 0 {
				diff = -diff
			}
			for k := 1; k < 3; k++ {
				d := c[k] - bg[k]
				if d < 0 {
					d = -d
				}
				diff += d
			}
			if diff > 60 {
				return x, true
			}
		}
	}
	return 0, false
}

// TestQuestionCardChoiceRowsAlign 校验选项行排版一致：所有选项行都从固定图标位之后开始文字布局，
// 宽度一致（不会因未选中而偏右或提前换行）。
func TestQuestionCardChoiceRowsAlign(t *testing.T) {
	labels := []string{
		"首批范围 全部模块 (recommended)",
		"只审核心模块",
		"只审新增模块",
		"Type something.",
	}
	for _, multi := range []bool{false, true} {
		name := "单选"
		if multi {
			name = "多选"
		}
		t.Run(name, func(t *testing.T) {
			choices := make([]ApprovalQuestionChoice, 0, len(labels))
			for _, label := range labels {
				choices = append(choices, ApprovalQuestionChoice{Label: label})
			}
			a := Approval{
				ThreadID:  "t1",
				RequestID: "r-q-align-" + name,
				Kind:      ApprovalKindQuestion,
				Questions: []ApprovalQuestion{{
					ID:             "q1",
					Header:         "首批范围",
					Prompt:         "第一批先审哪块？",
					AllowsMultiple: multi,
					Choices:        choices,
				}},
			}
			_, tt := newApprovalTester(t, []Approval{a})
			waitFor(t, tt, func() bool { return tt.HasText(labels[len(labels)-1]) })

			rects := make([]ui.Rect, 0, len(labels))
			for _, label := range labels {
				r, ok := tt.Find(questionChoiceA11yLabel(displayChoiceLabel(label), "", isRecommendedChoice(label)))
				if !ok {
					t.Fatalf("未找到选项 %q 的布局矩形", label)
				}
				rects = append(rects, r)
			}
			img := tt.Image()
			lefts := make([]int, 0, len(labels))
			for i, label := range labels {
				x, ok := questionChoiceInkLeft(img, rects[i])
				if !ok {
					t.Fatalf("选项 %q 区域内没有可识别的文字像素", label)
				}
				lefts = append(lefts, x)
			}
			// 预选的推荐项最左墨迹是勾选图标，而单选未选中项的图标位是留白，两者天然相差一个图标位，
			// 不能直接比较：未选中项彼此之间要求对齐，选中项只要求不比未选中项更靠右。
			selected := defaultQuestionChoices(a.Questions[0])
			unselected := make([]int, 0, len(labels))
			for i := range labels {
				if !slices.Contains(selected, i) {
					unselected = append(unselected, i)
				}
			}
			if len(unselected) == 0 {
				t.Fatalf("没有未选中的选项可用于校验对齐")
			}
			// 未选中项的文字必须靠近行左边界；原 bug 是文字从约一半行宽处开始。
			for _, i := range unselected {
				off := float64(lefts[i]) - float64(rects[i].X)
				if off > float64(rects[i].W)*0.25 {
					t.Errorf("选项 %q 文字左边界距行左边 %.1fpx，超过行宽 %v 的 25%%，说明文字被推到行中部", labels[i], off, rects[i].W)
				}
			}
			if multi {
				// 多选每个选项都有方框或勾选图标，图标位一致，仍按整体对齐校验。
				for i := 1; i < len(labels); i++ {
					if d := lefts[i] - lefts[0]; d > 6 || d < -6 {
						t.Errorf("选项 %q 文字左边界 %d 与首项 %d 不一致，说明未选中项文字被挤开", labels[i], lefts[i], lefts[0])
					}
				}
			} else {
				ref := unselected[0]
				for _, i := range unselected[1:] {
					if d := lefts[i] - lefts[ref]; d > 6 || d < -6 {
						t.Errorf("未选中选项 %q 文字左边界 %d 与未选中项 %q 的 %d 不一致，说明未选中项文字被挤开", labels[i], lefts[i], labels[ref], lefts[ref])
					}
				}
				for _, i := range selected {
					if i < 0 || i >= len(lefts) {
						continue
					}
					if lefts[i] > lefts[ref] {
						t.Errorf("选中选项 %q 最左墨迹 %d 比未选中项 %d 更靠右，说明图标位没有统一", labels[i], lefts[i], lefts[ref])
					}
				}
			}
			for i := 1; i < len(labels); i++ {
				if d := rects[i].H - rects[0].H; d > 0.5 || d < -0.5 {
					t.Errorf("选项 %q 行高 %v 与首项 %v 不一致，说明文字被压窄换行", labels[i], rects[i].H, rects[0].H)
				}
			}
		})
	}
}

func TestQuestionCardSingleOtherReplacesChoice(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	a.Questions[0].AllowsOther = true
	fake, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })
	if err := tt.Click("其他"); err != nil {
		t.Fatal(err)
	}
	tt.Type("自定义方案")
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := fake.LastRespondAnswers()
		return ok
	})
	call, _ := fake.LastRespondAnswers()
	if want := map[string][]string{"q1": {"自定义方案"}}; !reflect.DeepEqual(call.Answers, want) {
		t.Fatalf("单选填空应替换推荐项：got %v, want %v", call.Answers, want)
	}
}

func TestQuestionDisplayChoiceLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"方案甲 (recommended)", "方案甲"},
		{"方案甲 (Recommended)", "方案甲"},
		{"方案甲 (RECOMMENDED)", "方案甲"},
		{"方案甲（推荐）", "方案甲"},
		{"方案甲 (推荐)", "方案甲"},
		{"方案甲（recommended）", "方案甲"},
		{"  方案甲 (recommended)  ", "方案甲"},
		{"方案甲（推荐）(recommended)", "方案甲"},
		{"方案甲", "方案甲"},
		{"推荐", "推荐"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := displayChoiceLabel(tc.in); got != tc.want {
			t.Errorf("displayChoiceLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuestionRecommendedBadgeKeepsRawAnswer(t *testing.T) {
	a := questionApproval()
	fake, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })
	if !tt.HasText("推荐") {
		t.Fatalf("推荐项应显示「推荐」胶囊，当前文本：%q", tt.Texts())
	}
	if _, ok := tt.Find("方案甲"); !ok {
		t.Fatalf("推荐标注应从显示文字里去掉，当前文本：%q", tt.Texts())
	}

	if err := tt.Click("下一题"); err != nil {
		t.Fatalf("click '下一题' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("包含哪些模块") })
	for _, label := range []string{"模块一", "模块二"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q failed: %v", label, err)
		}
		tt.Frame()
	}
	if err := tt.Click("提交"); err != nil {
		t.Fatalf("click '提交' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := fake.LastRespondAnswers()
		return ok
	})
	call, _ := fake.LastRespondAnswers()
	if want := map[string][]string{"q1": {"方案甲 (recommended)"}, "q2": {"模块一", "模块二"}}; !reflect.DeepEqual(call.Answers, want) {
		t.Fatalf("提交值应保持原始 Label：got %v, want %v", call.Answers, want)
	}
}

func TestQuestionWithoutFreeTextShowsOnlyChoices(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	if a.Questions[0].AllowsOther {
		t.Fatal("用例前提：题目不接受自由文本")
	}
	fake, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })

	if _, ok := tt.Find("其他"); ok {
		t.Fatalf("不接受自由文本的题不应显示输入框：%q", tt.Texts())
	}
	if tt.HasText("其他指示…") {
		t.Fatalf("不接受自由文本的题只该给选项：%q", tt.Texts())
	}

	if err := tt.Click("方案乙"); err != nil {
		t.Fatalf("click '方案乙' failed: %v", err)
	}
	if err := tt.Click("提交"); err != nil {
		t.Fatalf("click '提交' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := fake.LastRespondAnswers()
		return ok
	})
	call, _ := fake.LastRespondAnswers()
	if want := map[string][]string{"q1": {"方案乙"}}; !reflect.DeepEqual(call.Answers, want) {
		t.Fatalf("提交值错误：got %v, want %v", call.Answers, want)
	}
}

func TestQuestionCardChoiceReplacesSingleOther(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	a.Questions[0].AllowsOther = true
	fake, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })
	if err := tt.Click("其他"); err != nil {
		t.Fatal(err)
	}
	tt.Type("自定义方案")
	tt.Frame()
	if err := tt.Click("方案乙"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := fake.LastRespondAnswers()
		return ok
	})
	call, _ := fake.LastRespondAnswers()
	if want := map[string][]string{"q1": {"方案乙"}}; !reflect.DeepEqual(call.Answers, want) {
		t.Fatalf("单选选项应清除填空：got %v, want %v", call.Answers, want)
	}
}

func planApproval() Approval {
	return Approval{
		ThreadID:  "t1",
		RequestID: "r-plan",
		Kind:      ApprovalKindPlan,
		Detail:    "# 整体思路\n\n先把计划卡改成摘要卡。\n\n## 改动点\n\n1. 改 UI\n2. 补测试\n\n## 验收\n\n预计跑通全部 UI 测试。",
		Options:   allowDenyOptions(),
	}
}

func TestPlanCardApprove(t *testing.T) {
	fake, tt := newApprovalTester(t, []Approval{planApproval()})
	// 操作条与时间线正文同时存在。
	waitFor(t, tt, func() bool {
		return tt.HasText("整体思路") && !tt.HasText("待实施") &&
			tt.HasText("驳回") && tt.HasText("批准计划") && !tt.HasText("阅读全文")
	})
	if err := tt.Click("批准计划"); err != nil {
		t.Fatalf("click '批准计划' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fake.LastRespondOptionID() == "allow" })
}

func TestPlanCardDeclineWithFeedback(t *testing.T) {
	if got := planDeclineAnswers("  "); got != nil {
		t.Errorf("空反馈应返回 nil，got %v", got)
	}
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	s.mu.Lock()
	s.approvals = []Approval{planApproval()}
	s.draft = "请补充测试"
	s.mu.Unlock()
	s.declinePlanDraft(planApproval(), false)
	want := map[string][]string{"feedback": {"请补充测试"}}
	waitForAutoDormant(t, func() bool {
		call, ok := fake.LastRespondAnswers()
		return ok && call.OptionID == "deny" && !call.Skip && reflect.DeepEqual(call.Answers, want)
	})
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "" {
		t.Fatalf("驳回计划后草稿应清空，got %q", draft)
	}
}

func TestSendPlanFeedback(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.approvals = []Approval{planApproval()}
	s.mu.Unlock()
	if !s.sendPlanFeedback("t1", "请补充测试") {
		t.Fatal("计划审批应接管反馈发送")
	}
	want := map[string][]string{"feedback": {"请补充测试"}}
	waitForAutoDormant(t, func() bool {
		call, ok := fake.LastRespondAnswers()
		return ok && call.OptionID == "deny" && !call.Skip && reflect.DeepEqual(call.Answers, want)
	})
}

func TestSendPlanFeedbackEmptyDraft(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.approvals = []Approval{planApproval()}
	s.mu.Unlock()
	if !s.sendPlanFeedback("t1", "  ") {
		t.Fatal("空草稿仍应由计划审批接管发送")
	}
	if _, ok := fake.LastRespondAnswers(); ok {
		t.Fatal("空草稿不应调用 RespondAnswers")
	}
}

func TestApprovalShortcutAction(t *testing.T) {
	testenv.TempHome(t)

	normalAppr := Approval{
		RequestID: "req-normal",
		Kind:      ApprovalKindCommand,
		Options: []ApprovalOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "allow_always", Title: "始终允许", Role: "approveAlways"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		},
	}
	planAppr := Approval{RequestID: "req-plan", Kind: ApprovalKindPlan, Options: allowDenyOptions()}
	if got := approvalShortcutAction(planAppr, ui.KeyEnter, 0, false, false, false, false, false, false, 0); got.Kind != ApprovalActionNone {
		t.Fatalf("计划 Enter 不应触发审批，got %+v", got)
	}
	if got := approvalShortcutAction(planAppr, ui.Key1, 0, false, false, false, false, false, false, 0); got.Kind != ApprovalActionNone {
		t.Fatalf("计划数字键不应触发审批，got %+v", got)
	}

	// 1~9 触发对应的 Options
	a1 := approvalShortcutAction(normalAppr, ui.Key1, 0, false, false, false, false, false, false, 0)
	if a1.Kind != ApprovalActionOption || a1.Index != 0 || a1.OptionID != "allow" {
		t.Errorf("expected option 0 (allow), got %+v", a1)
	}
	a2 := approvalShortcutAction(normalAppr, ui.Key2, 0, false, false, false, false, false, false, 0)
	if a2.Kind != ApprovalActionOption || a2.Index != 1 || a2.OptionID != "allow_always" {
		t.Errorf("expected option 1 (allow_always), got %+v", a2)
	}
	a3 := approvalShortcutAction(normalAppr, ui.Key3, 0, false, false, false, false, false, false, 0)
	if a3.Kind != ApprovalActionOption || a3.Index != 2 || a3.OptionID != "deny" {
		t.Errorf("expected option 2 (deny), got %+v", a3)
	}
	// 超出范围的按键
	a4 := approvalShortcutAction(normalAppr, ui.Key4, 0, false, false, false, false, false, false, 0)
	if a4.Kind != ApprovalActionNone {
		t.Errorf("expected none for out of range key, got %+v", a4)
	}

	// Enter 批准普通卡片
	aEnter := approvalShortcutAction(normalAppr, ui.KeyEnter, 0, false, false, false, false, false, false, 0)
	if aEnter.Kind != ApprovalActionApprove || aEnter.OptionID != "allow" {
		t.Errorf("expected approve action for Enter, got %+v", aEnter)
	}

	// Esc 拒绝普通卡片
	aEsc := approvalShortcutAction(normalAppr, ui.KeyEscape, 0, false, false, false, false, false, false, 0)
	if aEsc.Kind != ApprovalActionDecline || aEsc.OptionID != "deny" {
		t.Errorf("expected decline action for Esc, got %+v", aEsc)
	}

	// 问题卡片
	qAppr := Approval{
		RequestID: "req-q",
		Kind:      ApprovalKindQuestion,
	}

	// 数字键 1~9 选择选项
	qKey1 := approvalShortcutAction(qAppr, ui.Key1, 0, false, false, false, false, false, false, 3)
	if qKey1.Kind != ApprovalActionSelectChoice || qKey1.Index != 0 {
		t.Errorf("expected select_choice 0, got %+v", qKey1)
	}
	qKey4 := approvalShortcutAction(qAppr, ui.Key4, 0, false, false, false, false, false, false, 3)
	if qKey4.Kind != ApprovalActionNone {
		t.Errorf("expected none for key exceeding choices count, got %+v", qKey4)
	}

	// Enter：未全部回答时不提交
	qEnterIncomplete := approvalShortcutAction(qAppr, ui.KeyEnter, 0, false, false, false, false, false, false, 3)
	if qEnterIncomplete.Kind != ApprovalActionNone {
		t.Errorf("expected none when questions incomplete, got %+v", qEnterIncomplete)
	}

	// Enter：全部回答时提交
	qEnterComplete := approvalShortcutAction(qAppr, ui.KeyEnter, 0, false, false, false, false, true, false, 3)
	if qEnterComplete.Kind != ApprovalActionSubmit {
		t.Errorf("expected submit when questions complete, got %+v", qEnterComplete)
	}
	for _, answered := range []bool{false, true} {
		next := approvalShortcutAction(qAppr, ui.KeyEnter, 0, false, false, false, false, answered, true, 3)
		if next.Kind != ApprovalActionNext {
			t.Errorf("有下一题时应前进，answered=%v，got %+v", answered, next)
		}
	}
	otherBusyNext := approvalShortcutAction(qAppr, ui.KeyEnter, 0, false, false, false, true, true, true, 3)
	if otherBusyNext.Kind != ApprovalActionNone {
		t.Errorf("其他输入框聚焦时不应前进，got %+v", otherBusyNext)
	}

	// Esc：跳过
	qEsc := approvalShortcutAction(qAppr, ui.KeyEscape, 0, false, false, false, false, false, false, 3)
	if qEsc.Kind != ApprovalActionDecline {
		t.Errorf("expected decline for question Esc, got %+v", qEsc)
	}

	// 冲突测试：输入框聚焦且草稿非空时不拦截 Enter 和数字键
	busyEnter := approvalShortcutAction(normalAppr, ui.KeyEnter, 0, false, true, true, false, false, false, 0)
	if busyEnter.Kind != ApprovalActionNone {
		t.Errorf("expected no interception for Enter when composer focused and has draft, got %+v", busyEnter)
	}
	busyKey1 := approvalShortcutAction(normalAppr, ui.Key1, 0, false, true, true, false, false, false, 0)
	if busyKey1.Kind != ApprovalActionNone {
		t.Errorf("expected no interception for Key1 when composer focused and has draft, got %+v", busyKey1)
	}

	// 输入框聚焦但草稿为空时，正常拦截
	emptyDraftEnter := approvalShortcutAction(normalAppr, ui.KeyEnter, 0, false, true, false, false, false, false, 0)
	if emptyDraftEnter.Kind != ApprovalActionApprove {
		t.Errorf("expected interception for Enter when draft is empty, got %+v", emptyDraftEnter)
	}

	// 问题卡片“其他”文本框聚焦时不拦截 Enter 和数字键
	otherBusyEnter := approvalShortcutAction(qAppr, ui.KeyEnter, 0, false, false, false, true, true, false, 3)
	if otherBusyEnter.Kind != ApprovalActionNone {
		t.Errorf("expected no interception when other focused, got %+v", otherBusyEnter)
	}
	otherBusyKey1 := approvalShortcutAction(qAppr, ui.Key1, 0, false, false, false, true, false, false, 3)
	if otherBusyKey1.Kind != ApprovalActionNone {
		t.Errorf("expected no interception for Key1 when other focused, got %+v", otherBusyKey1)
	}

	// responding=true 时不响应
	respKey := approvalShortcutAction(normalAppr, ui.Key1, 0, true, false, false, false, false, false, 0)
	if respKey.Kind != ApprovalActionNone {
		t.Errorf("expected no action when responding, got %+v", respKey)
	}
}

// 数字键批准后，同一帧随后命中的 Enter/Esc 不得再对同一张卡片回调后端。
func TestApprovalShortcut_SingleFrameDoesNotRespondTwice(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	appr := Approval{
		ThreadID:  "t1",
		RequestID: "req-once",
		Kind:      ApprovalKindCommand,
		Options:   allowDenyOptions(),
	}
	// 后端已撤销该请求（审批列表里已经不存在该卡片），模拟同帧内先按数字键又按 Esc
	s.approvals = nil
	if target, ok := pendingApprovalByID(s, appr.RequestID); ok {
		t.Fatalf("expected no pending approval, got %+v", target)
	}

	s.approvals = []Approval{appr}
	target, ok := pendingApprovalByID(s, appr.RequestID)
	if !ok || target.RequestID != appr.RequestID {
		t.Fatalf("expected pending approval %q, got %+v ok=%v", appr.RequestID, target, ok)
	}
}

func TestResolvedPlans_ApprovedAndDismissed(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	threadID := "t1"
	s.selectedThreadID = threadID

	plan1 := Approval{
		ThreadID:  threadID,
		RequestID: "req-plan-1",
		Kind:      ApprovalKindPlan,
		Detail:    "第一阶段实施计划",
		Options:   allowDenyOptions(),
	}

	s.respondApproval(plan1, "allow")

	// 等待后台 goroutine 完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		plans := s.resolvedPlans[threadID]
		s.mu.Unlock()
		if len(plans) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.mu.Lock()
	plans := s.resolvedPlans[threadID]
	s.mu.Unlock()
	if len(plans) != 1 {
		t.Fatalf("expected 1 resolved plan, got %d", len(plans))
	}
	if !plans[0].Approved {
		t.Errorf("expected Approved=true, got false")
	}
	if plans[0].Plan != "第一阶段实施计划" {
		t.Errorf("expected plan detail %q, got %q", "第一阶段实施计划", plans[0].Plan)
	}

	// 驳回计划
	plan2 := Approval{
		ThreadID:  threadID,
		RequestID: "req-plan-2",
		Kind:      ApprovalKindPlan,
		Detail:    "第二阶段实施计划",
		Options:   allowDenyOptions(),
	}
	s.mu.Lock()
	s.approvals = append(s.approvals, plan2)
	s.mu.Unlock()
	s.respondAnswers(plan2, "deny", nil, false)

	for time.Now().Before(deadline.Add(2 * time.Second)) {
		s.mu.Lock()
		plans = s.resolvedPlans[threadID]
		s.mu.Unlock()
		if len(plans) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.mu.Lock()
	plans = s.resolvedPlans[threadID]
	s.mu.Unlock()
	if len(plans) != 2 {
		t.Fatalf("expected 2 resolved plans, got %d", len(plans))
	}
	if plans[1].Approved {
		t.Errorf("expected Approved=false for dismissed plan, got true")
	}
	if plans[1].Plan != "第二阶段实施计划" {
		t.Errorf("expected plan detail %q, got %q", "第二阶段实施计划", plans[1].Plan)
	}
}

// approvalGateClient 让 Respond 阻塞在 release 上逐个放行，复现「批量审批」的并发在途状态。
type approvalGateClient struct {
	Client
	release chan struct{}
}

func (c *approvalGateClient) Respond(ctx context.Context, a Approval, optionID string) error {
	<-c.release
	return c.Client.Respond(ctx, a, optionID)
}

// U-031：批量允许/拒绝会并发发出多个响应，按钮必须等全部返回才解锁；
// 用单个 bool 时第一个响应回来就解锁，同一批审批能被重复提交。
func TestBatchApprovalLockedUntilAllResponsesReturn(t *testing.T) {
	fake := NewFakeClient()
	gate := make(chan struct{}, 2)
	s := newChatState(&approvalGateClient{Client: fake, release: gate})

	a1 := Approval{ThreadID: "t1", RequestID: "req-1", Kind: ApprovalKindCommand, Options: allowDenyOptions()}
	a2 := Approval{ThreadID: "t1", RequestID: "req-2", Kind: ApprovalKindCommand, Options: allowDenyOptions()}
	s.mu.Lock()
	s.approvals = []Approval{a1, a2}
	s.mu.Unlock()

	responding := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.respondingApproval > 0
	}

	s.respondApproval(a1, "approve")
	s.respondApproval(a2, "approve")
	if !responding() {
		t.Fatal("批量响应在途时应保持锁定")
	}

	// 只放行第一个响应：另一个仍在途，按钮不能解锁。
	gate <- struct{}{}
	waitForAutoDormant(t, func() bool { return len(fake.RespondCalls()) == 1 })
	if !responding() {
		t.Fatal("首个响应返回后仍有在途响应，不应解锁")
	}

	gate <- struct{}{}
	waitForAutoDormant(t, func() bool { return len(fake.RespondCalls()) == 2 })
	waitForAutoDormant(t, func() bool { return !responding() })
}

// TestApprovalCard_ShowsDeliveryError 覆盖 U-032：上次答复没有送达时，卡片要说明原因并提示可以重新选择。
func TestApprovalCard_ShowsDeliveryError(t *testing.T) {
	_, tt := newApprovalTester(t, []Approval{{
		ThreadID:      "t1",
		RequestID:     "req-delivery",
		Kind:          ApprovalKindCommand,
		Title:         "命令申请",
		Detail:        "ls -la",
		DeliveryError: "pipe closed",
		Options:       allowDenyOptions(),
	}})

	waitFor(t, tt, func() bool {
		return tt.HasText("上次答复未送达：pipe closed，可重新选择")
	})
}

// TestApprovalCard_NoDeliveryErrorNotice 验证没有送达错误时不显示那行提示。
func TestApprovalCard_NoDeliveryErrorNotice(t *testing.T) {
	_, tt := newApprovalTester(t, []Approval{{
		ThreadID:  "t1",
		RequestID: "req-ok",
		Kind:      ApprovalKindCommand,
		Title:     "命令申请",
		Detail:    "ls -la",
		Options:   allowDenyOptions(),
	}})

	waitFor(t, tt, func() bool {
		return tt.HasText("命令申请")
	})
	if tt.HasText("上次答复未送达") {
		t.Fatal("没有送达错误时不应显示送达失败提示")
	}
}

// newQuestionCardTester 只渲染提问卡本身。根视图的侧栏等项目里也有纯数字文字（项目计数），
// 会与键帽数字重名，而 ui.Tester.Find 只返回第一个匹配，因此这些用例不经过 buildRoot。
func newQuestionCardTester(t *testing.T, a Approval) *ui.Tester {
	t.Helper()
	s := newChatState(NewFakeClient())
	return ui.NewTester(func(c *ui.Context) {
		renderQuestionCard(c, s, a, false)
	}, 1000, 700)
}

// 测试提问卡选项左侧序号位里的数字：数字键确实对这些选项生效，序号在文字左侧。
func TestQuestionCardChoiceKeyBadges(t *testing.T) {
	for _, multi := range []bool{false, true} {
		name := "单选"
		if multi {
			name = "多选"
		}
		t.Run(name, func(t *testing.T) {
			labels := []string{"甲方案", "乙方案", "丙方案"}
			choices := make([]ApprovalQuestionChoice, 0, len(labels))
			for _, label := range labels {
				choices = append(choices, ApprovalQuestionChoice{Label: label})
			}
			a := Approval{
				ThreadID:  "t1",
				RequestID: "r-q-badge-" + name,
				Kind:      ApprovalKindQuestion,
				Questions: []ApprovalQuestion{{
					ID:             "q1",
					Prompt:         "选择实现方案",
					AllowsMultiple: multi,
					Choices:        choices,
				}},
			}
			tt := newQuestionCardTester(t, a)
			waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })

			for i, label := range labels {
				badge, ok := tt.Find(fmt.Sprintf("%d", i+1))
				if !ok {
					t.Fatalf("选项 %q 缺少序号 %d，当前文本：%q", label, i+1, tt.Texts())
				}
				row, ok := tt.Find(label)
				if !ok {
					t.Fatalf("未找到选项行 %q", label)
				}
				// 序号在行首 16×16 的序号位里，垂直方向落在本行内。
				if badge.X-row.X > Space3+questionSlotSize || badge.Y < row.Y || badge.Y+badge.H > row.Y+row.H {
					t.Errorf("选项 %q 的序号应在行首序号位：badge=%+v row=%+v", label, badge, row)
				}
			}
		})
	}
}

// 测试 12 个选项时只给前 9 个画序号：数字键 1~9 才是实际生效的快捷键。
func TestQuestionCardKeyBadgesStopAtNine(t *testing.T) {
	labels := make([]string, 0, 12)
	choices := make([]ApprovalQuestionChoice, 0, 12)
	for i := 0; i < 12; i++ {
		label := "选项" + string(rune('A'+i))
		labels = append(labels, label)
		choices = append(choices, ApprovalQuestionChoice{Label: label})
	}
	a := Approval{
		ThreadID:  "t1",
		RequestID: "r-q-badge-long",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "选择实现方案",
			Choices: choices,
		}},
	}
	tt := newQuestionCardTester(t, a)
	waitFor(t, tt, func() bool { return tt.HasText(labels[len(labels)-1]) })

	for i := 0; i < 9; i++ {
		want := fmt.Sprintf("%d", i+1)
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("第 %d 个选项应有序号 %q，当前文本：%q", i+1, want, tt.Texts())
		}
	}
	for _, want := range []string{"10", "11", "12"} {
		if _, ok := tt.Find(want); ok {
			t.Fatalf("第 %s 个选项不应画序号，当前文本：%q", want, tt.Texts())
		}
	}
}

// TestApprovalCardSurfaceBackground 审批卡只保留左侧警示条与一条 Banner：
// 操作行左侧的空白区域应是 SurfaceCard，而不是整块警示色叠在表面色上的旧底色。
func TestApprovalCardSurfaceBackground(t *testing.T) {
	appr := Approval{
		ThreadID:  "t1",
		RequestID: "req-surface",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "ls -la",
		Reason:    "列出目录",
		Options:   allowDenyOptions(),
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Gap(Space3).Children(func() {
			ui.Row(c).Gap(Space3).Children(func() {
				ui.Box(c.Key("surface-swatch")).Size(12, 12).Background(ThemeSurfaceCard(c)).Label("surface-swatch")
				ui.Box(c.Key("tint-swatch")).Size(12, 12).
					Background(ThemeWarning(c).Alpha(0.08).Over(ThemeSurface(c))).
					Label("tint-swatch")
			})
			renderTypedApprovalCard(c, nil, appr, false, IconTerminal(), func() {
				renderApprovalCode(c, "approval-detail-"+appr.RequestID, appr.Detail)
			})
		})
	}, 640, 480)

	if _, ok := tt.Find("允许"); !ok {
		t.Fatalf("没找到「允许」按钮，可见文字: %v", tt.Texts())
	}
	if _, ok := tt.Find("拒绝"); !ok {
		t.Fatalf("没找到「拒绝」按钮，可见文字: %v", tt.Texts())
	}
	surfaceRect, ok := tt.Find("surface-swatch")
	if !ok {
		t.Fatal("没找到表面色对照色块")
	}
	tintRect, ok := tt.Find("tint-swatch")
	if !ok {
		t.Fatal("没找到旧底色对照色块")
	}

	headerRect, ok := tt.Find(approvalCardTitle(appr))
	if !ok {
		t.Fatalf("没找到卡片标题，可见文字: %v", tt.Texts())
	}

	img := tt.Image()
	surface := confirmPixel(t, img, surfaceRect)
	tint := confirmPixel(t, img, tintRect)
	// 采样标题左侧的卡内留白：位于左侧警示条与内容列之间，不落在按钮或 Banner 上。
	x := int(headerRect.X) - 8
	y := int(headerRect.Y + headerRect.H/2)
	if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() {
		t.Fatalf("采样点 (%d,%d) 超出画面 %v", x, y, img.Rect)
	}
	i := img.PixOffset(x, y)
	got := [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	if !colorNear(got, surface, 2) {
		t.Fatalf("卡片底色应是 SurfaceCard %v，实际 %v", surface, got)
	}
	if !colorNear(surface, tint, 12) && colorNear(got, tint, 4) {
		t.Fatalf("卡片底色仍是整块警示底 %v", tint)
	}
}

func TestUIReview4_DeliveryErrorUsesDangerTone(t *testing.T) {
	if got := approvalBannerTone(Approval{Reason: "仅原因"}); got != ToneWarning {
		t.Fatalf("无 DeliveryError 时应保持 ToneWarning，got %v", got)
	}
	if got := approvalBannerTone(Approval{DeliveryError: "pipe closed"}); got != ToneDanger {
		t.Fatalf("有 DeliveryError 时应为 ToneDanger，got %v", got)
	}

	cards := []struct {
		name string
		appr Approval
	}{
		{
			name: "command",
			appr: Approval{
				ThreadID:      "t1",
				RequestID:     "r-err-cmd",
				Kind:          ApprovalKindCommand,
				Title:         "Bash",
				Detail:        "ls",
				DeliveryError: "pipe closed",
				Options:       allowDenyOptions(),
			},
		},
		{
			name: "question",
			appr: Approval{
				ThreadID:      "t1",
				RequestID:     "r-err-q",
				Kind:          ApprovalKindQuestion,
				DeliveryError: "pipe closed",
				Questions: []ApprovalQuestion{
					{ID: "q1", Prompt: "选哪个？", Choices: []ApprovalQuestionChoice{{Label: "甲"}}},
				},
			},
		},
		{
			name: "plan",
			appr: Approval{
				ThreadID:      "t1",
				RequestID:     "r-err-plan",
				Kind:          ApprovalKindPlan,
				Detail:        "# 计划标题\n\n步骤一",
				DeliveryError: "pipe closed",
				Options:       allowDenyOptions(),
			},
		},
	}

	for _, tc := range cards {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			tt := ui.NewTester(func(c *ui.Context) {
				cardBg := ThemeSurfaceCard(c)
				switch tc.appr.Kind {
				case ApprovalKindPlan:
					cardBg = ThemeSurface(c)
				}
				ui.Column(c).FillWidth().Gap(Space2).Children(func() {
					ui.Column(c).FillWidth().Gap(Space1).Background(cardBg).Children(func() {
						Banner(c, BannerProps{Key: "ref-danger", Tone: ToneDanger, Text: "ref-danger"})
						Banner(c, BannerProps{Key: "ref-warning", Tone: ToneWarning, Text: "ref-warning"})
					})
					renderApprovalCard(c, s, tc.appr, false)
				})
			}, 720, 480)

			const wantNotice = "上次答复未送达：pipe closed，可重新选择"
			waitFor(t, tt, func() bool { return tt.HasText(wantNotice) })

			refDangerRect, ok := tt.Find("ref-danger")
			if !ok {
				t.Fatal("未找到 ref-danger")
			}
			refWarnRect, ok := tt.Find("ref-warning")
			if !ok {
				t.Fatal("未找到 ref-warning")
			}
			noticeRect, ok := tt.Find(wantNotice)
			if !ok {
				t.Fatalf("未找到送达错误文本 %q", wantNotice)
			}

			img := tt.Image()
			samplePad := func(r ui.Rect) [3]int {
				x := int(r.X + 4)
				y := int(r.Y - 4)
				if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() {
					t.Fatalf("采样点 (%d,%d) 超出画面 %v", x, y, img.Rect)
				}
				i := img.PixOffset(x, y)
				return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
			}
			dangerBg := samplePad(refDangerRect)
			warnBg := samplePad(refWarnRect)
			gotBg := samplePad(noticeRect)
			if !colorNear(gotBg, dangerBg, 3) {
				t.Fatalf("%s DeliveryError Banner 背景应接近 ToneDanger %v，实际 %v", tc.name, dangerBg, gotBg)
			}
			if !colorNear(dangerBg, warnBg, 4) && colorNear(gotBg, warnBg, 2) {
				t.Fatalf("%s DeliveryError Banner 背景不应是 ToneWarning %v", tc.name, warnBg)
			}
		})
	}
}

func TestUIReview4_NarrowLayoutAndRespondingLock(t *testing.T) {
	// 窄窗下长标题/路径/操作栏不把按钮挤出可视范围
	s := newChatState(NewFakeClient())
	plan := Approval{
		ThreadID:  "t1",
		RequestID: "r-narrow-plan",
		Kind:      ApprovalKindPlan,
		Detail:    "# 这是一个非常非常非常长的计划标题在窄窗口下应当截断并保留按钮\n\n内容",
		Options:   allowDenyOptions(),
	}
	fileAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-narrow-file",
		Kind:      ApprovalKindFileChange,
		Title:     "/Users/example/very/long/nested/directory/structure/that/must/not/overflow/the/narrow/approval/card.go",
		Detail:    "diff",
		Options:   allowDenyOptions(),
	}
	ttNarrow := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Gap(Space3).Padding(Space2).Children(func() {
			renderApprovalCard(c, s, plan, false)
			renderApprovalCard(c, s, fileAppr, false)
		})
	}, 320, 600)
	waitFor(t, ttNarrow, func() bool {
		return ttNarrow.HasText("批准计划") && ttNarrow.HasText("允许")
	})
	for _, label := range []string{"驳回", "批准计划", "允许", "拒绝"} {
		r, ok := ttNarrow.Find(label)
		if !ok {
			t.Fatalf("窄窗下未找到按钮 %q", label)
		}
		if r.X < 0 || r.X+r.W > 320 {
			t.Fatalf("窄窗下按钮 %q 超出可视区：rect=%+v", label, r)
		}
	}

	// 提交中（responding=true）时锁住选项和「其他」输入框
	qAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-lock-q",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:          "q1",
			Prompt:      "锁定测试",
			AllowsOther: true,
			Choices:     []ApprovalQuestionChoice{{Label: "选项一"}, {Label: "选项二"}},
		}},
	}
	sLock := newChatState(NewFakeClient())
	ttLock := ui.NewTester(func(c *ui.Context) {
		renderQuestionCard(c, sLock, qAppr, true)
	}, 720, 480)
	waitFor(t, ttLock, func() bool { return ttLock.HasText("锁定测试") })

	if err := ttLock.Click("选项一"); err != nil {
		t.Fatalf("点击选项一失败: %v", err)
	}
	ttLock.Frame()
	sLock.mu.Lock()
	draft := sLock.questionDraftFor(qAppr)
	sel := slices.Clone(draftAt(draft.selected, 0))
	sLock.mu.Unlock()
	if len(sel) != 0 {
		t.Fatalf("提交进行中不应允许切换选项，got %v", sel)
	}

	_ = ttLock.Click("其他")
	ttLock.Type("禁止输入")
	ttLock.Frame()
	sLock.mu.Lock()
	other := draftAt(draft.others, 0)
	sLock.mu.Unlock()
	if other != "" {
		t.Fatalf("提交进行中应禁用「其他」输入框，got %q", other)
	}
}

func TestApprovalRiskGrading(t *testing.T) {
	cases := []struct {
		name     string
		a        Approval
		wantText string
		wantTone Tone
		wantHigh bool
	}{
		{
			name:     "rm_rf",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "rm -rf /tmp/demo"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "git_reset_hard",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "git reset --hard HEAD"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "git_clean_upper",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "GIT CLEAN -fd"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "credential",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "ls -la", Reason: "credential"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "controlPlane",
			a:        Approval{Kind: ApprovalKindFileChange, Reason: "controlPlane"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "outside_workspace_en",
			a:        Approval{Kind: ApprovalKindFileChange, Reason: "Path is outside the workspace"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "outside_workspace_zh",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "ls", Reason: "访问工作区以外目录"},
			wantText: "高风险", wantTone: ToneDanger, wantHigh: true,
		},
		{
			name:     "npm_install",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "npm install lodash"},
			wantText: "联网/依赖", wantTone: ToneWarning, wantHigh: false,
		},
		{
			name:     "curl",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "curl -fsSL https://example.com"},
			wantText: "联网/依赖", wantTone: ToneWarning, wantHigh: false,
		},
		{
			name:     "file_change",
			a:        Approval{Kind: ApprovalKindFileChange, Title: "/workspace/main.go"},
			wantText: "写入文件", wantTone: ToneWarning, wantHigh: false,
		},
		{
			name:     "readonly_ls",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "ls -la"},
			wantText: "只读", wantTone: ToneNeutral, wantHigh: false,
		},
		{
			name:     "pipe_not_readonly",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "cat a.txt | grep x"},
			wantText: "", wantTone: ToneNeutral, wantHigh: false,
		},
		{
			name:     "multiline_not_readonly",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "ls\npwd"},
			wantText: "", wantTone: ToneNeutral, wantHigh: false,
		},
		{
			name:     "other_command",
			a:        Approval{Kind: ApprovalKindCommand, Detail: "go test ./..."},
			wantText: "", wantTone: ToneNeutral, wantHigh: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLabel, gotTone, gotHigh := approvalRiskBadge(tc.a)
			if gotLabel != tc.wantText || gotTone != tc.wantTone || gotHigh != tc.wantHigh {
				t.Fatalf("approvalRiskBadge(%+v) = (%q, %v, %v), want (%q, %v, %v)",
					tc.a, gotLabel, gotTone, gotHigh, tc.wantText, tc.wantTone, tc.wantHigh)
			}
		})
	}

	s := newChatState(NewFakeClient())
	highAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-risk-high",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "rm -rf /tmp/demo",
		Options:   allowDenyOptions(),
	}
	netAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-risk-net",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "npm install lodash",
		Options:   allowDenyOptions(),
	}
	fileAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-risk-file",
		Kind:      ApprovalKindFileChange,
		Title:     "/workspace/a.go",
		Detail:    "edit",
		Options:   allowDenyOptions(),
	}
	roAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-risk-ro",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "ls -la",
		Options:   allowDenyOptions(),
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Gap(Space3).Children(func() {
			ui.Row(c).Gap(Space3).Children(func() {
				ui.Box(c.Key("danger-swatch")).Size(12, 12).Background(ThemeDanger(c)).Label("danger-swatch")
				ui.Box(c.Key("warning-swatch")).Size(12, 12).Background(ThemeWarning(c)).Label("warning-swatch")
			})
			renderApprovalCard(c, s, highAppr, false)
			renderApprovalCard(c, s, netAppr, false)
			renderApprovalCard(c, s, fileAppr, false)
			renderApprovalCard(c, s, roAppr, false)
		})
	}, 720, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("高风险") && tt.HasText("联网/依赖") && tt.HasText("写入文件") && tt.HasText("只读")
	})

	dangerRect, ok := tt.Find("danger-swatch")
	if !ok {
		t.Fatal("未找到 danger-swatch")
	}
	warnRect, ok := tt.Find("warning-swatch")
	if !ok {
		t.Fatal("未找到 warning-swatch")
	}
	highTitleRect, ok := tt.Find(approvalCardTitle(highAppr))
	if !ok {
		t.Fatalf("未找到高风险卡片标题 %q", approvalCardTitle(highAppr))
	}

	img := tt.Image()
	dangerPx := confirmPixel(t, img, dangerRect)
	warnPx := confirmPixel(t, img, warnRect)
	y := int(highTitleRect.Y + highTitleRect.H/2)
	matchedDanger := false
	for x := 0; x < int(highTitleRect.X); x++ {
		i := img.PixOffset(x, y)
		px := [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
		if colorNear(px, dangerPx, 4) {
			matchedDanger = true
			break
		}
	}
	if !matchedDanger {
		t.Fatalf("高风险卡片左侧色条与图标应使用 ThemeDanger %v（对比 ThemeWarning %v）", dangerPx, warnPx)
	}
}

func TestApprovalTypingHoldAndHighRiskEnter(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	normalCmd := Approval{
		ThreadID:  "t1",
		RequestID: "r-hold-normal",
		Kind:      ApprovalKindCommand,
		Title:     "BashNormal",
		Detail:    "go test ./...",
		Reason:    "运行测试",
		Options:   allowDenyOptions(),
	}
	highCmd := Approval{
		ThreadID:  "t1",
		RequestID: "r-hold-high",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "rm -rf /tmp/demo",
		Options:   allowDenyOptions(),
	}

	// 1. 纯函数判定：800ms 打字窗口、600ms 新弹窗窗口、高风险禁止裸 Enter
	if !shouldHoldApprovalShortcut(normalCmd, ui.KeyEnter, true, now, now.Add(-400*time.Millisecond), now.Add(-2*time.Second)) {
		t.Fatal("草稿变动 800ms 内聚焦输入框时应暂缓 Enter")
	}
	if !shouldHoldApprovalShortcut(normalCmd, ui.Key1, true, now, now.Add(-400*time.Millisecond), now.Add(-2*time.Second)) {
		t.Fatal("草稿变动 800ms 内聚焦输入框时应暂缓数字键")
	}
	if !shouldHoldApprovalShortcut(normalCmd, ui.KeyEnter, true, now, now.Add(-2*time.Second), now.Add(-300*time.Millisecond)) {
		t.Fatal("审批出现 600ms 内聚焦输入框时应暂缓 Enter")
	}
	if !shouldHoldApprovalShortcut(normalCmd, ui.Key2, true, now, now.Add(-2*time.Second), now.Add(-300*time.Millisecond)) {
		t.Fatal("审批出现 600ms 内聚焦输入框时应暂缓数字键")
	}
	if shouldHoldApprovalShortcut(normalCmd, ui.KeyEnter, true, now, now.Add(-900*time.Millisecond), now.Add(-700*time.Millisecond)) {
		t.Fatal("超出防误触窗口后应放行 Enter")
	}
	if shouldHoldApprovalShortcut(normalCmd, ui.Key1, true, now, now.Add(-900*time.Millisecond), now.Add(-700*time.Millisecond)) {
		t.Fatal("超出防误触窗口后应放行数字键")
	}
	if shouldHoldApprovalShortcut(normalCmd, ui.KeyEscape, true, now, now.Add(-100*time.Millisecond), now.Add(-100*time.Millisecond)) {
		t.Fatal("Esc 不应被防误触窗口暂缓")
	}
	if !shouldHoldApprovalShortcut(highCmd, ui.KeyEnter, false, now, time.Time{}, now.Add(-10*time.Second)) {
		t.Fatal("高风险审批应始终拦截无修饰键裸 Enter")
	}
	if shouldHoldApprovalShortcut(highCmd, ui.Key1, false, now, time.Time{}, now.Add(-10*time.Second)) {
		t.Fatal("未聚焦输入框且过窗口时高风险审批应放行明确数字键")
	}

	// 2. renderApproval 集成验证
	oldNow := approvalNow
	var clockMu sync.Mutex
	curNow := now
	approvalNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}
	defer func() { approvalNow = oldNow }()
	advance := func(d time.Duration) {
		clockMu.Lock()
		curNow = curNow.Add(d)
		clockMu.Unlock()
	}

	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.approvals = []Approval{highCmd}
	s.composerFocused = false
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderApproval(c, s)
	}, 800, 600)
	waitFor(t, tt, func() bool { return tt.HasText("高风险") })

	// 高风险卡即使等待足够久且未聚焦输入框，裸 Enter 仍被拦截；按 1 则允许。
	advance(2 * time.Second)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if calls := fake.RespondCalls(); len(calls) != 0 {
		t.Fatalf("高风险卡片不应响应裸 Enter，got %+v", calls)
	}
	tt.Key(0, ui.Key1)
	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		return len(calls) == 1 && calls[0].Approval.RequestID == highCmd.RequestID && calls[0].OptionID == "allow"
	})

	// 切换为普通命令卡并聚焦输入框，验证 600ms 出现窗口和 800ms 打字窗口
	s.mu.Lock()
	s.approvals = []Approval{normalCmd}
	s.composerFocused = true
	s.draft = ""
	s.mu.Unlock()
	tt.Frame()
	waitFor(t, tt, func() bool { return tt.HasText("运行测试") })

	// 刚出现 200ms 内：拦截数字键和 Enter
	advance(200 * time.Millisecond)
	tt.Key(0, ui.Key1)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if calls := fake.RespondCalls(); len(calls) != 1 {
		t.Fatalf("审批刚出现 600ms 内不应响应快捷键，got %+v", calls)
	}

	// 过 600ms 后模拟打字再清空草稿，进入 800ms 打字保护期
	advance(500 * time.Millisecond)
	s.mu.Lock()
	s.draft = "abc"
	s.mu.Unlock()
	s.invalidate()
	tt.Frame()
	s.mu.Lock()
	s.draft = ""
	s.mu.Unlock()
	s.invalidate()
	tt.Frame()

	advance(300 * time.Millisecond)
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.Key1)
	tt.Frame()
	if calls := fake.RespondCalls(); len(calls) != 1 {
		t.Fatalf("草稿变动 800ms 内不应响应快捷键，got %+v", calls)
	}

	// 超过 800ms 后放行 Enter
	advance(600 * time.Millisecond)
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		return len(calls) == 2 && calls[1].Approval.RequestID == normalCmd.RequestID && calls[1].OptionID == "allow"
	})
}

func TestQuestionSingleAutoAdvanceAndFocusComposer(t *testing.T) {
	oldDelay := questionAutoAdvanceDelay
	defer func() { questionAutoAdvanceDelay = oldDelay }()

	// 1. 单题单选无自由输入：显示「补充要求」按钮，点击选项后 15ms 自动提交
	questionAutoAdvanceDelay = 15 * time.Millisecond
	single := Approval{
		ThreadID:  "t1",
		RequestID: "r-auto-single",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "单题单选自动提交",
			Choices: []ApprovalQuestionChoice{{Label: "甲"}, {Label: "乙"}},
		}},
	}
	fake1, tt1 := newApprovalTester(t, []Approval{single})
	waitFor(t, tt1, func() bool { return tt1.HasText("单题单选自动提交") })
	if !tt1.HasText("补充要求") {
		t.Fatalf("不允许自由输入时应显示「补充要求」按钮，当前文本：%q", tt1.Texts())
	}
	if err := tt1.Click("乙"); err != nil {
		t.Fatalf("click '乙' failed: %v", err)
	}
	waitFor(t, tt1, func() bool {
		call, ok := fake1.LastRespondAnswers()
		return ok && !call.Skip && reflect.DeepEqual(call.Answers, map[string][]string{"q1": {"乙"}})
	})

	// 允许自由输入时不显示「补充要求」按钮
	withOther := Approval{
		ThreadID:  "t1",
		RequestID: "r-with-other",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:          "q1",
			Prompt:      "带自由输入题",
			AllowsOther: true,
			Choices:     []ApprovalQuestionChoice{{Label: "甲"}},
		}},
	}
	ttOther := newQuestionCardTester(t, withOther)
	waitFor(t, ttOther, func() bool { return ttOther.HasText("带自由输入题") })
	if ttOther.HasText("补充要求") {
		t.Fatalf("允许自由输入时不应显示「补充要求」按钮，当前文本：%q", ttOther.Texts())
	}

	// 2. 多题单选：点击第 1 题选项自动前进到第 2 题，点击第 2 题选项自动提交
	multi := Approval{
		ThreadID:  "t1",
		RequestID: "r-auto-multi",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{
			{ID: "q1", Prompt: "第一题", Choices: []ApprovalQuestionChoice{{Label: "一甲"}, {Label: "一乙"}}},
			{ID: "q2", Prompt: "第二题", Choices: []ApprovalQuestionChoice{{Label: "二甲"}, {Label: "二乙"}}},
		},
	}
	fake2, tt2 := newApprovalTester(t, []Approval{multi})
	waitFor(t, tt2, func() bool { return tt2.HasText("第一题") })
	if err := tt2.Click("一甲"); err != nil {
		t.Fatalf("click '一甲' failed: %v", err)
	}
	waitFor(t, tt2, func() bool { return tt2.HasText("第二题") })
	if _, ok := fake2.LastRespondAnswers(); ok {
		t.Fatal("第一题自动前进到第二题时不应提前提交")
	}
	if err := tt2.Click("二乙"); err != nil {
		t.Fatalf("click '二乙' failed: %v", err)
	}
	waitFor(t, tt2, func() bool {
		call, ok := fake2.LastRespondAnswers()
		return ok && !call.Skip && reflect.DeepEqual(call.Answers, map[string][]string{"q1": {"一甲"}, "q2": {"二乙"}})
	})

	// 3. 触发前手动点「跳过」取消旧定时器，不重复提交
	questionAutoAdvanceDelay = 100 * time.Millisecond
	skipAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-auto-cancel-skip",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "取消跳过测试",
			Choices: []ApprovalQuestionChoice{{Label: "甲"}, {Label: "乙"}},
		}},
	}
	fake3, tt3 := newApprovalTester(t, []Approval{skipAppr})
	waitFor(t, tt3, func() bool { return tt3.HasText("取消跳过测试") })
	if err := tt3.Click("甲"); err != nil {
		t.Fatalf("click '甲' failed: %v", err)
	}
	if err := tt3.Click("跳过"); err != nil {
		t.Fatalf("click '跳过' failed: %v", err)
	}
	waitFor(t, tt3, func() bool {
		call, ok := fake3.LastRespondAnswers()
		return ok && call.Skip
	})
	time.Sleep(140 * time.Millisecond)
	fake3.mu.Lock()
	skipCallCount := len(fake3.respondAnswersCalls)
	fake3.mu.Unlock()
	if skipCallCount != 1 {
		t.Fatalf("手动跳过后应取消旧定时器，期望 1 次响应，实际 %d 次", skipCallCount)
	}

	// 4. 触发前改选：取消旧定时器并以新选项提交
	reselectAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-auto-reselect",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "改选取消旧定时器",
			Choices: []ApprovalQuestionChoice{{Label: "甲"}, {Label: "乙"}},
		}},
	}
	fake4, tt4 := newApprovalTester(t, []Approval{reselectAppr})
	waitFor(t, tt4, func() bool { return tt4.HasText("改选取消旧定时器") })
	if err := tt4.Click("甲"); err != nil {
		t.Fatalf("click '甲' failed: %v", err)
	}
	if err := tt4.Click("乙"); err != nil {
		t.Fatalf("click '乙' failed: %v", err)
	}
	waitFor(t, tt4, func() bool {
		call, ok := fake4.LastRespondAnswers()
		return ok && !call.Skip && reflect.DeepEqual(call.Answers, map[string][]string{"q1": {"乙"}})
	})
	time.Sleep(140 * time.Millisecond)
	fake4.mu.Lock()
	reselectCount := len(fake4.respondAnswersCalls)
	fake4.mu.Unlock()
	if reselectCount != 1 {
		t.Fatalf("改选后应只提交一次新选项，期望 1 次，实际 %d 次", reselectCount)
	}

	// 5. 点击「补充要求」取消定时器并请求聚焦主输入框
	focusAppr := Approval{
		ThreadID:  "t1",
		RequestID: "r-auto-focus",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "补充要求测试",
			Choices: []ApprovalQuestionChoice{{Label: "甲"}, {Label: "乙"}},
		}},
	}
	fake5 := NewFakeClient()
	s5 := newChatState(fake5)
	s5.mu.Lock()
	s5.approvals = []Approval{focusAppr}
	s5.mu.Unlock()
	tt5 := ui.NewTester(func(c *ui.Context) {
		renderQuestionCard(c, s5, focusAppr, false)
	}, 720, 480)
	waitFor(t, tt5, func() bool { return tt5.HasText("补充要求") })
	if err := tt5.Click("甲"); err != nil {
		t.Fatalf("click '甲' failed: %v", err)
	}
	if err := tt5.Click("补充要求"); err != nil {
		t.Fatalf("click '补充要求' failed: %v", err)
	}
	tt5.Frame()
	s5.mu.Lock()
	focusReq := s5.composerFocusRequest
	s5.mu.Unlock()
	if !focusReq {
		t.Fatal("点击「补充要求」后应设置 composerFocusRequest=true")
	}
	time.Sleep(140 * time.Millisecond)
	if _, ok := fake5.LastRespondAnswers(); ok {
		t.Fatal("点击「补充要求」后应取消自动推进，不应自动提交")
	}
}

// TestQuestionBodyShowsPromptOnly 题目区只显示 Prompt，不再单独多一行 Header；Prompt 为空时才用 Header。
func TestQuestionBodyShowsPromptOnly(t *testing.T) {
	show := func(id string, q ApprovalQuestion) *ui.Tester {
		a := Approval{ThreadID: "t1", RequestID: id, Kind: ApprovalKindQuestion, Questions: []ApprovalQuestion{q}}
		s := newChatState(NewFakeClient())
		tt := ui.NewTester(func(c *ui.Context) { renderQuestionCard(c, s, a, false) }, 720, 480)
		tt.Frame()
		return tt
	}
	choices := []ApprovalQuestionChoice{{Label: "全量覆盖"}, {Label: "仅核心流程"}}

	const sameText = "这次设计稿的覆盖范围希望怎么定?"
	tt := show("r-same-header-prompt", ApprovalQuestion{ID: "q1", Header: sameText, Prompt: sameText, Choices: choices})
	if got := countExactTexts(tt.Texts(), sameText); got != 1 {
		t.Fatalf("Header 与 Prompt 相同时文本应只出现 1 次，实际 %d 次，texts=%q", got, tt.Texts())
	}

	tt = show("r-diff-header-prompt", ApprovalQuestion{ID: "q1", Header: "覆盖范围", Prompt: "这次设计稿希望覆盖哪些模块?", Choices: choices})
	if !tt.HasText("这次设计稿希望覆盖哪些模块?") {
		t.Fatalf("应显示 Prompt，texts=%q", tt.Texts())
	}
	if tt.HasText("覆盖范围") {
		t.Fatalf("Prompt 不为空时不应再显示 Header，texts=%q", tt.Texts())
	}

	tt = show("r-header-only", ApprovalQuestion{ID: "q1", Header: "覆盖范围", Choices: choices})
	if !tt.HasText("覆盖范围") {
		t.Fatalf("Prompt 为空时应显示 Header，texts=%q", tt.Texts())
	}
}

// compactQuestionTester 渲染一张只含提问卡的 Tester，返回它与 chatState，便于读草稿。
func compactQuestionTester(t *testing.T, a Approval, reduced bool) (*chatState, *ui.Tester) {
	t.Helper()
	setAppReduceMotion(t, reduced)
	s := newChatState(NewFakeClient())
	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderQuestionCard(c, s, a, false)
	}, 720, 520, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
	return s, tt
}

func compactQuestionDraft(s *chatState, a Approval) (selected [][]int, current int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.questionDraftFor(a)
	return slices.Clone(d.selected), d.current
}

// openQuestionPickerForTest 点开右上角的题号触发器（当前第 cur 题、共 total 题）并等浮层入场结束。
func openQuestionPickerForTest(t *testing.T, tt *ui.Tester, cur, total int) {
	t.Helper()
	if err := tt.Click(fmt.Sprintf("第 %d 题，共 %d 题", cur, total)); err != nil {
		t.Fatalf("点题号触发器失败: %v", err)
	}
	tt.Advance(time.Second)
}

// TestQuestionCardCompactProgressJumpsBetweenQuestions 多题时只有「n/m ▾」触发器：没有「第 n / m 题」、「上一题」与未答计数，
// 点开浮层再点题号格可以直接跳题（替代旧的上一题按钮）。
func TestQuestionCardCompactProgressJumpsBetweenQuestions(t *testing.T) {
	a := questionApproval()
	s, tt := compactQuestionTester(t, a, false)
	tt.Advance(time.Second)
	for _, gone := range []string{"第 1 / 2 题", "上一题", "题未回答", "还有"} {
		if tt.HasText(gone) {
			t.Fatalf("定稿后不应再出现 %q：%q", gone, tt.Texts())
		}
	}
	if !tt.HasText("选择实现方案") || tt.HasText("包含哪些模块") {
		t.Fatalf("应从第一题开始：%q", tt.Texts())
	}
	if _, ok := tt.Find("第 1 题，共 2 题"); !ok {
		t.Fatalf("缺少题号触发器：%q", tt.Texts())
	}

	openQuestionPickerForTest(t, tt, 1, 2)
	if err := tt.Click("第 2 题：包含哪些模块"); err != nil {
		t.Fatalf("点题号格失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("包含哪些模块") && !tt.HasText("选择实现方案") })
	if _, cur := compactQuestionDraft(s, a); cur != 1 {
		t.Fatalf("点第 2 题后当前题应为 1，got %d", cur)
	}
	openQuestionPickerForTest(t, tt, 2, 2)
	if err := tt.Click("第 1 题：选择实现方案"); err != nil {
		t.Fatalf("点题号格失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") && !tt.HasText("包含哪些模块") })
	if _, cur := compactQuestionDraft(s, a); cur != 0 {
		t.Fatalf("点第 1 题后当前题应为 0，got %d", cur)
	}
}

// TestQuestionCardCompactKeyboardSubmit 数字键选择、回车提交，提交内容与改版前一致（原始 Label，多选按下标升序）。
func TestQuestionCardCompactKeyboardSubmit(t *testing.T) {
	oldDelay := questionAutoAdvanceDelay
	questionAutoAdvanceDelay = time.Hour
	defer func() { questionAutoAdvanceDelay = oldDelay }()

	cases := []struct {
		name  string
		multi bool
		keys  []ui.Key
		want  map[string][]string
	}{
		{"单选", false, []ui.Key{ui.Key2}, map[string][]string{"q1": {"乙 (recommended)"}}},
		{"多选", true, []ui.Key{ui.Key3, ui.Key1}, map[string][]string{"q1": {"甲", "丙"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label := "乙 (recommended)"
			a := Approval{
				ThreadID:  "t1",
				RequestID: "r-compact-keys-" + tc.name,
				Kind:      ApprovalKindQuestion,
				Questions: []ApprovalQuestion{{
					ID:             "q1",
					Prompt:         "选哪个？",
					AllowsMultiple: tc.multi,
					Choices:        []ApprovalQuestionChoice{{Label: "甲"}, {Label: label}, {Label: "丙"}},
				}},
			}
			if tc.multi {
				a.Questions[0].Choices[1].Label = "乙"
			} else {
				// 单选的推荐项默认已选，先按 1 清掉默认再按 2，验证数字键改选。
				tc.keys = append([]ui.Key{ui.Key1}, tc.keys...)
			}
			// 只渲染审批层：快捷键由 renderApproval 注册，与整窗的输入框焦点无关。
			fake := NewFakeClient()
			s := newChatState(fake)
			s.mu.Lock()
			s.approvals = []Approval{a}
			s.composerFocused = false
			s.mu.Unlock()
			tt := ui.NewTester(func(c *ui.Context) { renderApproval(c, s) }, 800, 600)
			waitFor(t, tt, func() bool { return tt.HasText("选哪个？") })
			for _, k := range tc.keys {
				tt.Key(0, k)
				tt.Frame()
			}
			if _, ok := fake.LastRespondAnswers(); ok {
				t.Fatal("只按数字键不应提交")
			}
			tt.Key(0, ui.KeyEnter)
			waitFor(t, tt, func() bool {
				call, ok := fake.LastRespondAnswers()
				return ok && !call.Skip && reflect.DeepEqual(call.Answers, tc.want)
			})
		})
	}
}

// TestQuestionCardCompactMultiSelectToggles 多选可勾选多项，再点一次取消。
func TestQuestionCardCompactMultiSelectToggles(t *testing.T) {
	a := Approval{
		ThreadID:  "t1",
		RequestID: "r-compact-multi",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:             "q1",
			Prompt:         "要哪些？",
			AllowsMultiple: true,
			Choices:        []ApprovalQuestionChoice{{Label: "选项甲"}, {Label: "选项乙"}, {Label: "选项丙"}},
		}},
	}
	s, tt := compactQuestionTester(t, a, false)
	tt.Advance(time.Second)
	for _, label := range []string{"选项丙", "选项甲"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("点击 %q 失败: %v", label, err)
		}
		tt.Frame()
	}
	if sel, _ := compactQuestionDraft(s, a); !reflect.DeepEqual(sel, [][]int{{0, 2}}) {
		t.Fatalf("多选应同时勾上甲和丙：got %v", sel)
	}
	if err := tt.Click("选项丙"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if sel, _ := compactQuestionDraft(s, a); !reflect.DeepEqual(sel, [][]int{{0}}) {
		t.Fatalf("再点一次应取消丙：got %v", sel)
	}
}

// TestQuestionCardCompactLongDetailKeepsFullTextInLabel 超长 Detail 在行内最多 3 行并省略，全文放进该行的无障碍名称。
func TestQuestionCardCompactLongDetailKeepsFullTextInLabel(t *testing.T) {
	detail := strings.Repeat("这一段说明很长，", 40)
	a := Approval{
		ThreadID:  "t1",
		RequestID: "r-compact-detail",
		Kind:      ApprovalKindQuestion,
		Questions: []ApprovalQuestion{{
			ID:      "q1",
			Prompt:  "选哪个？",
			Choices: []ApprovalQuestionChoice{{Label: "长说明项", Detail: detail}, {Label: "短项"}},
		}},
	}
	_, tt := compactQuestionTester(t, a, false)
	tt.Advance(time.Second)
	row, ok := tt.Find(questionChoiceA11yLabel("长说明项", strings.TrimSpace(detail), false))
	if !ok {
		t.Fatalf("该行的无障碍名称应含标题与说明全文：%q", tt.Texts())
	}
	short, ok := tt.Find("短项")
	if !ok {
		t.Fatalf("缺少短项：%q", tt.Texts())
	}
	// 说明最多 3 行：行高不超过 3 行说明加上下内边距与标题行。
	maxH := uiFont(TypeFootnote)*questionTextLineHeight*float32(questionDetailMaxLines+1) + 2*Space3 + Space1
	if row.H > maxH+8 {
		t.Fatalf("长说明行高 %.1f 超过 3 行说明的上限 %.1f", row.H, maxH)
	}
	if row.H <= short.H {
		t.Fatalf("长说明行应比短项更高：长 %.1f 短 %.1f", row.H, short.H)
	}
}

// TestQuestionCardCompactStaticFrameAfterAnimations 勾选与换题的动画跑完后不再请求下一帧（含减少动效）。
func TestQuestionCardCompactStaticFrameAfterAnimations(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "reduced"}[reduced], func(t *testing.T) {
			a := Approval{
				ThreadID:  "t1",
				RequestID: "r-compact-motion",
				Kind:      ApprovalKindQuestion,
				Questions: []ApprovalQuestion{
					{ID: "q1", Prompt: "第一问", AllowsMultiple: true, Choices: []ApprovalQuestionChoice{{Label: "子项甲"}, {Label: "子项乙"}}},
					{ID: "q2", Prompt: "第二问", AllowsMultiple: true, Choices: []ApprovalQuestionChoice{{Label: "子项丙"}, {Label: "子项丁"}}},
				},
			}
			_, tt := compactQuestionTester(t, a, reduced)
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("初始动画结束后界面静止，不应再请求下一帧")
			}

			if err := tt.Click("子项甲"); err != nil {
				t.Fatal(err)
			}
			if reduced && tt.FramePending() {
				t.Fatal("减少动效时勾选应直接到终点")
			}
			if !reduced && !tt.FramePending() {
				t.Fatal("普通模式勾选应有 140ms 过渡")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("勾选动画结束后不应再请求下一帧")
			}

			openQuestionPickerForTest(t, tt, 1, 2)
			if err := tt.Click("第 2 题：第二问"); err != nil {
				t.Fatal(err)
			}
			if reduced && tt.FramePending() {
				t.Fatal("减少动效时换题应直接到终点")
			}
			if !reduced && !tt.FramePending() {
				t.Fatal("普通模式换题应有 200ms 过渡")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("换题动画结束后不应再请求下一帧")
			}
			if !tt.HasText("第二问") {
				t.Fatalf("应已换到第二题：%q", tt.Texts())
			}
		})
	}
}

// questionPickerApproval 造 n 道单选题，题目为 "题目N" 加 tail。
func questionPickerApproval(id string, n int, tail string) Approval {
	qs := make([]ApprovalQuestion, n)
	for i := range qs {
		qs[i] = ApprovalQuestion{
			ID:      fmt.Sprintf("q%d", i+1),
			Prompt:  fmt.Sprintf("题目%d%s", i+1, tail),
			Choices: []ApprovalQuestionChoice{{Label: "选项甲"}, {Label: "选项乙"}},
		}
	}
	return Approval{ThreadID: "t1", RequestID: id, Kind: ApprovalKindQuestion, Questions: qs}
}

// 12 题且题目很长时，触发器宽度不超过 72px，也不挤到题目上。
func TestQuestionProgressPickerTriggerWidth(t *testing.T) {
	long := "：这次重构要覆盖到哪个范围？每个方案的影响面都不同，有的只改一个包，有的会连带改动调用方与历史卡片。"
	a := questionPickerApproval("r-picker-width", 12, long)
	// 第 10 题的「10/12」字符最多，宽度最大。
	s, tt := compactQuestionTester(t, a, false)
	s.mu.Lock()
	s.questionDraftFor(a).current = 9
	s.mu.Unlock()
	tt.Advance(time.Second)
	trigger, ok := tt.Find("第 10 题，共 12 题")
	if !ok {
		t.Fatalf("缺少题号触发器：%q", tt.Texts())
	}
	if trigger.W > 72 {
		t.Fatalf("触发器宽度 %.1f 超过 72", trigger.W)
	}
	title, ok := tt.Find("题目10" + long)
	if !ok {
		t.Fatalf("缺少题目：%q", tt.Texts())
	}
	if title.X+title.W > trigger.X+0.5 {
		t.Fatalf("题目与触发器重叠：title=%+v trigger=%+v", title, trigger)
	}
}

// 触发器显示「当前题号/总数」。
func TestQuestionProgressPickerTriggerText(t *testing.T) {
	a := questionPickerApproval("r-picker-text", 5, "")
	s, tt := compactQuestionTester(t, a, false)
	s.mu.Lock()
	s.questionDraftFor(a).current = 1
	s.mu.Unlock()
	tt.Advance(time.Second)
	if !tt.HasText("2/5") {
		t.Fatalf("触发器应显示 2/5：%q", tt.Texts())
	}
	if _, ok := tt.Find("第 2 题，共 5 题"); !ok {
		t.Fatalf("触发器缺少名称：%q", tt.Texts())
	}
}

// 点开浮层跳到第 3 题后浮层关闭；跳回第 1 题时之前的选择还在。
func TestQuestionProgressPickerJumpKeepsSelection(t *testing.T) {
	oldDelay := questionAutoAdvanceDelay
	questionAutoAdvanceDelay = time.Hour
	defer func() { questionAutoAdvanceDelay = oldDelay }()

	a := questionPickerApproval("r-picker-jump", 5, "")
	s, tt := compactQuestionTester(t, a, false)
	tt.Advance(time.Second)
	if err := tt.Click("选项乙"); err != nil {
		t.Fatal(err)
	}
	if sel, _ := compactQuestionDraft(s, a); !reflect.DeepEqual(sel[0], []int{1}) {
		t.Fatalf("第 1 题应选中选项乙，got %v", sel)
	}

	openQuestionPickerForTest(t, tt, 1, 5)
	if err := tt.Click("第 3 题：题目3"); err != nil {
		t.Fatalf("点第 3 题格失败: %v", err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("第 3 题：题目3"); return !ok })
	if _, cur := compactQuestionDraft(s, a); cur != 2 {
		t.Fatalf("应跳到第 3 题，got %d", cur)
	}
	if !tt.HasText("3/5") {
		t.Fatalf("触发器应显示 3/5：%q", tt.Texts())
	}

	openQuestionPickerForTest(t, tt, 3, 5)
	if err := tt.Click("第 1 题：题目1"); err != nil {
		t.Fatalf("点第 1 题格失败: %v", err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("第 1 题：题目1"); return !ok })
	sel, cur := compactQuestionDraft(s, a)
	if cur != 0 || !reflect.DeepEqual(sel[0], []int{1}) {
		t.Fatalf("跳回第 1 题后选择应保留：current=%d selected=%v", cur, sel)
	}
}

// 提交中触发器禁用，已展开的浮层被关掉。
func TestQuestionProgressPickerDisabledWhileResponding(t *testing.T) {
	a := questionPickerApproval("r-picker-busy", 5, "")
	s := newChatState(NewFakeClient())
	responding := false
	tt := ui.NewTester(func(c *ui.Context) { renderQuestionCard(c, s, a, responding) }, 720, 520)
	tt.Advance(time.Second)
	if info, ok := tt.Access("第 1 题，共 5 题"); !ok || info.Disabled {
		t.Fatalf("空闲时触发器应可点：%+v 存在=%v", info, ok)
	}

	openQuestionPickerForTest(t, tt, 1, 5)
	if _, ok := tt.Find("第 2 题：题目2"); !ok {
		t.Fatalf("浮层应已展开：%q", tt.Texts())
	}
	responding = true
	tt.Frame()
	tt.Advance(time.Second)
	if info, ok := tt.Access("第 1 题，共 5 题"); !ok || !info.Disabled {
		t.Fatalf("提交中触发器应禁用：%+v 存在=%v", info, ok)
	}
	if _, ok := tt.Find("第 2 题：题目2"); ok {
		t.Fatal("提交中浮层应关闭")
	}
	s.mu.Lock()
	open := s.questionDraftFor(a).pickerOpen
	s.mu.Unlock()
	if open {
		t.Fatal("提交中应清掉浮层开关")
	}
}

// 浮层展开时，数字键与回车不触发卡片自己的快捷键。
func TestQuestionProgressPickerHoldsCardShortcuts(t *testing.T) {
	oldDelay := questionAutoAdvanceDelay
	questionAutoAdvanceDelay = time.Hour
	defer func() { questionAutoAdvanceDelay = oldDelay }()

	a := questionPickerApproval("r-picker-keys", 1, "")
	a.Questions = append(a.Questions, questionPickerApproval("", 2, "").Questions[1])
	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.approvals = []Approval{a}
	s.composerFocused = false
	s.questionDraftFor(a).pickerOpen = true
	s.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderApproval(c, s) }, 800, 600)
	waitFor(t, tt, func() bool { _, ok := tt.Find("第 2 题：题目2"); return ok })
	tt.Key(0, ui.Key1)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	sel, cur := compactQuestionDraft(s, a)
	if cur != 0 || len(sel[0]) != 0 {
		t.Fatalf("浮层展开时数字键与回车应被忽略：current=%d selected=%v", cur, sel)
	}
}

// 浮层开合与跳题的动画结束后不再请求下一帧，减少动效时直接到终点。
func TestQuestionProgressPickerStaticFrame(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "reduced"}[reduced], func(t *testing.T) {
			a := questionPickerApproval("r-picker-motion", 5, "")
			_, tt := compactQuestionTester(t, a, reduced)
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("初始动画结束后界面静止，不应再请求下一帧")
			}

			if err := tt.Click("第 1 题，共 5 题"); err != nil {
				t.Fatal(err)
			}
			if reduced && tt.FramePending() {
				t.Fatal("减少动效时展开浮层应直接到终点")
			}
			if !reduced && !tt.FramePending() {
				t.Fatal("普通模式展开浮层应有过渡")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("展开动画结束后不应再请求下一帧")
			}

			if err := tt.Click("第 4 题：题目4"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("跳题与收起动画结束后不应再请求下一帧")
			}
			if _, ok := tt.Find("第 4 题：题目4"); ok {
				t.Fatal("跳题后浮层应关闭")
			}
			if !tt.HasText("4/5") {
				t.Fatalf("应已换到第 4 题：%q", tt.Texts())
			}
		})
	}
}
