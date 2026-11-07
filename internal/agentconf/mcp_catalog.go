package agentconf

import (
	"errors"
	"os"
	"sort"
	"strings"
)

// 预设条目字段的取值类型。
const (
	MCPCatalogFieldSecret = "secret"
	MCPCatalogFieldText   = "text"
	MCPCatalogFieldPath   = "path"
)

// 预设条目分类，同时也是设置页里的分组顺序与标题。
const (
	MCPCatalogCategoryDeveloper = "开发"
	MCPCatalogCategoryBrowser   = "浏览器"
	MCPCatalogCategoryKnowledge = "知识"
	MCPCatalogCategorySearch    = "搜索"
	MCPCatalogCategoryWork      = "协作"
	MCPCatalogCategoryData      = "数据"
	MCPCatalogCategoryCloud     = "云"
)

// MCPCatalogField 描述预设条目在连接前需要用户填写的一项内容。
// Key 同时是 Args、Env、URL、Headers 中的占位符名（写作 {key}）。
type MCPCatalogField struct {
	Key         string
	Label       string
	Kind        string // 取 secret、text、path
	Default     string
	Placeholder string
	Required    bool
	Help        string
}

// MCP 登录方式。auto 表示按服务器元数据判断，等价于未声明；
// device 走设备流，loopback 走授权码 + PKCE + 本地回调。
// 与 internal/mcp 包里的同名常量各自定义，字符串取值必须保持一致。
const (
	MCPAuthModeAuto     = "auto"
	MCPAuthModeLoopback = "loopback"
	MCPAuthModeDevice   = "device"
)

