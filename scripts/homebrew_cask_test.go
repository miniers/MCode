package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runHomebrewCask(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	script, err := filepath.Abs("homebrew_cask.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestHomebrewCaskGenerate(t *testing.T) {
	t.Parallel()
	const (
		version  = "0.2.35"
		arm64SHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		amd64SHA = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)
	sumsPath := filepath.Join(t.TempDir(), "SHA256SUMS")
	sumsContent := strings.Join([]string{
		strings.Repeat("a", 64) + "  mcode-0.2.35-arm64.dmg.blockmap",
		arm64SHA + "  mcode-0.2.35-arm64.dmg",
		amd64SHA + "  mcode-0.2.35-amd64.dmg",
		strings.Repeat("b", 64) + "  mcode-setup-0.2.35-amd64.exe",
	}, "\n") + "\n"
	if err := os.WriteFile(sumsPath, []byte(sumsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runHomebrewCask(t, version, sumsPath)
	if err != nil {
		t.Fatalf("生成 cask 失败: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("意外的 stderr 输出: %s", stderr)
	}

	for _, want := range []string{
		`cask "mcode" do`,
		`arch arm: "arm64", intel: "amd64"`,
		`version "0.2.35"`,
		`sha256 arm:   "` + arm64SHA + `",`,
		`intel: "` + amd64SHA + `"`,
		`url "https://github.com/miniers/MCode/releases/download/v#{version}/mcode-#{version}-#{arch}.dmg"`,
		`depends_on macos: :sonoma`,
		`app "MCode.app"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"auto_updates", "zap "} {
		if strings.Contains(stdout, absent) {
			t.Fatalf("输出不应包含 %q:\n%s", absent, stdout)
		}
	}

	if ruby, err := exec.LookPath("ruby"); err == nil {
		rbPath := filepath.Join(t.TempDir(), "mcode.rb")
		if err := os.WriteFile(rbPath, []byte(stdout), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(ruby, "-c", rbPath).CombinedOutput()
		if err != nil {
			t.Fatalf("ruby 语法校验失败: %v\n%s", err, out)
		}
	}
}

func TestHomebrewCaskErrors(t *testing.T) {
	t.Parallel()
	const (
		validSHA1 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		validSHA2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)

	dir := t.TempDir()
	missingAMD64 := filepath.Join(dir, "missing-amd64")
	if err := os.WriteFile(missingAMD64, []byte(validSHA1+"  mcode-0.2.35-arm64.dmg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingARM64 := filepath.Join(dir, "missing-arm64")
	if err := os.WriteFile(missingARM64, []byte(validSHA2+"  mcode-0.2.35-amd64.dmg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	badHash := filepath.Join(dir, "bad-hash")
	if err := os.WriteFile(badHash, []byte("not-a-sha256  mcode-0.2.35-arm64.dmg\n"+validSHA2+"  mcode-0.2.35-amd64.dmg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefixMismatch := filepath.Join(dir, "prefix-mismatch")
	if err := os.WriteFile(prefixMismatch, []byte(validSHA1+"  mcode-0.2.35-arm64.dmg.sig\n"+validSHA2+"  mcode-0.2.35-amd64.dmg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "缺少参数", args: nil},
		{name: "参数过多", args: []string{"0.2.35", missingAMD64, "extra"}},
		{name: "非法版本", args: []string{"v0.2.35", missingAMD64}},
		{name: "文件不存在", args: []string{"0.2.35", filepath.Join(dir, "nonexistent")}},
		{name: "缺少 amd64", args: []string{"0.2.35", missingAMD64}},
		{name: "缺少 arm64", args: []string{"0.2.35", missingARM64}},
		{name: "哈希格式错误", args: []string{"0.2.35", badHash}},
		{name: "非精确匹配", args: []string{"0.2.35", prefixMismatch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runHomebrewCask(t, tc.args...)
			if err == nil {
				t.Fatalf("预期失败，但退出码为 0\nstdout: %s", stdout)
			}
			if stdout != "" {
				t.Fatalf("失败时 stdout 必须为空，实际输出: %q", stdout)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Fatal("失败时 stderr 必须输出错误信息")
			}
		})
	}
}
