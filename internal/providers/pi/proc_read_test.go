package pi

import (
	"bufio"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// 测试普通多行情况：所有正常行按顺序完整读出，未触发 tooLong。
func TestReadBoundedLine_NormalMultipleLines(t *testing.T) {
	input := "first line\nsecond line\nthird line\n"
	r := bufio.NewReaderSize(strings.NewReader(input), 8)
	limit := 32

	var got []string
	for {
		line, tooLong, err := readBoundedLine(r, limit)
		if len(line) > 0 {
			got = append(got, string(line))
		}
		if tooLong {
			t.Fatalf("普通行不应触发 tooLong: %q", string(line))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("读取发生非预期错误: %v", err)
			}
			break
		}
	}

	want := []string{"first line\n", "second line\n", "third line\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("读出行内容不匹配，got %q, want %q", got, want)
	}
}

// 测试超长行被丢弃，且后续的正常行照常读出。
func TestReadBoundedLine_OversizedLineDroppedAndSubsequentRead(t *testing.T) {
	input := "short prefix\nthis is a very long line that exceeds the small limit configured for testing\nnormal after\n"
	r := bufio.NewReaderSize(strings.NewReader(input), 8)
	limit := 16

	// 第一行：正常短行
	line1, tooLong1, err1 := readBoundedLine(r, limit)
	if err1 != nil || tooLong1 || string(line1) != "short prefix\n" {
		t.Fatalf("第一行读取失败: got %q, tooLong=%v, err=%v", string(line1), tooLong1, err1)
	}

	// 第二行：超长行，应返回 tooLong=true
	line2, tooLong2, err2 := readBoundedLine(r, limit)
	if err2 != nil {
		t.Fatalf("第二行读取出错: %v", err2)
	}
	if !tooLong2 {
		t.Fatalf("第二行应标记为 tooLong=true，但 got false (len=%d)", len(line2))
	}
	if len(line2) <= limit {
		t.Fatalf("第二行累积长度应超过 limit(%d)，got %d", limit, len(line2))
	}

	// 第三行：超长行之后的正常行照常读出
	line3, tooLong3, err3 := readBoundedLine(r, limit)
	if err3 != nil || tooLong3 || string(line3) != "normal after\n" {
		t.Fatalf("第三行读取失败: got %q, tooLong=%v, err=%v", string(line3), tooLong3, err3)
	}

	// 之后应到达 EOF
	line4, tooLong4, err4 := readBoundedLine(r, limit)
	if !errors.Is(err4, io.EOF) || tooLong4 || len(line4) != 0 {
		t.Fatalf("EOF 行不匹配: got %q, tooLong=%v, err=%v", string(line4), tooLong4, err4)
	}
}

// 测试最后一行没有换行的情况：原样返回并附带 io.EOF，保持 ReadBytes 语义。
func TestReadBoundedLine_NoNewlineAtEOF(t *testing.T) {
	t.Run("短行无换行", func(t *testing.T) {
		input := "line1\nlast line without newline"
		r := bufio.NewReaderSize(strings.NewReader(input), 8)
		limit := 32

		line1, tooLong1, err1 := readBoundedLine(r, limit)
		if err1 != nil || tooLong1 || string(line1) != "line1\n" {
			t.Fatalf("第一行读取失败: got %q, tooLong=%v, err=%v", string(line1), tooLong1, err1)
		}

		line2, tooLong2, err2 := readBoundedLine(r, limit)
		if !errors.Is(err2, io.EOF) {
			t.Fatalf("最后一行应返回 io.EOF，got: %v", err2)
		}
		if tooLong2 {
			t.Fatalf("最后一行不应为 tooLong: %q", string(line2))
		}
		if string(line2) != "last line without newline" {
			t.Fatalf("最后一行内容不匹配: got %q", string(line2))
		}
	})

	t.Run("超长行无换行", func(t *testing.T) {
		input := "short\nthis is an oversized line without trailing newline at eof"
		r := bufio.NewReaderSize(strings.NewReader(input), 8)
		limit := 12

		line1, tooLong1, err1 := readBoundedLine(r, limit)
		if err1 != nil || tooLong1 || string(line1) != "short\n" {
			t.Fatalf("第一行读取失败: got %q, tooLong=%v, err=%v", string(line1), tooLong1, err1)
		}

		line2, tooLong2, err2 := readBoundedLine(r, limit)
		if !errors.Is(err2, io.EOF) {
			t.Fatalf("超长无换行行应返回 io.EOF，got: %v", err2)
		}
		if !tooLong2 {
			t.Fatalf("超长无换行行应为 tooLong=true")
		}
		if len(line2) <= limit {
			t.Fatalf("累积长度应超过 limit(%d)，got %d", limit, len(line2))
		}
	})
}

// 测试单行刚好等于 limit 的情况：不应被标记为超长。
func TestReadBoundedLine_ExactLimit(t *testing.T) {
	t.Run("含换行且长度等于 limit", func(t *testing.T) {
		// "012345678\n" 包含换行共 10 字节
		input := "012345678\n"
		limit := 10
		r := bufio.NewReaderSize(strings.NewReader(input), 4)

		line, tooLong, err := readBoundedLine(r, limit)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if tooLong {
			t.Fatalf("长度刚好等于 limit 时不应标记为 tooLong: len=%d, limit=%d", len(line), limit)
		}
		if string(line) != input {
			t.Fatalf("内容不匹配: got %q, want %q", string(line), input)
		}

		// 下一次读取应返回 EOF
		emptyLine, emptyTooLong, emptyErr := readBoundedLine(r, limit)
		if !errors.Is(emptyErr, io.EOF) || emptyTooLong || len(emptyLine) != 0 {
			t.Fatalf("EOF 读取不匹配: got %q, tooLong=%v, err=%v", string(emptyLine), emptyTooLong, emptyErr)
		}
	})

	t.Run("无换行且长度等于 limit", func(t *testing.T) {
		// "0123456789" 共 10 字节，未以换行结尾直接 EOF
		input := "0123456789"
		limit := 10
		r := bufio.NewReaderSize(strings.NewReader(input), 4)

		line, tooLong, err := readBoundedLine(r, limit)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("应返回 io.EOF，got: %v", err)
		}
		if tooLong {
			t.Fatalf("长度刚好等于 limit 时不应标记为 tooLong: len=%d, limit=%d", len(line), limit)
		}
		if string(line) != input {
			t.Fatalf("内容不匹配: got %q, want %q", string(line), input)
		}
	})

	t.Run("超过 limit 1 字节应判定为 tooLong", func(t *testing.T) {
		// "0123456789\n" 共 11 字节，limit 为 10
		input := "0123456789\n"
		limit := 10
		r := bufio.NewReaderSize(strings.NewReader(input), 4)

		line, tooLong, err := readBoundedLine(r, limit)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if !tooLong {
			t.Fatalf("长度 11 超过 limit 10 时应标记为 tooLong: len=%d", len(line))
		}
	})
}
