package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

type fakeCodexDeliveryAcker struct {
	decision harness.Decision
	err      error
	acked    bool
	ackReq   harness.ApprovalRequest
	ackErr   error
}

func (f *fakeCodexDeliveryAcker) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	return f.decision, f.err
}

func (f *fakeCodexDeliveryAcker) AckDelivery(req harness.ApprovalRequest, err error) {
	f.acked = true
	f.ackReq = req
	f.ackErr = err
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func TestCodexDeliveryAck_Success(t *testing.T) {
	testenv.TempHome(t)

	acker := &fakeCodexDeliveryAcker{decision: harness.DecisionAllow}
	handler := NewProtocolHandler(harness.Turn{
		Mode:     harness.ModeAgent,
		Approver: acker,
	})

	reqMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`101`),
		Method:  "item/commandExecution/requestApproval",
		Params:  json.RawMessage(`{"itemId":"item-1","command":"echo hello"}`),
	}

	reply, err := handler.HandleServerRequest(context.Background(), &reqMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.onAck == nil {
		t.Fatal("expected reply.onAck to be set")
	}

	var buf bytes.Buffer
	client := newAppClient(nopWriteCloser{&buf})
	sendErr := client.send(reply)
	reply.onAck(sendErr)

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called")
	}
	if acker.ackErr != nil {
		t.Fatalf("expected nil ackErr, got: %v", acker.ackErr)
	}
	if acker.ackReq.ToolName != "Bash" || acker.ackReq.ToolID != "item-1" {
		t.Errorf("unexpected approval request: %+v", acker.ackReq)
	}
}

func TestCodexDeliveryAck_PipeFailure(t *testing.T) {
	testenv.TempHome(t)

	acker := &fakeCodexDeliveryAcker{decision: harness.DecisionAllow}
	handler := NewProtocolHandler(harness.Turn{
		Mode:     harness.ModeAgent,
		Approver: acker,
	})

	reqMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`102`),
		Method:  "item/fileChange/requestApproval",
		Params:  json.RawMessage(`{"itemId":"file-item-2","reason":"edit"}`),
	}

	reply, err := handler.HandleServerRequest(context.Background(), &reqMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply == nil || reply.onAck == nil {
		t.Fatal("expected reply with onAck")
	}

	pr, pw := io.Pipe()
	_ = pr.Close()
	_ = pw.Close()

	client := newAppClient(pw)
	sendErr := client.send(reply)
	reply.onAck(sendErr)

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called on send failure")
	}
	if acker.ackErr == nil {
		t.Fatal("expected non-nil ackErr on broken pipe")
	}
	if acker.ackReq.ToolName != "FileChange" || acker.ackReq.ToolID != "file-item-2" {
		t.Errorf("unexpected approval request: %+v", acker.ackReq)
	}
}

func TestCodexDeliveryAck_ApproveError(t *testing.T) {
	testenv.TempHome(t)

	approveErr := errors.New("approver context timeout")
	acker := &fakeCodexDeliveryAcker{
		err: approveErr,
	}
	handler := NewProtocolHandler(harness.Turn{
		Mode:     harness.ModeAgent,
		Approver: acker,
	})

	reqMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`103`),
		Method:  "applyPatchApproval",
		Params:  json.RawMessage(`{"callId":"patch-call-1"}`),
	}

	reply, err := handler.HandleServerRequest(context.Background(), &reqMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.onAck != nil {
		t.Fatal("reply.onAck should be nil when Approve already errored")
	}

	if !acker.acked {
		t.Fatal("expected AckDelivery to be called when Approve fails")
	}
	if !errors.Is(acker.ackErr, approveErr) && !strings.Contains(acker.ackErr.Error(), "approver context timeout") {
		t.Fatalf("expected approveErr, got: %v", acker.ackErr)
	}
}

func TestCodexHarness_ConfirmsApprovalDelivery(t *testing.T) {
	testenv.TempHome(t)
	h := New(Options{})
	if !h.ConfirmsApprovalDelivery() {
		t.Fatal("expected ConfirmsApprovalDelivery to return true")
	}
}
