//go:build darwin || linux

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForContent 轮询文件内容直到出现 want；后台进程是异步的，只能这样断言。
func waitForContent(t *testing.T, path, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), want) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type relaunchFixture struct {
	script      string
	appPath     string
	bin         string
	commandLog  string
	relaunchLog string
}

// mock 脚本把每次调用追加到 TEST_LOG；pgrep 由 TEST_APP_PID 决定输出，
// osascript 负责杀掉 TEST_QUIT_PID，用来模拟“应用自己退出了”。
// lsappinfo 由 TEST_LS_PID 模拟 LaunchServices 查到的实例；
// ps 只在查 etime（不查 stat）时用 TEST_PS_ETIME 冒充，其余交给真实 ps；
// TEST_LATE_APP_PID 用来模拟“第一次 pgrep 漏掉、open 之后才查到旧实例”。
const relaunchMock = `#!/bin/bash
set -u
name="${0##*/}"
printf '%s %s\n' "$name" "$*" >> "$TEST_LOG"
case "$name" in
pgrep)
    # 日志里已经有 open 说明是重启后的第二次查找：此时才报旧实例，模拟漏查。
    if [[ -n "${TEST_LATE_APP_PID:-}" ]] && grep -q '^open ' "$TEST_LOG"; then
        printf '%s\n' "$TEST_LATE_APP_PID"
        exit 0
    fi
    [[ -n "${TEST_APP_PID:-}" ]] || exit 1
    printf '%s\n' "$TEST_APP_PID"
    ;;
lsappinfo)
    [[ -n "${TEST_LS_PID:-}" ]] || exit 0
    printf 'pid = %s !cgsConnection\n' "$TEST_LS_PID"
    ;;
ps)
    if [[ -n "${TEST_PS_ETIME:-}" && "$*" == *etime=* && "$*" != *stat=* ]]; then
        printf '%s\n' "$TEST_PS_ETIME"
        exit 0
    fi
    for real in /bin/ps /usr/bin/ps; do
        if [[ -x "$real" ]]; then
            exec "$real" "$@"
        fi
    done
    exit 1
    ;;
osascript)
    if [[ -n "${TEST_QUIT_PID:-}" ]]; then
        kill "${TEST_QUIT_PID}" 2>/dev/null || true
    fi
    ;;
esac
exit 0
`

func newRelaunchFixture(t *testing.T) *relaunchFixture {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("缺少 perl，跳过脱离会话的重启用例")
	}
	script, err := filepath.Abs("relaunch_app.sh")
	if err != nil {
		t.Fatal(err)
	}
	// 不用 t.TempDir：脚本会被 perl 脱离成后台进程，测试结束时它可能还在写
	// relaunch.log，t.TempDir 的清理会因目录非空而偶发失败，这里改成带重试的清理。
	dir, err := os.MkdirTemp("", "relaunch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("清理临时目录失败: %v", err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pgrep", "lsappinfo", "ps", "osascript", "open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(relaunchMock), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 应用路径带空格，顺便验证引号处理。
	appPath := filepath.Join(dir, "My App.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &relaunchFixture{
		script:      script,
		appPath:     appPath,
		bin:         bin,
		commandLog:  filepath.Join(dir, "commands"),
		relaunchLog: filepath.Join(dir, "relaunch.log"),
	}
}

func (f *relaunchFixture) env(extra map[string]string) []string {
	env := append(os.Environ(),
		"PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_LOG="+f.commandLog,
		"TEST_APP_PID=",
		"TEST_QUIT_PID=",
		"TEST_LS_PID=",
		"TEST_LATE_APP_PID=",
		"TEST_PS_ETIME=",
		"MCODE_RELAUNCH_LOG="+f.relaunchLog,
		// 进程内环境可能来自另一个刚启动的重启进程，这里显式关掉脱离标记与超时覆盖。
		"MCODE_RELAUNCH_DETACHED=0",
		"MCODE_RELAUNCH_QUIT_TIMEOUT=",
		"MCODE_RELAUNCH_CALLER_TIMEOUT=",
	)
	for key, value := range extra {
		// 后出现的环境变量覆盖继承值（os/exec 会保留最后一条）。
		env = append(env, key+"="+value)
	}
	return env
}

