//go:build darwin || linux

package remote

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestStewardAttachmentCopyDoesNotHoldBackendLock(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.SetDataDir(t.TempDir())
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "项目", Path: t.TempDir()})
	id, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "attachment")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	b.attachments.register(&attachmentRecord{id: "attachment", name: "attachment.txt", mimeType: "text/plain", path: fifo, createdAt: time.Now()})
	writerReady := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		writerReady <- err
		if err == nil {
			<-release
			_ = f.Close()
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, _, err := b.appendStewardMessage(ctx, id, "user", "", "附件", []string{"attachment"})
		done <- err
	}()
	select {
	case err := <-writerReady:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		// 打开读写端以释放可能仍在等待的写端。
		f, _ := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
		close(release)
		if f != nil {
			_ = f.Close()
		}
		t.Fatal("等待附件复制超时")
	}
	readDone := make(chan struct{})
	go func() {
		b.StewardFeedThreadID()
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Error("附件复制期间后端锁被占用")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("附件复制未结束")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	b.attachments.register(&attachmentRecord{id: "missing", name: "missing.txt", path: missing, createdAt: time.Now()})
	if _, _, err := b.appendStewardMessage(ctx, id, "user", "", "附件", []string{"missing"}); err == nil {
		t.Fatal("复制不存在的附件应失败")
	}
	if _, ok := b.attachments.get("missing"); !ok {
		t.Fatal("复制失败后附件记录丢失")
	}
}
