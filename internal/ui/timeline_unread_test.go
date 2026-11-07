package mcodeui

import "testing"

// unreadStep 是未读状态的一帧：这一帧看到的消息列表、是否停在底部，以及期望的未读数。
type unreadStep struct {
	messages []Message
	atEnd    bool
	want     int
}

// TestTimelineUnreadObserve 验证离开底部后的未读计数：只数用户消息与助手正文，
// 同一条消息流式变长不重复计数，回到底部清零。
func TestTimelineUnreadObserve(t *testing.T) {
	user := func(id, text string) Message { return Message{ID: id, Role: "user", Text: text} }
	answer := func(id, text string) Message { return Message{ID: id, Role: "assistant", Text: text} }
	tool := func(id string) Message { return Message{ID: id, Role: "tool", Kind: "tool", Text: "bash"} }
	thinking := func(id string) Message { return Message{ID: id, Role: "assistant", Kind: "reasoning", Text: "想想"} }
	system := func(id string) Message { return Message{ID: id, Role: "system", Text: "系统提示"} }

	tests := []struct {
		name  string
		steps []unreadStep
	}{
		{
			name: "在底部时不计数",
			steps: []unreadStep{
				{messages: []Message{user("u1", "你好"), answer("a1", "在的")}, atEnd: true, want: 0},
				{messages: []Message{user("u1", "你好"), answer("a1", "在的"), answer("a2", "还有别的吗")}, atEnd: true, want: 0},
			},
		},
		{
			name: "离开后新增 2 条助手消息计为 2",
			steps: []unreadStep{
				{messages: []Message{user("u1", "解释一下"), answer("a1", "第一段")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "解释一下"), answer("a1", "第一段"), answer("a2", "第二段"), answer("a3", "第三段")}, atEnd: false, want: 2},
			},
		},
		{
			name: "只新增工具消息计为 0",
			steps: []unreadStep{
				{messages: []Message{user("u1", "跑一下测试"), answer("a1", "开始")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "跑一下测试"), answer("a1", "开始"), tool("t1"), tool("t2")}, atEnd: false, want: 0},
			},
		},
		{
			name: "思考与系统消息不计入未读",
			steps: []unreadStep{
				{messages: []Message{user("u1", "看一下"), answer("a1", "好的")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "看一下"), answer("a1", "好的"), thinking("r1"), system("s1")}, atEnd: false, want: 0},
			},
		},
		{
			name: "同一条消息流式变长不重复计数",
			steps: []unreadStep{
				{messages: []Message{user("u1", "写一段"), answer("a1", "已经")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "写一段"), answer("a1", "已经写好了")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "写一段"), answer("a1", "已经写好了，再补充一点")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "写一段"), answer("a1", "已经写好了，再补充一点"), answer("a2", "新的回复")}, atEnd: false, want: 1},
			},
		},
		{
			name: "用户消息也计入未读",
			steps: []unreadStep{
				{messages: []Message{answer("a1", "上一轮")}, atEnd: false, want: 0},
				{messages: []Message{answer("a1", "上一轮"), user("u2", "再问一句")}, atEnd: false, want: 1},
				{messages: []Message{answer("a1", "上一轮"), user("u2", "再问一句"), user("u3", "还有")}, atEnd: false, want: 2},
			},
		},
		{
			name: "基线为空会话时从第一条消息开始计",
			steps: []unreadStep{
				{messages: nil, atEnd: false, want: 0},
				{messages: []Message{user("u1", "第一条")}, atEnd: false, want: 1},
			},
		},
		{
			name: "回到底部清零并在再次离开时重新记基线",
			steps: []unreadStep{
				{messages: []Message{user("u1", "问"), answer("a1", "答")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "问"), answer("a1", "答"), answer("a2", "新增")}, atEnd: false, want: 1},
				{messages: []Message{user("u1", "问"), answer("a1", "答"), answer("a2", "新增")}, atEnd: true, want: 0},
				{messages: []Message{user("u1", "问"), answer("a1", "答"), answer("a2", "新增")}, atEnd: false, want: 0},
				{messages: []Message{user("u1", "问"), answer("a1", "答"), answer("a2", "新增"), answer("a3", "再来一条")}, atEnd: false, want: 1},
			},
		},
		{
			name: "基线锚点消息消失时按条数差估算",
			steps: []unreadStep{
				{messages: []Message{answer("a1", "旧"), answer("a2", "旧二")}, atEnd: false, want: 0},
				{messages: []Message{answer("a3", "新一"), answer("a4", "新二"), answer("a5", "新三")}, atEnd: false, want: 1},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var state timelineUnreadState
			for i, step := range tc.steps {
				if got := state.observe("t1", step.messages, step.atEnd); got != step.want {
					t.Fatalf("第 %d 步 observe = %d, want %d", i+1, got, step.want)
				}
			}
		})
	}
}

// TestTimelineUnreadStateKeepsThreadsSeparate 验证未读基线跟着会话走，切换会话不串计数。
func TestTimelineUnreadStateKeepsThreadsSeparate(t *testing.T) {
	var state timelineUnreadState
	base := []Message{{ID: "u1", Role: "user", Text: "问"}, {ID: "a1", Role: "assistant", Text: "答"}}
	other := []Message{{ID: "b1", Role: "user", Text: "另一个会话"}}

	if got := state.observe("t1", base, false); got != 0 {
		t.Fatalf("t1 首次离开底部 observe = %d, want 0", got)
	}
	if got := state.observe("t2", other, false); got != 0 {
		t.Fatalf("t2 首次离开底部 observe = %d, want 0", got)
	}
	if got := state.observe("t1", append(append([]Message{}, base...), Message{ID: "a2", Role: "assistant", Text: "新"}), false); got != 1 {
		t.Fatalf("t1 新增一条助手正文 observe = %d, want 1", got)
	}
	if got := state.observe("t2", other, false); got != 0 {
		t.Fatalf("t2 未新增消息时 observe = %d, want 0", got)
	}
	state.reset("t1")
	if got := state.observe("t1", base, false); got != 0 {
		t.Fatalf("reset 后重新离开底部 observe = %d, want 0", got)
	}
}

// TestUnreadJumpLabel 验证回到底部胶囊的未读文案，超过 99 条显示 99+。
func TestUnreadJumpLabel(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, ""},
		{-3, ""},
		{1, "1 条新消息"},
		{2, "2 条新消息"},
		{99, "99 条新消息"},
		{100, "99+ 条新消息"},
		{120, "99+ 条新消息"},
	}
	for _, tc := range tests {
		if got := unreadJumpLabel(tc.n); got != tc.want {
			t.Errorf("unreadJumpLabel(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
