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

	"mcode/internal/harness"
)

// Input 保存一次工具调用的门禁输入，对应 TS 的 gate、event 和 cwd。
type Input struct {
	Mode           harness.PermissionMode
	Plan           bool
	FinalReport    bool
	ToolName       string
	ToolID         string
	Args           json.RawMessage
	WorkDir        string
	AdditionalDirs []string
	// ControlPlanePaths 包含调用方需要保护的额外控制面文件或目录。
	ControlPlanePaths []string
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
func IsLookOnly(name string) bool {
	switch name {
	case "read", "grep", "find", "ls", "mcode_mcp_list", "ask_user_question", "todo_write", "vision":
		return true
	}
	return false
}

// Check 对应 TS 的 kindOf、checkSensitive 和 verdictWithoutAsking。
func Check(in Input) Verdict {
	var args map[string]any
	_ = json.Unmarshal(in.Args, &args)
	str := func(key string) string {
		value, _ := args[key].(string)
		return value
	}
	kind := harness.RequestKindTool
	if in.ToolName == "bash" {
		kind = harness.RequestKindCommand
	} else if in.ToolName == "write" || in.ToolName == "edit" {
		kind = harness.RequestKindFileChange
	}
	command := str("command")
	sensitive := checkSensitive(in, args, kind, command)
	// 最终报告阶段禁用全部工具；对应 verdictWithoutAsking。
	if in.FinalReport {
		return Verdict{Action: Deny, Reason: "Blocked by mcode: this turn is only for the final report, so tools are off."}
	}
	reads := IsLookOnly(in.ToolName) || kind == harness.RequestKindCommand && onlyReads(command)
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
	// 敏感调用总要询问，其余按模式决定放行。
	if sensitive == "" {
		if IsLookOnly(in.ToolName) || in.Mode == harness.ModeFullAccess || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits) && kind == harness.RequestKindFileChange || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits || in.Mode == harness.ModeReadOnly) && kind == harness.RequestKindCommand && onlyReads(command) {
			return Verdict{Action: Allow}
		}
	}
	summary := ""
	switch kind {
	case harness.RequestKindCommand:
		summary = str("command")
	case harness.RequestKindFileChange:
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

var nullRedirect = regexp.MustCompile(`\d*&?>>?\s*/dev/null([\s;|&]|$)`)
var fdRedirect = regexp.MustCompile(`\d?>&\d`)

// onlyReads 对应 TS 的 onlyReads 和 splitCommandPieces。
func onlyReads(command string) bool {
	// 丢弃输出及复制文件描述符不属于写文件。
	text := nullRedirect.ReplaceAllString(command, "$1")
	text = fdRedirect.ReplaceAllString(text, "")
	if strings.ContainsAny(text, "\r\n>`") || strings.Contains(text, "$(") || strings.Contains(text, "<(") || strings.Contains(text, ">(") {
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
			i++
			continue
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
	safe := " ls cat head tail wc grep rg find tree du stat file pwd echo which sort uniq diff cd "
	git := " status log diff show blame branch ls-files "
	branch := " -a -r -v -vv --list --all --remotes --show-current --verbose --contains --merged --no-merged "
	find := " -delete -exec -execdir -ok -okdir -fprint -fprint0 -fprintf -fls "
	for _, piece := range pieces {
		t := strings.Fields(piece)
		if len(t) == 0 {
			continue
		}
		p := t[0]
		// git 只允许只读子命令和安全的 branch 参数。
		if p == "git" {
			if len(t) < 2 {
				return false
			}
			sub := t[1]
			if sub == "worktree" {
				if len(t) < 3 || t[2] != "list" {
					return false
				}
				continue
			}
			if !strings.Contains(git, " "+sub+" ") {
				return false
			}
			for _, a := range t[2:] {
				if a == "--ext-diff" || a == "--output" || strings.HasPrefix(a, "--output=") || sub == "branch" && !strings.Contains(branch, " "+a+" ") {
					return false
				}
			}
			continue
		}
		if !strings.Contains(safe, " "+p+" ") {
			return false
		}
		// 拒绝 rg、sort 和 find 中可执行命令或写文件的参数。
		for _, a := range t[1:] {
			if p == "rg" && (a == "--pre" || strings.HasPrefix(a, "--pre=")) || p == "sort" && (strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output")) || p == "find" && strings.Contains(find, " "+a+" ") {
				return false
			}
		}
	}
	return true
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
		dirs = append(dirs, filepath.Join(home, ".pi", "agent"))
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

// checkSensitive 对应 TS 的 checkSensitive、isControlPlanePath 和 commandTouchesControlPlane。
func checkSensitive(in Input, args map[string]any, kind harness.RequestKind, command string) string {
	dirs := controlPaths(in)
	get := func(key string) string {
		value, _ := args[key].(string)
		return value
	}
	target := get("path")
	if target == "" {
		target = get("file_path")
	}
	// 控制面写入优先于凭证判断，且解析不存在目标的父目录符号链接。
	if kind == harness.RequestKindFileChange && strings.TrimSpace(target) != "" {
		p := strings.TrimSpace(target)
		home, _ := os.UserHomeDir()
		if p == "~" {
			p = home
		} else if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		a := absolute(in.WorkDir, p)
		real := realNearest(a)
		for _, d := range dirs {
			if within(a, d) || within(real, d) {
				return "controlPlane"
			}
		}
	}
	if kind == harness.RequestKindCommand && !onlyReads(command) {
		for _, d := range dirs {
			if strings.Contains(command, d) {
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
				if credential(s, project) {
					return "credential"
				}
			}
		}
	}
	if kind == harness.RequestKindCommand && credential(command, project) || kind == harness.RequestKindFileChange && credential(target, project) {
		return "credential"
	}
	return ""
}

var procEnviron = regexp.MustCompile(`/proc/(self|[0-9]+)/environ\b`)
var envFile = regexp.MustCompile(`(^|[\\/\s"'` + "`" + `=])\.env(\.[^\s\\/'"` + "`" + `=;:,&|]+)?`)

// credential 对应 TS 的 touchesCredential 和 matchesEnvFile。
func credential(text, project string) bool {
	home, _ := os.UserHomeDir()
	for _, s := range []string{"~/.ssh/", "/.ssh/", "id_rsa", "id_ed25519", "~/.aws/", "~/.config/gcloud", "~/.docker/config.json", "~/.kube/config", "~/.netrc", "~/.pi/agent/auth.json", "~/.ssh", "~/.aws"} {
		if strings.Contains(text, s) {
			return true
		}
		if home != "" && strings.HasPrefix(s, "~/") && strings.Contains(text, filepath.Join(home, s[2:])) {
			return true
		}
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
