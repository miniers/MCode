//go:build darwin || linux

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 假工具链：把每次调用记到 CHECK_LOG，行为由环境变量控制。
const fakeGo = `#!/usr/bin/env bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$CHECK_LOG"
if [[ "${1:-}" == "list" ]]; then
    printf 'mcode/internal/a\nmcode/build.noindex/stage0/seed\nmcode/dist/darwin-arm64/mcode.app/Contents/Resources\n'
fi
exit 0
`

const fakeGofmt = `#!/usr/bin/env bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$CHECK_LOG"
printf '%s' "${CHECK_GOFMT_OUTPUT:-}"
exit 0
`

// fakeGit 必须输出至少一个文件：macOS 的 xargs 遇到空输入时不执行命令，
// 原来的假 git 什么都不输出，gofmt 用例就从未真正调用过假 gofmt。
const fakeGit = `#!/usr/bin/env bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$CHECK_LOG"
if [[ "${1:-}" == "ls-files" ]]; then
    printf 'a.go\0'
fi
exit 0
`

type checkFixture struct {
	root  string
	log   string
	bin   string
	envvs []string
}

// newCheckFixture 在临时目录里搭一个假仓库根：真实的 check.sh 加假工具链，
// 只验证脚本的编排逻辑，不跑真正的构建和测试。
func newCheckFixture(t *testing.T) *checkFixture {
	t.Helper()
	root := t.TempDir()

	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("check.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "check.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	// 依赖规则检查是独立脚本，这里用总返回成功的替身。
	if err := os.WriteFile(filepath.Join(scriptsDir, "check-deps.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go": fakeGo, "gofmt": fakeGofmt, "git": fakeGit} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return &checkFixture{
		root: root,
		log:  filepath.Join(root, "check.log"),
		bin:  bin,
	}
}

// run 跑一遍 check.sh，返回合并输出、日志行和退出错误。
func (f *checkFixture) run(t *testing.T, extra map[string]string) (string, []string, error) {
	t.Helper()
	args := append([]string{filepath.Join(f.root, "scripts", "check.sh")}, f.envvs...)
	cmd := exec.Command("bash", args...)
	env := append(os.Environ(),
		"PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CHECK_LOG="+f.log,
	)
	for k := range extra {
		env = append(env, k+"="+extra[k])
	}
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return string(output), f.lines(), err
}

func (f *checkFixture) lines() []string {
	data, _ := os.ReadFile(f.log)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestCheckScriptExcludesBuildOutput(t *testing.T) {
	f := newCheckFixture(t)
	_, lines, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("check.sh 应当成功退出: %v\n%s", err, strings.Join(lines, "\n"))
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "go vet") || strings.HasPrefix(line, "go test") {
			if !strings.Contains(line, "mcode/internal/a") {
				t.Fatalf("检查命令缺少有效包: %s", line)
			}
		}
		if (strings.Contains(line, "build.noindex") || strings.Contains(line, "mcode/dist/")) &&
			strings.HasPrefix(line, "go ") && !strings.HasPrefix(line, "go list") {
			t.Fatalf("打包产物包不应参与检查: %s", line)
		}
		if strings.Contains(line, "-race") {
			t.Fatalf("未传 --race 时不应跑 race: %s", line)
		}
	}

	var sawBuild, sawVet, sawTest bool
	for _, line := range lines {
		if strings.HasPrefix(line, "go build") && strings.Contains(line, "mcode/internal/a") {
			sawBuild = true
		}
		if strings.HasPrefix(line, "go vet") && strings.Contains(line, "mcode/internal/a") {
			sawVet = true
		}
		if strings.HasPrefix(line, "go test") && strings.Contains(line, "mcode/internal/a") {
			sawTest = true
		}
	}
	if !sawBuild || !sawVet || !sawTest {
		t.Fatalf("缺少 go build、go vet 或 go test 调用:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCheckScriptRaceFlag(t *testing.T) {
	f := newCheckFixture(t)
	f.envvs = []string{"--race"}
	_, lines, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("check.sh --race 应当成功退出: %v\n%s", err, strings.Join(lines, "\n"))
	}

	for _, line := range lines {
		if strings.Contains(line, "test") && strings.Contains(line, "-race") {
			return
		}
	}
	t.Fatalf("缺少带 -race 的 go test 调用:\n%s", strings.Join(lines, "\n"))
}

func TestCheckScriptStopsOnGofmtDiff(t *testing.T) {
	f := newCheckFixture(t)
	output, lines, err := f.run(t, map[string]string{"CHECK_GOFMT_OUTPUT": "bad.go\n"})
	if err == nil {
		t.Fatal("gofmt 有差异时应当非零退出")
	}

	if !strings.Contains(output, "bad.go") {
		t.Fatalf("未报告未格式化文件:\n%s", output)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "go build") {
			t.Fatalf("gofmt 失败后不应继续构建: %s", line)
		}
	}
}
