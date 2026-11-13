package mcodeui

import (
	"context"
	"testing"
)

type gatedExportClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (f *gatedExportClient) History(ctx context.Context, id string) ([]Message, error) {
	close(f.started)
	<-f.release
	close(f.done)
	return nil, context.Canceled
}
func TestExportStartToast(t *testing.T) {
	client := &gatedExportClient{FakeClient: NewFakeClient(), started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.exportThread("t1")
	<-client.started
	if got := recallTestToast(s); got != "正在导出…" {
		t.Fatalf("toast = %q", got)
	}
	close(client.release)
	<-client.done
}
