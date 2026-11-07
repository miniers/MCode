package mcodeui

import "testing"

// TestTurnRunSuppressesEnd 验证「回合仍在运行」时的完成块推迟判定：只压正在进行的那一轮，
// 会话运行中的历史回合与会话空闲时的最新回合都照常显示完成块。
func TestTurnRunSuppressesEnd(t *testing.T) {
	tests := []struct {
		name      string
		running   bool
		latest    int
		turnIndex int
		want      bool
	}{
		{name: "运行中的最新回合", running: true, latest: 2, turnIndex: 2, want: true},
		{name: "运行中的历史回合", running: true, latest: 2, turnIndex: 1, want: false},
		{name: "会话已结束", running: false, latest: 2, turnIndex: 2, want: false},
		{name: "还没消息", running: true, latest: -1, turnIndex: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := turnRunSuppressesEnd(tt.running, tt.latest, tt.turnIndex); got != tt.want {
				t.Errorf("turnRunSuppressesEnd(%v, %d, %d) = %v, want %v",
					tt.running, tt.latest, tt.turnIndex, got, tt.want)
			}
		})
	}
}

// TestRunningTurn 验证进行中回合的取法：运行中取消息里的最大回合号；会话空闲、刚发出新消息的
// 本地乐观运行中、以及会话已不存在时都不算进行中，避免把上一轮已经结束的完成块一起压掉。
func TestRunningTurn(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	s.mu.Lock()
	s.threads = []Thread{{ID: "t1"}}
	s.messages = []Message{{ID: "u1", Role: "user", TurnIndex: 0}, {ID: "a1", Role: "assistant", TurnIndex: 1}}
	s.messagesRev++
	s.mu.Unlock()

	if running, _ := s.runningTurn("t1"); running {
		t.Fatal("会话空闲时不应判定为进行中")
	}

	// 后端确认运行中：最新回合就是进行中的那一轮。
	s.mu.Lock()
	s.threads[0].IsRunning = true
	s.mu.Unlock()
	if running, latest := s.runningTurn("t1"); !running || latest != 1 {
		t.Fatalf("运行中应取最新回合号 1，实际 running=%v latest=%d", running, latest)
	}

	// 刚发出新消息、后端还没回推这一轮：上一轮已经结束，不能算进行中。
	s.mu.Lock()
	s.optimisticRunning["t1"] = true
	s.mu.Unlock()
	if running, _ := s.runningTurn("t1"); running {
		t.Fatal("本地乐观运行中不应压掉上一轮的完成块")
	}

	// 主会话本体空闲、名下子线程还在跑：这一轮同样没结束。
	s.mu.Lock()
	delete(s.optimisticRunning, "t1")
	s.threads[0].IsRunning = false
	s.threads[0].SwarmBusy = true
	s.mu.Unlock()
	if running, latest := s.runningTurn("t1"); !running || latest != 1 {
		t.Fatalf("SwarmBusy 时应判定为进行中，实际 running=%v latest=%d", running, latest)
	}

	// 未知会话（如 bee 子线程还没挂进状态表）：不做推迟。
	if running, _ := s.runningTurn("missing"); running {
		t.Fatal("未知会话不应判定为进行中")
	}
}
