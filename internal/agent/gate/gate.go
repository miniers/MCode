// Package gate 实现内置 agent 的工具权限门禁。
package gate

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/patch"
	"mcode/internal/harness"
)

// Input 保存一次工具调用的门禁输入，对应 TS 的 gate、event 和 cwd。
type Input struct {
	Mode harness.PermissionMode
	Plan bool
	// Sandboxed 为 true 表示 bash 在只读 OS 沙箱里运行，本地写入由内核拦截，门禁只拦远程写。
	Sandboxed   bool
	FinalReport bool
	ToolName    string
	ToolID      string
	Args        json.RawMessage
	// ReadOnlyHint 由已解析的工具描述的 ReadOnly 提供，不来自模型参数。
	ReadOnlyHint   bool
	WorkDir        string
	AdditionalDirs []string
	// ControlPlanePaths 包含调用方需要保护的额外控制面文件或目录。
	ControlPlanePaths []string
	// HardLocked 表示当前线程是看板任务执行会话，危险命令需要用户先答决策卡。
	HardLocked bool
	// HardLockOverride 非空表示任务已获一次性解锁，只对本次运行有效。
	HardLockOverride string
}

// Action 表示门禁决定，对应 TS 的 verdictWithoutAsking。
type Action string

const (
	// Allow 表示直接放行。
	Allow Action = "allow"
	// Deny 表示直接拦截。
	Deny Action = "deny"
	// Ask 表示需要询问用户。
	Ask Action = "ask"
	// DefaultBashTimeout 是 bash 未指定有效超时时的默认值。
	DefaultBashTimeout = 300 * time.Second
	// MaxBashTimeout 是 bash 超时上限。
	MaxBashTimeout = 1800 * time.Second
)

// Verdict 保存门禁决定及需要用户授权时的请求。
type Verdict struct {
	Action     Action
	Reason     string
	Request    harness.ApprovalRequest
	SessionKey string
}

// Session 保存本会话中用户允许的非敏感请求。
type Session struct {
	mu      sync.RWMutex
	allowed map[string]bool
}

// Allowed 判断请求是否已获本会话授权。
func (s *Session) Allowed(key string) bool {
	if key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowed[key]
}

// Remember 记录本会话授权。
func (s *Session) Remember(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allowed == nil {
		s.allowed = make(map[string]bool)
	}
	s.allowed[key] = true
}

// ClampBashTimeout 按 TS 的 tool_call 处理默认 bash 超时和上限。
func ClampBashTimeout(seconds float64) time.Duration {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return DefaultBashTimeout
	}
	if seconds > 1800 {
		return MaxBashTimeout
	}
	return time.Duration(seconds * float64(time.Second))
}

// PlanModePrompt 返回 TS 的 PLAN_PROMPT。
func PlanModePrompt() string {
	return "Plan mode is on, so this turn is read-only: no file edits and no commands that change state. You can still investigate: read files, use grep / find / ls, run read-only shell commands (ls, cat, rg, git status, ...), and call the mcode MCP tools. Look into the task, then finish with the plan written in markdown."
}

// IsLookOnly 对应 TS 的 LOOK_ONLY_TOOLS。
// bash_output 只读；kill_bash 只停止本引擎自己启动的后台任务，不改变外部状态。
func IsLookOnly(name string) bool {
	switch name {
	case "read", "recall", "list_files", "search_text", "grep", "find", "ls", "mcode_mcp_list", "ask_user_question", "todo_write", "vision", "bash_output", "kill_bash":
		return true
	}
	return false
}

// argString 按键名取字符串参数。先精确匹配；匹配不到时退回大小写不敏感匹配，
// 因为部分 provider 用 struct 解析参数，encoding/json 的键名匹配不区分大小写。
func argString(args map[string]any, key string) string {
	if value, ok := args[key]; ok {
		if s, ok := value.(string); ok {
			return s
		}
	}
	for k, value := range args {
		if !strings.EqualFold(k, key) {
			continue
		}
		if s, ok := value.(string); ok {
			return s
		}
	}
	return ""
}

