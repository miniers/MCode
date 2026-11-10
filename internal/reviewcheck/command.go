package reviewcheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/procutil"
)

const (
	// DefaultTimeout 是 L0 检查命令的默认总超时。
	DefaultTimeout = 180 * time.Second
	// MaxPackages 是自动识别时最多检查的受影响包数，超出的包只记录不阻塞。
	MaxPackages = 8
	// defaultTailLines 是检查输出默认保留的行数。
	defaultTailLines = 40
	// defaultTailBytes 是检查输出默认保留的字节数。
	defaultTailBytes = 4096
)

// ProjectConfig 是项目级检查命令配置，保存在 <dataDir>/review-checks 下。
type ProjectConfig struct {
	// Command 是项目自定义检查命令；为空表示自动识别。
	Command string `json:"command"`
	// TimeoutSeconds 是检查总超时秒数，范围 30..600，0 按 180 处理。
	TimeoutSeconds int `json:"timeout_seconds"`
	// RunTests 为 true 时在自动识别命令后追加受影响包的 go test。
	RunTests bool `json:"run_tests"`
}

// Command 描述一条待执行的 L0 检查命令，可由多个参数向量依次执行。
type Command struct {
	// Shell 是可执行文件名，例如 sh 或 go。
	Shell string
	// Args 是依次执行的参数向量列表；任一失败即停止。
	Args [][]string
	// Display 是给审查原因和 UI 展示的命令行文本。
	Display string
	// Timeout 是这条命令的总超时；0 表示使用 DefaultTimeout。
	Timeout time.Duration
	// Env 是需要覆盖的环境变量，格式 KEY=VALUE，追加在进程环境之后。
	Env []string
}

// Result 是一次 L0 检查命令的执行结果。
type Result struct {
	// OK 表示所有命令都以 0 退出；跳过检查时为 true。
	OK bool
	// Skipped 表示没有可执行的命令或检查被主动跳过。
	Skipped bool
	// Display 是实际执行的命令行文本。
	Display string
	// Output 是合并 stdout/stderr 后保留的尾部输出。
	Output string
	// Duration 是从开始到结束的总耗时。
	Duration time.Duration
	// TimedOut 表示命令因超时被终止。
	TimedOut bool
}

// ResolveCommand 决定本次 L0 要执行的检查命令：
// configured 非空时用 sh -c 执行它；否则在 worktree 根有 go.mod 时，
// 只为 changed 里 .go 文件所在的受影响包生成 go build 和 go vet（最多 MaxPackages 个）；
// 其他项目返回 false，表示跳过命令检查。
func ResolveCommand(worktree string, changed []string, configured string, timeout time.Duration) (Command, bool) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return Command{
			Shell:   "sh",
			Args:    [][]string{{"-c", configured}},
			Display: configured,
			Timeout: timeout,
		}, true
	}
	if strings.TrimSpace(worktree) == "" {
		return Command{}, false
	}
	if _, err := os.Stat(filepath.Join(worktree, "go.mod")); err != nil {
		return Command{}, false
	}
	pkgs := affectedPackages(worktree, changed)
	if len(pkgs) == 0 {
		return Command{}, false
	}
	buildArgs := append([]string{"build"}, pkgs...)
	vetArgs := append([]string{"vet"}, pkgs...)
	display := "go build " + strings.Join(pkgs, " ") + " && go vet " + strings.Join(pkgs, " ")
	return Command{
		Shell:   "go",
		Args:    [][]string{buildArgs, vetArgs},
		Display: display,
		Timeout: timeout,
		Env:     []string{"GOFLAGS=" + goBuildFlags()},
	}, true
}

// WithTests 在自动识别的 go 命令后追加受影响包的
// go test -count=1 -timeout 120s；runTests 为 false 或命令不是自动识别结果时原样返回。
func WithTests(cmd Command, runTests bool) Command {
	if !runTests || cmd.Shell != "go" || len(cmd.Args) == 0 {
		return cmd
	}
	build := cmd.Args[0]
	if len(build) < 2 || build[0] != "build" {
		return cmd
	}
	pkgs := append([]string(nil), build[1:]...)
	testArgs := append([]string{"test", "-count=1", "-timeout", "120s"}, pkgs...)
	cmd.Args = append(append([][]string{}, cmd.Args...), testArgs)
	cmd.Display += " && go test -count=1 -timeout 120s " + strings.Join(pkgs, " ")
	return cmd
}

