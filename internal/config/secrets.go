package config

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"mcode/internal/native/keyring"

	"github.com/egoist/mygo"
)

// ErrSecretNotFound 重新导出 mygo 的哨兵错误，使不允许直接导入 mygo 的包也能判断“未找到”。
var ErrSecretNotFound = mygo.ErrSecretNotFound

// SecretStore 包装凭据存储，不接触明文文件。
type SecretStore interface {
	// Get 获取 account 对应的密钥字符串。若未找到返回 mygo.ErrSecretNotFound。
	Get(account string) (string, error)
	// Set 存储 account 对应的密钥字符串。
	Set(account, value string) error
	// Delete 删除 account 对应的密钥。若 key 不存在亦返回 nil。
	Delete(account string) error
}

// legacySecretStore 基于 mygo.Secrets 的底层系统凭据库实现，仅用于迁移和回退。
type legacySecretStore struct {
	mod mygo.SecretsModule
}

func newLegacySecretStore(service string) SecretStore {
	// MyGo 与 security 在 macOS 上使用相同的 service/account，旧值可直接读取。
	// 不能将它们当作两个存储迁移，否则删除“旧值”也会删除当前凭据。
	if runtime.GOOS == "darwin" {
		return nil
	}
	return &legacySecretStore{
		mod: mygo.Secrets.Service(service),
	}
}

func (s *legacySecretStore) Get(account string) (string, error) {
	val, err := s.mod.Get(account)
	if err != nil {
		return "", err
	}
	return string(val), nil
}

func (s *legacySecretStore) Set(account, value string) error {
	return s.mod.Set(account, []byte(value))
}

func (s *legacySecretStore) Delete(account string) error {
	return s.mod.Delete(account)
}

// keyringSecretStore 优先使用 keyring.Backend，未命中或不受支持时平滑回退/迁移旧数据。
type keyringSecretStore struct {
	service string
	primary keyring.Backend
	legacy  SecretStore
}

// newKeyringSecretStore 创建基于 primary keyring 与 legacy fallback 的 SecretStore。
func newKeyringSecretStore(service string, primary keyring.Backend, legacy SecretStore) SecretStore {
	return &keyringSecretStore{
		service: service,
		primary: primary,
		legacy:  legacy,
	}
}

// NewSecretStore 返回指定 service 名称的 SecretStore（基于纯 Go keyring，保留旧 mygo 凭据回退）。
func NewSecretStore(service string) SecretStore {
	return newKeyringSecretStore(service, keyring.NewDefaultBackend(), newLegacySecretStore(service))
}

func (s *keyringSecretStore) Get(account string) (string, error) {
	val, err := s.primary.Get(s.service, account)
	if err == nil {
		return val, nil
	}
	if s.legacy == nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", err
	}

	if errors.Is(err, keyring.ErrNotFound) {
		// primary 未找到，回退读取 legacy
		legVal, legErr := s.legacy.Get(account)
		if legErr != nil {
			if errors.Is(legErr, ErrSecretNotFound) {
				return "", ErrSecretNotFound
			}
			return "", legErr
		}

		// legacy 命中，尝试写入 primary 并验证一致性，成功后删除旧数据（尽力而为）
		if setErr := s.primary.Set(s.service, account, legVal); setErr == nil {
			if checkVal, getErr := s.primary.Get(s.service, account); getErr == nil && checkVal == legVal {
				_ = s.legacy.Delete(account)
			}
		}
		return legVal, nil
	}

	// primary 返回其他错误（含 ErrUnsupported）时直接回退到 legacy
	legVal, legErr := s.legacy.Get(account)
	if legErr != nil {
		if errors.Is(legErr, ErrSecretNotFound) {
			if errors.Is(err, keyring.ErrUnsupported) {
				return "", ErrSecretNotFound
			}
			return "", err
		}
		return "", legErr
	}
	return legVal, nil
}

func (s *keyringSecretStore) Set(account, value string) error {
	// 只有平台不支持时才回退；写入失败后回退会让旧 primary 遮蔽刚保存的新值。
	err := s.primary.Set(s.service, account, value)
	if errors.Is(err, keyring.ErrUnsupported) && s.legacy != nil {
		return s.legacy.Set(account, value)
	}
	if err != nil {
		return err
	}
	if s.legacy != nil {
		return s.legacy.Delete(account)
	}
	return nil
}

func (s *keyringSecretStore) Delete(account string) error {
	var primaryErr, legacyErr error
	if err := s.primary.Delete(s.service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) && !errors.Is(err, keyring.ErrUnsupported) {
		primaryErr = err
	}
	if s.legacy != nil {
		if err := s.legacy.Delete(account); err != nil && !errors.Is(err, ErrSecretNotFound) {
			legacyErr = err
		}
	}

	if primaryErr != nil && legacyErr != nil {
		return errors.Join(primaryErr, legacyErr)
	}
	if primaryErr != nil {
		return primaryErr
	}
	if legacyErr != nil {
		return legacyErr
	}
	return nil
}

