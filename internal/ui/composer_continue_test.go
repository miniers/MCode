package mcodeui

import "testing"

func TestShouldShowContinuePill(t *testing.T) {
	tests := []struct {
		name     string
		st       continuePillState
		wantShow bool
		wantKind continuePillKind
	}{
		{
			name:     "本地中断显示已中断",
			st:       continuePillState{locallyInterrupted: true},
			wantShow: true,
			wantKind: continuePillInterrupted,
		},
		{
			name:     "后端标记中断且未忽略显示",
			st:       continuePillState{lastStatus: "interrupted", lastMessageID: "m2"},
			wantShow: true,
			wantKind: continuePillInterrupted,
		},
		{
			name:     "后端标记取消也按已中断显示",
			st:       continuePillState{lastStatus: "cancelled"},
			wantShow: true,
			wantKind: continuePillInterrupted,
		},
		{
			name:     "后端状态大小写与空格不影响判定",
			st:       continuePillState{lastStatus: " Interrupted "},
			wantShow: true,
			wantKind: continuePillInterrupted,
		},
		{
			name:     "已忽略且最后消息未变化不显示",
			st:       continuePillState{lastStatus: "interrupted", lastMessageID: "m2", dismissed: true},
			wantShow: false,
			wantKind: continuePillNone,
		},
		{
			name:     "忽略后出现新消息重新显示",
			st:       continuePillState{lastStatus: "interrupted", lastMessageID: "m3", dismissed: false},
			wantShow: true,
			wantKind: continuePillInterrupted,
		},
		{
			name:     "失败且最后一条是错误消息不显示",
			st:       continuePillState{lastStatus: "failed", lastMessageID: "m2", lastMessageIsError: true},
			wantShow: false,
			wantKind: continuePillNone,
		},
		{
			name:     "失败且最后一条不是错误消息显示失败文案",
			st:       continuePillState{lastStatus: "failed", lastMessageID: "m2"},
			wantShow: true,
			wantKind: continuePillFailed,
		},
		{
			name:     "运行中不显示",
			st:       continuePillState{lastStatus: "interrupted", locallyInterrupted: true, isRunning: true},
			wantShow: false,
			wantKind: continuePillNone,
		},
		{
			name:     "已完成不显示",
			st:       continuePillState{lastStatus: "completed", lastMessageID: "m2"},
			wantShow: false,
			wantKind: continuePillNone,
		},
		{
			name:     "状态未知不显示",
			st:       continuePillState{lastMessageID: "m2"},
			wantShow: false,
			wantKind: continuePillNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotShow, gotKind := shouldShowContinuePill(tt.st)
			if gotShow != tt.wantShow || gotKind != tt.wantKind {
				t.Fatalf("shouldShowContinuePill(%+v) = (%v, %v)，期望 (%v, %v)",
					tt.st, gotShow, gotKind, tt.wantShow, tt.wantKind)
			}
		})
	}
}

func TestContinuePillLabels(t *testing.T) {
	tests := []struct {
		kind        continuePillKind
		wantStatus  string
		wantTooltip string
	}{
		{kind: continuePillInterrupted, wantStatus: "上一轮已中断", wantTooltip: "继续上一轮"},
		{kind: continuePillFailed, wantStatus: "上一轮失败", wantTooltip: "从失败处继续"},
	}
	for _, tt := range tests {
		t.Run(tt.wantStatus, func(t *testing.T) {
			status, tooltip := continuePillLabels(tt.kind)
			if status != tt.wantStatus || tooltip != tt.wantTooltip {
				t.Fatalf("continuePillLabels(%v) = (%q, %q)，期望 (%q, %q)",
					tt.kind, status, tooltip, tt.wantStatus, tt.wantTooltip)
			}
		})
	}
}

func TestContinuePillSnapshotDismissedFollowsLastMessage(t *testing.T) {
	s := &chatState{
		selectedThreadID:  "t1",
		threads:           []Thread{{ID: "t1", LastStatus: "interrupted"}},
		messages:          []Message{{ID: "m1", Role: "user"}, {ID: "m2", Role: "assistant"}},
		interrupted:       make(map[string]bool),
		continueDismissed: map[string]string{"t1": "m2"},
	}

	show, kind := shouldShowContinuePill(s.continuePillSnapshotLocked("t1"))
	if show || kind != continuePillNone {
		t.Fatalf("同一轮被忽略后应不显示，实际 (%v, %v)", show, kind)
	}

	// 出现新一轮：最后一条消息 ID 变化，忽略记录失效，胶囊重新出现。
	s.messages = append(s.messages, Message{ID: "m3", Role: "user"})
	show, kind = shouldShowContinuePill(s.continuePillSnapshotLocked("t1"))
	if !show || kind != continuePillInterrupted {
		t.Fatalf("出现新消息后应重新显示，实际 (%v, %v)", show, kind)
	}
}

func TestContinuePillSnapshotFailedWithErrorCard(t *testing.T) {
	s := &chatState{
		selectedThreadID: "t1",
		threads:          []Thread{{ID: "t1", LastStatus: "failed"}},
		messages:         []Message{{ID: "m1", Role: "user"}, {ID: "m2", Kind: "error", Role: "assistant"}},
	}
	if show, kind := shouldShowContinuePill(s.continuePillSnapshotLocked("t1")); show || kind != continuePillNone {
		t.Fatalf("失败且有错误卡片时不应显示继续胶囊，实际 (%v, %v)", show, kind)
	}
}
