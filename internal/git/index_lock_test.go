package git

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestClearStaleIndexLock(t *testing.T) {
	r, root := newRevertTestRepo(t)
	indexPath, err := r.IndexPath(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := indexPath + ".lock"
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleIndexLockAge - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	original := gitProcessRunning
	t.Cleanup(func() { gitProcessRunning = original })
	gitProcessRunning = func(context.Context) (bool, error) { return false, nil }
	cleared, err := r.ClearStaleIndexLock(context.Background(), root)
	if err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("残留锁仍存在: %v", err)
	}
}

func TestClearStaleIndexLockKeepsFreshOrActive(t *testing.T) {
	r, root := newRevertTestRepo(t)
	indexPath, err := r.IndexPath(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := indexPath + ".lock"
	original := gitProcessRunning
	t.Cleanup(func() { gitProcessRunning = original })
	for _, test := range []struct {
		name    string
		mtime   time.Time
		running bool
	}{
		{name: "fresh", mtime: time.Now(), running: false},
		{name: "active", mtime: time.Now().Add(-staleIndexLockAge - time.Second), running: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(lockPath, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(lockPath, test.mtime, test.mtime); err != nil {
				t.Fatal(err)
			}
			gitProcessRunning = func(context.Context) (bool, error) { return test.running, nil }
			cleared, err := r.ClearStaleIndexLock(context.Background(), root)
			if err != nil || cleared {
				t.Fatalf("cleared=%v err=%v", cleared, err)
			}
			if _, err := os.Stat(lockPath); err != nil {
				t.Fatalf("锁应保留: %v", err)
			}
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
		})
	}
}
