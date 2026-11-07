// 本文件移植自 magpie 的 internal/edit/jsonc_valid.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。nextToken 与 isSpace 取自 magpie 的
// internal/edit/jsonarray.go。

package magpiecfg

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errEditInvalid 表示编辑被拒绝，因为它留下的文件会被读取它的程序判为无效。
var errEditInvalid = errors.New("the edit would leave invalid JSON/JSONC; not written")

// ValidJSONC 报告 raw 是否是各程序读取的那种带注释和尾随逗号的 JSONC
// （OpenCode、VS Code 和 Zed 用 jsonc-parser 的 allowTrailingComma 解析）：
// 逗号可以结束一个成员或元素列表，但前面什么都没有的逗号（"{ , }"、"[ ,]"、
// ",,"）在它们那里是错误。本文档经由 tidwall/jsonc 读取，而它会把每个紧跟
// 右括号的逗号抹掉，所以经它处理后通过 json.Valid 的文件仍可能是程序判为
// 无效的文件；每次编辑在写入前都先用这里检查。
func ValidJSONC(raw []byte) bool {
	b, ok := blankComments(bytes.TrimPrefix(raw, bom))
	if !ok {
		return false
	}
	for _, i := range trailingCommas(b) {
		if lone(b, i) {
			return false
		}
		b[i] = ' '
	}
	return json.Valid(b)
}

// blankComments 是 raw 的副本，每个注释都换成空格、换行保留，所以其中的
// 偏移就是 raw 中的偏移；块注释没有闭合时返回 false。
func blankComments(raw []byte) ([]byte, bool) {
	b := append([]byte(nil), raw...)
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				return nil, false
			}
			for j := i; j < i+2+end+2; j++ {
				if b[j] != '\n' && b[j] != '\r' {
					b[j] = ' '
				}
			}
			i += 2 + end + 1
		}
	}
	return b, true
}

// trailingCommas 是无注释的 b 中、字符串外、后面紧跟右括号的逗号偏移。
func trailingCommas(b []byte) []int {
	var at []int
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == ',':
			j := i + 1
			for j < len(b) && isSpace(b[j]) {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				at = append(at, i)
			}
		}
	}
	return at
}

// lone 表示无注释的 b 中 i 处的逗号前面没有值：只有左括号、另一个逗号或
// 冒号。
func lone(b []byte, i int) bool {
	k := i - 1
	for k >= 0 && isSpace(b[k]) {
		k--
	}
	return k < 0 || b[k] == ',' || b[k] == '{' || b[k] == '[' || b[k] == ':'
}

// dropLoneCommas 返回去掉"前面没有值、只跟在右括号前"的逗号后的 raw：这是
// magpie 删除 JSONC 里唯一成员且它带尾随逗号时自己留下的东西（"mcp": {,}），
// 程序会判它无效。其它内容原样保留。
func dropLoneCommas(raw []byte) []byte {
	b, ok := blankComments(raw)
	if !ok {
		return raw
	}
	at := trailingCommas(b)
	var out []byte
	for n := len(at) - 1; n >= 0; n-- {
		if i := at[n]; lone(b, i) {
			if out == nil {
				out = append([]byte(nil), raw...)
			}
			out = append(out[:i], out[i+1:]...)
		}
	}
	if out == nil {
		return raw
	}
	return out
}

// checkJSONC 返回一次编辑的结果 out，或当它会把原本有效的文件写坏时报错。
func checkJSONC(out, orig []byte) ([]byte, error) {
	if !ValidJSONC(out) && ValidJSONC(orig) {
		return nil, errEditInvalid
	}
	return out, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// nextToken 是 raw 中 i 之后第一个既不是空白也不在注释里的字节偏移：位于
// 两个 token 之间，也就是被 jsonc 抹掉的尾随逗号所在的位置。
func nextToken(raw []byte, i int) int {
	for i < len(raw) {
		switch {
		case isSpace(raw[i]):
			i++
		case bytes.HasPrefix(raw[i:], []byte("//")):
			if n := bytes.IndexByte(raw[i:], '\n'); n >= 0 {
				i += n + 1
			} else {
				return len(raw)
			}
		case bytes.HasPrefix(raw[i:], []byte("/*")):
			if n := bytes.Index(raw[i+2:], []byte("*/")); n >= 0 {
				i += n + 4
			} else {
				return len(raw)
			}
		default:
			return i
		}
	}
	return i
}
