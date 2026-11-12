package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestProgressParsing(t *testing.T) {
	p := parseProgress("Receiving objects:  45% (9/20)")
	if p.Phase != "Receiving objects" || p.Percent != 45 {
		t.Fatal(p)
	}
	if parseProgress("remote: counting").Percent != -1 {
		t.Fatal("未知百分比")
	}
	args := []string{"fetch", "origin"}
	if !reflect.DeepEqual(progressArgs(args, nil), args) {
		t.Fatal("无回调时参数改变")
	}
}
func TestFetchProgressAndCancel(t *testing.T) {
	r, dir, _ := initSyncRemote(t)
	runTestGit(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	var lines []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { lines = append(lines, p) })
	if e := r.Fetch(ctx, dir); e != nil {
		t.Fatal(e)
	}
	if len(lines) == 0 {
		t.Fatal("没有进度回调")
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if e := r.Fetch(c, dir); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestRunningCommandCancellation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(bin, 1)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	_, err := r.run(WithProgress(ctx, func(Progress) {}), dir, nil, "fetch")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
