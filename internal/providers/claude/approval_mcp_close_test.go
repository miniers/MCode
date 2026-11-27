package claude

import (
	"context"
	"testing"
	"time"
)

func TestApprovalServerCloseStopsWatcher(t *testing.T) {
	srv, err := startApprovalServer(context.Background(), nil)
	if err != nil {
		t.Fatalf("startApprovalServer failed: %v", err)
	}

	if err := srv.close(); err != nil {
		t.Fatalf("first close() failed: %v", err)
	}
	if err := srv.close(); err != nil {
		t.Fatalf("second close() failed: %v", err)
	}

	select {
	case <-srv.watcherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher goroutine did not exit after close()")
	}
}
