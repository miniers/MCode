package kanban

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestCheckWaits(t *testing.T) {
	for _, tc := range []struct{ name, command string; wakes int; want string }{
		{"成功", "true", 0, "todo"}, {"退避", "false", 0, "waiting"},
		{"超时", "false", 6, "needs_approval"}, {"写命令", "rm marker", 0, "needs_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
			dir := svc.opts.Projects.(*mockProjects).dir
			marker := filepath.Join(dir, "marker")
			if err := os.WriteFile(marker, []byte("保留"), 0600); err != nil { t.Fatal(err) }
			now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
			project := "p"
			meta := TaskMeta{WaitKind:"machine", WaitCheck:tc.command, WakeAt:now.Format(time.RFC3339), Wakes:tc.wakes, Summary:"等待数据"}
			if err := db.CreateTask(ctx, store.Task{ID:"t", ProjectID:&project, Title:"等待", Status:"waiting", Meta:EncodeTaskMeta(meta)}); err != nil { t.Fatal(err) }
			if err := svc.CheckWaits(ctx, now); err != nil { t.Fatal(err) }
			task, err := db.GetTask(ctx, "t"); if err != nil { t.Fatal(err) }
			if task.Status != tc.want { t.Fatalf("状态 %s", task.Status) }
			m, err := ParseTaskMeta(task.Meta); if err != nil { t.Fatal(err) }
			if tc.name == "退避" && (m.Wakes != 1 || m.WakeAt != now.Add(10*time.Minute).Format(time.RFC3339)) { t.Fatalf("退避数据 %#v", m) }
			if tc.want == "needs_approval" { decisions, err := db.ListStewardDecisions(ctx, "open", 0); if err != nil || len(decisions) != 1 { t.Fatalf("决策卡 %#v %v", decisions, err) } }
			if _, err := os.Stat(marker); err != nil { t.Fatalf("文件被删除: %v", err) }
		})
	}
}