// Run 在 worktree 下依次执行 cmd，合并 stdout/stderr，只保留尾部输出。
// 命令在独立进程组中运行；超时或 ctx 取消时杀掉整个进程组，避免子进程残留。
func Run(ctx context.Context, worktree string, cmd Command, maxOutput int) Result {
	start := time.Now()
	result := Result{OK: true, Display: cmd.Display}
	if cmd.Shell == "" || len(cmd.Args) == 0 {
		result.Skipped = true
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	buf := newTailBuffer(outputBufferLimit(maxOutput))
	env := commandEnv(cmd.Env)
	for _, args := range cmd.Args {
		process := exec.CommandContext(ctx, cmd.Shell, args...)
		process.Dir = worktree
		process.Env = env
		setProcessGroup(process)
		process.Cancel = func() error { return killProcessGroup(process) }
		process.WaitDelay = 2 * time.Second
		process.Stdout = buf
		process.Stderr = buf
		if err := process.Run(); err != nil {
			result.OK = false
			result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			break
		}
	}
	result.Duration = time.Since(start)
	result.Output = Tail(buf.String(), 0, maxOutput)
	return result
}

// Tail 保留字符串末尾最多 maxLines 行、maxBytes 字节；参数为 0 时使用
// 默认的 40 行和 4KB。截断字节时丢弃不完整的行，避免输出从半个字符开始。
func Tail(s string, maxLines, maxBytes int) string {
	if maxLines <= 0 {
		maxLines = defaultTailLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultTailBytes
	}
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// affectedPackages 从改动文件推导受影响包参数，去重并最多返回 MaxPackages 个。
func affectedPackages(worktree string, changed []string) []string {
	seen := map[string]struct{}{}
	pkgs := make([]string, 0, MaxPackages)
	for _, file := range changed {
		file = normalizeSlashes(file)
		if strings.HasPrefix(file, "/") || !strings.HasSuffix(strings.ToLower(file), ".go") {
			continue
		}
		rel := path.Dir(file)
		if rel == "" {
			rel = "."
		}
		rel = path.Clean(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if rel != "." && skipPackageDir(worktree, rel) {
			continue
		}
		arg := "."
		if rel != "." {
			arg = "./" + rel
		}
		if _, dup := seen[arg]; dup {
			continue
		}
		seen[arg] = struct{}{}
		pkgs = append(pkgs, arg)
		if len(pkgs) >= MaxPackages {
			break
		}
	}
	return pkgs
}

// skipPackageDir 判断目录是否要排除：vendor、testdata、node_modules、.git、
// 其他隐藏目录，以及自身含 go.mod 的嵌套模块。
func skipPackageDir(worktree, rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
		switch segment {
		case "vendor", "testdata", "node_modules":
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(worktree, filepath.FromSlash(rel), "go.mod")); err == nil {
		return true
	}
	return false
}

// goBuildFlags 返回要写入 GOFLAGS 的值：保留原值，缺少 -p 时追加 -p=2，
// 避免并发 bee 的 go build 互相抢 CPU。
func goBuildFlags() string {
	current := strings.TrimSpace(os.Getenv("GOFLAGS"))
	for _, flag := range strings.Fields(current) {
		if flag == "-p" || strings.HasPrefix(flag, "-p=") {
			return current
		}
	}
	if current == "" {
		return "-p=2"
	}
	return current + " -p=2"
}

// commandEnv 在继承进程环境的基础上追加覆盖项；exec 对重复键取最后一项。
func commandEnv(overrides []string) []string {
	if len(overrides) == 0 {
		return nil
	}
	return append(os.Environ(), overrides...)
}

// outputBufferLimit 计算输出缓冲上限：至少 16KB，最多 1MB，
// 保证截断后仍有足够内容供 Tail 取尾部。
func outputBufferLimit(maxOutput int) int {
	if maxOutput <= 0 {
		maxOutput = defaultTailBytes
	}
	limit := maxOutput * 4
	if limit < 16*1024 {
		limit = 16 * 1024
	}
	if limit > 1<<20 {
		limit = 1 << 20
	}
	return limit
}

// newTailBuffer 创建指定上限的尾部缓冲。
func newTailBuffer(limit int) *procutil.TailBuffer {
	if limit <= 0 {
		limit = defaultTailBytes
	}
	return procutil.NewTailBuffer(limit)
}
