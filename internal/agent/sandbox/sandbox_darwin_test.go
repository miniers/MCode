//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"mcode/internal/harness"
)

func TestReadOnlyConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	results := make([]harness.Sandbox, 16)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = ReadOnly()
		}()
	}
	wg.Wait()
	for _, got := range results {
		if got != results[0] {
			t.Fatal("ReadOnly returned different instances")
		}
		if got != nil && !IsReadOnly(got) {
			t.Fatalf("IsReadOnly(%T) = false", got)
		}
	}
	if results[0] == nil {
		t.Skip("sandbox-exec 不可用")
	}
}

func TestReadOnlyCommand(t *testing.T) {
	if ReadOnly() == nil {
		t.Skip("sandbox-exec 不可用")
	}
	workDir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home.+[test]")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := newReadOnly(home)
	if err != nil {
		t.Fatal(err)
	}
	if !IsReadOnly(s) {
		t.Fatal("newReadOnly returned an unrecognized sandbox")
	}
	for _, dir := range []string{s.scratch, filepath.Join(s.scratch, "tmp"), filepath.Join(s.scratch, "gocache"), filepath.Join(s.scratch, "cache")} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("scratch permissions: %s: %v, %v", dir, info, err)
		}
	}
	run := func(t *testing.T, script string, args ...string) (string, error) {
		t.Helper()
		cmd, cleanup, err := s.Command(context.Background(), workDir, harness.ModeReadOnly, "/bin/sh", append([]string{"-c", script, "sandbox-test"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		if cleanup != nil {
			t.Fatal("scratch directory must be retained between commands")
		}
		if cmd.Dir != workDir {
			t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, workDir)
		}
		// 模拟 shell 层覆盖环境，沙箱约束仍须生效。
		cmd.Env = append(os.Environ(), "HOME="+s.home, "TMPDIR=/not-writable", "GOCACHE=/not-writable", "XDG_CACHE_HOME=/not-writable", "GIT_OPTIONAL_LOCKS=1")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		cmd.WaitDelay = time.Second
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		timer := time.AfterFunc(15*time.Second, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
		err = cmd.Wait()
		return output.String(), err
	}
	writeFile := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("readable"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("work directory is read only", func(t *testing.T) {
		out, err := run(t, `printf blocked > blocked`)
		if err == nil {
			t.Fatalf("write succeeded: %s", out)
		}
		if _, err := os.Stat(filepath.Join(workDir, "blocked")); !os.IsNotExist(err) {
			t.Fatalf("blocked file exists or cannot be checked: %v", err)
		}
	})
	t.Run("scratch is writable and persistent", func(t *testing.T) {
		out, err := run(t, `f=$(mktemp "$TMPDIR/sandbox-test.XXXXXX") && printf scratch > "$f" && printf '%s' "$f"`)
		if err != nil {
			t.Fatalf("scratch write: %v: %s", err, out)
		}
		path := out
		defer os.Remove(path)
		out, err = run(t, `exec /bin/cat "$1"`, path)
		if err != nil || out != "scratch" {
			t.Fatalf("scratch read in next command: %v: %s", err, out)
		}
	})
	t.Run("ordinary file is readable", func(t *testing.T) {
		path := filepath.Join(workDir, "ordinary")
		writeFile(path)
		out, err := run(t, `exec /bin/cat "$1"`, path)
		if err != nil || out != "readable" {
			t.Fatalf("read: %v: %s", err, out)
		}
	})
	t.Run("network is available", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("network")) }))
		defer server.Close()
		out, err := run(t, `exec /usr/bin/curl --noproxy '*' -fsS -m 5 "$1"`, server.URL)
		if err != nil || out != "network" {
			t.Fatalf("curl: %v: %s", err, out)
		}
	})
	t.Run("credentials are unreadable", func(t *testing.T) {
		for _, relative := range []string{".ssh/id_rsa", ".aws/credentials", ".config/gcloud/credentials.db", ".config/gh/hosts.yml", ".docker/config.json", ".kube/config", ".netrc", ".pi/agent/auth.json", ".git-credentials", ".mcode/config.json", ".mcode/always_allow.JSON"} {
			t.Run(relative, func(t *testing.T) {
				path := filepath.Join(home, relative)
				writeFile(path)
				if out, err := run(t, `exec /bin/cat "$1"`, path); err == nil || strings.Contains(out, "readable") {
					t.Fatalf("credential read: %v: %s", err, out)
				}
			})
		}
	})
	t.Run("nested mcode JSON remains readable", func(t *testing.T) {
		path := filepath.Join(home, ".mcode", "swarm-worktrees", "test", "config.json")
		writeFile(path)
		out, err := run(t, `exec /bin/cat "$1"`, path)
		if err != nil || out != "readable" {
			t.Fatalf("nested JSON read: %v: %s", err, out)
		}
	})
	t.Run("outside process cannot be signaled", func(t *testing.T) {
		sleep := exec.Command("/bin/sleep", "30")
		if err := sleep.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = sleep.Process.Kill()
			_ = sleep.Wait()
		}()
		out, err := run(t, `kill -TERM "$1"`, fmt.Sprint(sleep.Process.Pid))
		if err == nil {
			t.Fatalf("outside signal succeeded: %s", out)
		}
		if err := syscall.Kill(sleep.Process.Pid, 0); err != nil {
			t.Fatalf("outside process is no longer alive: %v", err)
		}
	})
	t.Run("same sandbox process can be signaled", func(t *testing.T) {
		out, err := run(t, `/bin/sleep 5 & child=$!; kill -TERM "$child"; wait "$child"; test "$?" -eq 143`)
		if err != nil {
			t.Fatalf("same-sandbox signal: %v: %s", err, out)
		}
	})
	t.Run("go cache uses scratch", func(t *testing.T) {
		goPath, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(t, `exec "$1" env GOCACHE`, goPath)
		if err != nil || strings.TrimSpace(out) != filepath.Join(s.scratch, "gocache") {
			t.Fatalf("go env GOCACHE: %v: %s", err, out)
		}
	})
	t.Run("other environment overrides", func(t *testing.T) {
		out, err := run(t, `printf '%s\n%s' "$XDG_CACHE_HOME" "$GIT_OPTIONAL_LOCKS"`)
		if err != nil || out != filepath.Join(s.scratch, "cache")+"\n0" {
			t.Fatalf("environment: %v: %s", err, out)
		}
	})
}

func TestNewReadOnlyInvalidHome(t *testing.T) {
	if _, err := newReadOnly(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("nonexistent HOME accepted")
	}
}
