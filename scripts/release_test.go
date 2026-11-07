package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func releaseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func releaseRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")
	releaseGit(t, root, "init", "--bare", remote)
	releaseGit(t, root, "clone", remote, clone)
	releaseGit(t, clone, "switch", "-c", "main")
	releaseGit(t, clone, "config", "user.name", "测试用户")
	releaseGit(t, clone, "config", "user.email", "test@example.com")
	if err := os.Mkdir(filepath.Join(clone, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("release.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "scripts", "release.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "mygo.json"), []byte("{\n  \"version\": \"0.2.9\",\n  \"name\": \"测试\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseGit(t, clone, "add", ".")
	releaseGit(t, clone, "commit", "-m", "初始提交")
	releaseGit(t, clone, "push", "-u", "origin", "main")
	return clone, remote
}

func runRelease(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "scripts", "release.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReleaseVersions(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		args          []string
	}{
		{name: "默认补丁", version: "0.2.10"},
		{name: "次版本", version: "0.3.0", args: []string{"minor"}},
		{name: "预发布版本", version: "1.0.0-beta.1", args: []string{"1.0.0-beta.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, remote := releaseRepo(t)
			before := releaseGit(t, dir, "rev-parse", "HEAD")
			out, err := runRelease(t, dir, tc.args...)
			if err != nil {
				t.Fatalf("发布失败: %v\n%s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(dir, "mygo.json"))
			if err != nil {
				t.Fatal(err)
			}
			want := "{\n  \"version\": \"" + tc.version + "\",\n  \"name\": \"测试\"\n}\n"
			if string(data) != want {
				t.Fatalf("版本文件不符合预期: %s", data)
			}
			changelog, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(changelog), "\n## "+tc.version+"\n") || !strings.Contains(string(changelog), "- 初始提交") {
				t.Fatalf("更新日志不符合预期: %s", changelog)
			}
			if !strings.Contains(releaseGit(t, dir, "show", "--format=", "--name-only", "HEAD"), "CHANGELOG.md") {
				t.Fatal("发布提交缺少更新日志")
			}
			tag := "v" + tc.version
			if releaseGit(t, dir, "rev-parse", "HEAD^") != before || releaseGit(t, dir, "log", "-1", "--format=%s") != "chore(release): "+tag {
				t.Fatal("发布提交不符合预期")
			}
			if releaseGit(t, dir, "rev-parse", tag+"^{}") != releaseGit(t, dir, "rev-parse", "HEAD") {
				t.Fatal("标签未指向发布提交")
			}
			if releaseGit(t, dir, "ls-remote", "--tags", remote, "refs/tags/"+tag) == "" {
				t.Fatal("远程缺少发布标签")
			}
		})
	}
}

func TestReleaseNotesFromCommits(t *testing.T) {
	dir, _ := releaseRepo(t)
	releaseGit(t, dir, "tag", "v0.2.9")
	for _, subject := range []string{"feat: 新增更新说明", "test: 补测试"} {
		if err := os.WriteFile(filepath.Join(dir, "change"), []byte(subject), 0o644); err != nil {
			t.Fatal(err)
		}
		releaseGit(t, dir, "add", "change")
		releaseGit(t, dir, "commit", "-m", subject)
	}
	if out, err := runRelease(t, dir); err != nil {
		t.Fatalf("发布失败: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "- 新增更新说明") || strings.Contains(string(data), "补测试") || strings.Contains(string(data), "初始提交") {
		t.Fatalf("提交生成的更新日志不符合预期: %s", data)
	}
}

func TestReleaseNotesFile(t *testing.T) {
	dir, _ := releaseRepo(t)
	notes := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(notes, []byte("\n### 新功能\n- 中文说明\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRelease(t, dir, "--notes", notes); err != nil {
		t.Fatalf("发布失败: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# 更新日志\n\n## 0.2.10\n\n### 新功能\n- 中文说明\n" {
		t.Fatalf("指定的更新日志不符合预期: %s", data)
	}
}

func TestReleaseExistingChangelog(t *testing.T) {
	dir, _ := releaseRepo(t)
	path := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("# 更新日志\n\n## 0.2.9\n\n- 旧内容\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseGit(t, dir, "add", "CHANGELOG.md")
	releaseGit(t, dir, "commit", "-m", "docs: 旧日志")
	if out, err := runRelease(t, dir); err != nil {
		t.Fatalf("发布失败: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	newIndex, oldIndex := strings.Index(string(data), "## 0.2.10\n"), strings.Index(string(data), "## 0.2.9\n")
	if newIndex < 0 || oldIndex < 0 || newIndex >= oldIndex || !strings.Contains(string(data), "- 旧内容") {
		t.Fatalf("已有更新日志不符合预期: %s", data)
	}
}

func TestReleasePreconditionsAndDryRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		dirty, tag, dry bool
		want string
	}{
		{name: "工作区不干净", dirty: true, want: "工作区不干净"},
		{name: "标签已存在", tag: true, want: "本地标签 v0.2.10 已存在"},
		{name: "试运行", args: []string{"--dry-run"}, dry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, remote := releaseRepo(t)
			if tc.dirty {
				if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("未提交"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.tag {
				releaseGit(t, dir, "tag", "v0.2.10")
			}
			before := releaseGit(t, dir, "rev-parse", "HEAD")
			status := releaseGit(t, dir, "status", "--porcelain")
			version, err := os.ReadFile(filepath.Join(dir, "mygo.json"))
			if err != nil {
				t.Fatal(err)
			}
			out, err := runRelease(t, dir, tc.args...)
			if (err == nil) != tc.dry {
				t.Fatalf("运行结果不符合预期: %v\n%s", err, out)
			}
			if tc.dry {
				if !strings.Contains(out, "更新内容:") || !strings.Contains(out, "git add mygo.json CHANGELOG.md") {
					t.Fatalf("试运行输出缺少更新内容或暂存命令: %s", out)
				}
				if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); !os.IsNotExist(err) {
					t.Fatalf("试运行不应创建更新日志: %v", err)
				}
			}
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("输出不包含预期信息 %q: %s", tc.want, out)
			}
			after, err := os.ReadFile(filepath.Join(dir, "mygo.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(version) != string(after) || before != releaseGit(t, dir, "rev-parse", "HEAD") || status != releaseGit(t, dir, "status", "--porcelain") {
				t.Fatal("文件或提交意外改变")
			}
			if releaseGit(t, dir, "ls-remote", "--tags", remote, "refs/tags/v0.2.10") != "" {
				t.Fatal("远程意外出现标签")
			}
			if !tc.tag && releaseGit(t, dir, "tag", "--list", "v0.2.10") != "" {
				t.Fatal("本地意外出现标签")
			}
		})
	}
}
