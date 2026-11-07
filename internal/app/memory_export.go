package app

// 本文件实现记忆导出（Markdown）的渲染：导出逻辑在 client.go 的 ExportMemories 里读远端数据，
// 渲染写成纯函数，便于单测。

import (
	"fmt"
	"strings"
	"time"

	"mcode/internal/nmem"
)

// memoryExportTitle 是导出 Markdown 的一级标题。
const memoryExportTitle = "记忆导出"

// renderMemoryExport 把记忆列表渲染成可读的 Markdown：一级标题、一行说明（空间名、导出时间、条数），
// 然后每条记忆一个二级标题，下一行写 ID、类型、重要度与更新时间（字段有才写），最后是正文。
func renderMemoryExport(space string, exportedAt time.Time, memories []nmem.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", memoryExportTitle)
	fmt.Fprintf(&b, "空间：%s · 导出时间：%s · 共 %d 条\n",
		exportSpaceLabel(space), exportedAt.Format("2006-01-02 15:04"), len(memories))

	if len(memories) == 0 {
		b.WriteString("\n（没有可导出的记忆）\n")
		return b.String()
	}

	for _, m := range memories {
		title := strings.TrimSpace(m.Title)
		if title == "" {
			title = "未命名记忆"
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)

		meta := make([]string, 0, 4)
		if id := strings.TrimSpace(m.ID); id != "" {
			meta = append(meta, "ID："+id)
		}
		if unitType := strings.TrimSpace(m.UnitType); unitType != "" {
			meta = append(meta, "类型："+unitType)
		}
		if m.Importance > 0 {
			meta = append(meta, fmt.Sprintf("重要度：%.1f", m.Importance))
		}
		// 更新时间缺失时回落到创建时间，两者都没有就整项省略。
		updated := m.UpdatedAt
		if updated.IsZero() {
			updated = m.CreatedAt
		}
		if !updated.IsZero() {
			meta = append(meta, "更新时间："+updated.UTC().Format("2006-01-02 15:04"))
		}
		if len(meta) > 0 {
			fmt.Fprintf(&b, "%s\n\n", strings.Join(meta, " · "))
		}

		content := strings.TrimSpace(m.Content)
		if content == "" {
			content = "（无正文）"
		}
		b.WriteString(content)
		b.WriteString("\n")
	}
	return b.String()
}

// exportSpaceLabel 在空间为空（由服务端使用默认空间）时给出可读标注。
func exportSpaceLabel(space string) string {
	if s := strings.TrimSpace(space); s != "" {
		return s
	}
	return "（默认空间）"
}
