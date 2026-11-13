package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func checkSandboxDir(path, temp, systemTemp, defaultDir string) error {
	if !(inside(path, temp) || inside(path, systemTemp)) || path == temp || path == systemTemp || inside(path, defaultDir) {
		dirs := temp
		if systemTemp != temp {
			dirs += " 或 " + systemTemp
		}
		return fmt.Errorf("数据目录必须位于系统临时目录内（%s），且不能位于默认数据目录", dirs)
	}
	return nil
}

func run() error {
	data := flag.String("data", "", "临时数据目录")
	showcase := flag.Bool("showcase", false, "灌入展示数据")
	theme := flag.String("theme", "", "外观主题：light、dark 或空（不改）")
	flag.Parse()
	if *data == "" {
		return errors.New("必须指定数据目录")
	}
	path, err := filepath.EvalSymlinks(*data)
	if err != nil {
		return fmt.Errorf("解析数据目录: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultDir := filepath.Join(home, ".mcode")
	if resolved, e := filepath.EvalSymlinks(defaultDir); e == nil {
		defaultDir = resolved
	}
	systemTemp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		systemTemp = temp
	}
	if err := checkSandboxDir(path, temp, systemTemp, defaultDir); err != nil {
		return err
	}
	for _, name := range []string{"mcode.db", "mcode.db-wal", "mcode.db-shm", "config.json"} {
		if info, e := os.Lstat(filepath.Join(path, name)); e == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("不允许使用特殊文件: %s", name)
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(path, "mcode.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	autos, err := db.ListAutomations(ctx, "")
	if err != nil {
		return err
	}
	if len(autos) != 0 {
		return errors.New("沙箱目录包含自动化任务，拒绝启动")
	}
	cfg := config.Defaults()
	cfg.Remote.Enabled = false
	if *showcase {
		cfg.Remote.Enabled = true
		cfg.Remote.Listen = "127.0.0.1:43117"
	}
	disabled := false
	cfg.Automation.PreventSleep = &disabled
	if *theme != "" {
		switch *theme {
		case config.ThemeLight, config.ThemeDark:
			cfg.Appearance.Theme = *theme
		default:
			return fmt.Errorf("非法主题：%s（可选 light 或 dark）", *theme)
		}
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	if *showcase {
		return seedShowcase(ctx, db, path)
	}
	project := store.Project{ID: "ui-demo", Name: "UI 演示", Path: path}
	if err := db.UpsertProject(ctx, project); err != nil {
		return err
	}
	now := time.Now().UTC()
	threads := []struct{ id, title, text string }{
		{"demo-code", "演示：长代码块", "这是一段用于检查正文换行、段落间距与不同窗口宽度下阅读体验的演示文字。" + strings.Repeat("界面应当保持清晰、稳定，并让长内容自然换行。", 8) + "\n\n```go\nfunc renderExample() {\n    " + strings.Repeat("longIdentifier", 10) + " := \"用于检查横向滚动的长代码行\"\n}\n```"},
		{"demo-tools", "演示：工具调用", "下面展示成功和失败的工具调用。"},
		{"demo-diff", "演示：Diff", "```diff\n--- a/example.go\n+++ b/example.go\n@@ -1,2 +1,2 @@\n-旧内容\n+新内容\n```"},
		{"demo-swarm", "演示：多代理", "两个子代理分别检查布局和交互，已完成各自的演示汇报。"},
	}
	for _, item := range threads {
		if _, err := db.GetThread(ctx, item.id); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := db.CreateThread(ctx, store.Thread{ID: item.id, ProjectID: project.ID, Title: item.title, Provider: "memory", CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		for i, msg := range []store.Message{{ID: item.id + "-user", ThreadID: item.id, Role: "user", Kind: "text", Text: "请展示这个界面。", TurnIndex: 1}, {ID: item.id + "-assistant", ThreadID: item.id, Role: "assistant", Kind: "text", Text: item.text, TurnIndex: 1, IsFinal: true}} {
			msg.Seq = int64(i + 1)
			if _, err := db.AppendMessage(ctx, msg); err != nil {
				return err
			}
		}
	}
	for i, status := range []string{"completed", "failed"} {
		name, input, output := "bash", `{"command":"演示命令"}`, "演示输出："+status
		_, err := db.AppendMessage(ctx, store.Message{ID: fmt.Sprintf("demo-tool-%d", i), ThreadID: "demo-tools", Role: "tool", Kind: "tool", Text: output, TurnIndex: 1, ToolName: &name, ToolInput: &input, ToolStatus: &status})
		if err != nil && !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return err
		}
	}
	bees := []swarm.Bee{{ThreadID: "demo-bee-layout", Index: 1, Name: "布局", Task: "检查布局", Status: "completed", Reported: true, Summary: "布局检查完成", ProjectID: project.ID, StartedAt: now}, {ThreadID: "demo-bee-interaction", Index: 2, Name: "交互", Task: "检查交互", Status: "completed", Reported: true, Summary: "交互检查完成", ProjectID: project.ID, StartedAt: now}}
	payload, err := json.Marshal(bees)
	if err != nil {
		return err
	}
	if err := db.SaveSwarmLead(ctx, store.SwarmLeadRecord{LeadThreadID: "demo-swarm", Enabled: true, ProjectID: project.ID, LeadWorkDir: path, NextIndex: 3, BeesJSON: string(payload)}); err != nil {
		return err
	}
	for _, bee := range bees {
		if _, err := db.GetThread(ctx, bee.ThreadID); errors.Is(err, store.ErrNotFound) {
			if err := db.CreateThread(ctx, store.Thread{ID: bee.ThreadID, ProjectID: project.ID, ParentThreadID: "demo-swarm", Title: "演示：" + bee.Name, Provider: "memory", IsHelper: true}); err != nil {
				return err
			}
			if _, err := db.AppendMessage(ctx, store.Message{ID: bee.ThreadID + "-report", ThreadID: bee.ThreadID, Role: "assistant", Kind: "text", Text: bee.Summary, TurnIndex: 1, IsFinal: true}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := db.SaveSwarmReport(ctx, store.SwarmReport{BeeID: bee.ThreadID, LeadThreadID: "demo-swarm", Name: bee.Name, Task: bee.Task, Summary: bee.Summary}); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "沙箱灌数据失败：", err)
		os.Exit(1)
	}
}
