package openai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

type mockApprover struct {
	decision harness.Decision
	err      error
	called   bool
}

func (m *mockApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	m.called = true
	return m.decision, m.err
}

func TestTools_NormalPathExecution(t *testing.T) {
	workDir := t.TempDir()

	// 1. write_file
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	turn := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeFullAccess,
	}

	callWrite := toolCall{
		Index:     0,
		ID:        "call_w1",
		Name:      "write_file",
		Arguments: `{"path":"hello.txt","content":"line1\nline2\nline3\n"}`,
	}
	out, isErr := executeToolCall(context.Background(), turn, callWrite, emit)
	if isErr {
		t.Fatalf("write_file returned error: %s", out)
	}
	if !strings.Contains(out, "Wrote hello.txt") {
		t.Errorf("write_file output unexpected: %s", out)
	}
	if len(events) == 0 || events[0].Type != harness.EventFileChange || events[0].Path != "hello.txt" {
		t.Errorf("expected EventFileChange for hello.txt, got %+v", events)
	}

	// 2. read_file
	callRead := toolCall{
		Index:     1,
		ID:        "call_r1",
		Name:      "read_file",
		Arguments: `{"path":"hello.txt","start_line":2,"line_count":1}`,
	}
	out, isErr = executeToolCall(context.Background(), turn, callRead, emit)
	if isErr {
		t.Fatalf("read_file returned error: %s", out)
	}
	if !strings.Contains(out, "lines 2-2 of 4") || !strings.Contains(out, "line2") {
		t.Errorf("read_file slicing output unexpected: %s", out)
	}

	// 3. edit_file
	events = nil
	callEdit := toolCall{
		Index:     2,
		ID:        "call_e1",
		Name:      "edit_file",
		Arguments: `{"path":"hello.txt","old_string":"line2","new_string":"line2_modified"}`,
	}
	out, isErr = executeToolCall(context.Background(), turn, callEdit, emit)
	if isErr {
		t.Fatalf("edit_file returned error: %s", out)
	}
	if !strings.Contains(out, "Edited hello.txt") {
		t.Errorf("edit_file output unexpected: %s", out)
	}
	if len(events) == 0 || events[0].Type != harness.EventFileChange {
		t.Errorf("expected EventFileChange on edit_file, got %+v", events)
	}

	// 4. search_text
	callSearch := toolCall{
		Index:     3,
		ID:        "call_s1",
		Name:      "search_text",
		Arguments: `{"pattern":"line2_modified"}`,
	}
	out, isErr = executeToolCall(context.Background(), turn, callSearch, emit)
	if isErr {
		t.Fatalf("search_text returned error: %s", out)
	}
	if !strings.Contains(out, "hello.txt:2:line2_modified") {
		t.Errorf("search_text output unexpected: %s", out)
	}

	// 5. list_files
	callList := toolCall{
		Index:     4,
		ID:        "call_l1",
		Name:      "list_files",
		Arguments: `{"recursive":false}`,
	}
	out, isErr = executeToolCall(context.Background(), turn, callList, emit)
	if isErr {
		t.Fatalf("list_files returned error: %s", out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Errorf("list_files output unexpected: %s", out)
	}
}

func TestTools_EscapeRejection(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("classified"), 0644)

	turn := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeFullAccess,
	}

	// 1. .. 相对路径越界读取
	call1 := toolCall{
		ID:        "c1",
		Name:      "read_file",
		Arguments: `{"path":"../secret.txt"}`,
	}
	out, isErr := executeToolCall(context.Background(), turn, call1, nil)
	if !isErr || !strings.Contains(out, "Stay inside") {
		t.Errorf("expected .. path rejected, got out=%q, isErr=%v", out, isErr)
	}

	// 2. 绝对路径越界写入
	call2 := toolCall{
		ID:        "c2",
		Name:      "write_file",
		Arguments: `{"path":"/etc/passwd","content":"test"}`,
	}
	out, isErr = executeToolCall(context.Background(), turn, call2, nil)
	if !isErr || !strings.Contains(out, "Stay inside") {
		t.Errorf("expected absolute path outside workDir rejected, got out=%q, isErr=%v", out, isErr)
	}

	// 3. 符号链接逃逸检查
	symlinkPath := filepath.Join(workDir, "symlink_outside")
	err := os.Symlink(outsideFile, symlinkPath)
	if err == nil {
		call3 := toolCall{
			ID:        "c3",
			Name:      "read_file",
			Arguments: `{"path":"symlink_outside"}`,
		}
		out, isErr = executeToolCall(context.Background(), turn, call3, nil)
		if !isErr || !strings.Contains(out, "Stay inside") {
			t.Errorf("expected symlink escape rejected, got out=%q, isErr=%v", out, isErr)
		}
	}
}

