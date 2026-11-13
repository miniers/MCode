package git

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestParseConflictsAndResolve(t *testing.T) {
	tests := []struct {
		name, text   string
		count        int
		base         bool
		ours, theirs string
	}{
		{"普通", "a\n<<<<<<< ours\no\n=======\nt\n>>>>>>> theirs\nz\n", 1, false, "o\n", "t\n"},
		{"diff3", "<<<<<<< x\no\n||||||| base\nb\n=======\nt\n>>>>>>> y\n", 1, true, "o\n", "t\n"},
		{"多块", "<<<<<<<\na\n=======\nb\n>>>>>>>\n<<<<<<<\nc\n=======\nd\n>>>>>>>\n", 2, false, "a\n", "b\n"},
		{"长标记", "<<<<<<<< x\no\n========\nt\n>>>>>>>> y\n", 1, false, "o\n", "t\n"},
		{"CRLF", "<<<<<<< x\r\no\r\n=======\r\nt\r\n>>>>>>> y\r\n", 1, false, "o\r\n", "t\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseConflicts(tt.text)
			if len(got) != tt.count {
				t.Fatalf("冲突块数量：%d", len(got))
			}
			if got[0].HasBase != tt.base || !reflect.DeepEqual(got[0].Ours, []string{tt.ours}) || !reflect.DeepEqual(got[0].Theirs, []string{tt.theirs}) {
				t.Fatalf("冲突块：%+v", got[0])
			}
		})
	}
	text := "<<<<<<< x\no\n||||||| base\nb\n=======\nt\n>>>>>>> y\n<<<<<<< x\nx\n=======\ny\n>>>>>>> y\n"
	for _, tt := range []struct {
		choice ConflictChoice
		want   string
	}{{ConflictOurs, "o\n"}, {ConflictTheirs, "t\n"}, {ConflictBoth, "o\nt\n"}, {ConflictBase, "b\n"}} {
		want := tt.want + "<<<<<<< x\nx\n=======\ny\n>>>>>>> y\n"
		if got := ResolveRegions(text, map[int]ConflictChoice{0: tt.choice}); got != want {
			t.Errorf("%s: %q", tt.choice, got)
		}
	}
}
func TestConflictSidesAndResolveFileSide(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "UU", true: "DU"}[deleted], func(t *testing.T) {
			r, dir := setupTestRepo(t)
			ctx := context.Background()
			path := "README.md"
			write := func(s string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, path), []byte(s), 0644); err != nil {
					t.Fatal(err)
				}
			}
			runGitOutput(t, r, dir, "checkout", "-b", "side")
			write("side\n")
			runGitOutput(t, r, dir, "commit", "-am", "side")
			runGitOutput(t, r, dir, "checkout", "-")
			if deleted {
				runGitOutput(t, r, dir, "rm", path)
				runGitOutput(t, r, dir, "commit", "-m", "delete")
			} else {
				write("ours\n")
				runGitOutput(t, r, dir, "commit", "-am", "ours")
			}
			if _, err := r.run(ctx, dir, nil, "merge", "side"); err == nil {
				t.Fatal("应当冲突")
			}
			files, err := r.ConflictFiles(ctx, dir)
			if err != nil || len(files) != 1 {
				t.Fatalf("冲突文件：%v %v", files, err)
			}
			ours, base, theirs, err := r.ConflictSides(ctx, dir, path)
			if err != nil || base == nil || string(theirs) != "side\n" || (deleted && ours != nil) {
				t.Fatalf("三侧：%q %q %q %v", ours, base, theirs, err)
			}
			if err := r.ResolveFileSide(ctx, dir, path, true); err != nil {
				t.Fatal(err)
			}
			files, err = r.ConflictFiles(ctx, dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("未解决：%v %v", files, err)
			}
		})
	}
}

func TestWriteWorktreeFile(t *testing.T) {
	r, dir := setupTestRepo(t)
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteWorktreeFile(context.Background(), dir, "README.md", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("写入内容 = %q, %v", got, err)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("权限未保留: %o", info.Mode().Perm())
	}
	for _, p := range []string{"../x", ".git/config"} {
		if err := r.WriteWorktreeFile(context.Background(), dir, p, []byte("x")); err == nil {
			t.Fatalf("路径应被拒绝: %s", p)
		}
	}
}

func TestWriteWorktreeFileRejectsLinksAndCanceledContext(t *testing.T) {
	r, dir := setupTestRepo(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside-link": outside, "git-link": filepath.Join(dir, ".git", "config")} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		if err := r.WriteWorktreeFile(context.Background(), dir, name, []byte("changed")); err == nil {
			t.Fatalf("应拒绝链接 %s", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.WriteWorktreeFile(ctx, dir, "README.md", []byte("changed")); err == nil {
		t.Fatal("已取消请求仍然写入")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "keep" {
		t.Fatalf("越界文件被修改: %q %v", data, err)
	}
}
