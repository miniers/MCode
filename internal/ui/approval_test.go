package mcodeui

import (
	"context"
	"fmt"
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
		return tt.HasText("第 1 / 2 题") && tt.HasText("选择实现方案") && tt.HasText("还有 1 题未回答")
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
	waitFor(t, tt, func() bool { return !tt.HasText("题未回答") })
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
	waitFor(t, tt, func() bool { return tt.HasText("第 1 / 2 题") })
	if !tt.HasText("下一题") || tt.HasText("提交") {
		t.Fatal("第一题应显示下一题而非提交")
	}
	s.mu.Lock()
	s.questionDraftFor(a).current = 1
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("第 2 / 2 题") })
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
				r, ok := tt.Find(label)
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
	if err := tt.Click("输入其他答案"); err != nil {
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

func TestQuestionOtherInstructionButton(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	if a.Questions[0].AllowsOther {
		t.Fatal("用例前提：题目不接受自由文本")
	}
	fake := NewFakeClient()
	th := Thread{ID: "t1", ProjectID: "p1", Title: "ApprovalTest", Provider: "claude", UpdatedAt: time.Now()}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {th}},
	)
	var s *chatState
	tt := ui.NewTester(Root(fake, func(cs *chatState) { s = cs }), 1000, 700)
	fake.TriggerApprovals("t1", []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("其他指示…") })
	if tt.HasText("输入其他答案") {
		t.Fatalf("不接受自由文本的题不应显示输入框：%q", tt.Texts())
	}

	if err := tt.Click("其他指示…"); err != nil {
		t.Fatalf("click '其他指示…' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := fake.LastRespondAnswers()
		return ok
	})
	call, _ := fake.LastRespondAnswers()
	if !call.Skip {
		t.Fatalf("其他指示应跳过提问，got %+v", call)
	}
	if len(call.Answers) != 0 {
		t.Fatalf("其他指示不应提交答案，got %v", call.Answers)
	}
	// 跳过之后焦点应回到会话输入框，用户可以直接用下一条消息补充指示。
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})
}

func TestQuestionAllowsOtherHidesOtherInstructionButton(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	a.Questions[0].AllowsOther = true
	_, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("输入其他答案") })
	if tt.HasText("其他指示…") {
		t.Fatalf("接受自由文本的题不应再显示「其他指示…」：%q", tt.Texts())
	}
}

func TestQuestionCardChoiceReplacesSingleOther(t *testing.T) {
	a := questionApproval()
	a.Questions = a.Questions[:1]
	a.Questions[0].AllowsOther = true
	fake, tt := newApprovalTester(t, []Approval{a})
	waitFor(t, tt, func() bool { return tt.HasText("选择实现方案") })
	if err := tt.Click("输入其他答案"); err != nil {
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
	// 计划卡：状态胶囊、Markdown 正文与底部按钮都应同屏可见。
	waitFor(t, tt, func() bool {
		return tt.HasText("待实施") && tt.HasText("整体思路") && tt.HasText("预计跑通全部 UI 测试。") &&
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
	fake, tt := newApprovalTester(t, []Approval{planApproval()})
	waitFor(t, tt, func() bool { return tt.HasText("驳回") })
	if err := tt.Click("驳回"); err != nil {
		t.Fatalf("click '驳回' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认驳回") })
	tt.Type("请补充测试")
	tt.Frame()
	if err := tt.Click("确认驳回"); err != nil {
		t.Fatalf("click '确认驳回' failed: %v", err)
	}
	want := map[string][]string{"feedback": {"请补充测试"}}
	waitFor(t, tt, func() bool {
		call, ok := fake.LastRespondAnswers()
		return ok && call.OptionID == "deny" && !call.Skip && reflect.DeepEqual(call.Answers, want)
	})
}

func TestApprovalShortcutAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	normalAppr := Approval{
		RequestID: "req-normal",
		Kind:      ApprovalKindCommand,
		Options: []ApprovalOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "allow_always", Title: "始终允许", Role: "approveAlways"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		},
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
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

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

// 测试提问卡选项右侧的数字键帽：数字键确实对这些选项生效，键帽贴在文字右侧。
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
					t.Fatalf("选项 %q 缺少键帽 %d，当前文本：%q", label, i+1, tt.Texts())
				}
				labelRect, ok := tt.Find(label)
				if !ok {
					t.Fatalf("未找到选项文字 %q", label)
				}
				if badge.X < labelRect.X {
					t.Errorf("选项 %q 的键帽应位于文字右侧：badge.X=%.1f label.X=%.1f", label, badge.X, labelRect.X)
				}
			}
		})
	}
}

// 测试 12 个选项时只给前 9 个画键帽：数字键 1~9 才是实际生效的快捷键。
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
			t.Fatalf("第 %d 个选项应有键帽 %q，当前文本：%q", i+1, want, tt.Texts())
		}
	}
	for _, want := range []string{"10", "11", "12"} {
		if _, ok := tt.Find(want); ok {
			t.Fatalf("第 %s 个选项不应画键帽，当前文本：%q", want, tt.Texts())
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
