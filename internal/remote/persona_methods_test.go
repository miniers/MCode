package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// fakePersonaLister 是注入用的假人设目录服务，记录调用参数方便断言。
// byPair 非 nil 时按 "provider|model" 返回不同列表，没命中的组合按「不是 Magpie 端点」返回空列表。
type fakePersonaLister struct {
	personas []wire.Persona
	byPair   map[string][]wire.Persona
	err      error
	calls    int
	provider string
	model    string
	force    bool
}

func (f *fakePersonaLister) Personas(ctx context.Context, provider, model string, force bool) ([]wire.Persona, error) {
	f.calls++
	f.provider = provider
	f.model = model
	f.force = force
	if f.byPair != nil {
		return f.byPair[provider+"|"+model], f.err
	}
	return f.personas, f.err
}

// newPersonaTestBackend 造一个带项目的 MemoryBackend，并返回新建会话的 ID。
func newPersonaTestBackend(t *testing.T) (*MemoryBackend, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "proj", Path: t.TempDir()})
	created, err := backend.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("create thread failed: %v", err)
	}
	return backend, created.ThreadID
}

// newPersonaTestDispatcher 用真实 MemoryBackend 构造 Dispatcher，conn 按已认证处理。
func newPersonaTestDispatcher(backend *MemoryBackend) (*Dispatcher, Conn) {
	return NewDispatcher(&fakeAuth{}, backend), &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
}

func TestProvidersPersonas_ListSuccess(t *testing.T) {
	backend, _ := newPersonaTestBackend(t)
	svc := &fakePersonaLister{personas: []wire.Persona{
		{ID: "reviewer", Name: "审查员"},
		{ID: "tutor", Name: "导师", Description: "讲题"},
	}}
	backend.SetService(personaServiceName, svc)
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"providers.personas","params":{"provider":"pi:magpie","model":"magpie/glm"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.PersonasListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Personas) != 2 || result.Personas[0].ID != "reviewer" || result.Personas[1].Description != "讲题" {
		t.Fatalf("unexpected personas: %s", raw)
	}
	if svc.calls != 1 || svc.provider != "pi:magpie" || svc.model != "magpie/glm" {
		t.Fatalf("service called with unexpected args: %+v", svc)
	}
	if svc.force {
		t.Fatal("providers.personas 未带 force 时不应请求强制刷新")
	}

	// 服务返回 nil 时输出空数组，客户端不必处理 null。
	svc.personas = nil
	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"providers.personas","params":{"provider":"pi:magpie","model":"magpie/glm","force":true},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(raw), `"personas":[]`) {
		t.Fatalf("empty list should marshal as [], got %s", raw)
	}
	if !svc.force {
		t.Fatal("force 参数没有传给服务")
	}
}

func TestProvidersPersonas_NoService(t *testing.T) {
	backend, _ := newPersonaTestBackend(t)
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"providers.personas","params":{"provider":"pi:magpie","model":"magpie/glm"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("未注入服务时应返回 MethodNotFound，得到 %+v", resp.Error)
	}
}

func TestThreadsSetPersona_KeepsOtherPrefs(t *testing.T) {
	backend, threadID := newPersonaTestBackend(t)
	svc := &fakePersonaLister{personas: []wire.Persona{{ID: "reviewer", Name: "审查员"}}}
	backend.SetService(personaServiceName, svc)

	if _, err := backend.SetThreadComposerPrefs(context.Background(), threadID, ThreadComposerPrefs{
		InteractionMode: "plan",
		Effort:          "high",
	}); err != nil {
		t.Fatalf("seed composer prefs failed: %v", err)
	}
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":"reviewer"},"id":2}`, threadID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var summary wire.ThreadSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Persona != "reviewer" {
		t.Fatalf("persona 未写入，得到 %q", summary.Persona)
	}
	// 其它会话偏好必须保持原样（与桌面端只改 Persona 字段一致）。
	if summary.InteractionMode != "plan" || derefString(summary.Effort) != "high" {
		t.Fatalf("其它 prefs 被改动: interactionMode=%q effort=%q", summary.InteractionMode, derefString(summary.Effort))
	}
	stored, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("get thread failed: %v", err)
	}
	if stored.Persona != "reviewer" || stored.InteractionMode != "plan" || derefString(stored.Effort) != "high" {
		t.Fatalf("后端状态不对: %+v", stored)
	}

	// 空串表示清除人设，其它字段同样不动。
	resp = callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":""},"id":3}`, threadID))
	if resp.Error != nil {
		t.Fatalf("clear persona failed: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	summary = wire.ThreadSummary{}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Persona != "" || summary.InteractionMode != "plan" || derefString(summary.Effort) != "high" {
		t.Fatalf("清除人设后摘要不对: %+v", summary)
	}
	// 清除不需要校验目录，服务不应被再次调用。
	if svc.calls != 1 {
		t.Fatalf("清除人设不该请求人设目录，调用次数 %d", svc.calls)
	}
}