func TestTools_ReadOnlyModeRejection(t *testing.T) {
	workDir := t.TempDir()
	turn := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeReadOnly,
	}

	call := toolCall{
		ID:        "c_write",
		Name:      "write_file",
		Arguments: `{"path":"sample.txt","content":"hello"}`,
	}
	out, isErr := executeToolCall(context.Background(), turn, call, nil)
	if !isErr || out != userRejectedText {
		t.Fatalf("expected write_file declined in ModeReadOnly, got isErr=%v, out=%s", isErr, out)
	}
}

func TestTools_AskMode_Approver(t *testing.T) {
	workDir := t.TempDir()

	// 场景 1：Approver 为 nil，直接视为拒绝
	turnNilApprover := harness.Turn{
		WorkDir:  workDir,
		Mode:     harness.ModeSupervised,
		Approver: nil,
	}
	call := toolCall{
		ID:        "c_write1",
		Name:      "write_file",
		Arguments: `{"path":"nil_appr.txt","content":"test"}`,
	}
	out, isErr := executeToolCall(context.Background(), turnNilApprover, call, nil)
	if !isErr || out != userRejectedText {
		t.Errorf("expected nil approver to decline, got isErr=%v, out=%s", isErr, out)
	}

	// 场景 2：Approver 判定为 Deny
	apprDeny := &mockApprover{decision: harness.DecisionDeny}
	turnDeny := harness.Turn{
		WorkDir:  workDir,
		Mode:     harness.ModeSupervised,
		Approver: apprDeny,
	}
	out, isErr = executeToolCall(context.Background(), turnDeny, call, nil)
	if !isErr || out != userRejectedText {
		t.Errorf("expected approver deny, got isErr=%v, out=%s", isErr, out)
	}
	if !apprDeny.called {
		t.Errorf("expected Approver to be called")
	}

	// 场景 3：Approver 判定为 Allow
	apprAllow := &mockApprover{decision: harness.DecisionAllow}
	turnAllow := harness.Turn{
		WorkDir:  workDir,
		Mode:     harness.ModeSupervised,
		Approver: apprAllow,
	}
	out, isErr = executeToolCall(context.Background(), turnAllow, call, nil)
	if isErr || !strings.Contains(out, "Wrote nil_appr.txt") {
		t.Errorf("expected approver allow to execute write, got isErr=%v, out=%s", isErr, out)
	}
	if !apprAllow.called {
		t.Errorf("expected Approver to be called")
	}
}

// TestTools_InvalidArgumentsJSON 验证当 executeToolCall 收到非法 JSON 参数时直接返回 isError，
// 不执行写文件，且不会弹出权限审批。
func TestTools_InvalidArgumentsJSON(t *testing.T) {
	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "invalid_args.txt")

	approver := &mockApprover{decision: harness.DecisionAllow}
	turn := harness.Turn{
		WorkDir:  workDir,
		Mode:     harness.ModeSupervised,
		Approver: approver,
	}

	call := toolCall{
		ID:        "c_invalid_json",
		Name:      "write_file",
		Arguments: `{"path":"invalid_args.txt","content":"incomplete`,
	}

	out, isErr := executeToolCall(context.Background(), turn, call, nil)
	if !isErr {
		t.Fatalf("expected isError=true for invalid JSON arguments, got isError=false, out=%s", out)
	}
	if !strings.HasPrefix(out, "error: invalid tool arguments JSON:") {
		t.Errorf("expected error message to start with 'error: invalid tool arguments JSON:', got %q", out)
	}

	// 1. 断言不应触发权限审批（发生在权限审批之前）
	if approver.called {
		t.Errorf("approver should not be called when tool arguments JSON is invalid")
	}

	// 2. 断言目标文件未被写入
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("expected target file %s not to be created, but it exists", targetPath)
	}
}
