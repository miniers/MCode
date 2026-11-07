package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// newProjectScriptsTestBackend 构造项目脚本测试用的 MemoryBackend；dataDir 非空时项目脚本落到磁盘。
func newProjectScriptsTestBackend(t *testing.T, dataDir string) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(func() { hub.Close() })
	b := NewMemoryBackend(hub)
	t.Cleanup(func() { b.Close() })
	if dataDir != "" {
		b.SetDataDir(dataDir)
	}
	return b
}

// addProjectScriptsTestThread 登记项目与其会话（项目路径指向临时目录），返回项目工作目录。
func addProjectScriptsTestThread(t *testing.T, b *MemoryBackend, projectID, threadID string) string {
	t.Helper()
	workDir := t.TempDir()
	b.AddProject(wire.ProjectSummary{ID: projectID, Name: projectID, Path: workDir})
	b.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: projectID, Title: threadID}, nil)
	return workDir
}

// 保存时补全 ID、去掉名称与命令的首尾空白；名称或命令为空、名称重复返回错误且不覆盖已有脚本；
// scripts.list 的 projectScripts 能看到该脚本。
func TestProjectScriptsSaveNormalizeAndList(t *testing.T) {
	ctx := context.Background()
	b := newProjectScriptsTestBackend(t, "")
	addProjectScriptsTestThread(t, b, "p1", "t1")

	if err := b.SetProjectScripts("t1", []ProjectScript{{Name: "  lint  ", Command: "  make lint  "}}); err != nil {
		t.Fatalf("save project scripts: %v", err)
	}
	scripts, err := b.ProjectScripts("t1")
	if err != nil {
		t.Fatalf("read project scripts: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("expected 1 project script, got %+v", scripts)
	}
	if scripts[0].ID == "" {
		t.Fatalf("保存时应补全脚本 ID: %+v", scripts[0])
	}
	if scripts[0].Name != "lint" || scripts[0].Command != "make lint" {
		t.Fatalf("名称与命令应去掉首尾空白, got %+v", scripts[0])
	}
	scriptID := scripts[0].ID

	for _, tc := range []struct {
		name    string
		scripts []ProjectScript
	}{
		{"名称为空", []ProjectScript{{Name: "   ", Command: "make lint"}}},
		{"命令为空", []ProjectScript{{Name: "lint", Command: "   "}}},
		{"名称重复", []ProjectScript{
			{Name: "lint", Command: "make lint"},
			{Name: " lint ", Command: "make lint2"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.SetProjectScripts("t1", tc.scripts); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}

	// 校验失败不应覆盖已保存的脚本
	afterFail, err := b.ProjectScripts("t1")
	if err != nil {
		t.Fatalf("read project scripts after failed saves: %v", err)
	}
	if len(afterFail) != 1 || afterFail[0].ID != scriptID || afterFail[0].Name != "lint" {
		t.Fatalf("校验失败后已有脚本应保持不变, got %+v", afterFail)
	}

	// scripts.list 的 projectScripts 里出现该脚本，ID 与名称一致
	res, err := b.ScriptsList(ctx, wire.ScriptsListParams{ThreadID: "t1"})
	if err != nil {
		t.Fatalf("scripts.list: %v", err)
	}
	if len(res.ProjectScripts) != 1 {
		t.Fatalf("expected 1 project script in scripts.list, got %+v", res.ProjectScripts)
	}
	if res.ProjectScripts[0].ID != scriptID || res.ProjectScripts[0].Name != "lint" {
		t.Fatalf("scripts.list projectScripts mismatch: %+v (want id=%s name=lint)", res.ProjectScripts, scriptID)
	}
}

// 有 dataDir 时写入 <dataDir>/remote/project-scripts.json（以项目 ID 为键），
// 用同一 dataDir 的新 backend 能读回；不同项目的脚本互不影响。
func TestProjectScriptsPersistenceAndProjectIsolation(t *testing.T) {
	dataDir := t.TempDir()
	b := newProjectScriptsTestBackend(t, dataDir)
	addProjectScriptsTestThread(t, b, "p1", "t1")
	addProjectScriptsTestThread(t, b, "p2", "t2")

	if err := b.SetProjectScripts("t1", []ProjectScript{{Name: "lint", Command: "make lint"}}); err != nil {
		t.Fatalf("save p1 scripts: %v", err)
	}
	if err := b.SetProjectScripts("t2", []ProjectScript{{Name: "build", Command: "make build"}}); err != nil {
		t.Fatalf("save p2 scripts: %v", err)
	}

	p1Scripts, err := b.ProjectScripts("t1")
	if err != nil {
		t.Fatalf("read p1 scripts: %v", err)
	}
	p2Scripts, err := b.ProjectScripts("t2")
	if err != nil {
		t.Fatalf("read p2 scripts: %v", err)
	}
	if len(p1Scripts) != 1 || p1Scripts[0].Name != "lint" {
		t.Fatalf("p1 scripts polluted by another project: %+v", p1Scripts)
	}
	if len(p2Scripts) != 1 || p2Scripts[0].Name != "build" {
		t.Fatalf("p2 scripts polluted by another project: %+v", p2Scripts)
	}

	// 磁盘文件以项目 ID 为键
	path := filepath.Join(dataDir, "remote", projectScriptsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted project scripts: %v", err)
	}
	var stored map[string][]ProjectScript
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("invalid json in %s: %v", path, err)
	}
	if len(stored["p1"]) != 1 || stored["p1"][0].Name != "lint" {
		t.Fatalf("persisted p1 scripts mismatch: %+v", stored["p1"])
	}
	if len(stored["p2"]) != 1 || stored["p2"][0].Name != "build" {
		t.Fatalf("persisted p2 scripts mismatch: %+v", stored["p2"])
	}

	// 同一 dataDir 的新 backend 能读回同样的脚本（项目按 ID 对齐，会话 ID 可以不同）
	reopened := newProjectScriptsTestBackend(t, dataDir)
	addProjectScriptsTestThread(t, reopened, "p1", "t1-reopened")
	reloaded, err := reopened.ProjectScripts("t1-reopened")
	if err != nil {
		t.Fatalf("read project scripts from new backend: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].ID != p1Scripts[0].ID || reloaded[0].Name != p1Scripts[0].Name || reloaded[0].Command != p1Scripts[0].Command {
		t.Fatalf("reloaded scripts mismatch: got %+v, want %+v", reloaded, p1Scripts)
	}
}
