package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageRelease(t *testing.T) {
	script, err := filepath.Abs("package_release.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, identity, fail, omit string
		args                       []string
		wantError, wantNotarize    bool
	}{
		{name: "ad hoc"},
		{name: "explicit ad hoc", identity: "-"},
		{name: "signed", identity: "Developer ID Application: Test", wantNotarize: true},
		{name: "skip notarization", identity: "Developer ID Application: Test", args: []string{"--skip-notarize"}},
		{name: "universal", args: []string{"--platform", "darwin/universal"}},
		{name: "intel", args: []string{"--platform", "darwin/amd64"}},
		{name: "build failure", fail: "go", wantError: true},
		{name: "bad app signature", fail: "codesign", wantError: true},
		{name: "bad image", fail: "hdiutil", wantError: true},
		{name: "bad ticket", identity: "Developer ID Application: Test", fail: "xcrun", wantError: true},
		{name: "gatekeeper rejection", identity: "Developer ID Application: Test", fail: "spctl", wantError: true},
		{name: "missing app", omit: "app", wantError: true},
		{name: "missing image", omit: "dmg", wantError: true},
		{name: "unsupported platform", args: []string{"--platform", "linux/amd64"}, wantError: true},
		{name: "missing platform", args: []string{"--platform"}, wantError: true},
		{name: "missing output", args: []string{"--out"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			out := filepath.Join(dir, "output with spaces")
			logFile := filepath.Join(dir, "commands")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			// 旧架构产物不能被用来掩盖本次构建缺失的 .app。
			if err := os.MkdirAll(filepath.Join(out, "darwin-stale", "MCode.app"), 0o755); err != nil {
				t.Fatal(err)
			}
			mock := `#!/bin/bash
set -eu
name="${0##*/}"
printf '%s %s\n' "$name" "$*" >> "$TEST_LOG"
[[ "$name" != "$TEST_FAIL" ]] || exit 1
case "$name" in
uname) echo Darwin ;;
go)
    shift 3
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -platform) platform="$2"; shift 2 ;;
            -o) out="$2"; shift 2 ;;
            *) shift ;;
        esac
    done
    target="$out/${platform/\//-}"
    mkdir -p "$target"
    [[ "$TEST_OMIT" == app ]] || mkdir -p "$target/MCode.app"
    [[ "$TEST_OMIT" == dmg ]] || touch "$target/mcode-0.1.0-arm64.dmg"
    ;;
esac
`
			for _, name := range []string{"go", "uname", "codesign", "hdiutil", "xcrun", "spctl"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{script, "--out", out}, tc.args...)
			cmd := exec.Command("bash", args...)
			cmd.Env = os.Environ()
			for key, value := range map[string]string{
				"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME": dir, "MCODE_SIGN_IDENTITY": tc.identity,
				"MYGO_UPDATER_PRIVATE_KEY": "", "TEST_LOG": logFile,
				"TEST_FAIL": tc.fail, "TEST_OMIT": tc.omit,
				// 测试不得联网，也不得改写受版本控制的 internal/modelsdev/snapshot.json：
				// 默认已不刷新快照，这里再显式关掉，避免调用方环境把它打开。
				"MCODE_REFRESH_SNAPSHOT": "0",
			} {
				// 后出现的环境变量覆盖继承值，避免使用真实证书或私钥。
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v\n%s", err, tc.wantError, output)
			}
			if tc.wantError {
				return
			}
			data, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(data)
			target := "darwin-arm64"
			if tc.name == "universal" {
				target = "darwin-universal"
			} else if tc.name == "intel" {
				target = "darwin-amd64"
			}
			for _, want := range []string{
				"codesign --verify --deep --strict " + filepath.Join(out, target, "MCode.app"),
				"hdiutil verify " + filepath.Join(out, target, "mcode-0.1.0-arm64.dmg"),
			} {
				if !strings.Contains(commands, want) {
					t.Fatalf("missing %q in:\n%s", want, commands)
				}
			}
			if strings.Contains(commands, "-skip-notarize") == tc.wantNotarize {
				t.Fatalf("wrong notarization build flags:\n%s", commands)
			}
			if strings.Contains(commands, "spctl --assess --type execute") != tc.wantNotarize ||
				strings.Contains(commands, "spctl --assess --type open") != tc.wantNotarize ||
				strings.Contains(commands, "xcrun stapler validate") != tc.wantNotarize {
				t.Fatalf("wrong notarization checks:\n%s", commands)
			}
		})
	}
}
