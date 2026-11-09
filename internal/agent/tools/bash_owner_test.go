package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"mcode/internal/agent/shell"
)

func TestBashOwnerIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 Unix shell")
	}
	a := shell.WithOwner(context.Background(), "bash-test-a")
	b := shell.WithOwner(context.Background(), "bash-test-b")
	id, _, err := shell.Start(a, shell.Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Kill(id)
	raw, _ := json.Marshal(map[string]string{"id": id})
	for _, run := range []func(context.Context, Env, json.RawMessage) (Result, error){runBashOutput, runKillBash} {
		result, err := run(b, Env{}, raw)
		if err != nil || !result.IsError {
			t.Fatalf("跨线程访问: result=%+v err=%v", result, err)
		}
	}
	result, err := runBashOutput(a, Env{}, raw)
	if err != nil || result.IsError {
		t.Fatalf("同线程读取: result=%+v err=%v", result, err)
	}
}
