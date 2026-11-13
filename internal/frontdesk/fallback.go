package frontdesk

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"mcode/internal/store"
)

// createdTaskReply 是创建任务但模型没有给文字时的前台回复，任务卡片会附在它下方。
const createdTaskReply = "已创建任务"

func (d *Desk) reply(ctx context.Context, threadID, text string) {
	if d.Feed == nil {
		log.Printf("frontdesk: 回复存储不可用")
		return
	}
	if err := d.Feed.AppendAssistant(ctx, threadID, KindStewardReply, text); err != nil {
		log.Printf("frontdesk: 写入回复失败：%v", err)
	}
}

func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (d *Desk) tryCLIFallback(ctx context.Context, in Inbound, snap Snapshot) (bool, error) {
	enabled, opts := d.CLIFallback, d.CLIOptions
	if d.Settings != nil {
		enabled, opts.Provider, opts.Model = d.Settings()
	}
	if enabled && d.Generator != nil {
		return d.cliFallback(ctx, in, snap, opts)
	}
	return false, nil
}

func (d *Desk) fallback(ctx context.Context, in Inbound, snap Snapshot, reasons ...error) error {
	if ok, err := d.tryCLIFallback(ctx, in, snap); ok || err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d.Tasks == nil {
		if len(reasons) > 0 && errors.Is(reasons[0], errModelUnavailable) {
			return reasons[0]
		}
		return errors.New("frontdesk: 任务服务不可用")
	}
	taskID := in.ReplyTo
	if taskID != "" {
		if _, err := d.Tasks.GetTask(ctx, taskID); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			d.reply(ctx, in.ThreadID, "这个任务已删除")
			return nil
		}
	}
	if taskID == "" && !strings.ContainsAny(in.Text, "\r\n") && len([]rune(in.Text)) <= 40 {
		task, mentionedAt, err := d.Tasks.RecentTask(ctx, in.ThreadID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		now := d.now()
		if err == nil && task.ID != "" && !mentionedAt.After(now) && now.Sub(mentionedAt) <= 10*time.Minute {
			taskID = task.ID
		}
	}
	if taskID != "" {
		if err := d.Tasks.AmendTask(ctx, taskID, in.Text, in.MessageID); err != nil {
			return err
		}
	} else {
		title := clipRunes(strings.TrimSpace(strings.SplitN(in.Text, "\n", 2)[0]), 80)
		if _, err := d.Tasks.CreateTask(ctx, CreateTaskInput{Title: title, Body: in.Text, Origin: "owner", SourceMessageID: in.MessageID}); err != nil {
			return err
		}
	}
	if len(reasons) > 0 && errors.Is(reasons[0], ErrNoModel) {
		d.reply(ctx, in.ThreadID, "没有可用模型，请在主会话选择模型。")
	}
	return nil
}
