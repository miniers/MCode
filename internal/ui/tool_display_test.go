package mcodeui

import "testing"

// TestToolDisplayFallbackFields 验证截断 JSON 的回退取值：键名大小写不敏感，返回规范键名。
func TestToolDisplayFallbackFields(t *testing.T) {
	pathFields := toolDisplayFieldsFromText(`edit_file {"Path":"/repo/a.go","OldStr`)
	if got, _ := pathFields["path"].(string); got != "/repo/a.go" {
		t.Errorf("path = %q, want %q", got, "/repo/a.go")
	}
	if _, ok := pathFields["Path"]; ok {
		t.Errorf("fields 应只保留规范小写键名，实际含 %q", "Path")
	}

	filePathFields := toolDisplayFieldsFromText(`read_file {"FilePath":"/repo/b.go`)
	if got, _ := filePathFields["filePath"].(string); got != "/repo/b.go" {
		t.Errorf("filePath = %q, want %q", got, "/repo/b.go")
	}

	cmdFields := toolDisplayFieldsFromText(`bash {"Command":"ls -la`)
	if got, _ := cmdFields["command"].(string); got != "ls -la" {
		t.Errorf("command = %q, want %q", got, "ls -la")
	}

	if fields := toolDisplayFieldsFromText(`bash 没有任何 JSON`); fields != nil {
		t.Errorf("无 JSON 时应返回 nil，实际 %v", fields)
	}
}
