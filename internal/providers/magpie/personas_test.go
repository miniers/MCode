package magpie

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// personaTestBaseURL 是本机 magpie 的默认地址形状，ReadPersonas 只认本机地址。
const personaTestBaseURL = "http://127.0.0.1:3425/v1"

// writePluginsFile 把 content 写成 dir 下的 name。
func writePluginsFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("写入 %s 失败: %v", name, err)
	}
}

// usePersonaConfigDir 把配置目录指向 dir，测试结束自动恢复。
func usePersonaConfigDir(t *testing.T, dir string) {
	t.Helper()
	restore := SetConfigDirForTest(dir)
	t.Cleanup(restore)
}

// pluginsJSON 拼出 plugins.json 的内容：entry 原样成为数组里的第一条插件条目。
func pluginsJSON(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"plugins": entries})
	if err != nil {
		t.Fatalf("生成 plugins.json 失败: %v", err)
	}
	return string(raw)
}

// personaEntry 是 persona-rules 插件的条目，options 由调用方给。
func personaEntry(options map[string]any) map[string]any {
	return map[string]any{
		"spec":    "@magpie-community/middleware-persona-rules@latest",
		"options": options,
	}
}

// personaIDs 取出人设 ID，便于只比较关心的部分。
func personaIDs(personas []Persona) []string {
	out := make([]string, 0, len(personas))
	for _, p := range personas {
		out = append(out, p.ID)
	}
	return out
}

func TestReadPersonas(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{
		"personas": []map[string]any{
			{"id": "coder", "name": "编码", "description": "只写代码", "text": "你是资深工程师"},
			{"id": "writer", "description": "写文档", "text": "你是技术写作"},
		},
	})))

	got, err := ReadPersonas(personaTestBaseURL)
	if err != nil {
		t.Fatalf("ReadPersonas 出错: %v", err)
	}
	want := []Persona{
		{ID: "coder", Name: "编码", Description: "只写代码"},
		// 名字为空时回落为 ID。
		{ID: "writer", Name: "writer", Description: "写文档"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadPersonas = %#v，想要 %#v", got, want)
	}
}

func TestReadPersonasPluginSpecVariants(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want bool
	}{
		{name: "社区带版本", spec: "@magpie-community/middleware-persona-rules@latest", want: true},
		{name: "社区不带版本", spec: "@magpie-community/persona-rules", want: true},
		{name: "git 仓库", spec: "github:someone/persona-rules", want: true},
		{name: "本地目录", spec: "/opt/magpie/plugins/persona-rules", want: true},
		{name: "工具类前缀", spec: "@magpie-community/tools-persona-rules@1.2.3", want: true},
		{name: "别的插件", spec: "@magpie-community/middleware-model-map@latest", want: false},
		{name: "空 spec", spec: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			usePersonaConfigDir(t, dir)
			writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, map[string]any{
				"spec": tc.spec,
				"options": map[string]any{"personas": []map[string]any{
					{"id": "coder", "text": "你是资深工程师"},
				}},
			}))

			got, err := ReadPersonas(personaTestBaseURL)
			if err != nil {
				t.Fatalf("ReadPersonas 出错: %v", err)
			}
			if found := len(got) > 0; found != tc.want {
				t.Fatalf("spec %q 命中 = %v，想要 %v", tc.spec, found, tc.want)
			}
		})
	}
}

func TestReadPersonasNonLoopback(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{
		"personas": []map[string]any{{"id": "coder", "text": "你是资深工程师"}},
	})))

	for _, baseURL := range []string{
		"http://10.0.0.5:3425/v1",
		"https://magpie.example.com/v1",
		"",
	} {
		got, err := ReadPersonas(baseURL)
		if err != nil {
			t.Fatalf("ReadPersonas(%q) 出错: %v", baseURL, err)
		}
		if len(got) != 0 {
			t.Fatalf("ReadPersonas(%q) = %v，远端的 magpie 读不到本机文件，想要空", baseURL, personaIDs(got))
		}
	}
}

func TestReadPersonasLocalURLForms(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{
		"personas": []map[string]any{{"id": "coder", "text": "你是资深工程师"}},
	})))

	for _, baseURL := range []string{
		"http://127.0.0.1:3425/v1",
		"http://localhost:3425/v1",
		"http://[::1]:3425/v1",
		// 不带 scheme 的写法也按本机地址处理。
		"127.0.0.1:3425/v1",
	} {
		got, err := ReadPersonas(baseURL)
		if err != nil {
			t.Fatalf("ReadPersonas(%q) 出错: %v", baseURL, err)
		}
		if len(got) != 1 {
			t.Fatalf("ReadPersonas(%q) = %v，想要读到 1 条", baseURL, personaIDs(got))
		}
	}
}

func TestReadPersonasNoPluginEntry(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "没有 plugins.json",
			files: map[string]string{
				"other.json": "{}",
			},
		},
		{
			name: "插件列表里没有 persona-rules",
			files: map[string]string{
				"plugins.json": pluginsJSON(t, map[string]any{"spec": "@magpie-community/middleware-model-map@latest"}),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			usePersonaConfigDir(t, dir)
			for name, content := range tc.files {
				writePluginsFile(t, dir, name, content)
			}
			got, err := ReadPersonas(personaTestBaseURL)
			if err != nil {
				t.Fatalf("ReadPersonas 出错: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("ReadPersonas = %v，想要空", personaIDs(got))
			}
		})
	}
}

