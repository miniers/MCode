package harness

import (
	"testing"
)

func TestDecisionString(t *testing.T) {
	tests := []struct {
		decision Decision
		want     string
	}{
		{DecisionAllow, "allow"},
		{DecisionDeny, "deny"},
		{DecisionAsk, "ask"},
		{Decision(999), "Decision(999)"},
	}

	for _, tt := range tests {
		if got := tt.decision.String(); got != tt.want {
			t.Errorf("Decision.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name         string
		mode         PermissionMode
		rules        []Rule
		toolName     string
		arg          string
		readOnlyTool bool
		want         Decision
	}{
		// 1) 规则优先级：Deny > Ask > Allow
		{
			name: "deny takes precedence over allow",
			mode: ModeFullAccess,
			rules: []Rule{
				{Tool: "bash", Pattern: "*", Decision: DecisionAllow},
				{Tool: "bash", Pattern: "rm *", Decision: DecisionDeny},
			},
			toolName:     "bash",
			arg:          "rm -rf /",
			readOnlyTool: false,
			want:         DecisionDeny,
		},
		{
			name: "deny takes precedence over ask",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "bash", Pattern: "*", Decision: DecisionAsk},
				{Tool: "bash", Pattern: "shutdown *", Decision: DecisionDeny},
			},
			toolName:     "bash",
			arg:          "shutdown -h now",
			readOnlyTool: false,
			want:         DecisionDeny,
		},
		{
			name: "ask takes precedence over allow",
			mode: ModeFullAccess,
			rules: []Rule{
				{Tool: "bash", Pattern: "*", Decision: DecisionAllow},
				{Tool: "bash", Pattern: "git push *", Decision: DecisionAsk},
			},
			toolName:     "bash",
			arg:          "git push origin main",
			readOnlyTool: false,
			want:         DecisionAsk,
		},

		// 2) 空 Pattern 匹配任意参数
		{
			name: "empty pattern matches any argument",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "curl", Pattern: "", Decision: DecisionAllow},
			},
			toolName:     "curl",
			arg:          "https://example.com/api",
			readOnlyTool: false,
			want:         DecisionAllow,
		},

		// 3) 通配符匹配
		{
			name: "wildcard tool and wildcard pattern match",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "file_*", Pattern: "*.go", Decision: DecisionAllow},
			},
			toolName:     "file_read",
			arg:          "main.go",
			readOnlyTool: false,
			want:         DecisionAllow,
		},
		{
			name: "single char wildcard pattern match",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "cat", Pattern: "test?.txt", Decision: DecisionAllow},
			},
			toolName:     "cat",
			arg:          "test1.txt",
			readOnlyTool: false,
			want:         DecisionAllow,
		},
		{
			name: "pattern mismatch falls back to mode default",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "cat", Pattern: "test?.txt", Decision: DecisionAllow},
			},
			toolName:     "cat",
			arg:          "test12.txt",
			readOnlyTool: false,
			want:         DecisionAsk,
		},
		{
			name: "tool wildcard match all",
			mode: ModeAgent,
			rules: []Rule{
				{Tool: "*", Pattern: "safe_*", Decision: DecisionAllow},
			},
			toolName:     "custom_worker",
			arg:          "safe_job",
			readOnlyTool: false,
			want:         DecisionAllow,
		},

		// 4) ModeReadOnly 覆盖规则
		{
			name: "readonly mode overrides allow rule for non-readonly tool",
			mode: ModeReadOnly,
			rules: []Rule{
				{Tool: "bash", Pattern: "*", Decision: DecisionAllow},
			},
			toolName:     "bash",
			arg:          "echo safe",
			readOnlyTool: false,
			want:         DecisionDeny,
		},
		{
			name: "readonly mode overrides ask rule for non-readonly tool",
			mode: ModeReadOnly,
			rules: []Rule{
				{Tool: "write_file", Pattern: "", Decision: DecisionAsk},
			},
			toolName:     "write_file",
			arg:          "output.txt",
			readOnlyTool: false,
			want:         DecisionDeny,
		},
		{
			name: "readonly mode obeys rule for readonly tool",
			mode: ModeReadOnly,
			rules: []Rule{
				{Tool: "read_file", Pattern: "secret.key", Decision: DecisionDeny},
			},
			toolName:     "read_file",
			arg:          "secret.key",
			readOnlyTool: true,
			want:         DecisionDeny,
		},

		// 5) 无匹配规则时的模式默认策略
		// 5.1 ModeFullAccess
		{
			name:         "full access default for non-readonly tool",
			mode:         ModeFullAccess,
			rules:        nil,
			toolName:     "bash",
			arg:          "ls",
			readOnlyTool: false,
			want:         DecisionAllow,
		},
		{
			name:         "full access default for readonly tool",
			mode:         ModeFullAccess,
			rules:        nil,
			toolName:     "read_file",
			arg:          "a.txt",
			readOnlyTool: true,
			want:         DecisionAllow,
		},

		// 5.2 ModeReadOnly
		{
			name:         "readonly default for readonly tool",
			mode:         ModeReadOnly,
			rules:        nil,
			toolName:     "view_file",
			arg:          "doc.md",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "readonly default for non-readonly tool",
			mode:         ModeReadOnly,
			rules:        nil,
			toolName:     "delete_file",
			arg:          "doc.md",
			readOnlyTool: false,
			want:         DecisionDeny,
		},

		// 5.3 ModeSupervised
		{
			name:         "supervised mode default for readonly tool",
			mode:         ModeSupervised,
			rules:        nil,
			toolName:     "read_file",
			arg:          "config.json",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "supervised mode default for non-readonly tool",
			mode:         ModeSupervised,
			rules:        nil,
			toolName:     "bash",
			arg:          "deploy",
			readOnlyTool: false,
			want:         DecisionAsk,
		},

		// 5.4 ModeAutoAcceptEdits
		{
			name:         "autoAcceptEdits mode default for readonly tool",
			mode:         ModeAutoAcceptEdits,
			rules:        nil,
			toolName:     "read_file",
			arg:          "config.json",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "autoAcceptEdits mode default for non-readonly tool",
			mode:         ModeAutoAcceptEdits,
			rules:        nil,
			toolName:     "bash",
			arg:          "deploy",
			readOnlyTool: false,
			want:         DecisionAsk,
		},

		// 5.5 ModeAuto
		{
			name:         "auto mode default for readonly tool",
			mode:         ModeAuto,
			rules:        nil,
			toolName:     "read_file",
			arg:          "config.json",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "auto mode default for non-readonly tool",
			mode:         ModeAuto,
			rules:        nil,
			toolName:     "bash",
			arg:          "deploy",
			readOnlyTool: false,
			want:         DecisionAsk,
		},

		// 5.6 ModeAgent (兼容别名)
		{
			name:         "agent mode default for readonly tool",
			mode:         ModeAgent,
			rules:        nil,
			toolName:     "read_file",
			arg:          "config.json",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "agent mode default for non-readonly tool",
			mode:         ModeAgent,
			rules:        nil,
			toolName:     "bash",
			arg:          "deploy",
			readOnlyTool: false,
			want:         DecisionAsk,
		},

		// 5.7 未指定/未知模式（降级为普通询问行为）
		{
			name:         "unknown mode default for readonly tool",
			mode:         PermissionMode("unknown"),
			rules:        nil,
			toolName:     "read_file",
			arg:          "info.txt",
			readOnlyTool: true,
			want:         DecisionAllow,
		},
		{
			name:         "unknown mode default for non-readonly tool",
			mode:         PermissionMode("unknown"),
			rules:        nil,
			toolName:     "bash",
			arg:          "run",
			readOnlyTool: false,
			want:         DecisionAsk,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.mode, tt.rules, tt.toolName, tt.arg, tt.readOnlyTool)
			if got != tt.want {
				t.Fatalf("Decide() = %v (%s), want %v (%s)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestDecideAuto(t *testing.T) {
	for _, mode := range []PermissionMode{ModeAuto, ModeAutoAcceptEdits} {
		for _, tc := range []struct {
			tool, arg string
			want      Decision
		}{
			{"write_file", "a.go", DecisionAllow},
			{"edit_file", "a.go", DecisionAllow},
			{"bash", "ls", DecisionAllow},
			{"bash", "git status", DecisionAllow},
			{"bash", "go build", DecisionAsk},
			{"bash", "git branch new", DecisionAsk},
			{"bash", "rg --pre=touch pattern", DecisionAsk},
		} {
			t.Run(string(mode)+"/"+tc.tool+"/"+tc.arg, func(t *testing.T) {
				if got := Decide(mode, nil, tc.tool, tc.arg, false); got != tc.want {
					t.Errorf("Decide() = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input   string
		want    PermissionMode
		wantErr bool
	}{
		{"readonly", ModeReadOnly, false},
		{"supervised", ModeSupervised, false},
		{"autoAcceptEdits", ModeAuto, false},
		{"auto", ModeAuto, false},
		{"fullAccess", ModeFullAccess, false},
		// 旧值兼容
		{"agent", ModeAuto, false},
		{"full_access", ModeFullAccess, false},
		// 非法值与空字符串
		{"", "", true},
		{"invalid", "", true},
		{"full-access", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseMode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseMode(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if tt.wantErr && err == nil {
				t.Errorf("ParseMode(%q) expected error, got nil", tt.input)
			}
		})
	}
}
