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

const KindStewardReply = "steward_reply"

type FeedStore interface {
 AppendAssistant(context.Context, string, string, string) error
 Recent(context.Context, string, int) ([]llm.Message, error)
}
type CreateTaskInput struct { Title, Body, ProjectID, Priority, Provider, Model, Origin string }
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
type DecisionService interface {
 ListDecisions(context.Context, string, int) ([]store.StewardDecision, error)
 AnswerDecision(context.Context, string, string, string) error
}
type Project struct { ID, Name, Path string }
type ProjectService interface { List(context.Context) ([]Project, error) }
type Generator func(context.Context, textgen.Options, string) (string, error)

type Desk struct {
 Client llm.Client
 Model string
 Tools *Toolbox
 Feed FeedStore
 Tasks TaskService
 Decisions DecisionService
 Memory MemoryService
 Projects ProjectService
 ProjectPath string
 Now func() time.Time
 MaxRounds int
 CLIFallback bool
 Generator Generator
 CLIOptions textgen.Options
 mu sync.Mutex
 queue []Inbound
 running bool
}
type Inbound struct { ThreadID, Text, ReplyTo string; SentAt time.Time; Images []llm.Part }
type Snapshot struct { Text string; OpenTaskIDs, OpenDecisionIDs []string; Answered string }
var decisionPattern = regexp.MustCompile(`(?s)^\s*(D-\d{4,})\b[\s:：,，\-]*(.*)$`)
func (d *Desk) now() time.Time { if d.Now != nil { return d.Now() }; return time.Now() }
func (d *Desk) HandleMessage(ctx context.Context, in Inbound) error {
 if m := decisionPattern.FindStringSubmatch(in.Text); m != nil {
  if d.Decisions == nil { return errors.New("决策服务不可用") }
  return d.Decisions.AnswerDecision(ctx, m[1], m[2], "frontdesk")
 }
 snap := d.snapshot(ctx, in)
 return d.loop(ctx, in, snap)
}
func (d *Desk) Enqueue(in Inbound) {
 d.mu.Lock(); d.queue = append(d.queue, in)
 if d.running { d.mu.Unlock(); return }; d.running = true; d.mu.Unlock()
 go func() { for { d.mu.Lock(); if len(d.queue)==0 { d.running=false; d.mu.Unlock(); return }; next:=d.queue[0]; d.queue=d.queue[1:]; d.mu.Unlock(); if err:=d.HandleMessage(context.Background(), next); err!=nil { log.Printf("frontdesk: %v", err) } } }()
}
