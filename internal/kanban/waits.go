package kanban

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mcode/internal/agent/gate"
	"mcode/internal/procattr"
	"mcode/internal/store"
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
	var firstErr error
	for _, t := range tasks {
		if t.Status != string(TaskStatusWaiting) {
			continue
		}
		meta, err := ParseTaskMeta(t.Meta)
		if err != nil {
			failure := fmt.Errorf("等待任务 %s 元数据无效: %w", t.ID, err)
			if updateErr := s.recordWaitFailure(ctx, &t, failure); updateErr != nil && firstErr == nil {
				firstErr = updateErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if meta.WaitKind != "machine" && meta.WaitKind != "human" {
			continue
		}
		if meta.WaitKind == "human" && strings.TrimSpace(meta.WakeAt) == "" {
			continue
		}
		wake, err := time.Parse(time.RFC3339, meta.WakeAt)
		if err != nil {
			failure := fmt.Errorf("等待任务 %s 的 WakeAt 无效: %w", t.ID, err)
			if updateErr := s.recordWaitFailure(ctx, &t, failure); updateErr != nil && firstErr == nil {
				firstErr = updateErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if wake.After(now) {
			continue
		}
		question := ""
		if meta.WaitKind == "human" {
			question = "等待的人工处理已到期，是否继续等"
		} else if !gate.IsReadOnlyCommand(meta.WaitCheck) {
			question = "等待检查命令不是只读命令，是否继续"
		} else {
			if t.ProjectID == nil || s.opts.Projects == nil {
				failure := fmt.Errorf("等待任务 %s 缺少项目目录", t.ID)
				if updateErr := s.recordWaitFailure(ctx, &t, failure); updateErr != nil && firstErr == nil {
					firstErr = updateErr
				}
				if firstErr == nil {
					firstErr = failure
				}
				continue
			}
			root, err := s.opts.Projects.ProjectRoot(ctx, *t.ProjectID)
			if err != nil {
				failure := fmt.Errorf("等待任务 %s 获取项目目录失败: %w", t.ID, err)
				if updateErr := s.recordWaitFailure(ctx, &t, failure); updateErr != nil && firstErr == nil {
					firstErr = updateErr
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if strings.TrimSpace(root) == "" {
				failure := fmt.Errorf("等待任务 %s 的项目目录为空", t.ID)
				if updateErr := s.recordWaitFailure(ctx, &t, failure); updateErr != nil && firstErr == nil {
					firstErr = updateErr
				}
				if firstErr == nil {
					firstErr = failure
				}
				continue
			}
			checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			cmd := procattr.ShellCommand(checkCtx, meta.WaitCheck)
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
		var res ExecutorResult
		if question != "" {
			res = ExecutorResult{Status: ResultNeedsDecision, Summary: meta.Summary, Question: question, Options: []DecisionOption{{Key: "A", Text: "继续等"}, {Key: "B", Text: "停下"}}, Recommendation: "A"}
			if err := s.applyExecutorResult(ctx, &t, res, now); err != nil {
				return err
			}
			s.markReceiptBlocked(t.ID)
		}
		updated, err := s.opts.Store.UpdateTask(ctx, t, t.Revision)
		if err != nil {
			return err
		}
		if res.Status != "" {
			s.notifyResult(ctx, updated, res)
		}
		s.revision.Add(1)
		s.notifyChange()
	}
	return firstErr
}

func (s *Service) recordWaitFailure(ctx context.Context, t *store.Task, failure error) error {
	reason := failure.Error()
	if t.LastFailureReason != nil && *t.LastFailureReason == reason {
		return nil
	}
	t.LastFailureReason = &reason
	updated, err := s.opts.Store.UpdateTask(ctx, *t, t.Revision)
	if err != nil {
		return err
	}
	*t = updated
	s.revision.Add(1)
	s.notifyChange()
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
		if meta.WaitKind != "children" {
			continue
		}
		lines := []string{"子任务结果"}
		ready := true
		for _, id := range meta.Children {
			child, ok := byID[id]
			if !ok || child.status != string(TaskStatusDone) && child.status != string(TaskStatusFailed) && child.status != string(TaskStatusCancelled) && child.status != string(TaskStatusRejected) {
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
