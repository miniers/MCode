package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/store"
	"mcode/internal/textgen"
)

type scripted struct {
	replies   []*llm.Response
	texts     []string
	requests  []llm.Request
	err       error
	reasoning string
}

func (s *scripted) Stream(_ context.Context, r llm.Request, cb func(llm.Delta)) (*llm.Response, error) {
	s.requests = append(s.requests, r)
	if s.err != nil {
		return nil, s.err
	}
	i := len(s.requests) - 1
	if s.reasoning != "" {
		cb(llm.Delta{Type: "reasoning", Text: s.reasoning, Reasoning: s.reasoning})
	}
	cb(llm.Delta{Type: "text", Text: s.texts[i]})
	return s.replies[i], nil
}

type fake struct {
	mu     sync.Mutex
	events []string
	recent store.Task
	recentAt time.Time
	recentThread string
	createErr error
	amendErr error
	getErr error
}

func (f *fake) event(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, s)
}
func (f *fake) AppendAssistant(_ context.Context, _, _, text string) error {
	f.event("reply:" + text)
	return nil
}
func (f *fake) Recent(context.Context, string, int) ([]llm.Message, error) { return nil, nil }
func (f *fake) ListTasks(context.Context, string) ([]store.Task, error) {
	f.event("list")
	return nil, nil
}
func (f *fake) GetTask(_ context.Context, id string) (store.Task, error) {
	return store.Task{ID: id, Status: "done"}, f.getErr
}
func (f *fake) CreateTask(_ context.Context, in CreateTaskInput) (store.Task, error) {
	f.event("create:" + in.Title + ":" + in.Origin)
	return store.Task{ID: "t"}, f.createErr
}
func (f *fake) AmendTask(_ context.Context, id, text string) error {
	f.event("amend:" + id + ":" + text)
	return f.amendErr
}
func (f *fake) CancelTask(_ context.Context, id string) error {
	f.event("cancel:" + id)
	return nil
}
func (f *fake) SetPriority(context.Context, string, string) error          { return nil }
func (f *fake) ReassignTask(context.Context, string, string, string) error { return nil }
func (f *fake) RecentTask(_ context.Context, thread string) (store.Task, time.Time, error) {
	if f.recentThread != "" && f.recentThread != thread {
		return store.Task{}, time.Time{}, nil
	}
	return f.recent, f.recentAt, nil
}
func (f *fake) ListDecisions(context.Context, string, int) ([]store.StewardDecision, error) {
	return nil, nil
}
func (f *fake) AnswerDecision(_ context.Context, id, answer, by string) error {
	f.event("answer:" + id + ":" + answer)
	return nil
}
func call(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: name, Name: name, Arguments: json.RawMessage(args)}
}
func response(calls ...llm.ToolCall) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: calls}}
}
func TestLoop(t *testing.T) {
	cases := []struct {
		name    string
		replies []*llm.Response
		texts   []string
		want    []string
		count   int
	}{
		{"读取后回复", []*llm.Response{response(call("list_tasks", `{}`)), response()}, []string{"丢弃", "完成"}, []string{"list", "reply:完成"}, 2},
		{"读写混合", []*llm.Response{response(call("list_tasks", `{}`), call("create_task", `{"title":"工作"}`)), response(call("create_task", `{"title":"工作"}`))}, []string{"丢弃", "收到"}, []string{"list", "reply:收到", "create:工作:owner"}, 2},
		{"只有写", []*llm.Response{response(call("create_task", `{"title":"工作"}`))}, []string{"收到"}, []string{"reply:收到", "create:工作:owner"}, 1},
		{"无效参数", []*llm.Response{response(call("create_task", `{}`)), response()}, []string{"丢弃", "修正"}, []string{"reply:修正"}, 2},
		{"无效JSON", []*llm.Response{response(call("create_task", `{`)), response()}, []string{"丢弃", "修正"}, []string{"reply:修正"}, 2},
		{"未知", []*llm.Response{response(call("missing", `{}`)), response()}, []string{"丢弃", "修正"}, []string{"reply:修正"}, 2},
		{"轮数耗尽", []*llm.Response{response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`))}, []string{"丢弃", "丢弃", "丢弃", "丢弃"}, []string{"list", "list", "list", "list", "create:原话:owner"}, 4},
		{"写工具无文字", []*llm.Response{response(call("create_task", `{"title":"工作"}`))}, []string{""}, []string{"create:工作:owner"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			s := &scripted{replies: tc.replies, texts: tc.texts}
			d := Desk{Client: s, Feed: f, Tasks: f}
			err := d.loop(context.Background(), Inbound{Text: "原话"}, Snapshot{Text: "上下文"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.events, tc.want) {
				t.Fatalf("events %v", f.events)
			}
			if len(s.requests) != tc.count {
				t.Fatal(len(s.requests))
			}
			if tc.count > 1 && len(s.requests[1].Messages) < 3 {
				t.Fatal("缺少工具结果")
			}
			if tc.name == "读写混合" {
				p := s.requests[1].Messages[3].Content[0].Text
				if p != `"尚未执行，需要的话再发一次"` {
					t.Fatal(p)
				}
			}
		})
	}
}

func TestHandleMessageWriteFailures(t *testing.T) {
	writeErr := errors.New("写入失败")
	for _, tc := range []struct {
		name string
		calls []llm.ToolCall
		createErr error
		wantErr error
		want []string
	}{
		{"部分成功", []llm.ToolCall{call("create_task", `{"title":"任务"}`), call("amend_task", `{"task_id":"t","text":"继续"}`)}, nil, nil, []string{"list", "create:任务:owner", "amend:t:继续"}},
		{"全部失败", []llm.ToolCall{call("create_task", `{"title":"任务"}`)}, writeErr, writeErr, []string{"list", "create:任务:owner"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{createErr: tc.createErr, amendErr: writeErr}
			s := &scripted{replies: []*llm.Response{response(tc.calls...)}, texts: []string{""}}
			d := Desk{Client: s, Feed: f, Tasks: f}
			err := d.HandleMessage(context.Background(), Inbound{Text: "工作"})
			if !errors.Is(err, tc.wantErr) || !reflect.DeepEqual(f.events, tc.want) {
				t.Fatal(err, f.events)
			}
		})
	}
}

func TestCLIWritePartialSuccess(t *testing.T) {
	f := &fake{amendErr: errors.New("写入失败")}
	d := Desk{Client: &scripted{err: errors.New("模型失败")}, Feed: f, Tasks: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
		return `{"reply":"","calls":[{"tool":"create_task","args":{"title":"任务"}},{"tool":"amend_task","args":{"task_id":"t","text":"继续"}}]}`, nil
	}}
	err := d.HandleMessage(context.Background(), Inbound{Text: "工作"})
	if err != nil || !reflect.DeepEqual(f.events, []string{"list", "create:任务:owner", "amend:t:继续"}) {
		t.Fatal(err, f.events)
	}
}

func TestFallback(t *testing.T) {
	f := &fake{}
	s := &scripted{err: errors.New("401")}
	d := Desk{Client: s, Feed: f, Tasks: f}
	if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if len(s.requests) != 1 || !reflect.DeepEqual(f.events, []string{"create:工作:owner"}) {
		t.Fatal(f.events)
	}
	f.events = nil
	f.recent = store.Task{ID: "t"}
	f.recentAt = time.Now()
	if err := d.fallback(context.Background(), Inbound{Text: "继续"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"amend:t:继续"}) {
		t.Fatal(f.events)
	}
}
func TestCLI(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		enabled   bool
		want      []string
		calls     int
	}{
		{"关闭", "", false, []string{"create:工作:owner"}, 0},
		{"正常", `{"reply":"收到","calls":[{"tool":"create_task","args":{"title":"任务"}}]}`, true, []string{"reply:收到", "create:任务:owner"}, 1},
		{"损坏", "{", true, []string{"create:工作:owner"}, 1},
		{"丢弃读", `{"reply":"收到","calls":[{"tool":"list_tasks","args":{}}]}`, true, []string{"create:工作:owner"}, 1},
		{"空答", `{"reply":"","calls":[]}`, true, []string{"create:工作:owner"}, 1},
		{"空答写入", `{"reply":"","calls":[{"tool":"create_task","args":{"title":"任务"}}]}`, true, []string{"create:任务:owner"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			n := 0
			d := Desk{Feed: f, Tasks: f, CLIFallback: tc.enabled, Generator: func(context.Context, textgen.Options, string) (string, error) {
				n++
				return tc.out, nil
			}}
			if err := d.fallback(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
				t.Fatal(err)
			}
			if n != tc.calls || !reflect.DeepEqual(f.events, tc.want) {
				t.Fatal(n, f.events)
			}
		})
	}
}
func TestBareDecision(t *testing.T) {
	f := &fake{}
	s := &scripted{}
	d := Desk{Client: s, Decisions: f}
	if err := d.HandleMessage(context.Background(), Inbound{Text: "D-0001 A"}); err != nil {
		t.Fatal(err)
	}
	if len(s.requests) != 0 || !reflect.DeepEqual(f.events, []string{"answer:D-0001:A"}) {
		t.Fatal(f.events)
	}
}

func TestSnapshotRecentAnswerHint(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	current := now
	f := &fake{}
	d := Desk{Tasks: f, Decisions: f, Now: func() time.Time { return current }}
	if err := d.HandleMessage(context.Background(), Inbound{Text: "D-0001 B"}); err != nil {
		t.Fatal(err)
	}
	snap := d.snapshot(context.Background(), Inbound{})
	if snap.Answered != "D-0001" || !strings.Contains(snap.Text, "刚答过 D-0001，这条可能是在改答") {
		t.Fatalf("五分钟内快照未提示改答: %+v", snap)
	}
	current = now.Add(5*time.Minute + time.Second)
	snap = d.snapshot(context.Background(), Inbound{})
	if snap.Answered != "" || strings.Contains(snap.Text, "刚答过 D-0001") {
		t.Fatalf("超过五分钟仍提示改答: %+v", snap)
	}
}

func TestAnswerDecisionRecordsRecentAnswer(t *testing.T) {
	f := &fake{}
	d := Desk{Tasks: f, Decisions: f}
	if err := d.AnswerDecision(context.Background(), "D-0001", "A", "owner"); err != nil {
		t.Fatal(err)
	}
	if snap := d.snapshot(context.Background(), Inbound{}); !strings.Contains(snap.Text, "刚答过 D-0001") {
		t.Fatalf("快照未提示最近回答: %+v", snap)
	}
}

func TestQueue(t *testing.T) {
	f := &fake{}
	d := Desk{Decisions: f}
	for _, s := range []string{"D-0001 A", "D-0002 B", "D-0003 C"} {
		d.Enqueue(Inbound{Text: s})
	}
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		n := len(f.events)
		events := append([]string{}, f.events...)
		f.mu.Unlock()
		if n == 3 {
			if !reflect.DeepEqual(events, []string{"answer:D-0001:A", "answer:D-0002:B", "answer:D-0003:C"}) {
				t.Fatal(events)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("队列超时")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
func TestTaskTools(t *testing.T) {
	f := &fake{}
	d := Desk{Tasks: f, Decisions: f}
	tools := NewToolbox(&d)
	for _, c := range []llm.ToolCall{call("create_task", `{"title":"任务"}`), call("amend_task", `{"task_id":"t","text":"继续"}`), call("cancel_task", `{"task_id":"child"}`), call("answer_decision", `{"id":"D-0001","answer":"A"}`)} {
		tool, _ := tools.Get(c.Name)
		if _, err := tool.Run(context.Background(), c.Arguments); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(f.events, []string{"create:任务:owner", "amend:t:继续", "cancel:child", "answer:D-0001:A"}) {
		t.Fatal(f.events)
	}
}

func TestFrontdeskBehaviors(t *testing.T) {
	t.Run("思考增量不进入回复", func(t *testing.T) {
		f := &fake{}
		s := &scripted{replies: []*llm.Response{response()}, texts: []string{"回复"}, reasoning: "思考"}
		d := Desk{Client: s, Feed: f, Tasks: f}
		if err := d.loop(context.Background(), Inbound{}, Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.events, []string{"reply:回复"}) {
			t.Fatal(f.events)
		}
	})

	t.Run("读取结果作为工具消息传入下一轮", func(t *testing.T) {
		f := &fake{}
		s := &scripted{replies: []*llm.Response{response(call("list_tasks", `{}`)), response()}, texts: []string{"", "完成"}}
		d := Desk{Client: s, Feed: f, Tasks: f}
		if err := d.loop(context.Background(), Inbound{Text: "状态"}, Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if len(s.requests) != 2 {
			t.Fatalf("请求次数：%d", len(s.requests))
		}
		msgs := s.requests[1].Messages
		if len(msgs) < 3 || msgs[2].Role != "tool" || msgs[2].ToolCallID != "list_tasks" || len(msgs[2].Content) == 0 || msgs[2].Content[0].Text != "null" {
			t.Fatalf("工具结果：%+v", msgs)
		}
	})

	t.Run("无效写入和未知工具返回错误", func(t *testing.T) {
		for _, tc := range []struct{ name, tool, args, message string }{
			{"缺少参数", "create_task", `{}`, "缺少 title"},
			{"无效JSON", "create_task", `{`, "需要 JSON 对象"},
			{"未知工具", "missing", `{}`, "未知工具"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := &fake{}
				s := &scripted{replies: []*llm.Response{response(call(tc.tool, tc.args)), response()}, texts: []string{"", ""}}
				d := Desk{Client: s, Feed: f, Tasks: f}
				if err := d.loop(context.Background(), Inbound{}, Snapshot{}); err != nil {
					t.Fatal(err)
				}
				if len(s.requests) != 2 || len(s.requests[1].Messages) < 3 {
					t.Fatal("缺少下一轮请求")
				}
				msg := s.requests[1].Messages[2]
				if len(f.events) != 0 || msg.Role != "tool" || !msg.IsError || len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Text, tc.message) {
					t.Fatalf("事件 %v，结果 %+v", f.events, msg)
				}
			})
		}
	})

	t.Run("空文本的干净轮允许静默回复", func(t *testing.T) {
		f := &fake{}
		s := &scripted{replies: []*llm.Response{response()}, texts: []string{"  "}}
		d := Desk{Client: s, Feed: f, Tasks: f}
		if err := d.loop(context.Background(), Inbound{}, Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if len(f.events) != 0 {
			t.Fatal(f.events)
		}
	})

	t.Run("五位决策编号直接回答", func(t *testing.T) {
		f := &fake{}
		s := &scripted{}
		d := Desk{Client: s, Decisions: f}
		if err := d.HandleMessage(context.Background(), Inbound{Text: "D-10000 B"}); err != nil {
			t.Fatal(err)
		}
		if len(s.requests) != 0 || !reflect.DeepEqual(f.events, []string{"answer:D-10000:B"}) {
			t.Fatal(f.events)
		}
	})
}

func TestSilentFallbackBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		text string
		age  time.Duration
		want string
	}{
		{"四十字且十分钟内追加", strings.Repeat("字", 40), 10 * time.Minute, "amend:t:" + strings.Repeat("字", 40)},
		{"超过四十字新建", strings.Repeat("字", 81), time.Minute, "create:" + strings.Repeat("字", 80) + ":owner"},
		{"多行新建并取首行", "第一行\n第二行", time.Minute, "create:第一行:owner"},
		{"超过十分钟新建", "继续", 10*time.Minute + time.Second, "create:继续:owner"},
		{"未来提及无效", "继续", -time.Second, "create:继续:owner"},
		{"CRLF 首行", " 第一行\r\n第二行", time.Minute, "create:第一行:owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{recent: store.Task{ID: "t"}, recentAt: now.Add(-tc.age)}
			d := Desk{Tasks: f, Feed: f, Now: func() time.Time { return now }}
			if err := d.fallback(context.Background(), Inbound{Text: tc.text}, Snapshot{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.events, []string{tc.want}) {
				t.Fatal(f.events)
			}
		})
	}
}

func TestSelectModel(t *testing.T) {
	t.Run("显式模型优先", func(t *testing.T) {
		sources := []apisource.Source{{Kind: "test", Models: []string{"haiku"}, DefaultModel: "default"}}
		_, model, err := SelectSource(sources, nil, "test", "chosen")
		if err != nil || model != "chosen" {
			t.Fatalf("模型 %q，错误 %v", model, err)
		}
	})
	for _, name := range []string{"haiku", "mini", "flash"} {
		t.Run("按关键词选择_"+name, func(t *testing.T) {
			sources := []apisource.Source{{Kind: "test", Models: []string{"large", "model-" + name}, DefaultModel: "default"}}
			_, model, err := SelectSource(sources, nil, "test", "")
			if err != nil || model != "model-"+name {
				t.Fatalf("模型 %q，错误 %v", model, err)
			}
		})
	}
	t.Run("没有来源返回错误", func(t *testing.T) {
		_, _, err := SelectSource(nil, nil, "", "")
		if !errors.Is(err, ErrNoModel) {
			t.Fatal(err)
		}
	})
}

type busyFake struct {
	*fake
	done chan struct{}
}

func (f *busyFake) SetBusy(_ string, busy bool) {
	if busy {
		f.event("busy:true")
	} else {
		f.event("busy:false")
		close(f.done)
	}
}

func TestSilentFallbackAndBusy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client llm.Client
		want   string
	}{
		{"模型失败", &scripted{err: errors.New("401\n授权失败")}, ""},
		{"没有模型", &scripted{err: ErrNoModel}, "reply:没有可用模型，请在主会话选择模型。"},
		{"主动兜底", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &busyFake{fake: &fake{}, done: make(chan struct{})}
			d := Desk{Client: tc.client, Feed: f, Tasks: f}
			d.Enqueue(Inbound{ThreadID: "feed", Text: "工作"})
			select {
			case <-f.done:
			case <-time.After(time.Second):
				t.Fatal("处理超时")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := []string{"busy:true", "list", "create:工作:owner"}
			if tc.want != "" {
				want = append(want, tc.want)
			}
			want = append(want, "busy:false")
			if !reflect.DeepEqual(f.events, want) {
				t.Fatalf("事件 %v", f.events)
			}
		})
	}
}

func TestSelectSourceLegacyAndPrefixedModel(t *testing.T) {
	t.Setenv("FRONTDESK_TEST_KEY", "key")
	sources := []apisource.Source{{Kind: "magpie", APIKeyEnv: "FRONTDESK_TEST_KEY", Models: []string{"auto/deepseek-v4.1-flash", "auto/claude-opus-5-5"}}}
	for _, tc := range []struct{ provider, model, want string }{
		{"pi", "magpie-jp/auto/deepseek-v4.1-flash", "auto/deepseek-v4.1-flash"},
		{"magpie", "magpie-jp/auto/deepseek-v4.1-flash", "auto/deepseek-v4.1-flash"},
		{"magpie", "auto/claude-opus-5-5", "auto/claude-opus-5-5"},
		{"magpie", "unknown/model", "unknown/model"},
	} {
		src, model, err := SelectSource(sources, nil, tc.provider, tc.model)
		if err != nil || src.Kind != "magpie" || model != tc.want {
			t.Errorf("provider %q，来源 %q，模型 %q，错误 %v", tc.provider, src.Kind, model, err)
		}
	}
}

func TestErrorSummaryJSON(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`llm: API error (status 404): {"error":{"type":"not_found","message":"magpie knows no model example"}}`, "magpie knows no model example"},
		{`API error: {"message":" top\n level "}`, "top level"},
		{`API error: {broken`, "API error: {broken"},
	} {
		if got := errorSummary(errors.New(tc.input)); got != tc.want {
			t.Errorf("摘要 %q，期望 %q", got, tc.want)
		}
	}
}

func TestModelFailureCLIFallback(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   []string
	}{
		{`{"reply":"收到"}`, []string{"reply:收到"}},
		{`{"reply":"收到","calls":[]}`, []string{"reply:收到"}},
		{`{"reply":"收到","calls":null}`, []string{"reply:收到"}},
		{`{`, []string{"amend:t:工作"}},
	} {
		f := &fake{recent: store.Task{ID: "t"}, recentAt: time.Now()}
		d := Desk{Client: &scripted{err: errors.New("401")}, Feed: f, Tasks: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
			return tc.output, nil
		}}
		if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.events, tc.want) {
			t.Fatalf("事件 %v，期望 %v", f.events, tc.want)
		}
	}
}

func TestModelFailureMissingReplyTask(t *testing.T) {
	f := &fake{getErr: store.ErrNotFound}
	d := Desk{Client: &scripted{err: errors.New("401")}, Feed: f, Tasks: f}
	if err := d.loop(context.Background(), Inbound{ReplyTo: "missing", Text: "工作"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"create:工作:owner"}) {
		t.Fatal(f.events)
	}
}

func TestModelFailureCLIEmptyFallsBack(t *testing.T) {
	f := &fake{}
	d := Desk{Client: &scripted{err: errors.New("401")}, Feed: f, Tasks: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
		return `{"reply":"","calls":[]}`, nil
	}}
	if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"create:工作:owner"}) {
		t.Fatal(f.events)
	}
}

func TestFallbackAssociation(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, thread, replyTo, text, want string
		getErr error
	}{
		{"显式任务优先且可重开", "feed", "done", "长补充\n第二行", "amend:done:长补充\n第二行", nil},
		{"已清理任务匹配最近任务", "feed", "missing", "继续", "amend:recent:继续", store.ErrNotFound},
		{"失效目标返回错误", "feed", "missing", "继续", "", errors.New("not found")},
		{"跨会话不关联", "other", "", "继续", "create:继续:owner", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{recent: store.Task{ID: "recent"}, recentAt: now, recentThread: "feed", getErr: tc.getErr}
			d := Desk{Tasks: f, Feed: f, Now: func() time.Time { return now }}
			err := d.fallback(context.Background(), Inbound{ThreadID: tc.thread, ReplyTo: tc.replyTo, Text: tc.text}, Snapshot{})
			if (err != nil) != (tc.getErr != nil && !errors.Is(tc.getErr, store.ErrNotFound)) {
				t.Fatal(err)
			}
			if tc.want == "" && len(f.events) != 0 || tc.want != "" && !reflect.DeepEqual(f.events, []string{tc.want}) {
				t.Fatal(f.events)
			}
		})
	}
}

func TestFailureFallbackAndCLIBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		client *scripted
		generator Generator
		want string
	}{
		{"404", &scripted{err: errors.New("404")}, nil, "create:工作:owner"},
		{"空响应", &scripted{replies: []*llm.Response{nil}, texts: []string{""}}, nil, "create:工作:owner"},
		{"CLI 超时", &scripted{err: errors.New("404")}, func(context.Context, textgen.Options, string) (string, error) { return "", context.DeadlineExceeded }, "create:工作:owner"},
		{"整批校验", &scripted{err: errors.New("404")}, func(context.Context, textgen.Options, string) (string, error) { return `{"reply":"收到","calls":[{"tool":"create_task","args":{"title":"不执行"}},{"tool":"list_tasks","args":{}}]}`, nil }, "create:工作:owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			d := Desk{Client: tc.client, Tasks: f, Feed: f, CLIFallback: true, Generator: tc.generator}
			if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil || !reflect.DeepEqual(f.events, []string{tc.want}) {
				t.Fatal(err, f.events)
			}
		})
	}
	t.Run("CLI 写入失败不重复创建", func(t *testing.T) {
		f := &fake{createErr: errors.New("写入失败")}
		d := Desk{Tasks: f, Feed: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
			return `{"reply":"","calls":[{"tool":"create_task","args":{"title":"任务"}}]}`, nil
		}}
		if err := d.fallback(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err == nil || !reflect.DeepEqual(f.events, []string{"create:任务:owner"}) {
			t.Fatal(err, f.events)
		}
	})
	t.Run("无模型时 CLI 成功不提示", func(t *testing.T) {
		f := &fake{}
		d := Desk{Client: &scripted{err: ErrNoModel}, Tasks: f, Feed: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
			return `{"reply":"收到","calls":[]}`, nil
		}}
		if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil || !reflect.DeepEqual(f.events, []string{"reply:收到"}) {
			t.Fatal(err, f.events)
		}
	})
	t.Run("轮数耗尽追加最近提及", func(t *testing.T) {
		now := time.Now()
		f := &fake{recent: store.Task{ID: "t"}, recentAt: now}
		s := &scripted{replies: []*llm.Response{response(call("list_tasks", `{}`))}, texts: []string{""}}
		d := Desk{Client: s, Tasks: f, Feed: f, MaxRounds: 1, Now: func() time.Time { return now }}
		if err := d.loop(context.Background(), Inbound{Text: "继续"}, Snapshot{}); err != nil || !reflect.DeepEqual(f.events, []string{"list", "amend:t:继续"}) {
			t.Fatal(err, f.events)
		}
	})
	t.Run("最大四轮且 CLI 只调用一次", func(t *testing.T) {
		f := &fake{}
		s := &scripted{replies: []*llm.Response{response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`)), response(call("list_tasks", `{}`))}, texts: []string{"", "", "", ""}}
		calls := 0
		d := Desk{Client: s, Tasks: f, Feed: f, MaxRounds: 9, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) { calls++; return "{", nil }}
		if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil || len(s.requests) != 4 || calls != 1 || !reflect.DeepEqual(f.events, []string{"list", "list", "list", "list", "create:工作:owner"}) {
			t.Fatal(err, calls, f.events)
		}
	})
}

