package mcodeui

import "testing"

func TestQueueDragStartClearsPreviousDropTarget(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	setQueueDragging(s, "q2", true)
	setQueueDropBefore(s, "q2", "q1")
	setQueueDragging(s, "q2", false)
	setQueueDragging(s, "q2", true)
	if before := queueDropBeforeFor(s, "q2"); before != "" {
		t.Fatalf("重新拖拽后保留了旧落点 %q", before)
	}
	items := []QueuedFollowUp{{ID: "q1"}, {ID: "q2"}}
	if _, moved := reorderQueueItems(items, "q2", queueDropBeforeFor(s, "q2")); moved {
		t.Fatal("容器落点不应按旧落点重排")
	}

	setQueueDropBefore(s, "q2", "q1")
	setQueueDragging(s, "q2", true)
	if before := queueDropBeforeFor(s, "q2"); before != "q1" {
		t.Fatalf("拖拽中丢失了新落点 %q", before)
	}
}

func TestFileManagerThreadChangeResetsDialogDrag(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	t.Cleanup(func() { modalDragReset(modalDragFileManagerOp) })
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.threadID = "old-thread"
	st.ops = fileManagerOpsDialog{open: true, threadID: "old-thread"}
	fileManagerMu.Unlock()
	modalDragMu.Lock()
	modalDragOffsets[modalDragFileManagerOp] = &modalDragOffset{dx: 10, dy: 20, rw: 400, rh: 300}
	modalDragMu.Unlock()

	s.loadFileManagerDir("", "")
	fileManagerMu.Lock()
	ops := st.ops
	fileManagerMu.Unlock()
	if ops != (fileManagerOpsDialog{}) {
		t.Fatalf("切换会话后弹窗状态未清空：%+v", ops)
	}
	modalDragMu.Lock()
	_, exists := modalDragOffsets[modalDragFileManagerOp]
	modalDragMu.Unlock()
	if exists {
		t.Fatal("切换会话后弹窗拖拽和尺寸状态未复位")
	}
}
