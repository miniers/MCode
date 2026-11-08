package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	for _, option := range []string{"--windows", "--bump"} {
		if !strings.Contains(string(output), option) {
			t.Fatalf("missing %s in help:\n%s", option, output)
		}
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
		if !strings.HasPrefix(entry, "MCODE_SIGN_IDENTITY=") && !strings.HasPrefix(entry, "MCODE_REFRESH_SNAPSHOT=") && !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "CLEAN=") && !strings.HasPrefix(entry, "BUMP=") {
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

func TestPackagePersonalVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
		env  string
		want string
	}{
		{name: "默认", want: "0.1.0"},
		{name: "参数", arg: "--bump", want: "0.1.1"},
		{name: "环境变量", env: "BUMP=1", want: "0.1.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := packagePersonalDotenvCommand(t, nil, false, "")
			root := filepath.Dir(filepath.Dir(cmd.Args[1]))
			if err := os.MkdirAll(filepath.Join(root, "web", "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			original := "{\n  \"version\": \"0.1.0\"\n}\n"
			if err := os.WriteFile(filepath.Join(root, "mygo.json"), []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			stubs := map[string]string{
				"npm": "#!/bin/bash\nexit 0\n",
				// 在构建阶段记录版本，然后失败以避免安装应用。
				"go": "#!/bin/bash\ncp mygo.json observed.json\nexit 1\n",
			}
			if runtime.GOOS == "linux" {
				// GNU sed 的原地编辑参数与 macOS 不同。
				stubs["sed"] = "#!/bin/bash\nif [[ $1 == -i ]]; then shift 2; exec /usr/bin/sed -i \"$@\"; fi\nexec /usr/bin/sed \"$@\"\n"
			}
			for name, content := range stubs {
				if err := os.WriteFile(filepath.Join(root, "bin", name), []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.arg != "" {
				cmd.Args = append(cmd.Args, tc.arg)
			}
			if tc.env != "" {
				cmd.Env = append(cmd.Env, tc.env)
			}
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("预期构建桩失败")
			}
			observed, err := os.ReadFile(filepath.Join(root, "observed.json"))
			if err != nil {
				t.Fatalf("未到达构建阶段: %v\n%s", err, output)
			}
			if !strings.Contains(string(observed), `"version": "`+tc.want+`"`) {
				t.Fatalf("构建时版本不正确: %s", observed)
			}
			after, err := os.ReadFile(filepath.Join(root, "mygo.json"))
			if err != nil || string(after) != original {
				t.Fatalf("失败后版本未恢复: %s, %v", after, err)
			}
			if tc.want == "0.1.0" && strings.Contains(string(output), "已还原 mygo.json 的版本号") {
				t.Fatalf("默认运行不应报告版本还原: %s", output)
			}
		})
	}
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
