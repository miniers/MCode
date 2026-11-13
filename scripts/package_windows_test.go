package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPackageWindowsScript(t *testing.T) {
	data, err := os.ReadFile("package_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		"-Clean",
		"-Bump",
		"-NoRelaunch",
		"windows/amd64",
		"-skip-notarize",
		"relaunch_app.ps1",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("package_windows.ps1 缺少 %q", want)
		}
	}

	helper, err := os.ReadFile("relaunch_app.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(helper), "'/S'") {
		t.Fatal("relaunch_app.ps1 缺少静默安装参数 '/S'")
	}
}

func TestPackageWindowsHelp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 打包脚本只在 Windows 上运行")
	}
	script, err := filepath.Abs("package_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error=%v\n%s", err, output)
	}
	for _, option := range []string{"-Clean", "-Bump", "-NoRelaunch"} {
		if !strings.Contains(string(output), option) {
			t.Fatalf("帮助信息缺少 %s:\n%s", option, output)
		}
	}
}

func TestRelaunchAppWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 重启脚本只在 Windows 上运行")
	}
	dir := t.TempDir()
	installer := filepath.Join(dir, "fake-installer.cmd")
	marker := filepath.Join(dir, "installed.txt")
	logPath := filepath.Join(dir, "relaunch.log")
	content := "@echo off\r\necho installed > \"%~dp0installed.txt\"\r\nexit /b 0\r\n"
	if err := os.WriteFile(installer, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("relaunch_app.ps1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script,
		"-Installer", installer,
		"-Identifier", "mcode.test",
		"-DefaultInstallDir", filepath.Join(dir, "install"),
		"-NoRelaunch",
		"-LogPath", logPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error=%v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("安装程序没有运行: %v\n%s", err, output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "安装完成") {
		t.Fatalf("日志缺少安装完成:\n%s", logData)
	}
}
