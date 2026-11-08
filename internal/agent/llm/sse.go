package llm

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// sseEvent 表示单条 SSE 事件数据。
type sseEvent struct {
	Event string
	Data  string
	ID    string
}

// sseReader 基于 bufio.Reader 实现标准 Server-Sent Events 流式解析器。
// 支持 data: 多行拼接、event:、id:、注释行 (:)、空行分隔、[DONE] 结束标记、CRLF 与超长行。
type sseReader struct {
	reader *bufio.Reader
}

// newSSEReader 创建并返回一个 sseReader 实例。
func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{
		reader: bufio.NewReader(r),
	}
}

// readEvent 读取并返回下一条完整的 SSE 事件。
// 遇到 EOF 且无残留数据时返回 io.EOF。
func (r *sseReader) readEvent() (*sseEvent, error) {
	var cur sseEvent
	hasData := false

	for {
		lineBytes, err := r.reader.ReadBytes('\n')
		if err != nil && len(lineBytes) == 0 {
			if hasData {
				return &cur, nil
			}
			return nil, err
		}

		// 去除行末的 CRLF 或 LF
		line := string(bytes.TrimRight(lineBytes, "\r\n"))

		// 空行：触发当前事件派发
		if line == "" {
			if hasData || cur.Event != "" || cur.ID != "" {
				return &cur, nil
			}
			if err != nil {
				return nil, err
			}
			continue
		}

		// 注释行：以 ':' 开头，忽略
		if strings.HasPrefix(line, ":") {
			if err != nil {
				if hasData {
					return &cur, nil
				}
				return nil, err
			}
			continue
		}

		// 解析 field 与 value（字段以首个冒号分隔，若冒号后紧跟单个空格则去除）
		var field, value string
		if colonIdx := strings.IndexByte(line, ':'); colonIdx >= 0 {
			field = line[:colonIdx]
			value = line[colonIdx+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		} else {
			field = line
			value = ""
		}

		switch field {
		case "data":
			if hasData {
				cur.Data += "\n" + value
			} else {
				cur.Data = value
				hasData = true
			}
		case "event":
			cur.Event = value
		case "id":
			cur.ID = value
		}

		if err != nil {
			if hasData || cur.Event != "" || cur.ID != "" {
				return &cur, nil
			}
			return nil, err
		}
	}
}
