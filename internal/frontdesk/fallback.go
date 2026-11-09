package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

func errorSummary(err error) string {
	if errors.Is(err, ErrNoModel) {
		return "没有可用模型，可在主会话选择模型"
	}
	text := err.Error()
	if start := strings.IndexByte(text, '{'); start >= 0 {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.NewDecoder(strings.NewReader(text[start:])).Decode(&body) == nil {
			if body.Error.Message != "" {
				text = body.Error.Message
			} else if body.Message != "" {
				text = body.Message
			}
		}
	}
	return truncate(strings.Join(strings.Fields(text), " "), 60)
}

func (d *Desk) reply(ctx context.Context, threadID, text string) {
	if d.Feed == nil {
		log.Printf("frontdesk: 回复存储不可用")
		return
	}
	if err := d.Feed.AppendAssistant(ctx, threadID, KindStewardReply, text); err != nil {
		log.Printf("frontdesk: 写入回复失败：%v", err)
	}
}

func (d *Desk) tryCLIFallback(ctx context.Context, in Inbound, snap Snapshot) (bool, error) {
	if d.CLIFallback && d.Generator != nil {
		return d.cliFallback(ctx, in, snap)
	}
	return false, nil
}

func (d *Desk) fallback(ctx context.Context, in Inbound, snap Snapshot, reasons ...error) error {
	if ok, err := d.tryCLIFallback(ctx, in, snap); ok || err != nil {
		return err
	}
	if d.Tasks == nil {
		return errors.New("frontdesk: 任务服务不可用")
	}
	prefix := ""
	if len(reasons) > 0 && reasons[0] != nil {
		prefix = "前台模型不可用：" + errorSummary(reasons[0]) + "，"
	}
	if !strings.ContainsAny(in.Text, "\r\n") && len([]rune(in.Text)) <= 40 {
		if task, err := d.Tasks.RecentTask(ctx); err == nil && task.ID != "" && !task.UpdatedAt.After(d.now()) && d.now().Sub(task.UpdatedAt) <= 10*time.Minute {
			if err := d.Tasks.AmendTask(ctx, task.ID, in.Text); err != nil {
				return err
			}
			text := "已补充到上一个任务"
			if task.Title != "" {
				text = "已补充到「" + task.Title + "」"
			}
			d.reply(ctx, in.ThreadID, prefix+text)
			return nil
		}
	}
	title := strings.TrimSpace(strings.SplitN(in.Text, "\n", 2)[0])
	title = truncate(title, 80)
	task, err := d.Tasks.CreateTask(ctx, CreateTaskInput{Title: title, Body: in.Text, Origin: "owner"})
	if err != nil {
		return err
	}
	if task.Title != "" {
		title = task.Title
	}
	d.reply(ctx, in.ThreadID, prefix+"已建任务「"+title+"」")
	return nil
}