// MCPCatalogAuth 描述目录条目声明的登录方式。
// ClientID 与 Scopes 留空时由服务器元数据决定，公共 client id 必须实测可用才能入库。
type MCPCatalogAuth struct {
	Mode     string   `json:"mode,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// validateMCPAuth 校验登录方式，nil 视为未声明（等价于 auto）。
// 非法取值一律报错，避免脏配置写进 mcp.json。
func validateMCPAuth(auth *MCPCatalogAuth) error {
	if auth == nil || auth.Mode == "" {
		return nil
	}
	switch auth.Mode {
	case MCPAuthModeAuto, MCPAuthModeLoopback, MCPAuthModeDevice:
		return nil
	default:
		return errors.New("不支持的 MCP 登录方式：" + auth.Mode)
	}
}

// cloneCatalogAuth 深拷贝登录元数据，Scopes 复制成新 slice，
// 避免调用方改动共享的目录条目。
func cloneCatalogAuth(auth *MCPCatalogAuth) *MCPCatalogAuth {
	if auth == nil {
		return nil
	}
	cloned := *auth
	if len(auth.Scopes) > 0 {
		cloned.Scopes = append([]string(nil), auth.Scopes...)
	}
	return &cloned
}

// MCPCatalogEntry 描述一个开箱可添加的 MCP 服务预设。
// Transport 取 stdio、http、oauth；oauth 条目在 mcode 里仍以 http 传输接入，
// 由设置页已有的登录按钮完成授权。
type MCPCatalogEntry struct {
	ID          string
	Name        string
	Vendor      string
	Category    string
	Summary     string
	SampleTools []string
	DocsURL     string
	KeysURL     string
	Transport   string
	Command     string
	Args        []string
	Env         map[string]string
	URL         string
	Headers     map[string]string
	Fields      []MCPCatalogField
	Auth        *MCPCatalogAuth `json:"auth,omitempty"`
}

// MCPCatalogCategories 返回分类的展示顺序。
func MCPCatalogCategories() []string {
	return []string{
		MCPCatalogCategoryDeveloper,
		MCPCatalogCategoryBrowser,
		MCPCatalogCategoryKnowledge,
		MCPCatalogCategorySearch,
		MCPCatalogCategoryWork,
		MCPCatalogCategoryData,
		MCPCatalogCategoryCloud,
	}
}

// Resolve 把用户填写的字段套进预设，得到可直接保存的 MCPServer。
// secretEnvKeys 列出必须存入凭据库的环境变量名（如 PostgreSQL 预设的 DSN），
// 同时写进 srv.SecretEnvKeys，供 SaveMCPServer 按调用方声明加密。
func (e MCPCatalogEntry) Resolve(values map[string]string) (MCPServer, []string, error) {
	if err := validateMCPAuth(e.Auth); err != nil {
		return MCPServer{}, nil, err
	}

	filled := make(map[string]string, len(e.Fields))
	for _, f := range e.Fields {
		val := strings.TrimSpace(values[f.Key])
		if val == "" {
			val = strings.TrimSpace(f.Default)
		}
		if val == "" && f.Required {
			label := f.Label
			if label == "" {
				label = f.Key
			}
			return MCPServer{}, nil, errors.New("请填写" + label)
		}
		filled[f.Key] = val
	}

	secretFields := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		if f.Kind == MCPCatalogFieldSecret {
			secretFields[f.Key] = true
		}
	}

	srv := MCPServer{
		Agent: "custom",
		Scope: "global",
		Name:  e.ID,
		Auth:  cloneCatalogAuth(e.Auth),
	}

	for _, arg := range e.Args {
		srv.Args = append(srv.Args, fillCatalogPlaceholders(arg, filled))
	}

	var secretEnvKeys []string
	if len(e.Env) > 0 {
		srv.Env = make(map[string]string, len(e.Env))
		for name, tmpl := range e.Env {
			val := fillCatalogPlaceholders(tmpl, filled)
			// 引用了未填字段的变量整条略过，避免把空值当成有效配置。
			if val == "" && catalogTemplateHasEmptyField(tmpl, filled) {
				continue
			}
			srv.Env[name] = val
			for _, key := range catalogTemplateFields(tmpl) {
				if secretFields[key] {
					secretEnvKeys = append(secretEnvKeys, name)
					break
				}
			}
		}
	}
	sort.Strings(secretEnvKeys)
	srv.SecretEnvKeys = secretEnvKeys

	if len(e.Headers) > 0 {
		srv.Headers = make(map[string]string, len(e.Headers))
		for name, tmpl := range e.Headers {
			// 可选密钥留空时不发 `Bearer ` 这类半截请求头，整条去掉。
			if catalogTemplateHasEmptyField(tmpl, filled) {
				continue
			}
			srv.Headers[name] = fillCatalogPlaceholders(tmpl, filled)
		}
	}

	switch e.Transport {
	case "http", "oauth":
		srv.Transport = "http"
		srv.URL = fillCatalogURL(e.URL, filled)
	default:
		srv.Transport = "stdio"
		srv.Command = e.Command
	}

	return srv, secretEnvKeys, nil
}

// fillCatalogPlaceholders 把 {key} 替换为用户取值，未知占位符原样保留。
func fillCatalogPlaceholders(text string, values map[string]string) string {
	var b strings.Builder
	rest := text
	for {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end += start
		b.WriteString(rest[:start])
		key := rest[start+1 : end]
		if val, ok := values[key]; ok {
			b.WriteString(val)
		} else {
			b.WriteString(rest[start : end+1])
		}
		rest = rest[end+1:]
	}
}

// fillCatalogURL 与 fillCatalogPlaceholders 相同，但当占位符出现在主机位置时，
// 用户粘贴的 https://gitlab.example.com/ 会先去掉协议和结尾斜杠再拼接。
func fillCatalogURL(text string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		token := "{" + key + "}"
		if !strings.Contains(text, token) {
			continue
		}
		val := values[key]
		if strings.Contains(text, "://"+token) {
			val = trimCatalogHost(val)
		}
		text = strings.ReplaceAll(text, token, val)
	}
	return text
}

// trimCatalogHost 去掉地址里的协议前缀与结尾斜杠。
func trimCatalogHost(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, scheme) {
			value = value[len(scheme):]
			break
		}
	}
	return strings.TrimRight(value, "/")
}

// catalogTemplateFields 提取模板里出现的占位符名。
func catalogTemplateFields(text string) []string {
	var keys []string
	rest := text
	for {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			return keys
		}
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			return keys
		}
		end += start
		keys = append(keys, rest[start+1:end])
		rest = rest[end+1:]
	}
}

// catalogTemplateHasEmptyField 判断模板里是否有占位符取值为空（含未声明的占位符）。
func catalogTemplateHasEmptyField(text string, values map[string]string) bool {
	for _, key := range catalogTemplateFields(text) {
		if values[key] == "" {
			return true
		}
	}
	return false
}

// catalogHomeDir 取当前用户主目录，用作路径类字段的默认值。
func catalogHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// MCPCatalog 返回设置页展示的全部预设条目，顺序与界面上的推荐顺序一致。
func MCPCatalog() []MCPCatalogEntry {
	return []MCPCatalogEntry{
		{
			ID:          "playwright",
			Name:        "Playwright",
			Vendor:      "Microsoft",
			Category:    MCPCatalogCategoryBrowser,
			Summary:     "驱动真实浏览器：打开页面、点击、填表、截图",
			SampleTools: []string{"browser_navigate", "browser_click", "browser_snapshot", "browser_take_screenshot"},
			DocsURL:     "https://github.com/microsoft/playwright-mcp",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@playwright/mcp@latest"},
		},
		{
			ID:          "chrome-devtools",
			Name:        "Chrome DevTools",
			Vendor:      "Google",
			Category:    MCPCatalogCategoryBrowser,
			Summary:     "检查页面结构、控制台消息与性能记录",
			SampleTools: []string{"navigate_page", "take_screenshot", "list_console_messages", "performance_start_trace"},
			DocsURL:     "https://github.com/ChromeDevTools/chrome-devtools-mcp",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "chrome-devtools-mcp@latest"},
		},
		{
			ID:          "fetch",
			Name:        "Fetch",
			Vendor:      "Anthropic",
			Category:    MCPCatalogCategoryBrowser,
			Summary:     "把网页下载成纯文本供阅读",
			SampleTools: []string{"fetch"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch",
			Transport:   "stdio",
			Command:     "uvx",
			Args:        []string{"mcp-server-fetch"},
		},
		{
			ID:          "memory",
			Name:        "Memory",
			Vendor:      "Anthropic",
			Category:    MCPCatalogCategoryKnowledge,
			Summary:     "记住人物、事实以及它们之间的关系",
			SampleTools: []string{"create_entities", "create_relations", "search_nodes", "read_graph"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/memory",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@modelcontextprotocol/server-memory"},
		},
		{
			ID:          "sequential-thinking",
			Name:        "Sequential Thinking",
			Vendor:      "Anthropic",
			Category:    MCPCatalogCategoryKnowledge,
			Summary:     "把复杂问题一步步推演清楚",
			SampleTools: []string{"sequentialthinking"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/sequentialthinking",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@modelcontextprotocol/server-sequential-thinking"},
		},
		{
			ID:          "git",
			Name:        "Git",
			Vendor:      "Anthropic",
			Category:    MCPCatalogCategoryDeveloper,
			Summary:     "查看仓库状态、差异、历史与提交",
			SampleTools: []string{"git_status", "git_diff", "git_log", "git_commit"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/git",
			Transport:   "stdio",
			Command:     "uvx",
			Args:        []string{"mcp-server-git"},
		},
		{
			ID:          "aws-docs",
			Name:        "AWS Docs",
			Vendor:      "Amazon Web Services",
			Category:    MCPCatalogCategoryCloud,
			Summary:     "检索并阅读 AWS 官方文档",
			SampleTools: []string{"read_documentation", "search_documentation", "recommend"},
			DocsURL:     "https://awslabs.github.io/mcp/servers/aws-documentation-mcp-server",
			Transport:   "stdio",
			Command:     "uvx",
			Args:        []string{"awslabs.aws-documentation-mcp-server@latest"},
			Env:         map[string]string{"FASTMCP_LOG_LEVEL": "ERROR"},
		},
		{
			ID:          "deepwiki",
			Name:        "DeepWiki",
			Vendor:      "Cognition",
			Category:    MCPCatalogCategoryKnowledge,
			Summary:     "阅读仓库文档并回答相关问题",
			SampleTools: []string{"read_wiki_structure", "read_wiki_contents", "ask_question"},
			DocsURL:     "https://docs.devin.ai/work-with-devin/deepwiki-mcp",
			Transport:   "http",
			URL:         "https://mcp.deepwiki.com/mcp",
		},
		{
			ID:          "brave-search",
			Name:        "Brave Search",
			Vendor:      "Brave",
			Category:    MCPCatalogCategorySearch,
			Summary:     "搜索网页、本地地点与新闻",
			SampleTools: []string{"brave_web_search", "brave_local_search", "brave_news_search"},
			DocsURL:     "https://github.com/brave/brave-search-mcp-server",
			KeysURL:     "https://api-dashboard.search.brave.com/app/keys",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@brave/brave-search-mcp-server"},
			Env:         map[string]string{"BRAVE_API_KEY": "{key}"},
			Fields: []MCPCatalogField{
				{Key: "key", Label: "API 密钥", Kind: MCPCatalogFieldSecret, Placeholder: "BSA…", Required: true, Help: "在页面上创建密钥后复制过来"},
			},
		},
		{
			ID:          "firecrawl",
			Name:        "Firecrawl",
			Vendor:      "Firecrawl",
			Category:    MCPCatalogCategoryBrowser,
			Summary:     "抓取、搜索并遍历整站内容",
			SampleTools: []string{"firecrawl_scrape", "firecrawl_search", "firecrawl_crawl", "firecrawl_map"},
			DocsURL:     "https://github.com/firecrawl/firecrawl-mcp-server",
			KeysURL:     "https://www.firecrawl.dev/app/api-keys",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "firecrawl-mcp"},
			Env:         map[string]string{"FIRECRAWL_API_KEY": "{key}"},
			Fields: []MCPCatalogField{
				{Key: "key", Label: "API 密钥", Kind: MCPCatalogFieldSecret, Placeholder: "fc-…", Required: true, Help: "在页面上创建密钥后复制过来"},
			},
		},
		{
			ID:          "perplexity",
			Name:        "Perplexity",
			Vendor:      "Perplexity",
			Category:    MCPCatalogCategorySearch,
			Summary:     "联网提问并给出带来源的回答",
			SampleTools: []string{"perplexity_search", "perplexity_ask", "perplexity_research", "perplexity_reason"},
			DocsURL:     "https://github.com/perplexityai/modelcontextprotocol",
			KeysURL:     "https://www.perplexity.ai/settings/api",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@perplexity-ai/mcp-server"},
			Env:         map[string]string{"PERPLEXITY_API_KEY": "{key}"},
			Fields: []MCPCatalogField{
				{Key: "key", Label: "API 密钥", Kind: MCPCatalogFieldSecret, Placeholder: "pplx-…", Required: true, Help: "在页面上创建密钥后复制过来"},
			},
		},
		{
			ID:          "mongodb",
			Name:        "MongoDB",
			Vendor:      "MongoDB",
			Category:    MCPCatalogCategoryData,
			Summary:     "在集合中查找并修改文档",
			SampleTools: []string{"find", "aggregate", "list-collections", "insert-many"},
			DocsURL:     "https://github.com/mongodb-js/mongodb-mcp-server",
			KeysURL:     "https://cloud.mongodb.com",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "mongodb-mcp-server"},
			Env:         map[string]string{"MDB_MCP_CONNECTION_STRING": "{url}"},
			Fields: []MCPCatalogField{
				{Key: "url", Label: "连接字符串", Kind: MCPCatalogFieldSecret, Placeholder: "mongodb+srv://…", Required: true, Help: "在 Atlas 里点 Connect → Drivers 后复制连接串"},
			},
		},
		{
			ID:          "slack",
			Name:        "Slack",
			Vendor:      "Slack",
			Category:    MCPCatalogCategoryWork,
			Summary:     "列出频道，读取并发送消息",
			SampleTools: []string{"channels_list", "conversations_history", "conversations_search_messages", "conversations_add_message"},
			DocsURL:     "https://github.com/korotovsky/slack-mcp-server",
			KeysURL:     "https://api.slack.com/apps",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "slack-mcp-server@latest", "--transport", "stdio"},
			Env: map[string]string{
				"SLACK_MCP_XOXB_TOKEN":       "{token}",
				"SLACK_MCP_ADD_MESSAGE_TOOL": "true",
			},
			Fields: []MCPCatalogField{
				{Key: "token", Label: "Bot Token", Kind: MCPCatalogFieldSecret, Placeholder: "xoxb-…", Required: true, Help: "从应用页复制 Bot User OAuth Token，并把机器人拉进要读取的频道"},
			},
		},
		{
			ID:          "filesystem",
			Name:        "Filesystem",
			Vendor:      "Anthropic",
			Category:    MCPCatalogCategoryDeveloper,
			Summary:     "读写并搜索一个目录里的文件",
			SampleTools: []string{"read_text_file", "write_file", "list_directory", "search_files"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
			Transport:   "stdio",
			Command:     "npx",
			Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "{root}"},
			Fields: []MCPCatalogField{
				{Key: "root", Label: "目录", Kind: MCPCatalogFieldPath, Default: catalogHomeDir(), Placeholder: "/Users/you/Projects", Required: true, Help: "该服务可以使用的目录"},
			},
		},
		{
			ID:          "sqlite",
			Name:        "SQLite",
			Vendor:      "SQLite",
			Category:    MCPCatalogCategoryData,
			Summary:     "读写本地数据库文件",
			SampleTools: []string{"read_query", "write_query", "list_tables", "describe-table"},
			DocsURL:     "https://github.com/modelcontextprotocol/servers-archived/tree/main/src/sqlite",
			Transport:   "stdio",
			Command:     "uvx",
			Args:        []string{"mcp-server-sqlite", "--db-path", "{path}"},
			Fields: []MCPCatalogField{
				{Key: "path", Label: "数据库文件", Kind: MCPCatalogFieldPath, Placeholder: "/path/to/app.db", Required: true, Help: "文件不存在时会自动创建"},
			},
		},
		{
			ID:          "github",
			Name:        "GitHub",
			Vendor:      "GitHub",
			Category:    MCPCatalogCategoryDeveloper,
			Summary:     "跨仓库处理 Issue、Pull Request 与代码",
			SampleTools: []string{"list_issues", "create_pull_request", "search_code", "get_file_contents"},
			DocsURL:     "https://github.com/github/github-mcp-server",
			KeysURL:     "https://github.com/settings/personal-access-tokens/new",
			Transport:   "http",
			URL:         "https://api.githubcopilot.com/mcp/",
			Headers:     map[string]string{"Authorization": "Bearer {token}"},
			Fields: []MCPCatalogField{
				{Key: "token", Label: "访问令牌", Kind: MCPCatalogFieldSecret, Placeholder: "ghp_…", Required: true, Help: "在页面上创建细粒度令牌后复制过来"},
			},
		},
		{
			ID:          "context7",
			Name:        "Context7",
			Vendor:      "Upstash",
			Category:    MCPCatalogCategoryKnowledge,
			Summary:     "为你用到的库提供最新文档与示例",
			SampleTools: []string{"resolve-library-id", "query-docs"},
			DocsURL:     "https://github.com/upstash/context7",
			KeysURL:     "https://context7.com/dashboard",
			Transport:   "http",
			URL:         "https://mcp.context7.com/mcp",
			Headers:     map[string]string{"Authorization": "Bearer {key}"},
			Fields: []MCPCatalogField{
				{Key: "key", Label: "API 密钥", Kind: MCPCatalogFieldSecret, Placeholder: "ctx7sk-…", Required: false, Help: "可选，填了能提高调用限额"},
			},
		},
		{
			ID:          "huggingface",
			Name:        "Hugging Face",
			Vendor:      "Hugging Face",
			Category:    MCPCatalogCategoryKnowledge,
			Summary:     "检索模型、数据集与论文",
			SampleTools: []string{"hub_repo_search", "hub_repo_details", "hf_fs", "hf_whoami"},
			DocsURL:     "https://github.com/huggingface/hf-mcp-server",
			KeysURL:     "https://huggingface.co/settings/tokens",
			Transport:   "http",
			URL:         "https://huggingface.co/mcp",
			Headers:     map[string]string{"Authorization": "Bearer {token}"},
			Fields: []MCPCatalogField{
				{Key: "token", Label: "访问令牌", Kind: MCPCatalogFieldSecret, Placeholder: "hf_…", Required: false, Help: "可选，填了能提高限额"},
			},
		},
		{
			ID:          "postgres",
			Name:        "PostgreSQL",
			Vendor:      "PostgreSQL",
			Category:    MCPCatalogCategoryData,
			Summary:     "执行 SQL 并查看数据库结构",
			SampleTools: []string{"execute_sql", "search_objects"},
			DocsURL:     "https://github.com/bytebase/dbhub",
			Transport:   "stdio",
			Command:     "npx",
			// 连接串走环境变量，避免出现在进程参数与配置明文里。
			Args: []string{"-y", "@bytebase/dbhub@latest", "--transport", "stdio"},
			Env:  map[string]string{"DSN": "{url}"},
			Fields: []MCPCatalogField{
				{Key: "url", Label: "连接字符串", Kind: MCPCatalogFieldSecret, Placeholder: "postgresql://user:password@host:5432/db", Required: true, Help: "连接串，建议使用只读账号"},
			},
		},
		{
			ID:          "gitlab",
			Name:        "GitLab",
			Vendor:      "GitLab",
			Category:    MCPCatalogCategoryDeveloper,
			Summary:     "跨项目处理 Issue、合并请求、流水线与代码",
			SampleTools: []string{"list_merge_requests", "get_merge_request_diffs", "create_issue", "get_pipeline_jobs"},
			DocsURL:     "https://docs.gitlab.com/user/model_context_protocol/mcp_server/",
			Transport:   "oauth",
			URL:         "https://{instance}/api/v4/mcp",
			Auth:        &MCPCatalogAuth{Mode: MCPAuthModeAuto},
			Fields: []MCPCatalogField{
				{Key: "instance", Label: "GitLab 地址", Kind: MCPCatalogFieldText, Default: "gitlab.com", Placeholder: "gitlab.com", Required: false, Help: "gitlab.com，或自建实例的主机名"},
			},
		},
		{
			ID:          "tavily",
			Name:        "Tavily",
			Vendor:      "Tavily",
			Category:    MCPCatalogCategorySearch,
			Summary:     "联网搜索并提取页面正文",
			SampleTools: []string{"tavily-search", "tavily-extract", "tavily-crawl"},
			DocsURL:     "https://github.com/tavily-ai/tavily-mcp",
			Transport:   "oauth",
			URL:         "https://mcp.tavily.com/mcp",
			Auth:        &MCPCatalogAuth{Mode: MCPAuthModeAuto},
		},
		{
			ID:          "exa",
			Name:        "Exa",
			Vendor:      "Exa",
			Category:    MCPCatalogCategorySearch,
			Summary:     "联网搜索、阅读页面，并给出偏代码的回答",
			SampleTools: []string{"web_search_exa", "web_fetch_exa", "agent_run"},
			DocsURL:     "https://github.com/exa-labs/exa-mcp-server",
			Transport:   "oauth",
			URL:         "https://mcp.exa.ai/mcp",
			Auth:        &MCPCatalogAuth{Mode: MCPAuthModeAuto},
		},
	}
}
