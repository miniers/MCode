package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
)

// swarmExtTestLead 是一个合法 UUID，用来覆盖参数校验之后的成功路径。
const swarmExtTestLead = "11111111-1111-1111-1111-111111111111"

// swarmExtTestService 记录调用参数并按预设返回，替代 app 层注入的真实实现。
type swarmExtTestService struct {
	bees      []wire.SwarmExtInterruptedBee
	count     int
	resumed   int
	dismissed int
	err       error

	gotLead string
	calls   []string

	// 失败子代理「沿用改动续做」的调用记录与预设错误。
	retryLead string
	retryBee  string
	retryErr  error

	// threads.swarm.report 的调用记录与预设结果。
	reportThread string
	reportBee    string
	reportText   string
	reportName   string
	reportErr    error
}

func (s *swarmExtTestService) Interrupted(ctx context.Context, leadThreadID string) ([]wire.SwarmExtInterruptedBee, int, error) {
	s.gotLead = leadThreadID
	s.calls = append(s.calls, "interrupted")
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.bees, s.count, nil
}

func (s *swarmExtTestService) ResumeInterrupted(ctx context.Context, leadThreadID string) (int, error) {
	s.gotLead = leadThreadID
	s.calls = append(s.calls, "resume")
	if s.err != nil {
		return 0, s.err
	}
	return s.resumed, nil
}

func (s *swarmExtTestService) DismissInterrupted(ctx context.Context, leadThreadID string) (int, error) {
	s.gotLead = leadThreadID
	s.calls = append(s.calls, "dismiss")
	if s.err != nil {
		return 0, s.err
	}
	return s.dismissed, nil
}

func (s *swarmExtTestService) RetryBee(ctx context.Context, threadID, beeThreadID string) error {
	s.retryLead = threadID
	s.retryBee = beeThreadID
	s.calls = append(s.calls, "retryBee")
	return s.retryErr
}

func (s *swarmExtTestService) ReportFull(ctx context.Context, threadID, beeID string) (wire.SwarmExtReportResult, error) {
	s.reportThread = threadID
	s.reportBee = beeID
	s.calls = append(s.calls, "report")
	if s.reportErr != nil {
		return wire.SwarmExtReportResult{}, s.reportErr
	}
	return wire.SwarmExtReportResult{BeeID: beeID, Name: s.reportName, Text: s.reportText, Truncated: false}, nil
}

// newSwarmExtTestDispatcher 构造带 MemoryBackend 的 Dispatcher，便于按需注入/移除服务。
func newSwarmExtTestDispatcher(t *testing.T) (*Dispatcher, *MemoryBackend) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	return NewDispatcher(&fakeAuth{}, backend), backend
}

// swarmExtResultJSON 把响应结果重新编码成 JSON 字符串，便于按字段断言。
func swarmExtResultJSON(t *testing.T, resp wire.Response) string {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(raw)
}

func TestThreadsSwarmInterruptedRejectsBadParams(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	// 服务已注入，确保失败来自参数校验而不是「方法未实现」。
	backend.SetService(swarmExtServiceName, &swarmExtTestService{})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []struct {
		name string
		raw  string
	}{
		{"missingThreadID", `{"jsonrpc":"2.0","method":"threads.swarm.interrupted","params":{},"id":1}`},
		{"notUUID", `{"jsonrpc":"2.0","method":"threads.swarm.interrupted","params":{"threadID":"lead-1"},"id":2}`},
		{"unknownField", `{"jsonrpc":"2.0","method":"threads.swarm.interrupted","params":{"threadID":"` + swarmExtTestLead + `","extra":1},"id":3}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn, tc.raw)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
}

func TestThreadsSwarmInterruptedWithoutService(t *testing.T) {
	d, _ := newSwarmExtTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 未注入服务时三条方法都按未实现处理，前端据此隐藏续跑入口。
	for _, method := range []string{"threads.swarm.interrupted", "threads.swarm.resume", "threads.swarm.dismissInterrupted"} {
		raw := `{"jsonrpc":"2.0","method":"` + method + `","params":{"threadID":"` + swarmExtTestLead + `"},"id":1}`
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("%s: expected MethodNotFound, got: %+v", method, resp.Error)
		}
	}
}

func TestThreadsSwarmInterruptedSuccess(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	svc := &swarmExtTestService{
		count: 3,
		bees: []wire.SwarmExtInterruptedBee{{
			ThreadID: "22222222-2222-2222-2222-222222222222",
			Name:     "bee-1",
			Task:     "补测试",
		}},
	}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.interrupted","params":{"threadID":"`+swarmExtTestLead+`"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := swarmExtResultJSON(t, resp)
	for _, want := range []string{`"count":3`, `"name":"bee-1"`, `"task":"补测试"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("result %s should contain %s", result, want)
		}
	}
	if svc.gotLead != swarmExtTestLead {
		t.Fatalf("service should receive lead id, got %q", svc.gotLead)
	}
}

