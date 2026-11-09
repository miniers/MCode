package mcodeui

import (
	"testing"
	"time"
)

func TestStewardUnreadClearedOnSelectionAndNewMessage(t *testing.T) {
	fake := newStewardFakeClient("t9")
	s := newChatState(fake)
	t.Cleanup(s.close)
	thread := Thread{ID: "t9", ProjectID: "p2", HasUnread: true}
	s.threads = []Thread{thread}
	s.threadsByProj = map[string][]Thread{"p2": {thread}}
	if !s.stewardThreadUnread("t9") {
		t.Fatal("未选中的总管应有未读点")
	}
	s.selectThread("t9")
	if s.stewardThreadUnread("t9") || s.threadsByProj["p2"][0].HasUnread {
		t.Fatal("打开总管后本地未读应清除")
	}
	waitCalls := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			calls := fake.BulkMarkReadCalls()
			if len(calls) >= want {
				call := calls[want-1]
				if call.ProjectID != "p2" || len(call.ThreadIDs) != 1 || call.ThreadIDs[0] != "t9" {
					t.Fatalf("已读调用错误: %+v", call)
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("没有收到已读调用")
	}
	waitCalls(1)
	s.onWatchUpdate(ThreadUpdate{ThreadID: "t9", Thread: thread, Messages: []Message{{ID: "m1", Role: "assistant", Kind: "steward_result"}}})
	if s.stewardThreadUnread("t9") {
		t.Fatal("正在查看时收到结果不应保留未读")
	}
	waitCalls(2)
}
