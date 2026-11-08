package gate

import (
 "encoding/json"
 "math"
 "path/filepath"
 "testing"
 "time"

 "mcode/internal/harness"
)

func TestCheck(t *testing.T) {
 work:=t.TempDir(); home:=t.TempDir(); t.Setenv("HOME",home); gateFile:=filepath.Join(t.TempDir(),"gate.json")
 type testCase struct { name string; mode harness.PermissionMode; plan, final bool; tool, field, value string; action Action; sensitive string }
 cases:=[]testCase{
 {"env_source_full",harness.ModeFullAccess,false,false,"bash","command","source .env 2>/dev/null; security find-identity",Allow,""},
 {"env_local_full",harness.ModeFullAccess,false,false,"bash","command","cat ./.env.local",Allow,""},
 {"env_read_full",harness.ModeFullAccess,false,false,"read","path",filepath.Join(work,".env"),Allow,""},
 {"env_write_full",harness.ModeFullAccess,false,false,"write","path",".env",Allow,""},
 {"env_ssh_full",harness.ModeFullAccess,false,false,"bash","command","cat ~/.ssh/id_rsa",Ask,"credential"},
 {"env_absolute_outside_full",harness.ModeFullAccess,false,false,"bash","command","cat /tmp/elsewhere/.env",Ask,"credential"},
 {"env_relative_outside_full",harness.ModeFullAccess,false,false,"bash","command","cat ../x/.env",Ask,"credential"},
 {"env_mixed_full",harness.ModeFullAccess,false,false,"bash","command","cat .env ../x/.env",Ask,"credential"},
 {"env_read_outside_full",harness.ModeFullAccess,false,false,"read","path","../x/.env",Ask,"credential"},
 {"env_write_outside_full",harness.ModeFullAccess,false,false,"write","path","../x/.env",Ask,"credential"},
 {"env_source_auto",harness.ModeAuto,false,false,"bash","command","source .env",Ask,"credential"},
 {"env_control_full",harness.ModeFullAccess,false,false,"write","path",gateFile,Ask,"controlPlane"},
 {"a_write",harness.ModeFullAccess,false,false,"write","path","normal.txt",Allow,""},
 {"a_bash",harness.ModeFullAccess,false,false,"bash","command","make build",Allow,""},
 {"b_read_ssh",harness.ModeFullAccess,false,false,"read","path",filepath.Join(home,".ssh/id_rsa"),Ask,"credential"},
 {"c_bash_aws",harness.ModeAuto,false,false,"bash","command","cat ~/.aws/credentials",Ask,"credential"},
 {"c_bash_ls",harness.ModeAuto,false,false,"bash","command","ls -la",Allow,""},
 {"d_read_env",harness.ModeAuto,false,false,"read","path",".env",Ask,"credential"},
 {"d_read_env_example",harness.ModeAuto,false,false,"read","path",".env.example",Allow,""},
 {"e_write_gate_dir",harness.ModeFullAccess,false,false,"write","path",filepath.Join(filepath.Dir(gateFile),"injected.json"),Allow,""},
 {"e_bash_tmpdir_loop",harness.ModeFullAccess,false,false,"bash","command","for p in a b; do printf x; done",Allow,""},
 {"e_write_tmpdir_file",harness.ModeFullAccess,false,false,"write","path",filepath.Join(filepath.Dir(gateFile),"repo/x.txt"),Allow,""},
 {"e_write_gate_file",harness.ModeFullAccess,false,false,"write","path",gateFile,Ask,"controlPlane"},
 {"e_bash_rm_gate_file",harness.ModeFullAccess,false,false,"bash","command","rm "+gateFile,Ask,"controlPlane"},
 {"e_write_agent_models",harness.ModeFullAccess,false,false,"write","path",filepath.Join(home,".pi/agent/models.json"),Ask,"controlPlane"},
 {"f_write_plan","",true,false,"write","path","plan.md",Deny,""},
 {"f_read_plan","",true,false,"read","path","plan.md",Allow,""},
 {"g_read_final_report","",false,true,"read","path","final.txt",Deny,""},
 {"h_edit_supervised",harness.ModeSupervised,false,false,"edit","path","main.go",Ask,""},
 {"i_mcp_list_supervised",harness.ModeSupervised,false,false,"mcode_mcp_list","", "",Allow,""},
 {"j_bash_default_timeout",harness.ModeFullAccess,false,false,"bash","command","echo hello",Allow,""},
 {"j_bash_timeout_clamped",harness.ModeFullAccess,false,false,"bash","command","sleep 10",Allow,""},
 {"j_bash_timeout_preserved",harness.ModeFullAccess,false,false,"bash","command","sleep 1",Allow,""},
 {"s_bash_rg_plan","",true,false,"bash","command","rg -n planBlocksTool internal/",Allow,""},
 {"s_bash_git_status_plan","",true,false,"bash","command","git status",Allow,""},
 {"s_bash_redirect_plan","",true,false,"bash","command","echo x > f",Deny,""},
 {"s_bash_rm_plan","",true,false,"bash","command","rm -rf build",Deny,""},
 {"t_mcp_mcode_plan",harness.ModeFullAccess,true,false,"mcp__mcode__kanban","action","read_board",Allow,""},
 {"t_mcp_mcode_plan_supervised",harness.ModeSupervised,true,false,"mcp__mcode__kanban","action","read_board",Ask,""},
 {"t_mcp_other_plan","",true,false,"mcp__other__run","cmd","ls",Deny,""},
 {"u_grep_plan","",true,false,"grep","pattern","planBlocksTool",Allow,""},
 {"u_edit_plan","",true,false,"edit","path","planlimits.go",Deny,""},
 {"k_ask_user_question_supervised",harness.ModeSupervised,false,false,"ask_user_question","question","Proceed?",Allow,""},
 {"todo_write_supervised",harness.ModeSupervised,false,false,"todo_write","", "",Allow,""},
 {"todo_write_plan",harness.ModeSupervised,true,false,"todo_write","", "",Allow,""},
 {"l_ask_final_report","",false,true,"ask_user_question","question","Final?",Deny,""},
 {"p_vision_absent_read",harness.ModeSupervised,false,false,"read","path","note.txt",Allow,""},
 {"q_vision",harness.ModeSupervised,false,false,"vision","path","shot.png",Allow,""},
 {"r_vision",harness.ModeSupervised,false,false,"vision","path","screenshot.png",Allow,""},
 {"s_vision_credential",harness.ModeFullAccess,false,false,"vision","path",filepath.Join(home,".ssh/id_rsa"),Ask,"credential"},
 {"t_vision_final","",false,true,"vision","path","final.png",Deny,""},
 {"y_ls_control",harness.ModeFullAccess,false,false,"bash","command","ls -l "+filepath.Join(home,".pi/agent/sessions")+" 2>/dev/null | head",Allow,""},
 {"y_du_control",harness.ModeFullAccess,false,false,"bash","command","du -sh "+filepath.Join(home,".pi/agent/sessions"),Allow,""},
 {"y_redirect_control",harness.ModeFullAccess,false,false,"bash","command","echo x > "+filepath.Join(home,".pi/agent/models.json"),Ask,"controlPlane"},
 {"y_auth",harness.ModeFullAccess,false,false,"bash","command","cat "+filepath.Join(home,".pi/agent/auth.json"),Ask,"credential"},
 {"y_auto_devnull",harness.ModeAuto,false,false,"bash","command","ls -la 2>/dev/null",Allow,""},
 {"y_dup_stderr",harness.ModeAuto,false,false,"bash","command","cat a 2>&1 | head",Allow,""},
 {"y_auto_redirect",harness.ModeAuto,false,false,"bash","command","ls > out.txt",Ask,""},
 {"y_plan_devnull","",true,false,"bash","command","rg foo 2>/dev/null",Allow,""},
 {"z_readonly_grep",harness.ModeReadOnly,false,false,"bash","command","grep -n foo bar.txt",Allow,""},
 {"z_readonly_rm",harness.ModeReadOnly,false,false,"bash","command","rm a.txt",Ask,""},
 {"z_supervised_grep",harness.ModeSupervised,false,false,"bash","command","grep -n foo bar.txt",Ask,""},
 {"z_cd_ls",harness.ModeAuto,false,false,"bash","command","cd internal && ls",Allow,""},
 {"z_ls_files",harness.ModeAuto,false,false,"bash","command","git ls-files dist",Allow,""},
 {"z_worktree_list",harness.ModeAuto,false,false,"bash","command","git worktree list",Allow,""},
 {"z_grep_pipe",harness.ModeAuto,false,false,"bash","command",`grep -n "outDir\|webassets" web/vite.config.ts | head -20`,Allow,""},
 {"z_echo_grep",harness.ModeAuto,false,false,"bash","command",`echo "--- scripts:"; grep -n '"scripts"' -A 15 web/package.json`,Allow,""},
 {"z_worktree_add",harness.ModeAuto,false,false,"bash","command","git worktree add ../x",Ask,""},
 {"z_quoted_semicolon",harness.ModeAuto,false,false,"bash","command",`grep "a; rm b" f; rm c`,Ask,""},
 {"z_unterminated",harness.ModeAuto,false,false,"bash","command",`grep "unterminated f`,Ask,""},
 {"z_cd_rm",harness.ModeAuto,false,false,"bash","command","cd x && rm y",Ask,""},
 }
 for _,tc:=range cases { t.Run(tc.name,func(t *testing.T){ args:=map[string]any{}; if tc.field!="" { args[tc.field]=tc.value }; raw,_:=json.Marshal(args); got:=Check(Input{Mode:tc.mode, Plan:tc.plan, FinalReport:tc.final, ToolName:tc.tool, Args:raw, WorkDir:work, ControlPlanePaths:[]string{gateFile}}); if got.Action!=tc.action || got.Request.Sensitive!=tc.sensitive || (got.SessionKey!="")!=(tc.action==Ask && tc.sensitive=="") { t.Errorf("得到 %+v，期望 action=%s sensitive=%s",got,tc.action,tc.sensitive) } }) }
}

