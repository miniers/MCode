package frontdesk

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"mcode/internal/agent/llm"
)

type timeoutFeed struct {
	replies chan string
	busy    chan bool
}

func (f *timeoutFeed) AppendAssistant(_ context.Context, _, _, text string) error {
	f.replies <- text
	return nil
}
func (f *timeoutFeed) Recent(context.Context, string, int) ([]llm.Message, error) { return nil, nil }
func (f *timeoutFeed) SetBusy(_ string, busy bool)                                { f.busy <- busy }

func timeoutReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("等待消息超时")
		var zero T
		return zero
	}
}

func TestMessageWorkerCopiesExportedFields(t *testing.T) {
	originalFeed, workerFeed := &timeoutFeed{}, &timeoutFeed{}
	d := &Desk{
		Feed:           originalFeed,
		Model:          "model",
		MaxRounds:      3,
		ProjectPath:    "/project",
		Settings:       func() (bool, string) { return true, "provider" },
		Automations:    &fakeAutomationService{},
		queue:          []Inbound{{Text: "queued"}},
		running:        true,
		recentAnswerID: "D-0001",
		recentAnswerAt: time.Unix(123, 0),
		handle:         func(context.Context, Inbound) error { return nil },
	}
	worker := d.messageWorker(workerFeed)
	src, dst := reflect.ValueOf(d).Elem(), reflect.ValueOf(worker).Elem()
	for i := 0; i < src.NumField(); i++ {
		field := src.Type().Field(i)
		if !field.IsExported() || field.Name == "Feed" {
			continue
		}
		if field.Type.Kind() == reflect.Func {
			if src.Field(i).Pointer() != dst.Field(i).Pointer() {
				t.Errorf("字段 %s 未复制", field.Name)
			}
		} else if !reflect.DeepEqual(src.Field(i).Interface(), dst.Field(i).Interface()) {
			t.Errorf("字段 %s 未复制", field.Name)
		}
	}
	if worker.Feed != workerFeed {
		t.Error("未使用消息专用 Feed")
	}
	if worker.queue != nil || worker.running || worker.handle != nil {
		t.Error("复制了队列状态")
	}
	if worker.recentAnswerID != d.recentAnswerID || !worker.recentAnswerAt.Equal(d.recentAnswerAt) {
		t.Error("未复制最近的回答")
	}
	d.Feed = nil
	if d.messageWorker(workerFeed).Feed != nil {
		t.Error("原 Feed 为空时不应设置消息专用 Feed")
	}
}

func TestEnqueueTimeoutContinuesAndIgnoresLateError(t *testing.T) {
	oldTimeout, oldGrace := messageTimeout, messageGrace
	messageTimeout, messageGrace = 20*time.Millisecond, 20*time.Millisecond
	defer func() { messageTimeout, messageGrace = oldTimeout, oldGrace }()
	feed := &timeoutFeed{replies: make(chan string, 4), busy: make(chan bool, 4)}
	release := make(chan struct{})
	returned := make(chan struct{})
	second := make(chan struct{})
	d := &Desk{Feed: feed, handle: func(_ context.Context, in Inbound) error {
		if in.Text == "first" {
			<-release
			close(returned)
			return errors.New("迟到的错误")
		}
		close(second)
		return nil
	}}
	d.Enqueue(Inbound{ThreadID: "feed", Text: "first"})
	d.Enqueue(Inbound{ThreadID: "feed", Text: "second"})
	if got := timeoutReceive(t, feed.replies); got != "处理超时，请重试" {
		t.Fatalf("回复错误: %q", got)
	}
	if !timeoutReceive(t, feed.busy) || timeoutReceive(t, feed.busy) {
		t.Fatal("超时后应清除忙碌状态")
	}
	timeoutReceive(t, second)
	if !timeoutReceive(t, feed.busy) || timeoutReceive(t, feed.busy) {
		t.Fatal("下一条消息的忙碌状态错误")
	}
	close(release)
	timeoutReceive(t, returned)
	select {
	case got := <-feed.replies:
		t.Fatalf("迟到的错误产生了第二条回复: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

type timeoutClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *timeoutClient) Stream(_ context.Context, _ llm.Request, delta func(llm.Delta)) (*llm.Response, error) {
	close(c.started)
	<-c.release
	delta(llm.Delta{Type: "text", Text: "迟到的回复"})
	return &llm.Response{}, nil
}

func TestEnqueueTimeoutSuppressesLateModelReply(t *testing.T) {
	oldTimeout, oldGrace := messageTimeout, messageGrace
	messageTimeout, messageGrace = 20*time.Millisecond, 20*time.Millisecond
	defer func() { messageTimeout, messageGrace = oldTimeout, oldGrace }()
	feed := &timeoutFeed{replies: make(chan string, 4), busy: make(chan bool, 4)}
	client := &timeoutClient{started: make(chan struct{}), release: make(chan struct{})}
	d := &Desk{Feed: feed, Client: client}
	d.Enqueue(Inbound{ThreadID: "feed", Text: "first"})
	timeoutReceive(t, client.started)
	if got := timeoutReceive(t, feed.replies); got != "处理超时，请重试" {
		t.Fatalf("回复错误: %q", got)
	}
	timeoutReceive(t, feed.busy)
	timeoutReceive(t, feed.busy)
	close(client.release)
	select {
	case got := <-feed.replies:
		t.Fatalf("迟到的模型回复未被拦截: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}
