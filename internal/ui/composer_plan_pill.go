package mcodeui

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// isTodoToolMessage 判断一条消息是否为工具调用消息（待办清单只会出现在工具消息里）。
func isTodoToolMessage(m Message) bool {
	return m.Role == "tool" || m.Kind == "tool"
}

var claudeTaskIDPattern = regexp.MustCompile(`Task #([0-9]+)`)

type claudeTaskAccumulator struct {
	items         []todoItem
	ids           []string
	inProgressMsg []int
	created       int
}

func (a *claudeTaskAccumulator) add(m Message) bool {
	return a.addAt(m, -1)
}

func (a *claudeTaskAccumulator) addAt(m Message, msgIdx int) bool {
	if !isTodoToolMessage(m) {
		return false
	}
	create := strings.EqualFold(m.ToolName, "TaskCreate")
	if !create && !strings.EqualFold(m.ToolName, "TaskUpdate") {
		return false
	}
	var input struct {
		TaskID  string  `json:"taskId"`
		Subject *string `json:"subject"`
		Status  string  `json:"status"`
	}
	if json.Unmarshal([]byte(m.ToolInput), &input) != nil {
		return true
	}
	if create {
		a.created++
		id := strconv.Itoa(a.created)
		if _, output, ok := strings.Cut(m.Text, "\n"); ok {
			if match := claudeTaskIDPattern.FindStringSubmatch(output); match != nil {
				id = match[1]
			}
		}
		text := ""
		if input.Subject != nil {
			text = *input.Subject
		}
		a.ids = append(a.ids, id)
		a.items = append(a.items, todoItem{Text: text, Status: "pending"})
		a.inProgressMsg = append(a.inProgressMsg, -1)
		return true
	}
	for j, id := range a.ids {
		if id != input.TaskID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(input.Status), "deleted") {
			a.ids = append(a.ids[:j], a.ids[j+1:]...)
			a.items = append(a.items[:j], a.items[j+1:]...)
			a.inProgressMsg = append(a.inProgressMsg[:j], a.inProgressMsg[j+1:]...)
			break
		}
		if input.Subject != nil {
			a.items[j].Text = *input.Subject
		}
		if input.Status != "" {
			st := normalizeTodoStatus(input.Status)
			a.items[j].Status = st
			if st == "in_progress" {
				a.inProgressMsg[j] = msgIdx
			} else {
				a.inProgressMsg[j] = -1
			}
		}
		break
	}
	return true
}

// claudeTaskPlan 按创建顺序聚合 Claude 的增量任务消息。
func claudeTaskPlan(msgs []Message) ([]todoItem, int) {
	var tasks claudeTaskAccumulator
	last := -1
	for i, m := range msgs {
		if tasks.addAt(m, i) {
			last = i
		}
	}
	return tasks.items, last
}

// collapseTodoMessages 保留每轮各类待办的最后一条，Claude 任务跨轮累计。
func collapseTodoMessages(messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	keep := make([]bool, 0, len(messages))
	var tasks claudeTaskAccumulator
	lastPlan, lastTask := -1, -1
	flushTask := func() {
		if lastTask < 0 {
			return
		}
		if len(tasks.items) == 0 {
			keep[lastTask] = false
			return
		}
		todos := make([]map[string]string, 0, len(tasks.items))
		for _, item := range tasks.items {
			todos = append(todos, map[string]string{"content": item.Text, "status": item.Status})
		}
		input, _ := json.Marshal(map[string]any{"todos": todos})
		result[lastTask].ToolInput = string(input)
	}
	for _, m := range messages {
		if m.Role == "user" {
			flushTask()
			lastPlan, lastTask = -1, -1
		}
		result = append(result, m)
		keep = append(keep, true)
		idx := len(result) - 1
		if tasks.add(m) {
			if lastTask >= 0 {
				keep[lastTask] = false
			}
			lastTask = idx
		} else if isTodoToolMessage(m) {
			if _, ok := todoItemsFromMessage(m); ok {
				if lastPlan >= 0 {
					keep[lastPlan] = false
				}
				lastPlan = idx
			}
		}
	}
	flushTask()
	filtered := make([]Message, 0, len(result))
	for i, m := range result {
		if keep[i] {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// latestTodoPlan 返回最新的整份清单或增量任务聚合结果及其下标。
func latestTodoPlan(msgs []Message) ([]todoItem, int) {
	tasks, taskIdx := claudeTaskPlan(msgs)
	for i := len(msgs) - 1; i > taskIdx; i-- {
		m := msgs[i]
		if !isTodoToolMessage(m) {
			continue
		}
		if parsed, ok := todoItemsFromMessage(m); ok {
			return parsed, i
		}
	}
	return tasks, taskIdx
}