// Check 对应 TS 的 kindOf、checkSensitive 和 verdictWithoutAsking。
func Check(in Input) Verdict {
	var args map[string]any
	_ = json.Unmarshal(in.Args, &args)
	str := func(key string) string {
		return argString(args, key)
	}
	kind := harness.RequestKindTool
	if in.ToolName == "bash" {
		kind = harness.RequestKindCommand
	} else if in.ToolName == "write" || in.ToolName == "edit" || in.ToolName == "apply_patch" {
		kind = harness.RequestKindFileChange
	}
	command := str("command")
	// 硬锁只作用于看板任务的 bash 类调用；普通交互会话 HardLocked 为 false，行为不变。
	if in.HardLocked && in.ToolName == "bash" {
		if lock, hit := CheckHardLock(command, in.HardLockOverride); hit {
			return Verdict{Action: Deny, Reason: "危险操作已锁定，需要用户答决策卡后解锁：" + lock.Reason}
		}
	}
	webFetch := in.ToolName == "web_fetch" || in.ToolName == "mcp__magpie_search__web_search" || in.ToolName == "mcp__magpie_search__web_fetch"
	var patchPaths []string
	if in.ToolName == "apply_patch" {
		patchPaths, _ = patch.Paths(str("input"))
	}
	sensitive := checkSensitive(in, args, kind, command)
	// 最终报告阶段禁用全部工具；对应 verdictWithoutAsking。
	if in.FinalReport {
		return Verdict{Action: Deny, Reason: "Blocked by mcode: this turn is only for the final report, so tools are off."}
	}
	commandReads := kind == harness.RequestKindCommand && (OnlyReads(command) || in.Sandboxed && !RemoteWrites(command))
	reads := IsLookOnly(in.ToolName) || commandReads || webFetch
	// 计划模式允许无敏感内容的调研，内置 MCP 交给常规规则。
	if in.Plan {
		if reads {
			if sensitive == "" {
				return Verdict{Action: Allow}
			}
		} else if !strings.HasPrefix(in.ToolName, "mcp__mcode__") {
			return Verdict{Action: Deny, Reason: "Blocked by mcode: plan mode only allows reading."}
		}
	}
	if in.Mode == harness.ModeReadOnly && !reads && !in.ReadOnlyHint {
		if in.ToolName == "bash" {
			if in.Sandboxed {
				return Verdict{Action: Deny, Reason: "只读档不能向远程写入（git push、curl 提交数据、gh 写操作、ssh 等），请只做调研。"}
			}
			return Verdict{Action: Deny, Reason: "只读档只能运行只读命令，请使用 ls、cat、sed -n、rg、git log、go doc 或 read/grep 工具，不要换写法重试被拒的命令。"}
		}
		return Verdict{Action: Deny, Reason: "只读模式不允许修改：" + in.ToolName}
	}
	// 敏感调用总要询问，其余按模式决定放行。
	if sensitive == "" {
		if IsLookOnly(in.ToolName) || in.Mode == harness.ModeReadOnly && in.ReadOnlyHint || in.Mode == harness.ModeFullAccess || webFetch && (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits || in.Mode == harness.ModeReadOnly) || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits) && kind == harness.RequestKindFileChange || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits || in.Mode == harness.ModeReadOnly) && commandReads {
			return Verdict{Action: Allow}
		}
	}
	summary := ""
	switch {
	case in.ToolName == "apply_patch":
		summary = strings.Join(patchPaths, ", ")
	case webFetch:
		summary = str("url")
	case kind == harness.RequestKindCommand:
		summary = str("command")
	case kind == harness.RequestKindFileChange:
		summary = str("path")
	default:
		for _, k := range []string{"path", "pattern", "glob"} {
			if summary = str(k); summary != "" {
				break
			}
		}
	}
	name := in.ToolName
	if name == "" {
		name = "tool"
	}
	req := harness.ApprovalRequest{ToolID: in.ToolID, ToolName: name, Input: in.Args, Summary: summary, Sensitive: sensitive, Kind: kind}
	key := ""
	if sensitive == "" {
		b, err := json.Marshal([]any{in.ToolName, args})
		if err == nil {
			key = string(b)
		}
	}
	return Verdict{Action: Ask, Request: req, SessionKey: key}
}

// SensitiveFor 为外部 provider 的审批请求补算敏感标记；返回值同 checkSensitive。
func SensitiveFor(kind harness.RequestKind, toolName string, input json.RawMessage, command string, workDir string) string {
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	return checkSensitive(Input{ToolName: toolName, WorkDir: workDir}, args, kind, command)
}

