package magpiecfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/providers/loginenv"
)

// 本文件生成 Cline 的全局配置计划。Cline 3.x 只读同目录的两份 JSON：
// settings/providers.json（provider 设置与 lastUsedProvider）与
// settings/models.json（provider 元数据与模型列表）。provider 用独立的
// id，不占 Cline 内置的 openai-compatible 槽位，避免覆盖用户已有配置。

const (
	// ClineAgent 是 cline 在 mcode 配置里的 agent 名，必须与
	// config.ProviderCline 相同。
	ClineAgent = "cline"
	// ClineProvider 是写进 Cline 配置的 provider id。
	ClineProvider = "mcode-magpie"
	// clineProviderName 是 Cline 里显示的 provider 名称。
	clineProviderName = "Magpie"
)

// ClineModel 是一个要写进 models.json 的模型及其元数据。
type ClineModel struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	Vision        bool
	Reasoning     bool
}

// ClineOptions 是生成 Cline 计划所需的输入。Now 为零值时取当前时间，
// 测试可以注入固定时间让 updatedAt 可断言。
type ClineOptions struct {
	BaseURL string
	APIKey  string
	Main    string
	Models  []ClineModel
	Now     time.Time
}

// ClinePaths 返回要接管的 providers.json 与同目录 models.json 路径。
//
// 解析顺序与 internal/providers/acp/cline_settings.go 的 clineProvidersFile
// 逐字对齐，避免设置页写一份、适配器读另一份：
//   - $CLINE_DATA_DIR/settings/providers.json，未设置时用 ~/.cline/data/settings/providers.json；
//   - 上一步的文件不存在、而 ~/.cline/settings/providers.json 存在时用后者；
//   - 都不存在时仍返回 ~/.cline/data/settings/providers.json，由 Connect 创建。
//
// Cline 自己也支持 CLINE_DIR，但现有适配器不读它；这里同样不引入，否则两处
// 解析会分叉。将来要支持必须两处一起改。
func ClinePaths() (providersPath, modelsPath string, err error) {
	home := loginenv.HomeDirectory()
	dataDir := strings.TrimSpace(os.Getenv("CLINE_DATA_DIR"))
	if dataDir == "" {
		dataDir = filepath.Join(home, ".cline", "data")
	}
	providersPath = filepath.Join(dataDir, "settings", "providers.json")
	if _, statErr := os.Stat(providersPath); statErr != nil {
		legacy := filepath.Join(home, ".cline", "settings", "providers.json")
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			providersPath = legacy
		}
	}
	return providersPath, filepath.Join(filepath.Dir(providersPath), "models.json"), nil
}

// ClinePlan 生成 Cline 两份 JSON 的写入计划。BaseURL 只去掉结尾斜杠：
// Magpie 端点地址已经带 /v1，不能像 claude 那样再去掉 /v1。Main 为空或不在
// Models 里时取第一条模型。
func ClinePlan(opts ClineOptions) (Plan, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	apiKey := strings.TrimSpace(opts.APIKey)
	switch {
	case baseURL == "":
		return Plan{}, errors.New("magpiecfg: Cline 的 BaseURL 为空")
	case apiKey == "":
		return Plan{}, errors.New("magpiecfg: Cline 的 API Key 为空")
	case len(opts.Models) == 0:
		return Plan{}, errors.New("magpiecfg: Cline 没有可写的模型")
	}
	models := make([]ClineModel, 0, len(opts.Models))
	seen := make(map[string]bool, len(opts.Models))
	for _, m := range opts.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return Plan{}, errors.New("magpiecfg: Cline 的模型 ID 为空")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if m.Name == "" {
			m.Name = id
		}
		m.ID = id
		models = append(models, m)
	}
	main := strings.TrimSpace(opts.Main)
	if !seen[main] {
		main = models[0].ID
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	providersPath, modelsPath, err := ClinePaths()
	if err != nil {
		return Plan{}, err
	}

	modelRows := make(map[string]any, len(models))
	for _, m := range models {
		modelRows[m.ID] = map[string]any{
			"name":              m.Name,
			"contextWindow":     m.ContextWindow,
			"maxTokens":         m.MaxTokens,
			"supportsVision":    m.Vision,
			"supportsReasoning": m.Reasoning,
		}
	}
	return Plan{
		Agent: ClineAgent,
		Files: []FilePlan{
			{
				Path:   providersPath,
				Format: FormatJSON,
				Fields: []Field{
					{Path: "version", Value: 1},
					{Path: "lastUsedProvider", Value: ClineProvider},
					{Path: "providers." + ClineProvider, Value: map[string]any{
						"settings": map[string]any{
							"provider": ClineProvider,
							"apiKey":   apiKey,
							"model":    main,
							"baseUrl":  baseURL,
							"headers":  map[string]string{"User-Agent": "cline"},
						},
						"updatedAt":   now.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
						"tokenSource": "manual",
					}},
				},
			},
			{
				Path:   modelsPath,
				Format: FormatJSON,
				Fields: []Field{
					{Path: "version", Value: 1},
					{Path: "providers." + ClineProvider, Value: map[string]any{
						"provider": map[string]any{
							"name":           clineProviderName,
							"baseUrl":        baseURL,
							"defaultModelId": main,
							"client":         "openai-compatible",
						},
						"models": modelRows,
					}},
				},
			},
		},
	}, nil
}