func TestThreadsSwarmResumeAndDismissSuccess(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	svc := &swarmExtTestService{resumed: 2, dismissed: 1}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.resume","params":{"threadID":"`+swarmExtTestLead+`"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected resume error: %+v", resp.Error)
	}
	if result := swarmExtResultJSON(t, resp); !strings.Contains(result, `"resumed":2`) {
		t.Fatalf("unexpected resume result: %s", result)
	}

	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.dismissInterrupted","params":{"threadID":"`+swarmExtTestLead+`"},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected dismiss error: %+v", resp.Error)
	}
	if result := swarmExtResultJSON(t, resp); !strings.Contains(result, `"dismissed":1`) {
		t.Fatalf("unexpected dismiss result: %s", result)
	}
	if len(svc.calls) != 2 || svc.calls[0] != "resume" || svc.calls[1] != "dismiss" {
		t.Fatalf("unexpected service calls: %v", svc.calls)
	}
}

func TestThreadsSwarmResumeServiceError(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	backend.SetService(swarmExtServiceName, &swarmExtTestService{err: swarm.ErrLeadNotFound})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 业务错误转成拒绝错误，前端能直接展示原因而不是一句笼统的内部错误。
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.resume","params":{"threadID":"`+swarmExtTestLead+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected OperationRejected, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "lead") {
		t.Fatalf("error message should carry business reason, got %q", resp.Error.Message)
	}

	// 已有 *ExtError 原样透传。
	extErr := ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	wrapped := swarmExtWrapError(extErr)
	var got *ExtError
	if !errors.As(wrapped, &got) || got.Code != wire.InvalidParams {
		t.Fatalf("ExtError should pass through, got %+v", wrapped)
	}
}

// swarmExtTestBee 是第二个合法 UUID，用于续做参数的 beeThreadID。
const swarmExtTestBee = "33333333-3333-3333-3333-333333333333"

// 验证 threads.swarm.retryBee 对 threadID / beeThreadID 都做 UUID 校验，且拒绝多余字段。
func TestThreadsSwarmRetryBeeRejectsBadParams(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	// 服务已注入，确保失败来自参数校验而不是「方法未实现」。
	svc := &swarmExtTestService{}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []struct {
		name string
		raw  string
	}{
		{"missingBeeThreadID", `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"` + swarmExtTestLead + `"},"id":1}`},
		{"missingThreadID", `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"beeThreadID":"` + swarmExtTestBee + `"},"id":2}`},
		{"threadNotUUID", `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"lead-1","beeThreadID":"` + swarmExtTestBee + `"},"id":3}`},
		{"beeNotUUID", `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"` + swarmExtTestLead + `","beeThreadID":"bee-1"},"id":4}`},
		{"unknownField", `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"` + swarmExtTestLead + `","beeThreadID":"` + swarmExtTestBee + `","extra":1},"id":5}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn, tc.raw)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
	if len(svc.calls) != 0 {
		t.Fatalf("参数不合法时不应调用服务，实际 %v", svc.calls)
	}
}

// 验证未注入续跑服务时 threads.swarm.retryBee 返回 MethodNotFound，前端据此隐藏入口。
func TestThreadsSwarmRetryBeeWithoutService(t *testing.T) {
	d, _ := newSwarmExtTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 参数里的 threadID 可以是主会话，也可以是 bee 自己的会话。
	for _, threadID := range []string{swarmExtTestLead, swarmExtTestBee} {
		raw := `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"` + threadID + `","beeThreadID":"` + swarmExtTestBee + `"},"id":1}`
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
		}
	}
}

