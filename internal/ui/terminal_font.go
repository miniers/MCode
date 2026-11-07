package mcodeui

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/resources"
)

const (
	// terminalBaseFontFamily 是终端的基础等宽字体：mygo 把 "monospace" 解析成系统等宽字体，
	// 终端单元格宽度也按它的 ASCII 步进计算。没装等宽 Nerd Font 时它就是主字体。
	terminalBaseFontFamily = "monospace"
	// terminalFallbackFontFamily 是随应用打包的 Nerd Font 图标字体的家族名，
	// 与 resources/fonts/SymbolsNerdFontMono-Regular.ttf 内部家族名一致。
	terminalFallbackFontFamily = "Symbols Nerd Font Mono"
	// terminalMaxSystemFallbacks 限制等宽、非等宽两组各自从系统里挑出的家族数量。
	terminalMaxSystemFallbacks = 3
)

var (
	// terminalFontOnce 保证字体探测整个进程只做一次。
	terminalFontOnce sync.Once
	// terminalFontFamilyList 缓存算好的终端字体族列表。
	terminalFontFamilyList string
	// terminalFallbackOnce 保证内置图标字体只注册一次。
	terminalFallbackOnce sync.Once
	// terminalFallbackRegistered 缓存内置字体是否注册成功。
	terminalFallbackRegistered bool
)

// terminalFontFamily 返回终端 Options.Font.Family 用的字体族列表，以逗号分隔：
// 已安装的等宽 Nerd Font 在前作主字体，其次系统等宽字体，其余家族在 mygo 里作为
// 缺失字形的回退（macOS 上 CoreText 的 cascade list，见 mygo 的
// internal/text/coretext_darwin.go 中 ctFont 的 cascadeList 处理）。
// 结果只计算一次并缓存。
func terminalFontFamily() string {
	terminalFontOnce.Do(func() {
		terminalFontFamilyList = buildTerminalFontFamily()
	})
	return terminalFontFamilyList
}

// buildTerminalFontFamily 组装终端字体族列表：等宽 Nerd Font（若已安装，作主字体）、
// 系统等宽字体、其余 Nerd Font、内置图标字体。
//
// 之所以把已安装的等宽 Nerd Font 排在主字体位置：这些补丁字体的 ASCII 和私有区图标
// 在同一张字体里，终端不必依赖引擎「主字体缺字形时再用后面的家族」这一回退路径。
// 现在 macOS 上 Starship 的私有区图标（U+E0A0、U+F07B 等）就卡在这条回退路径上，
// 显示为带问号的方框，而 ASCII 和 ❯ 正常。
// 只列等宽家族：非等宽 Nerd Font 的 ASCII 步进不齐，会把终端网格撑歪。
func buildTerminalFontFamily() string {
	mono, other := systemNerdFontFamilies()
	families := make([]string, 0, len(mono)+len(other)+2)
	if len(mono) > 0 {
		families = append(families, mono[0])
		mono = mono[1:]
	}
	families = append(families, terminalBaseFontFamily)
	families = append(families, mono...)
	families = append(families, other...)
	if registerTerminalFallbackFont() {
		families = append(families, terminalFallbackFontFamily)
	}
	return strings.Join(families, ", ")
}

// systemNerdFontFamilies 从系统已安装的字体里挑出 Nerd Font 家族名，
// 分成等宽与非等宽两组，各自按 terminalMaxSystemFallbacks 截断。
// 家族名取自 mygo 的字体列表（macOS 上由 CoreText 枚举全部已安装字体，
// 已覆盖 ~/Library/Fonts、/Library/Fonts 与 /System/Library/Fonts），
// 因为回退只认家族名，直接读字体文件名拿不到 CoreText 需要的名字。
func systemNerdFontFamilies() (mono, other []string) {
	for _, f := range ui.FontFamilies() {
		if f.Registered || !isNerdFontFamily(f.Name) {
			continue
		}
		if f.Monospaced {
			mono = append(mono, f.Name)
			continue
		}
		other = append(other, f.Name)
	}
	mono = mono[:min(len(mono), terminalMaxSystemFallbacks)]
	other = other[:min(len(other), terminalMaxSystemFallbacks)]
	return mono, other
}

// isNerdFontFamily 判断家族名是否来自 Nerd Font 补丁字体，
// 覆盖 "MesloLGS NF"、"MesloLGL Nerd Font Mono"、"JetBrainsMono Nerd Font" 等命名。
func isNerdFontFamily(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "nerd font") || strings.Contains(lower, "nerdfont") {
		return true
	}
	return strings.HasSuffix(strings.TrimSpace(name), " NF")
}

// registerTerminalFallbackFont 把内置的 Nerd Font 图标字体注册给 mygo，
// 使系统没装 Nerd Font 时也能显示主题图标；注册失败或资源缺失时返回 false。
// 注册过的家族名可以直接写进字体族列表，mygo 会优先用注册的字体来匹配。
func registerTerminalFallbackFont() bool {
	terminalFallbackOnce.Do(func() {
		if len(resources.SymbolsNerdFontMonoTTF) == 0 {
			return
		}
		if err := ui.RegisterFont(resources.SymbolsNerdFontMonoTTF, terminalFallbackFontFamily); err != nil {
			return
		}
		terminalFallbackRegistered = true
	})
	return terminalFallbackRegistered
}
