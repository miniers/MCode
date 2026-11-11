package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"mcode/internal/agent/llm"
)

// Tool 定义一个前台工具。
type Tool struct {
	Name, Description string
	Parameters        json.RawMessage
	Write             bool
	Run               func(context.Context, json.RawMessage) (any, error)
}

// Toolbox 保存前台工具集合。
type Toolbox struct{ tools map[string]Tool }

// NewToolbox 创建与前台服务绑定的工具集合。
func NewToolbox(d *Desk) *Toolbox {
	return newToolbox(d, Inbound{})
}

func newToolbox(d *Desk, in Inbound) *Toolbox {
	t := &Toolbox{tools: map[string]Tool{}}
	add := func(name, description string, write bool, required []string, optional []string, run func(context.Context, map[string]string) (any, error)) {
		props := map[string]any{}
		for _, k := range append(append([]string{}, required...), optional...) {
			props[k] = map[string]string{"type": "string"}
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required})
		tool := Tool{Name: name, Description: description, Write: write, Parameters: schema}
		tool.Run = func(ctx context.Context, raw json.RawMessage) (any, error) {
			a, err := validate(tool, raw)
			if err != nil {
				return nil, err
			}
			return run(ctx, a)
		}
		t.tools[name] = tool
	}
	add("get_status", "查看任务状态", false, nil, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Tasks.ListTasks(c, "")
	})
	add("list_tasks", "列出任务", false, nil, []string{"project_id"}, func(c context.Context, a map[string]string) (any, error) {
		return d.Tasks.ListTasks(c, a["project_id"])
	})
	add("get_task", "读取任务", false, []string{"task_id"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Tasks.GetTask(c, a["task_id"])
	})
	add("list_decisions", "列出决策卡", false, nil, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Decisions.ListDecisions(c, "open", 15)
	})
	add("read_conversation", "读取对话", false, []string{"thread_id"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Feed.Recent(c, a["thread_id"], 12)
	})
	add("read_memory", "读取记忆", false, []string{"id"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Memory.Get(c, d.ProjectPath, a["id"])
	})
	add("search", "搜索记忆", false, []string{"query"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Memory.Search(c, d.ProjectPath, a["query"], 12)
	})
	add("list_projects", "列出项目", false, nil, nil, func(c context.Context, a map[string]string) (any, error) {
		return d.Projects.List(c)
	})
	add("create_task", "创建任务", true, []string{"title"}, []string{"body", "project_id", "priority", "provider", "model"}, func(c context.Context, a map[string]string) (any, error) {
		return d.Tasks.CreateTask(c, CreateTaskInput{Title: a["title"], Body: a["body"], ProjectID: a["project_id"], Priority: a["priority"], Provider: a["provider"], Model: a["model"], Origin: "owner", SourceMessageID: in.MessageID})
	})
	if d.Automations != nil {
		add("create_automation", "创建定时任务：按固定节奏独立运行，结果会写回总管会话。schedule 格式为 interval:秒数、daily:HH:mm、weekdays:HH:mm、weekly:星期数字,HH:mm（0 为周日）或 cron:表达式。check 是可选的只读检查命令，退出码 0 才运行，非 0 跳过本轮。", true, []string{"project_id", "name", "prompt", "schedule"}, []string{"check"}, func(c context.Context, a map[string]string) (any, error) {
			return d.Automations.CreateAutomation(c, AutomationInput{ProjectID: a["project_id"], Name: a["name"], Prompt: a["prompt"], Schedule: a["schedule"], Check: a["check"]})
		})
	}
	add("amend_task", "追加任务消息", true, []string{"task_id", "text"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return nil, d.Tasks.AmendTask(c, a["task_id"], a["text"])
	})
	add("cancel_task", "取消任务", true, []string{"task_id"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return nil, d.Tasks.CancelTask(c, a["task_id"])
	})
	add("set_priority", "设置优先级", true, []string{"task_id", "priority"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return nil, d.Tasks.SetPriority(c, a["task_id"], a["priority"])
	})
	add("reassign_task", "更换执行来源和模型", true, []string{"task_id", "provider", "model"}, nil, func(c context.Context, a map[string]string) (any, error) {
		task, err := d.Tasks.GetTask(c, a["task_id"])
		if err != nil {
			return nil, err
		}
		if task.Status != "todo" && task.Status != "waiting" {
			return nil, errors.New("只能更改待办或等待中的任务")
		}
		return nil, d.Tasks.ReassignTask(c, a["task_id"], a["provider"], a["model"])
	})
	memory := func(edit bool) func(context.Context, map[string]string) (any, error) {
		return func(c context.Context, a map[string]string) (any, error) {
			req := ProposeRequest{ProjectPath: d.ProjectPath, Content: a["content"], UserRequested: true}
			if edit {
				req.TargetMemoryID = a["id"]
			}
			return d.Memory.Propose(c, req)
		}
	}
	add("remember", "记住用户要求", true, []string{"content"}, nil, memory(false))
	add("edit_memory", "修改记忆", true, []string{"id", "content"}, nil, memory(true))
	add("answer_decision", "回答决策卡", true, []string{"id", "answer"}, nil, func(c context.Context, a map[string]string) (any, error) {
		return nil, d.answerDecision(c, a["id"], a["answer"], "frontdesk")
	})
	return t
}

// Specs 返回按名称排序的模型工具定义。
func (t *Toolbox) Specs() []llm.ToolSpec {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.tools))
	for n := range t.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]llm.ToolSpec, 0, len(names))
	for _, n := range names {
		x := t.tools[n]
		out = append(out, llm.ToolSpec{Name: x.Name, Description: x.Description, Parameters: x.Parameters})
	}
	return out
}

// Get 按名称查找工具。
func (t *Toolbox) Get(name string) (Tool, bool) {
	if t == nil {
		return Tool{}, false
	}
	v, ok := t.tools[name]
	return v, ok
}

// ErrInvalidArguments 表示工具参数无效。
var ErrInvalidArguments = errors.New("工具参数错误")

func validate(t Tool, raw json.RawMessage) (map[string]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w：需要 JSON 对象", ErrInvalidArguments)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(t.Parameters, &schema); err != nil {
		return nil, fmt.Errorf("%w：无效 schema", ErrInvalidArguments)
	}
	args := map[string]string{}
	for k, v := range obj {
		if _, ok := schema.Properties[k]; !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || string(v) == "null" {
			return nil, fmt.Errorf("%w：%s 必须是字符串", ErrInvalidArguments, k)
		}
		args[k] = s
	}
	for _, k := range schema.Required {
		if strings.TrimSpace(args[k]) == "" {
			if t.Name == "answer_decision" && k == "answer" {
				return nil, errors.New("请给出答案")
			}
			return nil, fmt.Errorf("%w：缺少 %s", ErrInvalidArguments, k)
		}
	}
	return args, nil
}
