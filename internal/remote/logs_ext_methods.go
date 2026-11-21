package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mcode/internal/remote/wire"
)

func init() {
	RegisterMethod("logs.subscribe", handleLogsSubscribe)
	RegisterMethod("logs.unsubscribe", handleLogsUnsubscribe)
}

// handleLogsSubscribe 订阅指定会话的实时工具/脚本日志流，并返回当前流与环缓冲快照。
func handleLogsSubscribe(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.RemoteLogsSubscribeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(p.ThreadID)
	if threadID == "" {
		return nil, ExtInvalidParams("Invalid params: 'threadID' is required")
	}

	b, ok := d.memoryBackend()
	if !ok {
		return nil, ExtNotImplemented()
	}
	if !b.hasThread(threadID) {
		return nil, &ExtError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	return b.ensureLogHub().Subscribe(c, threadID), nil
}

// handleLogsUnsubscribe 取消当前连接对指定会话实时日志流的订阅。
func handleLogsUnsubscribe(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.RemoteLogsUnsubscribeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(p.ThreadID)
	if threadID == "" {
		return nil, ExtInvalidParams("Invalid params: 'threadID' is required")
	}

	b, ok := d.memoryBackend()
	if !ok {
		return nil, ExtNotImplemented()
	}
	b.ensureLogHub().Unsubscribe(c, threadID)
	return wire.RemoteLogsUnsubscribeResult{Unsubscribed: true}, nil
}
