package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const commandCardTestMultiStep = "go build ./... && go vet ./... && go test ./..."

func commandCardTestMessage(id, command, status string, age time.Duration) Message {
	input := fmt.Sprintf(`{"command":%q}`, command)
	return Message{
		ID: id, Role: "tool", ToolName: "Bash", ToolStatus: status, ToolInput: input,
		Text:      "Bash " + input + "\n输出一行",
		CreatedAt: time.Now().Add(-age).Format(time.RFC3339Nano),
	}
}

// commandCardTestActivity 造一份已有进程采样的活动状态：CPU 99%，内存 8 GB，一个 compile 进程。
func commandCardTestActivity(command string, sampled bool) *commandActivityState {
	st := &commandActivityState{command: command, steps: commandSteps(command), previous: make(map[int]CommandProc)}
	if sampled {
		st.hasProcess = true
		st.sampleCount = 2
		st.cpu = 99
		st.history = []float64{40, 99}
		st.rss = 8 << 30
		st.maxStep = 1
		st.lastBusyAt = time.Now()
		st.processes = []commandProcessView{{name: "compile", object: "ui", cpu: 78, rss: 8 << 30}}
	}
	return st
}

func commandCardTestState(m Message, st *commandActivityState) *chatState {
	s := newChatState(NewFakeClient())
	s.expandedTool[m.ID] = true
	s.commandActivity = map[string]*commandActivityState{m.ID: st}
	return s
}

func commandCardTestTitle(m Message) string {
	verb, target, _ := toolCallTitle(m)
	return toolChipShortTitle(m, verb, target)
}

