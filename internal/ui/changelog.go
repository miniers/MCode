package mcodeui

import (
	"strings"

	mcode "mcode"
)

func changelogBody() string { return parseChangelogBody(mcode.Changelog) }

func parseChangelogBody(text string) string {
	text = strings.TrimPrefix(text, "# 更新日志")
	return strings.TrimSpace(text)
}

func changelogSection(version string) string {
	return parseChangelogSection(mcode.Changelog, version)
}

func parseChangelogSection(text, version string) string {
	version = strings.TrimPrefix(version, "v")
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if start < 0 {
			if line == "## "+version {
				start = i + 1
			}
		} else if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}