var nullRedirect = regexp.MustCompile(`\d*&?>>?\s*/dev/null([\s;|&]|$)`)
var fdRedirect = regexp.MustCompile(`\d?>&\d`)

// OnlyReads 判断 shell 命令是否仅包含允许的只读操作。
func OnlyReads(command string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	// 丢弃输出及复制文件描述符不属于写文件。
	text := nullRedirect.ReplaceAllString(command, "$1")
	text = fdRedirect.ReplaceAllString(text, "")
	if strings.ContainsAny(text, "\r\n`") || strings.Contains(text, "$(") {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '&' && (i == 0 || text[i-1] != '&') && (i+1 == len(text) || text[i+1] != '&') {
			return false
		}
	}
	// 仅在引号外拆分命令；引号未闭合时保守处理。
	var pieces []string
	start := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if c == '\\' {
			if quote == 0 {
				return false
			}
			i++
			continue
		}
		if c == '$' {
			if quote == 0 || i+1 < len(text) && (text[i+1] >= 'a' && text[i+1] <= 'z' || text[i+1] >= 'A' && text[i+1] <= 'Z' || text[i+1] >= '0' && text[i+1] <= '9' || strings.ContainsRune("_{(@*#?$!-", rune(text[i+1]))) {
				return false
			}
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '<' || c == '>' {
			return false
		}
		if c == '|' || c == ';' || c == '&' && i+1 < len(text) && text[i+1] == '&' {
			pieces = append(pieces, text[start:i])
			if (c == '|' || c == '&') && i+1 < len(text) && text[i+1] == c {
				i++
			}
			start = i + 1
		}
	}
	if quote != 0 {
		return false
	}
	pieces = append(pieces, text[start:])
	safe := " ls cat head tail wc grep rg find tree du stat file pwd echo which sort diff cd whoami id uname printenv nl cut tr basename dirname realpath readlink md5 md5sum shasum sha1sum sha256sum od hexdump strings df uptime sw_vers ps jq true false "
	git := " status log diff show blame branch ls-files describe rev-parse rev-list shortlog cat-file ls-tree ls-remote show-ref name-rev merge-base for-each-ref count-objects grep whatchanged tag remote config reflog "
	branch := " -a -r -v -vv --list --all --remotes --show-current --verbose --contains --merged --no-merged "
	find := " -delete -exec -execdir -ok -okdir -fprint -fprint0 -fprintf -fls "
	for _, piece := range pieces {
		var t []string
		var word strings.Builder
		var q byte
		started := false
		for i := 0; i < len(piece); i++ {
			c := piece[i]
			if c == '\\' && q == '"' && i+1 < len(piece) {
				i++
				word.WriteByte(piece[i])
				started = true
				continue
			}
			if c == '\'' || c == '"' {
				if q == 0 {
					q = c
					started = true
					continue
				}
				if q == c {
					q = 0
					continue
				}
			}
			if q == 0 && (c == ' ' || c == '\t') {
				if started {
					t = append(t, word.String())
					word.Reset()
					started = false
				}
				continue
			}
			word.WriteByte(c)
			started = true
		}
		if started {
			t = append(t, word.String())
		}
		if len(t) == 0 {
			continue
		}
		p := t[0]
		if strings.HasPrefix(p, "/") {
			if filepath.Dir(p) != "/bin" && filepath.Dir(p) != "/usr/bin" {
				return false
			}
			p = filepath.Base(p)
		}
		switch p {
		case "curl":
			if !curlReads(t[1:]) {
				return false
			}
			continue
		case "hostname":
			if len(t) != 1 {
				return false
			}
			continue
		case "date":
			for _, a := range t[1:] {
				if a != "-u" && !strings.HasPrefix(a, "+") {
					return false
				}
			}
			continue
		case "env":
			if len(t) != 1 {
				return false
			}
			continue
		case "go", "adb", "defaults", "plutil", "command":
			allowed := map[string]string{"go": " version env list doc ", "adb": " devices version get-state get-serialno ", "defaults": " read ", "plutil": " -p ", "command": " -v -V "}
			if len(t) < 2 || !strings.Contains(allowed[p], " "+t[1]+" ") {
				return false
			}
			if p == "go" && t[1] == "env" {
				for _, a := range t[2:] {
					if strings.HasPrefix(a, "-") && a != "-json" && a != "-changed" {
						return false
					}
				}
			}
			if p == "go" && t[1] == "list" {
				for _, a := range t[2:] {
					if strings.HasPrefix(a, "-") {
						return false
					}
				}
			}
			if p == "plutil" {
				for _, a := range t[2:] {
					if strings.HasPrefix(a, "-") {
						return false
					}
				}
			}
			continue
		case "sed":
			if len(t) < 3 || t[1] != "-n" || !regexp.MustCompile(`^([0-9$]+,)?[0-9$]+p$`).MatchString(t[2]) {
				return false
			}
			for _, a := range t[3:] {
				if strings.HasPrefix(a, "-") {
					return false
				}
			}
			continue
		case "uniq", "xxd":
			// 两者都支持将第二个位置参数作为输出文件。
			count := 0
			for _, a := range t[1:] {
				if a == "-" || !strings.HasPrefix(a, "-") {
					count++
				}
			}
			if count > 1 {
				return false
			}
			continue
		}
		// git 只允许只读子命令和安全的参数。
		if p == "git" {
			i := 1
			for i < len(t) && t[i] == "-C" {
				if i+1 >= len(t) || t[i+1] == "" || gitArgDenied(t[i+1]) {
					return false
				}
				i += 2
			}
			if i >= len(t) {
				return false
			}
			sub := t[i]
			args := t[i+1:]
			for _, a := range args {
				if gitArgDenied(a) {
					return false
				}
			}
			if sub == "worktree" {
				if len(args) < 1 || args[0] != "list" {
					return false
				}
				continue
			}
			if !strings.Contains(git, " "+sub+" ") {
				return false
			}
			for _, a := range args {
				if sub == "branch" && !strings.Contains(branch, " "+a+" ") {
					return false
				}
			}
			if !gitReadArgs(sub, args) {
				return false
			}
			continue
		}
		if !strings.Contains(safe, " "+p+" ") {
			return false
		}
		// 拒绝 rg、sort、tree 和 find 中可执行命令或写文件的参数。
		for _, a := range t[1:] {
			sortShortWrite := p == "sort" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "oT")
			if p == "rg" && (a == "--pre" || strings.HasPrefix(a, "--pre=")) || p == "sort" && (sortShortWrite || strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--temporary-directory") || strings.HasPrefix(a, "--compress-program")) || p == "tree" && (strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output")) || p == "find" && strings.Contains(find, " "+a+" ") {
				return false
			}
		}
	}
	return true
}

