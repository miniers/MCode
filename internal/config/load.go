package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"mcode/internal/fsutil"
)

const configFileName = "config.json"

var configWriteMu sync.Mutex

// Update 在同一写锁下读取并更新配置，避免不同设置入口覆盖彼此的修改。
func Update(dataDir string, update func(*Config) error) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	cfg, err := Load(dataDir)
	if err != nil {
		return err
	}
	if err := update(&cfg); err != nil {
		return err
	}
	return save(dataDir, cfg)
}

// ConfigPath 返回指定数据目录下的配置文件绝对/相对路径。
func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, configFileName)
}

// Load 读取 <dataDir>/config.json。
// 若文件不存在，返回 Defaults()，不报错且不创建文件。
// 文件中缺失的字段将保留默认值；非法字段被重置为默认值并记入日志（见 LoadWithWarnings）。
func Load(dataDir string) (Config, error) {
	cfg, warnings, err := LoadWithWarnings(dataDir)
	for _, w := range warnings {
		log.Printf("config: %s", w)
	}
	return cfg, err
}

// LoadWithWarnings 与 Load 相同，额外返回被重置为默认值的非法字段说明。
// 逐字段重置而不是整体报错，否则磁盘上一个非法值会让 Load 永远失败，
// 设置页的每次保存都卡在这一步，用户既存不下去也改不回来。
func LoadWithWarnings(dataDir string) (Config, []string, error) {
	path := ConfigPath(dataDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil, nil
		}
		return Config{}, nil, fmt.Errorf("read config file: %w", err)
	}

	migratedRaw, err := migrateRaw(dataDir, raw)
	if err != nil {
		return Config{}, nil, err
	}

	cfg := Defaults()
	// 避免 JSON 复用默认切片中的档位，导致缺失的提示词继承默认值。
	cfg.Orchestration.Tiers = nil
	if err := json.Unmarshal(migratedRaw, &cfg); err != nil {
		return Config{}, nil, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg = Normalize(cfg)

	warnings := repairInvalidFields(&cfg)

	if err := Validate(cfg); err != nil {
		return Config{}, warnings, err
	}

	return cfg, warnings, nil
}

// repairInvalidFields 把 Validate 会拒绝、且 Normalize 不会修正的字段重置为默认值，返回逐条说明。
// Normalize 已负责夹值与枚举回落，这里只处理剩余能按字段定位的非法值。
func repairInvalidFields(cfg *Config) []string {
	var warnings []string
	def := Defaults()

	// roles 的 provider 回退依赖 models.provider，先修它。
	if !IsKnownProvider(*cfg, cfg.Models.Provider) {
		warnings = append(warnings, fmt.Sprintf("models.provider %q 非法，已重置为 %q", cfg.Models.Provider, def.Models.Provider))
		cfg.Models.Provider = def.Models.Provider
	}

	// 权限模式非法时回退到 supervised 而不是默认的 fullAccess：磁盘上的笔误（如 "readOnly"）
	// 不应被静默提升为完全访问权限。
	switch cfg.Permissions.Mode {
	case ModeReadOnly, ModeSupervised, ModeAuto, ModeFullAccess:
	default:
		warnings = append(warnings, fmt.Sprintf("permissions.mode %q 非法，已重置为 %q", cfg.Permissions.Mode, ModeSupervised))
		cfg.Permissions.Mode = ModeSupervised
	}

	switch cfg.Appearance.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		warnings = append(warnings, fmt.Sprintf("appearance.theme %q 非法，已重置为 %q", cfg.Appearance.Theme, def.Appearance.Theme))
		cfg.Appearance.Theme = def.Appearance.Theme
	}

	if cfg.Orchestration.PanelWidth < 0 || cfg.Orchestration.PanelHeight < 0 {
		warnings = append(warnings, "orchestration.panel_width/panel_height 为负值，已重置为 0（自动）")
		cfg.Orchestration.PanelWidth = 0
		cfg.Orchestration.PanelHeight = 0
	}

	if !validTierSet(cfg.Orchestration.Tiers) {
		warnings = append(warnings, "orchestration.tiers 含非法或重复的档位 ID，或全部只读，已重置为默认档位")
		cfg.Orchestration.Tiers = def.Orchestration.Tiers
		cfg.Orchestration.DefaultTier = def.Orchestration.DefaultTier
	}

	for _, role := range ModelRoles {
		r := cfg.Models.Role(role)
		if r != nil && r.Model != "" && r.Provider == "" {
			warnings = append(warnings, fmt.Sprintf("models.roles.%s.provider 缺失，已回退为 %q", role, cfg.Models.Provider))
			r.Provider = cfg.Models.Provider
		}
	}

	return warnings
}

// validTierSet 判断档位列表满足 Validate 的约束：ID 合法且不重复，非空时至少有一个可写档位。
func validTierSet(tiers []SwarmTier) bool {
	seen := make(map[string]bool, len(tiers))
	hasWritable := false
	for _, tier := range tiers {
		if !tierIDRegex.MatchString(tier.ID) || seen[tier.ID] {
			return false
		}
		seen[tier.ID] = true
		if !tier.ReadOnly {
			hasWritable = true
		}
	}
	return len(tiers) == 0 || hasWritable
}

// Save 原子写入 <dataDir>/config.json，权限为 0600。
// 若目录不存在，将以 0700 权限递归创建。
// 写入失败时原文件内容不受破坏。
func Save(dataDir string, cfg Config) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	return save(dataDir, cfg)
}

func save(dataDir string, cfg Config) error {
	cfg = Normalize(cfg)
	if err := Validate(cfg); err != nil {
		return fmt.Errorf("validate config before saving: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := fsutil.WriteFileAtomic(ConfigPath(dataDir), data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	// 降级快照：配置升到 v8 后旧版本 mcode 读不懂新结构，另存一份 v7 镜像供回退时读取。
	// 写失败只记日志，不影响保存结果——快照是便利，不是保存成功的前提。
	writeV7Snapshot(dataDir, cfg)
	return nil
}

// v7SnapshotFileName 是降级快照的文件名，与 config.json 同目录。
const v7SnapshotFileName = "config.v7.json"

// writeV7Snapshot 按 v7 结构把当前配置另存为 <dataDir>/config.v7.json。
// TODO(v9): 确认不再需要跨大版本回退时，连同 migrate.exportV7 一起移除。
func writeV7Snapshot(dataDir string, cfg Config) {
	raw, err := exportV7(cfg)
	if err != nil {
		log.Printf("config: 生成 v7 降级快照失败: %v", err)
		return
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		log.Printf("config: 生成 v7 降级快照失败: %v", err)
		return
	}
	data = append(data, '\n')

	if err := fsutil.WriteFileAtomic(filepath.Join(dataDir, v7SnapshotFileName), data, 0o600); err != nil {
		log.Printf("config: 写入 v7 降级快照失败: %v", err)
	}
}
