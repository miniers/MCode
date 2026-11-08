package remote

import (
	"bytes"
	"context"
	"encoding/json"

	"mcode/internal/remote/wire"
)

// app.update.install 会让桌面端下载更新并重启，远程连接会因此断开。
const appUpdateExtServiceName = "appUpdate"

type appUpdateExtService interface {
	Status(ctx context.Context) (wire.AppUpdateStatus, error)
	Check(ctx context.Context) (wire.AppUpdateStatus, error)
	Install(ctx context.Context) error
}

func init() {
	RegisterMethod("app.update.get", handleAppUpdateGet)
	RegisterMethod("app.update.check", handleAppUpdateCheck)
	RegisterMethod("app.update.install", handleAppUpdateInstall)
}

func appUpdateService(d *Dispatcher, params json.RawMessage, method string) (appUpdateExtService, error) {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || trimmed[0] != '{' || !checkEmptyParams(params) {
		return nil, ExtInvalidParams("Invalid params: '" + method + "' takes no parameters")
	}
	svc, ok := backendService[appUpdateExtService](d.backend, appUpdateExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	return svc, nil
}

func handleAppUpdateGet(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	svc, err := appUpdateService(d, params, "app.update.get")
	if err != nil {
		return nil, err
	}
	return svc.Status(ctx)
}

func handleAppUpdateCheck(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	svc, err := appUpdateService(d, params, "app.update.check")
	if err != nil {
		return nil, err
	}
	return svc.Check(ctx)
}

func handleAppUpdateInstall(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	svc, err := appUpdateService(d, params, "app.update.install")
	if err != nil {
		return nil, err
	}
	if err := svc.Install(ctx); err != nil {
		return nil, err
	}
	return wire.AppUpdateInstallResult{Started: true}, nil
}
