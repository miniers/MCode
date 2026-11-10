package app

import (
	"fmt"
	"testing"
	"time"

	"mcode/internal/remote"
)

func TestRemoteServiceRevokeDuringPushInitialization(t *testing.T) {
	service := NewRemoteService(t.TempDir(), nil)
	service.mu.Lock()
	auth, err := service.ensureAuthLocked()
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				service.mu.Lock()
				service.push = nil
				service.mu.Unlock()
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("device-%d", i)
		if err := auth.Store().Add(remote.DeviceRecord{DeviceID: id, PairedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if revoked, err := auth.RevokeDevice(id); err != nil || !revoked {
			t.Fatalf("revoke %s: revoked=%v, error=%v", id, revoked, err)
		}
	}
}
