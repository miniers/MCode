package commandcode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/modelcache"
)

// Model 是 Command Code 模型目录里的一行。
type Model struct {
	ID      string
	Name    string
	Detail  string
	Efforts []string
	// DefaultEffort 取自 ~/.commandcode/config.json 的 reasoningEffort；为空表示未配置。
	DefaultEffort string
	Default       bool
}

// SeedModels 返回 CLI 列表到达之前的初始目录。
func SeedModels() []Model {
	seed := []struct{ id, name, detail string }{
		{"claude-sonnet-5", "Claude Sonnet 5", "Anthropic · best combo of speed & intelligence"},
		{"claude-fable-5-1", "Claude Fable 5.1", "Anthropic · most capable for demanding reasoning & long-horizon agents"},
		{"claude-opus-5-5", "Claude Opus 5.5", "Anthropic · the newest Opus, for everyday complex work and coding"},
		{"claude-opus-5", "Claude Opus 5", "Anthropic · most intelligent Opus for agents and coding"},
		{"gpt-6-astra", "GPT-6 Astra", "OpenAI · most capable OpenAI model for demanding reasoning & agents"},
		{"gpt-5.6-sol", "GPT-5.6 Sol", "OpenAI · latest frontier model for general complex work"},
		{"gpt-5.6-luna", "GPT-5.6 Luna", "OpenAI · fast and efficient model"},
		{"google/gemini-3.8-flash", "Gemini 3.8 Flash", "Google · newest Gemini Flash, improved core reasoning"},
		{"deepseek/deepseek-v4-flash", "DeepSeek V4 Flash", "Open Source · fast hybrid-attention reasoning"},
		{"moonshotai/kimi-k3", "Kimi K3", "Open Source · long-horizon coding & knowledge work with 1M context"},
		{"zai-org/glm-5.3", "GLM 5.3", "Open Source · frontier coding with emergent cyber capabilities"},
	}
	out := make([]Model, len(seed))
	for i, s := range seed {
		out[i] = Model{ID: s.id, Name: s.name, Detail: s.detail, Efforts: commandcodeEfforts(s.id), Default: i == 0}
	}
	return out
}

var modelLinePattern = regexp.MustCompile(`^(\S+)\s{2,}(.+)$`)

// ParseModels 解析 `cmd --list-models` 的输出：分组标题行
// （如 "Anthropic"）之后每行一个 `<id>  <说明>`，部分带 "(default)" 或 "(recommended)"。
// configuredModel 是 CLI 配置里选中的模型，命中时成为唯一默认并排到最前；否则推荐行为默认。
// configuredEfforts 是 CLI 配置里各模型的 reasoningEffort。
func ParseModels(text, configuredModel string, configuredEfforts map[string]string) []Model {
	var options []Model
	section := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Available models") || strings.HasPrefix(line, "Pass the full id") ||
			strings.HasPrefix(line, "cmd ") || strings.HasPrefix(line, "Docs:") || strings.HasPrefix(line, "Usage") {
			continue
		}
		m := modelLinePattern.FindStringSubmatch(line)
		if m == nil {
			// 没有两个空格间隔的行是分组标题。
			first := []rune(line)[0]
			if (!strings.Contains(line, " ") && unicode.IsUpper(first)) || len([]rune(line)) < 24 {
				section = line
			}
			continue
		}
		id := m[1]
		if !strings.Contains(id, "-") && !strings.Contains(id, "/") {
			continue
		}
		detail := strings.TrimSpace(m[2])
		isDefault := false
		for _, tag := range []string{" (recommended)", " (default)"} {
			if strings.Contains(detail, tag) {
				detail = strings.Replace(detail, tag, "", 1)
				isDefault = true
			}
		}
		var parts []string
		if section != "" {
			parts = append(parts, section)
		}
		if detail != "" {
			parts = append(parts, detail)
		}
		options = append(options, Model{
			ID:            id,
			Name:          DisplayName(id),
			Detail:        strings.Join(parts, " · "),
			Efforts:       commandcodeEfforts(strings.ToLower(id)),
			DefaultEffort: configuredEfforts[id],
			Default:       isDefault,
		})
	}

	// 只保留一个默认：CLI 配置的模型优先（并排到最前），其次是第一个推荐行。
	chosen := -1
	if configuredModel != "" {
		for i, o := range options {
			if matchesModel(o.ID, configuredModel) {
				chosen = i
				break
			}
		}
	}
	if chosen >= 0 {
		picked := options[chosen]
		options = append(options[:chosen:chosen], options[chosen+1:]...)
		options = append([]Model{picked}, options...)
		chosen = 0
	} else {
		for i, o := range options {
			if o.Default {
				chosen = i
				break
			}
		}
	}
	for i := range options {
		options[i].Default = i == chosen
	}
	return options
}

