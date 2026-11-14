package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewMissingBlobAndFixedRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init")
	r := NewRunner("git", 2)
	base, err := r.ReviewRevision(ctx, root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("1", 40)
	head := run("100644 blob "+hash+"\tmissing.txt\n", "mktree", "--missing")
	files, err := r.ReviewFiles(ctx, root, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files[0].Missing || files[0].NewHash != hash {
		t.Fatalf("缺 blob 未标记: %+v", files)
	}
	old, newText, binary, large, missing, err := r.ReviewTexts(ctx, root, files[0])
	if err != nil || old != "" || newText != "" || binary || large || !missing {
		t.Fatalf("缺 blob 正文: %q %q %v %v %v %v", old, newText, binary, large, missing, err)
	}
	// 即使列表阶段以后对象消失，也不能返回单侧文本。
	_, _, _, _, missing, err = r.ReviewTexts(ctx, root, ReviewBlobFile{NewHash: hash})
	if err != nil || !missing {
		t.Fatalf("迟发缺 blob: %v %v", missing, err)
	}
	if err = os.WriteFile(filepath.Join(root, "file.txt"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, _, err := r.ReviewWorkingTree(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "file.txt"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err = r.ReviewFiles(ctx, root, base, first, nil)
	if err != nil || len(files) != 1 {
		t.Fatalf("固定版本: %+v %v", files, err)
	}
	_, text, _, _, _, err := r.ReviewTexts(ctx, root, files[0])
	if err != nil || text != "first\n" {
		t.Fatalf("快照发生漂移: %q %v", text, err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, _, err = r.ReviewWorkingTree(cancelled, root); err == nil {
		t.Fatal("取消未终止 Git 调用")
	}
}
