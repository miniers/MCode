package codex

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

func TestPlanSlashRequest(t *testing.T) {
	tests := []struct {
		name         string
		prompt       string
		resumable    bool
		wantKind     string
		wantInstruct string
	}{
		{name: "compact 精确匹配", prompt: "/compact", resumable: true, wantKind: slashRequestCompact},
		{name: "compact 前后空白", prompt: "  /compact \n", resumable: true, wantKind: slashRequestCompact},
		{name: "compact 带参数不拦截", prompt: "/compact now", resumable: true, wantKind: slashRequestNone},
		{name: "review 无附加文字", prompt: "/review", resumable: true, wantKind: slashRequestReview},
		{name: "review 带说明", prompt: "/review 关注并发安全", resumable: true, wantKind: slashRequestReview, wantInstruct: "关注并发安全"},
		{name: "review 换行分隔", prompt: "/review\n第二行说明", resumable: true, wantKind: slashRequestReview, wantInstruct: "第二行说明"},
		{name: "review 制表符分隔", prompt: "/review\t重点", resumable: true, wantKind: slashRequestReview, wantInstruct: "重点"},
		{name: "review 前缀不算", prompt: "/reviewx", resumable: true, wantKind: slashRequestNone},
		{name: "review 连字符不算", prompt: "/review-foo", resumable: true, wantKind: slashRequestNone},
		{name: "普通文本不拦截", prompt: "帮我 /review 一下", resumable: true, wantKind: slashRequestNone},
		// 没有可恢复的会话时不拦截：compact 没有历史可压缩，review 也无处附着
		{name: "无可恢复会话不拦截 compact", prompt: "/compact", resumable: false, wantKind: slashRequestNone},
		{name: "无可恢复会话不拦截 review", prompt: "/review", resumable: false, wantKind: slashRequestNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, instructions := planSlashRequest(tc.prompt, tc.resumable)
			if kind != tc.wantKind {
				t.Fatalf("expected kind %q, got %q", tc.wantKind, kind)
			}
			if instructions != tc.wantInstruct {
				t.Fatalf("expected instructions %q, got %q", tc.wantInstruct, instructions)
			}
		})
	}
}

func TestBuildCompactParams(t *testing.T) {
	params := buildCompactParams("th_1")
	if len(params) != 1 {
		t.Fatalf("expected only threadId, got %+v", params)
	}
	if params["threadId"] != "th_1" {
		t.Fatalf("unexpected threadId: %v", params["threadId"])
	}
}

func TestBuildReviewParams(t *testing.T) {
	// 无附加文字：审阅工作区未提交改动
	params := buildReviewParams("th_1", "")
	if params["threadId"] != "th_1" {
		t.Fatalf("unexpected threadId: %v", params["threadId"])
	}
	if params["delivery"] != "inline" {
		t.Fatalf("expected delivery inline, got %v", params["delivery"])
	}
	target, ok := params["target"].(map[string]any)
	if !ok {
		t.Fatalf("target is not a map: %#v", params["target"])
	}
	if target["type"] != "uncommittedChanges" {
		t.Fatalf("expected target type uncommittedChanges, got %v", target["type"])
	}
	if _, ok := target["instructions"]; ok {
		t.Fatalf("uncommittedChanges 不应带 instructions: %+v", target)
	}

	// 有附加文字：custom 目标携带说明
	params = buildReviewParams("th_1", "只看并发问题")
	target, _ = params["target"].(map[string]any)
	if target["type"] != "custom" {
		t.Fatalf("expected target type custom, got %v", target["type"])
	}
	if target["instructions"] != "只看并发问题" {
		t.Fatalf("unexpected instructions: %v", target["instructions"])
	}
}

// codexSlashLogScript 是带请求日志的假 app-server：支持 thread/compact/start 与 review/start。
const codexSlashLogScript = `
import sys, json

thread_id = "th_mock_123"
turn_id = "turn_mock_abc"

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/compact/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"COMPACTED"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
    elif method == "review/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"reviewThreadId":thread_id,"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"REVIEWED"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"PLAIN"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`

