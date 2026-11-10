package reviewcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/fsutil"
)

const (
	// projectConfigDirName 是项目级检查配置在 dataDir 下的子目录。
	projectConfigDirName = "review-checks"
	// minProjectTimeoutSeconds 是项目级超时的下限。
	minProjectTimeoutSeconds = 30
	// maxProjectTimeoutSeconds 是项目级超时的上限。
	maxProjectTimeoutSeconds = 600
)

// ProjectConfigPath 返回项目级检查配置路径：
// <dataDir>/review-checks/<sha256(绝对项目路径)[:8] 的 hex>.json。
// dataDir 为空时回退到 <home>/.mcode，哈希算法与 internal/agentconf 的项目技能文件一致。
func ProjectConfigPath(dataDir, projectDir string) string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return ""
	}
	dataDir = strings.TrimSpace(dataDir)
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		dataDir = filepath.Join(home, ".mcode")
	}
	sum := sha256.Sum256([]byte(projectDir))
	name := hex.EncodeToString(sum[:8]) + ".json"
	return filepath.Join(dataDir, projectConfigDirName, name)
}

// LoadProjectConfig 读取项目级检查配置；文件不存在时返回零值和 nil。
// 读到的超时统一归一到 30..600 秒，0 按 180 处理。
func LoadProjectConfig(dataDir, projectDir string) (ProjectConfig, error) {
	configPath := ProjectConfigPath(dataDir, projectDir)
	if configPath == "" {
		return ProjectConfig{}, nil
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return ProjectConfig{}, nil
	}
	if err != nil {
		return ProjectConfig{}, fmt.Errorf("reviewcheck: 读取项目检查配置 %s: %w", configPath, err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ProjectConfig{}, fmt.Errorf("reviewcheck: 解析项目检查配置 %s: %w", configPath, err)
	}
	return normalizeProjectConfig(cfg), nil
}

// SaveProjectConfig 以 0600 权限原子写入项目级检查配置：先写同目录临时文件，
// 再 rename 覆盖，避免读到半截 JSON。TimeoutSeconds 会限制在 30..600，0 按 180。
func SaveProjectConfig(dataDir, projectDir string, cfg ProjectConfig) error {
	configPath := ProjectConfigPath(dataDir, projectDir)
	if configPath == "" {
		return errors.New("reviewcheck: 保存项目检查配置需要非空项目目录")
	}
	cfg = normalizeProjectConfig(cfg)
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("reviewcheck: 编码项目检查配置: %w", err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("reviewcheck: 创建配置目录 %s: %w", dir, err)
	}
	if err := fsutil.WriteFileAtomic(configPath, raw, 0o600); err != nil {
		return fmt.Errorf("reviewcheck: 替换项目检查配置 %s: %w", configPath, err)
	}
	return nil
}

// normalizeProjectConfig 去空白并归一秒数：0 → 180，其余限制在 30..600。
func normalizeProjectConfig(cfg ProjectConfig) ProjectConfig {
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = int(DefaultTimeout / time.Second)
	}
	if cfg.TimeoutSeconds < minProjectTimeoutSeconds {
		cfg.TimeoutSeconds = minProjectTimeoutSeconds
	}
	if cfg.TimeoutSeconds > maxProjectTimeoutSeconds {
		cfg.TimeoutSeconds = maxProjectTimeoutSeconds
	}
	return cfg
}
