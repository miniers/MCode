// 跳过依赖 pi 宿主的工具注册、ctx.ui 执行、环境变量及 vision 子进程参数用例。
package gate

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

func TestCheck(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	gateFile := filepath.Join(t.TempDir(), "gate.json")
	type testCase struct {
		name               string
		mode               harness.PermissionMode
		plan, final        bool
		tool, field, value string
		action             Action
		sensitive          string
	}
	cases := []testCase{
		{"env_source_full", harness.ModeFullAccess, false, false, "bash", "command", "source .env 2>/dev/null; security find-identity", Allow, ""},
		{"env_local_full", harness.ModeFullAccess, false, false, "bash", "command", "cat ./.env.local", Allow, ""},
		{"env_read_full", harness.ModeFullAccess, false, false, "read", "path", filepath.Join(work, ".env"), Allow, ""},
		{"env_write_full", harness.ModeFullAccess, false, false, "write", "path", ".env", Allow, ""},
		{"env_ssh_full", harness.ModeFullAccess, false, false, "bash", "command", "cat ~/.ssh/id_rsa", Ask, "credential"},
		{"env_absolute_outside_full", harness.ModeFullAccess, false, false, "bash", "command", "cat /tmp/elsewhere/.env", Ask, "credential"},
		{"env_relative_outside_full", harness.ModeFullAccess, false, false, "bash", "command", "cat ../x/.env", Ask, "credential"},
		{"env_mixed_full", harness.ModeFullAccess, false, false, "bash", "command", "cat .env ../x/.env", Ask, "credential"},
		{"env_read_outside_full", harness.ModeFullAccess, false, false, "read", "path", "../x/.env", Ask, "credential"},
		{"env_dotdot_prefix_matches_ts", harness.ModeFullAccess, false, false, "read", "path", "..private/.env", Ask, "credential"},
		{"env_write_outside_full", harness.ModeFullAccess, false, false, "write", "path", "../x/.env", Ask, "credential"},
		{"env_source_auto", harness.ModeAuto, false, false, "bash", "command", "source .env", Ask, "credential"},
		{"env_control_full", harness.ModeFullAccess, false, false, "write", "path", gateFile, Ask, "controlPlane"},
		{"a_write", harness.ModeFullAccess, false, false, "write", "path", "normal.txt", Allow, ""},
		{"a_bash", harness.ModeFullAccess, false, false, "bash", "command", "make build", Allow, ""},
		{"b_read_ssh", harness.ModeFullAccess, false, false, "read", "path", filepath.Join(home, ".ssh/id_rsa"), Ask, "credential"},
		{"c_bash_aws", harness.ModeAuto, false, false, "bash", "command", "cat ~/.aws/credentials", Ask, "credential"},
		{"c_bash_ls", harness.ModeAuto, false, false, "bash", "command", "ls -la", Allow, ""},
		{"d_read_env", harness.ModeAuto, false, false, "read", "path", ".env", Ask, "credential"},
		{"d_read_env_example", harness.ModeAuto, false, false, "read", "path", ".env.example", Allow, ""},
		{"e_write_gate_dir", harness.ModeFullAccess, false, false, "write", "path", filepath.Join(filepath.Dir(gateFile), "injected.json"), Allow, ""},
		{"e_bash_tmpdir_loop", harness.ModeFullAccess, false, false, "bash", "command", "for p in a b; do printf x; done", Allow, ""},
		{"e_write_tmpdir_file", harness.ModeFullAccess, false, false, "write", "path", filepath.Join(filepath.Dir(gateFile), "repo/x.txt"), Allow, ""},
		{"e_write_gate_file", harness.ModeFullAccess, false, false, "write", "path", gateFile, Ask, "controlPlane"},
		{"e_bash_rm_gate_file", harness.ModeFullAccess, false, false, "bash", "command", "rm " + gateFile, Ask, "controlPlane"},
		{"e_write_agent_models", harness.ModeFullAccess, false, false, "write", "path", filepath.Join(home, ".pi/agent/models.json"), Ask, "controlPlane"},
		{"f_write_plan", "", true, false, "write", "path", "plan.md", Deny, ""},
		{"f_read_plan", "", true, false, "read", "path", "plan.md", Allow, ""},
		{"g_read_final_report", "", false, true, "read", "path", "final.txt", Deny, ""},
		{"h_edit_supervised", harness.ModeSupervised, false, false, "edit", "path", "main.go", Ask, ""},
		{"i_mcp_list_supervised", harness.ModeSupervised, false, false, "mcode_mcp_list", "", "", Allow, ""},
		{"j_bash_default_timeout", harness.ModeFullAccess, false, false, "bash", "command", "echo hello", Allow, ""},
		{"j_bash_timeout_5000_clamped", harness.ModeFullAccess, false, false, "bash", "command", "sleep 10", Allow, ""},
		{"j_bash_timeout_60_preserved", harness.ModeFullAccess, false, false, "bash", "command", "sleep 1", Allow, ""},
		{"s_bash_rg_plan", "", true, false, "bash", "command", "rg -n planBlocksTool internal/", Allow, ""},
		{"s_bash_git_status_plan", "", true, false, "bash", "command", "git status", Allow, ""},
		{"s_bash_redirect_plan", "", true, false, "bash", "command", "echo x > f", Deny, ""},
		{"s_bash_rm_plan", "", true, false, "bash", "command", "rm -rf build", Deny, ""},
		{"t_mcp_mcode_plan", harness.ModeFullAccess, true, false, "mcp__mcode__kanban", "action", "read_board", Allow, ""},
		{"t_mcp_mcode_plan_supervised", harness.ModeSupervised, true, false, "mcp__mcode__kanban", "action", "read_board", Ask, ""},
		{"t_mcp_other_plan", "", true, false, "mcp__other__run", "cmd", "ls", Deny, ""},
		{"u_grep_plan", "", true, false, "grep", "pattern", "planBlocksTool", Allow, ""},
		{"u_edit_plan", "", true, false, "edit", "path", "planlimits.go", Deny, ""},
		{"k_ask_user_question_supervised", harness.ModeSupervised, false, false, "ask_user_question", "question", "Proceed?", Allow, ""},
		{"todo_write_supervised", harness.ModeSupervised, false, false, "todo_write", "", "", Allow, ""},
		{"todo_write_plan", harness.ModeSupervised, true, false, "todo_write", "", "", Allow, ""},
		{"l_ask_user_question_final_report", "", false, true, "ask_user_question", "question", "Final?", Deny, ""},
		{"p_vision_absent_without_model", harness.ModeSupervised, false, false, "read", "path", "note.txt", Allow, ""},
		{"q_vision_registered_with_model", harness.ModeSupervised, false, false, "vision", "path", "shot.png", Allow, ""},
		{"r_vision_supervised_allow", harness.ModeSupervised, false, false, "vision", "path", "screenshot.png", Allow, ""},
		{"s_vision_credential_ask", harness.ModeFullAccess, false, false, "vision", "path", filepath.Join(home, ".ssh/id_rsa"), Ask, "credential"},
		{"t_vision_final_report_block", "", false, true, "vision", "path", "final.png", Deny, ""},
		{"y_bash_ls_control_plane_devnull", harness.ModeFullAccess, false, false, "bash", "command", "ls -l " + filepath.Join(home, ".pi/agent/sessions") + " 2>/dev/null | head", Allow, ""},
		{"y_bash_du_control_plane", harness.ModeFullAccess, false, false, "bash", "command", "du -sh " + filepath.Join(home, ".pi/agent/sessions"), Allow, ""},
		{"y_bash_redirect_control_plane", harness.ModeFullAccess, false, false, "bash", "command", "echo x > " + filepath.Join(home, ".pi/agent/models.json"), Ask, "controlPlane"},
		{"y_bash_cat_control_plane_auth", harness.ModeFullAccess, false, false, "bash", "command", "cat " + filepath.Join(home, ".pi/agent/auth.json"), Ask, "credential"},
		{"y_bash_auto_devnull", harness.ModeAuto, false, false, "bash", "command", "ls -la 2>/dev/null", Allow, ""},
		{"y_bash_auto_dup_stderr", harness.ModeAuto, false, false, "bash", "command", "cat a 2>&1 | head", Allow, ""},
		{"y_bash_auto_write_redirect", harness.ModeAuto, false, false, "bash", "command", "ls > out.txt", Ask, ""},
		{"y_bash_plan_devnull", "", true, false, "bash", "command", "rg foo 2>/dev/null", Allow, ""},
		{"z_bash_readonly_grep_allow", harness.ModeReadOnly, false, false, "bash", "command", "grep -n foo bar.txt", Allow, ""},
		{"z_bash_readonly_case_insensitive_rm_ask", harness.ModeReadOnly, false, false, "bash", "Command", "rm -rf build", Ask, ""},
		{"z_bash_plan_case_insensitive_rm_deny", "", true, false, "bash", "Command", "rm -rf build", Deny, ""},
		{"z_bash_auto_tree_output_ask", harness.ModeAuto, false, false, "bash", "command", "tree -o out.txt internal", Ask, ""},
		{"z_bash_auto_tree_long_output_ask", harness.ModeAuto, false, false, "bash", "command", "tree --output=out.txt internal", Ask, ""},
		{"z_write_mcode_always_allow", harness.ModeFullAccess, false, false, "write", "path", filepath.Join(home, ".mcode/always_allow.json"), Ask, "controlPlane"},
		{"z_bash_readonly_rm_ask", harness.ModeReadOnly, false, false, "bash", "command", "rm a.txt", Ask, ""},
		{"z_bash_supervised_grep_ask", harness.ModeSupervised, false, false, "bash", "command", "grep -n foo bar.txt", Ask, ""},
		{"z_bash_auto_cd_and_ls", harness.ModeAuto, false, false, "bash", "command", "cd internal && ls", Allow, ""},
		{"z_bash_auto_git_ls_files", harness.ModeAuto, false, false, "bash", "command", "git ls-files dist", Allow, ""},
		{"z_bash_auto_git_worktree_list", harness.ModeAuto, false, false, "bash", "command", "git worktree list", Allow, ""},
		{"z_bash_auto_grep_pipe_head", harness.ModeAuto, false, false, "bash", "command", `grep -n "outDir\|webassets" web/vite.config.ts | head -20`, Allow, ""},
		{"z_bash_auto_echo_then_grep", harness.ModeAuto, false, false, "bash", "command", `echo "--- scripts:"; grep -n '"scripts"' -A 15 web/package.json`, Allow, ""},
		{"z_bash_auto_git_worktree_add_ask", harness.ModeAuto, false, false, "bash", "command", "git worktree add ../x", Ask, ""},
		{"z_bash_auto_quoted_semicolon_ask", harness.ModeAuto, false, false, "bash", "command", `grep "a; rm b" f; rm c`, Ask, ""},
		{"z_bash_auto_unterminated_quote_ask", harness.ModeAuto, false, false, "bash", "command", `grep "unterminated f`, Ask, ""},
		{"z_bash_auto_cd_and_rm_ask", harness.ModeAuto, false, false, "bash", "command", "cd x && rm y", Ask, ""},
		{"bash_output_supervised", harness.ModeSupervised, false, false, "bash_output", "id", "bg1", Allow, ""},
		{"kill_bash_supervised", harness.ModeSupervised, false, false, "kill_bash", "id", "bg1", Allow, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.field != "" {
				args[tc.field] = tc.value
			}
			raw, _ := json.Marshal(args)
			got := Check(Input{Mode: tc.mode, Plan: tc.plan, FinalReport: tc.final, ToolName: tc.tool, Args: raw, WorkDir: work, ControlPlanePaths: []string{gateFile}})
			if got.Action != tc.action || got.Request.Sensitive != tc.sensitive || (got.SessionKey != "") != (tc.action == Ask && tc.sensitive == "") {
				t.Errorf("得到 %+v，期望 action=%s sensitive=%s", got, tc.action, tc.sensitive)
			}
		})
	}
}

