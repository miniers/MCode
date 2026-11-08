package frontdesk

import (
	"context"
	"errors"
	"log"
	"regexp"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/store"
	"mcode/internal/textgen"
)

// KindStewardReply 标识前台回复消息。
const KindStewardReply = "steward_reply"

// FeedStore 提供主会话消息读写。
type FeedStore interface {
	AppendAssistant(context.Context, string, string, string) error
	Recent(context.Context, string, int) ([]llm.Message, error)
}

// CreateTaskInput 描述前台创建的任务。
type CreateTaskInput struct{ Title, Body, ProjectID, Priority, Provider, Model, Origin string }

// TaskService 提供任务操作。
type TaskService interface {
	ListTasks(context.Context, string) ([]store.Task, error)
	GetTask(context.Context, string) (store.Task, error)
	CreateTask(context.Context, CreateTaskInput) (store.Task, error)
	AmendTask(context.Context, string, string) error
	CancelTask(context.Context, string) error
	SetPriority(context.Context, string, string) error
	ReassignTask(context.Context, string, string, string) error
	RecentTask(context.Context) (store.Task, error)
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
	ProjectPath    string
	Now            func() time.Time
	MaxRounds      int
	CLIFallback    bool
	Generator      Generator
	CLIOptions     textgen.Options
	mu             sync.Mutex
	queue          []Inbound
	running        bool
	recentAnswerID string
	recentAnswerAt time.Time
}

// Inbound 描述一条收到的消息。
type Inbound struct {
	ThreadID, Text, ReplyTo string
	SentAt                  time.Time
	Images                  []llm.Part
}

// Snapshot 保存前台调用使用的上下文。
type Snapshot struct {
	Text                         string
	OpenTaskIDs, OpenDecisionIDs []string
	Answered                     string
}

var decisionPattern = regexp.MustCompile(`(?s)^\s*(D-\d{4,})\b[\s:：,，\-]*(.*)$`)

func (d *Desk) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Desk) answerDecision(ctx context.Context, id, answer, by string) error {
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
		return d.answerDecision(ctx, m[1], m[2], "frontdesk")
	}
	snap := d.snapshot(ctx, in)
	return d.loop(ctx, in, snap)
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
			if err := d.HandleMessage(context.Background(), next); err != nil {
				log.Printf("frontdesk: %v", err)
			}
		}
	}()
}
