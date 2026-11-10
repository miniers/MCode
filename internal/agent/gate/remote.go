package gate

import (
	"path/filepath"
	"strings"
)

// RemoteWrites 尽力检查命令是否向远程写入，仅用于防误操作，不是安全边界。
func RemoteWrites(command string) bool {
	return remoteWrites(command, 0)
}

func remoteWrites(s string, depth int) bool {
	if depth > 32 {
		return true
	}
	var words []string
	var word strings.Builder
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	check := func() bool { flush(); result := remoteCommand(words, depth); words = nil; return result }
	var quote byte
	parens := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		if c == '\\' {
			if i+1 == len(s) {
				return true
			}
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", rune(s[i+1])) {
				word.WriteByte(c)
				started = true
				continue
			}
			i++
			word.WriteByte(s[i])
			started = true
			continue
		}
		if c == '`' || c == '$' && i+1 < len(s) && s[i+1] == '(' {
			start := i + 1
			end := -1
			if c == '$' {
				start++
				nesting := 1
				var q byte
				for j := start; j < len(s); j++ {
					ch := s[j]
					if ch == '\\' && q != '\'' {
						j++
						continue
					}
					if q == '\'' {
						if ch == '\'' {
							q = 0
						}
						continue
					}
					if ch == '\'' && q == 0 {
						q = ch
						continue
					}
					if ch == '"' {
						if q == '"' {
							q = 0
						} else if q == 0 {
							q = ch
						}
						continue
					}
					if q == 0 {
						if ch == '(' {
							nesting++
						}
						if ch == ')' {
							nesting--
							if nesting == 0 {
								end = j
								break
							}
						}
					}
				}
			} else {
				for j := start; j < len(s); j++ {
					if s[j] == '\\' {
						j++
						continue
					}
					if s[j] == '`' {
						end = j
						break
					}
				}
			}
			if end < 0 || remoteWrites(s[start:end], depth+1) {
				return true
			}
			started = true
			i = end
			continue
		}
		if c == '"' {
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			started = true
			continue
		}
		if quote == 0 && c == '\'' {
			quote = c
			started = true
			continue
		}
		if quote == 0 && strings.ContainsRune(";|&\n()", rune(c)) {
			if c == '(' {
				parens++
			} else if c == ')' {
				parens--
				if parens < 0 {
					return true
				}
			}
			if check() {
				return true
			}
			continue
		}
		if quote == 0 && strings.ContainsRune(" \t\r", rune(c)) {
			flush()
			continue
		}
		word.WriteByte(c)
		started = true
	}
	if quote != 0 || parens != 0 {
		return true
	}
	return check()
}

