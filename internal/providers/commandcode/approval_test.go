package commandcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

// recordingApprover 记录审批请求并返回固定判决。
type recordingApprover struct {
	decision harness.Decision
	requests []harness.ApprovalRequest
}

func (a *recordingApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.requests = append(a.requests, req)
	return a.decision, nil
}

// recordingInteractor 在 Approver 之上实现 Interactor。
type recordingInteractor struct {
	recordingApprover
	response harness.ApprovalResponse
}

func (a *recordingInteractor) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.requests = append(a.requests, req)
	return a.response, nil
}

// newTestGate 在临时目录里建一个 gate。
func newTestGate(t *testing.T, mode harness.PermissionMode, approver harness.Approver) (*gate, string) {
	t.Helper()
	dir := t.TempDir()
	turn := harness.Turn{ID: "turn-1", Mode: mode, Approver: approver}
	return newGate(dir, turn, map[string]bool{}), dir
}

// writeRequest 模拟 mod 写一条请求文件。
func writeRequest(t *testing.T, dir, id, toolName, input string) {
	t.Helper()
	payload := `{"toolCallId":"` + id + `","toolName":"` + toolName + `","input":` + input + `}`
	if err := os.WriteFile(filepath.Join(dir, id+".request.json"), []byte(payload), 0o600); err != nil {
		t.Fatalf("写入请求文件失败: %v", err)
	}
}

// readReply 读回复文件里的 decision；没有回复文件时返回空串。
func readReply(t *testing.T, dir, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".reply.json"))
	if err != nil {
		return ""
	}
	var payload struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("回复文件不是合法 JSON: %v", err)
	}
	return payload.Decision
}

func TestApprovalModEmbedded(t *testing.T) {
	if !strings.Contains(approvalMod, "beforeToolCall") || !strings.Contains(approvalMod, "MCODE_APPROVALS") {
		t.Fatal("审批 mod 源码应内嵌（approval_mod.ts）")
	}
}

func TestGateAutoApproveModes(t *testing.T) {
	tests := []struct {
		name       string
		mode       harness.PermissionMode
		toolName   string
		input      string
		wantAsked  bool
		wantDecide string
	}{
		{"监督模式命令要问", harness.ModeSupervised, "shell_command", `{"command":"ls -la"}`, true, "allow"},
		{"自动接受编辑放行写文件", harness.ModeAutoAcceptEdits, "write_file", `{"file_path":"a.go","content":"x"}`, false, "allow"},
		{"自动接受编辑仍要问命令", harness.ModeAutoAcceptEdits, "shell_command", `{"command":"ls"}`, true, "allow"},
		{"自动模式放行只读命令", harness.ModeAuto, "shell_command", `{"command":"ls -la && git status"}`, false, "allow"},
		{"自动模式拦截可写命令", harness.ModeAuto, "shell_command", `{"command":"rm -rf build"}`, true, "allow"},
		{"自动模式放行 run_command 只读命令", harness.ModeAuto, "run_command", `{"command":"ls"}`, false, "allow"},
		{"自动模式拦截 run_command 可写命令", harness.ModeAuto, "run_command", `{"command":"rm a"}`, true, "allow"},
		{"自动模式放行停 shell", harness.ModeAuto, "kill_shell", `{"id":"1"}`, false, "allow"},
		{"只读模式命令要问", harness.ModeReadOnly, "shell_command", `{"command":"ls"}`, true, "allow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approver := &recordingApprover{decision: harness.DecisionAllow}
			gate, dir := newTestGate(t, test.mode, approver)
			writeRequest(t, dir, "tc-1", test.toolName, test.input)
			gate.scan(context.Background())

			if asked := len(approver.requests) > 0; asked != test.wantAsked {
				t.Fatalf("是否询问审批方应为 %v，得到 %v（请求: %+v）", test.wantAsked, asked, approver.requests)
			}
			if got := readReply(t, dir, "tc-1"); got != test.wantDecide {
				t.Fatalf("回复 decision 应为 %q，得到 %q", test.wantDecide, got)
			}
			// 请求文件读完即删，不会再被处理一次。
			if _, err := os.Stat(filepath.Join(dir, "tc-1.request.json")); !os.IsNotExist(err) {
				t.Fatal("请求文件应在读取后删除")
			}
		})
	}
}

