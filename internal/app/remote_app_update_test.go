package app

import (
	"context"
	"errors"
	"testing"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

type fakeRemoteAppUpdateClient struct {
	status mcodeui.AppUpdateStatus
	notes  string
}

func (c *fakeRemoteAppUpdateClient) AppVersion() string                    { return "1" }
func (c *fakeRemoteAppUpdateClient) LatestUpdate() mcodeui.AppUpdateStatus { return c.status }
func (c *fakeRemoteAppUpdateClient) UpdateNotes() string                   { return c.notes }
func (c *fakeRemoteAppUpdateClient) CheckForUpdate(context.Context) (mcodeui.AppUpdateStatus, error) {
	return c.status, nil
}
func (c *fakeRemoteAppUpdateClient) InstallUpdate(context.Context, func(float64)) error { return nil }

func TestRemoteAppUpdateStatus(t *testing.T) {
	svc := remoteAppUpdateService{client: &fakeRemoteAppUpdateClient{
		status: mcodeui.AppUpdateStatus{Latest: "2", Available: true}, notes: "发布说明",
	}}
	status, err := svc.Status(context.Background())
	if err != nil || status.Current != "1" || status.Latest != "2" || status.Notes != "发布说明" {
		t.Fatalf("状态错误: %+v, %v", status, err)
	}
}

func TestRemoteAppUpdateInstallUnavailable(t *testing.T) {
	for _, status := range []mcodeui.AppUpdateStatus{{}, {Available: true}, {Installable: true}} {
		svc := remoteAppUpdateService{client: &fakeRemoteAppUpdateClient{status: status}}
		err := svc.Install(context.Background())
		var extErr *remote.ExtError
		if !errors.As(err, &extErr) || extErr.Code != wire.InvalidParams {
			t.Fatalf("不可安装时应返回参数错误: %v", err)
		}
	}
}
