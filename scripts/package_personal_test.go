package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagePersonalHelp(t *testing.T) {
	script, err := filepath.Abs("package_personal.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error=%v\n%s", err, output)
	}
	if !strings.Contains(string(output), "--windows") {
		t.Fatalf("missing --windows in help:\n%s", output)
	}
}

func TestPackagePersonalAdHocWithoutIdentity(t *testing.T) {
	cmd := packagePersonalDotenvCommand(t, nil, false, "")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if !strings.Contains(stderr.String(), "未设置 MCODE_SIGN_IDENTITY，改用临时签名") || strings.Contains(stderr.String(), "请设置 MCODE_SIGN_IDENTITY") {
		t.Fatalf("缺少临时签名警告或仍有旧错误:\n%s", stderr.String())
	}
}

func packagePersonalDotenvCommand(t *testing.T, identity *string, dotenv bool, securityOutput string) *exec.Cmd {
	t.Helper()
	script, err := os.ReadFile("package_personal.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	scripts := filepath.Join(root, "scripts")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{scripts, bin} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyPath := filepath.Join(scripts, "package_personal.sh")
	if err := os.WriteFile(copyPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	if dotenv {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("export MCODE_SIGN_IDENTITY=\"Developer ID Application: Test (ABC123)\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/bash\necho Darwin\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte("#!/bin/bash\nprintf '%s\\n' '"+securityOutput+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", copyPath, "--no-relaunch")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MCODE_SIGN_IDENTITY=") && !strings.HasPrefix(entry, "MCODE_REFRESH_SNAPSHOT=") && !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "CLEAN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "MCODE_REFRESH_SNAPSHOT=0", "CLEAN=0")
	if identity != nil {
		cmd.Env = append(cmd.Env, "MCODE_SIGN_IDENTITY="+*identity)
	}
	// 假项目根没有 web，脚本在进入该目录时失败，不会联网或安装到 /Applications。
	return cmd
}

func TestPackagePersonalLoadsDotenv(t *testing.T) {
	cmd := packagePersonalDotenvCommand(t, nil, true, `  1) ABCDEF "Developer ID Application: Test (ABC123)"`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if strings.Contains(stderr.String(), "改用临时签名") {
		t.Fatalf("未读取 .env 中的签名身份:\n%s", stderr.String())
	}
}

func TestPackagePersonalEnvOverridesDotenv(t *testing.T) {
	identity := ""
	cmd := packagePersonalDotenvCommand(t, &identity, true, `  1) ABCDEF "Developer ID Application: Test (ABC123)"`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if !strings.Contains(stderr.String(), "未设置 MCODE_SIGN_IDENTITY") {
		t.Fatalf("空环境变量未覆盖 .env:\n%s", stderr.String())
	}
}

func TestPackagePersonalAdHocWhenIdentityNotFound(t *testing.T) {
	cmd := packagePersonalDotenvCommand(t, nil, true, "")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if !strings.Contains(stderr.String(), "钥匙串中找不到签名身份") {
		t.Fatalf("缺少身份不存在的警告:\n%s", stderr.String())
	}
}

func TestPackagePersonalIdentityMatchesLiterally(t *testing.T) {
	identity := "Developer ID Application: Test*"
	cmd := packagePersonalDotenvCommand(t, &identity, false, `  1) ABCDEF "Developer ID Application: Test (ABC123)"`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if !strings.Contains(stderr.String(), "钥匙串中找不到签名身份") {
		t.Fatalf("缺少身份不存在的警告:\n%s", stderr.String())
	}
}

func TestPackagePersonalWindows(t *testing.T) {
	script, err := filepath.Abs("package_personal.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// 冒充非 macOS 系统，让脚本在参数解析之后、任何构建动作之前退出。
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/bash\necho Linux\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--windows", "--no-relaunch")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		t.Fatalf("预期失败（需要 macOS）却成功了\n%s", stderr.String())
	}
	stderrText := stderr.String()
	if !strings.Contains(stderrText, "需要 macOS") {
		t.Fatalf("missing 需要 macOS in:\n%s", stderrText)
	}
	if strings.Contains(stderrText, "未知参数") {
		t.Fatalf("--windows 未被识别:\n%s", stderrText)
	}
}
