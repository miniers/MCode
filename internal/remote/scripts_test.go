package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// runStatusOnce 调一次 scripts.runStatus，返回解析后的结果或 RPC 错误。
func runStatusOnce(t *testing.T, ctx context.Context, d *Dispatcher, conn *fakeConn, threadID, runID string) (wire.ScriptsRunStatusResult, *wire.RPCError) {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.runStatus","params":{"threadID":"%s","runID":"%s"},"id":30}`, threadID, runID)
	raw := d.Handle(ctx, conn, []byte(req))
	var resp struct {
		Result wire.ScriptsRunStatusResult `json:"result"`
		Error  *wire.RPCError              `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal error: %v, raw: %s", err, string(raw))
	}
	return resp.Result, resp.Error
}

// waitScriptRunFinished 轮询 scripts.runStatus 直到运行结束，避开依赖固定 sleep 的时效性顾虑。
func waitScriptRunFinished(t *testing.T, ctx context.Context, d *Dispatcher, conn *fakeConn, threadID, runID string) wire.ScriptsRunStatusResult {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last wire.ScriptsRunStatusResult
	for time.Now().Before(deadline) {
		res, rpcErr := runStatusOnce(t, ctx, d, conn, threadID, runID)
		if rpcErr != nil {
			t.Fatalf("runStatus error: %+v", rpcErr)
		}
		last = res
		if res.Status != "running" {
			return res
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("script run %s did not finish in time, last status: %+v", runID, last)
	return last
}

// 1. scripts.list 锁文件探测 runner、package.json 脚本解析与空 projectScripts 返回
func TestScriptsList_Success(t *testing.T) {
	ctx := context.Background()
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 写入 package.json
	pkgJSON := `{
		"name": "sample-pkg",
		"scripts": {
			"build": "echo building",
			"test": "echo testing"
		}
	}`
	if err := os.WriteFile(filepath.Join(workDir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// 写入 pnpm-lock.yaml 验证 runner 识别
	if err := os.WriteFile(filepath.Join(workDir, "pnpm-lock.yaml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.list","params":{"threadID":"%s"},"id":10}`, threadID)
	respRaw := d.Handle(ctx, conn, []byte(req))

	var resp struct {
		Result wire.ScriptsListResult `json:"result"`
		Error  *wire.RPCError         `json:"error"`
	}
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal error: %v, raw: %s", err, string(respRaw))
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}

	res := resp.Result
	if res.Runner != "pnpm" {
		t.Errorf("expected runner 'pnpm', got '%s'", res.Runner)
	}
	if len(res.PackageScripts) != 2 {
		t.Fatalf("expected 2 package scripts, got %d", len(res.PackageScripts))
	}
	if res.PackageScripts[0].Name != "build" || res.PackageScripts[1].Name != "test" {
		t.Errorf("unexpected script order or names: %+v", res.PackageScripts)
	}
	if res.ProjectScripts == nil || len(res.ProjectScripts) != 0 {
		t.Errorf("expected empty non-nil projectScripts, got %+v", res.ProjectScripts)
	}
}

