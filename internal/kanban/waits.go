package kanban

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"mcode/internal/agent/gate"
)

const MaxWaitWakes = 6

func BackoffMinutes(n int) int {
	if n < 0 {
		n = 0
	}
	if n > 3 {
		n = 3
	}
	return 5 * (1 << n)
}

func (s *Service) CheckWaits(ctx context.Context, now time.Time) error {
	tasks, err := s.opts.Store.ListTasks(ctx, "")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status != string(TaskStatusWaiting) {
			continue
		}
		meta, err := ParseTaskMeta(t.Meta)
		if err != nil {
			return err
		}
		if meta.WaitKind != "machine" {
			continue
		}
		wake, err := time.Parse(time.RFC3339, meta.WakeAt)
		if err != nil {
			return err
		}
		if wake.After(now) {
			continue
		}
		question := ""
		if !gate.IsReadOnlyCommand(meta.WaitCheck) {
			question = "等待检查命令不是只读命令，是否继续"
		} else {
			if t.ProjectID == nil || s.opts.Projects == nil {
				return fmt.Errorf("等待任务 %s 缺少项目目录", t.ID)
			}
			root, err := s.opts.Projects.ProjectRoot(ctx, *t.ProjectID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(root) == "" {
				return fmt.Errorf("等待任务 %s 的项目目录为空", t.ID)
			}
			checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			cmd := exec.CommandContext(checkCtx, "sh", "-c", meta.WaitCheck)
			cmd.Dir = root
			err = cmd.Run()
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				t.Status = string(TaskStatusTodo)
				meta.WaitKind, meta.WaitCheck, meta.WakeAt, meta.WaitBase, meta.Wakes = "", "", "", "", 0
			} else {
				meta.Wakes++
				meta.WakeAt = now.Add(time.Duration(BackoffMinutes(meta.Wakes)) * time.Minute).Format(time.RFC3339)
				if meta.Wakes > MaxWaitWakes {
					question = "等待超时：" + meta.Summary
				}
			}
		}
		t.Meta = EncodeTaskMeta(meta)
		if question != "" {
			res := ExecutorResult{Status: ResultNeedsDecision, Summary: meta.Summary, Question: question, Options: []DecisionOption{{Key: "A", Text: "继续等"}, {Key: "B", Text: "停下"}}, Recommendation: "A"}
			if err := s.applyExecutorResult(ctx, &t, res, now); err != nil {
				return err
			}
			s.markReceiptBlocked(t.ID)
		}
		if _, err := s.opts.Store.UpdateTask(ctx, t, t.Revision); err != nil {
			return err
		}
		s.revision.Add(1)
		s.notifyChange()
	}
	return nil
}

func (s *Service) WakeParents(ctx context.Context) error {
	tasks, err := s.opts.Store.ListTasks(ctx, "")
	if err != nil {
		return err
	}
	byID := make(map[string]struct{ title, status, summary string }, len(tasks))
	for _, t := range tasks {
		meta, err := ParseTaskMeta(t.Meta)
		if err != nil {
			return err
		}
		byID[t.ID] = struct{ title, status, summary string }{t.Title, t.Status, meta.Summary}
	}
	for _, t := range tasks {
		if t.Status != string(TaskStatusWaiting) {
			continue
		}
		meta, err := ParseTaskMeta(t.Meta)
		if err != nil {
			return err
		}
		if meta.WaitKind != "children" || len(meta.Children) == 0 {
			continue
		}
		lines := []string{"子任务结果"}
		ready := true
		for _, id := range meta.Children {
			child, ok := byID[id]
			if !ok || child.status != string(TaskStatusDone) && child.status != string(TaskStatusFailed) && child.status != string(TaskStatusCancelled) {
				ready = false
				break
			}
			lines = append(lines, fmt.Sprintf("- %s（%s）：%s", child.title, child.status, child.summary))
		}
		if !ready {
			continue
		}
		t.Notes = strings.TrimSpace(t.Notes + "\n\n" + strings.Join(lines, "\n"))
		t.Status = string(TaskStatusTodo)
		meta.Children, meta.WaitKind = nil, ""
		t.Meta = EncodeTaskMeta(meta)
		if _, err := s.opts.Store.UpdateTask(ctx, t, t.Revision); err != nil {
			return err
		}
		s.revision.Add(1)
		s.notifyChange()
	}
	return nil
}
