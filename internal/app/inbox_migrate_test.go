package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mcode/internal/projctx"
	"mcode/internal/store"
)

func TestMigrateLegacyInboxMemos(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	paths := []string{t.TempDir(), t.TempDir()}
	want := make(map[string]map[string]string)
	for _, path := range paths {
		svc, err := projctx.New(path)
		if err != nil {
			t.Fatal(err)
		}
		want[path] = make(map[string]string)
		for _, memo := range []struct{ title, body string }{{"标题一", "正文一"}, {"标题二", "正文二"}} {
			item, err := svc.DraftMemo(memo.body, memo.title)
			if err != nil {
				t.Fatal(err)
			}
			want[path][memo.title] = item.Content
		}
	}
	n, err := migrateLegacyInboxMemos(ctx, st, paths)
	if err != nil || n != 4 {
		t.Fatalf("迁移结果: %d, %v", n, err)
	}
	proposals, err := st.ListMemoryProposals(ctx, "", store.MemoryProposalStatusPending, 0)
	if err != nil || len(proposals) != 4 {
		t.Fatalf("提议: %d, %v", len(proposals), err)
	}
	for _, p := range proposals {
		if want[p.ProjectPath][p.Title] != p.Content || p.UnitType != "learning" || p.Source != "inbox-migration" || p.Scope != store.MemoryProposalScopeProject {
			t.Errorf("提议内容错误: %+v", p)
		}
		delete(want[p.ProjectPath], p.Title)
	}
	for _, path := range paths {
		svc, err := projctx.New(path)
		if err != nil {
			t.Fatal(err)
		}
		items, err := svc.ListInbox()
		if err != nil || len(items) != 0 || len(want[path]) != 0 {
			t.Errorf("项目 %s 收件箱或提议错误: %v, %v, %v", path, items, want[path], err)
		}
	}
	if n, err := migrateLegacyInboxMemos(ctx, st, paths); err != nil || n != 0 {
		t.Fatalf("重复迁移: %d, %v", n, err)
	}
}

func TestMigrateLegacyInboxKeepsMemoOnStoreFailure(t *testing.T) {
	path := t.TempDir()
	svc, err := projctx.New(path)
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.DraftMemo("正文", "标题")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := migrateLegacyInboxMemos(context.Background(), st, []string{path}); err == nil || n != 0 {
		t.Fatalf("关闭数据库后的迁移: %d, %v", n, err)
	}
	items, err := svc.ListInbox()
	if err != nil || len(items) != 1 || items[0].Name != item.Name || items[0].Content != item.Content {
		t.Fatalf("备忘未保留: %v, %v", items, err)
	}
}

// countInboxProposals 返回全部记忆提议的条数（不限项目与状态）。
func countInboxProposals(t *testing.T, st *store.Store) int {
	t.Helper()
	proposals, err := st.ListMemoryProposals(context.Background(), "", "", 0)
	if err != nil {
		t.Fatalf("列出提议: %v", err)
	}
	return len(proposals)
}

// TestMigrateLegacyInboxRetryAfterDiscardFailure 覆盖「建提议成功、删备忘失败」后的重试：
// 重跑不重复建提议，恢复权限后再跑一次清空收件箱且提议仍只有一条。
func TestMigrateLegacyInboxRetryAfterDiscardFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不支持通过权限位模拟收件箱删除失败")
	}
	ctx := context.Background()
	path := t.TempDir()
	svc, err := projctx.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DraftMemo("正文", "标题"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// 把 inbox 目录设为只读，删除备忘会失败；End 恢复原权限上限并再跑一次迁移。
	inbox := svc.InboxPath()
	info, err := os.Stat(inbox)
	if err != nil {
		t.Fatal(err)
	}
	origMode := info.Mode().Perm()
	if err := os.Chmod(inbox, 0o555); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if err := os.Chmod(inbox, origMode); err != nil {
			t.Fatalf("恢复权限: %v", err)
		}
	}
	t.Cleanup(restore)

	n, err := migrateLegacyInboxMemos(ctx, st, []string{path})
	if err == nil || n != 1 {
		t.Fatalf("删除失败时的迁移: %d, %v", n, err)
	}
	if got := countInboxProposals(t, st); got != 1 {
		t.Fatalf("首次迁移后的提议数: %d", got)
	}

	// 第二次：提议已存在，跳过建提议、只重试删除，且不计入 migrated。
	n, err = migrateLegacyInboxMemos(ctx, st, []string{path})
	if err == nil || n != 0 {
		t.Fatalf("重复迁移（删除仍失败）: %d, %v", n, err)
	}
	if got := countInboxProposals(t, st); got != 1 {
		t.Fatalf("重复迁移后的提议数: %d", got)
	}

	// 第三次：恢复权限后收件箱被清空，提议仍只有一条。
	restore()
	n, err = migrateLegacyInboxMemos(ctx, st, []string{path})
	if err != nil || n != 0 {
		t.Fatalf("恢复权限后的迁移: %d, %v", n, err)
	}
	items, err := svc.ListInbox()
	if err != nil || len(items) != 0 {
		t.Fatalf("收件箱未清空: %v, %v", items, err)
	}
	if got := countInboxProposals(t, st); got != 1 {
		t.Fatalf("恢复权限后的提议数: %d", got)
	}
}

// TestMigrateLegacyInboxCancelledContext 覆盖 ctx 已取消时立即返回、不写入。
func TestMigrateLegacyInboxCancelledContext(t *testing.T) {
	path := t.TempDir()
	svc, err := projctx.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DraftMemo("正文", "标题"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := migrateLegacyInboxMemos(ctx, st, []string{path})
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("取消后的迁移: %d, %v", n, err)
	}
	if got := countInboxProposals(t, st); got != 0 {
		t.Fatalf("取消后不应建提议，实际: %d", got)
	}
	items, err := svc.ListInbox()
	if err != nil || len(items) != 1 {
		t.Fatalf("取消后备忘应保留: %v, %v", items, err)
	}
}
