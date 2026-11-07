package pi

import (
	"path/filepath"
	"testing"
)

func TestConfigDir(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{
			name: "PI_CODING_AGENT_DIR 优先",
			env:  map[string]string{"PI_CODING_AGENT_DIR": "/tmp/pi-agent", "HOME": "/tmp/home"},
			home: "/tmp/other-home",
			want: "/tmp/pi-agent",
		},
		{
			name: "home 参数优先于 env 的 HOME",
			env:  map[string]string{"HOME": "/tmp/home"},
			home: "/tmp/other-home",
			want: filepath.Join("/tmp/other-home", ".pi", "agent"),
		},
		{
			name: "home 为空时用 env 的 HOME",
			env:  map[string]string{"HOME": "/tmp/home"},
			home: "",
			want: filepath.Join("/tmp/home", ".pi", "agent"),
		},
		{
			name: "空白的 PI_CODING_AGENT_DIR 不算设置",
			env:  map[string]string{"PI_CODING_AGENT_DIR": "  ", "HOME": "/tmp/home"},
			home: "",
			want: filepath.Join("/tmp/home", ".pi", "agent"),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ConfigDir(testCase.env, testCase.home); got != testCase.want {
				t.Errorf("ConfigDir(%v, %q) = %q，期望 %q", testCase.env, testCase.home, got, testCase.want)
			}
		})
	}

	// env 与 home 都取不到时退到进程主目录，只核对结尾。
	got := ConfigDir(map[string]string{}, "")
	if suffix := filepath.Join(".pi", "agent"); got == suffix || filepath.Base(filepath.Dir(got)) != ".pi" {
		t.Errorf("ConfigDir 的回退结果不像 <home>/.pi/agent: %q", got)
	}
}

func TestAuthStatus(t *testing.T) {
	t.Run("空文件视为未登录", func(t *testing.T) {
		dir := writeConfigDir(t, map[string][]byte{"auth.json": loadPiTestdata(t, "auth_empty.json")})
		signedIn, providers, err := AuthStatus(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir)
		if err != nil {
			t.Fatalf("AuthStatus 失败: %v", err)
		}
		if signedIn || len(providers) != 0 {
			t.Errorf("空 auth.json 应视为未登录，实际 signedIn=%v providers=%v", signedIn, providers)
		}
	})

	t.Run("有键的对象视为已登录并返回排序键名", func(t *testing.T) {
		dir := writeConfigDir(t, map[string][]byte{"auth.json": loadPiTestdata(t, "auth_full.json")})
		signedIn, providers, err := AuthStatus(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir)
		if err != nil {
			t.Fatalf("AuthStatus 失败: %v", err)
		}
		if !signedIn {
			t.Fatal("有登录条目的 auth.json 应视为已登录")
		}
		want := []string{"anthropic", "google", "openai-codex", "some-api-key"}
		if len(providers) != len(want) {
			t.Fatalf("provider 列表不符: %v", providers)
		}
		for i, key := range want {
			if providers[i] != key {
				t.Errorf("第 %d 个 provider = %q，期望 %q（应排序）", i, providers[i], key)
			}
		}
	})

	t.Run("空对象视为未登录", func(t *testing.T) {
		dir := writeConfigDir(t, map[string][]byte{"auth.json": []byte("{}\n")})
		signedIn, providers, err := AuthStatus(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir)
		if err != nil || signedIn || len(providers) != 0 {
			t.Errorf("{} 应视为未登录: signedIn=%v providers=%v err=%v", signedIn, providers, err)
		}
	})

	t.Run("文件不存在视为未登录且不是错误", func(t *testing.T) {
		dir := t.TempDir()
		signedIn, providers, err := AuthStatus(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir)
		if err != nil || signedIn || providers != nil {
			t.Errorf("缺文件时应有 (false, nil, nil)，实际 (%v, %v, %v)", signedIn, providers, err)
		}
	})

	t.Run("不是 JSON 对象时报错", func(t *testing.T) {
		dir := writeConfigDir(t, map[string][]byte{"auth.json": []byte("[1,2]")})
		if _, _, err := AuthStatus(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir); err == nil {
			t.Error("auth.json 不是对象时应报错")
		}
	})
}

func TestSettings(t *testing.T) {
	dir := writeConfigDir(t, map[string][]byte{"settings.json": loadPiTestdata(t, "settings.json")})
	settings := Settings(map[string]string{"PI_CODING_AGENT_DIR": dir}, dir)
	if settings == nil {
		t.Fatal("settings.json 应能读出")
	}
	if got := piString(settings, "defaultThinkingLevel"); got != "high" {
		t.Errorf("defaultThinkingLevel = %q，期望 high", got)
	}

	emptyDir := t.TempDir()
	if settings := Settings(map[string]string{"PI_CODING_AGENT_DIR": emptyDir}, emptyDir); settings != nil {
		t.Errorf("缺 settings.json 时应返回 nil，实际 %v", settings)
	}
}
