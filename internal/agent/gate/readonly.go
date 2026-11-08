package gate

import "strings"

// IsReadOnlyCommand 判断等待检查是否只读取数据。
func IsReadOnlyCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	// 禁止 shell 展开或转义改变检查过的命令与参数。
	if strings.ContainsAny(cmd, "$\\'\"*?[") {
		return false
	}
	return cmd == "true" || cmd == "false" || cmd != "" && onlyReads(cmd)
}
