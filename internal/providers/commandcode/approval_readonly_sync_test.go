package commandcode

// 本测试守住 TS 与 Go 两侧的“无需审批”边界：approval_mod.ts 的 NO_APPROVAL_NEEDED 集合里的工具
// 直接 return、不写审批请求文件，Go 侧的 autoApproves / shellMayWriteFiles 根本看不到它们。
// 因此凡是 Go 侧要按工具名判断的工具（执行命令、写改文件、会话级操作）都不能出现在
// NO_APPROVAL_NEEDED 里，否则打开不受信任的仓库时会绕过审批直接执行。

import (
	"regexp"
	"testing"

	"mcode/internal/harness"
)

// readonlySetPattern 抓 NO_APPROVAL_NEEDED 集合源码：const NO_APPROVAL_NEEDED = new Set([ ... ])。
var readonlySetPattern = regexp.MustCompile(`(?s)const NO_APPROVAL_NEEDED = new Set\(\[(.*?)\]\)`)

// readonlyNamePattern 抓集合里的工具名字符串。
var readonlyNamePattern = regexp.MustCompile(`'([A-Za-z0-9_]+)'`)

// goJudgedTools 是 Go 侧会自己判断的工具：autoApproves 的 ModeAuto 分支与 approvalRequest
// 的命令 / 文件写改分类里显式列出的名字（见 approval.go）。
var goJudgedTools = map[string]bool{
	"run_command":     true,
	"shell_command":   true,
	"powershell":      true,
	"monitor_command": true,
	"kill_shell":      true,
	"write_file":      true,
	"edit_file":       true,
	"cron_create":     true,
	"cron_delete":     true,
	"task_stop":       true,
}

// readOnlyTools 从内嵌的 mod 源码里取出 NO_APPROVAL_NEEDED 集合里的工具名。
func readOnlyTools(t *testing.T) map[string]bool {
	t.Helper()
	match := readonlySetPattern.FindStringSubmatch(approvalMod)
	if match == nil {
		t.Fatal("没能在 approval_mod.ts 源码里找到 NO_APPROVAL_NEEDED 集合")
	}
	tools := map[string]bool{}
	for _, name := range readonlyNamePattern.FindAllStringSubmatch(match[1], -1) {
		tools[name[1]] = true
	}
	if len(tools) < 5 {
		t.Fatalf("NO_APPROVAL_NEEDED 只解析出 %d 个工具，正则或集合格式可能已变", len(tools))
	}
	return tools
}

// TestApprovalModReadOnlyExcludesCommandTools 确保执行命令与写改类工具不进 NO_APPROVAL_NEEDED：
// run_command 一旦在这里，TS 侧就不写请求文件，智能体可以不经任何审批跑 shell。
func TestApprovalModReadOnlyExcludesCommandTools(t *testing.T) {
	tools := readOnlyTools(t)
	if tools["run_command"] {
		t.Fatal("NO_APPROVAL_NEEDED 不能包含 run_command：TS 侧会跳过审批，Go 侧没有机会判断")
	}
	for name := range tools {
		if goJudgedTools[name] {
			t.Fatalf("NO_APPROVAL_NEEDED 不能包含 %q：Go 侧会按工具名判断它，放进 NO_APPROVAL_NEEDED 等于绕过审批", name)
		}
	}
}

// TestApprovalModReadOnlyNotAutoApproved 确认 NO_APPROVAL_NEEDED 里的工具在 Go 侧没有任何自动放行分支：
// Go 只有按权限模式的通用判断，没有只读工具白名单，所以 NO_APPROVAL_NEEDED 只能放完全不产生变更、
// Go 也从未特殊处理过的工具名。
func TestApprovalModReadOnlyNotAutoApproved(t *testing.T) {
	gate, _ := newTestGate(t, harness.ModeAuto, nil)
	for name := range readOnlyTools(t) {
		if gate.autoApproves(name, nil) {
			t.Fatalf("NO_APPROVAL_NEEDED 里的 %q 在 Go 侧自动模式下会被放行，两侧放行依据不一致", name)
		}
	}
}
