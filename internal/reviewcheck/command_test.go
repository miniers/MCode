package reviewcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/procattr"
)

// writeGoMod 在目录里写一个最小 go.mod。
func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatalf("写入 go.mod: %v", err)
	}
}

// TestResolveGoCommandPackages 验证只为受影响目录生成 go build 和 go vet，
// 跳过 vendor/testdata/隐藏目录/嵌套模块，并且最多 8 个包。
func TestResolveGoCommandPackages(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	dir := t.TempDir()
	writeGoMod(t, dir)
	writeGoMod(t, filepath.Join(dir, "sub"))

	changed := []string{
		"root.go",
		"internal/a.go",
		"internal/a_extra.go",
		"vendor/v.go",
		"testdata/t.go",
		".hidden/h.go",
		"sub/s.go",
		"cmd/x/main.go",
		"web/y.ts",
	}
	for i := 0; i < 12; i++ {
		changed = append(changed, "./pkg"+string(rune('0'+i/10))+string(rune('0'+i%10))+"/f.go")
	}
	cmd, ok := ResolveCommand(dir, changed, "", 0)
	if !ok {
		t.Fatal("ResolveCommand 返回 false，期望识别为 Go 项目")
	}
	if len(cmd.Args) != 2 {
		t.Fatalf("Args 长度 = %d，期望 build 和 vet 两条", len(cmd.Args))
	}
	build := cmd.Args[0]
	vet := cmd.Args[1]
	if len(build) != 1+MaxPackages {
		t.Fatalf("build 参数 = %v，期望 1+%d 个", build, MaxPackages)
	}
	wantPrefix := []string{".", "./internal", "./cmd/x", "./pkg00", "./pkg01"}
	for i, want := range wantPrefix {
		if build[i+1] != want {
			t.Errorf("build 参数[%d] = %q，期望 %q（完整：%v）", i, build[i+1], want, build)
		}
	}
	if vet[0] != "vet" || len(vet) != len(build) {
		t.Errorf("vet 参数 = %v，期望与 build 相同的包", vet)
	}
	for _, pkg := range build[1:] {
		if strings.Contains(pkg, "vendor") || strings.Contains(pkg, "testdata") || strings.Contains(pkg, "sub") {
			t.Errorf("不应包含被排除的目录：%q", pkg)
		}
	}
	if cmd.Shell != "go" || cmd.Timeout != DefaultTimeout {
		t.Errorf("Shell/Timeout = %q/%v，期望 go/%v", cmd.Shell, cmd.Timeout, DefaultTimeout)
	}
	if len(cmd.Env) != 1 || cmd.Env[0] != "GOFLAGS=-p=2" {
		t.Errorf("Env = %#v，期望 GOFLAGS=-p=2", cmd.Env)
	}
	if !strings.Contains(cmd.Display, "go build . ./internal") || !strings.Contains(cmd.Display, " && go vet ") {
		t.Errorf("Display = %q，缺少 build/vet 信息", cmd.Display)
	}
}

// TestResolveCommandSkipsNonGoProject 验证没有 go.mod 且没有配置命令时跳过。
func TestResolveCommandSkipsNonGoProject(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ResolveCommand(dir, []string{"src/a.go"}, "", 0); ok {
		t.Fatal("非 Go 项目返回 true，期望跳过")
	}
	if _, ok := ResolveCommand("", []string{"src/a.go"}, "", 0); ok {
		t.Fatal("空 worktree 返回 true，期望跳过")
	}
}

// TestResolveCommandConfiguredOverrides 验证配置命令优先于自动识别，并用平台 shell 执行。
func TestResolveCommandConfiguredOverrides(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	cmd, ok := ResolveCommand(dir, []string{"internal/a.go"}, "  make check  ", 45*time.Second)
	if !ok {
		t.Fatal("配置命令返回 false，期望执行")
	}
	wantShell, wantArgs := procattr.ShellArgs("make check")
	if cmd.Shell != wantShell {
		t.Errorf("Shell = %q，期望 %q", cmd.Shell, wantShell)
	}
	if len(cmd.Args) != 1 || !reflect.DeepEqual(cmd.Args[0], wantArgs) {
		t.Errorf("Args = %#v，期望 [%q]", cmd.Args, wantArgs)
	}
	if cmd.Display != "make check" || cmd.Timeout != 45*time.Second {
		t.Errorf("Display/Timeout = %q/%v", cmd.Display, cmd.Timeout)
	}
}

