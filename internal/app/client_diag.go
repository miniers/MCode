package app

import "fmt"

// ExportDiagnosticsTo 把 app 层运行日志（先旧后新）脱敏合并后写入 dst。
// 日志文件尚未生成或已被清理时，只会导出存在的那一份。
func (c *Client) ExportDiagnosticsTo(dst string) error {
	if dst == "" {
		return fmt.Errorf("diagnostics destination is empty")
	}
	return ExportDiagnostics(dst, logFilePaths())
}
