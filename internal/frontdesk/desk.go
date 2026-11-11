package frontdesk

import (
	"context"
	"errors"
	"log"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/store"
	"mcode/internal/textgen"
)

// KindStewardReply 标识前台回复消息。
const KindStewardReply = "steward_reply"

var messageTimeout = 3 * time.Minute
var messageGrace = 10 * time.Second

var errModelUnavailable = errors.New("前台模型不可用")

// FeedStore 提供主会话消息读写。
type FeedStore interface {
	AppendAssistant(context.Context, string, string, string) error
	Recent(context.Context, string, int) ([]llm.Message, error)
}

// BusyFeed 可选地向会话发布前台处理状态。
type BusyFeed interface {
	SetBusy(threadID string, busy bool)
}

// CreateTaskInput 描述前台创建的任务。
type CreateTaskInput struct{ Title, Body, ProjectID, Priority, Provider, Model, Origin, SourceMessageID string }

// TaskService 提供任务操作。
type TaskService interface {
	ListTasks(context.Context, string) ([]store.Task, error)
	GetTask(context.Context, string) (store.Task, error)
	CreateTask(context.Context, CreateTaskInput) (store.Task, error)
	AmendTask(context.Context, string, string, string) error
	CancelTask(context.Context, string) error
	SetPriority(context.Context, string, string) error
	ReassignTask(context.Context, string, string, string) error
	RecentTask(ctx context.Context, threadID string) (store.Task, time.Time, error)
}

// DecisionService 提供决策卡操作。
type DecisionService interface {
	ListDecisions(context.Context, string, int) ([]store.StewardDecision, error)
	AnswerDecision(context.Context, string, string, string) error
}

// Project 描述项目。
type Project struct{ ID, Name, Path string }

// ProjectService 提供项目列表。
type ProjectService interface {
	List(context.Context) ([]Project, error)
}

// Generator 调用文本生成服务。
type Generator func(context.Context, textgen.Options, string) (string, error)

// Desk 处理主会话的前台消息。
type Desk struct {
	Client         llm.Client
	Model          string
	Tools          *Toolbox
	Feed           FeedStore
	Tasks          TaskService
	Decisions      DecisionService
	Memory         MemoryService
	Projects       ProjectService
	Automations    AutomationService
	ProjectPath    string
	Now            func() time.Time
	MaxRounds      int
	CLIFallback    bool
	Settings       func() (cliFallback bool, provider, model string)
	Generator      Generator
	CLIOptions     textgen.Options
	mu             sync.Mutex
	queue          []Inbound
	running        bool
	recentAnswerID string
	recentAnswerAt time.Time
	handle         func(context.Context, Inbound) error
}

type messageFeed struct {
	FeedStore
	mu     sync.Mutex
	closed bool
}

func (f *messageFeed) AppendAssistant(ctx context.Context, threadID, kind, text string) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return context.DeadlineExceeded
	}
	f.mu.Unlock()
	return f.FeedStore.AppendAssistant(ctx, threadID, kind, text)
}

// Inbound 描述一条收到的消息。
type Inbound struct {
	ThreadID, MessageID, Text, ReplyTo string
	SentAt                             time.Time
	Images                             []llm.Part
}

// Snapshot 保存前台调用使用的上下文。
type Snapshot struct {
	Text     string
	Answered string
}

var decisionPattern = regexp.MustCompile(`(?s)^\s*(D-\d{4,})\b[\s:：,，\-]*(.*)$`)

func (d *Desk) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// AnswerDecision 回答决策卡并记录最近一次回答。
func (d *Desk) AnswerDecision(ctx context.Context, id, answer, by string) error {
	return d.answerDecision(ctx, id, answer, by)
}

func (d *Desk) answerDecision(ctx context.Context, id, answer, by string) error {
	if strings.TrimSpace(answer) == "" {
		return errors.New("请给出答案")
	}
	if d.Decisions == nil {
		return errors.New("决策服务不可用")
	}
	if err := d.Decisions.AnswerDecision(ctx, id, answer, by); err != nil {
		return err
	}
	at := d.now()
	d.mu.Lock()
	d.recentAnswerID = id
	d.recentAnswerAt = at
	d.mu.Unlock()
	return nil
}

