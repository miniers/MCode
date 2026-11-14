package scripts_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"mcode/internal/store"
	"mcode/internal/swarm"
)

func TestUISandboxSeedIsIdle(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "ui_dev_sandbox.sh", "--seed-only")
	cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("灌数据失败: %v\n%s", err, out)
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	turns, err := db.ListUnfinishedTurns(ctx)
	if err != nil || len(turns) != 0 {
		t.Fatalf("存在未完成轮次: %v, %v", turns, err)
	}
	orphans, err := db.ListOrphanRunningToolMessages(ctx, nil)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("存在运行中的工具消息: %v, %v", orphans, err)
	}
	threads, err := db.ListThreads(ctx, "ui-demo", true)
	if err != nil || len(threads) == 0 {
		t.Fatalf("读取演示会话失败: %v, %v", threads, err)
	}
	for _, thread := range threads {
		if thread.LastStatus == "running" || thread.LastStatus == "starting" {
			t.Errorf("会话 %s 状态为 %s", thread.ID, thread.LastStatus)
		}
		followUps, err := db.ListFollowUps(ctx, thread.ID)
		if err != nil || len(followUps) != 0 {
			t.Errorf("会话 %s 存在排队消息: %v, %v", thread.ID, followUps, err)
		}
	}
	leads, err := db.ListSwarmLeads(ctx)
	if err != nil || len(leads) == 0 {
		t.Fatalf("读取主会话失败: %v, %v", leads, err)
	}
	for _, lead := range leads {
		var queue []json.RawMessage
		if err := json.Unmarshal([]byte(lead.QueueJSON), &queue); err != nil || len(queue) != 0 {
			t.Errorf("主会话 %s 存在排队任务: %v, %v", lead.LeadThreadID, queue, err)
		}
		var bees []swarm.Bee
		if err := json.Unmarshal([]byte(lead.BeesJSON), &bees); err != nil || len(bees) == 0 {
			t.Fatalf("读取子代理失败: %v, %v", bees, err)
		}
		for _, bee := range bees {
			if bee.Status != swarm.StatusCompleted || !bee.Reported || bee.Interrupted || bee.PendingMerge || bee.ReviewState != "" || bee.ReviewStage != "" || (bee.Merged && !bee.Delivered) {
				t.Errorf("子代理 %s 未处于安全的完成状态: %+v", bee.ThreadID, bee)
			}
		}
	}
}

