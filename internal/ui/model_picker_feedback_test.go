package mcodeui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestModelPickerRefreshFailureToast(t *testing.T) {
	client := modelsRefreshTestClient{Client: NewFakeClient(), refresh: func(context.Context) ([]ProviderInfo, error) { return nil, errors.New("offline") }}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.refreshProviders()
	deadline := time.After(uiWaitTimeout)
	for {
		if strings.Contains(recallTestToast(s), "刷新模型目录失败: offline") {
			return
		}
		select {
		case <-deadline:
			t.Fatal("等待刷新失败提示超时")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
