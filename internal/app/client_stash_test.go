package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientStashPushMessage(t *testing.T) {
	client, root, threadID := branchClient(t)
	writeRevertRepoFile(t, root, "file.txt", "changed\n")
	writeRevertRepoFile(t, root, "untracked.txt", "new\n")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.GitStashPush(ctx, threadID, "自定义说明"); err != nil {
		t.Fatal(err)
	}
	entries, err := client.GitStashList(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "自定义说明") {
		t.Fatalf("贮藏说明未保存：%+v", entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(data) != "base\n" {
		t.Fatalf("已跟踪改动未贮藏：%q，%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("未跟踪文件没有贮藏：%v", err)
	}
	if err := client.GitStashPush(ctx, "missing-thread", "说明"); err == nil {
		t.Fatal("无工作目录的会话应拒绝贮藏")
	}
	if err := NewClient(nil).GitStashPush(ctx, threadID, "说明"); err == nil {
		t.Fatal("缺少 backend 时应拒绝贮藏")
	}
}
