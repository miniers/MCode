package codex

import (
	"reflect"
	"testing"

	"mcode/internal/harness"
)

// TestBuildThreadParamsWritableRoots 覆盖附加目录写进 writable_roots：只有 workspace-write 档
// 且附加目录非空时才带 sandbox_workspace_write 配置，其余档位与空附加目录都不带。
func TestBuildThreadParamsWritableRoots(t *testing.T) {
	turn := harness.Turn{
		WorkDir:        "/work/main",
		AdditionalDirs: []string{"/proj/a", "/work/main", "/proj/b"},
	}

	params, _ := buildThreadParams(turn, "workspace-write", "on-request", "", harness.BuiltinMCP{}, nil, nil)
	cfg, _ := params["config"].(map[string]any)
	if cfg == nil {
		t.Fatalf("workspace-write 下应写入 config: %v", params)
	}
	section, _ := cfg["sandbox_workspace_write"].(map[string]any)
	want := map[string]any{"writable_roots": []string{"/proj/a", "/proj/b"}}
	if !reflect.DeepEqual(section, want) {
		t.Fatalf("sandbox_workspace_write = %#v, want %#v", section, want)
	}

	// 没有附加目录时不带 writable_roots。
	empty, _ := buildThreadParams(harness.Turn{WorkDir: "/work/main"}, "workspace-write", "on-request", "", harness.BuiltinMCP{}, nil, nil)
	if cfg, _ := empty["config"].(map[string]any); cfg["sandbox_workspace_write"] != nil {
		t.Fatalf("没有附加目录时不应带 sandbox_workspace_write: %v", empty)
	}

	// read-only 与 danger-full-access 档不带 writable_roots。
	for _, sandbox := range []string{"read-only", "danger-full-access"} {
		got, _ := buildThreadParams(turn, sandbox, "on-request", "", harness.BuiltinMCP{}, nil, nil)
		if cfg, _ := got["config"].(map[string]any); cfg != nil {
			if _, exists := cfg["sandbox_workspace_write"]; exists {
				t.Fatalf("%s 档不应带 writable_roots: %v", sandbox, got)
			}
		}
	}
}

func TestBuildThreadParamsWindowsSandbox(t *testing.T) {
	original := isWindowsHost
	t.Cleanup(func() { isWindowsHost = original })
	turn := harness.Turn{WorkDir: "/work/main", AdditionalDirs: []string{"/proj/a"}}
	for _, tc := range []struct {
		name    string
		windows bool
		sandbox string
		want    bool
	}{
		{"windows workspace-write", true, "workspace-write", true},
		{"windows read-only", true, "read-only", true},
		{"windows full access", true, "danger-full-access", false},
		{"other host", false, "workspace-write", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isWindowsHost = tc.windows
			params, _ := buildThreadParams(turn, tc.sandbox, "on-request", "", harness.BuiltinMCP{URL: "http://builtin", Token: "token"}, nil, nil)
			cfg := params["config"].(map[string]any)
			windows, exists := cfg["windows"]
			if exists != tc.want || (exists && !reflect.DeepEqual(windows, map[string]any{"sandbox": "unelevated"})) {
				t.Fatalf("windows = %#v, exists = %v, want %v", windows, exists, tc.want)
			}
			if _, exists := cfg["mcp_servers"]; !exists {
				t.Fatal("mcp_servers missing")
			}
			if tc.sandbox == "workspace-write" {
				want := map[string]any{"writable_roots": []string{"/proj/a"}}
				if !reflect.DeepEqual(cfg["sandbox_workspace_write"], want) {
					t.Fatalf("sandbox_workspace_write = %#v, want %#v", cfg["sandbox_workspace_write"], want)
				}
			}
		})
	}
}
