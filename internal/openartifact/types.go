package openartifact

import "strings"

// ArtifactType 表示 open-artifact 已安装的交付物类型条目。
type ArtifactType struct {
	ID   string
	Name string
}

// ParseInstalledTypes 从 open-artifact 入口 skill 正文的 `## Installed types` 小节解析已安装类型列表。
// 解析范围从 `## Installed types` 标题行开始，到下一个 `## ` 标题为止；未找到该小节时返回空切片。
func ParseInstalledTypes(skill string) []ArtifactType {
	if strings.TrimSpace(skill) == "" {
		return nil
	}

	normalized := strings.ReplaceAll(skill, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	inSection := false
	var types []ArtifactType
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if !inSection {
			if line == "## Installed types" {
				inSection = true
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "- `") {
			continue
		}
		rest := line[len("- `"):]
		closeTick := strings.IndexByte(rest, '`')
		if closeTick <= 0 {
			continue
		}
		id := strings.TrimSpace(rest[:closeTick])
		afterTick := strings.TrimSpace(rest[closeTick+1:])
		if !strings.HasPrefix(afterTick, ":") {
			continue
		}
		desc := strings.TrimSpace(afterTick[1:])
		name := desc
		if dotIdx := strings.Index(desc, ". "); dotIdx >= 0 {
			name = strings.TrimSpace(desc[:dotIdx])
		}
		if id == "" {
			continue
		}
		types = append(types, ArtifactType{
			ID:   id,
			Name: name,
		})
	}
	return types
}
