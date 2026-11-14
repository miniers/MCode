package agentconf

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"mcode/internal/fsutil"
	"mcode/internal/testenv"
)

func seedManagedSkill(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: sample\n---\n正文"), 0o640); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImportSkill(t *testing.T) {
	home := testenv.TempHome(t)
	source := seedManagedSkill(t, filepath.Join(t.TempDir(), "sample"))
	script := filepath.Join(source, "scripts", "run.sh")
	if err := os.Mkdir(filepath.Dir(script), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo skill"), 0o751); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	target, err := ImportSkill(home, source)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(home, ".agents", "skills", "sample") {
		t.Fatalf("目标错误：%s", target)
	}
	for _, rel := range []string{"SKILL.md", "scripts/run.sh"} {
		original, err := os.ReadFile(filepath.Join(source, rel))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(target, rel))
		if err != nil || string(copied) != string(original) {
			t.Fatalf("复制内容不一致：%s, %v", rel, err)
		}
		before, _ := os.Stat(filepath.Join(source, rel))
		after, _ := os.Stat(filepath.Join(target, rel))
		if before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("未保留权限：%s", rel)
		}
	}
	if link, err := os.Readlink(filepath.Join(target, "link")); err != nil || link != external {
		t.Fatalf("应复制符号链接本身：%s, %v", link, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("临时目录未清理：%v, %v", entries, err)
	}
}

func TestImportSkillRejectsInvalidSource(t *testing.T) {
	for _, kind := range []string{"缺少SKILL.md", "目标已存在", "源位于目标内", "源别名指向目标内", "源包含目标根", "SKILL.md是链接", "源不是目录"} {
		t.Run(kind, func(t *testing.T) {
			home := testenv.TempHome(t)
			source := seedManagedSkill(t, filepath.Join(t.TempDir(), "sample"))
			switch kind {
			case "缺少SKILL.md":
				if err := os.Remove(filepath.Join(source, "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "目标已存在":
				seedManagedSkill(t, filepath.Join(home, ".agents", "skills", "sample"))
			case "源包含目标根":
				source = seedManagedSkill(t, home)
			case "源位于目标内", "源别名指向目标内":
				source = seedManagedSkill(t, filepath.Join(home, ".agents", "skills", "sample"))
				if kind == "源别名指向目标内" {
					alias := filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(source, alias); err != nil {
						t.Fatal(err)
					}
					source = alias
				}
			case "SKILL.md是链接":
				md := filepath.Join(source, "SKILL.md")
				if err := os.Rename(md, md+".txt"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(md+".txt", md); err != nil {
					t.Fatal(err)
				}
			case "源不是目录":
				source = filepath.Join(source, "SKILL.md")
			}
			if _, err := ImportSkill(home, source); err == nil {
				t.Fatal("应拒绝导入")
			}
		})
	}
}

func TestImportSkillConcurrent(t *testing.T) {
	home := testenv.TempHome(t)
	source := seedManagedSkill(t, filepath.Join(t.TempDir(), "sample"))
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ImportSkill(home, source)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("同名并发导入成功 %d 次，应为 1", success)
	}
}

func TestDeleteSkill(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅 macOS 使用替换的废纸篓目录")
	}
	home := testenv.TempHome(t)
	trash := t.TempDir()
	previous := fsutil.TrashDir
	fsutil.TrashDir = func() (string, error) { return trash, nil }
	t.Cleanup(func() { fsutil.TrashDir = previous })
	project := t.TempDir()
	opts := Options{Home: home, ProjectDir: project, DataDir: t.TempDir()}
	for _, scope := range []string{"global", "project", "symlink"} {
		t.Run(scope, func(t *testing.T) {
			root := home
			if scope == "project" {
				root = project
			}
			dir := filepath.Join(root, ".agents", "skills", scope)
			target := ""
			if scope == "symlink" {
				target = seedManagedSkill(t, filepath.Join(t.TempDir(), "external"))
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				seedManagedSkill(t, dir)
			}
			sk := Skill{Agent: "codex", Scope: scope, Name: scope, Path: filepath.Join(dir, "SKILL.md")}
			for _, configScope := range []string{"global", "project"} {
				if err := saveDisabledSkills(getSkillsConfigPath(opts, configScope), map[string]bool{skillDisabledKey(sk): true, "keep": true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := DeleteSkill(opts, sk); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatalf("源条目仍在：%v", err)
			}
			if _, err := os.Lstat(filepath.Join(trash, scope)); err != nil {
				t.Fatal(err)
			}
			if target != "" {
				if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
					t.Fatalf("链接目标被修改：%v", err)
				}
				if got, err := os.Readlink(filepath.Join(trash, scope)); err != nil || got != target {
					t.Fatalf("废纸篓中应保留链接：%s, %v", got, err)
				}
			}
			for _, configScope := range []string{"global", "project"} {
				set, err := loadDisabledSkills(getSkillsConfigPath(opts, configScope))
				if err != nil || set[skillDisabledKey(sk)] || !set["keep"] {
					t.Fatalf("停用键清理错误：%v, %v", set, err)
				}
			}
		})
	}
}

func TestDeleteSkillRejectsPluginAndOutsidePath(t *testing.T) {
	home := testenv.TempHome(t)
	opts := Options{Home: home}
	for _, kind := range []string{"plugin", "outside", "nested", "root"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(home, ".agents", "skills", kind)
			sk := Skill{Name: kind}
			switch kind {
			case "plugin":
				sk.Plugin = "plugin"
			case "outside":
				dir = filepath.Join(t.TempDir(), "outside")
			case "nested":
				dir = filepath.Join(dir, "child")
			case "root":
				dir = filepath.Join(home, ".agents", "skills")
			}
			seedManagedSkill(t, dir)
			sk.Path = filepath.Join(dir, "SKILL.md")
			if err := DeleteSkill(opts, sk); err == nil {
				t.Fatal("应拒绝删除")
			}
			if _, err := os.Stat(sk.Path); err != nil {
				t.Fatalf("被拒绝的技能应保留：%v", err)
			}
		})
	}
}
