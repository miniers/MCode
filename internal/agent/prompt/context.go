package prompt

import (
	"os"
	"path/filepath"
)

const (
	maxContextFileBytes  = 32 * 1024
	maxContextTotalBytes = 96 * 1024
)

var contextCandidates = []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

// ContextFile 是已加载的项目指令文件。
type ContextFile struct {
	Path    string
	Content string
}

// ContextFiles 加载全局及继承的项目指令文件。
func ContextFiles(opts Options) []ContextFile {
	var files []ContextFile
	seen := make(map[string]bool)
	addDir := func(dir string) {
		if dir == "" {
			return
		}
		for _, name := range contextCandidates {
			path := filepath.Join(dir, name)
			canonical, err := filepath.Abs(path)
			if err != nil {
				canonical = filepath.Clean(path)
			}
			if seen[canonical] {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			content := string(data)
			if len(data) > maxContextFileBytes {
				content = string(data[:maxContextFileBytes]) + "\n[Truncated: file exceeds 32 KB.]"
			}
			files = append(files, ContextFile{Path: canonical, Content: content})
			seen[canonical] = true
			return
		}
	}

	addDir(opts.GlobalDir)
	if opts.WorkDir == "" {
		return files
	}
	workDir, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		workDir = filepath.Clean(opts.WorkDir)
	}
	home := ""
	if opts.HomeDir != "" {
		home, _ = filepath.Abs(opts.HomeDir)
		home = filepath.Clean(home)
	}
	var reverseDirs []string
	current := filepath.Clean(workDir)
	stopped := false
	for {
		reverseDirs = append(reverseDirs, current)
		if (home != "" && current == home) || isGitRoot(current) {
			stopped = true
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if !stopped {
		reverseDirs = []string{workDir}
	}
	for left, right := 0, len(reverseDirs)-1; left < right; left, right = left+1, right-1 {
		reverseDirs[left], reverseDirs[right] = reverseDirs[right], reverseDirs[left]
	}
	ancestorFiles := make([]ContextFile, 0, len(reverseDirs))
	for _, dir := range reverseDirs {
		before := len(files)
		addDir(dir)
		if len(files) > before {
			ancestorFiles = append(ancestorFiles, files[len(files)-1])
		}
	}
	files = append([]ContextFile(nil), files[:len(files)-len(ancestorFiles)]...)
	total := 0
	for _, file := range files {
		total += len(file.Content)
	}
	for _, file := range ancestorFiles {
		total += len(file.Content)
	}
	for len(ancestorFiles) > 0 && total > maxContextTotalBytes {
		total -= len(ancestorFiles[0].Content)
		ancestorFiles = ancestorFiles[1:]
	}
	files = append(files, ancestorFiles...)
	return files
}

func isGitRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}