func gitArgDenied(arg string) bool {
	if strings.HasPrefix(arg, "--") {
		name, _, _ := strings.Cut(arg, "=")
		if len(name) > 2 {
			for _, flag := range []string{"--output", "--exec-path", "--git-dir", "--work-tree", "--config-env", "--upload-pack", "--receive-pack", "--open-files-in-pager", "--ext-diff"} {
				if strings.HasPrefix(flag, name) {
					return true
				}
			}
		}
	}
	return strings.HasPrefix(arg, "-c")
}

func gitReadArgs(sub string, args []string) bool {
	switch sub {
	case "ls-remote":
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "u") {
				return false
			}
		}
	case "grep":
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "O") {
				return false
			}
		}
	case "remote":
		return len(args) == 0 || len(args) == 1 && (args[0] == "-v" || args[0] == "--verbose") || len(args) == 2 && (args[0] == "get-url" || args[0] == "show") && !strings.HasPrefix(args[1], "-")
	case "reflog":
		return len(args) == 0 || args[0] == "show"
	case "config":
		read := false
		for _, a := range args {
			switch a {
			case "--get", "--get-all", "--get-regexp", "-l", "--list":
				read = true
			case "--global", "--local", "--system", "--worktree", "--includes", "--no-includes", "--null", "-z", "--show-origin", "--show-scope":
			default:
				if strings.HasPrefix(a, "-") {
					return false
				}
			}
		}
		return read
	case "tag":
		list, positional := false, false
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "-l" || a == "--list":
				list = true
			case a == "-n":
			case strings.HasPrefix(a, "-n") && len(a) > 2 && strings.Trim(a[2:], "0123456789") == "":
			case strings.HasPrefix(a, "--sort="):
			case strings.HasPrefix(a, "--contains=") || strings.HasPrefix(a, "--points-at=") || strings.HasPrefix(a, "--merged=") || strings.HasPrefix(a, "--no-merged="):
				list = true
			case a == "--sort" || a == "--points-at":
				if a == "--points-at" {
					list = true
				}
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "-") {
					return false
				}
			case a == "--contains" || a == "--merged" || a == "--no-merged":
				list = true
			case strings.HasPrefix(a, "-"):
				return false
			default:
				positional = true
			}
		}
		return !positional || list
	}
	return true
}