// matchesModel 与 CLI 一致：按完整 id 或斜杠后的短名不区分大小写匹配。
func matchesModel(id, query string) bool {
	lhs, rhs := strings.ToLower(id), strings.ToLower(query)
	if lhs == rhs {
		return true
	}
	short := func(s string) string { return s[strings.LastIndex(s, "/")+1:] }
	return short(lhs) == short(rhs)
}

// DisplayName 由模型 id 生成显示名：去掉厂商前缀，
// 末尾成对的数字读作版本号，例如 `claude-fable-5-1` → "Claude Fable 5.1"，
// `moonshotai/kimi-k2.7-code` → "Kimi K2.7 Code"，`gpt-5.6-terra` → "GPT-5.6 Terra"。
func DisplayName(id string) string {
	short := id[strings.LastIndex(id, "/")+1:]
	base, _, _ := strings.Cut(short, ":")
	suffix := ""
	if strings.Contains(short, ":free") {
		suffix = " Free"
	}
	isNumber := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !unicode.IsDigit(r) {
				return false
			}
		}
		return true
	}
	tokens := strings.Split(base, "-")
	for i := 0; i+1 < len(tokens); {
		if isNumber(tokens[i]) && isNumber(tokens[i+1]) {
			tokens[i] += "." + tokens[i+1]
			tokens = append(tokens[:i+1], tokens[i+2:]...)
		} else {
			i++
		}
	}
	words := make([]string, len(tokens))
	for i, t := range tokens {
		switch strings.ToLower(t) {
		case "gpt", "glm":
			words[i] = strings.ToUpper(t)
		case "hy3", "hy4", "v4", "v4.1", "v2.5":
			words[i] = strings.ToUpper(t)
		default:
			if t == "" || unicode.IsDigit([]rune(t)[0]) {
				words[i] = t
			} else {
				words[i] = strings.ToUpper(t[:1]) + t[1:]
			}
		}
	}
	if len(words) > 1 && words[0] == "GPT" {
		return "GPT-" + strings.Join(words[1:], " ") + suffix
	}
	return strings.Join(words, " ") + suffix
}

// userConfig 读 ~/.commandcode/config.json 里 CLI 选中的模型与各模型的 reasoningEffort。
func userConfig(home string) (model string, efforts map[string]string) {
	data, err := os.ReadFile(filepath.Join(home, ".commandcode", "config.json"))
	if err != nil {
		return "", nil
	}
	var cfg struct {
		Model           string          `json:"model"`
		ReasoningEffort json.RawMessage `json:"reasoningEffort"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return "", nil
	}
	_ = json.Unmarshal(cfg.ReasoningEffort, &efforts)
	return cfg.Model, efforts
}

// FetchModels 运行 `cmd --list-models --no-auto-update` 读取账号可用的模型。
func FetchModels(ctx context.Context, binary string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, binary, "--list-models", "--no-auto-update"))
	cmd.Env = loginenv.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	model, efforts := userConfig(loginenv.HomeDirectory())
	return ParseModels(stripANSI(string(out)), model, efforts), nil
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

var modelCache modelcache.Cache[Model]

// CachedModels 返回 CLI 列出的模型；尚未拉到时返回 SeedModels，并在后台运行一次
// `cmd --list-models`（找不到 CLI 或失败后 10 分钟内不再重试）。
func CachedModels() []Model {
	if models := modelCache.Get(fetchCachedModels); len(models) > 0 {
		return models
	}
	return SeedModels()
}

// RefreshModels 强制重新读取 Command Code 的模型目录。
func RefreshModels(ctx context.Context, binary string) error {
	return modelCache.Refresh(ctx, func(ctx context.Context) ([]Model, error) {
		return fetchModelsWithBinary(ctx, binary)
	})
}

func fetchCachedModels(ctx context.Context) ([]Model, error) {
	return fetchModelsWithBinary(ctx, DefaultBinary)
}

func fetchModelsWithBinary(ctx context.Context, binary string) ([]Model, error) {
	if binary == "" {
		binary = DefaultBinary
	}
	var models []Model
	var bin string
	var err error
	if runtime.GOOS == "windows" && strings.EqualFold(binary, DefaultBinary) {
		bin, err = ResolveBinary()
	} else {
		bin, err = loginenv.LookPath(binary)
	}
	if err == nil {
		models, err = FetchModels(ctx, bin)
		if err == nil {
			models = FilterByPlan(ctx, models, PlanLimitsOptions{HomeDir: loginenv.HomeDirectory()})
		}
	}
	return models, err
}
