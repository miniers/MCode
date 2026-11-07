package mcodeui

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// composerSendKeyCache 缓存从持久化配置读到的发送键。发送键是全局唯一设置，
// 不必每个会话各存一份，这里用包级变量加互斥锁缓存 2 秒，不往 chatState 上加字段。
var (
	composerSendKeyMu       sync.Mutex
	composerSendKeyCache    string
	composerSendKeyCachedAt time.Time
)

// composerSendKey 读取发送消息的按键配置（enter | shift_enter）。
// 与 queueWhileRunningEnabled 相同的读取策略：优先读内存中的设置面板配置；
// getChatSettings(s) 为 nil 时回退读持久化配置 config.Load(dataDir)，缓存 2 秒；读不到时按 Enter 发送。
func (s *chatState) composerSendKey() string {
	if s == nil {
		return config.ComposerSendKeyEnter
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return normalizeComposerSendKey(st.cfg.Session.ComposerSendKey)
	}

	composerSendKeyMu.Lock()
	if !composerSendKeyCachedAt.IsZero() && time.Since(composerSendKeyCachedAt) < 2*time.Second {
		cached := composerSendKeyCache
		composerSendKeyMu.Unlock()
		return cached
	}
	composerSendKeyMu.Unlock()

	key := config.ComposerSendKeyEnter
	if dataDir := resolveDataDirFallback(); dataDir != "" {
		if cfg, err := config.Load(dataDir); err == nil {
			key = normalizeComposerSendKey(cfg.Session.ComposerSendKey)
		}
	}

	composerSendKeyMu.Lock()
	composerSendKeyCache = key
	composerSendKeyCachedAt = time.Now()
	composerSendKeyMu.Unlock()
	return key
}

// normalizeComposerSendKey 把空值与非法值归为 Enter 发送，只有 shift_enter 原样返回。
func normalizeComposerSendKey(key string) string {
	if key == config.ComposerSendKeyShiftEnter {
		return config.ComposerSendKeyShiftEnter
	}
	return config.ComposerSendKeyEnter
}

// composerSendMods 返回发送消息时要匹配的修饰键：Shift+Enter 发送时为 ui.Shift，Enter 发送时为 0。
func composerSendMods(sendKey string) ui.Modifiers {
	if sendKey == config.ComposerSendKeyShiftEnter {
		return ui.Shift
	}
	return 0
}