// TestResolveCommandRunTests 验证 run_tests 只在开启时追加受影响包的 go test。
func TestResolveCommandRunTests(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	cmd, ok := ResolveCommand(dir, []string{"internal/a.go"}, "", 0)
	if !ok {
		t.Fatal("ResolveCommand 返回 false")
	}
	if got := WithTests(cmd, false); len(got.Args) != 2 {
		t.Errorf("未开启 run_tests 时 Args 长度 = %d，期望 2", len(got.Args))
	}
	got := WithTests(cmd, true)
	if len(got.Args) != 3 {
		t.Fatalf("开启 run_tests 时 Args 长度 = %d，期望 3", len(got.Args))
	}
	testArgs := got.Args[2]
	wantHead := []string{"test", "-count=1", "-timeout", "120s"}
	for i, want := range wantHead {
		if testArgs[i] != want {
			t.Errorf("go test 参数[%d] = %q，期望 %q", i, testArgs[i], want)
		}
	}
	if testArgs[len(testArgs)-1] != "./internal" {
		t.Errorf("go test 包 = %v，期望 ./internal", testArgs[4:])
	}
	if !strings.Contains(got.Display, "go test -count=1 -timeout 120s ./internal") {
		t.Errorf("Display = %q，缺少 go test", got.Display)
	}
}

// TestRunCommandTimeoutKeepsTail 验证超时返回 OK=false、TimedOut=true，且输出保留尾部。
func TestRunCommandTimeoutKeepsTail(t *testing.T) {
	cmd := Command{
		Shell:   "sh",
		Display: "slow",
		Timeout: 500 * time.Millisecond,
		Args:    [][]string{{"-c", "printf 'first\\nsecond\\nthird\\n'; sleep 10"}},
	}
	start := time.Now()
	res := Run(context.Background(), t.TempDir(), cmd, 0)
	if res.OK {
		t.Fatal("OK = true，期望超时失败")
	}
	if !res.TimedOut {
		t.Fatal("TimedOut = false，期望 true")
	}
	if !strings.Contains(res.Output, "third") {
		t.Errorf("Output = %q，期望保留尾部 third", res.Output)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("超时杀死耗时 %v，期望 5 秒内结束", elapsed)
	}
}

// TestRunCommandExitStatus 验证普通成功与失败命令的 OK/TimedOut。
func TestRunCommandExitStatus(t *testing.T) {
	dir := t.TempDir()
	okRes := Run(context.Background(), dir, Command{Shell: "sh", Args: [][]string{{"-c", "true"}}, Timeout: time.Minute}, 0)
	if !okRes.OK || okRes.TimedOut || okRes.Skipped {
		t.Fatalf("true 结果 = %+v，期望 OK", okRes)
	}
	failRes := Run(context.Background(), dir, Command{Shell: "sh", Display: "false", Args: [][]string{{"-c", "echo boom >&2; exit 3"}}, Timeout: time.Minute}, 0)
	if failRes.OK || failRes.TimedOut {
		t.Fatalf("失败命令结果 = %+v，期望 OK=false 且未超时", failRes)
	}
	if !strings.Contains(failRes.Output, "boom") {
		t.Errorf("Output = %q，期望包含 stderr", failRes.Output)
	}
}

// TestTailTruncates 验证按行数和字节数保留尾部。
func TestTailTruncates(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line%03d\n", i)
	}
	got := Tail(b.String(), 5, 0)
	lines := strings.Split(got, "\n")
	if len(lines) != 5 || lines[4] != "line100" {
		t.Fatalf("Tail 结果 = %#v，期望最后 5 行且以 line100 结尾", lines)
	}
	got = Tail(strings.Repeat("x", 10000), 0, 100)
	if len(got) != 100 {
		t.Errorf("Tail 字节截断长度 = %d，期望 100", len(got))
	}
}
