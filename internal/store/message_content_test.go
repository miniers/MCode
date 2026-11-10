package store

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnzstdMessageContentLimit(t *testing.T) {
	oldLimit := messageContentMaxBytes
	messageContentMaxBytes = 1 << 20
	t.Cleanup(func() { messageContentMaxBytes = oldLimit })
	enc, err := zstdMessageEncoder()
	if err != nil {
		t.Fatal(err)
	}
	frame := enc.EncodeAll(make([]byte, 64<<10), nil)
	payload := bytes.Repeat(frame, messageContentMaxBytes/(64<<10)+1)
	if _, err := unzstdMessageContent(payload); err == nil || !strings.Contains(err.Error(), "decompressed content exceeds") {
		t.Fatalf("expected size limit error, got %v", err)
	}
	got, err := unzstdMessageContent(enc.EncodeAll([]byte("正常内容"), nil))
	if err != nil || got != "正常内容" {
		t.Fatalf("round trip: got %q, error %v", got, err)
	}
}

// 逐字节一致：空串、中文、emoji、含 \x00 的二进制式文本、1MB 可压缩文本都要原样还原。
func TestMessageContentRoundTrip(t *testing.T) {
	longText := strings.Repeat("abc中文日志行 12345\n", 50000) // 约 1MB
	cases := []struct {
		name      string
		text      string
		toolInput string
	}{
		{name: "都为空", text: "", toolInput: ""},
		{name: "只有正文", text: "工具输出正文", toolInput: ""},
		{name: "只有输入", text: "", toolInput: `{"path":"a.go"}`},
		{name: "中文", text: "读取文件失败：权限不足", toolInput: `{"命令":"ls -la /tmp"}`},
		{name: "emoji", text: "✅ 完成 🚀 🥳", toolInput: "😀😀😀"},
		{name: "含 NUL", text: "a\x00b\x00c", toolInput: "\x00\x00"},
		{name: "超长单行", text: longText, toolInput: longText},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			textZ, toolInputZ, err := encodeMessageContent(tc.text, tc.toolInput, 0)
			if err != nil {
				t.Fatalf("encodeMessageContent 失败: %v", err)
			}
			if tc.text == "" && textZ != nil {
				t.Fatalf("正文为空时应返回 nil，实际 %d 字节", len(textZ))
			}
			if tc.toolInput == "" && toolInputZ != nil {
				t.Fatalf("tool_input 为空时应返回 nil，实际 %d 字节", len(toolInputZ))
			}

			text, toolInput, err := decodeMessageContent(messageEncodingZstd, textZ, toolInputZ)
			if err != nil {
				t.Fatalf("decodeMessageContent 失败: %v", err)
			}
			if text != tc.text {
				t.Fatalf("正文还原不一致：期望 %d 字节，实际 %d 字节", len(tc.text), len(text))
			}
			if toolInput != tc.toolInput {
				t.Fatalf("tool_input 还原不一致：期望 %d 字节，实际 %d 字节", len(tc.toolInput), len(toolInput))
			}
		})
	}
}

// 老库里的 gzip 数据必须仍然能解开：算法升级不能把历史消息变成乱码。
func TestDecodeMessageContentLegacyGzip(t *testing.T) {
	const text = "旧版本用 gzip 压缩的工具输出\n第二行"
	const toolInput = `{"command":"git status --short"}`

	textZ, err := gzipMessageContent(text)
	if err != nil {
		t.Fatalf("gzipMessageContent 失败: %v", err)
	}
	toolInputZ, err := gzipMessageContent(toolInput)
	if err != nil {
		t.Fatalf("gzipMessageContent 失败: %v", err)
	}

	gotText, gotInput, err := decodeMessageContent(messageEncodingGzip, textZ, toolInputZ)
	if err != nil {
		t.Fatalf("decodeMessageContent(gzip) 失败: %v", err)
	}
	if gotText != text || gotInput != toolInput {
		t.Fatal("gzip 老数据还原不一致")
	}
}

// 1MB 可压缩文本必须真的变小，否则「压缩」只是把正文搬了个位置。
func TestMessageContentCompressesLongText(t *testing.T) {
	text := strings.Repeat("abc中文日志行 12345\n", 50000)
	textZ, _, err := encodeMessageContent(text, "", 0)
	if err != nil {
		t.Fatalf("encodeMessageContent 失败: %v", err)
	}
	if len(textZ) >= len(text) {
		t.Fatalf("压缩后未变小：原文 %d 字节，压缩后 %d 字节", len(text), len(textZ))
	}
}

// 空编码表示未压缩，原样返回，不做任何解压。
func TestDecodeMessageContentEmptyEncoding(t *testing.T) {
	text, toolInput, err := decodeMessageContent("", nil, nil)
	if err != nil {
		t.Fatalf("空编码不应报错: %v", err)
	}
	if text != "" || toolInput != "" {
		t.Fatalf("空编码应返回空串，实际 %q / %q", text, toolInput)
	}
}

// 未知编码一律报错，读路径不会把二进制当明文交出去。
func TestDecodeMessageContentUnknownEncoding(t *testing.T) {
	if _, _, err := decodeMessageContent("snappy", nil, nil); err == nil {
		t.Fatal("未知编码应报错")
	}
}

// 帧损坏（校验和或魔数被改）必须报错，不能静默返回半截正文。
func TestDecodeMessageContentCorrupt(t *testing.T) {
	textZ, _, err := encodeMessageContent("需要压缩的正文内容", "", 0)
	if err != nil {
		t.Fatalf("encodeMessageContent 失败: %v", err)
	}
	corrupt := append([]byte(nil), textZ...)
	corrupt[0] ^= 0xff
	if _, _, err := decodeMessageContent(messageEncodingZstd, corrupt, nil); err == nil {
		t.Fatal("压缩字节损坏时应报错")
	}
}

// bit 级往返：zstd 输出对同一输入是确定的，同一份数据编码两次必须完全相同（幂等的基础）。
func TestEncodeMessageContentDeterministic(t *testing.T) {
	first, firstInput, err := encodeMessageContent(`{"a":1}`, "工具输入", 0)
	if err != nil {
		t.Fatalf("首次编码失败: %v", err)
	}
	second, secondInput, err := encodeMessageContent(`{"a":1}`, "工具输入", 0)
	if err != nil {
		t.Fatalf("再次编码失败: %v", err)
	}
	if string(first) != string(second) || string(firstInput) != string(secondInput) {
		t.Fatal("同一输入的 zstd 输出不稳定，幂等性会受影响")
	}
}