func assignment(s string) bool {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return false
	}
	for j, c := range s[:i] {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func remoteCommand(t []string, depth int) bool {
	for len(t) > 0 {
		if assignment(t[0]) {
			t = t[1:]
			continue
		}
		name := filepath.Base(t[0])
		t = t[1:]
		switch name {
		case "env", "sudo", "command", "exec", "nohup", "time", "nice", "timeout", "xargs", "stdbuf":
			duration := name == "timeout"
			for len(t) > 0 {
				a := t[0]
				if a == "--" {
					t = t[1:]
					break
				}
				if name == "env" && assignment(a) {
					t = t[1:]
					continue
				}
				if strings.HasPrefix(a, "-") && a != "-" {
					t = t[1:]
					takes := name == "env" && (a == "-u" || a == "--unset" || a == "-C" || a == "--chdir") || name == "nice" && a == "-n" || name == "timeout" && (a == "-s" || a == "-k" || a == "--signal" || a == "--kill-after") || name == "stdbuf" && (a == "-i" || a == "-o" || a == "-e") || name == "xargs" && (a == "-n" || a == "-P" || a == "-s" || a == "-L" || a == "-I" || a == "-d" || a == "-E" || a == "-a") || name == "sudo" && (a == "-u" || a == "-g" || a == "-h" || a == "-p" || a == "-C" || a == "-D" || a == "-R" || a == "-T")
					if takes {
						if len(t) == 0 {
							return true
						}
						t = t[1:]
					}
					continue
				}
				if duration {
					duration = false
					t = t[1:]
					continue
				}
				break
			}
			continue
		case "sh", "bash", "zsh", "dash":
			for i, a := range t {
				if a == "--" {
					break
				}
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "c") {
					if i+1 >= len(t) {
						return true
					}
					return remoteWrites(t[i+1], depth+1)
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
			}
			return false
		case "eval":
			return remoteWrites(strings.Join(t, " "), depth+1)
		case "ssh", "scp", "sftp", "rsync":
			return true
		case "git":
			for len(t) > 0 && strings.HasPrefix(t[0], "-") {
				a := t[0]
				t = t[1:]
				if a == "-C" || a == "-c" {
					if len(t) == 0 {
						return true
					}
					t = t[1:]
				}
			}
			return len(t) > 0 && (t[0] == "push" || t[0] == "send-email")
		case "curl":
			return curlRemote(t)
		case "wget":
			return wgetRemote(t)
		case "gh":
			return ghRemote(t)
		case "npm", "pnpm", "yarn", "bun", "cargo":
			return len(t) > 0 && t[0] == "publish"
		case "docker":
			return len(t) > 0 && t[0] == "push"
		default:
			return false
		}
	}
	return false
}

func safeMethod(s string, head bool) bool {
	return strings.EqualFold(s, "GET") || head && strings.EqualFold(s, "HEAD")
}

func curlRemote(t []string) bool {
	for i := 0; i < len(t); i++ {
		a := t[i]
		if a == "--data" || strings.HasPrefix(a, "--data-") || strings.HasPrefix(a, "--data=") || a == "--json" || strings.HasPrefix(a, "--json=") || a == "--form" || strings.HasPrefix(a, "--form=") || a == "--form-string" || strings.HasPrefix(a, "--form-string=") || a == "--upload-file" || strings.HasPrefix(a, "--upload-file=") || a == "--config" || strings.HasPrefix(a, "--config=") {
			return true
		}
		if a == "--request" {
			i++
			if i >= len(t) || !safeMethod(t[i], true) {
				return true
			}
			continue
		}
		if strings.HasPrefix(a, "--request=") {
			if !safeMethod(strings.TrimPrefix(a, "--request="), true) {
				return true
			}
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			for j := 1; j < len(a); j++ {
				if strings.ContainsRune("dFTK", rune(a[j])) {
					return true
				}
				if a[j] == 'X' {
					value := a[j+1:]
					if value == "" {
						i++
						if i >= len(t) {
							return true
						}
						value = t[i]
					}
					if !safeMethod(value, true) {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

func wgetRemote(t []string) bool {
	for i := 0; i < len(t); i++ {
		a := t[i]
		for _, flag := range []string{"--post-data", "--post-file", "--body-data", "--body-file"} {
			if a == flag || strings.HasPrefix(a, flag+"=") {
				return true
			}
		}
		if a == "--method" {
			i++
			if i >= len(t) || !safeMethod(t[i], true) {
				return true
			}
		} else if strings.HasPrefix(a, "--method=") && !safeMethod(strings.TrimPrefix(a, "--method="), true) {
			return true
		}
	}
	return false
}

func ghRemote(t []string) bool {
	if len(t) == 0 || t[0] == "--version" || t[0] == "help" || t[0] == "search" {
		return false
	}
	if t[0] == "auth" {
		return len(t) < 2 || t[1] != "status"
	}
	if t[0] == "api" {
		for i := 1; i < len(t); i++ {
			a := t[i]
			if a == "--field" || a == "--raw-field" || a == "--input" || strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input=") {
				return true
			}
			if a == "--method" || a == "-X" {
				i++
				if i >= len(t) || !safeMethod(t[i], false) {
					return true
				}
				continue
			}
			if strings.HasPrefix(a, "--method=") {
				if !safeMethod(strings.TrimPrefix(a, "--method="), false) {
					return true
				}
				continue
			}
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				for j := 1; j < len(a); j++ {
					if a[j] == 'f' || a[j] == 'F' {
						return true
					}
					if a[j] == 'X' {
						value := a[j+1:]
						if value == "" {
							i++
							if i >= len(t) {
								return true
							}
							value = t[i]
						}
						if !safeMethod(value, false) {
							return true
						}
						break
					}
				}
			}
		}
		return false
	}
	return len(t) < 2 || !strings.Contains(" view list status diff checks watch ", " "+t[1]+" ")
}
