package mcodeui

import "testing"

// TestSwarmBeesPending 覆盖「Swarm 子代理是否还没结束」的判定：
// bee 自身不算主会话，排队中的派发与运行中的子线程都要让主会话认为还没结束。
func TestSwarmBeesPending(t *testing.T) {
	tests := []struct {
		name string
		st   SwarmState
		want bool
	}{
		{
			name: "主会话且有运行中的子线程",
			st:   SwarmState{Bees: []SwarmBee{{Name: "Aspen", Status: "running"}}},
			want: true,
		},
		{
			name: "子线程自身不推迟",
			st:   SwarmState{IsBee: true, Bees: []SwarmBee{{Name: "Aspen", Status: "running"}}},
			want: false,
		},
		{
			name: "只有排队中的派发",
			st:   SwarmState{Queued: 1},
			want: true,
		},
		{
			name: "子线程都在终态",
			st: SwarmState{Bees: []SwarmBee{
				{Name: "Birch", Status: "completed"},
				{Name: "Aspen", Status: "failed"},
				{Name: "Saul", Status: "stopped"},
			}},
			want: false,
		},
		{
			name: "全空",
			st:   SwarmState{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := swarmBeesPending(tt.st); got != tt.want {
				t.Errorf("swarmBeesPending(%+v) = %v, want %v", tt.st, got, tt.want)
			}
		})
	}
}