func TestUISandboxShowcaseSeed(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		cmd := exec.Command("bash", "ui_dev_sandbox.sh", "--showcase", "--seed-only")
		cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("第 %d 次灌展示数据失败: %v\n%s", i+1, err, out)
		}
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projects, err := db.ListProjects(ctx)
	if err != nil || len(projects) != 3 {
		t.Fatalf("项目数=%d，错误=%v", len(projects), err)
	}
	const (
		orbitProjectID  = "7b0c2e1a-4f3d-4c8e-9a51-000000000001"
		lumenProjectID  = "7b0c2e1a-4f3d-4c8e-9a51-000000000002"
		pocketProjectID = "7b0c2e1a-4f3d-4c8e-9a51-000000000003"
		paginationID    = "7b0c2e1a-4f3d-4c8e-9a51-000000000011"
		swarmID         = "7b0c2e1a-4f3d-4c8e-9a51-000000000012"
		birchThreadID   = "7b0c2e1a-4f3d-4c8e-9a51-000000000042"
	)
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	projectIDs := map[string]bool{orbitProjectID: true, lumenProjectID: true, pocketProjectID: true}
	for _, project := range projects {
		if !uuidPattern.MatchString(project.ID) || !projectIDs[project.ID] {
			t.Errorf("项目 ID 不是预期 UUID: %s", project.ID)
		}
	}
	messages, _, err := db.ListMessages(ctx, paginationID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var hasUser, hasAssistant, hasTool, hasCode, hasEdit bool
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			hasUser = true
		case "assistant":
			hasAssistant = true
			hasCode = hasCode || strings.Contains(msg.Text, "```go")
		case "tool":
			hasTool = true
			hasEdit = hasEdit || msg.ToolName != nil && *msg.ToolName == "Edit"
		}
	}
	if !hasUser || !hasAssistant || !hasTool || !hasCode || !hasEdit {
		t.Fatalf("主角线程消息不完整: user=%v assistant=%v tool=%v code=%v edit=%v", hasUser, hasAssistant, hasTool, hasCode, hasEdit)
	}
	leads, err := db.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundLead bool
	for _, lead := range leads {
		if lead.LeadThreadID != swarmID {
			continue
		}
		foundLead = true
		var bees []swarm.Bee
		if err := json.Unmarshal([]byte(lead.BeesJSON), &bees); err != nil {
			t.Fatal(err)
		}
		if len(bees) < 3 {
			t.Fatalf("子代理不足: %d", len(bees))
		}
		for _, bee := range bees {
			if bee.Status != swarm.StatusCompleted || !bee.Reported {
				t.Errorf("子代理未完成汇报: %+v", bee)
			}
			if !uuidPattern.MatchString(bee.ThreadID) || !uuidPattern.MatchString(bee.ProjectID) {
				t.Errorf("子代理 ID 不是 UUID: %+v", bee)
			}
		}
		for i, name := range []string{"Aspen", "Birch", "Cedar"} {
			if bees[i].Name != name {
				t.Errorf("子代理 %d 名称=%q，期望=%q", i+1, bees[i].Name, name)
			}
		}
		for _, bee := range bees {
			usages, err := db.ListTurnUsage(ctx, bee.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			var tokens int64
			for _, u := range usages {
				tokens += u.InputTokens + u.OutputTokens
			}
			if tokens <= 0 {
				t.Errorf("子代理 %s 缺少累计 token 用量", bee.Name)
			}
		}
	}
	if !foundLead {
		t.Fatal("缺少编排主线程记录")
	}
	birchMessages, _, err := db.ListMessages(ctx, birchThreadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	birchToolCount := 0
	birchHasEdit := false
	for _, msg := range birchMessages {
		if msg.Role != "tool" {
			continue
		}
		birchToolCount++
		birchHasEdit = birchHasEdit || msg.ToolName != nil && *msg.ToolName == "Edit"
	}
	if birchToolCount < 3 || !birchHasEdit {
		t.Fatalf("Birch 子线程工具消息不完整: count=%d edit=%v", birchToolCount, birchHasEdit)
	}
	if value, ok, err := db.GetSetting(ctx, "demo_seeded"); err != nil || !ok || value != "1" {
		t.Fatalf("demo_seeded 设置异常: value=%q, ok=%v, err=%v", value, ok, err)
	}
	hits, err := db.SearchMessages(ctx, "分页", 100)
	if err != nil {
		t.Fatal(err)
	}
	threadsWithPagination := map[string]bool{}
	for _, hit := range hits {
		threadsWithPagination[hit.ThreadID] = true
	}
	if len(threadsWithPagination) < 3 {
		t.Fatalf("分页消息分布在线程数=%d", len(threadsWithPagination))
	}
	var cfg struct {
		Remote struct {
			Enabled bool   `json:"enabled"`
			Listen  string `json:"listen"`
		} `json:"remote"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Remote.Enabled || cfg.Remote.Listen != "127.0.0.1:43117" {
		t.Fatalf("remote 配置异常: %+v", cfg.Remote)
	}
	turns, err := db.ListUnfinishedTurns(ctx)
	if err != nil || len(turns) != 0 {
		t.Fatalf("存在未完成轮次: %v, %v", turns, err)
	}
	orphans, err := db.ListOrphanRunningToolMessages(ctx, nil)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("存在运行中的工具消息: %v, %v", orphans, err)
	}
	for _, projectID := range []string{orbitProjectID, lumenProjectID, pocketProjectID} {
		threads, err := db.ListThreads(ctx, projectID, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range threads {
			if !uuidPattern.MatchString(thread.ID) || !uuidPattern.MatchString(thread.ProjectID) || (thread.ParentThreadID != "" && !uuidPattern.MatchString(thread.ParentThreadID)) {
				t.Errorf("线程 ID 不是 UUID: %+v", thread)
			}
			if thread.LastStatus == "running" || thread.LastStatus == "starting" {
				t.Errorf("线程 %s 状态为 %s", thread.ID, thread.LastStatus)
			}
			followUps, err := db.ListFollowUps(ctx, thread.ID)
			if err != nil || len(followUps) != 0 {
				t.Errorf("线程 %s 有排队消息: %v, %v", thread.ID, followUps, err)
			}
		}
	}
}

func TestUISandboxShowcaseTheme(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "ui_dev_sandbox.sh", "--showcase", "--theme", "light", "--seed-only")
	cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("灌数据失败: %v\n%s", err, out)
	}
	var cfg struct {
		Appearance struct {
			Theme string `json:"theme"`
		} `json:"appearance"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Appearance.Theme != "light" {
		t.Fatalf("主题=%q，期望 light", cfg.Appearance.Theme)
	}
	cmd = exec.Command("bash", "ui_dev_sandbox.sh", "--theme", "bogus", "--seed-only")
	cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("非法主题应失败: %s", out)
	}
}

func TestUISandboxAllowsSystemTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 /tmp")
	}
	dir, err := os.MkdirTemp("/tmp", "mcode-ui-sandbox-test.")
	if err != nil {
		t.Fatalf("创建 /tmp 目录失败: %v", err)
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command("bash", "ui_dev_sandbox.sh", "--seed-only")
	cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("在 /tmp 下灌数据失败: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "mcode.db")); err != nil {
		t.Fatalf("缺少数据库: %v", err)
	}
}

