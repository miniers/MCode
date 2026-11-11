package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mu           sync.Mutex
	events       []string
	recent       store.Task
	recentAt     time.Time
	recentThread string
	createErr    error
	amendErr     error
	getErr       error
	recentErr    error
	created      chan CreateTaskInput
	amended      chan string
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
	if f.created != nil {
		f.created <- in
	}
	return store.Task{ID: "t"}, f.createErr
}
func (f *fake) AmendTask(_ context.Context, id, text, sourceMessageID string) error {
	f.event("amend:" + id + ":" + text)
	if f.amended != nil {
		f.amended <- sourceMessageID
	}
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
	return f.recent, f.recentAt, f.recentErr
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
		name      string
		calls     []llm.ToolCall
		createErr error
		wantErr   error
		want      []string
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
func TestFallbackRecentTaskErrors(t *testing.T) {
	storageErr := errors.New("存储失败")
	for _, tc := range []struct {
		err, wantErr error
		want         []string
	}{
		{storageErr, storageErr, nil},
		{fmt.Errorf("查询: %w", store.ErrNotFound), nil, []string{"create:工作:owner"}},
	} {
		f := &fake{recentErr: tc.err}
		err := (&Desk{Tasks: f}).fallback(context.Background(), Inbound{Text: "工作"}, Snapshot{})
		if !errors.Is(err, tc.wantErr) || !reflect.DeepEqual(f.events, tc.want) {
			t.Fatal(err, f.events)
		}
	}
}

func TestCLIFallbackSettingsChange(t *testing.T) {
	f := &fake{}
	enabled := false
	model := ""
	var providers, models []string
	d := Desk{Tasks: f, Settings: func() (bool, string, string) { return enabled, "codex", model }, Generator: func(_ context.Context, opts textgen.Options, _ string) (string, error) {
		providers = append(providers, opts.Provider)
		models = append(models, opts.Model)
		return `{"reply":"","calls":[]}`, nil
	}}
	if err := d.fallback(context.Background(), Inbound{Text: "第一轮"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	enabled = true
	if err := d.fallback(context.Background(), Inbound{Text: "第二轮"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	// 配置了总管模型时，CLI 兜底用它，不再强制最便宜的模型。
	model = "gpt-6.1-sol"
	if err := d.fallback(context.Background(), Inbound{Text: "第三轮"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"create:第一轮:owner"}) || !reflect.DeepEqual(providers, []string{"codex", "codex"}) {
		t.Fatal(f.events, providers)
	}
	if want := []string{textgen.CheapestModel("codex"), "gpt-6.1-sol"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("模型 %v，想要 %v", models, want)
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
		{"空答", `{"reply":"","calls":[]}`, true, nil, 1},
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

func TestQueueCreateTaskRecordsMessage(t *testing.T) {
	f := &fake{created: make(chan CreateTaskInput, 1)}
	s := &scripted{replies: []*llm.Response{response(call("create_task", `{"title":"任务"}`))}, texts: []string{""}}
	d := Desk{Client: s, Feed: f, Tasks: f}
	d.Enqueue(Inbound{ThreadID: "feed", MessageID: "message-1", Text: "工作"})
	select {
	case in := <-f.created:
		if in.SourceMessageID != "message-1" {
			t.Fatalf("消息来源错误: %+v", in)
		}
	case <-time.After(time.Second):
		t.Fatal("等待创建任务超时")
	}
}

func TestAmendTaskRecordsMessage(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			f := &fake{amended: make(chan string, 1)}
			s := &scripted{replies: []*llm.Response{response(call("amend_task", `{"task_id":"t","text":"继续"}`))}, texts: []string{""}}
			if fallback {
				s.err = errors.New("模型失败")
			}
			d := Desk{Client: s, Feed: f, Tasks: f}
			if err := d.HandleMessage(context.Background(), Inbound{MessageID: "message-1", Text: "继续", ReplyTo: "t"}); err != nil {
				t.Fatal(err)
			}
			select {
			case id := <-f.amended:
				if id != "message-1" {
					t.Fatalf("消息来源错误: %q", id)
				}
			default:
				t.Fatal("未补充任务")
			}
		})
	}
}

func TestFallbackCreateTaskRecordsMessage(t *testing.T) {
	for _, cli := range []bool{false, true} {
		t.Run(fmt.Sprintf("CLI=%t", cli), func(t *testing.T) {
			f := &fake{created: make(chan CreateTaskInput, 1)}
			d := Desk{Client: &scripted{err: errors.New("模型失败")}, Feed: f, Tasks: f, CLIFallback: cli}
			if cli {
				d.Generator = func(context.Context, textgen.Options, string) (string, error) {
					return `{"reply":"","calls":[{"tool":"create_task","args":{"title":"任务"}}]}`, nil
				}
			}
			if err := d.HandleMessage(context.Background(), Inbound{ThreadID: "feed", MessageID: "message-1", Text: "工作"}); err != nil {
				t.Fatal(err)
			}
			select {
			case in := <-f.created:
				if in.SourceMessageID != "message-1" {
					t.Fatalf("消息来源错误: %+v", in)
				}
			default:
				t.Fatal("未创建任务")
			}
		})
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
		sources := []apisource.Source{{Kind: "test", Models: []string{"haiku", "chosen"}, DefaultModel: "default"}}
		_, model, err := SelectSource(sources, nil, "test", "chosen")
		if err != nil || model != "chosen" {
			t.Fatalf("模型 %q，错误 %v", model, err)
		}
	})
	t.Run("自动跟随默认模型而不挑小模型", func(t *testing.T) {
		sources := []apisource.Source{{Kind: "test", Models: []string{"model-haiku", "model-mini", "large"}, DefaultModel: "large"}}
		_, model, err := SelectSource(sources, nil, "test", "")
		if err != nil || model != "large" {
			t.Fatalf("模型 %q，错误 %v", model, err)
		}
	})
	t.Run("没有默认模型时取目录第一个", func(t *testing.T) {
		sources := []apisource.Source{{Kind: "test", Models: []string{"first", "model-haiku"}}}
		_, model, err := SelectSource(sources, nil, "test", "")
		if err != nil || model != "first" {
			t.Fatalf("模型 %q，错误 %v", model, err)
		}
	})
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
		// 不在目录里的模型回落到默认。
		{"magpie", "unknown/model", "auto/deepseek-v4.1-flash"},
	} {
		src, model, err := SelectSource(sources, nil, tc.provider, tc.model)
		if err != nil || src.Kind != "magpie" || model != tc.want {
			t.Errorf("provider %q，来源 %q，模型 %q，错误 %v", tc.provider, src.Kind, model, err)
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
	if !reflect.DeepEqual(f.events, []string{"reply:这个任务已删除"}) {
		t.Fatal(f.events)
	}
}

func TestModelFailureCLIEmptyIsNoop(t *testing.T) {
	f := &fake{}
	d := Desk{Client: &scripted{err: errors.New("401")}, Feed: f, Tasks: f, CLIFallback: true, Generator: func(context.Context, textgen.Options, string) (string, error) {
		return `{"reply":"","calls":[]}`, nil
	}}
	if err := d.loop(context.Background(), Inbound{Text: "工作"}, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 0 {
		t.Fatal(f.events)
	}
}

func TestFallbackAssociation(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, thread, replyTo, text, want string
		getErr                            error
	}{
		{"显式任务优先且可重开", "feed", "done", "长补充\n第二行", "amend:done:长补充\n第二行", nil},
		{"已清理任务提示删除", "feed", "missing", "继续", "reply:这个任务已删除", store.ErrNotFound},
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
		name      string
		client    *scripted
		generator Generator
		want      string
	}{
		{"404", &scripted{err: errors.New("404")}, nil, "create:工作:owner"},
		{"空响应", &scripted{replies: []*llm.Response{nil}, texts: []string{""}}, nil, "create:工作:owner"},
		{"CLI 超时", &scripted{err: errors.New("404")}, func(context.Context, textgen.Options, string) (string, error) { return "", context.DeadlineExceeded }, "create:工作:owner"},
		{"整批校验", &scripted{err: errors.New("404")}, func(context.Context, textgen.Options, string) (string, error) {
			return `{"reply":"收到","calls":[{"tool":"create_task","args":{"title":"不执行"}},{"tool":"list_tasks","args":{}}]}`, nil
		}, "create:工作:owner"},
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

type committingMemory struct {
	requests []ProposeRequest
	commits  int
}

func (m *committingMemory) Search(context.Context, string, string, int) ([]MemoryHit, error) {
	return nil, nil
}
func (m *committingMemory) Get(context.Context, string, string) (MemoryHit, error) {
	return MemoryHit{}, nil
}
func (m *committingMemory) Forget(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *committingMemory) Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error) {
	m.requests = append(m.requests, req)
	if req.UserRequested {
		return m.Commit(ctx, "proposal")
	}
	return ProposeResult{ID: "proposal"}, nil
}
func (m *committingMemory) Commit(context.Context, string) (ProposeResult, error) {
	m.commits++
	return ProposeResult{ID: "proposal", Status: "committed"}, nil
}

func TestMemoryToolsCommitOnce(t *testing.T) {
	for _, tc := range []struct{ name, args, target string }{
		{"remember", `{"content":"偏好"}`, ""},
		{"edit_memory", `{"id":"memory","content":"新偏好"}`, "memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &committingMemory{}
			tool, _ := NewToolbox(&Desk{Memory: m, ProjectPath: "project"}).Get(tc.name)
			result, err := tool.Run(context.Background(), json.RawMessage(tc.args))
			if err != nil || m.commits != 1 || len(m.requests) != 1 || !m.requests[0].UserRequested || m.requests[0].TargetMemoryID != tc.target || m.requests[0].ProjectPath != "project" || result != (ProposeResult{ID: "proposal", Status: "committed"}) {
				t.Fatal(result, err, m.commits, m.requests)
			}
		})
	}
}

func TestDecisionRequiresAnswer(t *testing.T) {
	f := &fake{}
	d := Desk{Feed: f, Decisions: f}
	for _, text := range []string{"D-0001", "D-0001 :  \t"} {
		if err := d.HandleMessage(context.Background(), Inbound{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	tool, _ := NewToolbox(&d).Get("answer_decision")
	for _, args := range []string{`{"id":"D-0001","answer":"   "}`, `{"id":"D-0001"}`} {
		if _, err := tool.Run(context.Background(), json.RawMessage(args)); err == nil || err.Error() != "请给出答案" {
			t.Fatal(err)
		}
	}
	if err := d.AnswerDecision(context.Background(), "D-0001", " ", "owner"); err == nil || err.Error() != "请给出答案" {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"reply:请给出答案", "reply:请给出答案"}) {
		t.Fatal(f.events)
	}
}

type queueFeed struct {
	*fake
	done chan struct{}
}

func (f *queueFeed) SetBusy(_ string, busy bool) {
	if busy {
		f.event("busy:true")
	} else {
		f.event("busy:false")
		f.done <- struct{}{}
	}
}

type panicDecision struct{ *fake }

func (p *panicDecision) AnswerDecision(ctx context.Context, id, answer, by string) error {
	if id == "D-0001" {
		panic("处理失败")
	}
	return p.fake.AnswerDecision(ctx, id, answer, by)
}

func TestQueueRecoversAndContinues(t *testing.T) {
	f := &queueFeed{fake: &fake{}, done: make(chan struct{}, 2)}
	d := Desk{Feed: f, Decisions: &panicDecision{fake: f.fake}}
	d.Enqueue(Inbound{Text: "D-0001 A"})
	d.Enqueue(Inbound{Text: "D-0002 B"})
	for i := 0; i < 2; i++ {
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Fatal("队列未继续处理")
		}
	}
	f.mu.Lock()
	events := append([]string(nil), f.events...)
	f.mu.Unlock()
	if !reflect.DeepEqual(events, []string{"busy:true", "reply:任务未能保存，请重试", "busy:false", "busy:true", "answer:D-0002:B", "busy:false"}) {
		t.Fatal(events)
	}
	deadline := time.After(time.Second)
	for {
		d.mu.Lock()
		running := d.running
		d.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("队列运行状态未重置")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type deadlineClient struct{ deadline chan time.Duration }

func (c *deadlineClient) Stream(ctx context.Context, _ llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		c.deadline <- 0
	} else {
		c.deadline <- time.Until(deadline)
	}
	return response(), nil
}

func TestQueueSetsMessageDeadline(t *testing.T) {
	f := &busyFake{fake: &fake{}, done: make(chan struct{})}
	client := &deadlineClient{deadline: make(chan time.Duration, 1)}
	d := Desk{Feed: f, Client: client}
	d.Enqueue(Inbound{Text: "工作"})
	select {
	case remaining := <-client.deadline:
		if remaining <= 0 || remaining > messageTimeout {
			t.Fatalf("处理期限：%v", remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("未调用模型")
	}
	select {
	case <-f.done:
	case <-time.After(time.Second):
		t.Fatal("处理未完成")
	}
}

func TestQueueErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client llm.Client
		tasks  bool
		err    error
		want   string
	}{
		{"保存失败", nil, true, errors.New("写入失败"), "任务未能保存，请重试"},
		{"处理超时", &scripted{err: context.DeadlineExceeded}, true, nil, "处理超时，请重试"},
		{"模型失败且无法兜底", &scripted{err: errors.New("模型失败")}, false, nil, "模型暂时不可用，请重试"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &busyFake{fake: &fake{createErr: tc.err}, done: make(chan struct{})}
			d := Desk{Feed: f, Client: tc.client}
			if tc.tasks {
				d.Tasks = f
			}
			d.Enqueue(Inbound{Text: "工作"})
			select {
			case <-f.done:
			case <-time.After(time.Second):
				t.Fatal("处理超时")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.events) < 2 || f.events[len(f.events)-2] != "reply:"+tc.want || f.events[len(f.events)-1] != "busy:false" {
				t.Fatal(f.events)
			}
		})
	}
}
