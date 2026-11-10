package mcodeui

import "testing"

func TestSetToastRoutesEmbeddedStateToParent(t *testing.T) {
	parent := newChatState(NewFakeClient())
	child := newEmbeddedBeeState(parent)
	t.Cleanup(func() {
		embeddedMu.Lock()
		delete(embeddedParent, child)
		embeddedMu.Unlock()
		child.close()
		parent.close()
	})

	embeddedMu.Lock()
	embeddedParent[child] = parent
	embeddedMu.Unlock()

	child.setToast("来自子代理")

	parent.mu.Lock()
	parentToast := parent.toastMessage
	parent.mu.Unlock()
	child.mu.Lock()
	childToast := child.toastMessage
	child.mu.Unlock()
	if parentToast != "来自子代理" || childToast != "" {
		t.Fatalf("嵌入状态 Toast = parent %q, child %q，want parent only", parentToast, childToast)
	}
}

func TestSetToastKeepsUnregisteredState(t *testing.T) {
	state := newChatState(NewFakeClient())
	t.Cleanup(state.close)

	state.setToast("独立窗口")

	state.mu.Lock()
	toast := state.toastMessage
	state.mu.Unlock()
	if toast != "独立窗口" {
		t.Fatalf("未登记状态 Toast = %q，want %q", toast, "独立窗口")
	}
}
