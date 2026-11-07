package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"mcode/internal/remote/wire"
)

// maxMessageMediaBytes 限制单次读取消息媒体的累计字节上限，避免异常分块导致内存膨胀。
const maxMessageMediaBytes = 32 << 20

// ReadMessageMedia 分块读取时间线消息中的媒体字节，返回 MIME 类型与完整内容。
// variant 为空时按原图（full）读取；分块通过 nextOffset 递进，直到后端不再返回下一偏移。
func (c *Client) ReadMessageMedia(ctx context.Context, threadID, messageID, mediaID, variant string) (string, []byte, error) {
	if c.backend == nil {
		return "", nil, errors.New("remote backend 不可用")
	}
	if variant == "" {
		variant = wire.MediaVariantFull
	}

	var (
		buf      bytes.Buffer
		mimeType string
		offset   *int64
	)
	for {
		res, err := c.backend.ThreadsMediaRead(ctx, wire.ThreadsMediaReadParams{
			ThreadID:  threadID,
			MessageID: messageID,
			MediaID:   mediaID,
			Variant:   variant,
			Offset:    offset,
		})
		if err != nil {
			return "", nil, err
		}
		if mimeType == "" {
			mimeType = res.MimeType
		}

		if res.Base64 != "" {
			chunk, err := base64.StdEncoding.DecodeString(res.Base64)
			if err != nil {
				return "", nil, fmt.Errorf("解码媒体分块失败: %w", err)
			}
			if buf.Len()+len(chunk) > maxMessageMediaBytes {
				return "", nil, fmt.Errorf("媒体内容超过 %d 字节上限", maxMessageMediaBytes)
			}
			buf.Write(chunk)
		}

		if res.NextOffset == nil {
			break
		}
		var current int64
		if offset != nil {
			current = *offset
		}
		// 偏移量未前进说明后端分块逻辑异常，直接报错避免死循环。
		if *res.NextOffset <= current {
			return "", nil, fmt.Errorf("媒体分块偏移未前进: %d -> %d", current, *res.NextOffset)
		}
		next := *res.NextOffset
		offset = &next
	}

	return mimeType, buf.Bytes(), nil
}
