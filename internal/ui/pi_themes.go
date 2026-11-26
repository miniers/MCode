package mcodeui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/config"
)

type piThemeFile struct {
	path string
	name string
}

var piThemeDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "themes")
}

// piBuiltinThemePaths 是 npm 包里的 pi 内置主题。测试可替换，避免扫到本机安装。
var piBuiltinThemePaths = func() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local/lib/node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/theme/light.json"),
		filepath.Join(home, ".local/lib/node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/theme/dark.json"),
	}
}

func listPiThemeFiles() []piThemeFile {
	paths := make([]string, 0)
	if dir := piThemeDir(); dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
	}
	for _, path := range piBuiltinThemePaths() {
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		}
	}
	out := make([]piThemeFile, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		if data, err := os.ReadFile(path); err == nil {
			var raw struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &raw) == nil && strings.TrimSpace(raw.Name) != "" {
				name = raw.Name
			}
		}
		out = append(out, piThemeFile{path: path, name: name})
	}
	return out
}

func importPiThemeFile(st *SettingsState, page *themesSheetState, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var id string
	mutateTheme(st, func(a *config.AppearanceConfig) {
		theme, err := a.ImportPiTheme(data)
		if err == nil {
			id = theme.ID
		}
	})
	if id == "" {
		return false
	}
	page.editingID = id
	return true
}
