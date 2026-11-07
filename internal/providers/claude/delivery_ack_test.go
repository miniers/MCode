package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"mcode/internal/harness"
)

type fakeDeliveryAckerApprover struct {
	decision harness.Decision
	err      error
	acked    bool
	ackReq   harness.ApprovalRequest
	ackErr   error
}

func (f *fakeDeliveryAckerApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	return f.decision, f.err
}

func (f *fakeDeliveryAckerApprover) AckDelivery(req harness.ApprovalRequest, err error) {
	f.acked = true
	f.ackReq = req
	f.ackErr = err
}

func TestClaudeDeliveryAck_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	acker := &fakeDeliveryAckerApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, acker)
	defer cleanup()

	reqBody := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "Bash",
				"tool_use_id": "call_123",
				"input": {"command": "ls -la"}
			}
		}
	}`)

	resp, _ := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called on successful delivery")
	}
	if acker.ackErr != nil {
		t.Fatalf("expected nil ackErr on success, got: %v", acker.ackErr)
	}
	if acker.ackReq.ToolID != "call_123" || acker.ackReq.ToolName != "Bash" {
		t.Errorf("unexpected approval request: %+v", acker.ackReq)
	}
}

type brokenResponseWriter struct {
	header http.Header
}

func (b *brokenResponseWriter) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}
	return b.header
}

func (b *brokenResponseWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func (b *brokenResponseWriter) WriteHeader(int) {}

func TestClaudeDeliveryAck_WriteFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	acker := &fakeDeliveryAckerApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, acker)
	defer cleanup()

	reqID := json.RawMessage(`1`)
	rawArgs := json.RawMessage(`{
		"tool_name": "Bash",
		"tool_use_id": "call_err",
		"input": {"command": "echo test"}
	}`)

	httpReq, err := http.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	bw := &brokenResponseWriter{}
	server.handleApproveCall(bw, httpReq, &reqID, rawArgs)

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called on write failure")
	}
	if !errors.Is(acker.ackErr, io.ErrClosedPipe) {
		t.Fatalf("expected io.ErrClosedPipe, got %v", acker.ackErr)
	}
	if acker.ackReq.ToolID != "call_err" {
		t.Errorf("unexpected ToolID: %s", acker.ackReq.ToolID)
	}
}

func TestClaudeDeliveryAck_ApproveError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	approveErr := errors.New("user dismissed")
	acker := &fakeDeliveryAckerApprover{
		err: approveErr,
	}
	server, cleanup := newTestApprovalServer(t, acker)
	defer cleanup()

	reqBody := []byte(`{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "Bash",
				"tool_use_id": "call_fail",
				"input": {"command": "rm -rf /"}
			}
		}
	}`)

	resp, _ := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called when Approve returns error")
	}
	if !errors.Is(acker.ackErr, approveErr) && !strings.Contains(acker.ackErr.Error(), "user dismissed") {
		t.Fatalf("expected user dismissed error, got: %v", acker.ackErr)
	}
}

func TestClaudeHarness_ConfirmsApprovalDelivery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := New(Options{})
	if !h.ConfirmsApprovalDelivery() {
		t.Fatal("expected ConfirmsApprovalDelivery to return true")
	}
}
