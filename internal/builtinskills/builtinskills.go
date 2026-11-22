package builtinskills

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
	"mcode/internal/projctx"
)

const dirName = "builtin-skills"

//go:embed skills
var skillsFS embed.FS

// Dir 返回数据目录下内置技能根目录路径；dataDir 为空时返回空串。
func Dir(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, dirName)
}

// Install 将嵌入的内置技能写入 <dataDir>/builtin-skills/，并注册为 projctx 内置技能目录。
// 内容相同时跳过写盘；dataDir 为空时直接返回 nil。
func Install(dataDir string) error {
	// shortcut: 不清理已从二进制移除的旧内置技能，内置技能有删减时再补清理。
	rootDir := Dir(dataDir)
	if rootDir == "" {
		return nil
	}

	err := fs.WalkDir(skillsFS, "skills", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(path, "skills")
		rel = strings.TrimPrefix(rel, "/")
		target := rootDir
		if rel != "" {
			target = filepath.Join(rootDir, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := skillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			return nil
		}
		return fsutil.WriteFileAtomic(target, data, 0o644)
	})
	if err != nil {
		return err
	}

	projctx.SetBuiltinSkillsDir(rootDir)
	return nil
}
