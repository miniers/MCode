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
func (f *fake) GetTask(context.Context, string) (store.Task, error) {
	return store.Task{Status: "todo"}, nil
}
func (f *fake) CreateTask(_ context.Context, in CreateTaskInput) (store.Task, error) {
	f.event("create:" + in.Title + ":" + in.Origin)
	return store.Task{ID: "t"}, nil
}
func (f *fake) AmendTask(_ context.Context, id, text string) error {
	f.event("amend:" + id + ":" + text)
	return nil
}
func (f *fake) CancelTask(_ context.Context, id string) error {
	f.event("cancel:" + id)
	return nil
}
func (f *fake) SetPriority(context.Context, string, string) error          { return nil }
func (f *fake) ReassignTask(context.Context, string, string, string) error { return nil }
func (f *fake) RecentTask(context.Context) (store.Task, error)             { return f.recent, nil }
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
func TestFallback(t *testing.T) {
	f := &fake{}
	s := &scripted{err: errors.New("401")}
	d := Desk{Client: s, Tasks: f}
	if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if len(s.requests) != 1 || !reflect.DeepEqual(f.events, []string{"create:工作:owner"}) {
		t.Fatal(f.events)
	}
	f.events = nil
	f.recent = store.Task{ID: "t", UpdatedAt: time.Now()}
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
		{"丢弃读", `{"reply":"收到","calls":[{"tool":"list_tasks","args":{}}]}`, true, []string{"reply:收到"}, 1},
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

	t.Run("空文本的干净轮不落回复", func(t *testing.T) {
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{recent: store.Task{ID: "t", UpdatedAt: now.Add(-tc.age)}}
			d := Desk{Tasks: f, Now: func() time.Time { return now }}
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
