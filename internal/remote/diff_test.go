package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func (b *fakeBackend) GetDiff(ctx context.Context, params wire.ThreadsGetDiffParams) (wire.ThreadsGetDiffResult, error) {
	return wire.ThreadsGetDiffResult{
		ThreadID:     params.ThreadID,
		TurnID:       params.TurnID,
		Diff:         "",
		Truncated:    false,
		FilesChanged: []string{},
		Turns:        []wire.ReviewTurn{},
	}, nil
}

func TestMemoryBackend_GetDiffWorkflow(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	// 1. 初始化 Git 仓库并设置用户身份
	runGit("init")
	runGit("config", "user.name", "test-user")
	runGit("config", "user.email", "test@localhost")

	// 写入初始基线文件并提交，作为工作区基准 HEAD
	baseFile := filepath.Join(repoDir, "base.txt")
	if err := os.WriteFile(baseFile, []byte("baseline content\n"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-m", "initial commit")

	headBefore := runGit("rev-parse", "HEAD")
	statusBefore := runGit("status", "--porcelain")

	// 2. 初始化 MemoryBackend 与关联项目
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{
		ID:   projID,
		Name: "test-git-project",
		Path: repoDir,
	})

	// 3. 注册 Mock Harness，在 Run 中针对不同轮次写入与修改文件
	var (
		turnCount int
		turnIDs   []string
	)
	mockH := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turnCount++
			turnIDs = append(turnIDs, turn.ID)
			if turnCount == 1 {
				// 第一轮：新增 file1.txt
				f1 := filepath.Join(turn.WorkDir, "file1.txt")
				if err := os.WriteFile(f1, []byte("content line 1\n"), 0644); err != nil {
					return err
				}
			} else if turnCount == 2 {
				// 第二轮：新增 file2.txt
				f2 := filepath.Join(turn.WorkDir, "file2.txt")
				if err := os.WriteFile(f2, []byte("content line 2\n"), 0644); err != nil {
					return err
				}
			}
			emit(harness.Event{Type: harness.EventText, Text: fmt.Sprintf("Turn %d completed", turnCount)})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.RegisterHarness(mockH)

	// 4. 创建会话并执行第一轮与第二轮
	thRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := thRes.ThreadID

	// 执行第一轮
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run first turn",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage turn 1 failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	// 执行第二轮
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run second turn",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage turn 2 failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	if len(turnIDs) < 2 {
		t.Fatalf("expected at least 2 turns recorded, got %d", len(turnIDs))
	}
	turn1ID := turnIDs[0]
	turn2ID := turnIDs[1]

	// 5. 断言存在两个 turn 的 start 和 end ref
	gitRunner := git.NewRunner(gitBin, 4)
	refs, err := gitRunner.ListRefs(ctx, repoDir, "refs/mcode/checkpoints/"+threadID+"/")
	if err != nil {
		t.Fatalf("ListRefs checkpoints failed: %v", err)
	}

	expectedRefs := map[string]bool{
		git.CheckpointRef(threadID, turn1ID) + "/start": false,
		git.CheckpointRef(threadID, turn1ID) + "/end":   false,
		git.CheckpointRef(threadID, turn2ID) + "/start": false,
		git.CheckpointRef(threadID, turn2ID) + "/end":   false,
	}
	for _, r := range refs {
		if _, ok := expectedRefs[r.Name]; ok {
			expectedRefs[r.Name] = true
		}
	}
	for refName, found := range expectedRefs {
		if !found {
			t.Errorf("expected checkpoint ref %s not found in repository", refName)
		}
	}

	// 6. 对第二轮调用 getDiff，断言只包含第二轮的改动
	diffTurn2, err := backend.GetDiff(ctx, wire.ThreadsGetDiffParams{
		ThreadID: threadID,
		TurnID:   &turn2ID,
	})
	if err != nil {
		t.Fatalf("GetDiff turn2 failed: %v", err)
	}
	if !strings.Contains(diffTurn2.Diff, "file2.txt") {
		t.Errorf("expected turn2 diff to contain file2.txt, got:\n%s", diffTurn2.Diff)
	}
	if strings.Contains(diffTurn2.Diff, "file1.txt") {
		t.Errorf("expected turn2 diff NOT to contain file1.txt, got:\n%s", diffTurn2.Diff)
	}
	if len(diffTurn2.FilesChanged) != 1 || diffTurn2.FilesChanged[0] != "file2.txt" {
		t.Errorf("expected turn2 filesChanged [file2.txt], got: %v", diffTurn2.FilesChanged)
	}

	// 7. 不带 turnID 时调用 getDiff，断言包含全部改动，且 turns 长度为 2
	diffAll, err := backend.GetDiff(ctx, wire.ThreadsGetDiffParams{
		ThreadID: threadID,
	})
	if err != nil {
		t.Fatalf("GetDiff all turns failed: %v", err)
	}
	if !strings.Contains(diffAll.Diff, "file1.txt") || !strings.Contains(diffAll.Diff, "file2.txt") {
		t.Errorf("expected all-turns diff to contain both file1.txt and file2.txt, got:\n%s", diffAll.Diff)
	}
	if len(diffAll.Turns) != 2 {
		t.Fatalf("expected turns length 2, got %d", len(diffAll.Turns))
	}
	if diffAll.Turns[0].TurnID != turn1ID || diffAll.Turns[1].TurnID != turn2ID {
		t.Errorf("unexpected turns order or ID: %+v", diffAll.Turns)
	}
	if !diffAll.Turns[0].HasChanges || !diffAll.Turns[1].HasChanges {
		t.Errorf("expected both turns to have changes, got: %+v", diffAll.Turns)
	}

	// 8. 断言非 git 目录的项目会返回 failureReason，且不报错
	nonGitDir := t.TempDir()
	nonGitProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{
		ID:   nonGitProjID,
		Name: "non-git-project",
		Path: nonGitDir,
	})
	thNG, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: nonGitProjID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread for non-git project failed: %v", err)
	}
	ngDiff, err := backend.GetDiff(ctx, wire.ThreadsGetDiffParams{
		ThreadID: thNG.ThreadID,
	})
	if err != nil {
		t.Fatalf("expected GetDiff on non-git dir not to error, got: %v", err)
	}
	if ngDiff.FailureReason == nil || *ngDiff.FailureReason == "" {
		t.Errorf("expected failureReason to be non-empty for non-git project")
	}
	if ngDiff.Diff != "" {
		t.Errorf("expected empty diff for non-git project, got: %s", ngDiff.Diff)
	}

	// 9. 断言用户仓库的 HEAD 和 index 保持不变
	headAfter := runGit("rev-parse", "HEAD")
	if headBefore != headAfter {
		t.Errorf("expected HEAD commit to remain %s, got %s", headBefore, headAfter)
	}
	cachedDiff := runGit("diff", "--cached")
	if cachedDiff != "" {
		t.Errorf("expected user index to be untouched (empty cached diff), got: %s", cachedDiff)
	}
	statusAfter := runGit("status", "--porcelain")
	// 初始状态与最终状态对比：仅工作区增加了未暂存的 untracked 文件，用户暂存区 index 没有任何 staged 变更
	for _, line := range strings.Split(statusAfter, "\n") {
		if strings.HasPrefix(line, "A ") || strings.HasPrefix(line, "M ") || strings.HasPrefix(line, "D ") {
			t.Errorf("unexpected staged index change in status: %s", line)
		}
	}
	_ = statusBefore

	// 10. 通过 dispatcher 调用一次 threads.getDiff，校验 JSON 字段名
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	dispatcher := NewDispatcher(&fakeAuth{}, backend)
	reqJSON := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.getDiff","params":{"threadID":"%s"},"id":1}`, threadID)
	respRaw := dispatcher.Handle(ctx, conn, []byte(reqJSON))
	if respRaw == nil {
		t.Fatalf("expected non-nil response from dispatcher")
	}

	var rpcResp struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *wire.RPCError  `json:"error"`
		ID      int             `json:"id"`
	}
	if err := json.Unmarshal(respRaw, &rpcResp); err != nil {
		t.Fatalf("unmarshal rpc response failed: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}

	var resultMap map[string]any
	if err := json.Unmarshal(rpcResp.Result, &resultMap); err != nil {
		t.Fatalf("unmarshal result map failed: %v", err)
	}

	// 校验顶级字段名
	topFields := []string{"threadID", "diff", "truncated", "filesChanged", "turns"}
	for _, f := range topFields {
		if _, ok := resultMap[f]; !ok {
			t.Errorf("expected top-level field %q in JSON result, got payload: %s", f, string(rpcResp.Result))
		}
	}

	// 校验 turns 数组内元素字段名
	turnsRaw, ok := resultMap["turns"].([]any)
	if !ok || len(turnsRaw) == 0 {
		t.Fatalf("expected non-empty 'turns' array in JSON result")
	}
	firstTurnObj, ok := turnsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected turn item in 'turns' to be a JSON object")
	}
	turnFields := []string{"turnID", "index", "status", "hasChanges"}
	for _, tf := range turnFields {
		if _, ok := firstTurnObj[tf]; !ok {
			t.Errorf("expected turn item field %q in JSON turn object", tf)
		}
	}
}

