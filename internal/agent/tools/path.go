package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func canonical(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", e
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}

func resolve(env Env, path string) (string, error) {
	if env.WorkDir == "" {
		return "", fmt.Errorf("work directory is required")
	}
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(env.WorkDir, path)
	}
	target, err := canonical(path)
	if err != nil {
		return "", err
	}
	for _, root := range append([]string{env.WorkDir}, env.AdditionalDirs...) {
		base, e := canonical(root)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(base, target)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return target, nil
		}
	}
	return "", fmt.Errorf("path outside allowed directories: %s", path)
}
