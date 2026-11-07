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

	// 没有附加目录时不带 config。
	empty, _ := buildThreadParams(harness.Turn{WorkDir: "/work/main"}, "workspace-write", "on-request", "", harness.BuiltinMCP{}, nil, nil)
	if _, exists := empty["config"]; exists {
		t.Fatalf("没有附加目录时不应带 config: %v", empty)
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
