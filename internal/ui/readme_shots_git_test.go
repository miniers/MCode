package mcodeui

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// readmeGitShowcaseClient 在 readmeShowcaseClient 基础上补充 Git 面板、历史、改动与分支 PR 展示数据。
type readmeGitShowcaseClient struct {
	*readmeShowcaseClient
	entries []git.StatusEntry
	commits []git.LogCommit
	pr      *git.PRSummary
}

var (
	_ gitPaneClient     = (*readmeGitShowcaseClient)(nil)
	_ gitChangesClient  = (*readmeGitShowcaseClient)(nil)
	_ gitHistoryClient  = (*readmeGitShowcaseClient)(nil)
	_ gitPRBranchClient = (*readmeGitShowcaseClient)(nil)
	_ reviewClient      = (*readmeGitShowcaseClient)(nil)
)

func (c *readmeGitShowcaseClient) GitStatusEntries(context.Context, string) ([]git.StatusEntry, error) {
	return append([]git.StatusEntry(nil), c.entries...), nil
}

func (c *readmeGitShowcaseClient) GitPullWith(context.Context, string, string) error { return nil }
func (c *readmeGitShowcaseClient) GitPushForceWithLease(context.Context, string) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitOpState(context.Context, string) (git.OpState, error) {
	return git.OpState{}, nil
}
func (c *readmeGitShowcaseClient) GitContinueOp(context.Context, string) error { return nil }
func (c *readmeGitShowcaseClient) GitSkipOp(context.Context, string) error     { return nil }
func (c *readmeGitShowcaseClient) GitAbortOp(context.Context, string) error    { return nil }
func (c *readmeGitShowcaseClient) GitRestoreSnapshot(context.Context, string, *git.Snapshot) error {
	return nil
}

func (c *readmeGitShowcaseClient) GitStageFiles(context.Context, string, []string) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitUnstageFiles(context.Context, string, []string) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitDiscardFiles(context.Context, string, []string) (*git.Snapshot, error) {
	return nil, nil
}
func (c *readmeGitShowcaseClient) GitCommitIndex(context.Context, string, string, bool) (string, error) {
	return "b4e82d1", nil
}
func (c *readmeGitShowcaseClient) GitLastCommitMessage(context.Context, string) (string, error) {
	return "perf(order): 为订单状态与创建时间增加联合索引", nil
}
func (c *readmeGitShowcaseClient) GitHeadPushed(context.Context, string) (bool, error) {
	return false, nil
}

func (c *readmeGitShowcaseClient) GitLog(_ context.Context, _ string, opts git.LogOptions) ([]git.LogCommit, error) {
	if opts.Skip >= len(c.commits) {
		return nil, nil
	}
	end := len(c.commits)
	if opts.Limit > 0 && opts.Skip+opts.Limit < end {
		end = opts.Skip + opts.Limit
	}
	out := make([]git.LogCommit, end-opts.Skip)
	copy(out, c.commits[opts.Skip:end])
	return out, nil
}

func (c *readmeGitShowcaseClient) GitShowCommit(_ context.Context, _ string, sha string) (git.CommitDetail, error) {
	for _, item := range c.commits {
		if item.SHA == sha {
			return git.CommitDetail{
				LogCommit: item,
				Body:      item.Subject,
				Files: []git.CommitFile{
					{Path: "internal/order/repository.go", Status: "M", Added: 14, Deleted: 6},
				},
			}, nil
		}
	}
	return git.CommitDetail{LogCommit: git.LogCommit{SHA: sha}}, nil
}

func (c *readmeGitShowcaseClient) GitCommitDiff(context.Context, string, string, string) ([]git.FileDiff, error) {
	return nil, nil
}
func (c *readmeGitShowcaseClient) GitCreateBranchAt(context.Context, string, string, string, bool) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitTagCreate(context.Context, string, string, string, string) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitCherryPick(context.Context, string, string) error { return nil }
func (c *readmeGitShowcaseClient) GitRevertCommit(context.Context, string, string) error {
	return nil
}
func (c *readmeGitShowcaseClient) GitReset(context.Context, string, string, string) error {
	return nil
}

