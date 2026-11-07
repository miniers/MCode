package pi

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type gateTestCase struct {
	ID       string            `json:"id"`
	Gate     map[string]any    `json:"gate,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	ToolName string            `json:"toolName"`
	Input    map[string]any    `json:"input"`
	Execute  bool              `json:"execute,omitempty"`
	UIAnswer any               `json:"uiAnswer,omitempty"`

	// VisionArgs 非空时 driver 不走门禁，只用这里的 env 算一遍 vision 子进程参数。
	VisionArgs *gateVisionArgsCase `json:"visionArgs,omitempty"`
}

// gateVisionArgsCase 是 vision 参数用例的入参：环境变量由用例自己给，不依赖宿主机。
type gateVisionArgsCase struct {
	Env       map[string]string `json:"env,omitempty"`
	Model     string            `json:"model,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	ImagePath string            `json:"imagePath,omitempty"`
}

type gateTestResult struct {
	ID              string         `json:"id"`
	Result          string         `json:"result"`
	Sensitive       string         `json:"sensitive"`
	Options         []string       `json:"options"`
	Input           map[string]any `json:"input"`
	RegisteredTools []string       `json:"registeredTools,omitempty"`
	UIMethod        string         `json:"uiMethod,omitempty"`
	ExecuteResult   map[string]any `json:"executeResult,omitempty"`
	VisionArgs      []string       `json:"visionArgs,omitempty"`
}

func findGatePaths(t *testing.T) (gatePath string, driverPath string) {
	t.Helper()

	gatePath = "mcode-gate.ts"
	if _, err := os.Stat(gatePath); os.IsNotExist(err) {
		gatePath = filepath.Join("internal", "providers", "pi", "mcode-gate.ts")
	}
	absGate, err := filepath.Abs(gatePath)
	if err != nil {
		t.Fatalf("无法获取 mcode-gate.ts 绝对路径: %v", err)
	}

	driverPath = filepath.Join("testdata", "gate_driver.mjs")
	if _, err := os.Stat(driverPath); os.IsNotExist(err) {
		driverPath = filepath.Join("internal", "providers", "pi", "testdata", "gate_driver.mjs")
	}
	absDriver, err := filepath.Abs(driverPath)
	if err != nil {
		t.Fatalf("无法获取 gate_driver.mjs 绝对路径: %v", err)
	}

	return absGate, absDriver
}

