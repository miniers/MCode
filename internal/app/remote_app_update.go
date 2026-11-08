package app

import (
	"context"
	"log"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

type remoteAppUpdateClient interface {
	AppVersion() string
	LatestUpdate() mcodeui.AppUpdateStatus
	UpdateNotes() string
	CheckForUpdate(context.Context) (mcodeui.AppUpdateStatus, error)
	InstallUpdate(context.Context, func(float64)) error
}

type remoteAppUpdateService struct {
	client remoteAppUpdateClient
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		a.backend.SetService("appUpdate", remoteAppUpdateService{client: a.remoteClient()})
	})
}

func (s remoteAppUpdateService) status(status mcodeui.AppUpdateStatus) wire.AppUpdateStatus {
	if status.Current == "" {
		status.Current = s.client.AppVersion()
	}
	return wire.AppUpdateStatus{
		Current: status.Current, Latest: status.Latest, Available: status.Available,
		Installable: status.Installable, URL: status.URL, Notes: s.client.UpdateNotes(),
	}
}

func (s remoteAppUpdateService) Status(ctx context.Context) (wire.AppUpdateStatus, error) {
	return s.status(s.client.LatestUpdate()), nil
}

func (s remoteAppUpdateService) Check(ctx context.Context) (wire.AppUpdateStatus, error) {
	status, err := s.client.CheckForUpdate(ctx)
	if err != nil {
		return wire.AppUpdateStatus{}, err
	}
	return s.status(status), nil
}

func (s remoteAppUpdateService) Install(ctx context.Context) error {
	status := s.client.LatestUpdate()
	if !status.Available || !status.Installable {
		return remote.ExtInvalidParams("没有可安装的更新")
	}
	go func() {
		if err := s.client.InstallUpdate(context.Background(), nil); err != nil {
			log.Printf("remote app update: 安装更新失败: %v", err)
		}
	}()
	return nil
}
