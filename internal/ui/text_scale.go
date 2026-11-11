package mcodeui

import "mcode/internal/config"

type themeFonts struct {
	sidebar, conversation, inter, code, terminal, editor int
}

func resolveThemeFonts(cfg config.AppearanceConfig, id string) themeFonts {
	fonts := cfg.ThemeFonts(id)
	for key, value := range fonts {
		if value <= 0 {
			_, _, fonts[key], _ = config.ThemeFontRange(key)
		} else {
			fonts[key], _ = config.NormalizeThemeFont(key, value)
		}
	}
	return themeFonts{fonts["sidebar"], fonts["conversation"], fonts["interface"], fonts["code"], fonts["terminal"], fonts["editor"]}
}

func currentThemeFonts() themeFonts {
	theme := activeTheme.Load()
	if theme.fixedFonts != nil {
		return *theme.fixedFonts
	}
	if themeDark.Load() {
		return theme.darkFonts
	}
	return theme.lightFonts
}

func sidebarScaleFactor() float32      { return float32(currentThemeFonts().sidebar) / 100 }
func chatScaleFactor() float32         { return float32(currentThemeFonts().conversation) / 100 }
func sidebarFont(size float32) float32 { return size * sidebarScaleFactor() }
func chatFont(size float32) float32    { return size * chatScaleFactor() }
func uiFont(size float32) float32      { return size * float32(currentThemeFonts().inter) / 100 }
func scaledFont(size float32) float32  { return chatFont(size) }
func textScaleFactor() float32         { return chatScaleFactor() }
func codeFontSize() float32            { return float32(currentThemeFonts().code) }
func terminalFontSize() int            { return currentThemeFonts().terminal }
func editorFontSize() float32          { return float32(currentThemeFonts().editor) }
