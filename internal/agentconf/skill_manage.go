package agentconf

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcode/internal/fsutil"
)

var skillImportMu sync.Mutex

// ImportSkill 把技能文件夹完整复制到统一的全局技能目录。
func ImportSkill(home, srcDir string) (string, error) {
	skillImportMu.Lock()
	defer skillImportMu.Unlock()
	home, _, err := resolveOptions(Options{Home: home})
	if err != nil {
		return "", err
	}
	srcDir, err = filepath.Abs(srcDir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("请选择技能文件夹")
	}
	md, err := os.Lstat(filepath.Join(srcDir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("读取 SKILL.md 失败：%w", err)
	}
	if !md.Mode().IsRegular() {
		return "", errors.New("SKILL.md 必须是普通文件")
	}
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if skillPathWithin(resolvedRoot, source) {
		return "", errors.New("不能从目标技能目录内导入")
	}
	if skillPathWithin(source, resolvedRoot) {
		return "", errors.New("技能文件夹不能包含目标技能目录")
	}
	target := filepath.Join(root, filepath.Base(srcDir))
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New("同名技能文件夹已存在")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".skill-import-")
	if err != nil {
		return "", err
	}
	defer func() {
		// 失败时也能清理已经复制为只读的目录。
		_ = filepath.WalkDir(tmp, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return err
		})
		_ = os.RemoveAll(tmp)
	}()
	if err := copySkillDirectory(source, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return target, nil
}

func skillPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copySkillDirectory(source, target string) error {
	var dirs []struct {
		path string
		mode fs.FileMode
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			if rel != "." {
				if err := os.Mkdir(dest, 0o700); err != nil {
					return err
				}
			}
			dirs = append(dirs, struct {
				path string
				mode fs.FileMode
			}{dest, info.Mode().Perm()})
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, dest)
		case info.Mode().IsRegular():
			return copySkillFile(path, dest, info.Mode().Perm())
		default:
			return fmt.Errorf("不支持的技能文件：%s", path)
		}
	})
	if err != nil {
		return err
	}
	// 子项复制完成后再收紧目录权限，允许导入只读目录。
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}
	return nil
}

func copySkillFile(source, target string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	modeErr := out.Chmod(mode)
	return errors.Join(copyErr, modeErr, out.Close())
}

// DeleteSkill 只允许把已知技能根目录的直接子项移到废纸篓。
func DeleteSkill(opts Options, skill Skill) error {
	if skill.Plugin != "" {
		return errors.New("不能删除插件技能")
	}
	home, projectDir, err := resolveOptions(opts)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(filepath.Dir(skill.Path))
	if err != nil {
		return err
	}
	roots := globalSkillSources(home)
	if projectDir != "" {
		roots = append(roots, projectSkillSources(projectDir)...)
	}
	allowed := false
	for _, root := range roots {
		if filepath.Dir(dir) == filepath.Clean(root.path) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("技能路径不在允许的技能目录内")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("技能路径必须是文件夹或符号链接")
	}
	if err := fsutil.MoveToTrash(dir); err != nil {
		return err
	}
	opts.Home, opts.ProjectDir = home, projectDir
	key := skillDisabledKey(skill)
	globalErr := removeSkillDisabledKeys(getSkillsConfigPath(opts, "global"), key)
	var projectErr error
	if projectDir != "" {
		projectErr = removeSkillDisabledKeys(getSkillsConfigPath(opts, "project"), key)
	}
	if err := errors.Join(globalErr, projectErr); err != nil {
		return fmt.Errorf("技能已移到废纸篓，但清理停用配置失败：%w", err)
	}
	return nil
}