// TestMemoryBackend_GetDiffOnlySessionTouchedFiles 验证 diff 面板只展示本会话工具实际触碰的文件：
// 同期用户手改的文件（未通过工具事件上报）不出现在会话与单轮 diff 中；
// 轮次结束后用户再修改的文件也因区间改为 start→finish 检查点而被排除。
func TestMemoryBackend_GetDiffOnlySessionTouchedFiles(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.name", "test-user")
	runGit("config", "user.email", "test@localhost")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("baseline\n"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-m", "initial commit")

	st, err := store.Open(filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	ctx := context.Background()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	backend.SetGitRunner(git.NewRunner(gitBin, 2))

	var turnIDs []string
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		turnIDs = append(turnIDs, turn.ID)
		sessionPath := filepath.Join(turn.WorkDir, "session.txt")
		if err := os.WriteFile(sessionPath, []byte("session change\n"), 0644); err != nil {
			return err
		}
		// 模拟本会话执行期间用户手改的文件：不经过工具事件，因此不属于本会话的改动。
		if err := os.WriteFile(filepath.Join(turn.WorkDir, "concurrent-user.txt"), []byte("user edit\n"), 0644); err != nil {
			return err
		}
		input, err := json.Marshal(map[string]string{"file_path": sessionPath})
		if err != nil {
			return err
		}
		emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-1", ToolName: "Write", Input: input})
		emit(harness.Event{Type: harness.EventDone})
		return nil
	}}
	backend.SetHarness(mock, repoDir, harness.ModeAgent)

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "diff-filter", Path: repoDir})
	thread, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projID})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "modify"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thread.ThreadID)

	if len(turnIDs) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turnIDs))
	}

	// 轮次结束后用户继续手改工作区。
	if err := os.WriteFile(filepath.Join(repoDir, "manual-after.txt"), []byte("manual\n"), 0644); err != nil {
		t.Fatalf("write manual-after.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("manual baseline edit\n"), 0644); err != nil {
		t.Fatalf("edit base.txt: %v", err)
	}

	record, err := st.GetTurn(ctx, turnIDs[0])
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if len(record.TouchedFiles) != 1 || record.TouchedFiles[0] != "session.txt" {
		t.Fatalf("expected TouchedFiles=[session.txt], got %+v", record.TouchedFiles)
	}

	unexpected := []string{"concurrent-user.txt", "manual-after.txt", "base.txt"}
	assertOnlySessionFile := func(label string, result wire.ThreadsGetDiffResult) {
		t.Helper()
		if !strings.Contains(result.Diff, "session.txt") {
			t.Errorf("%s: expected diff to contain session.txt, got:\n%s", label, result.Diff)
		}
		for _, name := range unexpected {
			if strings.Contains(result.Diff, name) {
				t.Errorf("%s: expected diff NOT to contain %s, got:\n%s", label, name, result.Diff)
			}
			for _, changed := range result.FilesChanged {
				if changed == name {
					t.Errorf("%s: unexpected filesChanged entry %s: %v", label, name, result.FilesChanged)
				}
			}
		}
		if len(result.FilesChanged) != 1 || result.FilesChanged[0] != "session.txt" {
			t.Errorf("%s: expected filesChanged=[session.txt], got %v", label, result.FilesChanged)
		}
	}

	diffAll, err := backend.GetDiff(ctx, wire.ThreadsGetDiffParams{ThreadID: thread.ThreadID})
	if err != nil {
		t.Fatalf("GetDiff all: %v", err)
	}
	assertOnlySessionFile("all turns", diffAll)

	turnID := turnIDs[0]
	diffTurn, err := backend.GetDiff(ctx, wire.ThreadsGetDiffParams{ThreadID: thread.ThreadID, TurnID: &turnID})
	if err != nil {
		t.Fatalf("GetDiff turn: %v", err)
	}
	assertOnlySessionFile("single turn", diffTurn)
}