func TestThreadsSetPersona_RejectsUnknownPersona(t *testing.T) {
	backend, threadID := newPersonaTestBackend(t)
	backend.SetService(personaServiceName, &fakePersonaLister{
		personas: []wire.Persona{{ID: "reviewer", Name: "审查员"}},
	})
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":"ghost"},"id":2}`, threadID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("列表里没有的人设应返回 InvalidParams，得到 %+v", resp.Error)
	}
	// 校验失败时不能落库。
	stored, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("get thread failed: %v", err)
	}
	if stored.Persona != "" {
		t.Fatalf("非法人设不该写入，得到 %q", stored.Persona)
	}
}

// 弹窗里刚换了模型（选择存在 composerOptions，要等下一次发送才写回会话）时，
// 列表按新 provider/model 拉、写入却按会话旧值校验，就会把刚看到的人设判成未知。
// 客户端把列表用的 provider/model 一并带上来后必须能切换成功。
func TestThreadsSetPersona_UsesClientProviderModel(t *testing.T) {
	backend, threadID := newPersonaTestBackend(t)
	svc := &fakePersonaLister{byPair: map[string][]wire.Persona{
		"pi|magpie-jp/auto/deepseek-v4.1-flash": {{ID: "xiaomajiang", Name: "小码酱 · YG 专属"}},
	}}
	backend.SetService(personaServiceName, svc)
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":"xiaomajiang","provider":"pi","model":"magpie-jp/auto/deepseek-v4.1-flash"},"id":2}`,
		threadID))
	if resp.Error != nil {
		t.Fatalf("列表里有过的人设应能切换，得到 %+v", resp.Error)
	}
	if svc.provider != "pi" || svc.model != "magpie-jp/auto/deepseek-v4.1-flash" {
		t.Fatalf("应先按客户端送来的 provider/model 取目录，得到 provider=%q model=%q", svc.provider, svc.model)
	}
	stored, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("get thread failed: %v", err)
	}
	if stored.Persona != "xiaomajiang" {
		t.Fatalf("人设未写入，得到 %q", stored.Persona)
	}

	// 会话自己的 provider/model 读不到目录（不是 Magpie 端点）时不阻拦：与桌面端口径一致，
	// 桌面端只按当前列表渲染，写回时不做二次校验。
	resp = callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":"reviewer"},"id":3}`, threadID))
	if resp.Error != nil {
		t.Fatalf("目录读不到时不应拒绝，得到 %+v", resp.Error)
	}
}

func TestThreadsSetPersona_RejectsInvalidThreadID(t *testing.T) {
	backend, _ := newPersonaTestBackend(t)
	backend.SetService(personaServiceName, &fakePersonaLister{
		personas: []wire.Persona{{ID: "reviewer", Name: "审查员"}},
	})
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":"not-a-uuid","persona":"reviewer"},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("非法 threadID 应返回 InvalidParams，得到 %+v", resp.Error)
	}
}

func TestThreadsSetPersona_NoService(t *testing.T) {
	backend, threadID := newPersonaTestBackend(t)
	d, conn := newPersonaTestDispatcher(backend)

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"threads.setPersona","params":{"threadID":%q,"persona":"reviewer"},"id":2}`, threadID))
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("未注入服务时应返回 MethodNotFound，得到 %+v", resp.Error)
	}
}
