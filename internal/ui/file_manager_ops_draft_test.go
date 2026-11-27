package mcodeui

import (
	"errors"
	"testing"
)

func TestFileManagerOpsDraftSurvivesBackgroundFinish(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := getFileManagerState(s)

	s.openFileManagerOpsDialog(fileManagerOpsNewFile, "", "")
	const draft = "user-typing.txt"
	st.opsNameDraft = draft

	fileManagerMu.Lock()
	op := st.ops
	fileManagerMu.Unlock()

	staleOp := op
	staleOp.gen = op.gen - 1
	s.finishFileManagerOps(staleOp, "stale.txt", nil)
	if st.opsNameDraft != draft {
		t.Fatalf("不匹配 gen 的后台完成回调改动了 opsNameDraft：got %q, want %q", st.opsNameDraft, draft)
	}

	s.finishFileManagerOps(op, "user-typing.txt", errors.New("创建失败"))
	if st.opsNameDraft != draft {
		t.Fatalf("失败的后台完成回调改动了 opsNameDraft：got %q, want %q", st.opsNameDraft, draft)
	}
}
