package kanban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/store"
)

// promptCapturePlanner 记录规划器收到的目标 Prompt，用于验证落盘后仍以全文规划。
type promptCapturePlanner struct {
	prompts []string
}

func (p *promptCapturePlanner) Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error) {
	p.prompts = append(p.prompts, goal.Prompt)
	return PlanResult{Summary: "落盘测试规划", Tasks: []PlannedTask{{Title: "唯一任务"}}}, nil
}

// newGoalPromptTestService 创建带独立数据目录的看板服务。
func newGoalPromptTestService(t *testing.T, dataDir string) (*Service, *store.Store, *promptCapturePlanner) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	planner := &promptCapturePlanner{}
	svc := NewService(Options{
		Store:    db,
		Driver:   newMockDriver(),
		Projects: &mockProjects{dir: t.TempDir()},
		Planner:  planner,
		DataDir:  dataDir,
	})
	t.Cleanup(func() { svc.Close() })
	return svc, db, planner
}

// longGoalPrompt 生成超过 4096 字节的目标 Prompt。
func longGoalPrompt(prefix string) string {
	return prefix + strings.Repeat("超长目标需求描述。", 500)
}

func promptContentHash(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(h[:])
}

// TestCreateGoalMaterializesLongPrompt：超长 Prompt 落盘为 <hash>.md，库内只留摘要，
// ReadGoal 回读全文一致，规划器收到的是全文。
func TestCreateGoalMaterializesLongPrompt(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc, _, planner := newGoalPromptTestService(t, dataDir)

	prompt := longGoalPrompt("目标：")
	if len([]byte(prompt)) <= goalPromptMaterializeThreshold {
		t.Fatalf("测试 Prompt 未超过阈值: %d 字节", len([]byte(prompt)))
	}
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	hash := promptContentHash(prompt)
	path := filepath.Join(dataDir, "goals", hash+".md")

	stored, err := svc.opts.Store.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if !isMaterializedGoalPrompt(stored.Prompt) || stored.Prompt == prompt {
		t.Fatalf("库内 Prompt 应为落盘摘要，实际: %q", stored.Prompt)
	}
	if stored.ContentHash != hash {
		t.Fatalf("内容哈希不一致: %s != %s", stored.ContentHash, hash)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if string(data) != prompt {
		t.Fatal("落盘文件内容与原文不一致")
	}

	text, err := svc.ReadGoal(ctx, goal.ID, hash)
	if err != nil || text != prompt {
		t.Fatalf("ReadGoal 回读失败: %d 字节, %v", len(text), err)
	}
	if _, err := svc.ReadGoal(ctx, goal.ID, "wrong-hash"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("错误哈希应返回 ErrHashMismatch，实际: %v", err)
	}

	svc.wg.Wait()
	if len(planner.prompts) != 1 || planner.prompts[0] != prompt {
		t.Fatalf("规划器应收到全文，实际 %d 条", len(planner.prompts))
	}
}

// TestCreateGoalReusesMaterializedFile：相同内容只落盘一个文件。
func TestCreateGoalReusesMaterializedFile(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc, _, _ := newGoalPromptTestService(t, dataDir)

	prompt := longGoalPrompt("共享：")
	for i := 0; i < 2; i++ {
		if _, err := svc.CreateGoal(ctx, "proj-1", prompt); err != nil {
			t.Fatalf("创建目标失败: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dataDir, "goals"))
	if err != nil {
		t.Fatalf("读取 goals 目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != promptContentHash(prompt)+".md" {
		t.Fatalf("相同内容应只落盘一个文件，实际: %v", entries)
	}
}

// TestReadGoalRejectsTamperedMaterializedPrompt：落盘文件被篡改时读取报错。
func TestReadGoalRejectsTamperedMaterializedPrompt(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc, _, _ := newGoalPromptTestService(t, dataDir)

	prompt := longGoalPrompt("篡改：")
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	hash := promptContentHash(prompt)
	if err := os.WriteFile(filepath.Join(dataDir, "goals", hash+".md"), []byte("被篡改的内容"), 0o644); err != nil {
		t.Fatalf("篡改落盘文件失败: %v", err)
	}
	if _, err := svc.ReadGoal(ctx, goal.ID, hash); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("篡改后应返回 ErrHashMismatch，实际: %v", err)
	}
}

// TestCreateGoalKeepsShortPromptInline：未超阈值的 Prompt 不落盘。
func TestCreateGoalKeepsShortPromptInline(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc, _, _ := newGoalPromptTestService(t, dataDir)

	prompt := "短目标：只改一个按钮文案"
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	stored, err := svc.opts.Store.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if stored.Prompt != prompt || isMaterializedGoalPrompt(stored.Prompt) {
		t.Fatalf("短 Prompt 应原样存库，实际: %q", stored.Prompt)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "goals")); !os.IsNotExist(err) {
		t.Fatalf("短 Prompt 不应创建 goals 目录，实际: %v", err)
	}

	text, err := svc.ReadGoal(ctx, goal.ID, stored.ContentHash)
	if err != nil || text != prompt {
		t.Fatalf("ReadGoal: %q, %v", text, err)
	}
}

// TestCreateGoalWithoutDataDirFallsBackToInline：未配置数据目录时退化为全部存库。
func TestCreateGoalWithoutDataDirFallsBackToInline(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newGoalPromptTestService(t, "")

	prompt := longGoalPrompt("无目录：")
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	stored, err := svc.opts.Store.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if stored.Prompt != prompt || isMaterializedGoalPrompt(stored.Prompt) {
		t.Fatalf("未配置数据目录时应全部存库，实际: %q", stored.Prompt)
	}

	text, err := svc.ReadGoal(ctx, goal.ID, stored.ContentHash)
	if err != nil || text != prompt {
		t.Fatalf("ReadGoal: %d 字节, %v", len(text), err)
	}
}

// TestCreateGoalFallsBackWhenGoalDirUnwritable：落盘失败时回退为库内全文，不阻断创建。
func TestCreateGoalFallsBackWhenGoalDirUnwritable(t *testing.T) {
	ctx := context.Background()
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("准备阻塞路径失败: %v", err)
	}
	svc, _, _ := newGoalPromptTestService(t, blocker)

	prompt := longGoalPrompt("落盘失败：")
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	stored, err := svc.opts.Store.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if stored.Prompt != prompt {
		t.Fatalf("落盘失败时应回退为库内全文，实际: %q", stored.Prompt)
	}

	text, err := svc.ReadGoal(ctx, goal.ID, stored.ContentHash)
	if err != nil || text != prompt {
		t.Fatalf("ReadGoal: %d 字节, %v", len(text), err)
	}
}
