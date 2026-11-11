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

func TestSandboxedReadOnly(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	command := `strings -n 20 ` + filepath.Join(home, ".mcode/mcode.db") + ` | awk -F'x' '{print $2}'`
	for _, tc := range []struct {
		command         string
		mode            harness.PermissionMode
		plan, sandboxed bool
		want            Action
	}{
		{`python3 -c 'open("x","w")'`, harness.ModeReadOnly, false, true, Allow},
		{"sed -i s/a/b/ f", harness.ModeReadOnly, false, true, Allow},
		{"make -n", harness.ModeReadOnly, false, true, Allow},
		{"go test ./...", harness.ModeReadOnly, false, true, Allow},
		{"git push", harness.ModeReadOnly, false, true, Deny},
		{"gh pr create", harness.ModeReadOnly, false, true, Deny},
		{"cat ~/.ssh/id_rsa", harness.ModeReadOnly, false, true, Ask},
		{command, harness.ModeReadOnly, false, true, Allow},
		{command, harness.ModeReadOnly, false, false, Deny},
		{`python3 -c 'open("x","w")'`, harness.ModeReadOnly, false, false, Deny},
		{"sed -i s/a/b/ f", harness.ModeReadOnly, false, false, Deny},
		{"python3 -c 'print(1)'", harness.ModeAuto, true, true, Allow},
		{"git push", harness.ModeAuto, true, true, Deny},
	} {
		t.Run(tc.command, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"command": tc.command})
			if got := Check(Input{Mode: tc.mode, Plan: tc.plan, Sandboxed: tc.sandboxed, ToolName: "bash", Args: args}); got.Action != tc.want {
				t.Errorf("得到 %+v，期望 %s", got, tc.want)
			}
		})
	}
	args, _ := json.Marshal(map[string]string{"command": `awk '{print}' ` + filepath.Join(home, ".mcode/mcode.db")})
	if got := Check(Input{Mode: harness.ModeAuto, ToolName: "bash", Args: args}); got.Action != Ask || got.Request.Sensitive != "controlPlane" {
		t.Errorf("得到 %+v，期望 controlPlane", got)
	}
}

