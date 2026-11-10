package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/remote/wire"
)

type previewTestService struct{}

func (previewTestService) Preview(ctx context.Context, sch wire.AutomationExtSchedule, after time.Time, count int) (wire.AutomationExtPreviewResult, error) {
	result, err := automation.PreviewSchedule(ctx, automation.Schedule{Type: sch.Type, TimeOfDay: sch.TimeOfDay, Timezone: sch.Timezone, Expression: sch.Expression}, after, count)
	if err != nil {
		return wire.AutomationExtPreviewResult{}, err
	}
	out := wire.AutomationExtPreviewResult{Times: []string{}, Exhausted: result.Exhausted}
	for _, at := range result.Times {
		out.Times = append(out.Times, at.UTC().Format(time.RFC3339Nano))
	}
	return out, nil
}

func TestAutomationsPreviewRPC(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	backend.SetService(automationExtServiceName, previewTestService{})
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(conn)
	request := func(params string) wire.Response {
		return callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"automations.preview","params":`+params+`,"id":1}`)
	}
	result := automationExtResult[wire.AutomationExtPreviewResult](t, request(`{"schedule":{"type":"daily","timeOfDay":"09:00","timezone":"Asia/Shanghai"},"after":"2026-11-10T00:00:00Z"}`))
	want := []string{"2026-11-10T01:00:00Z", "2026-11-11T01:00:00Z", "2026-11-12T01:00:00Z"}
	if !reflect.DeepEqual(result.Times, want) || result.Exhausted {
		t.Fatalf("result = %+v", result)
	}
	defaultAfter := automationExtResult[wire.AutomationExtPreviewResult](t, request(`{"schedule":{"type":"daily","timeOfDay":"09:00","timezone":"UTC"}}`))
	if len(defaultAfter.Times) != 3 {
		t.Fatalf("default preview = %+v", defaultAfter)
	}
	first, err := time.Parse(time.RFC3339, defaultAfter.Times[0])
	if err != nil || first.Before(time.Now().Add(-time.Minute)) || first.After(time.Now().Add(25*time.Hour)) {
		t.Fatalf("default after: first = %q, err = %v", defaultAfter.Times[0], err)
	}
	for _, params := range []string{
		`{"schedule":{"type":"daily","timeOfDay":"09:00"},"extra":1}`,
		`{"schedule":{"type":"daily","timeOfDay":"09:00","extra":1}}`,
		`{"schedule":{"type":"daily","timeOfDay":"09:00"},"count":0}`,
		`{"schedule":{"type":"daily","timeOfDay":"09:00"},"count":6}`,
		`{"schedule":{"type":"daily","timeOfDay":"09:00"},"count":null}`,
		`{"schedule":{"type":"daily","timeOfDay":"09:00"},"after":"bad"}`,
		`{"schedule":{"type":"cron","expression":"99 * * * *"}}`,
	} {
		resp := request(params)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Errorf("params %s: error = %+v", params, resp.Error)
		}
	}
	if len(conn.notifications) != 0 {
		t.Fatalf("preview broadcast: %v", conn.notifications)
	}
}

func TestAutomationsPreviewSchemaAndMaxIterations(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		OneOf []struct {
			Ref string `json:"$ref"`
		} `json:"oneOf"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RemoteAutomationsPreviewRequest", "RemoteAutomationsPreviewResponse"} {
		found := false
		for _, entry := range schema.OneOf {
			found = found || entry.Ref == "#/$defs/"+name
		}
		if !found || len(schema.Defs[name]) == 0 {
			t.Errorf("schema missing %s", name)
		}
	}
	var input struct {
		Properties map[string]struct {
			Maximum int `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema.Defs["RemoteAutomationExtInput"], &input); err != nil {
		t.Fatal(err)
	}
	if input.Properties["maxIterations"].Maximum != 1000 {
		t.Fatalf("maxIterations maximum = %d", input.Properties["maxIterations"].Maximum)
	}
	for _, n := range []int{50, 51, 1000} {
		var in wire.AutomationExtInput
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"name":"test","prompt":"test","schedule":{"type":"daily","timeOfDay":"09:00"},"maxIterations":%d}`, n)), &in); err != nil {
			t.Fatal(err)
		}
		normalized, err := automationExtNormalizeInput(in)
		if err != nil || *normalized.MaxRuns != n || *normalized.MaxIterations != n {
			t.Fatalf("%d: normalized = %+v, err = %v", n, normalized, err)
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip wire.AutomationExtInput
		if err := json.Unmarshal(raw, &roundtrip); err != nil || *roundtrip.MaxRuns != n || *roundtrip.MaxIterations != n {
			t.Fatalf("%d: roundtrip = %+v, err = %v", n, roundtrip, err)
		}
	}
}