func TestDirectAnswersAndDecisionCorrection(t *testing.T) {
	for _, text := range []string{"你好", "现在怎么样"} {
		t.Run(text, func(t *testing.T) {
			f := &fake{}
			s := &scripted{replies: []*llm.Response{response()}, texts: []string{"直接回答"}}
			d := Desk{Client: s, Tasks: f, Feed: f}
			if err := d.HandleMessage(context.Background(), Inbound{Text: text}); err != nil || !reflect.DeepEqual(f.events, []string{"list", "reply:直接回答"}) || !strings.Contains(s.requests[0].System, "闲聊、致谢") {
				t.Fatal(err, f.events)
			}
		})
	}
	t.Run("最近决策改答", func(t *testing.T) {
		f := &fake{}
		s := &scripted{replies: []*llm.Response{response(call("answer_decision", `{"id":"D-0001","answer":"B"}`))}, texts: []string{""}}
		d := Desk{Client: s, Tasks: f, Feed: f, Decisions: f}
		if err := d.AnswerDecision(context.Background(), "D-0001", "A", "owner"); err != nil {
			t.Fatal(err)
		}
		if err := d.HandleMessage(context.Background(), Inbound{Text: "改成 B"}); err != nil || !strings.Contains(s.requests[0].System, "刚答过 D-0001") || !reflect.DeepEqual(f.events, []string{"answer:D-0001:A", "list", "answer:D-0001:B"}) {
			t.Fatal(err, f.events)
		}
	})
}

func TestSelectSourceSkipsKeyedSourceWithoutModel(t *testing.T) {
	t.Setenv("FRONTDESK_TEST_KEY", "key")
	sources := []apisource.Source{
		{Kind: "empty", APIKeyEnv: "FRONTDESK_TEST_KEY"},
		{Kind: "ready", APIKeyEnv: "FRONTDESK_TEST_KEY", DefaultModel: "mini"},
	}
	src, model, err := SelectSource(sources, nil, "", "")
	if err != nil || src.Kind != "ready" || model != "mini" {
		t.Fatalf("来源 %s，模型 %s，错误 %v", src.Kind, model, err)
	}
}
