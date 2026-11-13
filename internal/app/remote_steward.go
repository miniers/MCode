package app

import (
	"context"
	"errors"
)

type stewardExtClient interface {
	OpenStewardThread(ctx context.Context) (string, error)
	ResetStewardThread(ctx context.Context) (string, error)
}

type remoteStewardService struct {
	client stewardExtClient
}

var errStewardServiceUnavailable = errors.New("总管服务不可用")

func (s remoteStewardService) Open(ctx context.Context, projectID string) (string, error) {
	return s.OpenStewardThread(ctx)
}

func (s remoteStewardService) Reset(ctx context.Context, projectID string) (string, error) {
	return s.ResetStewardThread(ctx)
}

func (s remoteStewardService) OpenStewardThread(ctx context.Context) (string, error) {
	if s.client == nil {
		return "", errStewardServiceUnavailable
	}
	return s.client.OpenStewardThread(ctx)
}

func (s remoteStewardService) ResetStewardThread(ctx context.Context) (string, error) {
	if s.client == nil {
		return "", errStewardServiceUnavailable
	}
	return s.client.ResetStewardThread(ctx)
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		client := a.remoteClient()
		if client != nil {
			a.backend.SetService("steward", remoteStewardService{client: client})
		}
	})
}
