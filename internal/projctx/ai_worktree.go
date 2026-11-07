package projctx

import (
	"os"
	"path/filepath"
	"strings"
)

// ToolWrite 描述会话时间线里一次工具调用留下的写入线索：
// 命令类工具给出命令原文（其中的 cd、git -C、重定向与各写入命令都会被解析），
// 编辑类工具给出它直接写入的文件路径。
type ToolWrite struct {
	Command string
	Paths   []string
}

// shellSubcommand 是一条命令里按 &&、||、;、|、& 和换行拆出的一段，以及它是否带输出重定向。
type shellSubcommand struct {
	words    []string
	redirect bool
}

// LocateWorktree 按会话时间线判断会话实际写入的 worktree 并返回其绝对路径。
// root 为会话自身的工作目录，worktrees 为同仓库下的其他候选 worktree：调用方应剔除应用自管
// （见 git.IsManagedWorktreePath）与属于其他会话的目录。返回空串表示会话没有在自建 worktree 里干活
// ——写入都落在 root 之内，或者时间线里没有能对上任何 worktree 的写入。
//
// 判定顺序：按时间顺序回放工具调用，cwd 跟随 cd（git -C 只影响那一条命令），
// 收集写入路径；再从最新的写入往回找，取包含该路径的最长 worktree。
func LocateWorktree(root string, worktrees []string, writes []ToolWrite) string {
	rootPath := standardizePath(root)
	if rootPath == "" {
		return ""
	}
	candidates := make([]string, 0, len(worktrees)+1)
	candidates = append(candidates, rootPath)
	for _, wt := range worktrees {
		if path := standardizePath(wt); path != "" && path != rootPath {
			candidates = append(candidates, path)
		}
	}

	cwd := rootPath
	var paths []string
	for _, write := range writes {
		if command := strings.TrimSpace(write.Command); command != "" {
			paths = append(paths, commandWrites(command, &cwd)...)
		}
		for _, path := range write.Paths {
			path = strings.TrimSpace(path)
			if path == "" || strings.Contains(path, "://") {
				continue
			}
			paths = append(paths, resolveAgainst(rootPath, path))
		}
	}

	for i := len(paths) - 1; i >= 0; i-- {
		if holder := worktreeHolder(paths[i], candidates); holder != "" && holder != rootPath {
			return holder
		}
	}
	return ""
}

// standardizePath 规整为绝对路径：展开 ~、清理冗余分隔符，并在路径（或其父目录）存在时解析符号链接。
// 路径不存在时保留清理后的形式，保证写入尚未落盘的文件也能参与前缀匹配。
func standardizePath(path string) string {
	path = expandHome(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		path = abs
	}
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(real, filepath.Base(path))
	}
	return path
}

// expandHome 把开头的 ~ 展开为用户主目录。
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// resolveAgainst 把可能是相对的 path 解析到 base 之下，并规整为绝对路径。
func resolveAgainst(base, path string) string {
	path = expandHome(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return standardizePath(path)
}

// worktreeHolder 返回包含 path 的最长候选路径；path 不在任何候选之下时返回空串。
func worktreeHolder(path string, candidates []string) string {
	best := ""
	for _, candidate := range candidates {
		if path != candidate && !strings.HasPrefix(path, candidate+string(filepath.Separator)) {
			continue
		}
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	return best
}

// commandWrites 返回一条命令写入的目录与路径（由旧到新）。cwd 随 cd 前进，
// 因为同一条会话里的命令共用一个 shell 工作目录。
func commandWrites(command string, cwd *string) []string {
	var writes []string
	for _, sub := range splitShellSubcommands(command) {
		words := dropEnvAssignments(sub.words)
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" {
			target := "~"
			if len(words) > 1 {
				target = words[1]
			}
			*cwd = resolveAgainst(*cwd, trimToken(target))
			continue
		}

		dir := *cwd
		args := words
		if words[0] == "git" && len(words) > 2 && words[1] == "-C" {
			dir = resolveAgainst(*cwd, trimToken(words[2]))
			args = append([]string{words[0]}, words[3:]...)
		}
		if !sub.redirect && !isWriteCommand(args) {
			continue
		}
		// 命令本身在 dir 里跑，先把 dir 记为一次写入，再补上它点名的路径。
		writes = append(writes, dir)
		if args[0] == "git" {
			if path := gitWorktreeAddPath(args); path != "" {
				writes = append(writes, resolveAgainst(dir, trimToken(path)))
			}
		}
		for _, word := range args[1:] {
			token := trimToken(word)
			if !isPathToken(token) {
				continue
			}
			writes = append(writes, resolveAgainst(dir, token))
		}
	}
	return writes
}

// splitShellSubcommands 按 &&、||、;、|、&、< 和换行拆分命令，处理单双引号与反引号，
// 并记录每段是否带输出重定向（>、>>、2>、&> 都算）。
func splitShellSubcommands(command string) []shellSubcommand {
	var (
		subs     []shellSubcommand
		words    []string
		word     strings.Builder
		quote    rune
		redirect bool
	)
	flushWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	flushSub := func() {
		flushWord()
		if len(words) > 0 || redirect {
			subs = append(subs, shellSubcommand{words: words, redirect: redirect})
		}
		words = nil
		redirect = false
	}

	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
		case ' ', '\t', '\r':
			flushWord()
		case '\n', ';', '|', '&', '<':
			flushSub()
		case '>':
			redirect = true
			flushWord()
		default:
			word.WriteRune(r)
		}
	}
	flushSub()
	return subs
}

