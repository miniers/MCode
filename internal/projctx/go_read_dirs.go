package projctx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var goReadDirsCache = struct {
	sync.Mutex
	dirs map[string][]string
}{dirs: make(map[string][]string)}

// GoReadDirs 返回 Go 项目的模块缓存和标准库源码目录，供只读工具访问。
func GoReadDirs(projectPath string) []string {
	if _, err := os.Stat(filepath.Join(projectPath, "go.mod")); err != nil {
		return nil
	}

	goReadDirsCache.Lock()
	defer goReadDirsCache.Unlock()
	if dirs, ok := goReadDirsCache.dirs[projectPath]; ok {
		return append([]string(nil), dirs...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE", "GOROOT")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var dirs []string
	for i, line := range strings.Split(string(output), "\n") {
		if i > 1 {
			break
		}
		dir := strings.TrimSpace(line)
		if !filepath.IsAbs(dir) {
			continue
		}
		if i == 1 {
			dir = filepath.Join(dir, "src")
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, filepath.Clean(dir))
		}
	}
	goReadDirsCache.dirs[projectPath] = dirs
	return append([]string(nil), dirs...)
}
