package app

import (
	"context"
	"testing"

	"mcode/internal/testenv"
)

func TestClient_BulkArchiveAndUndo(t *testing.T) {
	testenv.TempHome(t)
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	appl, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer appl.Close()

	client := appl.Client()

	projects, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("client.Projects failed: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("expected at least 1 project")
	}
	projID := projects[0].ID

	// 创建一个会话
	thID, err := client.CreateThread(ctx, projID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 批量归档该会话
	token, err := client.BulkArchive(ctx, projID, []string{thID})
	if err != nil {
		t.Fatalf("BulkArchive failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty undo token from BulkArchive")
	}

	// 撤销批量归档
	if err := client.UndoBulkOperation(ctx, projID, token); err != nil {
		t.Fatalf("UndoBulkOperation failed: %v", err)
	}

	// 验证撤销后该会话在正常会话列表中恢复且未被归档
	threads, err := client.Threads(ctx, projID)
	if err != nil {
		t.Fatalf("client.Threads failed: %v", err)
	}
	found := false
	for _, th := range threads {
		if th.ID == thID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected thread %s to be present in active threads list after undo", thID)
	}
}
