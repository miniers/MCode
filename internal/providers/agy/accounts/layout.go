package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// macOnlyLinks 是只在 macOS 上存在的宿主配置项，Windows 上没有共享意义。
var macOnlyLinks = map[string]bool{
	".zshrc":  true,
	"Library": true,
}

// accountDir 返回该隔离账号的根目录 <dataDir>/agy-accounts/<id>。
func (m *Manager) accountDir(id string) string {
	return filepath.Join(m.opts.DataDir, "agy-accounts", id)
}

// accountHomeDir 返回隔离环境作为 HOME 的子目录 <dataDir>/agy-accounts/<id>/home。
func (m *Manager) accountHomeDir(id string) string {
	return filepath.Join(m.accountDir(id), "home")
}

// setupAccountLayout 搭建隔离账号的目录结构并生成软链接与私有文件。
// 严禁修改或创建 HostHome 目录中的任何文件；目标不存在时照常创建软链接；已存在软链接跳过。
func (m *Manager) setupAccountLayout(id string) error {
	home := m.accountHomeDir(id)
	geminiDir := filepath.Join(home, ".gemini")
	antigravityCLIDir := filepath.Join(geminiDir, "antigravity-cli")

	if err := os.MkdirAll(antigravityCLIDir, dirPerm0700); err != nil {
		return fmt.Errorf("create antigravity-cli dir: %w", err)
	}

	// 1. 初始化私有文件 installation_id（若未存在）
	instIDPath := filepath.Join(geminiDir, "installation_id")
	if _, err := os.Stat(instIDPath); os.IsNotExist(err) {
		instID := uuid.NewString()
		if err := os.WriteFile(instIDPath, []byte(instID), filePerm0600); err != nil {
			return fmt.Errorf("write installation_id: %w", err)
		}
	}

	// 2. 软链接配置与映射列表（相对 home 路径 -> 相对 HostHome 路径）
	symlinks := []string{
		filepath.Join(".gemini", "antigravity-cli", "settings.json"),
		filepath.Join(".gemini", "config"),
		filepath.Join(".gemini", "skills"),
		filepath.Join(".gemini", "hooks"),
		filepath.Join(".gemini", "conversations"),
		filepath.Join(".gemini", "brain"),
		filepath.Join(".gemini", "annotations"),
		".gitconfig",
		".ssh",
		".config",
		".npmrc",
		".zshrc",
		"Library",
	}

	for _, rel := range symlinks {
		// .zshrc、Library 是 macOS 专属路径，Windows 上跳过。
		if goos == "windows" && macOnlyLinks[rel] {
			continue
		}

		target := filepath.Join(m.opts.HostHome, rel)
		linkPath := filepath.Join(home, rel)

		// 检查链接本身是否存在（即使为断链）
		if _, err := os.Lstat(linkPath); err == nil {
			continue // 链接已存在，跳过
		}

		if err := os.MkdirAll(filepath.Dir(linkPath), dirPerm0700); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", linkPath, err)
		}

		if err := os.Symlink(target, linkPath); err != nil {
			// Windows 未开启开发者模式时创建软链接会失败。共享宿主配置只是便利，
			// 不阻断账号创建：普通文件降级为复制一份，目录（已在上面的 Symlink 尝试过）直接跳过。
			if copyErr := shareHostFile(target, linkPath); copyErr != nil {
				log.Printf("agy accounts: 无法共享宿主配置 %s（%v），已跳过", rel, err)
			}
		}
	}

	return nil
}

// shareHostFile 在软链接不可用时把宿主普通文件复制到隔离目录；目录无法在不建链接的前提下共享，返回错误。
func shareHostFile(target, linkPath string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s 是目录，无法在不创建链接的情况下共享", target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	return os.WriteFile(linkPath, data, filePerm0600)
}

// generateDeviceProfile 按照规划 §6.1 生成独立的设备指纹。
func generateDeviceProfile() DeviceProfile {
	// machineId: auth0|user_ 加 32 位 hex（取 sha256 随机值的 16 字节即 32 个 hex 字符）
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	h := sha256.Sum256(buf)
	machineHex := hex.EncodeToString(h[:16])
	machineID := "auth0|user_" + machineHex

	macMachineID := uuid.NewString()
	devDeviceID := uuid.NewString()
	sqmID := fmt.Sprintf("{%s}", strings.ToUpper(uuid.NewString()))

	return DeviceProfile{
		MachineId:        machineID,
		MacMachineId:     macMachineID,
		DevDeviceId:      devDeviceID,
		SqmId:            sqmID,
		ServiceMachineId: devDeviceID,
	}
}