// HandleMessage 同步处理一条消息。
func (d *Desk) HandleMessage(ctx context.Context, in Inbound) error {
	if m := decisionPattern.FindStringSubmatch(in.Text); m != nil {
		if strings.TrimSpace(m[2]) == "" {
			d.reply(ctx, in.ThreadID, "请给出答案")
			return nil
		}
		return d.answerDecision(ctx, m[1], m[2], "frontdesk")
	}
	snap := d.snapshot(ctx, in)
	return d.loop(ctx, in, snap)
}

func (d *Desk) messageWorker(feed FeedStore) *Desk {
	d.mu.Lock()
	defer d.mu.Unlock()
	worker := &Desk{}
	src, dst := reflect.ValueOf(d).Elem(), reflect.ValueOf(worker).Elem()
	// 使用反射让新增的导出字段自动随消息复制。
	for i := 0; i < src.NumField(); i++ {
		if src.Type().Field(i).IsExported() {
			dst.Field(i).Set(src.Field(i))
		}
	}
	worker.recentAnswerID, worker.recentAnswerAt = d.recentAnswerID, d.recentAnswerAt
	if d.Feed != nil {
		worker.Feed = feed
	}
	return worker
}

// Enqueue 按入队顺序异步处理消息。
func (d *Desk) Enqueue(in Inbound) {
	d.mu.Lock()
	d.queue = append(d.queue, in)
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()
	go func() {
		for {
			d.mu.Lock()
			if len(d.queue) == 0 {
				d.running = false
				d.mu.Unlock()
				return
			}
			next := d.queue[0]
			d.queue = d.queue[1:]
			d.mu.Unlock()
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("frontdesk: 处理消息 panic：%v", r)
						d.reply(context.Background(), next.ThreadID, "任务未能保存，请重试")
					}
				}()
				if feed, ok := d.Feed.(BusyFeed); ok {
					feed.SetBusy(next.ThreadID, true)
					defer feed.SetBusy(next.ThreadID, false)
				}
				ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
				defer cancel()
				// 每条消息使用独立的回复入口，超时后拒绝迟到的写入。
				feed := &messageFeed{FeedStore: d.Feed}
				worker := d.messageWorker(feed)
				done := make(chan error, 1)
				go func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("frontdesk: 处理消息 panic：%v", r)
							done <- errors.New("处理消息异常")
						}
					}()
					if d.handle != nil {
						done <- d.handle(ctx, next)
					} else {
						done <- worker.HandleMessage(ctx, next)
					}
				}()
				var err error
				select {
				case err = <-done:
				case <-ctx.Done():
					timer := time.NewTimer(messageGrace)
					select {
					case err = <-done:
						timer.Stop()
					case <-timer.C:
						feed.mu.Lock()
						feed.closed = true
						feed.mu.Unlock()
						log.Printf("frontdesk: 处理消息超时，放弃等待")
						d.reply(context.Background(), next.ThreadID, "处理超时，请重试")
						return
					}
				}
				worker.mu.Lock()
				d.mu.Lock()
				if worker.recentAnswerAt.After(d.recentAnswerAt) {
					d.recentAnswerID, d.recentAnswerAt = worker.recentAnswerID, worker.recentAnswerAt
				}
				d.mu.Unlock()
				worker.mu.Unlock()
				if err != nil {
					log.Printf("frontdesk: %v", err)
					message := "任务未能保存，请重试"
					if errors.Is(err, context.DeadlineExceeded) {
						message = "处理超时，请重试"
					} else if errors.Is(err, errModelUnavailable) {
						message = "模型暂时不可用，请重试"
					}
					d.reply(context.Background(), next.ThreadID, message)
				}
			}()
		}
	}()
}
