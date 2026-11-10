package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"mcode/internal/remote/wire"
)

type fakeAppUpdateExtService struct {
	status   wire.AppUpdateStatus
	checks   int
	installs int
}

func (s *fakeAppUpdateExtService) Status(context.Context) (wire.AppUpdateStatus, error) {
	return s.status, nil
}

func (s *fakeAppUpdateExtService) Check(context.Context) (wire.AppUpdateStatus, error) {
	s.checks++
	return s.status, nil
}

func (s *fakeAppUpdateExtService) Install(context.Context) error {
	s.installs++
	return nil
}

func TestAppUpdateMethods(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAppUpdateExtService{status: wire.AppUpdateStatus{Current: "1", Latest: "2", Available: true, Installable: true, Notes: "说明"}}
	backend.SetService(appUpdateExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	for _, method := range []string{"app.update.get", "app.update.check", "app.update.install"} {
		t.Run(method, func(t *testing.T) {
			resp := callExtension(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{},"id":1}`, method))
			if resp.Error != nil {
				t.Fatalf("调用失败: %+v", resp.Error)
			}
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				t.Fatal(err)
			}
			if method == "app.update.install" {
				var result wire.AppUpdateInstallResult
				if err := json.Unmarshal(raw, &result); err != nil || !result.Started || svc.installs != 1 {
					t.Fatalf("安装结果错误: %s, %v", raw, err)
				}
			} else {
				var result wire.AppUpdateStatus
				if err := json.Unmarshal(raw, &result); err != nil || result != svc.status {
					t.Fatalf("状态错误: %s, %v", raw, err)
				}
			}
			for _, params := range []string{`{"extra":true}`, `null`, `[]`} {
				resp = callExtension(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s,"id":1}`, method, params))
				if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
					t.Fatalf("参数 %s 应被拒绝: %+v", params, resp.Error)
				}
			}
		})
	}
	if svc.checks != 1 || svc.installs != 1 {
		t.Fatalf("调用次数错误: checks=%d installs=%d", svc.checks, svc.installs)
	}
}

func TestAppUpdateMethodsNotImplemented(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	for _, method := range []string{"app.update.get", "app.update.check", "app.update.install"} {
		resp := callExtension(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{},"id":1}`, method))
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("%s 未注入服务: %+v", method, resp.Error)
		}
	}
}
