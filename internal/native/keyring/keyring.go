package keyring

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	// ErrNotFound 表示请求的凭据在凭据库中不存在。
	ErrNotFound = errors.New("keyring: item not found")
	// ErrUnsupported 表示当前平台不支持原生系统凭据库后端。
	ErrUnsupported = errors.New("keyring: unsupported platform backend")
)

// Backend 定义系统底层凭据存储接口。
type Backend interface {
	// Get 获取指定 service 与 account 的凭据。若不存在返回 ErrNotFound。
	Get(service, account string) (string, error)
	// Set 存储指定 service 与 account 的凭据。
	Set(service, account, value string) error
	// Delete 删除指定 service 与 account 的凭据。若条目不存在亦返回 nil。
	Delete(service, account string) error
}

const b64Prefix = "mcode-b64:"

// EncodeValue 将明文凭据统一编码为带有 mcode-b64: 前缀的 base64 字符串。
func EncodeValue(value string) string {
	return b64Prefix + base64.StdEncoding.EncodeToString([]byte(value))
}

// DecodeValue 对读取的值进行解码；若含 mcode-b64: 前缀则 base64 解码，否则原样返回。
func DecodeValue(raw string) (string, error) {
	if strings.HasPrefix(raw, b64Prefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, b64Prefix))
		if err != nil {
			return "", fmt.Errorf("decode base64 secret: %w", err)
		}
		return string(decoded), nil
	}
	return raw, nil
}

// QuoteArg 将参数使用双引号包裹，并转义内部的反斜杠与双引号。
func QuoteArg(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// memoryBackend 内存凭据后端实现，并发安全，主要用于测试。
type memoryBackend struct {
	mu   sync.RWMutex
	data map[string]string // key: service + "\x00" + account
}

func itemKey(service, account string) string {
	return service + "\x00" + account
}

// NewMemoryBackend 创建一个并发安全的内存凭据存储后端。
func NewMemoryBackend() Backend {
	return &memoryBackend{
		data: make(map[string]string),
	}
}

func (m *memoryBackend) Get(service, account string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	val, ok := m.data[itemKey(service, account)]
	if !ok {
		return "", ErrNotFound
	}
	return val, nil
}

func (m *memoryBackend) Set(service, account, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[itemKey(service, account)] = value
	return nil
}

func (m *memoryBackend) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, itemKey(service, account))
	return nil
}

// NewDefaultBackend 返回当前平台的默认凭据存储后端。
// darwin 下返回 macOS security CLI 后端，其他平台返回报 ErrUnsupported 的后端。
func NewDefaultBackend() Backend {
	return newPlatformDefaultBackend()
}