// startProcess 启动一个进程并异步回收，避免 kill -0 看到僵尸进程。
func startProcess(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd
}

func TestRelaunchAppNotRunning(t *testing.T) {
	f := newRelaunchFixture(t)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(nil)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("应用未运行时脚本应当成功返回: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "已在后台安排重启") {
		t.Fatalf("缺少前台提示:\n%s", output)
	}
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("后台没有打开应用，日志:\n%s", data)
	}
	data, err := os.ReadFile(f.commandLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "osascript") {
		t.Fatalf("应用未运行时不应调用 osascript:\n%s", data)
	}
}

// 回归用例：非 UTF-8 locale 下，紧跟在变量名后的全角字符不能被当成变量名的一部分，
// 否则 set -u 会报 'APP_NAME\xef: unbound variable'。
func TestRelaunchAppNonUTF8Locale(t *testing.T) {
	f := newRelaunchFixture(t)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(map[string]string{"LC_ALL": "C", "LANG": "C"})
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("非 UTF-8 locale 下脚本应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("后台没有打开应用，日志:\n%s", data)
	}
	data, err := os.ReadFile(f.relaunchLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "unbound variable") {
		t.Fatalf("日志里出现 unbound variable:\n%s", data)
	}
}

func TestRelaunchAppQuitsRunningApp(t *testing.T) {
	f := newRelaunchFixture(t)
	app := startProcess(t, "sleep", "60")
	pid := strconv.Itoa(app.Process.Pid)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(map[string]string{"TEST_APP_PID": pid, "TEST_QUIT_PID": pid})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("脚本应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("后台没有打开应用，日志:\n%s", data)
	}
	data, err := os.ReadFile(f.commandLog)
	if err != nil {
		t.Fatal(err)
	}
	commands := string(data)
	quit := strings.Index(commands, "osascript")
	open := strings.Index(commands, "open "+f.appPath)
	if quit < 0 || open < 0 || quit > open {
		t.Fatalf("应当先退出旧应用再打开新应用:\n%s", commands)
	}
}

func TestRelaunchAppQuitTimeout(t *testing.T) {
	f := newRelaunchFixture(t)
	// 忽略 TERM 且 exec，让 SIGTERM 无效，同时保证 kill -9 能一次清掉。
	app := startProcess(t, "bash", "-c", "trap '' TERM; exec sleep 60")
	pid := strconv.Itoa(app.Process.Pid)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(map[string]string{
		"TEST_APP_PID":                pid,
		"TEST_QUIT_PID":               pid,
		"MCODE_RELAUNCH_QUIT_TIMEOUT": "1",
	})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("前台阶段应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.relaunchLog, "放弃重启", 25*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("超时后应当写错误日志，日志:\n%s", data)
	}
	// 再等一会，确认失败后确实没有打开应用。
	time.Sleep(2 * time.Second)
	if data, err := os.ReadFile(f.commandLog); err == nil && strings.Contains(string(data), "open "+f.appPath) {
		t.Fatalf("进程退不掉时不应打开应用:\n%s", data)
	}
}

func TestRelaunchAppFindsAppViaLsappinfo(t *testing.T) {
	f := newRelaunchFixture(t)
	// pgrep 漏掉（TEST_APP_PID 留空），只能靠 lsappinfo 找到旧实例。
	app := startProcess(t, "sleep", "60")
	pid := strconv.Itoa(app.Process.Pid)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(map[string]string{"TEST_LS_PID": pid, "TEST_QUIT_PID": pid})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("脚本应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("后台没有打开应用，日志:\n%s", data)
	}
	data, err := os.ReadFile(f.commandLog)
	if err != nil {
		t.Fatal(err)
	}
	commands := string(data)
	quit := strings.Index(commands, "osascript")
	open := strings.Index(commands, "open "+f.appPath)
	if quit < 0 || open < 0 || quit > open {
		t.Fatalf("lsappinfo 查到旧实例时应当先退出再打开:\n%s", commands)
	}
}