func TestMagpieSearchPermissions(t *testing.T) {
	for _, mode := range []harness.PermissionMode{harness.ModeAuto, harness.ModeAutoAcceptEdits, harness.ModeReadOnly} {
		for _, name := range []string{"mcp__magpie_search__web_search", "mcp__magpie_search__web_fetch"} {
			if got := Check(Input{Mode: mode, ToolName: name}).Action; got != Allow {
				t.Errorf("%s %s: %s", mode, name, got)
			}
		}
	}
	for _, name := range []string{"mcp__other__web_search", "mcp__magpie_search__other", "mcode_mcp_call"} {
		if got := Check(Input{Mode: harness.ModeAuto, ToolName: name, ReadOnlyHint: true}).Action; got != Ask {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := Check(Input{Mode: harness.ModeAuto, ToolName: "mcp__magpie_search__web_search", FinalReport: true}).Action; got != Deny {
		t.Errorf("final report: %s", got)
	}
}

func TestIsLookOnlyNamedTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"list_files", true},
		{"search_text", true},
		{"write", false},
		{"edit", false},
		{"bash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLookOnly(tc.name); got != tc.want {
				t.Errorf("IsLookOnly(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

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
		{"k_recall_supervised", harness.ModeSupervised, false, false, "recall", "query", "gate", Allow, ""},
		{"f_recall_plan", "", true, false, "recall", "query", "gate", Allow, ""},
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
		{"z_bash_readonly_case_insensitive_rm_deny", harness.ModeReadOnly, false, false, "bash", "Command", "rm -rf build", Deny, ""},
		{"z_bash_plan_case_insensitive_rm_deny", "", true, false, "bash", "Command", "rm -rf build", Deny, ""},
		{"z_bash_auto_tree_output_ask", harness.ModeAuto, false, false, "bash", "command", "tree -o out.txt internal", Ask, ""},
		{"z_bash_auto_tree_long_output_ask", harness.ModeAuto, false, false, "bash", "command", "tree --output=out.txt internal", Ask, ""},
		{"z_write_mcode_always_allow", harness.ModeFullAccess, false, false, "write", "path", filepath.Join(home, ".mcode/always_allow.json"), Ask, "controlPlane"},
		{"z_bash_readonly_rm_deny", harness.ModeReadOnly, false, false, "bash", "command", "rm a.txt", Deny, ""},
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
		{"uniq a b", false}, {"sort --compress-program=sh x", false}, {"sort --compress-program sh x", false}, {"sort -T /tmp x", false}, {"sort -nT /tmp x", false}, {"sort --temporary-directory=/tmp x", false}, {"sort file", true}, {"grep x file", true},
		{"find . -delete", false}, {"find . -exec rm {} \\;", false}, {"rg --pre=sh x", false}, {"sort -o out in", false}, {"tree -o out.txt .", false}, {"tree --output=out.txt .", false}, {"tree -L 2 .", true}, {"git branch -D x", false}, {"git diff --output=out", false}, {"git worktree list", true}, {"git branch --list", true}, {"echo 'a; b'", true}, {"echo x 2>/dev/null", true}, {"cat a 2>&1 | head", true}, {"echo $(rm a)", false}, {"echo x & rm a", false},
		{"curl -sL -m 30 https://raw.githubusercontent.com/x/y/main/a.java | nl -ba", true},
		{"git describe --tags 2>/dev/null; git tag | tail -20", true},
		{"git -C /tmp/x log --oneline -3", true}, {"git -C /tmp -C x status", true}, {"git -C", false},
		{"git rev-parse HEAD", true}, {"git tag -l 'v1*'", true}, {"git tag --sort=-creatordate | head", true},
		{"git tag --contains HEAD", true}, {"git tag --points-at HEAD", true}, {"git tag -n10", true},
		{"git remote -v", true}, {"git remote get-url origin", true}, {"git remote show origin", true},
		{"git config --get remote.origin.url", true}, {"git reflog", true}, {"git reflog show HEAD", true},
		{"git ls-remote origin", true}, {"git grep -n foo", true}, {"curl -H 'Accept: */*' https://example.com", true},
		{"git log --oneline -3", true}, {"git log --stat", true}, {"git ls-remote --tags origin", true}, {"git ls-remote -q origin", true}, {"git diff --exit-code", true},
		{"curl -fsSL https://example.com/a", true}, {"curl -s -m 30 -H 'Accept: application/json' https://api.github.com/repos/a/b", true}, {"curl -I https://example.com", true},
		{"curl -sLm30 --request=GET https://x", true}, {"curl --max-time 30 --write-out '%{http_code}' https://x", true},
		{"git tag v1", false}, {"git tag -d v1", false}, {"git tag -ld v1", false}, {"git tag --list -f v1", false},
		{"git remote add o url", false}, {"git config user.name x", false}, {"git config --unset a.b", false}, {"git config --get --add a.b x", false},
		{"git reflog expire --all", false}, {"git -c core.pager=x log", false}, {"git -C /tmp log --ext-diff", false},
		{"curl -o out https://x", false}, {"curl -sLo out https://x", false}, {"curl -O https://x/a", false}, {"curl -d a=b https://x", false},
		{"curl -X POST https://x", false}, {"curl -T f https://x", false}, {"curl -K cfg", false}, {"curl file:///etc/passwd", false}, {"curl https://x > out", false},
		{"curl -w @file https://x", false}, {"curl --write-out=%output{out} https://x", false}, {"curl --request=POST https://x", false}, {"curl -m", false},
		{"git tag --contains HEAD -d v1", false}, {"git config --list --edit", false}, {"git reflog delete HEAD@{0}", false},
		{"git ls-remote --upload-pack=touch /tmp/x origin", false}, {"git ls-remote --upload-pack evil origin", false}, {"git ls-remote -u evil origin", false},
		{"git ls-remote --upload-pa=touch origin", false}, {"git ls-remote -uevil origin", false}, {"git ls-remote -qu origin", false},
		{"git log --outp=x", false}, {"git log --ext", false}, {"git diff --ext-d", false}, {"git grep --open-files=vim foo", false}, {"git log --exec-p=/tmp", false},
		{"git grep -O foo", false}, {"git grep -nOless foo", false}, {"git grep -iOless foo", false}, {"git grep --open-files-in-pager foo", false}, {"git grep --open-files-in-pager=vim foo", false},
		{"curl -H @headers.txt https://example.com", false}, {"curl --header=@h https://example.com", false}, {"curl -H@h https://example.com", false},
		{`cat C:\Users\a\b.txt`, true}, {`Get-Content C:\x\y.txt`, true},
		{"get-childitem -Recurse -Filter *.go", true}, {"Select-String -Path a.txt -Pattern foo", true},
		{"git.exe status", true}, {"where.exe go", true}, {"ls 2>$null", true}, {"dir > NUL", true},
		{"tasklist", true}, {"ipconfig /all", true}, {`reg query HKCU\Software\x`, true},
		{"go list -m -json all", true}, {"go list -f '{{.Dir}}' ./...", true}, {"go mod why -m golang.org/x/sys", true},
		{"gh pr view 1", true}, {"gh api repos/a/b", true}, {"adb logcat -d -t 100", true},
		{"adb shell getprop ro.product.model", true}, {"jq '{a}' x.json", true}, {"tac a", true}, {"base64 a", true},
		{"ls (Remove-Item x)", false}, {"Get-ChildItem | Where-Object { Remove-Item x }", false},
		{"Get-Process | Stop-Process", false}, {"sc a.txt x", false}, {"Set-Content a x", false}, {"iwr https://x", false},
		{`echo \"; rm x; echo \"`, false}, {`cat a\;rm x`, false}, {"ipconfig /release", false}, {`reg add HKCU\x`, false},
		{`echo "a\"; Remove-Item x; echo "`, false}, {`grep "x\"" a`, false},
		{`grep "a\.b" f`, true}, {`echo "a\\b"`, true},
		{"go list -toolexec x ./...", false}, {"go list -mod=mod ./...", false}, {"gh pr create", false},
		{"adb pull /sdcard/a.txt", false}, {"adb shell rm /sdcard/x", false}, {"fd -x rm", false}, {"fd -Hx rm", false},
		{"base64 -o out a", false}, {"git show HEAD@{1}", false},
		{`cat \\server\share\a.txt`, true}, {`cat C:\x\a-b_1.txt`, true}, {`rg 'a{2}' .`, true},
		{`cat a\`, false}, {`cat a\ b`, false}, {`cat a\|rm x`, false}, {`cat a\$x`, false},
		{`cat a\<b`, false}, {`cat a\>b`, false}, {`cat a\&rm x`, false}, {"cat a\\\nrm x", false},
		{"ls > $NULL", true}, {"dir 2>>nul", true}, {"dir > NUL.txt", false}, {"ls > $nullx", false},
		{"GET-CONTENT.EXE a.txt", true}, {"ipconfig", true}, {"ipconfig -ALL", true}, {"ipconfig /DISPLAYDNS", true},
		{"ipconfig /all /release", false}, {"ipconfig /flushdns", false}, {"reg QUERY HKCU", true}, {"reg", false},
		{"go mod graph", true}, {"go mod graph -modfile=x", false}, {"go mod why -vendor x", true}, {"go mod why -mod=mod x", false},
		{"go mod download", false}, {"go list -json=Name -f='{{.Dir}}' -e -deps -test -u -versions -find -compiled -retracted ./...", true},
		{"go list -f", false}, {"go list -modfile=x ./...", false}, {"go list -exec=x ./...", false}, {"go list -overlay=x ./...", false},
		{"fd --exec=rm", false}, {"fd --exec-batch=rm", false}, {"fdfind -X=rm", false}, {"fd --exec rm", false},
		{"fdfind --exec-batch rm", false}, {"fd -HX rm", false}, {"fd -H x", true}, {"fdfind x", true},
		{"base64 --output=out a", false}, {"base64 --output out a", false}, {"base64 -d a", true},
		{"adb -s serial pull /sdcard/a", false}, {"adb -d -s serial shell ls", true}, {"adb -H host -P 5037 -L tcp:5037 devices", true},
		{"adb -s", false}, {"adb -s serial", false}, {"adb logcat -c", false}, {"gh api repos/a/b -X POST", false},
	} {
		if got := OnlyReads(tc.command); got != tc.want {
			t.Errorf("%q 得到 %v，期望 %v", tc.command, got, tc.want)
		}
	}
	for _, sub := range strings.Fields("describe rev-parse rev-list shortlog cat-file ls-tree ls-remote show-ref name-rev merge-base for-each-ref count-objects grep whatchanged") {
		if !OnlyReads("git "+sub) || OnlyReads("git "+sub+" --ext-diff") || OnlyReads("git "+sub+" --output=x") {
			t.Errorf("git %s 参数检查失败", sub)
		}
	}
}

func TestReadOnlyPrompt(t *testing.T) {
	plain, sandboxed := ReadOnlyPrompt(false), ReadOnlyPrompt(true)
	t.Log(plain)
	if plain == "" || sandboxed == "" {
		t.Fatal("只读提示不能为空")
	}
	for _, name := range []string{"rg", "git log", "Get-Content"} {
		if !strings.Contains(plain, name) {
			t.Errorf("白名单提示缺少 %q", name)
		}
	}
	if !strings.Contains(sandboxed, "$TMPDIR") || strings.Contains(sandboxed, "rg") {
		t.Fatalf("沙箱提示错误：%q", sandboxed)
	}
	for _, name := range []string{"gci", "gc", "gi", "gp", "gl", "gcm", "gps", "gsv", "sls", "select", "measure", "group", "ft", "fl"} {
		if strings.Contains(" "+plain+" ", " "+name+" ") {
			t.Errorf("提示不应列出 PowerShell 别名 %q", name)
		}
	}
}

func TestReadOnlyBashCommands(t *testing.T) {
	allowed := []string{"whoami", "uname -a", "sed -n '1,20p' a.go", "go doc fmt", "go env GOPATH", "adb devices", "adb shell ls", "command -v adb", "env", "cd x && whoami", "jq '.' file", "uniq -c file", "xxd file", "date -u", "hostname"}
	denied := []string{"xargs ls", "awk '{print}' f", "sed -i 's/a/b/' f", "sed 's/a/b/w out' f", "go build ./...", "go test ./...", "adb shell rm x", "adb install x.apk", "env FOO=1 rm x", "defaults write a b c", "command rm x", "which adb | xargs -I{} {} version", "go env -w GOPATH=/tmp", "go list -toolexec=sh ./...", "uniq a b", "xxd -r - out", "hostname newname", "date 010101012025", "git log --exec-path=/tmp", "git diff --out'put'=out", "find . '-exec' rm x +"}
	allowed = append(allowed, `rg -e "->" internal`, `rg -- "->" .`, `git log --grep "-x"`, `grep -n "<div" a.html`, `rg "=>" .`, `rg 'a<b>c' .`, `rg "foo$" .`, `rg "a$|b$" .`, `rg 'foo$HOME' .`, `rg "a$)b" .`, `rg "a$ b" .`, `rg "a$\b" .`)
	denied = append(denied, `rg '--pre=x' foo`, `rg "--pre" x foo`, `find . -ex''ec rm {} ';'`, `find . "-delete"`, `sort '-o' out a`, `git log '-cfoo=bar'`, `git "diff" '--output=x'`, `tree '-o' x`, `cat a > b`, `cat a >b`, `cat < a`, `echo ">" > f`, `echo "$HOME"`, `find . "${X:=-delete}"`, `rg "$(id)" .`, "rg \"`id`\" .", `echo $x`, `echo "$1"`, `echo "$@"`, `echo "$*"`, `echo "$#"`, `echo "$?"`, `echo "$$"`, `echo "$!"`, `echo "$-"`, `rg "--pre=sh" x`)
	for _, group := range []struct {
		commands []string
		want     Action
	}{{allowed, Allow}, {denied, Deny}} {
		for _, command := range group.commands {
			t.Run(command, func(t *testing.T) {
				args, _ := json.Marshal(map[string]string{"command": command})
				got := Check(Input{Mode: harness.ModeReadOnly, ToolName: "bash", Args: args, WorkDir: t.TempDir()})
				if got.Action != group.want {
					t.Fatalf("%q: %+v, want %s", command, got, group.want)
				}
				if group.want == Deny && (!strings.Contains(got.Reason, "只读档只能运行只读命令") || !strings.Contains(got.Reason, "不要换写法重试")) {
					t.Errorf("缺少引导: %s", got.Reason)
				}
			})
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

func TestReadOnlyGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	for _, tc := range []struct {
		name string
		mode harness.PermissionMode
		tool string
		args string
		hint bool
		want Action
	}{
		{"write", harness.ModeReadOnly, "write", `{"path":"a"}`, false, Deny},
		{"edit", harness.ModeReadOnly, "edit", `{"path":"a"}`, false, Deny},
		{"patch", harness.ModeReadOnly, "apply_patch", `{"input":""}`, false, Deny},
		{"rm", harness.ModeReadOnly, "bash", `{"command":"rm -rf x"}`, false, Deny},
		{"redirect", harness.ModeReadOnly, "bash", `{"command":"echo a > f"}`, false, Deny},
		{"unknown mcp", harness.ModeReadOnly, "mcp__other__tool", `{}`, false, Deny},
		{"read", harness.ModeReadOnly, "read", `{"path":"a"}`, false, Allow},
		{"list", harness.ModeReadOnly, "list_files", `{}`, false, Allow},
		{"search", harness.ModeReadOnly, "search_text", `{}`, false, Allow},
		{"ls", harness.ModeReadOnly, "bash", `{"command":"ls"}`, false, Allow},
		{"git status", harness.ModeReadOnly, "bash", `{"command":"git status"}`, false, Allow},
		{"fetch", harness.ModeReadOnly, "web_fetch", `{"url":"https://example.com"}`, false, Allow},
		{"readonly mcp", harness.ModeReadOnly, "mcp__other__tool", `{}`, true, Allow},
		{"sensitive read", harness.ModeReadOnly, "read", `{"path":".env"}`, false, Ask},
		{"sensitive readonly mcp", harness.ModeReadOnly, "mcp__other__tool", `{"path":".env"}`, true, Ask},
		{"auto write", harness.ModeAuto, "write", `{"path":"a"}`, false, Allow},
		{"auto bash write", harness.ModeAuto, "bash", `{"command":"rm -rf x"}`, false, Ask},
		{"auto mcp hint", harness.ModeAuto, "mcp__other__tool", `{}`, true, Ask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(Input{Mode: tc.mode, ToolName: tc.tool, Args: json.RawMessage(tc.args), ReadOnlyHint: tc.hint, WorkDir: work})
			if got.Action != tc.want {
				t.Fatalf("得到 %+v，期望 %s", got, tc.want)
			}
		})
	}
}

func TestReadOnlyRejectsRememberedWrite(t *testing.T) {
	in := Input{Mode: harness.ModeSupervised, ToolName: "write", Args: json.RawMessage(`{"path":"a"}`), WorkDir: t.TempDir()}
	verdict := Check(in)
	if verdict.Action != Ask || verdict.SessionKey == "" {
		t.Fatalf("写调用应先请求授权：%+v", verdict)
	}
	var session Session
	session.Remember(verdict.SessionKey)
	if !session.Allowed(verdict.SessionKey) {
		t.Fatal("会话授权未记住")
	}
	in.Mode = harness.ModeReadOnly
	if got := Check(in); got.Action != Deny {
		t.Fatalf("只读模式应拒绝已授权的写调用：%+v", got)
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

// TestCheckHardLock 覆盖看板任务执行会话的危险命令硬锁。
func TestCheckHardLock(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "gh pr merge 12"})
	run := func(locked bool, override string) Verdict {
		return Check(Input{
			Mode:             harness.ModeFullAccess,
			ToolName:         "bash",
			Args:             args,
			HardLocked:       locked,
			HardLockOverride: override,
		})
	}

	got := run(true, "")
	if got.Action != Deny {
		t.Fatalf("HardLocked 时 gh pr merge 应被拒绝，实际 %+v", got)
	}
	if !strings.Contains(got.Reason, "危险操作已锁定，需要用户答决策卡后解锁") {
		t.Fatalf("拒绝理由不匹配: %q", got.Reason)
	}

	if got := run(false, ""); got.Action != Allow {
		t.Fatalf("未锁定时行为应保持不变，实际 %+v", got)
	}
	if got := run(true, "D-42"); got.Action != Allow {
		t.Fatalf("带 override 时应放行，实际 %+v", got)
	}
}
