package resources

import _ "embed"

// SymbolsNerdFontMonoTTF 是 Nerd Fonts 的纯图标字体（NerdFontsSymbolsOnly 发行包中的
// SymbolsNerdFontMono-Regular.ttf，MIT 许可，见 resources/fonts/LICENSE）。
// 它只含 Powerline、Font Awesome、Devicons 等私有区图标，不含 ASCII，
// 因此只作为终端字体的逐字形回退，不负责正文显示。
//
//go:embed fonts/SymbolsNerdFontMono-Regular.ttf
var SymbolsNerdFontMonoTTF []byte
