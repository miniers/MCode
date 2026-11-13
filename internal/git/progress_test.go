package git

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_GIT_RUN_HELPER") == "1" {
		// 测试二进制充当 git 替身：阻塞到调用方取消上下文。
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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
	bin := os.Args[0]
	r := NewRunner(bin, 1)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	_, err := r.run(WithProgress(ctx, func(Progress) {}), dir, []string{"GO_WANT_GIT_RUN_HELPER=1"}, "fetch")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
