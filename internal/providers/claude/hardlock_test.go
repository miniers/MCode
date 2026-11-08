package claude

import (
	"strings"
	"testing"

	"mcode/internal/harness"
)

func disallowedValue(t *testing.T, args []string) string {
	t.Helper()
	for i := 0; i < len(args); i++ {
		if args[i] == "--disallowed-tools" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("启动参数缺少 --disallowed-tools: %v", args)
	return ""
}

func TestBuildArgsHardLock(t *testing.T) {
	locked := disallowedValue(t, buildArgs(harness.Turn{HardLocked: true}))
	if !strings.Contains(locked, "Agent,Task") {
		t.Fatalf("硬锁模式下仍应禁用原生子代理: %q", locked)
	}
	if !strings.Contains(locked, "Bash(gh pr merge:*)") {
		t.Fatalf("硬锁模式下应包含 gh pr merge 模式: %q", locked)
	}

	unlocked := disallowedValue(t, buildArgs(harness.Turn{}))
	if strings.Contains(unlocked, "Bash(gh pr merge:*)") {
		t.Fatalf("未锁定时不应包含危险命令模式: %q", unlocked)
	}
	if unlocked != nativeSubagentTools {
		t.Fatalf("未锁定时 disallowed-tools = %q, want %q", unlocked, nativeSubagentTools)
	}

	override := disallowedValue(t, buildArgs(harness.Turn{HardLocked: true, HardLockOverride: "D-42"}))
	if strings.Contains(override, "Bash(gh pr merge:*)") {
		t.Fatalf("带 override 时不应包含危险命令模式: %q", override)
	}
}
