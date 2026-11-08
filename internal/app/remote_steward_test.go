package app

import (
	"context"
	"testing"
)

type fakeStewardClient struct{ openThreadID string }

func (f *fakeStewardClient) OpenStewardThread(context.Context) (string, error) {
	return f.openThreadID, nil
}

func TestStewardServiceOpen(t *testing.T) {
	svc := remoteStewardService{client: &fakeStewardClient{openThreadID: "feed"}}
	id, err := svc.Open(context.Background(), "project")
	if err != nil || id != "feed" {
		t.Fatalf("打开前台: %q %v", id, err)
	}
	if _, err := (remoteStewardService{}).Open(context.Background(), "project"); err != errStewardServiceUnavailable {
		t.Fatalf("缺少客户端: %v", err)
	}
}
