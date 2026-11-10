//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"mcode/internal/harness"
)

// 网络保留，所以凭证必须禁读。
const readOnlyProfile = `(version 1)
(allow default)
(deny file-write*)
(allow file-write*
    (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty")
    (literal "/dev/stdout") (literal "/dev/stderr") (literal "/dev/dtracehelper")
    (regex #"^/dev/fd/") (regex #"^/dev/ttys[0-9]+$")
    (subpath (param "SCRATCH")))
(deny file-read* file-write*
    (subpath (string-append (param "HOME") "/.ssh"))
    (subpath (string-append (param "HOME") "/.aws"))
    (subpath (string-append (param "HOME") "/.config/gcloud"))
    (subpath (string-append (param "HOME") "/.config/gh"))
    (literal (string-append (param "HOME") "/.docker/config.json"))
    (literal (string-append (param "HOME") "/.kube/config"))
    (literal (string-append (param "HOME") "/.netrc"))
    (literal (string-append (param "HOME") "/.pi/agent/auth.json"))
    (literal (string-append (param "HOME") "/.git-credentials"))
    (regex (string-append "^" (regex-quote (param "HOME")) "/\\.mcode/[^/]+\\.[jJ][sS][oO][nN]$")))
(deny signal)
(allow signal (target same-sandbox))`

type readOnly struct {
	scratch string
	home    string
}

func (*readOnly) readOnlySandbox() {}

var (
	readOnlyOnce sync.Once
	available    harness.Sandbox
)

// ReadOnly 返回可用的系统只读沙箱；仅探测一次，不可用时返回 nil。
func ReadOnly() harness.Sandbox {
	readOnlyOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run(); err != nil {
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		s, err := newReadOnly(home)
		if err == nil {
			available = s
		}
	})
	return available
}

func newReadOnly(home string) (*readOnly, error) {
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	scratch := filepath.Join(os.TempDir(), "mcode-readonly")
	for _, dir := range []string{scratch, filepath.Join(scratch, "tmp"), filepath.Join(scratch, "gocache"), filepath.Join(scratch, "cache")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil, err
	}
	return &readOnly{scratch: scratch, home: home}, nil
}

func (s *readOnly) Command(_ context.Context, workDir string, _ harness.PermissionMode, name string, args ...string) (*exec.Cmd, func(), error) {
	argv := []string{"-p", readOnlyProfile, "-D", "SCRATCH=" + s.scratch, "-D", "HOME=" + s.home,
		"/usr/bin/env", "TMPDIR=" + filepath.Join(s.scratch, "tmp"), "GOCACHE=" + filepath.Join(s.scratch, "gocache"),
		"XDG_CACHE_HOME=" + filepath.Join(s.scratch, "cache"), "GIT_OPTIONAL_LOCKS=0", name}
	cmd := exec.Command("/usr/bin/sandbox-exec", append(argv, args...)...)
	cmd.Dir = workDir
	return cmd, nil, nil
}