// commandCardHover 把指针移到名为 label 的元素中心，并推进时钟让悬停动画走完。
func commandCardHover(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("找不到 %q，当前文本 %q", label, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(DurSlow)
	tt.Frame()
}

func commandCardTextCount(tt *ui.Tester, sub string) int {
	n := 0
	for _, text := range tt.Texts() {
		if strings.Contains(text, sub) {
			n++
		}
	}
	return n
}

func TestCommandCardRunningMultiStep(t *testing.T) {
	setAppReduceMotion(t, true)
	m := commandCardTestMessage("card-multi", commandCardTestMultiStep, "running", 10*time.Second)
	s := commandCardTestState(m, commandCardTestActivity(commandCardTestMultiStep, true))
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	verb, target, detail := toolCallTitle(m)
	if _, ok := tt.Find(toolCallRowLabel(toolCallVerb(verb, m), target, detail)); ok {
		t.Fatalf("运行中的命令不应再渲染工具胶囊: %q", tt.Texts())
	}
	if n := commandCardTextCount(tt, "99%"); n != 1 {
		t.Fatalf("CPU 百分比应只出现一次，实际 %d 次: %q", n, tt.Texts())
	}
	for _, banned := range []string{"1/3", "CPU "} {
		if tt.HasText(banned) {
			t.Fatalf("卡片不应出现 %q: %q", banned, tt.Texts())
		}
	}
	for _, step := range []string{"go build ./...", "go vet ./...", "go test ./..."} {
		if !tt.HasText(step) {
			t.Fatalf("展开后应列出步骤 %q: %q", step, tt.Texts())
		}
	}
	if tt.HasText("compile") {
		t.Fatalf("进程行只在悬停浮层里出现: %q", tt.Texts())
	}
	commandCardHover(t, tt, commandCardTestTitle(m))
	if _, ok := tt.Find("终止此命令"); !ok {
		t.Fatalf("运行中的命令应有终止按钮: %q", tt.Texts())
	}
}

func TestCommandCardBackground(t *testing.T) {
	setAppReduceMotion(t, true)
	m := backgroundTasksTestBackground("card-bg")
	s := commandCardTestState(m, commandCardTestActivity("npm run build", false))
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("后台 ·") {
		t.Fatalf("后台命令的耗时应带「后台」: %q", tt.Texts())
	}
	commandCardHover(t, tt, commandCardTestTitle(m))
	if _, ok := tt.Find("复制命令"); !ok {
		t.Fatalf("后台命令应有复制按钮: %q", tt.Texts())
	}
	if _, ok := tt.Find("终止此命令"); ok {
		t.Fatal("后台命令不应渲染终止按钮")
	}
}

func TestCommandCardGaugePopover(t *testing.T) {
	setAppReduceMotion(t, true)
	m := commandCardTestMessage("card-gauge", commandCardTestMultiStep, "running", 10*time.Second)
	st := commandCardTestActivity(commandCardTestMultiStep, true)
	s := commandCardTestState(m, st)
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	memory := formatFileSize(st.rss)
	if tt.HasText("compile") || tt.HasText(memory) {
		t.Fatalf("未悬停时不应显示进程与内存: %q", tt.Texts())
	}
	r, ok := tt.Find("资源占用")
	if !ok {
		t.Fatalf("有进程采样时应显示 CPU 仪表: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	waitFor(t, tt, func() bool { return tt.HasText("compile") && tt.HasText(memory) })
	tt.Move(890, 490)
	waitFor(t, tt, func() bool { return !tt.HasText("compile") })
}

func TestCommandCardNoGaugeWithoutSample(t *testing.T) {
	setAppReduceMotion(t, true)
	m := commandCardTestMessage("card-nosample", "go test ./...", "running", 10*time.Second)
	s := commandCardTestState(m, commandCardTestActivity("go test ./...", false))
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find("资源占用"); ok {
		t.Fatalf("没有进程采样时不应显示 CPU 仪表: %q", tt.Texts())
	}
}

func TestCommandCardIdleAfterSampleAnimation(t *testing.T) {
	setAppReduceMotion(t, false)
	m := commandCardTestMessage("card-anim", commandCardTestMultiStep, "running", 2*time.Second)
	s := commandCardTestState(m, commandCardTestActivity(commandCardTestMultiStep, true))
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
	tt.Frame()
	s.mu.Lock()
	updateCommandActivity(s.commandActivity[m.ID], []CommandProc{
		{PID: 2, Command: "/go/compile -p ui", StartTime: tt.Now().Add(-10 * time.Second), CPUTime: 8 * time.Second, RSS: 1 << 30},
	}, tt.Now())
	s.mu.Unlock()
	tt.Frame()
	for i := 0; i < 20; i++ {
		tt.Advance(50 * time.Millisecond)
	}
	if tt.FramePending() {
		t.Fatal("采样更新的动画结束后不应再请求动画帧（每秒刷新耗时的定时除外）")
	}
}

func TestCommandCardStalled(t *testing.T) {
	setAppReduceMotion(t, true)
	cases := []struct {
		name    string
		sampled bool
		want    string
	}{
		{"空闲", true, "空闲"},
		{"无输出", false, "无输出"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := commandCardTestMessage("card-stalled-"+tc.name, "go test ./...", "running", 5*time.Minute)
			st := commandCardTestActivity("go test ./...", tc.sampled)
			st.lastBusyAt = time.Now().Add(-2 * time.Minute)
			s := commandCardTestState(m, st)
			s.toolRunIdle = map[string]toolRunIdleState{m.ID: {textLen: len(m.Text), changedAt: time.Now().Add(-2 * time.Minute)}}
			tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 900, 500)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}
			if !tt.HasText(tc.want) {
				t.Fatalf("长时间无输出时应显示 %q: %q", tc.want, tt.Texts())
			}
		})
	}
}

func TestCommandCardBadgeHasNoStepsOrCPU(t *testing.T) {
	m := commandCardTestMessage("card-badge", commandCardTestMultiStep, "running", time.Second)
	s := commandCardTestState(m, commandCardTestActivity(commandCardTestMultiStep, true))
	var badge string
	var stalled bool
	tt := ui.NewTester(func(c *ui.Context) { badge, stalled = s.runningCommandBadge(c, m) }, 400, 200)
	tt.Frame()
	if stalled || badge == "" || strings.Contains(badge, "CPU") || strings.Contains(badge, "/3") {
		t.Fatalf("badge = %q, stalled = %v", badge, stalled)
	}
}

func TestCommandCardBarHeightQuantized(t *testing.T) {
	for _, tc := range []struct {
		value, peak float64
		full, want  float32
	}{
		{0, 100, 12, 1},
		{50, 100, 12, 6},
		{99, 100, 12, 12},
		{340, 340, 12, 12},
		{33, 100, 12, 4},
	} {
		got := commandGaugeBarHeight(tc.value, tc.peak, tc.full)
		if got != tc.want {
			t.Errorf("commandGaugeBarHeight(%v, %v, %v) = %v, want %v", tc.value, tc.peak, tc.full, got, tc.want)
		}
	}
}
