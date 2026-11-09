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
	taskID := in.ReplyTo
	if taskID != "" {
		if _, err := d.Tasks.GetTask(ctx, taskID); err != nil {
			return err
		}
	} else if !strings.ContainsAny(in.Text, "\r\n") && len([]rune(in.Text)) <= 40 {
		task, mentionedAt, err := d.Tasks.RecentTask(ctx, in.ThreadID)
		now := d.now()
		if err == nil && task.ID != "" && !mentionedAt.After(now) && now.Sub(mentionedAt) <= 10*time.Minute {
			taskID = task.ID
		}
	}
	if taskID != "" {
		if err := d.Tasks.AmendTask(ctx, taskID, in.Text); err != nil {
			return err
		}
	} else {
		title := truncate(strings.TrimSpace(strings.SplitN(in.Text, "\n", 2)[0]), 80)
		if _, err := d.Tasks.CreateTask(ctx, CreateTaskInput{Title: title, Body: in.Text, Origin: "owner"}); err != nil {
			return err
		}
	}
	if len(reasons) > 0 && errors.Is(reasons[0], ErrNoModel) {
		d.reply(ctx, in.ThreadID, "没有可用模型，请在主会话选择模型。")
	}
	return nil
}