// 验证成功路径：参数原样交给服务、返回 ok=true。归一化由服务侧按 BeeOwner 完成。
func TestThreadsSwarmRetryBeeSuccess(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	svc := &swarmExtTestService{}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"`+swarmExtTestBee+`","beeThreadID":"`+swarmExtTestBee+`"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if result := swarmExtResultJSON(t, resp); !strings.Contains(result, `"ok":true`) {
		t.Fatalf("unexpected result: %s", result)
	}
	if svc.retryLead != swarmExtTestBee || svc.retryBee != swarmExtTestBee {
		t.Fatalf("service should receive raw thread ids, got lead=%q bee=%q", svc.retryLead, svc.retryBee)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "retryBee" {
		t.Fatalf("unexpected service calls: %v", svc.calls)
	}
}

// 验证续做失败时把业务错误转成可读的拒绝错误，前端能直接展示原因。
func TestThreadsSwarmRetryBeeServiceError(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	backend.SetService(swarmExtServiceName, &swarmExtTestService{retryErr: swarm.ErrBeeNotFound})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.retryBee","params":{"threadID":"`+swarmExtTestLead+`","beeThreadID":"`+swarmExtTestBee+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected OperationRejected, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "bee") {
		t.Fatalf("error message should carry business reason, got %q", resp.Error.Message)
	}
}

// 验证 threads.swarm.report 对 threadID / beeID 都做 UUID 校验，且拒绝多余字段。
func TestThreadsSwarmReportRejectsBadParams(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	svc := &swarmExtTestService{}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []struct {
		name string
		raw  string
	}{
		{"missingThreadID", `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"beeID":"` + swarmExtTestBee + `"},"id":1}`},
		{"missingBeeID", `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"` + swarmExtTestLead + `"},"id":2}`},
		{"threadNotUUID", `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"lead-1","beeID":"` + swarmExtTestBee + `"},"id":3}`},
		{"beeNotUUID", `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"` + swarmExtTestLead + `","beeID":"bee-1"},"id":4}`},
		{"unknownField", `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"` + swarmExtTestLead + `","beeID":"` + swarmExtTestBee + `","extra":1},"id":5}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn, tc.raw)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
	if len(svc.calls) != 0 {
		t.Fatalf("参数不合法时不应调用服务，实际 %v", svc.calls)
	}
}

// 验证未注入服务时 threads.swarm.report 返回 MethodNotFound，前端据此不显示「看全文」。
func TestThreadsSwarmReportWithoutService(t *testing.T) {
	d, _ := newSwarmExtTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	raw := `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"` + swarmExtTestLead + `","beeID":"` + swarmExtTestBee + `"},"id":1}`
	resp := callExtension(t, d, conn, raw)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// 验证成功路径：返回存档全文与 bee 名字，truncated 恒为 false，参数原样交给服务。
func TestThreadsSwarmReportSuccess(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	svc := &swarmExtTestService{reportName: "Aspen", reportText: "## 结论\n- 完成 A\n- 完成 B"}
	backend.SetService(swarmExtServiceName, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"`+swarmExtTestLead+`","beeID":"`+swarmExtTestBee+`"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := swarmExtResultJSON(t, resp)
	for _, want := range []string{`"beeID":"` + swarmExtTestBee + `"`, `"name":"Aspen"`, `"text":"## 结论\n- 完成 A\n- 完成 B"`, `"truncated":false`} {
		if !strings.Contains(result, want) {
			t.Fatalf("result %s should contain %s", result, want)
		}
	}
	if svc.reportThread != swarmExtTestLead || svc.reportBee != swarmExtTestBee {
		t.Fatalf("service should receive raw thread ids, got thread=%q bee=%q", svc.reportThread, svc.reportBee)
	}
}

// 验证 bee 不属于该主会话时按参数错误拒绝，前端提示而不是空结果。
func TestThreadsSwarmReportRejectsForeignBee(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	backend.SetService(swarmExtServiceName, &swarmExtTestService{
		reportErr: ExtInvalidParams("Invalid params: 'beeID' does not belong to 'threadID'"),
	})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"`+swarmExtTestLead+`","beeID":"`+swarmExtTestBee+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
}

// 验证存档缺失时错误码稳定，前端能按 ResourceNotFound 提示「找不到完整汇报」。
func TestThreadsSwarmReportArchiveMissing(t *testing.T) {
	d, backend := newSwarmExtTestDispatcher(t)
	backend.SetService(swarmExtServiceName, &swarmExtTestService{
		reportErr: &ExtError{Code: wire.ErrCodeResourceNotFound, Message: "找不到这条子代理的完整汇报"},
	})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.report","params":{"threadID":"`+swarmExtTestLead+`","beeID":"`+swarmExtTestBee+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ResourceNotFound, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "完整汇报") {
		t.Fatalf("error message should be user readable, got %q", resp.Error.Message)
	}
}
