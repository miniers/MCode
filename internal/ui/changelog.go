package mcodeui

import (
	"strings"

	mcode "mcode"
)

type changelogVersion struct {
	Version string
	Body    string
}

func changelogVersions() []changelogVersion { return parseChangelogVersions(mcode.Changelog) }

func parseChangelogVersions(text string) []changelogVersion {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimPrefix(text, "# 更新日志")
	lines := strings.Split(text, "\n")
	var versions []changelogVersion
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		if start >= 0 {
			versions[len(versions)-1].Body = strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
		versions = append(versions, changelogVersion{Version: strings.TrimPrefix(line, "## ")})
		start = i + 1
	}
	if start >= 0 {
		versions[len(versions)-1].Body = strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return versions
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
