package mcodeui

import (
	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// composerSendKey 读取发送消息的按键配置（enter | shift_enter）。
// 优先读内存中的设置面板配置；getChatSettings(s) 为 nil 时只读后台异步加载的持久化配置缓存，
// 缓存未就绪时按 Enter 发送，不在渲染线程同步读盘。
func (s *chatState) composerSendKey() string {
	if s == nil {
		return config.ComposerSendKeyEnter
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return normalizeComposerSendKey(st.cfg.Session.ComposerSendKey)
	}

	if cfg, ok := cachedFallbackConfig(s); ok {
		return normalizeComposerSendKey(cfg.Session.ComposerSendKey)
	}
	return config.ComposerSendKeyEnter
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