// 回归用例：pgrep 必须带 -a（macOS 默认排除自身祖先进程）并用 ^ 锚定可执行文件路径。
func TestRelaunchAppPgrepIncludesAncestors(t *testing.T) {
	f := newRelaunchFixture(t)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(nil)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("应用未运行时脚本应当成功返回: %v\n%s", err, output)
	}
	want := "pgrep -a -f ^" + f.appPath + "/Contents/MacOS/"
	if !waitForContent(t, f.commandLog, want, 10*time.Second) {
		data, _ := os.ReadFile(f.commandLog)
		t.Fatalf("pgrep 参数应当包含 -a 与锚定的可执行文件路径，命令日志:\n%s", data)
	}
}

// open 在旧实例还活着时只激活它、不报错，脚本必须因此失败而不是报告已重启。
func TestRelaunchAppReportsStaleInstance(t *testing.T) {
	f := newRelaunchFixture(t)
	app := startProcess(t, "sleep", "60")
	pid := strconv.Itoa(app.Process.Pid)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath)
	cmd.Env = f.env(map[string]string{"TEST_LATE_APP_PID": pid, "TEST_PS_ETIME": "01:59"})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("前台阶段应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.relaunchLog, "只激活了旧实例", 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("open 后仍是旧实例时应当写错误日志，日志:\n%s", data)
	}
	data, err := os.ReadFile(f.relaunchLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "已重新启动") {
		t.Fatalf("看到旧实例时不能报告已重新启动，日志:\n%s", data)
	}
}

func TestRelaunchAppCallerTimeout(t *testing.T) {
	f := newRelaunchFixture(t)
	// 永远不退出的调用方：脚本必须等到 CALLER_TIMEOUT 后继续重启，而不是一直等下去。
	caller := startProcess(t, "sleep", "60")
	callerPID := strconv.Itoa(caller.Process.Pid)

	cmd := exec.Command("bash", f.script, "MCode", f.appPath, callerPID)
	cmd.Env = f.env(map[string]string{"MCODE_RELAUNCH_CALLER_TIMEOUT": "1"})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("前台阶段应当成功返回: %v\n%s", err, output)
	}
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("调用方超时后仍应打开应用，日志:\n%s", data)
	}
	if !waitForContent(t, f.relaunchLog, "仍未退出", 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("日志应当说明调用方超时，日志:\n%s", data)
	}
}

func TestRelaunchAppSurvivesCallerProcessGroupKill(t *testing.T) {
	f := newRelaunchFixture(t)
	// 包装进程自成一个进程组：模拟 MCode 内置终端/运行脚本菜单里的调用方。
	wrapper := exec.Command("bash", "-c",
		"bash '"+f.script+"' 'MCode' '"+f.appPath+"' $$; sleep 30")
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	wrapper.Env = f.env(nil)
	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := wrapper.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	if !waitForContent(t, f.relaunchLog, "等待调用方", 10*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("辅助脚本没有开始等待调用方，日志:\n%s", data)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// 调用方整个进程组被杀掉后，重启仍要完成。
	_ = wrapper.Wait()
	if !waitForContent(t, f.commandLog, "open "+f.appPath, 20*time.Second) {
		data, _ := os.ReadFile(f.relaunchLog)
		t.Fatalf("调用方被杀死后没有打开应用，日志:\n%s", data)
	}
}

func TestRelaunchAppMissingArgs(t *testing.T) {
	f := newRelaunchFixture(t)

	cmd := exec.Command("bash", f.script, "MCode")
	cmd.Env = f.env(nil)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("参数不足时应当失败:\n%s", output)
	}
	if !strings.Contains(string(output), "错误") {
		t.Fatalf("缺少中文错误信息:\n%s", output)
	}

	notDir := filepath.Join(filepath.Dir(f.appPath), "missing.app")
	cmd = exec.Command("bash", f.script, "MCode", notDir)
	cmd.Env = f.env(nil)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("应用路径不是目录时应当失败:\n%s", output)
	}
}
