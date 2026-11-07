package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxDiffFileSize = 512 * 1024 // 512KB

// UnifiedDiff 计算单文件的统一差异 (Unified Diff)，输出带 diff --git、--- a/<path> 和 +++ b/<path> 头的标准 unified diff。
func UnifiedDiff(path, oldText, newText string) string {
	if oldText == newText {
		return ""
	}

	cleanPath := filepath.Clean(path)
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	if cleanPath == "" {
		cleanPath = path
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", cleanPath, cleanPath))

	if oldText == "" {
		sb.WriteString("new file mode 100644\n")
		sb.WriteString("--- /dev/null\n")
		sb.WriteString(fmt.Sprintf("+++ b/%s\n", cleanPath))
		newLines := strings.Split(newText, "\n")
		sb.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(newLines)))
		for _, l := range newLines {
			sb.WriteString("+" + l + "\n")
		}
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("--- a/%s\n", cleanPath))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", cleanPath))

	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	sb.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines)))
	for _, l := range oldLines {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range newLines {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

// PreviewEditDiff 预览单文件单处或全部替换的 diff。
// 超过 512KB 或读取失败时返回空串；oldStr 找不到时返回空串。
func PreviewEditDiff(path, oldStr, newStr string, replaceAll bool) string {
	if oldStr == "" {
		return ""
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Size() > maxDiffFileSize {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) > maxDiffFileSize {
		return ""
	}
	oldContent := string(data)
	if !strings.Contains(oldContent, oldStr) {
		return ""
	}
	n := 1
	if replaceAll {
		n = -1
	}
	newContent := strings.Replace(oldContent, oldStr, newStr, n)
	return UnifiedDiff(path, oldContent, newContent)
}

// PreviewWriteDiff 预览写入新文件或覆盖文件的 diff。
// 文件不存在时按旧内容为空处理；超过 512KB 或读取失败时返回空串。
func PreviewWriteDiff(path, content string) string {
	var oldContent string
	stat, err := os.Stat(path)
	if err == nil {
		if stat.Size() > maxDiffFileSize {
			return ""
		}
		data, rErr := os.ReadFile(path)
		if rErr != nil {
			return ""
		}
		oldContent = string(data)
	} else if !os.IsNotExist(err) {
		return ""
	}
	return UnifiedDiff(path, oldContent, content)
}

// EditOperation 表示 MultiEdit 中的单次文本替换操作。
type EditOperation struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

// PreviewMultiEditDiff 预览对同一文件依次执行多次替换累计后的 diff。
func PreviewMultiEditDiff(path string, edits []EditOperation) string {
	if len(edits) == 0 {
		return ""
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Size() > maxDiffFileSize {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) > maxDiffFileSize {
		return ""
	}
	oldContent := string(data)
	curContent := oldContent
	for _, e := range edits {
		if e.OldString == "" || !strings.Contains(curContent, e.OldString) {
			return ""
		}
		n := 1
		if e.ReplaceAll {
			n = -1
		}
		curContent = strings.Replace(curContent, e.OldString, e.NewString, n)
	}
	return UnifiedDiff(path, oldContent, curContent)
}
