package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

func TestMagpieRouteFor(t *testing.T) {
	routes := []MagpieRoute{{ProviderID: "gateway", BaseURL: "http://localhost/v1"}}
	for _, tc := range []struct {
		model string
		want  string
		hit   bool
		err   bool
	}{
		{"gateway/vendor/model", "vendor/model", true, false},
		{"unknown/model", "", false, false},
		{"gateway/ ", "", false, true},
	} {
		route, model, err := magpieRouteFor(tc.model, routes)
		if (err != nil) != tc.err || model != tc.want || (route.ProviderID != "") != tc.hit {
			t.Errorf("model %q: route=%+v model=%q err=%v", tc.model, route, model, err)
		}
	}
}

func TestMagpieTurn(t *testing.T) {
	routes := []MagpieRoute{{ProviderID: "gateway", Default: " fallback "}}
	for _, tc := range []struct {
		model string
		want  string
		hit   bool
	}{
		{"", "fallback", true},
		{"default", "fallback", true},
		{"gateway/chosen", "chosen", true},
		{"native", "native", false},
		{"other/model", "other/model", false},
	} {
		turn, route, err := magpieTurn(harness.Turn{Model: tc.model}, routes)
		if err != nil || turn.Model != tc.want || (route != nil) != tc.hit {
			t.Errorf("model %q: turn=%+v route=%+v err=%v", tc.model, turn, route, err)
		}
	}
}

func TestMagpieArgsAndEnv(t *testing.T) {
	route := MagpieRoute{BaseURL: "http://local/\"quoted\\path\n/v1", APIKey: "secret-key"}
	want := []string{
		"-c", `model_provider="mcode-magpie"`,
		"-c", `model_providers.mcode-magpie.name="Magpie"`,
		"-c", `model_providers.mcode-magpie.base_url="http://local/\"quoted\\path\u000A/v1"`,
		"-c", `model_providers.mcode-magpie.wire_api="responses"`,
		"-c", `model_providers.mcode-magpie.env_key="MCODE_MAGPIE_API_KEY"`,
		"-c", `model_providers.mcode-magpie.requires_openai_auth=false`,
	}
	args := magpieArgs(route)
	if !reflect.DeepEqual(args, want) || strings.Contains(strings.Join(args, " "), route.APIKey) {
		t.Fatalf("unexpected args: %q", args)
	}
	env := strings.Join(magpieEnv(route), "\n")
	for _, want := range []string{"MCODE_MAGPIE_API_KEY=secret-key", "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in env", want)
		}
	}
}

type magpieCaptureSandbox struct {
	script string
	args   [][]string
}

func (s *magpieCaptureSandbox) Command(ctx context.Context, workDir string, mode harness.PermissionMode, name string, args ...string) (*exec.Cmd, func(), error) {
	s.args = append(s.args, append([]string(nil), args...))
	return exec.CommandContext(ctx, "python3", "-u", "-c", s.script), nil, nil
}

func TestMagpieProcessRoutingAndResume(t *testing.T) {
	testenv.TempHome(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	sandbox := &magpieCaptureSandbox{script: codexRetryScript("", 0, filepath.Join(dir, "count"), logPath)}
	routes := []MagpieRoute{{ProviderID: "gateway", BaseURL: "http://localhost/v1", APIKey: "secret-key"}}
	h := New(Options{MagpieRoutes: func() []MagpieRoute { return routes }})
	t.Cleanup(func() { _ = h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	turn := harness.Turn{WorkDir: dir, Sandbox: sandbox, Prompt: "test", Model: "native", Env: []string{"NO_PROXY=example.com"}}
	run := func() *residentProcess {
		t.Helper()
		if err := h.Run(ctx, turn, nil); err != nil {
			t.Fatal(err)
		}
		procs := h.poolProcs()
		return procs[len(procs)-1]
	}
	native := run()
	turn.SessionID = "th_mock_123"
	turn.Model = "gateway/model"
	gateway := run()
	if gateway == native || run() != gateway {
		t.Fatal("route change must start a new process and the same route must reuse it")
	}
	if len(sandbox.args) != 2 || !reflect.DeepEqual(sandbox.args[1], append([]string{"app-server", "--stdio"}, magpieArgs(routes[0])...)) {
		t.Fatalf("unexpected sandbox arguments: %q", sandbox.args)
	}
	env := strings.Join(gateway.cmd.Env, "\n")
	if !strings.Contains(env, "MCODE_MAGPIE_API_KEY=secret-key") || !strings.Contains(env, "NO_PROXY=example.com,localhost,127.0.0.1,::1") {
		t.Fatal("gateway environment is missing the key or merged NO_PROXY")
	}
	routes[0].APIKey = "other-key"
	if run() == gateway {
		t.Fatal("changed key must start a new process")
	}
	turn.Model = "native"
	if run() != native {
		t.Fatal("returning to the native route must reuse its process")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var resumed bool
	var gatewayModel bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		if request.Method == "thread/resume" && request.Params["threadId"] == turn.SessionID {
			resumed = true
		}
		if request.Method == "turn/start" && request.Params["model"] == "model" {
			gatewayModel = true
		}
	}
	if !resumed || !gatewayModel {
		t.Fatalf("missing resumed thread or rewritten gateway model: resumed=%v model=%v", resumed, gatewayModel)
	}
}

func TestMagpieRouteKey(t *testing.T) {
	base := MagpieRoute{ProviderID: "one", BaseURL: "url", APIKey: "key"}
	if magpieRouteKey(nil) != "" || magpieRouteKey(&base) != magpieRouteKey(&base) {
		t.Fatal("invalid native or stable route key")
	}
	for _, other := range []MagpieRoute{
		{ProviderID: "two", BaseURL: "url", APIKey: "key"},
		{ProviderID: "one", BaseURL: "other", APIKey: "key"},
		{ProviderID: "one", BaseURL: "url", APIKey: "other"},
	} {
		if magpieRouteKey(&base) == magpieRouteKey(&other) {
			t.Fatal("different routes must have different keys")
		}
	}
}