func curlReads(args []string) bool {
	url := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
			url = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, value, hasValue := strings.Cut(a, "=")
			switch name {
			case "--silent", "--show-error", "--location", "--fail", "--head", "--include", "--verbose", "--compressed", "--insecure", "--globoff":
				if hasValue {
					return false
				}
				continue
			case "--max-time", "--connect-timeout", "--header", "--user-agent", "--referer", "--range", "--retry", "--retry-delay", "--write-out", "--request":
			default:
				return false
			}
			if !hasValue {
				i++
				if i >= len(args) {
					return false
				}
				value = args[i]
			}
			if !curlValueAllowed(name, value) {
				return false
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			return false
		}
		for j := 1; j < len(a); j++ {
			if strings.ContainsRune("sSLfIivkg", rune(a[j])) {
				continue
			}
			if !strings.ContainsRune("mHAerwX", rune(a[j])) {
				return false
			}
			name, value := "-"+string(a[j]), a[j+1:]
			if value == "" {
				i++
				if i >= len(args) {
					return false
				}
				value = args[i]
			}
			if !curlValueAllowed(name, value) {
				return false
			}
			break
		}
	}
	return url
}

func curlValueAllowed(name, value string) bool {
	if value == "" {
		return false
	}
	if name == "-X" || name == "--request" {
		return value == "GET" || value == "HEAD"
	}
	if name == "-H" || name == "--header" {
		return !strings.HasPrefix(value, "@")
	}
	return name != "-w" && name != "--write-out" || !strings.HasPrefix(value, "@") && !strings.Contains(value, "%output{")
}

// absolute 对应 TS 的 path.resolve。
func absolute(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return a
}

// within 对应 TS 的 isInsideOrEqual。
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// realNearest 对应 TS 的 resolveNearestExisting，解析最近存在的父目录。
func realNearest(p string) string {
	cur := p
	var parts []string
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(parts) - 1; i >= 0; i-- {
				real = filepath.Join(real, parts[i])
			}
			return real
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		parts = append(parts, filepath.Base(cur))
		cur = parent
	}
}

// controlPaths 对应 TS 的 getControlPlaneDirs；额外路径由调用方提供。
func controlPaths(in Input) []string {
	home, _ := os.UserHomeDir()
	dirs := append([]string(nil), in.ControlPlanePaths...)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".pi", "agent"), filepath.Join(home, ".mcode"))
	}
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		a := absolute(in.WorkDir, d)
		out = append(out, a)
		if r, e := filepath.EvalSymlinks(a); e == nil && r != a {
			out = append(out, r)
		}
	}
	return out
}

// insideControlPlaneWorkDir 判断 WorkDir 是否严格位于某个控制面目录之内（不等于目录本身）。
// swarm 子代理副本与 bee worktree 都放在 ~/.mcode 下，此时副本自身的文件不属于控制面。
func insideControlPlaneWorkDir(in Input, dirs []string) bool {
	wd := absolute(in.WorkDir, ".")
	if wd == "" || wd == string(filepath.Separator) {
		return false
	}
	real := realNearest(wd)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if wd != d && within(wd, d) {
			return true
		}
		if real != d && within(real, d) {
			return true
		}
	}
	return false
}

