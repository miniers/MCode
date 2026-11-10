package store

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// 消息正文压缩：正文（messages.text / messages.tool_input）清空后，
// 压缩字节写进 messages.text_z / messages.tool_input_z，messages.text_encoding 标记算法。
//
// 为什么必须用 BLOB 列：messages.text 声明为 TEXT，若把压缩字节直接塞进 TEXT 列，
// SQLite 的 TEXT 亲和性不会转换它，值以 BLOB 存储类落盘；而 database/sql 的 convertAssign
// 会把 []byte 静默赋给 string 目标，结果是读成功、内容是乱码。BLOB 列无亲和性转换，
// []byte 进出原样，且 zstd 帧自带内容校验，保证「解压 == 原文」逐字节成立。
//
// 算法用 zstd：纯 Go 实现（github.com/klauspost/compress，不引入 cgo），
// 同样的工具输出比 gzip 更小、编解码更快。gzip 只为解压旧数据保留，不再写新数据。

const (
	// messageEncodingZstd 标记 text_z/tool_input_z 使用 zstd 压缩；空串表示未压缩。
	messageEncodingZstd = "zstd"

	// messageEncodingGzip 是旧算法，只用于解压既有数据。
	messageEncodingGzip = "gzip"

	// messageContentMaxBytes 是单列解压后的上限，防脏数据（压缩炸弹）撑爆内存。
	messageContentMaxBytes = 64 << 20
)

// 包级无字典编码器：创建开销大，进程内复用；不关闭，随进程退出回收。
// 带字典的编码器与解码器由 message_dict.go 管理（注册表 + 编码器缓存）。
// zstd.Encoder.EncodeAll 与 zstd.Decoder.DecodeAll 都允许并发调用。
var (
	messageZstdOnce    sync.Once
	messageZstdEncoder *zstd.Encoder
	messageZstdEncErr  error
)

// messageContentDecodeError 表示某条消息的压缩正文解不开。
// 带上消息 ID，ListMessagesTolerant 才能在跳过该行的同时把它记入 badIDs。
type messageContentDecodeError struct {
	MessageID string
	Encoding  string
	Err       error
}

func (e *messageContentDecodeError) Error() string {
	return fmt.Sprintf("decode message %s content (encoding %q): %v", e.MessageID, e.Encoding, e.Err)
}

func (e *messageContentDecodeError) Unwrap() error { return e.Err }

// zstdMessageEncoder 懒初始化包级 zstd 编码器。
// 并发度 1：只做 EncodeAll（内存 buffered 模式），不需要异步流水线，也避免多开编码器占内存。
func zstdMessageEncoder() (*zstd.Encoder, error) {
	messageZstdOnce.Do(func() {
		messageZstdEncoder, messageZstdEncErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
			zstd.WithEncoderConcurrency(1),
		)
	})
	return messageZstdEncoder, messageZstdEncErr
}

// encodeMessageContent 用 zstd 压缩正文；dictID 为 0 表示不带字典，非 0 时使用已加载的共享字典。
// 两个入参都为空时返回 (nil, nil, nil)。
func encodeMessageContent(text, toolInput string, dictID uint32) (textZ, toolInputZ []byte, err error) {
	if text == "" && toolInput == "" {
		return nil, nil, nil
	}
	enc, err := messageEncoderForDict(dictID)
	if err != nil {
		return nil, nil, fmt.Errorf("init zstd encoder: %w", err)
	}
	// EncodeAll 是同步一次性接口，不会返回错误；空串保持 nil，与「原文为空」一一对应。
	if text != "" {
		textZ = enc.EncodeAll([]byte(text), nil)
	}
	if toolInput != "" {
		toolInputZ = enc.EncodeAll([]byte(toolInput), nil)
	}
	return textZ, toolInputZ, nil
}

// decodeMessageContent 按编码还原正文；未知编码返回错误，空编码原样返回（不做解压）。
// 压缩字节为 nil 表示该列原文是空串。
func decodeMessageContent(encoding string, textZ, toolInputZ []byte) (text, toolInput string, err error) {
	switch encoding {
	case "":
		return "", "", nil
	case messageEncodingGzip:
		if len(textZ) > 0 {
			if text, err = gunzipMessageContent(textZ); err != nil {
				return "", "", fmt.Errorf("gunzip message text: %w", err)
			}
		}
		if len(toolInputZ) > 0 {
			if toolInput, err = gunzipMessageContent(toolInputZ); err != nil {
				return "", "", fmt.Errorf("gunzip message tool_input: %w", err)
			}
		}
		return text, toolInput, nil
	case messageEncodingZstd:
		if len(textZ) > 0 {
			if text, err = unzstdMessageContent(textZ); err != nil {
				return "", "", fmt.Errorf("zstd decode message text: %w", err)
			}
		}
		if len(toolInputZ) > 0 {
			if toolInput, err = unzstdMessageContent(toolInputZ); err != nil {
				return "", "", fmt.Errorf("zstd decode message tool_input: %w", err)
			}
		}
		return text, toolInput, nil
	default:
		return "", "", fmt.Errorf("unknown message content encoding %q", encoding)
	}
}

// unzstdMessageContent 解压单列正文；zstd 帧自带校验和，损坏即报错。
func unzstdMessageContent(b []byte) (string, error) {
	dec, err := messageZstdDecoder()
	if err != nil {
		return "", err
	}
	out, err := dec.DecodeAll(b, nil)
	if err != nil {
		return "", err
	}
	if len(out) > messageContentMaxBytes {
		return "", fmt.Errorf("decompressed content exceeds %d bytes", int64(messageContentMaxBytes))
	}
	return string(out), nil
}

// gzipMessageContent 用默认压缩级别压缩一个字符串；只用于测试构造老库里的 gzip 数据。
func gzipMessageContent(s string) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzipMessageContent 解压单列正文；读完时 gzip 会校验 CRC32 与长度，损坏即报错。
func gunzipMessageContent(b []byte) (string, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	n, copyErr := io.Copy(&buf, io.LimitReader(r, messageContentMaxBytes+1))
	closeErr := r.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n > messageContentMaxBytes {
		return "", fmt.Errorf("decompressed content exceeds %d bytes", int64(messageContentMaxBytes))
	}
	return buf.String(), nil
}