// memorySecretStore 基于内存的凭据存储实现，供测试与界面开发使用。
type memorySecretStore struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewMemorySecretStore 创建并返回一个内存实现的 SecretStore。
func NewMemorySecretStore() SecretStore {
	return &memorySecretStore{
		data: make(map[string]string),
	}
}

func (m *memorySecretStore) Get(account string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	val, ok := m.data[account]
	if !ok {
		return "", ErrSecretNotFound
	}
	return val, nil
}

func (m *memorySecretStore) Set(account, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[account] = value
	return nil
}

func (m *memorySecretStore) Delete(account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, account)
	return nil
}

// SecretRef 标识一条具体服务与账户的凭据引用。
type SecretRef struct {
	Service string
	Account string
}

// LegacySecretRefs 返回旧凭据的引用列表，包括 Cloudflare API Token、OpenAI API Key，
// 以及 MCP OAuth 相关的凭据。
//
// 关于 MCP OAuth 账户名格式（服务名 mcode.mcp-oauth）：
// 在 internal/mcp/oauth.go 中，每个 MCP 服务器（标识为 serverID）的凭据账户名定义如下：
// 1. 令牌（access token 与 refresh token）账户名格式为：mcp-oauth:<serverID>:token
// 2. 动态客户端注册信息账户名格式为：mcp-oauth:<serverID>:client
func LegacySecretRefs(mcpOAuthAccounts []string) []SecretRef {
	refs := []SecretRef{
		{Service: "mcode", Account: "cloudflare_api_token"},
		{Service: "mcode", Account: "openai.apiKey"},
	}
	for _, acc := range mcpOAuthAccounts {
		refs = append(refs, SecretRef{
			Service: "mcode.mcp-oauth",
			Account: acc,
		})
	}
	return refs
}

// MigrateLegacySecrets 将旧 mygo 存储中的凭据一次性迁移到当前默认的 keyring 后端。
func MigrateLegacySecrets(refs []SecretRef) []error {
	return migrateLegacySecrets(refs, keyring.NewDefaultBackend(), func(service string) SecretStore {
		return newLegacySecretStore(service)
	})
}

// migrateLegacySecrets 是可注入版本的迁移逻辑，方便在测试中注入 mock keyring 与 legacy store。
// 逐条迁移：legacy 没有该条就跳过；primary 已有且一致则删除旧条目（幂等）；
// 否则写入 primary，读回一致后删除 legacy；任何一步失败都保留 legacy 并记录错误，然后继续处理下一条。
func migrateLegacySecrets(refs []SecretRef, primary keyring.Backend, legacyFor func(service string) SecretStore) []error {
	var errs []error
	for _, ref := range refs {
		legacy := legacyFor(ref.Service)
		if legacy == nil {
			continue
		}

		val, err := legacy.Get(ref.Account)
		if err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				// legacy 没有该条，跳过
				continue
			}
			errs = append(errs, fmt.Errorf("read legacy secret (%s/%s): %w", ref.Service, ref.Account, err))
			continue
		}

		// legacy 存在，检查 primary 是否已有
		current, err := primary.Get(ref.Service, ref.Account)
		if err == nil {
			if current != val {
				errs = append(errs, fmt.Errorf("verify primary secret (%s/%s): existing value mismatch", ref.Service, ref.Account))
				continue
			}
			if err := legacy.Delete(ref.Account); err != nil && !errors.Is(err, ErrSecretNotFound) {
				errs = append(errs, fmt.Errorf("delete legacy secret (%s/%s): %w", ref.Service, ref.Account, err))
			}
			continue
		}

		if !errors.Is(err, keyring.ErrNotFound) {
			// primary 检查时返回非 NotFound 错误
			errs = append(errs, fmt.Errorf("check primary secret (%s/%s): %w", ref.Service, ref.Account, err))
			continue
		}

		// 写入 primary
		if err := primary.Set(ref.Service, ref.Account, val); err != nil {
			errs = append(errs, fmt.Errorf("write primary secret (%s/%s): %w", ref.Service, ref.Account, err))
			continue
		}

		// 读回一致性校验
		readback, err := primary.Get(ref.Service, ref.Account)
		if err != nil {
			errs = append(errs, fmt.Errorf("readback primary secret (%s/%s): %w", ref.Service, ref.Account, err))
			continue
		}
		if readback != val {
			errs = append(errs, fmt.Errorf("verify primary secret (%s/%s): readback value mismatch", ref.Service, ref.Account))
			continue
		}

		// 读回一致后删除 legacy
		if err := legacy.Delete(ref.Account); err != nil && !errors.Is(err, ErrSecretNotFound) {
			errs = append(errs, fmt.Errorf("delete legacy secret (%s/%s): %w", ref.Service, ref.Account, err))
		}
	}
	return errs
}