func TestReadPersonasPluginDisabled(t *testing.T) {
	cases := []struct {
		name  string
		entry map[string]any
	}{
		{
			name: "条目 off",
			entry: map[string]any{
				"spec":    "@magpie-community/middleware-persona-rules@latest",
				"off":     true,
				"options": map[string]any{"personas": []map[string]any{{"id": "coder", "text": "你是资深工程师"}}},
			},
		},
		{
			name: "options.enabled 为 false",
			entry: map[string]any{
				"spec": "@magpie-community/middleware-persona-rules@latest",
				"options": map[string]any{
					"enabled":  false,
					"personas": []map[string]any{{"id": "coder", "text": "你是资深工程师"}},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			usePersonaConfigDir(t, dir)
			writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, tc.entry))
			got, err := ReadPersonas(personaTestBaseURL)
			if err != nil {
				t.Fatalf("ReadPersonas 出错: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("ReadPersonas = %v，插件关掉时想要空", personaIDs(got))
			}
		})
	}
}

func TestReadPersonasBackupFallback(t *testing.T) {
	cases := []struct {
		name string
		// broken 为 true 时主文件是坏 JSON；否则主文件不存在。
		broken bool
	}{
		{name: "主文件坏 JSON", broken: true},
		{name: "主文件缺失", broken: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			usePersonaConfigDir(t, dir)
			good := pluginsJSON(t, personaEntry(map[string]any{
				"personas": []map[string]any{{"id": "coder", "text": "你是资深工程师"}},
			}))
			writePluginsFile(t, dir, "plugins.json.bak", good)
			if tc.broken {
				writePluginsFile(t, dir, "plugins.json", `{"plugins":[{"spec":`)
			}

			got, err := ReadPersonas(personaTestBaseURL)
			if err != nil {
				t.Fatalf("ReadPersonas 出错: %v", err)
			}
			if want := []string{"coder"}; !reflect.DeepEqual(personaIDs(got), want) {
				t.Fatalf("ReadPersonas = %v，想要从 .bak 读到 %v", personaIDs(got), want)
			}
		})
	}
}

func TestReadPersonasBadJSONWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", `not json`)

	got, err := ReadPersonas(personaTestBaseURL)
	if err != nil {
		t.Fatalf("坏配置不该当错误上报: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadPersonas = %v，想要空", personaIDs(got))
	}
}

func TestReadPersonasFiltersInvalid(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{
		"personas": []map[string]any{
			{"id": "", "text": "空 ID"},
			{"id": "有 空格", "text": "含空格"},
			{"id": "中文", "text": "非 ASCII"},
			{"id": strings.Repeat("a", 65), "text": "过长"},
			{"id": "ok-1", "text": "   "},
			{"id": "ok_2", "text": "你是资深工程师"},
		},
	})))

	got, err := ReadPersonas(personaTestBaseURL)
	if err != nil {
		t.Fatalf("ReadPersonas 出错: %v", err)
	}
	if want := []string{"ok_2"}; !reflect.DeepEqual(personaIDs(got), want) {
		t.Fatalf("ReadPersonas = %v，想要 %v", personaIDs(got), want)
	}
}

func TestReadPersonasLimit(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	options := make([]map[string]any, 0, 35)
	for i := 0; i < 35; i++ {
		options = append(options, map[string]any{
			"id":   fmt.Sprintf("p%02d", i),
			"text": "人设正文",
		})
	}
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{"personas": options})))

	got, err := ReadPersonas(personaTestBaseURL)
	if err != nil {
		t.Fatalf("ReadPersonas 出错: %v", err)
	}
	if len(got) != personaLimit {
		t.Fatalf("ReadPersonas 条数 = %d，想要 %d", len(got), personaLimit)
	}
	// 保持原顺序：前 personaLimit 条被保留。
	if got[0].ID != "p00" || got[personaLimit-1].ID != "p29" {
		t.Fatalf("ReadPersonas 顺序不对: 首条 %q、末条 %q", got[0].ID, got[personaLimit-1].ID)
	}
}

func TestReadPersonasDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePluginsFile(t, dir, "plugins.json", pluginsJSON(t, personaEntry(map[string]any{
		"personas": []map[string]any{
			{"id": "coder", "name": "第一个", "text": "你是资深工程师"},
			{"id": "coder", "name": "第二个", "text": "你是资深工程师"},
			{"id": "writer", "text": "你是技术写作"},
		},
	})))

	got, err := ReadPersonas(personaTestBaseURL)
	if err != nil {
		t.Fatalf("ReadPersonas 出错: %v", err)
	}
	if want := []string{"coder", "writer"}; !reflect.DeepEqual(personaIDs(got), want) {
		t.Fatalf("ReadPersonas = %v，想要 %v", personaIDs(got), want)
	}
	if got[0].Name != "第一个" {
		t.Fatalf("重复 ID 保留了 %q，想要第一个", got[0].Name)
	}
}
