package claude

import (
	"reflect"
	"strings"
	"testing"

	"mcode/internal/harness"
)

// magpieTestRoute 是测试用路由：端点的 /v1 地址带末尾斜杠，验证注入时会被归一化。
func magpieTestRoute() MagpieRoute {
	return MagpieRoute{
		ProviderID: "magpie-jp",
		BaseURL:    "http://jp.example/v1/",
		APIKey:     "gw-key",
		Models:     []string{"auto/claude-sonnet-4.5"},
	}
}

func envValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func TestMagpieRouteFor(t *testing.T) {
	routes := []MagpieRoute{magpieTestRoute()}

	t.Run("命中", func(t *testing.T) {
		route, model, err := magpieRouteFor("magpie-jp/auto/claude-sonnet-4.5", routes)
		if err != nil {
			t.Fatalf("magpieRouteFor 返回错误: %v", err)
		}
		if route.ProviderID != "magpie-jp" || model != "auto/claude-sonnet-4.5" {
			t.Fatalf("route=%q model=%q，想要 magpie-jp / auto/claude-sonnet-4.5", route.ProviderID, model)
		}
	})

	t.Run("没有斜杠", func(t *testing.T) {
		route, model, err := magpieRouteFor("claude-sonnet-4-5", routes)
		if err != nil || !reflect.DeepEqual(route, MagpieRoute{}) || model != "" {
			t.Fatalf("route=%#v model=%q err=%v，想要零值与 nil", route, model, err)
		}
	})

	t.Run("前缀不认识", func(t *testing.T) {
		route, model, err := magpieRouteFor("unknown/x", routes)
		if err != nil || !reflect.DeepEqual(route, MagpieRoute{}) || model != "" {
			t.Fatalf("route=%#v model=%q err=%v，想要零值与 nil", route, model, err)
		}
	})

	t.Run("剩余为空", func(t *testing.T) {
		if _, _, err := magpieRouteFor("magpie-jp/", routes); err == nil {
			t.Fatal("剩余部分为空时应返回错误")
		}
	})
}

func TestMagpieTurnRewritesModelAndEnv(t *testing.T) {
	original := harness.Turn{
		Model: "magpie-jp/auto/claude-sonnet-4.5",
		Env:   []string{"NO_PROXY=example.com", "ANTHROPIC_MODEL=old-model"},
	}
	originalEnv := append([]string(nil), original.Env...)

	turn, err := magpieTurn(original, []MagpieRoute{magpieTestRoute()})
	if err != nil {
		t.Fatalf("magpieTurn 返回错误: %v", err)
	}
	if turn.Model != "auto/claude-sonnet-4.5" {
		t.Errorf("Model = %q，想要去掉前缀的网关模型 ID", turn.Model)
	}
	env := envValues(turn.Env)
	want := map[string]string{
		"ANTHROPIC_BASE_URL":   "http://jp.example",
		"ANTHROPIC_AUTH_TOKEN": "gw-key",
		"ANTHROPIC_MODEL":      "",
		"ANTHROPIC_API_KEY":    "",
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("%s = %q，想要 %q", key, env[key], value)
		}
	}
	if got := env["NO_PROXY"]; !strings.Contains(got, "127.0.0.1") {
		t.Errorf("NO_PROXY = %q，想要含 127.0.0.1", got)
	}
	if got := env["NO_PROXY"]; !strings.HasPrefix(got, "example.com") {
		t.Errorf("NO_PROXY = %q，想要保留原值开头", got)
	}
	if !reflect.DeepEqual(original.Env, originalEnv) {
		t.Errorf("原 turn.Env 被修改: %#v", original.Env)
	}
}

func TestMagpieTurnLeavesNonMagpieModel(t *testing.T) {
	original := harness.Turn{Model: "claude-sonnet-4-5", Env: []string{"A=1"}}
	turn, err := magpieTurn(original, []MagpieRoute{magpieTestRoute()})
	if err != nil {
		t.Fatalf("magpieTurn 返回错误: %v", err)
	}
	if !reflect.DeepEqual(turn, original) {
		t.Fatalf("未命中时 turn 应保持不变，得到 %#v，想要 %#v", turn, original)
	}
}

func TestMagpieTurnChangesResidentLaunchKey(t *testing.T) {
	h := New(Options{})
	turn := harness.Turn{
		Model:   "magpie-jp/auto/claude-sonnet-4.5",
		WorkDir: t.TempDir(),
	}
	before, err := h.planLaunch(turn)
	if err != nil {
		t.Fatalf("改写前 planLaunch 失败: %v", err)
	}
	rewritten, err := magpieTurn(turn, []MagpieRoute{magpieTestRoute()})
	if err != nil {
		t.Fatalf("magpieTurn 返回错误: %v", err)
	}
	after, err := h.planLaunch(rewritten)
	if err != nil {
		t.Fatalf("改写后 planLaunch 失败: %v", err)
	}
	if before.key == after.key {
		t.Fatal("注入前后 launchKey 相同，常驻进程不会按新路由重建")
	}
}

// 模型为 default 或空时使用路由上的默认模型；显式模型不被覆盖；没有默认值时行为不变。
func TestMagpieTurnUsesDefaultModel(t *testing.T) {
	defaultRoute := magpieTestRoute()
	defaultRoute.Default = "auto/claude-opus-4.5"

	for _, model := range []string{"default", ""} {
		original := harness.Turn{Model: model, Env: []string{"A=1"}}
		turn, err := magpieTurn(original, []MagpieRoute{defaultRoute})
		if err != nil {
			t.Fatalf("model=%q: magpieTurn 返回错误: %v", model, err)
		}
		if turn.Model != "auto/claude-opus-4.5" {
			t.Fatalf("model=%q: 改写后 = %q，想要默认模型 claude-opus-4.5", model, turn.Model)
		}
		args := buildArgs(turn)
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--model" && args[i+1] == "auto/claude-opus-4.5" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("model=%q: --model 未使用默认模型: %v", model, args)
		}
		if got := envValues(turn.Env)["ANTHROPIC_BASE_URL"]; got != "http://jp.example" {
			t.Errorf("model=%q: ANTHROPIC_BASE_URL = %q，想要路由地址", model, got)
		}
		if original.Model != model {
			t.Errorf("model=%q: 原 turn.Model 被修改成 %q", model, original.Model)
		}
	}

	// 显式选择模型时不覆盖。
	explicit := harness.Turn{Model: "magpie-jp/auto/claude-sonnet-4.5"}
	turn, err := magpieTurn(explicit, []MagpieRoute{defaultRoute})
	if err != nil {
		t.Fatalf("magpieTurn 返回错误: %v", err)
	}
	if turn.Model != "auto/claude-sonnet-4.5" {
		t.Fatalf("显式模型被覆盖成 %q", turn.Model)
	}

	// 没有默认值时 model=default 保持原样，buildArgs 不传 --model。
	turn, err = magpieTurn(harness.Turn{Model: "default"}, []MagpieRoute{magpieTestRoute()})
	if err != nil {
		t.Fatalf("magpieTurn 返回错误: %v", err)
	}
	if turn.Model != "default" || !reflect.DeepEqual(turn, harness.Turn{Model: "default"}) {
		t.Fatalf("没有默认值时 turn = %#v，想要原样返回", turn)
	}
	args := buildArgs(turn)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--model" {
			t.Fatalf("model=default 且没有默认值时不应急--model: %v", args)
		}
	}
}