// TestWorkDirInsideControlPlane 覆盖子代理副本位于 ~/.mcode 内时的控制面判断。
func TestWorkDirInsideControlPlane(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".mcode", "wt", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(mode harness.PermissionMode, tool, field, value, workDir string) Verdict {
		raw, _ := json.Marshal(map[string]any{field: value})
		return Check(Input{Mode: mode, ToolName: tool, Args: raw, WorkDir: workDir})
	}
	got := run(harness.ModeAutoAcceptEdits, "write", "path", filepath.Join(proj, "a.go"), proj)
	if got.Action != Allow || got.Request.Sensitive != "" {
		t.Fatalf("副本内写入 = %+v", got)
	}
	cfg := filepath.Join(home, ".mcode", "config.json")
	if got := run(harness.ModeFullAccess, "write", "path", cfg, proj); got.Request.Sensitive != "controlPlane" {
		t.Fatalf("写 config 敏感标记 = %q", got.Request.Sensitive)
	}
	if got := run(harness.ModeAuto, "bash", "command", "cat > "+filepath.Join(proj, "a.go"), proj); got.Request.Sensitive != "" {
		t.Fatalf("重定向命令敏感标记 = %q", got.Request.Sensitive)
	}
	if got := run(harness.ModeAuto, "bash", "command", "rm "+cfg, proj); got.Request.Sensitive != "controlPlane" {
		t.Fatalf("删除 config 敏感标记 = %q", got.Request.Sensitive)
	}
	if got := run(harness.ModeSupervised, "write", "path", filepath.Join(proj, "package.json"), proj); got.Request.Sensitive != "" {
		t.Fatalf("package.json 敏感标记 = %q", got.Request.Sensitive)
	}
	plain := t.TempDir()
	if got := run(harness.ModeAutoAcceptEdits, "write", "path", filepath.Join(plain, "a.go"), plain); got.Action != Allow || got.Request.Sensitive != "" {
		t.Fatalf("普通目录写入 = %+v", got)
	}
	if got := run(harness.ModeFullAccess, "write", "path", cfg, plain); got.Request.Sensitive != "controlPlane" {
		t.Fatalf("普通目录写控制面敏感标记 = %q", got.Request.Sensitive)
	}
}

func TestSensitiveFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Claude 风格的 read 请求使用 file_path 键，且不带 Sensitive 标记。
	got := SensitiveFor(harness.RequestKindTool, "Read", json.RawMessage(`{"file_path":"~/.ssh/id_rsa"}`), "", "")
	if got != "credential" {
		t.Fatalf("敏感标记 = %q，期望 credential", got)
	}
	// Claude 的写工具同样使用 file_path 键。
	if got := SensitiveFor(harness.RequestKindFileChange, "Write", json.RawMessage(`{"file_path":"~/.ssh/id_rsa"}`), "", ""); got != "credential" {
		t.Fatalf("写工具敏感标记 = %q，期望 credential", got)
	}
	// 命令补算也要能识别凭证读取。
	if got := SensitiveFor(harness.RequestKindCommand, "Bash", json.RawMessage(`{"command":"cat ~/.ssh/id_rsa"}`), "cat ~/.ssh/id_rsa", ""); got != "credential" {
		t.Fatalf("命令敏感标记 = %q，期望 credential", got)
	}
}

func TestOnlyReads(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"ls", true}, {"git status", true}, {"cat a", true}, {"ls > out", false}, {"cat a | tee b", false}, {"rm a", false}, {"git push", false},
		{"find . -delete", false}, {"find . -exec rm {} \\;", false}, {"rg --pre=sh x", false}, {"sort -o out in", false}, {"tree -o out.txt .", false}, {"tree --output=out.txt .", false}, {"tree -L 2 .", true}, {"git branch -D x", false}, {"git diff --output=out", false}, {"git worktree list", true}, {"git branch --list", true}, {"echo 'a; b'", true}, {"echo x 2>/dev/null", true}, {"cat a 2>&1 | head", true}, {"echo $(rm a)", false}, {"echo x & rm a", false},
	} {
		if got := onlyReads(tc.command); got != tc.want {
			t.Errorf("%q 得到 %v，期望 %v", tc.command, got, tc.want)
		}
	}
}
func TestClampBashTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds float64
		want    time.Duration
	}{
		{"j_bash_default_timeout", 0, DefaultBashTimeout},
		{"j_bash_timeout_5000_clamped", 5000, MaxBashTimeout},
		{"j_bash_timeout_60_preserved", 60, 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampBashTimeout(tc.seconds); got != tc.want {
				t.Errorf("%v 得到 %v，期望 %v", tc.seconds, got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		seconds float64
		want    time.Duration
	}{{0, DefaultBashTimeout}, {-1, DefaultBashTimeout}, {math.NaN(), DefaultBashTimeout}, {math.Inf(1), DefaultBashTimeout}, {60, 60 * time.Second}, {0.5, 500 * time.Millisecond}, {1800, MaxBashTimeout}, {5000, MaxBashTimeout}} {
		if got := ClampBashTimeout(tc.seconds); got != tc.want {
			t.Errorf("%v 得到 %v，期望 %v", tc.seconds, got, tc.want)
		}
	}
}
func TestSession(t *testing.T) {
	var s Session
	s.Remember("")
	if s.Allowed("") {
		t.Fatal("空键不可授权")
	}
	s.Remember("one")
	if !s.Allowed("one") || s.Allowed("two") {
		t.Fatal("会话授权范围错误")
	}
}

// TestCheckApplyPatch 覆盖 apply_patch 的多文件敏感判断与模式放行。
func TestCheckApplyPatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	run := func(mode harness.PermissionMode, plan bool, input string) Verdict {
		raw, _ := json.Marshal(map[string]any{"input": input})
		return Check(Input{Mode: mode, Plan: plan, ToolName: "apply_patch", Args: raw, WorkDir: work})
	}
	patchText := "*** Begin Patch\n*** Add File: a.go\n+x\n*** Update File: b.go\n@@\n-x\n+y\n*** End Patch"
	if got := run(harness.ModeAutoAcceptEdits, false, patchText); got.Action != Allow {
		t.Fatalf("AutoAcceptEdits 得到 %+v", got)
	}
	got := run(harness.ModeSupervised, false, patchText)
	if got.Action != Ask || got.Request.Kind != harness.RequestKindFileChange || !strings.Contains(got.Request.Summary, "a.go") || !strings.Contains(got.Request.Summary, "b.go") {
		t.Fatalf("Supervised 得到 %+v", got)
	}
	cfgPatch := "*** Begin Patch\n*** Update File: " + filepath.Join(home, ".mcode", "config.json") + "\n@@\n-x\n+y\n*** End Patch"
	if got := run(harness.ModeSupervised, false, cfgPatch); got.Request.Sensitive != "controlPlane" {
		t.Fatalf("控制面敏感标记 = %q", got.Request.Sensitive)
	}
	envPatch := "*** Begin Patch\n*** Add File: " + "." + "env" + "\n+TOKEN=1\n*** End Patch"
	if got := run(harness.ModeAuto, false, envPatch); got.Request.Sensitive != "credential" {
		t.Fatalf("凭证敏感标记 = %q", got.Request.Sensitive)
	}
	if got := run("", true, patchText); got.Action != Deny {
		t.Fatalf("计划模式得到 %+v", got)
	}
}

// TestCheckWebFetch 覆盖 web_fetch 按只读命令的放行规则。
func TestCheckWebFetch(t *testing.T) {
	work := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"url": "https://example.com", "maxChars": 1000})
	run := func(mode harness.PermissionMode, plan bool) Verdict {
		return Check(Input{Mode: mode, Plan: plan, ToolName: "web_fetch", Args: raw, WorkDir: work})
	}
	for _, mode := range []harness.PermissionMode{harness.ModeAuto, harness.ModeReadOnly} {
		if got := run(mode, false); got.Action != Allow {
			t.Fatalf("%s 得到 %+v", mode, got)
		}
	}
	if got := run("", true); got.Action != Allow {
		t.Fatalf("计划模式得到 %+v", got)
	}
	got := run(harness.ModeSupervised, false)
	if got.Action != Ask || got.Request.Kind != harness.RequestKindTool || got.Request.Summary != "https://example.com" {
		t.Fatalf("Supervised 得到 %+v", got)
	}
}