// slashLogRequest 是日志里一条请求的可断言视图。
type slashLogRequest struct {
	Method string `json:"method"`
	Params struct {
		ThreadID string `json:"threadId"`
		Delivery string `json:"delivery"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
		Target struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		} `json:"target"`
	} `json:"params"`
}

// collectSlashRequests 读出日志里的全部请求。
func collectSlashRequests(t *testing.T, logPath string) []slashLogRequest {
	t.Helper()
	var reqs []slashLogRequest
	for _, line := range readLines(t, logPath) {
		var req slashLogRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		reqs = append(reqs, req)
	}
	return reqs
}

// findSlashRequest 返回日志里第一条指定方法的请求。
func findSlashRequest(t *testing.T, reqs []slashLogRequest, method string) slashLogRequest {
	t.Helper()
	for _, req := range reqs {
		if req.Method == method {
			return req
		}
	}
	t.Fatalf("no %s request found", method)
	return slashLogRequest{}
}

// runSlashTurn 用给定的 prompt 与会话跑一轮，返回事件文本与请求日志。
func runSlashTurn(t *testing.T, prompt, sessionID, script string) (string, []slashLogRequest) {
	t.Helper()
	testenv.TempHome(t)
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script = withRequestLog(script, logPath)

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}

	h := New(Options{})
	defer h.Close()

	var texts []string
	turn := harness.Turn{
		WorkDir:   t.TempDir(),
		Mode:      harness.ModeAgent,
		Prompt:    prompt,
		SessionID: sessionID,
		Sandbox:   sb,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Run(ctx, turn, func(e harness.Event) {
		if e.Type == harness.EventText {
			texts = append(texts, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run(%q) failed: %v", prompt, err)
	}
	return strings.Join(texts, ""), collectSlashRequests(t, logPath)
}

func TestRunCompactUsesNativeRequest(t *testing.T) {
	text, reqs := runSlashTurn(t, "/compact", "th_mock_123", codexSlashLogScript)
	if text != "COMPACTED" {
		t.Fatalf("expected compacted text, got %q", text)
	}

	compact := findSlashRequest(t, reqs, "thread/compact/start")
	if compact.Params.ThreadID != "th_mock_123" {
		t.Fatalf("unexpected compact threadId: %q", compact.Params.ThreadID)
	}
	for _, req := range reqs {
		if req.Method == "turn/start" {
			t.Fatal("compact 轮次不应再发 turn/start")
		}
	}
}

func TestRunCompactWithoutTurnStartedStillCompletes(t *testing.T) {
	// thread/compact/start 的响应是空对象，turnId 只能来自 turn/started 通知；
	// 即使一条 turn/started 都没收到，也必须在 turn/completed 后正常收尾而不是卡住。
	const script = `
import sys, json

thread_id = "th_mock_123"
turn_id = "turn_mock_abc"

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/compact/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"NOS"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`
	text, _ := runSlashTurn(t, "/compact", "th_mock_123", script)
	if text != "NOS" {
		t.Fatalf("expected streamed text, got %q", text)
	}
}

func TestRunReviewUsesNativeRequest(t *testing.T) {
	t.Run("带说明文字用 custom 目标", func(t *testing.T) {
		text, reqs := runSlashTurn(t, "/review 只看并发问题", "th_mock_123", codexSlashLogScript)
		if text != "REVIEWED" {
			t.Fatalf("expected reviewed text, got %q", text)
		}

		review := findSlashRequest(t, reqs, "review/start")
		if review.Params.ThreadID != "th_mock_123" {
			t.Fatalf("unexpected review threadId: %q", review.Params.ThreadID)
		}
		if review.Params.Target.Type != "custom" {
			t.Fatalf("expected custom target, got %q", review.Params.Target.Type)
		}
		if review.Params.Target.Instructions != "只看并发问题" {
			t.Fatalf("unexpected target instructions: %q", review.Params.Target.Instructions)
		}
		if review.Params.Delivery != "inline" {
			t.Fatalf("expected inline delivery, got %q", review.Params.Delivery)
		}
		for _, req := range reqs {
			if req.Method == "turn/start" {
				t.Fatal("review 轮次不应再发 turn/start")
			}
		}
	})

	t.Run("无说明文字审阅未提交改动", func(t *testing.T) {
		_, reqs := runSlashTurn(t, "/review", "th_mock_123", codexSlashLogScript)
		review := findSlashRequest(t, reqs, "review/start")
		if review.Params.Target.Type != "uncommittedChanges" {
			t.Fatalf("expected uncommittedChanges target, got %q", review.Params.Target.Type)
		}
		if review.Params.Delivery != "inline" {
			t.Fatalf("expected inline delivery, got %q", review.Params.Delivery)
		}
	})
}

func TestRunCompactWithoutSessionFallsBackToTurnStart(t *testing.T) {
	// 新会话还没有可恢复的 thread：不拦截 /compact，按普通文本发给 turn/start。
	text, reqs := runSlashTurn(t, "/compact", "", codexSlashLogScript)
	if text != "PLAIN" {
		t.Fatalf("expected plain turn text, got %q", text)
	}
	turn := findSlashRequest(t, reqs, "turn/start")
	if len(turn.Params.Input) != 1 || turn.Params.Input[0].Text != "/compact" {
		t.Fatalf("unexpected turn/start input: %+v", turn.Params.Input)
	}
	for _, req := range reqs {
		if req.Method == "thread/compact/start" {
			t.Fatal("无可恢复会话时不应发 thread/compact/start")
		}
	}
}