func (c *readmeGitShowcaseClient) GitPRForBranch(context.Context, string) (*git.PRSummary, error) {
	if c.pr == nil {
		return nil, nil
	}
	cp := *c.pr
	return &cp, nil
}

func newReadmeGitShowcaseClient(t *testing.T) *readmeGitShowcaseClient {
	t.Helper()
	base := newReadmeShowcaseClient()
	workDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Minute)

	base.mu.Lock()
	base.gitState.WorkDir = workDir
	base.gitState.RemoteURL = "https://github.com/orbit-cloud/orbit-api.git"
	base.mu.Unlock()
	base.SetGenerateCommitMessageResult("feat(order): 订单列表改用复合游标分页并补全边界单测", nil)

	const repoOld = `package order

func (r *Repo) List(page, n int) ([]Order, error) {
	offset := max(0, (page-1)*n)
	return r.queryOrders(offset, n)
}
`

	const repoNew = `package order

func (r *Repo) List(after string, n int) (Page, error) {
	cur, err := DecodeCursor(after)
	if err != nil {
		return Page{}, err
	}
	rows, err := r.queryByCursor(cur, n+1)
	if err != nil {
		return Page{}, err
	}
	if len(rows) <= n {
		return Page{Items: rows}, nil
	}
	next := EncodeCursor(rows[n-1])
	return Page{Items: rows[:n], Next: next}, nil
}
`

	const cursorNew = `package order

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

type Cursor struct {
	CreatedAt time.Time ` + "`json:\"created_at\"`" + `
	ID        string    ` + "`json:\"id\"`" + `
}

func EncodeCursor(createdAt time.Time, id string) string {
	raw, _ := json.Marshal(Cursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, err
	}
	var cur Cursor
	return cur, json.Unmarshal(raw, &cur)
}
`

	const testOld = `package order

import (
	"context"
	"testing"
)

func TestListOrdersPagination(t *testing.T) {
	repo := newOrderRepo(t)
	items, err := repo.ListOrders(context.Background(), 1, 20)
	if err != nil || len(items) == 0 {
		t.Fatalf("unexpected result: %v", err)
	}
}
`

	const testNew = `package order

import (
	"context"
	"testing"
)

func TestListOrdersCursorBoundary(t *testing.T) {
	repo := newOrderRepo(t)
	first, err := repo.ListOrders(context.Background(), "", 20)
	if err != nil || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v, err=%v", first, err)
	}
	second, err := repo.ListOrders(context.Background(), first.NextCursor, 20)
	if err != nil || len(second.Items) == 0 {
		t.Fatalf("unexpected second page: %+v, err=%v", second, err)
	}
}
`

	reviewFiles := []ReviewFile{
		{Path: "internal/order/repository.go", Status: "M", Language: "go", Additions: 14, Deletions: 3},
		{Path: "internal/order/cursor.go", Status: "A", Language: "go", Additions: 27, Deletions: 0},
		{Path: "internal/order/repository_test.go", Status: "M", Language: "go", Additions: 12, Deletions: 4},
	}

	base.SetReviewSnapshot(readmePaginationThreadID, "session", "", "", ReviewSnapshot{
		Scope:        "session",
		HeadRevision: "a3f91c2",
		Files:        reviewFiles,
	})
	base.SetReviewSnapshot(readmePaginationThreadID, "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "a3f91c2",
		Files:        reviewFiles,
	})
	base.SetReviewSnapshot(readmePaginationThreadID, "uncommitted", "", "internal/order/repository.go", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "a3f91c2",
		File: &ReviewFileContent{
			Path:    "internal/order/repository.go",
			OldText: repoOld,
			NewText: repoNew,
		},
	})
	base.SetReviewSnapshot(readmePaginationThreadID, "uncommitted", "", "internal/order/cursor.go", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "a3f91c2",
		File: &ReviewFileContent{
			Path:    "internal/order/cursor.go",
			OldText: "",
			NewText: cursorNew,
		},
	})
	base.SetReviewSnapshot(readmePaginationThreadID, "uncommitted", "", "internal/order/repository_test.go", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "a3f91c2",
		File: &ReviewFileContent{
			Path:    "internal/order/repository_test.go",
			OldText: testOld,
			NewText: testNew,
		},
	})

	return &readmeGitShowcaseClient{
		readmeShowcaseClient: base,
		entries: []git.StatusEntry{
			{Path: "internal/order/repository.go", Worktree: 'M'},
			{Path: "internal/order/cursor.go", Index: 'A', Worktree: ' '},
			{Path: "internal/order/repository_test.go", Worktree: 'M'},
		},
		commits: []git.LogCommit{
			{
				SHA:        "a3f91c2d9b8412f0a9c3e1d7f4b2a109c8e7d6f5",
				Parents:    []string{"7e2c18a1b4d5c6e7f8a9b0c1d2e3f4a5b6c7d8e9"},
				Author:     "Alex Chen",
				Email:      "alex@orbit.dev",
				AuthorTime: now.Add(-14 * time.Minute),
				Subject:    "feat(order): 增加 (created_at, id) 复合游标编解码",
				Unpushed:   true,
				Refs: []git.LogRef{
					{Name: "HEAD", Kind: "head"},
					{Name: "feat/cursor-pagination", Kind: "local"},
				},
			},
			{
				SHA:        "7e2c18a1b4d5c6e7f8a9b0c1d2e3f4a5b6c7d8e9",
				Parents:    []string{"4b1d90f2e3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8"},
				Author:     "Alex Chen",
				Email:      "alex@orbit.dev",
				AuthorTime: now.Add(-42 * time.Minute),
				Subject:    "perf(order): 为订单状态与创建时间增加联合索引",
				Unpushed:   true,
			},
			{
				SHA:        "4b1d90f2e3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8",
				Parents:    []string{"1d8a45c3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9"},
				Author:     "Alex Chen",
				Email:      "alex@orbit.dev",
				AuthorTime: now.Add(-3 * time.Hour),
				Subject:    "refactor(order): 抽取订单查询条件构造器",
				Refs: []git.LogRef{
					{Name: "origin/feat/cursor-pagination", Kind: "remote"},
					{Name: "main", Kind: "local"},
				},
			},
			{
				SHA:        "1d8a45c3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9",
				Author:     "Maya Lin",
				Email:      "maya@orbit.dev",
				AuthorTime: now.Add(-26 * time.Hour),
				Subject:    "chore(db): 升级连接池与慢查询采样阈值",
			},
		},
		pr: &git.PRSummary{
			Number:         148,
			Title:          "feat(order): 订单列表接入游标分页",
			State:          "open",
			Author:         "alex",
			HeadBranch:     "feat/cursor-pagination",
			BaseBranch:     "main",
			URL:            "https://github.com/orbit-cloud/orbit-api/pull/148",
			UpdatedAt:      now.Add(-5 * time.Minute),
			ReviewDecision: "APPROVED",
			ChecksState:    "success",
		},
	}
}