func runGateDriver(t *testing.T, nodePath, driverPath, gatePath, homeDir, gateFile string, cases []gateTestCase) (map[string]gateTestResult, []string) {
	t.Helper()

	inputBytes, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("序列化测试用例失败: %v", err)
	}

	cmd := exec.Command(nodePath, driverPath, gatePath)
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "MCODE_PI_GATE=") || strings.HasPrefix(e, "MCODE_PI_VISION_MODEL=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env, "HOME="+homeDir, "MCODE_PI_GATE="+gateFile)
	cmd.Stdin = bytes.NewReader(inputBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("gate_driver 运行失败: %v, stderr: %s", err, stderr.String())
	}

	var output struct {
		RegisteredTools []string         `json:"registeredTools"`
		Results         []gateTestResult `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		var rawResults []gateTestResult
		if err2 := json.Unmarshal(stdout.Bytes(), &rawResults); err2 == nil {
			output.Results = rawResults
		} else {
			t.Fatalf("解析 gate_driver 输出失败: %v, 原始输出: %s", err, stdout.String())
		}
	}

	byID := make(map[string]gateTestResult, len(output.Results))
	for _, r := range output.Results {
		byID[r.ID] = r
	}
	return byID, output.RegisteredTools
}

func TestGateTS_Behavior(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 未找到，跳过门禁 TS 行为测试")
	}

	gatePath, driverPath := findGatePaths(t)

	homeDir := t.TempDir()
	gateDir := t.TempDir()
	gateFile := filepath.Join(gateDir, "gate.json")
	if err := os.WriteFile(gateFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("初始化 gate 文件失败: %v", err)
	}
	workDir := t.TempDir()
	projectDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("无法获取项目目录: %v", err)
	}
	// vision 参数用例要用一个真实存在的桥文件：只有文件存在才会加 -e。
	bridgeFile := filepath.Join(t.TempDir(), "mcode-magpie-bridge.ts")
	if err := os.WriteFile(bridgeFile, []byte("// magpie bridge\n"), 0o644); err != nil {
		t.Fatalf("初始化 Magpie 桥文件失败: %v", err)
	}

	cases := []gateTestCase{
		// fullAccess 只放行项目内的 .env，其他凭证和控制面写入仍须询问。
		{ID: "env_source_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cd " + projectDir + " && source .env 2>/dev/null; security find-identity -v -p codesigning"}},
		{ID: "env_local_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cat ./.env.local"}},
		{ID: "env_read_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "read", Input: map[string]any{"path": filepath.Join(projectDir, ".env")}},
		{ID: "env_write_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "write", Input: map[string]any{"path": ".env", "content": "x"}},
		{ID: "env_ssh_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cat ~/.ssh/id_rsa"}},
		{ID: "env_absolute_outside_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cat /tmp/elsewhere/.env"}},
		{ID: "env_relative_outside_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cat ../x/.env"}},
		{ID: "env_mixed_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "bash", Input: map[string]any{"command": "cat .env ../x/.env"}},
		{ID: "env_read_outside_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "read", Input: map[string]any{"path": "../x/.env"}},
		{ID: "env_write_outside_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "write", Input: map[string]any{"path": "../x/.env", "content": "x"}},
		{ID: "env_source_auto", Gate: map[string]any{"runtimeMode": "auto"}, ToolName: "bash", Input: map[string]any{"command": "source .env"}},
		{ID: "env_control_full", Gate: map[string]any{"runtimeMode": "fullAccess"}, ToolName: "write", Input: map[string]any{"path": gateFile, "content": "{}"}},

		// a. 在 fullAccess 下，write 一个普通路径，以及 bash 执行 make build，都直接放行。
		{
			ID:       "a_write",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "write",
			Input:    map[string]any{"path": filepath.Join(workDir, "normal.txt"), "content": "hello"},
		},
		{
			ID:       "a_bash",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "make build"},
		},

		// b. 在 fullAccess 下，read 路径 HOME/.ssh/id_rsa 需要询问，sensitive 为 credential，options 里没有 Allow for session。
		{
			ID:       "b_read_ssh",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(homeDir, ".ssh", "id_rsa")},
		},

		// c. 在 auto 下，bash 执行 cat ~/.aws/credentials 需要询问；bash 执行 ls -la 直接放行。
		{
			ID:       "c_bash_aws",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "cat ~/.aws/credentials"},
		},
		{
			ID:       "c_bash_ls",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "ls -la"},
		},

		// d. 在 auto 下，read .env 需要询问；read .env.example 直接放行。
		{
			ID:       "d_read_env",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(workDir, ".env")},
		},
		{
			ID:       "d_read_env_example",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(workDir, ".env.example")},
		},

		// e. 门禁文件所在目录（系统临时目录）不是控制面：fullAccess 下在它下面写文件和跑带子 shell 的命令都直接放行；
		//    但门禁文件本身仍是控制面，写它或用 bash 删它都要询问；write 到 HOME/.pi/agent/models.json 同样需要询问，sensitive 为 controlPlane。
		{
			ID:       "e_write_gate_dir",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "write",
			Input:    map[string]any{"path": filepath.Join(gateDir, "injected.json"), "content": "{}"},
		},
		{
			ID:       "e_bash_tmpdir_loop",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input: map[string]any{
				"command": "cd " + filepath.Join(gateDir, "repo") + " && for p in a b; do printf x; done",
			},
		},
		{
			ID:       "e_write_tmpdir_file",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "write",
			Input:    map[string]any{"path": filepath.Join(gateDir, "repo", "x.txt"), "content": "x"},
		},
		{
			ID:       "e_write_gate_file",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "write",
			Input:    map[string]any{"path": gateFile, "content": "{}"},
		},
		{
			ID:       "e_bash_rm_gate_file",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "rm " + gateFile},
		},
		{
			ID:       "e_write_agent_models",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "write",
			Input:    map[string]any{"path": filepath.Join(homeDir, ".pi", "agent", "models.json"), "content": "{}"},
		},

		// f. interactionMode 为 plan 时，write 一个普通路径被拦截；read 一个普通路径直接放行。
		{
			ID:       "f_write_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "write",
			Input:    map[string]any{"path": filepath.Join(workDir, "plan.md"), "content": "plan content"},
		},
		{
			ID:       "f_read_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(workDir, "plan.md")},
		},

		// g. finalReport 为 true 时，read 被拦截。
		{
			ID:       "g_read_final_report",
			Gate:     map[string]any{"finalReport": true},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(workDir, "final.txt")},
		},

		// h. 在 supervised 下，edit 一个普通路径需要询问，sensitive 为空。
		{
			ID:       "h_edit_supervised",
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "edit",
			Input:    map[string]any{"path": filepath.Join(workDir, "main.go")},
		},

		// i. 在 supervised 下，mcode_mcp_list 列举工具直接放行。
		{
			ID:       "i_mcp_list_supervised",
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "mcode_mcp_list",
			Input:    map[string]any{},
		},

		// j. bash 默认超时与上限测试
		{
			ID:       "j_bash_default_timeout",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "echo hello"},
		},
		{
			ID:       "j_bash_timeout_5000_clamped",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "sleep 10", "timeout": 5000},
		},
		{
			ID:       "j_bash_timeout_60_preserved",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "sleep 1", "timeout": 60},
		},

		// s. plan 模式下只读 shell 命令放行，改文件或改状态的命令仍被拦截。
		{
			ID:       "s_bash_rg_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "bash",
			Input:    map[string]any{"command": "rg -n planBlocksTool internal/"},
		},
		{
			ID:       "s_bash_git_status_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "bash",
			Input:    map[string]any{"command": "git status"},
		},
		{
			ID:       "s_bash_redirect_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "bash",
			Input:    map[string]any{"command": "echo x > f"},
		},
		{
			ID:       "s_bash_rm_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "bash",
			Input:    map[string]any{"command": "rm -rf build"},
		},

		// t. plan 模式下 mcode 自带 MCP 工具走常规判定：fullAccess 放行，其他档询问；别人家的 MCP 工具仍被拦截。
		{
			ID:       "t_mcp_mcode_plan",
			Gate:     map[string]any{"interactionMode": "plan", "runtimeMode": "fullAccess"},
			ToolName: "mcp__mcode__kanban",
			Input:    map[string]any{"action": "read_board"},
		},
		{
			ID:       "t_mcp_mcode_plan_supervised",
			Gate:     map[string]any{"interactionMode": "plan", "runtimeMode": "supervised"},
			ToolName: "mcp__mcode__kanban",
			Input:    map[string]any{"action": "read_board"},
		},
		{
			ID:       "t_mcp_other_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "mcp__other__run",
			Input:    map[string]any{"cmd": "ls"},
		},

		// u. plan 模式下 grep 放行、edit 拦截。
		{
			ID:       "u_grep_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "grep",
			Input:    map[string]any{"pattern": "planBlocksTool"},
		},
		{
			ID:       "u_edit_plan",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "edit",
			Input:    map[string]any{"path": filepath.Join(workDir, "planlimits.go")},
		},

		// k. supervised 模式下调用 ask_user_question 直接放行
		{
			ID:       "k_ask_user_question_supervised",
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "ask_user_question",
			Input:    map[string]any{"question": "Proceed with deployment?"},
		},

		// l. finalReport 为 true 时 ask_user_question 仍被拦截
		{
			ID:       "l_ask_user_question_final_report",
			Gate:     map[string]any{"finalReport": true},
			ToolName: "ask_user_question",
			Input:    map[string]any{"question": "Final question?"},
		},

		// m. ask_user_question execute 执行：有 options 时走 select
		{
			ID:       "m_ask_user_question_exec_select",
			Execute:  true,
			ToolName: "ask_user_question",
			Input:    map[string]any{"question": "Choose an option", "options": []any{"Option A", "Option B"}},
			UIAnswer: "Option A",
		},

		// n. ask_user_question execute 执行：无 options 时走 input
		{
			ID:       "n_ask_user_question_exec_input",
			Execute:  true,
			ToolName: "ask_user_question",
			Input:    map[string]any{"question": "Enter your name"},
			UIAnswer: "Alice",
		},

		// o. ask_user_question execute 执行：回答为空时回退为默认提示语
		{
			ID:       "o_ask_user_question_exec_empty",
			Execute:  true,
			ToolName: "ask_user_question",
			Input:    map[string]any{"question": "Any feedback?"},
			UIAnswer: "",
		},

		// p. 没设置 MCODE_PI_VISION_MODEL 时不注册 vision 工具。
		{
			ID:       "p_vision_absent_without_model",
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "read",
			Input:    map[string]any{"path": filepath.Join(workDir, "note.txt")},
		},

		// q. 设置 MCODE_PI_VISION_MODEL 后注册 vision 工具。
		{
			ID:       "q_vision_registered_with_model",
			Env:      map[string]string{"MCODE_PI_VISION_MODEL": "openai/gpt-4o"},
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "vision",
			Input:    map[string]any{"path": filepath.Join(workDir, "shot.png")},
		},

		// r. supervised 下 vision 读普通图片路径与 read 一样直接放行。
		{
			ID:       "r_vision_supervised_allow",
			Env:      map[string]string{"MCODE_PI_VISION_MODEL": "openai/gpt-4o"},
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "vision",
			Input:    map[string]any{"path": filepath.Join(workDir, "screenshot.png")},
		},

		// s. fullAccess 下 vision 的 path 是凭证文件时需要询问，sensitive 为 credential。
		{
			ID:       "s_vision_credential_ask",
			Env:      map[string]string{"MCODE_PI_VISION_MODEL": "openai/gpt-4o"},
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "vision",
			Input:    map[string]any{"path": filepath.Join(homeDir, ".ssh", "id_rsa")},
		},

		// t. finalReport 为 true 时 vision 仍被拦截。
		{
			ID:       "t_vision_final_report_block",
			Env:      map[string]string{"MCODE_PI_VISION_MODEL": "openai/gpt-4o"},
			Gate:     map[string]any{"finalReport": true},
			ToolName: "vision",
			Input:    map[string]any{"path": filepath.Join(workDir, "final.png")},
		},

		// u. 端点清单与桥路径都有且桥文件存在时，vision 子进程加 -e 加载桥（Magpie 路由的模型靠它注册）。
		{
			ID: "u_vision_args_with_magpie_bridge",
			VisionArgs: &gateVisionArgsCase{
				Env: map[string]string{
					"MCODE_PI_MAGPIE":        `[{"name":"m1"}]`,
					"MCODE_PI_MAGPIE_BRIDGE": bridgeFile,
				},
				Model:     "magpie/gpt-4o",
				Prompt:    "describe this image",
				ImagePath: filepath.Join(workDir, "shot.png"),
			},
		},

		// v. 只有端点清单、没有桥路径时不加 -e。
		{
			ID: "v_vision_args_without_bridge_env",
			VisionArgs: &gateVisionArgsCase{
				Env:       map[string]string{"MCODE_PI_MAGPIE": `[{"name":"m1"}]`},
				Model:     "magpie/gpt-4o",
				Prompt:    "describe this image",
				ImagePath: filepath.Join(workDir, "shot.png"),
			},
		},

		// w. 只有桥路径、没有端点清单时不加 -e（桥拿不到端点，加载了也没用）。
		{
			ID: "w_vision_args_without_magpie_env",
			VisionArgs: &gateVisionArgsCase{
				Env:       map[string]string{"MCODE_PI_MAGPIE_BRIDGE": bridgeFile},
				Model:     "magpie/gpt-4o",
				Prompt:    "describe this image",
				ImagePath: filepath.Join(workDir, "shot.png"),
			},
		},

		// x. 两个变量都有但桥文件不存在时不加 -e（否则 pi 会扇出扩展加载错误）。
		{
			ID: "x_vision_args_missing_bridge_file",
			VisionArgs: &gateVisionArgsCase{
				Env: map[string]string{
					"MCODE_PI_MAGPIE":        `[{"name":"m1"}]`,
					"MCODE_PI_MAGPIE_BRIDGE": filepath.Join(workDir, "absent-bridge.ts"),
				},
				Model:     "magpie/gpt-4o",
				Prompt:    "describe this image",
				ImagePath: filepath.Join(workDir, "shot.png"),
			},
		},
		// y. 控制面只防写：只读命令碰到 ~/.pi/agent 不再询问，写控制面与读凭证仍要询问；
		//    >/dev/null、2>&1 这类丢弃输出的重定向不算写，真正写文件的重定向仍然不算只读。
		{
			ID:       "y_bash_ls_control_plane_devnull",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input: map[string]any{
				"command": "ls -l " + filepath.Join(homeDir, ".pi", "agent", "sessions") + " 2>/dev/null | head",
			},
		},
		{
			ID:       "y_bash_du_control_plane",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "du -sh " + filepath.Join(homeDir, ".pi", "agent", "sessions")},
		},
		{
			ID:       "y_bash_redirect_control_plane",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "echo x > " + filepath.Join(homeDir, ".pi", "agent", "models.json")},
		},
		{
			ID:       "y_bash_cat_control_plane_auth",
			Gate:     map[string]any{"runtimeMode": "fullAccess"},
			ToolName: "bash",
			Input:    map[string]any{"command": "cat " + filepath.Join(homeDir, ".pi", "agent", "auth.json")},
		},
		{
			ID:       "y_bash_auto_devnull",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "ls -la 2>/dev/null"},
		},
		{
			ID:       "y_bash_auto_dup_stderr",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "cat a 2>&1 | head"},
		},
		{
			ID:       "y_bash_auto_write_redirect",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "ls > out.txt"},
		},
		{
			ID:       "y_bash_plan_devnull",
			Gate:     map[string]any{"interactionMode": "plan"},
			ToolName: "bash",
			Input:    map[string]any{"command": "rg foo 2>/dev/null"},
		},
		// z. readonly 与 auto 只放行只读命令；cd / git ls-files / git worktree list 也算只读；
		//    引号里的分隔符不再切开命令，但引号外的写操作仍要被切出来。
		{
			ID:       "z_bash_readonly_grep_allow",
			Gate:     map[string]any{"runtimeMode": "readonly"},
			ToolName: "bash",
			Input:    map[string]any{"command": "grep -n foo bar.txt"},
		},
		{
			ID:       "z_bash_readonly_rm_ask",
			Gate:     map[string]any{"runtimeMode": "readonly"},
			ToolName: "bash",
			Input:    map[string]any{"command": "rm a.txt"},
		},
		{
			ID:       "z_bash_supervised_grep_ask",
			Gate:     map[string]any{"runtimeMode": "supervised"},
			ToolName: "bash",
			Input:    map[string]any{"command": "grep -n foo bar.txt"},
		},
		{
			ID:       "z_bash_auto_cd_and_ls",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "cd internal && ls"},
		},
		{
			ID:       "z_bash_auto_git_ls_files",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "git ls-files dist"},
		},
		{
			ID:       "z_bash_auto_git_worktree_list",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "git worktree list"},
		},
		{
			ID:       "z_bash_auto_grep_pipe_head",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input: map[string]any{
				"command": "grep -n \"outDir\\|webassets\" web/vite.config.ts | head -20",
			},
		},
		{
			ID:       "z_bash_auto_echo_then_grep",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input: map[string]any{
				"command": "echo \"--- scripts:\"; grep -n '\"scripts\"' -A 15 web/package.json",
			},
		},
		{
			ID:       "z_bash_auto_git_worktree_add_ask",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "git worktree add ../x"},
		},
		{
			ID:       "z_bash_auto_quoted_semicolon_ask",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "grep \"a; rm b\" f; rm c"},
		},
		{
			ID:       "z_bash_auto_unterminated_quote_ask",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "grep \"unterminated f"},
		},
		{
			ID:       "z_bash_auto_cd_and_rm_ask",
			Gate:     map[string]any{"runtimeMode": "auto"},
			ToolName: "bash",
			Input:    map[string]any{"command": "cd x && rm y"},
		},
	}

	results, registeredTools := runGateDriver(t, nodePath, driverPath, gatePath, homeDir, gateFile, cases)

	t.Run("fullAccess_project_env", func(t *testing.T) {
		for _, id := range []string{"env_source_full", "env_local_full", "env_read_full", "env_write_full"} {
			if res := results[id]; res.Result != "allow" || res.Sensitive != "" {
				t.Errorf("%s 应放行，得到 result=%q sensitive=%q", id, res.Result, res.Sensitive)
			}
		}
		for _, id := range []string{"env_ssh_full", "env_absolute_outside_full", "env_relative_outside_full", "env_mixed_full", "env_read_outside_full", "env_write_outside_full", "env_source_auto"} {
			if res := results[id]; res.Result != "ask" || res.Sensitive != "credential" {
				t.Errorf("%s 应询问凭证，得到 result=%q sensitive=%q", id, res.Result, res.Sensitive)
			}
		}
		if res := results["env_control_full"]; res.Result != "ask" || res.Sensitive != "controlPlane" {
			t.Errorf("控制面写入应询问，得到 result=%q sensitive=%q", res.Result, res.Sensitive)
		}
	})

	t.Run("a_fullAccess_write_and_bash_allow", func(t *testing.T) {
		resWrite := results["a_write"]
		if resWrite.Result != "allow" {
			t.Errorf("fullAccess 下 write 普通路径应放行，得到 result=%q", resWrite.Result)
		}
		resBash := results["a_bash"]
		if resBash.Result != "allow" {
			t.Errorf("fullAccess 下 bash make build 应放行，得到 result=%q", resBash.Result)
		}
	})

	t.Run("b_fullAccess_read_ssh_key_ask_credential", func(t *testing.T) {
		res := results["b_read_ssh"]
		if res.Result != "ask" {
			t.Fatalf("fullAccess 下 read ~/.ssh/id_rsa 应询问，得到 result=%q", res.Result)
		}
		if res.Sensitive != "credential" {
			t.Errorf("sensitive 期望 credential，得到 %q", res.Sensitive)
		}
		if slices.Contains(res.Options, "Allow for session") {
			t.Errorf("凭证敏感操作 options 不应包含 Allow for session，得到 %v", res.Options)
		}
	})

	t.Run("c_auto_bash_credential_and_safe", func(t *testing.T) {
		resAws := results["c_bash_aws"]
		if resAws.Result != "ask" {
			t.Fatalf("auto 下 bash cat ~/.aws/credentials 应询问，得到 result=%q", resAws.Result)
		}
		if resAws.Sensitive != "credential" {
			t.Errorf("sensitive 期望 credential，得到 %q", resAws.Sensitive)
		}
		if slices.Contains(resAws.Options, "Allow for session") {
			t.Errorf("凭证敏感操作 options 不应包含 Allow for session，得到 %v", resAws.Options)
		}

		resLs := results["c_bash_ls"]
		if resLs.Result != "allow" {
			t.Errorf("auto 下 bash ls -la 应放行，得到 result=%q", resLs.Result)
		}
	})

	t.Run("d_auto_read_env_and_example", func(t *testing.T) {
		resEnv := results["d_read_env"]
		if resEnv.Result != "ask" {
			t.Fatalf("auto 下 read .env 应询问，得到 result=%q", resEnv.Result)
		}
		if resEnv.Sensitive != "credential" {
			t.Errorf("sensitive 期望 credential，得到 %q", resEnv.Sensitive)
		}

		resExample := results["d_read_env_example"]
		if resExample.Result != "allow" {
			t.Errorf("auto 下 read .env.example 应放行，得到 result=%q", resExample.Result)
		}
	})

	t.Run("e_fullAccess_write_control_plane", func(t *testing.T) {
		// 门禁文件所在目录是系统临时目录，不再算控制面，普通写入直接放行。
		for _, id := range []string{"e_write_gate_dir", "e_bash_tmpdir_loop", "e_write_tmpdir_file"} {
			res := results[id]
			if res.Result != "allow" {
				t.Errorf("fullAccess 下临时目录不是控制面，%s 应放行，得到 result=%q", id, res.Result)
			}
			if res.Sensitive != "" {
				t.Errorf("%s 不应是敏感操作，sensitive 应为空，得到 %q", id, res.Sensitive)
			}
		}

		// 门禁文件本身仍受保护。
		for _, id := range []string{"e_write_gate_file", "e_bash_rm_gate_file"} {
			res := results[id]
			if res.Result != "ask" {
				t.Fatalf("碰门禁文件本身应询问（%s），得到 result=%q", id, res.Result)
			}
			if res.Sensitive != "controlPlane" {
				t.Errorf("%s sensitive 期望 controlPlane，得到 %q", id, res.Sensitive)
			}
		}

		resModels := results["e_write_agent_models"]
		if resModels.Result != "ask" {
			t.Fatalf("write 到 HOME/.pi/agent/models.json 应询问，得到 result=%q", resModels.Result)
		}
		if resModels.Sensitive != "controlPlane" {
			t.Errorf("sensitive 期望 controlPlane，得到 %q", resModels.Sensitive)
		}
	})

	t.Run("f_plan_mode_block_write_allow_read", func(t *testing.T) {
		resWrite := results["f_write_plan"]
		if resWrite.Result != "block" {
			t.Errorf("plan 模式下 write 普通路径应被拦截，得到 result=%q", resWrite.Result)
		}

		resRead := results["f_read_plan"]
		if resRead.Result != "allow" {
			t.Errorf("plan 模式下 read 普通路径应放行，得到 result=%q", resRead.Result)
		}
	})

	t.Run("s_plan_mode_readonly_bash", func(t *testing.T) {
		for _, id := range []string{"s_bash_rg_plan", "s_bash_git_status_plan"} {
			if res := results[id]; res.Result != "allow" {
				t.Errorf("plan 模式下 %s 中的只读命令应放行，得到 result=%q", id, res.Result)
			}
		}
		for _, id := range []string{"s_bash_redirect_plan", "s_bash_rm_plan"} {
			if res := results[id]; res.Result != "block" {
				t.Errorf("plan 模式下 %s 中的写命令应被拦截，得到 result=%q", id, res.Result)
			}
		}
	})

	t.Run("t_plan_mode_mcp_tools", func(t *testing.T) {
		if res := results["t_mcp_mcode_plan"]; res.Result != "allow" {
			t.Errorf("plan + fullAccess 下 mcp__mcode__ 工具应放行，得到 result=%q", res.Result)
		}
		if res := results["t_mcp_mcode_plan_supervised"]; res.Result != "ask" {
			t.Errorf("plan + 非 fullAccess 下 mcp__mcode__ 工具应询问，得到 result=%q", res.Result)
		}
		if res := results["t_mcp_other_plan"]; res.Result != "block" {
			t.Errorf("plan 模式下外部 MCP 工具应被拦截，得到 result=%q", res.Result)
		}
	})

	t.Run("u_plan_mode_grep_edit", func(t *testing.T) {
		if res := results["u_grep_plan"]; res.Result != "allow" {
			t.Errorf("plan 模式下 grep 应放行，得到 result=%q", res.Result)
		}
		if res := results["u_edit_plan"]; res.Result != "block" {
			t.Errorf("plan 模式下 edit 应被拦截，得到 result=%q", res.Result)
		}
	})

	t.Run("g_finalReport_block_read", func(t *testing.T) {
		res := results["g_read_final_report"]
		if res.Result != "block" {
			t.Errorf("finalReport 为 true 时 read 应被拦截，得到 result=%q", res.Result)
		}
	})

	t.Run("h_supervised_edit_ask_not_sensitive", func(t *testing.T) {
		res := results["h_edit_supervised"]
		if res.Result != "ask" {
			t.Fatalf("supervised 下 edit 普通路径应询问，得到 result=%q", res.Result)
		}
		if res.Sensitive != "" {
			t.Errorf("普通 edit sensitive 应为空，得到 %q", res.Sensitive)
		}
		if !slices.Contains(res.Options, "Allow for session") {
			t.Errorf("非敏感操作 options 应包含 Allow for session，得到 %v", res.Options)
		}
	})

	t.Run("i_supervised_mcode_mcp_list_allow", func(t *testing.T) {
		res := results["i_mcp_list_supervised"]
		if res.Result != "allow" {
			t.Errorf("supervised 下 mcode_mcp_list 应直接放行，得到 result=%q", res.Result)
		}
	})

	t.Run("registered_tools_contains_ask_user_question", func(t *testing.T) {
		if !slices.Contains(registeredTools, "ask_user_question") {
			t.Errorf("扩展应注册 ask_user_question 工具，实际注册列表: %v", registeredTools)
		}
	})

	t.Run("p_vision_absent_without_model", func(t *testing.T) {
		res := results["p_vision_absent_without_model"]
		if slices.Contains(res.RegisteredTools, "vision") {
			t.Errorf("未设置 MCODE_PI_VISION_MODEL 时不应注册 vision，实际注册列表: %v", res.RegisteredTools)
		}
	})

	t.Run("q_vision_registered_with_model", func(t *testing.T) {
		res := results["q_vision_registered_with_model"]
		if !slices.Contains(res.RegisteredTools, "vision") {
			t.Errorf("设置 MCODE_PI_VISION_MODEL 后应注册 vision，实际注册列表: %v", res.RegisteredTools)
		}
	})

	t.Run("r_supervised_vision_allow", func(t *testing.T) {
		res := results["r_vision_supervised_allow"]
		if res.Result != "allow" {
			t.Errorf("supervised 下 vision 读普通图片应直接放行，得到 result=%q", res.Result)
		}
	})

	t.Run("s_vision_credential_ask", func(t *testing.T) {
		res := results["s_vision_credential_ask"]
		if res.Result != "ask" {
			t.Fatalf("fullAccess 下 vision 读 ~/.ssh/id_rsa 应询问，得到 result=%q", res.Result)
		}
		if res.Sensitive != "credential" {
			t.Errorf("sensitive 期望 credential，得到 %q", res.Sensitive)
		}
		if slices.Contains(res.Options, "Allow for session") {
			t.Errorf("凭证敏感操作 options 不应包含 Allow for session，得到 %v", res.Options)
		}
	})

	t.Run("t_vision_final_report_block", func(t *testing.T) {
		res := results["t_vision_final_report_block"]
		if res.Result != "block" {
			t.Errorf("finalReport 下 vision 应被拦截，得到 result=%q", res.Result)
		}
	})

	t.Run("u_vision_args_with_magpie_bridge", func(t *testing.T) {
		args := results["u_vision_args_with_magpie_bridge"].VisionArgs
		if !slices.Contains(args, "--no-extensions") {
			t.Errorf("vision 子进程仍应关闭自动扩展发现，得到 %v", args)
		}
		idx := slices.Index(args, "-e")
		if idx < 0 || idx+1 >= len(args) {
			t.Fatalf("配置 Magpie 后应显式 -e 加载桥，得到 %v", args)
		}
		if args[idx+1] != bridgeFile {
			t.Errorf("-e 应为桥路径 %q，得到 %q", bridgeFile, args[idx+1])
		}
		if !slices.Contains(args, "@"+filepath.Join(workDir, "shot.png")) {
			t.Errorf("参数里应把图片作为 @路径 附上，得到 %v", args)
		}
	})

	t.Run("v_vision_args_without_bridge_env", func(t *testing.T) {
		args := results["v_vision_args_without_bridge_env"].VisionArgs
		if slices.Contains(args, "-e") {
			t.Errorf("没配桥路径时不应加载扩展，得到 %v", args)
		}
	})

	t.Run("w_vision_args_without_magpie_env", func(t *testing.T) {
		args := results["w_vision_args_without_magpie_env"].VisionArgs
		if slices.Contains(args, "-e") {
			t.Errorf("没配端点清单时不应加载扩展，得到 %v", args)
		}
	})

	t.Run("x_vision_args_missing_bridge_file", func(t *testing.T) {
		args := results["x_vision_args_missing_bridge_file"].VisionArgs
		if slices.Contains(args, "-e") {
			t.Errorf("桥文件不存在时不应加载扩展，得到 %v", args)
		}
	})

	t.Run("y_control_plane_readonly_bash_allow", func(t *testing.T) {
		for _, id := range []string{"y_bash_ls_control_plane_devnull", "y_bash_du_control_plane"} {
			res := results[id]
			if res.Result != "allow" {
				t.Errorf("fullAccess 下只读命令碰到控制面应放行（%s），得到 result=%q", id, res.Result)
			}
			if res.Sensitive != "" {
				t.Errorf("%s 不是敏感操作，sensitive 应为空，得到 %q", id, res.Sensitive)
			}
		}
	})

	t.Run("y_control_plane_write_and_credential_ask", func(t *testing.T) {
		resWrite := results["y_bash_redirect_control_plane"]
		if resWrite.Result != "ask" {
			t.Fatalf("fullAccess 下写控制面文件应询问，得到 result=%q", resWrite.Result)
		}
		if resWrite.Sensitive != "controlPlane" {
			t.Errorf("写控制面 sensitive 期望 controlPlane，得到 %q", resWrite.Sensitive)
		}

		resAuth := results["y_bash_cat_control_plane_auth"]
		if resAuth.Result != "ask" {
			t.Fatalf("fullAccess 下读控制面凭证应询问，得到 result=%q", resAuth.Result)
		}
		if resAuth.Sensitive != "credential" {
			t.Errorf("读凭证 sensitive 期望 credential，得到 %q", resAuth.Sensitive)
		}
	})

	t.Run("y_harmless_redirects_are_readonly", func(t *testing.T) {
		for _, id := range []string{"y_bash_auto_devnull", "y_bash_auto_dup_stderr", "y_bash_plan_devnull"} {
			if res := results[id]; res.Result != "allow" {
				t.Errorf("丢弃输出的重定向仍应算只读（%s），得到 result=%q", id, res.Result)
			}
		}
		if res := results["y_bash_auto_write_redirect"]; res.Result != "ask" {
			t.Errorf("auto 下写文件的重定向不能放行，得到 result=%q", res.Result)
		}
	})

	t.Run("z_readonly_and_supervised_modes", func(t *testing.T) {
		if res := results["z_bash_readonly_grep_allow"]; res.Result != "allow" {
			t.Errorf("readonly 下纯读命令应放行，得到 result=%q", res.Result)
		}
		if res := results["z_bash_readonly_rm_ask"]; res.Result != "ask" {
			t.Errorf("readonly 下写命令仍要询问，得到 result=%q", res.Result)
		}
		if res := results["z_bash_supervised_grep_ask"]; res.Result != "ask" {
			t.Errorf("supervised 下只读命令仍要询问，得到 result=%q", res.Result)
		}
	})

	t.Run("z_auto_only_read_commands_allow", func(t *testing.T) {
		for _, id := range []string{
			"z_bash_auto_cd_and_ls",
			"z_bash_auto_git_ls_files",
			"z_bash_auto_git_worktree_list",
			"z_bash_auto_grep_pipe_head",
			"z_bash_auto_echo_then_grep",
		} {
			if res := results[id]; res.Result != "allow" {
				t.Errorf("auto 下只读命令应放行（%s），得到 result=%q", id, res.Result)
			}
		}
	})

	t.Run("z_auto_writes_and_unclosed_quote_ask", func(t *testing.T) {
		for _, id := range []string{
			"z_bash_auto_git_worktree_add_ask",
			"z_bash_auto_quoted_semicolon_ask",
			"z_bash_auto_unterminated_quote_ask",
			"z_bash_auto_cd_and_rm_ask",
		} {
			if res := results[id]; res.Result != "ask" {
				t.Errorf("auto 下写操作仍要询问（%s），得到 result=%q", id, res.Result)
			}
		}
	})

	t.Run("j_bash_timeout_handling", func(t *testing.T) {
		resDefault := results["j_bash_default_timeout"]
		if resDefault.Result != "allow" {
			t.Errorf("bash 默认超时用例应放行，得到 result=%q", resDefault.Result)
		}
		if timeout, ok := resDefault.Input["timeout"].(float64); !ok || timeout != 300 {
			t.Errorf("未设置 timeout 时应默认为 300，得到 %+v", resDefault.Input["timeout"])
		}

		resClamped := results["j_bash_timeout_5000_clamped"]
		if timeout, ok := resClamped.Input["timeout"].(float64); !ok || timeout != 1800 {
			t.Errorf("timeout 为 5000 时应被压到 1800，得到 %+v", resClamped.Input["timeout"])
		}

		resPreserved := results["j_bash_timeout_60_preserved"]
		if timeout, ok := resPreserved.Input["timeout"].(float64); !ok || timeout != 60 {
			t.Errorf("timeout 为 60 时应保持 60，得到 %+v", resPreserved.Input["timeout"])
		}
	})

	t.Run("k_supervised_ask_user_question_allow", func(t *testing.T) {
		res := results["k_ask_user_question_supervised"]
		if res.Result != "allow" {
			t.Errorf("supervised 下调用 ask_user_question 应直接放行，得到 result=%q", res.Result)
		}
	})

	t.Run("l_final_report_ask_user_question_block", func(t *testing.T) {
		res := results["l_ask_user_question_final_report"]
		if res.Result != "block" {
			t.Errorf("finalReport 为 true 时 ask_user_question 应被拦截，得到 result=%q", res.Result)
		}
	})

	t.Run("m_ask_user_question_exec_select", func(t *testing.T) {
		res := results["m_ask_user_question_exec_select"]
		if res.UIMethod != "select" {
			t.Errorf("有 options 时应调用 ctx.ui.select，得到 uiMethod=%q", res.UIMethod)
		}
		content, ok := res.ExecuteResult["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("executeResult content 期望非空切片，得到 %+v", res.ExecuteResult)
		}
		item, ok := content[0].(map[string]any)
		if !ok || item["text"] != "Option A" {
			t.Errorf("期望返回文本 Option A，得到 %+v", item)
		}
	})

	t.Run("n_ask_user_question_exec_input", func(t *testing.T) {
		res := results["n_ask_user_question_exec_input"]
		if res.UIMethod != "input" {
			t.Errorf("无 options 时应调用 ctx.ui.input，得到 uiMethod=%q", res.UIMethod)
		}
		content, ok := res.ExecuteResult["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("executeResult content 期望非空切片，得到 %+v", res.ExecuteResult)
		}
		item, ok := content[0].(map[string]any)
		if !ok || item["text"] != "Alice" {
			t.Errorf("期望返回文本 Alice，得到 %+v", item)
		}
	})

	t.Run("o_ask_user_question_exec_empty_fallback", func(t *testing.T) {
		res := results["o_ask_user_question_exec_empty"]
		content, ok := res.ExecuteResult["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("executeResult content 期望非空切片，得到 %+v", res.ExecuteResult)
		}
		item, ok := content[0].(map[string]any)
		if !ok || item["text"] != "The user did not answer." {
			t.Errorf("回答为空时期望返回 The user did not answer.，得到 %+v", item)
		}
	})
}