// 2. scripts.setFavorite 持久化（b.SetDataDir）与无 dataDir 时的内存回退
func TestScriptsSetFavorite_PersistenceAndMemory(t *testing.T) {
	ctx := context.Background()
	b, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 配置 dataDir 测试原子写入与持久化
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	// 1. 收藏 "build" 脚本
	favReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.setFavorite","params":{"threadID":"%s","name":"build","isFavorite":true},"id":11}`, threadID)
	favRespRaw := d.Handle(ctx, conn, []byte(favReq))
	var favResp struct {
		Result wire.ScriptsSetFavoriteResult `json:"result"`
		Error  *wire.RPCError                `json:"error"`
	}
	if err := json.Unmarshal(favRespRaw, &favResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if favResp.Error != nil {
		t.Fatalf("unexpected error: %+v", favResp.Error)
	}
	if len(favResp.Result.Favorites) != 1 || favResp.Result.Favorites[0] != "build" {
		t.Errorf("expected favorites ['build'], got %+v", favResp.Result.Favorites)
	}

	// 验证持久化文件存在且可解析
	favFile := filepath.Join(dataDir, "remote", "script-favorites.json")
	favBytes, err := os.ReadFile(favFile)
	if err != nil {
		t.Fatalf("failed to read persisted favorites file: %v", err)
	}
	var stored map[string][]string
	if err := json.Unmarshal(favBytes, &stored); err != nil {
		t.Fatalf("invalid json in favorites file: %v", err)
	}
	if len(stored[workDir]) != 1 || stored[workDir][0] != "build" {
		t.Errorf("stored favorites mismatch: %+v", stored)
	}

	// 2. 再收藏 "test"，验证返回列表按字母排序
	favReq2 := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.setFavorite","params":{"threadID":"%s","name":"a-lint","isFavorite":true},"id":12}`, threadID)
	favRespRaw2 := d.Handle(ctx, conn, []byte(favReq2))
	var favResp2 struct {
		Result wire.ScriptsSetFavoriteResult `json:"result"`
		Error  *wire.RPCError                `json:"error"`
	}
	_ = json.Unmarshal(favRespRaw2, &favResp2)
	if len(favResp2.Result.Favorites) != 2 || favResp2.Result.Favorites[0] != "a-lint" || favResp2.Result.Favorites[1] != "build" {
		t.Errorf("expected sorted favorites ['a-lint', 'build'], got %+v", favResp2.Result.Favorites)
	}

	// 3. 取消收藏 "build"
	unfavReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.setFavorite","params":{"threadID":"%s","name":"build","isFavorite":false},"id":13}`, threadID)
	unfavRespRaw := d.Handle(ctx, conn, []byte(unfavReq))
	var unfavResp struct {
		Result wire.ScriptsSetFavoriteResult `json:"result"`
		Error  *wire.RPCError                `json:"error"`
	}
	_ = json.Unmarshal(unfavRespRaw, &unfavResp)
	if len(unfavResp.Result.Favorites) != 1 || unfavResp.Result.Favorites[0] != "a-lint" {
		t.Errorf("expected remaining favorites ['a-lint'], got %+v", unfavResp.Result.Favorites)
	}

	// 4. dataDir 为空时的内存缓存模式测试
	bMem, _, _, _, threadIDMem := setupWorkspaceTestEnv(t)
	memRes, err := bMem.ScriptsSetFavorite(ctx, wire.ScriptsSetFavoriteParams{
		ThreadID:   threadIDMem,
		Name:       "start",
		IsFavorite: true,
	})
	if err != nil {
		t.Fatalf("in-memory ScriptsSetFavorite failed: %v", err)
	}
	if len(memRes.Favorites) != 1 || memRes.Favorites[0] != "start" {
		t.Errorf("expected in-memory favorites ['start'], got %+v", memRes.Favorites)
	}
}

// 3. scripts.run 校验：未知 package 脚本报错、未知 project 脚本报错、有效脚本返回 runID，
// 运行结果只存内存，由 scripts.runStatus 查询；未知 runID 返回 InvalidParams。
func TestScriptsRun_ValidationAndExecution(t *testing.T) {
	ctx := context.Background()
	b, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 写入 package.json
	pkgJSON := `{"scripts": {"greet": "echo hello"}}`
	_ = os.WriteFile(filepath.Join(workDir, "package.json"), []byte(pkgJSON), 0644)

	// 1. 未知 package 脚本报 InvalidParams 错误
	badReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.run","params":{"threadID":"%s","kind":"package","name":"non_existent"},"id":20}`, threadID)
	badRespRaw := d.Handle(ctx, conn, []byte(badReq))
	var badResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(badRespRaw, &badResp)
	if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams error for unknown script, got %+v", badResp.Error)
	}

	// 2. kind=project 找不到 scriptID 报 ResourceNotFound 错误
	projectReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.run","params":{"threadID":"%s","kind":"project","scriptID":"%s"},"id":21}`, threadID, newUUID())
	projectRespRaw := d.Handle(ctx, conn, []byte(projectReq))
	var projectResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(projectRespRaw, &projectResp)
	if projectResp.Error == nil || projectResp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ResourceNotFound error for missing project script, got %+v", projectResp.Error)
	}

	// 3. 有效脚本返回 ok: true 与 runID
	goodReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.run","params":{"threadID":"%s","kind":"package","name":"greet"},"id":22}`, threadID)
	goodRespRaw := d.Handle(ctx, conn, []byte(goodReq))
	var goodResp struct {
		Result wire.ScriptsRunResult `json:"result"`
		Error  *wire.RPCError        `json:"error"`
	}
	if err := json.Unmarshal(goodRespRaw, &goodResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if goodResp.Error != nil {
		t.Fatalf("unexpected error on valid script run: %+v", goodResp.Error)
	}
	if !goodResp.Result.OK {
		t.Errorf("expected ok: true")
	}
	if !isValidUUID(goodResp.Result.RunID) {
		t.Errorf("expected valid runID, got %q", goodResp.Result.RunID)
	}

	// 4. 项目脚本的退出码与输出存内存，可通过 scripts.runStatus 查询；
	// 不再往会话时间线追加任何消息。
	if err := b.SetProjectScripts(threadID, []ProjectScript{{Name: "echo-hello", Command: "echo hello-from-script"}}); err != nil {
		t.Fatalf("save project scripts: %v", err)
	}
	stored, err := b.ProjectScripts(threadID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("read project scripts: %v, %+v", err, stored)
	}
	projRunReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.run","params":{"threadID":"%s","kind":"project","scriptID":"%s"},"id":23}`, threadID, stored[0].ID)
	projRunRaw := d.Handle(ctx, conn, []byte(projRunReq))
	var projRunResp struct {
		Result wire.ScriptsRunResult `json:"result"`
		Error  *wire.RPCError        `json:"error"`
	}
	if err := json.Unmarshal(projRunRaw, &projRunResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if projRunResp.Error != nil {
		t.Fatalf("unexpected error on project script run: %+v", projRunResp.Error)
	}
	if projRunResp.Result.RunID == "" {
		t.Fatalf("expected runID on project script run")
	}

	status := waitScriptRunFinished(t, ctx, d, conn, threadID, projRunResp.Result.RunID)
	if status.RunID != projRunResp.Result.RunID {
		t.Errorf("runID mismatch: %q vs %q", status.RunID, projRunResp.Result.RunID)
	}
	if status.Label != "echo-hello" {
		t.Errorf("expected label 'echo-hello', got %q", status.Label)
	}
	if status.Status != "finished" {
		t.Errorf("expected status 'finished', got %q", status.Status)
	}
	if status.ExitCode == nil || *status.ExitCode != 0 {
		t.Errorf("expected exitCode 0, got %+v", status.ExitCode)
	}
	if !strings.Contains(status.Output, "hello-from-script") {
		t.Errorf("expected output to contain script stdout, got %q", status.Output)
	}

	// 5. 未知 runID 返回 InvalidParams，message 为 run not found
	missingReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.runStatus","params":{"threadID":"%s","runID":"%s"},"id":24}`, threadID, newUUID())
	missingRaw := d.Handle(ctx, conn, []byte(missingReq))
	var missingResp struct {
		Error *wire.RPCError `json:"error"`
	}
	if err := json.Unmarshal(missingRaw, &missingResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if missingResp.Error == nil || missingResp.Error.Code != wire.InvalidParams || missingResp.Error.Message != "run not found" {
		t.Errorf("expected InvalidParams 'run not found', got %+v", missingResp.Error)
	}
}

func TestCappedOutput_DropsBytesBeyondLimit(t *testing.T) {
	w := &cappedOutput{limit: 8}

	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	// 第二次写入只保留剩余空间，其余计入丢弃字节数
	if _, err := w.Write([]byte("efghij")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if got := w.buf.String(); got != "abcdefgh" {
		t.Fatalf("unexpected buffered output: %q", got)
	}
	if w.truncated != 2 {
		t.Fatalf("expected 2 dropped bytes, got %d", w.truncated)
	}

	// 写满后继续写入不再增长缓冲，只累加丢弃字节数
	if _, err := w.Write([]byte("zzz")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if got := w.buf.Len(); got != 8 {
		t.Fatalf("buffer must stay capped at 8 bytes, got %d", got)
	}
	if w.truncated != 5 {
		t.Fatalf("expected 5 dropped bytes, got %d", w.truncated)
	}
}

// TestScriptsRunStatus_StreamsOutputWhileRunning 验证脚本还在运行时，scripts.runStatus
// 就能返回截至当前的部分输出，结束后输出仍包含完整内容。
func TestScriptsRunStatus_StreamsOutputWhileRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("依赖 sh -c 与 sleep，Windows 下不适用")
	}

	ctx := context.Background()
	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	if err := b.SetProjectScripts(threadID, []ProjectScript{{Name: "streaming", Command: "echo first; sleep 2; echo second"}}); err != nil {
		t.Fatalf("save project scripts: %v", err)
	}
	stored, err := b.ProjectScripts(threadID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("read project scripts: %v, %+v", err, stored)
	}

	runReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"scripts.run","params":{"threadID":"%s","kind":"project","scriptID":"%s"},"id":31}`, threadID, stored[0].ID)
	runRaw := d.Handle(ctx, conn, []byte(runReq))
	var runResp struct {
		Result wire.ScriptsRunResult `json:"result"`
		Error  *wire.RPCError        `json:"error"`
	}
	if err := json.Unmarshal(runRaw, &runResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if runResp.Error != nil {
		t.Fatalf("unexpected error on project script run: %+v", runResp.Error)
	}
	runID := runResp.Result.RunID
	if runID == "" {
		t.Fatalf("expected runID on project script run")
	}

	// 运行期间必须能观测到 running 且带部分输出。
	sawPartial := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !sawPartial {
		res, rpcErr := runStatusOnce(t, ctx, d, conn, threadID, runID)
		if rpcErr != nil {
			t.Fatalf("runStatus error: %+v", rpcErr)
		}
		if res.Status == "running" && strings.Contains(res.Output, "first") {
			sawPartial = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawPartial {
		t.Errorf("expected to observe running status with partial output")
	}

	// 结束后输出包含完整内容。
	final := waitScriptRunFinished(t, ctx, d, conn, threadID, runID)
	if final.Status != "finished" {
		t.Errorf("expected status 'finished', got %q", final.Status)
	}
	if !strings.Contains(final.Output, "first") || !strings.Contains(final.Output, "second") {
		t.Errorf("expected final output to contain both lines, got %q", final.Output)
	}
}
