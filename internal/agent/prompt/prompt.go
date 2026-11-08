// Package prompt 为 Agent 构建系统提示词并加载项目上下文。
package prompt

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Options 包含构建系统提示词所需的输入。
type Options struct {
	WorkDir        string
	AdditionalDirs []string
	HomeDir        string
	GlobalDir      string
	ToolNames      []string
	Skills         []Skill
	Now            time.Time
	OS             string
}

// Skill 描述一个可用的技能文件。
type Skill struct {
	Name        string
	Description string
	Path        string
}

var toolGuidance = map[string]string{
	"read":              "Read files before changing them; use it to inspect relevant code.",
	"bash":              "Run bounded commands; set run_in_background for servers and watchers; avoid destructive actions unless requested.",
	"edit":              "Make precise changes to existing files using exact text replacements.",
	"write":             "Create new files or completely rewrite a file only when needed.",
	"grep":              "Search file contents for relevant patterns.",
	"find":              "Locate files by name or glob pattern.",
	"ls":                "Inspect directory contents.",
	"ask_user_question": "Ask only when a decision or required information blocks progress.",
	"todo_write":        "Track progress on multi-step tasks.",
	"apply_patch":       "Change files with a patch in the apply_patch format.",
	"web_fetch":         "Fetch a web page when the task needs docs or information from the web.",
	"bash_output":       "Read new output from a background command.",
	"kill_bash":         "Stop background commands you started once they are no longer needed.",
}

// hasTool 报告工具列表中是否包含指定工具。
func hasTool(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// Build 根据可用工具、上下文和环境组装系统提示词。
func Build(opts Options) string {
	var sections []string
	principles := []string{
		"- Keep the solution simple and focused.",
		"- Read relevant files before changing them.",
	}
	if hasTool(opts.ToolNames, "edit") {
		principles = append(principles, "- Use edit for precise changes to existing files.")
	} else if hasTool(opts.ToolNames, "apply_patch") {
		principles = append(principles, "- Use apply_patch for changes to existing files.")
	}
	principles = append(principles,
		"- Do not invent file paths or claim unverified results.",
		"- When finished, briefly summarize the changes and any incomplete work.",
	)
	sections = append(sections, "You are a coding assistant. Help users inspect files, run commands, and modify code.\n\nWork principles:\n"+strings.Join(principles, "\n"))

	known := make(map[string]bool, len(opts.ToolNames))
	var tools []string
	for _, name := range opts.ToolNames {
		guidance, ok := toolGuidance[name]
		if !ok || known[name] {
			continue
		}
		known[name] = true
		tools = append(tools, "- "+name+": "+guidance)
	}
	if len(tools) > 0 {
		sections = append(sections, "Available tools:\n"+strings.Join(tools, "\n"))
	}

	files := ContextFiles(opts)
	if len(files) > 0 {
		var context strings.Builder
		context.WriteString("Project context:")
		for _, file := range files {
			fmt.Fprintf(&context, "\n\n--- %s ---\n%s", file.Path, file.Content)
		}
		sections = append(sections, context.String())
	}

	if len(opts.Skills) > 0 {
		var skills strings.Builder
		skills.WriteString("Available skills:")
		for _, skill := range opts.Skills {
			fmt.Fprintf(&skills, "\n- %s: %s (read %s when needed)", skill.Name, skill.Description, skill.Path)
		}
		sections = append(sections, skills.String())
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	osName := opts.OS
	if osName == "" {
		osName = runtime.GOOS
	}
	env := fmt.Sprintf("Environment:\n- Working directory: %s", opts.WorkDir)
	if len(opts.AdditionalDirs) > 0 {
		env += "\n- Additional directories: " + strings.Join(opts.AdditionalDirs, ", ")
	}
	env += fmt.Sprintf("\n- Date: %s\n- Operating system: %s", now.Format("2006-01-02"), osName)
	sections = append(sections, env)
	return strings.Join(sections, "\n\n")
}
