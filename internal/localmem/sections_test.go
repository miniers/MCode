package localmem

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"mcode/internal/nmem"
)

func TestSections(t *testing.T) {
	text := "# 文档\n说明\n\n## 一\n旧内容\n```go\n## 代码\n```\n\n## 二\n末尾\n"
	sections := ListSections(text)
	if len(sections) != 2 || sections[0].Heading != "一" || !strings.Contains(sections[0].Body, "## 代码") || sections[1].Heading != "二" {
		t.Fatalf("sections: %#v", sections)
	}
	changed := UpsertSection(text, "一", "新内容")
	if !strings.Contains(changed, "# 文档\n说明") || !strings.Contains(changed, "新内容") || strings.Contains(changed, "旧内容") || !strings.Contains(changed, "## 二") {
		t.Fatalf("replace: %q", changed)
	}
	changed = UpsertSection(changed, "三", "追加")
	if !strings.Contains(changed, "## 三\n\n追加") {
		t.Fatalf("append: %q", changed)
	}
	changed = RemoveSection(changed, "二")
	if strings.Contains(changed, "## 二") || !strings.Contains(changed, "## 三") {
		t.Fatalf("remove: %q", changed)
	}
}

func TestListSectionsSkipsEmptyHeading(t *testing.T) {
	text := "# 文档\n## 一\n内容\n## \n仍在正文\n## 二\n尾\n"
	sections := ListSections(text)
	if len(sections) != 2 || sections[0].Heading != "一" || sections[1].Heading != "二" {
		t.Fatalf("空标题不应成为小节: %#v", sections)
	}
	if !strings.Contains(sections[0].Body, "仍在正文") {
		t.Fatalf("空标题行应并入上一节正文: %#v", sections[0])
	}
}

func TestUpsertSectionNormalizesBody(t *testing.T) {
	changed := UpsertSection("# 文档\n\n## 一\n旧\n", "一", "第一段\n## 看起来像标题\n第二段")
	sections := ListSections(changed)
	if len(sections) != 1 || sections[0].Heading != "一" {
		t.Fatalf("正文里的 ## 不应成为新小节: %#v", sections)
	}
	if !strings.Contains(sections[0].Body, "### 看起来像标题") || strings.Contains(sections[0].Body, "\n## ") {
		t.Fatalf("正文里的二级标题应降级: %q", sections[0].Body)
	}
	appended := UpsertSection("", "二", "开场\n## 仍是正文")
	sections = ListSections(appended)
	if len(sections) != 1 || !strings.Contains(sections[0].Body, "### 仍是正文") {
		t.Fatalf("追加分支也应规整正文: %#v", appended)
	}
}

func TestUpsertSectionKeepsFenceHeadings(t *testing.T) {
	changed := UpsertSection("# 文档\n", "一", "示例：\n```go\n## 代码\n```\n")
	sections := ListSections(changed)
	if len(sections) != 1 || !strings.Contains(sections[0].Body, "## 代码") {
		t.Fatalf("围栏里的 ## 应原样保留: %#v", sections)
	}
}

func TestUpsertSectionClosesOpenFence(t *testing.T) {
	changed := UpsertSection("", "一", "示例：\n```go\ncode")
	sections := ListSections(changed)
	if len(sections) != 1 || !strings.Contains(sections[0].Body, "code") {
		t.Fatalf("未闭合围栏应自动补上: %#v", sections)
	}
	changed = UpsertSection(changed, "二", "追加")
	sections = ListSections(changed)
	if len(sections) != 2 || sections[1].Heading != "二" {
		t.Fatalf("补上闭合围栏后追加的小节应可识别: %#v", sections)
	}
}

func TestUpsertSectionClosesExistingFence(t *testing.T) {
	text := "# 文档\n\n## 一\n```\n未闭合\n"
	changed := UpsertSection(text, "二", "追加")
	sections := ListSections(changed)
	if len(sections) != 2 || sections[1].Heading != "二" || !strings.Contains(sections[1].Body, "追加") {
		t.Fatalf("文档围栏未闭合时追加小节应可识别: %#v", sections)
	}
}

func TestUpsertSectionKeepsLaterSections(t *testing.T) {
	text := UpsertSection(UpsertSection("", "一", "示例：\n```go\ncode"), "二", "后一条")
	if len(ListSections(text)) != 2 {
		t.Fatalf("两条记忆都应可读回: %#v", ListSections(text))
	}
	changed := UpsertSection(text, "一", "改写")
	sections := ListSections(changed)
	if len(sections) != 2 || sections[0].Body != "改写" || sections[1].Heading != "二" {
		t.Fatalf("改写前一条不应丢后一条: %#v", sections)
	}
}

func TestReplaceSection(t *testing.T) {
	text := "# 文档\n\n## 一\n旧\n\n## 二\n后\n"
	changed := ReplaceSection(text, "一", "新", "正文")
	if !strings.Contains(changed, "## 新\n\n正文") || !strings.Contains(changed, "## 二\n后") || strings.Contains(changed, "## 一") {
		t.Fatalf("replace: %q", changed)
	}
	if got := ReplaceSection(text, "缺失", "新", "正文"); got != text {
		t.Fatalf("缺失小节应原样返回: %q", got)
	}
}

func TestErrNotFoundIsAPIError(t *testing.T) {
	var apiErr *nmem.APIError
	if !errors.As(ErrNotFound, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("ErrNotFound 应可解析为 404 的 APIError: %v", ErrNotFound)
	}
	if apiErr.Body != "localmem: memory not found" {
		t.Fatalf("Body = %q", apiErr.Body)
	}
}