// dropEnvAssignments 去掉命令开头的环境变量赋值（如 GIT_DIR=x git ...）。
func dropEnvAssignments(words []string) []string {
	for len(words) > 0 {
		eq := strings.IndexByte(words[0], '=')
		if eq <= 0 || !isEnvName(words[0][:eq]) {
			return words
		}
		words = words[1:]
	}
	return words
}

// isEnvName 判断是否形如环境变量名（字母或下划线开头，只含字母、数字与下划线）。
func isEnvName(name string) bool {
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return name != ""
}

// trimToken 去掉路径参数上常被一起写出的分隔符。
func trimToken(token string) string {
	return strings.Trim(token, ",:")
}

// isPathToken 判断参数是否显式写成路径（绝对路径、家目录或相对路径）。
func isPathToken(token string) bool {
	for _, prefix := range []string{"/", "~", "./", "../"} {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

// isWriteCommand 按命令名判断它是否写文件或写 Git 历史。
func isWriteCommand(words []string) bool {
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "tee", "mv", "cp", "rm", "mkdir", "touch", "patch", "apply_patch":
		return true
	case "sed":
		for _, word := range words {
			if strings.HasPrefix(word, "-i") || strings.HasPrefix(word, "--in-place") {
				return true
			}
		}
		return false
	case "perl":
		for _, word := range words {
			if strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") && len(word) <= 3 && strings.Contains(word, "i") {
				return true
			}
		}
		return false
	case "git":
		verb := ""
		for _, word := range words[1:] {
			if strings.HasPrefix(word, "-") {
				continue
			}
			verb = word
			break
		}
		if verb == "worktree" {
			// `git worktree list` 只读。
			for _, word := range words {
				if word == "list" {
					return false
				}
			}
			return true
		}
		switch verb {
		case "add", "commit", "apply", "merge", "checkout", "switch", "restore", "stash", "cherry-pick", "rebase", "reset":
			return true
		}
		return false
	default:
		return false
	}
}

// gitWorktreeAddPath 返回 `git worktree add` 的目标路径，跳过 -b、-B、--reason 及其取值。
func gitWorktreeAddPath(words []string) string {
	hasWorktree, addIdx := false, -1
	for i, word := range words {
		if word == "worktree" {
			hasWorktree = true
		}
		if word == "add" && addIdx < 0 {
			addIdx = i
		}
	}
	if !hasWorktree || addIdx < 0 {
		return ""
	}
	skipsValue := false
	for _, word := range words[addIdx+1:] {
		if skipsValue {
			skipsValue = false
			continue
		}
		switch word {
		case "-b", "-B", "--reason":
			skipsValue = true
			continue
		}
		if strings.HasPrefix(word, "-") {
			continue
		}
		return word
	}
	return ""
}
