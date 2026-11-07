package magpie

// 本文件读本机 magpie 的 persona-rules 插件配置里的人设清单。mcode 只需要 id、名称
// 与描述做切换与展示，人设正文由插件自己注入，所以这里不改后端、也不请求网关：配置
// 文件就在本机配置目录里。远端 magpie 读不到本机文件，一律按「没有人设」处理。

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Persona 是 persona-rules 插件里的一条人设：只需要展示用到的三项，正文不读。
type Persona struct {
	ID          string
	Name        string
	Description string
}

const (
	// personaPluginName 是 persona-rules 插件在 magpie 里的短名（见 magpie 的
	// plugin.Called：去掉作用域、同类包前缀与版本后缀后按短名匹配）。
	personaPluginName = "persona-rules"
	// personaLimit 是一条 magpie 配置里最多采用的人设数，避免异常配置拖慢界面。
	personaLimit = 30
)

// personaIDPattern 是人设 ID 的可用字符集合：标签里直接出现，不能有空格或方括号。
var personaIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// configDir 解析 magpie 的配置目录。macOS 上 magpie 也用 ~/.config/magpie，
// 不是 Application Support；测试用 SetConfigDirForTest 覆盖。
var configDir = defaultConfigDir

// defaultConfigDir 返回 magpie 的配置目录；取不到主目录时返回空串。
func defaultConfigDir() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "magpie")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "magpie")
}

// SetConfigDirForTest 让测试把配置目录固定为 dir，返回的函数恢复原解析。
// 只给测试用：生产代码不调用。
func SetConfigDirForTest(dir string) func() {
	prev := configDir
	configDir = func() string { return dir }
	return func() { configDir = prev }
}

// pluginList 是 plugins.json 里本文件用到的部分。
type pluginList struct {
	Plugins []pluginEntry `json:"plugins"`
}

// pluginEntry 是 plugins.json 里的一个插件条目。
type pluginEntry struct {
	Spec    string       `json:"spec"`
	Off     bool         `json:"off,omitempty"`
	Options pluginOption `json:"options,omitempty"`
}

// pluginOption 是插件拿到的 options；persona-rules 读 enabled 与 personas。
type pluginOption struct {
	Enabled  *bool           `json:"enabled,omitempty"`
	Personas []personaOption `json:"personas,omitempty"`
}

// personaOption 是 options.personas 里的一条人设，text 只用来判断是否为空。
type personaOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Text        string `json:"text"`
}

// ReadPersonas 读 baseURL 对应 magpie 的人设列表。只有本机网关（localhost 或回环地址）
// 才读本机配置：其余情况、配置缺失、插件没装或被关掉都返回空列表与 nil，调用方据此
// 隐藏人设功能，不当错误处理。
func ReadPersonas(baseURL string) ([]Persona, error) {
	if !IsLocalGateway(baseURL) {
		return nil, nil
	}
	dir := configDir()
	if dir == "" {
		return nil, nil
	}
	list, ok := readPluginList(filepath.Join(dir, "plugins.json"))
	if !ok {
		return nil, nil
	}
	for _, entry := range list.Plugins {
		if !personaPluginCalled(entry.Spec) {
			continue
		}
		// 用户关掉了插件，或插件自己的 enabled 为 false：都按没有人设处理。
		if entry.Off || (entry.Options.Enabled != nil && !*entry.Options.Enabled) {
			return nil, nil
		}
		return collectPersonas(entry.Options.Personas), nil
	}
	return nil, nil
}

// IsLocalGateway 判断 baseURL 是不是本机地址：远端 magpie 没有本机配置文件可读。
func IsLocalGateway(baseURL string) bool {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return false
	}
	host := ""
	if u, err := url.Parse(raw); err == nil {
		host = u.Hostname()
	}
	if host == "" {
		// 允许不带 scheme 的写法（127.0.0.1:3425/v1）：Hostname 只认 authority。
		if u, err := url.Parse("//" + strings.TrimPrefix(strings.TrimPrefix(raw, "//"), "/")); err == nil {
			host = u.Hostname()
		}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// readPluginList 读并解析一个 plugins.json；文件不存在或解析失败时由调用方决定回退。
func readPluginList(path string) (pluginList, bool) {
	// 先读主文件，坏档或缺失再看 .bak：magpie 崩溃写坏时 .bak 是上一代可用内容。
	for _, candidate := range []string{path, path + ".bak"} {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var list pluginList
		if err := json.Unmarshal(raw, &list); err != nil {
			continue
		}
		return list, true
	}
	return pluginList{}, false
}

// personaPluginCalled 判断 spec 是不是 persona-rules 插件，匹配口径与 magpie 的
// plugin.Called 一致：去掉路径前缀与 @版本后缀，再去掉同类包共有的前缀与 -auth 后缀。
func personaPluginCalled(spec string) bool {
	return pluginShortName(spec) == personaPluginName
}

// pluginShortName 复刻 magpie 的 plugin.Name + plugin.ShortName 的结果。
func pluginShortName(spec string) string {
	pkg := spec
	if i := strings.LastIndexAny(pkg, `/\`); i >= 0 {
		pkg = pkg[i+1:]
	}
	if at := strings.LastIndex(pkg, "@"); at > 0 {
		pkg = pkg[:at]
	}
	for _, kind := range []string{"middleware-", "opencode-", "tools-", "page-", "agent-"} {
		if strings.HasPrefix(pkg, kind) && len(pkg) > len(kind) {
			pkg = pkg[len(kind):]
			break
		}
	}
	return strings.TrimSuffix(pkg, "-auth")
}

// collectPersonas 按配置顺序挑出可用的人设：ID 合法、正文非空，ID 去重保留第一个，
// 名字为空时回落为 ID，最多 personaLimit 条。
func collectPersonas(options []personaOption) []Persona {
	out := make([]Persona, 0, len(options))
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		id := option.ID
		// ID 直接出现在标签里，带空格或超长的不合法；没 trim，否则标签对不上插件那边的 ID。
		if !personaIDPattern.MatchString(id) || seen[id] {
			continue
		}
		if strings.TrimSpace(option.Text) == "" {
			// 没有正文的人设插件注入不了任何东西，界面上也不该出现。
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(option.Name)
		if name == "" {
			name = id
		}
		out = append(out, Persona{
			ID:          id,
			Name:        name,
			Description: strings.TrimSpace(option.Description),
		})
		if len(out) >= personaLimit {
			break
		}
	}
	return out
}
