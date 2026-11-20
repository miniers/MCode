package harness

import (
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// resumeSessionToken is the placeholder in a stored resume template.
// A template is the argv after the binary, for example "--resume sessionid"
// or "resume sessionid".
const resumeSessionToken = "sessionid"

// defaultResumeTemplates are the resume commands known for agents that ship
// with mcode. A provider missing from this map has no menu entry unless a
// config override is registered. The token "sessionid" is replaced with the
// stored session id.
//
// Sources:
//   - claude: https://code.claude.com/docs/en/sessions (`claude --resume <session-id>`)
//   - commandcode, omp: the same `--resume` flag mcode already appends when it relaunches them
//   - pi: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md (`--session`)
//   - codex: `codex resume [SESSION_ID]` (CLI subcommand, not `--resume`)
//   - droid: https://docs.factory.ai/cli/reference/cli-reference (`droid --resume [sessionId]`)
//   - devin: https://docs.devin.ai/cli/reference/commands (`--resume <SESSION_ID>`)
//   - cursor: https://cursor.com/docs/cli/reference/parameters (`--resume [chatId]`)
//   - cline: https://github.com/cline/cline/blob/main/docs/cli/cli-reference.mdx (`--id <session-id>`)
//   - opencode: https://opencode.ai/v1/docs/cli (`--session` / `-s`); v2 run also takes `--session`
//   - grok: https://docs.x.ai/build/cli/reference (`-r, --resume [<ID>]`)
//   - agy: https://antigravity.google/docs/cli/headless (`--conversation <id>`)
//
// copilot's `--resume` is a picker with no session-id form, so it is absent.
var defaultResumeTemplates = map[string]string{
	"claude":      "--resume sessionid",
	"commandcode": "--resume sessionid",
	"omp":         "--resume sessionid",
	"pi":          "--session sessionid",
	"codex":       "resume sessionid",
	"droid":       "--resume sessionid",
	"devin":       "--resume sessionid",
	"cursor":      "--resume sessionid",
	"cline":       "--id sessionid",
	"opencode":    "--session sessionid",
	"grok":        "--resume sessionid",
	"agy":         "--conversation sessionid",
}

var (
	resumeMu       sync.RWMutex
	resumeOverride = map[string]string{}
)

// RegisterResumeTemplate stores a config-supplied template for provider.
// An empty template removes the override and falls back to the built-in
// default, which may itself be empty.
func RegisterResumeTemplate(provider, template string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	resumeMu.Lock()
	defer resumeMu.Unlock()
	if strings.TrimSpace(template) == "" {
		delete(resumeOverride, provider)
		return
	}
	resumeOverride[provider] = strings.TrimSpace(template)
}

// ResumeTemplate returns the resume argv template for provider, or "" when
// the agent did not register one. The menu item is omitted in that case.
func ResumeTemplate(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	resumeMu.RLock()
	if t, ok := resumeOverride[provider]; ok {
		resumeMu.RUnlock()
		return t
	}
	resumeMu.RUnlock()
	return defaultResumeTemplates[provider]
}

// FormatResumeCommand builds the shell command copied to the clipboard.
// It returns "" when the provider has no template, the binary is unknown,
// or the session id is empty.
func FormatResumeCommand(provider, binary, sessionID string) string {
	tmpl := ResumeTemplate(provider)
	if tmpl == "" || binary == "" || sessionID == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(provider), "omp") {
		sessionID = ompSessionID(sessionID)
	}
	before, after, ok := strings.Cut(tmpl, resumeSessionToken)
	if !ok {
		return ""
	}
	return binary + " " + before + shellQuote(sessionID) + after
}

// ompSessionID returns the id omp --resume accepts. mcode stores the session
// file path; the id is the suffix of <timestamp>_<id>.jsonl. Anything else is
// returned unchanged.
func ompSessionID(session string) string {
	base := filepath.Base(session)
	if !strings.HasSuffix(base, ".jsonl") {
		return session
	}
	name := strings.TrimSuffix(base, ".jsonl")
	id := name[strings.LastIndex(name, "_")+1:]
	if !ompSessionIDPattern.MatchString(id) {
		return session
	}
	return id
}

var ompSessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// FormatResumeCommandIn is FormatResumeCommand with `cd <dir> &&` in front.
// dir is the project directory. A relative dir is made absolute. An empty dir
// leaves the command unchanged. An empty command stays empty.
func FormatResumeCommandIn(dir, provider, binary, sessionID string) string {
	cmd := FormatResumeCommand(provider, binary, sessionID)
	dir = strings.TrimSpace(dir)
	if cmd == "" || dir == "" {
		return cmd
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return "cd " + shellQuote(dir) + " && " + cmd
}

// shellQuote quotes an argument that contains whitespace, quotes, or shell
// metacharacters. Other arguments are returned unchanged.
func shellQuote(arg string) string {
	if arg == "" || !strings.ContainsAny(arg, " \t\n\"'$`\\!*?[]()&|;<>") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