// TestReadmeShotsGit 在设置 MCODE_README_SHOTS=1 时生成右侧展开 Git 面板的 README 截图 docs/images/git.png。
func TestReadmeShotsGit(t *testing.T) {
	if os.Getenv("MCODE_README_SHOTS") != "1" {
		t.Skip("set MCODE_README_SHOTS=1 to regenerate README git screenshot")
	}

	client := newReadmeGitShowcaseClient(t)
	state, view := buildReadmeRoot(t, client, readmePaginationThreadID)
	state.toggleTodoCard("pag-todo1", true)

	opened := false
	wrappedView := func(c *ui.Context) {
		view(c)
		if !opened {
			state.mu.Lock()
			tid := state.selectedThreadID
			state.mu.Unlock()
			if tid == readmePaginationThreadID {
				opened = true
				toggleGitPane(state)
				dp := getDiffPanelState(state)
				diffPanelMu.Lock()
				dp.selectedPath = "internal/order/repository.go"
				setReviewCardOpenLocked(dp, "internal/order/repository.go", true)
				diffPanelMu.Unlock()
				c.Invalidate()
			}
		}
	}

	writeReadmeShot(t, "git", wrappedView, false)
	if !inspectorPaneOpen(state, sidePaneGitID) {
		t.Fatal("Git pane should be open in docs/images/git.png")
	}
}
