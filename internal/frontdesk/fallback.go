package frontdesk

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (d *Desk) fallback(ctx context.Context, in Inbound, snap Snapshot) error {
	if d.CLIFallback && d.Generator != nil {
		ok, err := d.cliFallback(ctx, in, snap)
		if ok || err != nil {
			return err
		}
	}
	if d.Tasks == nil {
		return errors.New("frontdesk: 任务服务不可用")
	}
	if !strings.ContainsAny(in.Text, "\r\n") && len([]rune(in.Text)) <= 40 {
		if task, err := d.Tasks.RecentTask(ctx); err == nil && task.ID != "" && !task.UpdatedAt.After(d.now()) && d.now().Sub(task.UpdatedAt) <= 10*time.Minute {
			return d.Tasks.AmendTask(ctx, task.ID, in.Text)
		}
	}
	title := strings.TrimSpace(strings.SplitN(in.Text, "\n", 2)[0])
	title = truncate(title, 80)
	_, err := d.Tasks.CreateTask(ctx, CreateTaskInput{Title: title, Body: in.Text, Origin: "owner"})
	return err
}
