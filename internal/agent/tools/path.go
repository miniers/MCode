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
	links := 0
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
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", fmt.Errorf("too many symbolic links: %s", path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			path = target
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", e
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}

// SecretPath 判断路径是否指向受保护的凭据。
func SecretPath(target string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true
	}
	target, err = canonical(target)
	if err != nil {
		return true
	}
	for _, name := range []string{
		".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker/config.json",
		".netrc", ".npmrc", ".pypirc", ".git-credentials", ".password-store",
		".config/gcloud", ".config/gh", "Library/Keychains", ".codex/auth.json",
		".claude/.credentials.json", ".mcode/config.json", ".mcode/config.json.bak",
		".mcode/agy-accounts.json", ".mcode/agy-accounts", ".mcode/remote-tls", ".mcode/tailnet",
	} {
		base, err := canonical(filepath.Join(home, name))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, target)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func expandPath(env Env, path string) string {
	if path == "" {
		path = "."
	}
	// 将用户目录简写展开，避免误解析为工作目录下的相对路径。
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(env.WorkDir, path)
	}
	return path
}

func resolvePath(env Env, path string) (string, error) {
	if env.WorkDir == "" {
		return "", fmt.Errorf("work directory is required")
	}
	return canonical(expandPath(env, path))
}

func resolveRead(env Env, path string) (string, error) {
	target, err := resolvePath(env, path)
	if err != nil {
		return "", err
	}
	if SecretPath(target) {
		return "", fmt.Errorf("protected credential path: %s", target)
	}
	return target, nil
}

func resolve(env Env, path string) (string, error) {
	target, err := resolvePath(env, path)
	if err != nil {
		return "", err
	}
	roots := append([]string{env.WorkDir}, env.AdditionalDirs...)
	for _, root := range roots {
		base, e := canonical(root)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(base, target)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return target, nil
		}
	}
	return "", fmt.Errorf("path outside allowed directories: %s", expandPath(env, path))
}