// stripWorkDir 把文本中出现的 WorkDir 路径替换成 "."，让只引用副本自身的命令不再命中控制面。
func stripWorkDir(text string, roots ...string) string {
	for _, root := range roots {
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		var b strings.Builder
		for {
			idx := strings.Index(text, root)
			if idx < 0 {
				b.WriteString(text)
				break
			}
			tail := idx + len(root)
			if idx > 0 && isPathByte(text[idx-1]) || tail < len(text) && isPathByte(text[tail]) {
				// 只是一段更长路径的前缀，原样保留后再继续找。
				b.WriteString(text[:tail])
				text = text[tail:]
				continue
			}
			b.WriteString(text[:idx])
			b.WriteString(".")
			text = text[tail:]
		}
		text = b.String()
	}
	return text
}

// isPathByte 判断字节是否属于路径中的文件名字符。
func isPathByte(c byte) bool {
	return c == '_' || c == '.' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

var homeVariable = regexp.MustCompile(`\$HOME\b`)

func normalizeHome(text string) string {
	home, _ := os.UserHomeDir()
	text = strings.NewReplacer("'", "", "\"", "").Replace(text)
	if home == "" {
		return text
	}
	text = strings.ReplaceAll(text, "${HOME}", home)
	text = homeVariable.ReplaceAllStringFunc(text, func(string) string { return home })
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '~' && (i == 0 || strings.ContainsRune(" \t\r\n=;|&(<>", rune(text[i-1]))) && (i+1 == len(text) || text[i+1] == '/') {
			b.WriteString(home)
		} else {
			b.WriteByte(text[i])
		}
	}
	return b.String()
}

func containsPath(text, path string) bool {
	for path != "" {
		i := strings.Index(text, path)
		if i < 0 {
			return false
		}
		end := i + len(path)
		if (i == 0 || !isPathByte(text[i-1]) && text[i-1] != '/') && (end == len(text) || strings.ContainsRune("/\\ \t\r\n\"';|&<>()", rune(text[end]))) {
			return true
		}
		text = text[end:]
	}
	return false
}

// checkSensitive 对应 TS 的 checkSensitive、isControlPlanePath 和 commandTouchesControlPlane。
func checkSensitive(in Input, args map[string]any, kind harness.RequestKind, command string) string {
	dirs := controlPaths(in)
	wd := absolute(in.WorkDir, ".")
	realWD := realNearest(wd)
	inside := insideControlPlaneWorkDir(in, dirs)
	var insideRoots []string
	if inside {
		insideRoots = []string{wd, realWD}
	}
	get := func(key string) string {
		return argString(args, key)
	}
	// 不同 provider 的路径键名不一致，逐个尝试后再判定。
	target := ""
	for _, key := range []string{"path", "file_path", "filePath", "notebook_path"} {
		if target = get(key); target != "" {
			break
		}
	}
	// apply_patch 会同时改动多个文件，逐个路径检查。
	var patchTargets []string
	if in.ToolName == "apply_patch" {
		patchTargets, _ = patch.Paths(argString(args, "input"))
	}
	writeTargets := append([]string{target}, patchTargets...)
	// 控制面写入优先于凭证判断，且解析不存在目标的父目录符号链接。
	if kind == harness.RequestKindFileChange {
		for _, t := range writeTargets {
			if controlPlaneTarget(in, dirs, inside, wd, realWD, t) {
				return "controlPlane"
			}
		}
	}
	if kind == harness.RequestKindCommand && !OnlyReads(command) {
		probe := normalizeHome(command)
		if inside {
			// 先把副本自身路径折叠成 "."，只引用副本的命令不再误判。
			probe = stripWorkDir(probe, insideRoots...)
		}
		for _, d := range dirs {
			if containsPath(probe, d) {
				return "controlPlane"
			}
		}
	}
	// fullAccess 仅豁免项目内的 .env，其他凭证仍需询问。
	project := ""
	if in.Mode == harness.ModeFullAccess {
		project = absolute(in.WorkDir, ".")
	}
	if IsLookOnly(in.ToolName) {
		for k, v := range args {
			s, ok := v.(string)
			if !ok {
				continue
			}
			k = strings.ToLower(k)
			if strings.Contains(k, "path") || strings.Contains(k, "pattern") || strings.Contains(k, "glob") || strings.Contains(k, "file") || strings.Contains(k, "dir") {
				if credential(s, project, insideRoots) {
					return "credential"
				}
			}
		}
	}
	if kind == harness.RequestKindCommand && credential(command, project, insideRoots) || (kind == harness.RequestKindFileChange || kind == harness.RequestKindTool) && credential(target, project, insideRoots) {
		return "credential"
	}
	if in.ToolName == "apply_patch" {
		for _, t := range patchTargets {
			if credential(t, project, insideRoots) {
				return "credential"
			}
		}
	}
	return ""
}