func TestUISandboxRejectsUnknownArgument(t *testing.T) {
	cmd := exec.Command("bash", "ui_dev_sandbox.sh", "--unknown")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("未知参数应失败: %s", out)
	}
}

func TestUISandbox(t *testing.T) {
	script, err := filepath.Abs("ui_dev_sandbox.sh")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("语法检查失败: %v\n%s", err, out)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// 用户主目录可能本身就在临时目录内（例如隔离测试环境），因此用临时目录的父目录
	// 构造一个确实位于临时目录之外的路径。
	for _, dir := range []string{filepath.Join(home, ".mcode"), filepath.Join(filepath.Dir(os.TempDir()), "mcode-ui-sandbox-test-not-created")} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			before, statErr := os.Stat(dir)
			cmd := exec.Command("bash", script, "--seed-only")
			cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("应拒绝目录 %s: %s", dir, out)
			}
			after, afterErr := os.Stat(dir)
			if os.IsNotExist(statErr) && !os.IsNotExist(afterErr) {
				t.Fatalf("意外创建目录 %s", dir)
			}
			if statErr == nil && (afterErr != nil || !before.ModTime().Equal(after.ModTime())) {
				t.Fatalf("目录发生改变: %s", dir)
			}
		})
	}
	dir := t.TempDir()
	cmd := exec.Command("bash", script, "--seed-only")
	cmd.Env = append(os.Environ(), "MCODE_UI_SANDBOX_DIR="+dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("灌数据失败: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("输出目录 = %q，期望 %q", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "mcode.db")); err != nil {
		t.Fatalf("缺少数据库: %v", err)
	}

	cmd = exec.Command("go", "run", "./ui_dev_seed", "-data", filepath.Join(filepath.Dir(os.TempDir()), "mcode-ui-sandbox-test-not-created"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("种子程序应拒绝非临时目录: %s", out)
	}
}
