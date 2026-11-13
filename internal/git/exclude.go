package git

import (
	"os"
	"path/filepath"
	"strings"
)

// EnsureGitExclude 尽力确保 <root>/.git/info/exclude 中包含指定的排除行。
// 仅当 <root>/.git 是目录时处理，不存在就追加，已存在就跳过，出错只忽略。
func EnsureGitExclude(root, entry string) {
	defer lockRepo(root)()
	gitDir := filepath.Join(root, ".git")
	st, err := os.Stat(gitDir)
	if err != nil || !st.IsDir() {
		return
	}

	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return
	}

	excludePath := filepath.Join(infoDir, "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == entry {
				return
			}
		}
	}

	var toAppend string
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		toAppend = "\n" + entry + "\n"
	} else {
		toAppend = entry + "\n"
	}

	f, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(toAppend)
}
