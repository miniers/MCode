package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

type fakeStewardExtService struct {
	openProjectID, openThreadID string
	openErr                     error
}

func (f *fakeStewardExtService) Open(ctx context.Context, projectID string) (string, error) {
	f.openProjectID = projectID
	return f.openThreadID, f.openErr
}
func newStewardExtTestDispatcher(t *testing.T, svc stewardExtService) (*Dispatcher, *memoryExtRecordingConn) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if svc != nil {
		backend.SetService(stewardExtServiceName, svc)
	}
	return newMemoryExtTestDispatcher(t, backend)
}
func TestStewardOpenSuccess(t *testing.T) {
	svc := &fakeStewardExtService{openThreadID: "steward-thread"}
	d, rec := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	resp := callExtension(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p1"},"id":1}`, stewardOpenMethod))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.openProjectID != "p1" {
		t.Fatalf("projectID 未透传: %q", svc.openProjectID)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result wire.StewardOpenResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "steward-thread" {
		t.Fatalf("threadID = %q", result.ThreadID)
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("open 不该广播，得到 %+v", rec.notifications)
	}
}
func TestStewardOpenValidation(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, _ := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	for _, raw := range []string{`{"jsonrpc":"2.0","method":"steward.open","params":{},"id":1}`, `{"jsonrpc":"2.0","method":"steward.open","params":{"projectID":"p1","unexpected":1},"id":1}`} {
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("参数错误应被拒绝: %+v", resp)
		}
	}
}
