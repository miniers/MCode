package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDanglingLinksStayWithinRoots(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		name, target string
		allowed      bool
	}{
		{"outside", outside, false}, {"inside", "missing", true},
	} {
		if err := os.Symlink(tc.target, filepath.Join(root, tc.name)); err != nil {
			t.Fatal(err)
		}
		p, err := resolve(Env{WorkDir: root}, tc.name)
		if tc.allowed && (err != nil || p != filepath.Join(canonicalRoot, "missing")) {
			t.Fatalf("%s: %s %v", tc.name, p, err)
		}
		if !tc.allowed && (err == nil || !strings.Contains(err.Error(), "path outside allowed directories")) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestAtomicWritePreservesLinkAndMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link: %v %v", info, err)
	}
	info, err = os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("content: %q %v", data, err)
	}
}

func TestAtomicToolWritesThroughSymlink(t *testing.T) {
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	env := Env{WorkDir: root}
	for _, tc := range []struct {
		run  func(context.Context, Env, json.RawMessage) (Result, error)
		args string
		want string
	}{
		{writeFile, `{"path":"link","content":"write\n"}`, "write\n"},
		{editFile, `{"path":"link","edits":[{"oldText":"write","newText":"edit"}]}`, "edit\n"},
		{applyPatch, `{"input":"*** Begin Patch\n*** Update File: link\n@@\n-edit\n+patch\n*** End Patch"}`, "patch\n"},
	} {
		result, err := tc.run(context.Background(), env, json.RawMessage(tc.args))
		if err != nil || result.IsError {
			t.Fatalf("result: %+v %v", result, err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != tc.want {
			t.Fatalf("content: %q %v", data, err)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link: %v %v", info, err)
		}
		info, err = os.Stat(target)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("mode: %v %v", info, err)
		}
	}
}

func TestSearchTimeoutFallsBackAndSkipsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".git", "node_modules", ".venv", "venv", "__pycache__", "dist", "build", "target", ".next", ".cache", "vendor"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "skip"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	previous := gitSearchTimeout
	gitSearchTimeout = time.Nanosecond
	defer func() { gitSearchTimeout = previous }()
	files, err := candidates(context.Background(), Env{WorkDir: root}, root)
	if err != nil || len(files) != 1 || files[0] != filepath.Join(root, "keep") {
		t.Fatalf("files: %v %v", files, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := candidates(ctx, Env{WorkDir: root}, root); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestReadContinuationAndOffsetBeyondEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte(strings.Repeat("x\n", maxOutputLines+1)), 0600); err != nil {
		t.Fatal(err)
	}
	env := Env{WorkDir: root}
	result, err := readFile(context.Background(), env, json.RawMessage(`{"path":"file"}`))
	if err != nil || result.IsError || !strings.Contains(result.Text, "continue with offset=401") {
		t.Fatalf("truncation: %v %+v", err, result)
	}
	t.Run("explicit limit", func(t *testing.T) {
		result, err := readFile(context.Background(), env, json.RawMessage(`{"path":"file","limit":2000}`))
		if err != nil || result.IsError || !strings.Contains(result.Text, "More lines available; use offset 2001 to continue reading.") {
			t.Fatalf("continuation: %v %+v", err, result)
		}
	})
	result, err = readFile(context.Background(), env, json.RawMessage(fmt.Sprintf(`{"path":"file","offset":%d}`, maxOutputLines+2)))
	if err != nil || !result.IsError || !strings.Contains(result.Text, fmt.Sprintf("offset %d is beyond end of file (%d lines)", maxOutputLines+2, maxOutputLines+1)) {
		t.Fatalf("offset: %v %+v", err, result)
	}
}

func TestReadByteLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte(strings.Repeat("x", maxOutputBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, args string
		limit      int
	}{
		{"default", `{"path":"file"}`, readDefaultBytes},
		{"explicit", `{"path":"file","limit":1}`, maxOutputBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := readFile(context.Background(), Env{WorkDir: root}, json.RawMessage(tc.args))
			if err != nil || result.IsError {
				t.Fatalf("read: %v %+v", err, result)
			}
			notice := "\nOutput truncated; continue with offset=2"
			if !strings.HasSuffix(result.Text, notice) || len(result.Text) != tc.limit+len(notice) {
				t.Fatalf("unexpected output length or notice: length=%d", len(result.Text))
			}
		})
	}
}

func TestReadDefaultSmallFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("first\nsecond"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := readFile(context.Background(), Env{WorkDir: root}, json.RawMessage(`{"path":"file"}`))
	if err != nil || result.IsError || result.Text != "1: first\n2: second\n" {
		t.Fatalf("read: %v %+v", err, result)
	}
}

func TestWebFetchRejectsMetadata(t *testing.T) {
	for _, address := range []string{"169.254.169.254", "[fe80::1]", "[fd00:ec2::254]"} {
		result, err := webFetch(context.Background(), Env{}, json.RawMessage(fmt.Sprintf(`{"url":"http://%s/"}`, address)))
		if err != nil || !result.IsError || !strings.Contains(result.Text, "web_fetch 不允许访问链路本地或云元数据地址") {
			t.Fatalf("%s: %+v %v", address, result, err)
		}
	}
}
