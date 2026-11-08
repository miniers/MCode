package tools

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashBackgroundTools(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("需要 Unix shell")
	}
	env := Env{WorkDir: t.TempDir()}
	result := runTool(t, env, "bash", map[string]any{"command": "echo hi", "run_in_background": true})
	if result.IsError || !strings.Contains(result.Text, "Started background job bg") {
		t.Fatalf("result=%+v", result)
	}
	id := strings.Fields(result.Text)[3]
	var output strings.Builder
	exited := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result = runTool(t, env, "bash_output", map[string]any{"id": id})
		output.WriteString(result.Text)
		if strings.Contains(result.Text, "Status: exited") {
			exited = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !exited || !strings.Contains(output.String(), "hi") || !strings.Contains(output.String(), "Status: exited (code 0)") {
		t.Fatalf("output=%q result=%+v", output.String(), result)
	}
	for _, name := range []string{"bash_output", "kill_bash"} {
		result = runTool(t, env, name, map[string]any{"id": "bg-unknown"})
		if !result.IsError || !strings.Contains(result.Text, "unknown background job") {
			t.Fatalf("%s result=%+v", name, result)
		}
	}
}