func TestGateRoundTripWithApprover(t *testing.T) {
	approver := &recordingApprover{decision: harness.DecisionDeny}
	gate, dir := newTestGate(t, harness.ModeSupervised, approver)
	writeRequest(t, dir, "tc-7", "shell_command", `{"command":"git push origin main"}`)

	gate.scan(context.Background())

	if len(approver.requests) != 1 {
		t.Fatalf("审批方应收到一次请求，得到 %+v", approver.requests)
	}
	request := approver.requests[0]
	if request.ToolID != "tc-7" || request.ToolName != "shell_command" || request.Kind != harness.RequestKindCommand {
		t.Fatalf("审批请求不符: %+v", request)
	}
	if request.Summary != "git push origin main" {
		t.Fatalf("摘要应取命令，得到 %q", request.Summary)
	}
	if got := readReply(t, dir, "tc-7"); got != "deny" {
		t.Fatalf("回复 decision 应为 deny，得到 %q", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tc-7.reply.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Reason != denialReason {
		t.Fatalf("拒绝应带上说明，得到 %q", reply.Reason)
	}
	// 回复文件先写 .tmp 再改名，不该留下中间文件。
	if _, err := os.Stat(filepath.Join(dir, "tc-7.reply.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("回复文件不应留下 .tmp")
	}
}

func TestGateDeniesWithoutApprover(t *testing.T) {
	gate, dir := newTestGate(t, harness.ModeSupervised, nil)
	writeRequest(t, dir, "tc-8", "write_file", `{"file_path":"a.txt","content":"x"}`)

	gate.scan(context.Background())

	if got := readReply(t, dir, "tc-8"); got != "deny" {
		t.Fatalf("没有审批方时应拒绝，得到 %q", got)
	}
}

func TestGateSessionGrant(t *testing.T) {
	interactor := &recordingInteractor{
		response: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"tc-1": {"always"}},
		},
	}
	gate, dir := newTestGate(t, harness.ModeSupervised, interactor)

	writeRequest(t, dir, "tc-1", "shell_command", `{"command":"ls"}`)
	gate.scan(context.Background())
	if got := readReply(t, dir, "tc-1"); got != "allow" {
		t.Fatalf("会话级答复应放行，得到 %q", got)
	}
	if len(interactor.requests) != 1 {
		t.Fatalf("应答应来自 Interactor，得到 %+v", interactor.requests)
	}

	// 同一种类（命令）的下一次请求不再询问。
	writeRequest(t, dir, "tc-2", "shell_command", `{"command":"rm -rf /"}`)
	gate.scan(context.Background())
	if got := readReply(t, dir, "tc-2"); got != "allow" {
		t.Fatalf("会话级放行后应直接放行，得到 %q", got)
	}
	if len(interactor.requests) != 1 {
		t.Fatalf("会话级放行后不应再问，得到 %+v", interactor.requests)
	}
}

func TestGateFileChangeKind(t *testing.T) {
	approver := &recordingApprover{decision: harness.DecisionAllow}
	gate, dir := newTestGate(t, harness.ModeSupervised, approver)
	writeRequest(t, dir, "tc-9", "write_file", `{"file_path":"src/main.go","content":"package main"}`)

	gate.scan(context.Background())

	if len(approver.requests) != 1 || approver.requests[0].Kind != harness.RequestKindFileChange {
		t.Fatalf("写文件应归到 fileChange，得到 %+v", approver.requests)
	}
	if approver.requests[0].Summary != "src/main.go" {
		t.Fatalf("摘要应取路径，得到 %q", approver.requests[0].Summary)
	}
}

func TestGatePollRepliesInTime(t *testing.T) {
	gate, dir := newTestGate(t, harness.ModeSupervised, &recordingApprover{decision: harness.DecisionAllow})
	gate.interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gate.poll(ctx)

	writeRequest(t, dir, "tc-10", "shell_command", `{"command":"ls"}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && readReply(t, dir, "tc-10") == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := readReply(t, dir, "tc-10"); got != "allow" {
		t.Fatalf("轮询应写回回复，得到 %q", got)
	}
}

func TestShellMayWriteFiles(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"", false},
		{"ls -la", false},
		{"cat main.go | grep foo", false},
		{"grep -rn foo . && git log -1", false},
		{"ls; cd /tmp", false},
		{"git status", false},
		{"sed -n '1,5p' file", false},
		{"sed -i 's/a/b/' file", true},
		{"find . -name '*.go'", false},
		{"find . -delete", true},
		{"sort -o out.txt in.txt", true},
		{"python3 --version", false},
		{"python3 build.py", true},
		{"awk '{print $1}' f", false},
		{"echo hi > out.txt", true},
		{"echo $(cat file)", true},
		{"rm -rf build", true},
		{"git commit -m x", true},
		{"xcodebuild -list", false},
		{"env", false},
		{"env FOO=1 python3 -c 1", true},
		// git 的写型子命令不能当成只读，纯列出形式仍然只读。
		{"git branch", false},
		{"git branch -v", false},
		{"git branch -D main", true},
		{"git branch new-branch", true},
		{"git tag -d v1.0", true},
		{"git tag -l 'v*'", true}, // 带 pattern 时按更保守处理（会询问）
		{"git remote add origin git@example.com:x/y.git", true},
		{"git remote -v", false},
		{"git config user.name someone", true},
		{"git config --list", false},
		// yq 的 -i/--inplace 会原地改写文件。
		{"yq '.a.b' f.yaml", false},
		{"yq -i '.a.b = 1' f.yaml", true},
		{"yq --inplace '.a.b = 1' f.yaml", true},
	}
	for _, test := range tests {
		if got := shellMayWriteFiles(test.command); got != test.want {
			t.Errorf("shellMayWriteFiles(%q) = %v，期望 %v", test.command, got, test.want)
		}
	}
}
