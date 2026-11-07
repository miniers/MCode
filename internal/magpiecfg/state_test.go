package magpiecfg

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"runtime"
	"testing"
)

func TestStatePermissionsAndRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不保留 POSIX 权限")
	}
	dataDir := t.TempDir()
	stash := &stashFile{Files: map[string]map[string]fieldState{
		"/tmp/settings.json": {
			"model":                  {Value: "user-model", Had: true},
			"env.ANTHROPIC_MODEL":    {},
			"env.ANTHROPIC_BASE_URL": {Value: "https://old.test", Had: true},
		},
	}}
	if err := saveStash(dataDir, ClaudeAgent, stash); err != nil {
		t.Fatalf("saveStash: %v", err)
	}
	dirInfo, err := os.Stat(stateDir(dataDir, ClaudeAgent))
	if err != nil {
		t.Fatalf("状态目录: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("状态目录权限 = %o，想要 700", got)
	}
	fileInfo, err := os.Stat(stashPath(dataDir, ClaudeAgent))
	if err != nil {
		t.Fatalf("stash 文件: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("stash 文件权限 = %o，想要 600", got)
	}
	loaded, err := loadStash(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadStash: %v", err)
	}
	if !reflect.DeepEqual(loaded, stash) {
		t.Fatalf("loadStash = %+v，想要 %+v", loaded, stash)
	}

	applied := &appliedFile{
		Files: map[string]appliedTarget{
			"/tmp/settings.json": {
				Created: true,
				Fields:  map[string]fieldState{"model": {Value: "gateway-model", Had: true}},
			},
		},
	}
	if err := saveApplied(dataDir, ClaudeAgent, applied); err != nil {
		t.Fatalf("saveApplied: %v", err)
	}
	fileInfo, err = os.Stat(appliedPath(dataDir, ClaudeAgent))
	if err != nil {
		t.Fatalf("applied 文件: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("applied 文件权限 = %o，想要 600", got)
	}
	loadedApplied, err := loadApplied(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	if !reflect.DeepEqual(loadedApplied, applied) {
		t.Fatalf("loadApplied = %+v，想要 %+v", loadedApplied, applied)
	}
}

func TestLoadStateMissingReturnsEmpty(t *testing.T) {
	dataDir := t.TempDir()
	stash, err := loadStash(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadStash: %v", err)
	}
	if stash == nil || stash.Files == nil || len(stash.Files) != 0 {
		t.Fatalf("缺失的 stash 应该读成空结构，得到 %+v", stash)
	}
	applied, err := loadApplied(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	if applied == nil || len(applied.Files) != 0 || applied.Created || applied.Kept {
		t.Fatalf("缺失的 applied 应该读成空结构，得到 %+v", applied)
	}
}

func TestApplied(t *testing.T) {
	for _, tt := range []struct {
		name    string
		state   string
		want    bool
		wantErr bool
	}{
		{name: "missing"},
		{name: "empty", state: " "},
		{name: "no files", state: `{}`},
		{name: "null", state: `null`},
		{name: "applied", state: `{"files":{"/tmp/settings.json":{"fields":{}}}}`, want: true},
		{name: "legacy", state: `{"path":"/tmp/settings.json","fields":{}}`, want: true},
		{name: "kept record", state: `{"kept":true,"files":{"/tmp/settings.json":{}}}`, want: true},
		{name: "invalid", state: `{`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if tt.state != "" {
				writeTestFile(t, appliedPath(dataDir, ClaudeAgent), tt.state)
			}
			got, err := Applied(dataDir, ClaudeAgent)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("Applied = %v, %v，想要 %v，错误 %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestAppliedRejectsInvalidAgent(t *testing.T) {
	if _, err := Applied(t.TempDir(), "../claude"); err == nil {
		t.Fatal("非法 agent 应返回错误")
	}
}

func TestRemoveState(t *testing.T) {
	dataDir := t.TempDir()
	if err := saveStash(dataDir, ClaudeAgent, &stashFile{}); err != nil {
		t.Fatalf("saveStash: %v", err)
	}
	if err := saveApplied(dataDir, ClaudeAgent, &appliedFile{Files: map[string]appliedTarget{
		"/tmp/settings.json": {Format: string(FormatJSON)},
	}}); err != nil {
		t.Fatalf("saveApplied: %v", err)
	}
	if err := removeState(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("removeState: %v", err)
	}
	for _, path := range []string{stashPath(dataDir, ClaudeAgent), appliedPath(dataDir, ClaudeAgent)} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s 应该已被删除，err = %v", path, err)
		}
	}
}

func TestLoadOldFormatState(t *testing.T) {
	dataDir := t.TempDir()
	// 旧版本把 GetJSON 的结果直接存成字符串，没有引号。
	writeTestFile(t, stashPath(dataDir, ClaudeAgent), `{"fields":{"model":{"value":"user-model","had":true},"env.NO_PROXY":{}}}`)
	writeTestFile(t, appliedPath(dataDir, ClaudeAgent), `{"path":"/tmp/settings.json","created":false,"kept":false,"fields":{"model":{"value":"gateway-model","had":true}}}`)
	stash, err := loadStash(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadStash: %v", err)
	}
	if got := stash.Files["/tmp/settings.json"]["model"]; !reflect.DeepEqual(got, fieldState{Value: "user-model", Had: true}) {
		t.Fatalf("旧格式 stash 的 model = %+v", got)
	}
	if got := stash.Files["/tmp/settings.json"]["env.NO_PROXY"]; !reflect.DeepEqual(got, fieldState{}) {
		t.Fatalf("旧格式 stash 的删除记录 = %+v", got)
	}
	applied, err := loadApplied(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	if got := applied.Files["/tmp/settings.json"].Fields["model"]; !reflect.DeepEqual(got, fieldState{Value: "gateway-model", Had: true}) {
		t.Fatalf("旧格式 applied 的 model = %+v", got)
	}
}
