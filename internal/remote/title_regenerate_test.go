package remote

import (
	"context"
	"errors"
	"testing"

	"mcode/internal/remote/wire"
)

func TestThreadsTitleRegenerate(t *testing.T) {
	d := NewDispatcher(&fakeAuth{}, &fakeBackend{})
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	// 未注入回调
	resp := execThreadOpsRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.title.regenerate","params":{"threadId":"t1"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected unavailable error, got %+v", resp.Error)
	}

	var gotID string
	d.SetTitleRegenerator(func(ctx context.Context, id string) (string, error) {
		gotID = id
		return "新标题", nil
	})

	// 参数缺失
	resp = execThreadOpsRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.title.regenerate","params":{},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got %+v", resp.Error)
	}

	// 成功
	resp = execThreadOpsRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.title.regenerate","params":{"threadId":"t1"},"id":3}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]any)
	if !ok || res["title"] != "新标题" || gotID != "t1" {
		t.Fatalf("result = %+v, gotID = %q", resp.Result, gotID)
	}

	// 回调报错并脱敏
	d.SetTitleRegenerator(func(ctx context.Context, id string) (string, error) {
		return "", errors.New("boom Bearer abcdef1234567890XYZ")
	})
	resp = execThreadOpsRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.title.regenerate","params":{"threadId":"t1"},"id":4}`)
	if resp.Error == nil || resp.Error.Message != "boom Bearer ***" {
		t.Fatalf("expected redacted error, got %+v", resp.Error)
	}
}