// controlPlaneTarget 判断单个写入目标是否落在控制面目录内。
// 位于控制面内的副本写自己的文件不算控制面；副本外的 ~/.mcode 路径照旧拦截。
func controlPlaneTarget(in Input, dirs []string, inside bool, wd, realWD, target string) bool {
	p := strings.TrimSpace(normalizeHome(target))
	if p == "" {
		return false
	}
	home, _ := os.UserHomeDir()
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	a := absolute(in.WorkDir, p)
	real := realNearest(a)
	if inside && (within(a, wd) || within(real, wd) || within(a, realWD) || within(real, realWD)) {
		return false
	}
	for _, d := range dirs {
		if within(a, d) || within(real, d) {
			return true
		}
	}
	return false
}

var procEnviron = regexp.MustCompile(`/proc/(self|[0-9]+)/environ\b`)
var envFile = regexp.MustCompile(`(^|[\\/\s"'` + "`" + `=])\.env(\.[^\s\\/'"` + "`" + `=;:,&|]+)?`)

// mcodeConfigJSON 判断 text 是否指向 mcode 数据目录下的 JSON 配置文件。
func mcodeConfigJSON(text, home string) bool {
	roots := []string{"~/.mcode/"}
	if home != "" {
		roots = append(roots, filepath.Join(home, ".mcode")+string(filepath.Separator))
	}
	for _, root := range roots {
		for rest := text; ; {
			idx := strings.Index(rest, root)
			if idx < 0 {
				break
			}
			rest = rest[idx+len(root):]
			name := rest
			if end := strings.IndexAny(name, " \t\r\n\"'`=;:,&|/\\"); end >= 0 {
				name = name[:end]
			}
			if strings.HasSuffix(strings.ToLower(name), ".json") {
				return true
			}
		}
	}
	return false
}

// credential 对应 TS 的 touchesCredential 和 matchesEnvFile。
// insideRoots 是位于控制面内的工作目录，其中的 JSON 不算 mcode 配置。
func credential(text, project string, insideRoots []string) bool {
	home, _ := os.UserHomeDir()
	original := text
	text = normalizeHome(text)
	for _, s := range []string{"~/.ssh/", "/.ssh/", "id_rsa", "id_ed25519", "~/.aws/", "~/.config/gcloud", "~/.docker/config.json", "~/.kube/config", "~/.netrc", "~/.pi/agent/auth.json", "~/.mcode/config.json", "~/.ssh", "~/.aws"} {
		if strings.Contains(original, s) || strings.Contains(text, s) {
			return true
		}
		if home != "" && strings.HasPrefix(s, "~/") && strings.Contains(text, filepath.Join(home, s[2:])) {
			return true
		}
	}
	// mcode 数据目录下的 JSON 都是控制面配置（always_allow.json、config.json、mcp.json 等）。
	if mcodeConfigJSON(stripWorkDir(text, insideRoots...), home) {
		return true
	}
	if procEnviron.MatchString(text) {
		return true
	}
	// 示例文件不是凭证；fullAccess 下项目内 .env 也可直接访问。
	for _, loc := range envFile.FindAllStringSubmatchIndex(text, -1) {
		start, end := loc[0], loc[1]
		if end < len(text) && !strings.ContainsRune("\\/ \t\r\n\"'`=;:,&|", rune(text[end])) {
			continue
		}
		ext := ""
		if loc[4] >= 0 {
			ext = strings.ToLower(text[loc[4]+1 : loc[5]])
		}
		if ext == "example" || ext == "sample" || ext == "template" {
			continue
		}
		idx := strings.Index(text[start:end], ".env") + start
		tokenStart := idx
		for tokenStart > 0 && !strings.ContainsRune(" \t\r\n\"'`=;:,&|", rune(text[tokenStart-1])) {
			tokenStart--
		}
		token := text[tokenStart:end]
		if project != "" && !strings.HasPrefix(token, "~") && within(absolute(project, token), project) {
			continue
		}
		return true
	}
	return false
}