func TestOnlyReads(t *testing.T) {
 for _,tc:=range []struct{command string; want bool}{
 {"ls",true},{"git status",true},{"cat a",true},{"ls > out",false},{"cat a | tee b",false},{"rm a",false},{"git push",false},
 {"find . -delete",false},{"find . -exec rm {} \\;",false},{"rg --pre=sh x",false},{"sort -o out in",false},{"git branch -D x",false},{"git diff --output=out",false},{"git worktree list",true},{"git branch --list",true},{"echo 'a; b'",true},{"echo x 2>/dev/null",true},{"cat a 2>&1 | head",true},{"echo $(rm a)",false},{"echo x & rm a",false},
 } { if got:=onlyReads(tc.command); got!=tc.want { t.Errorf("%q 得到 %v，期望 %v",tc.command,got,tc.want) } }
}
func TestClampBashTimeout(t *testing.T) { for _,tc:=range []struct{seconds float64; want time.Duration}{{0,DefaultBashTimeout},{-1,DefaultBashTimeout},{math.NaN(),DefaultBashTimeout},{math.Inf(1),DefaultBashTimeout},{60,60*time.Second},{0.5,500*time.Millisecond},{1800,MaxBashTimeout},{5000,MaxBashTimeout}} { if got:=ClampBashTimeout(tc.seconds); got!=tc.want { t.Errorf("%v 得到 %v，期望 %v",tc.seconds,got,tc.want) } } }
func TestSession(t *testing.T) { var s Session; s.Remember(""); if s.Allowed("") { t.Fatal("空键不可授权") }; s.Remember("one"); if !s.Allowed("one") || s.Allowed("two") { t.Fatal("会话授权范围错误") } }
