package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FinalizeInterruptedTurn 在同一事务内把该轮次下 running 的工具消息收尾为 failed，
// 并把轮次标记为 interrupted；任一步失败整体回滚，轮次保持原状，下次启动重试。
// 已处于终态的轮次直接返回 nil，重复调用幂等。
func (s *Store) FinalizeInterruptedTurn(ctx context.Context, turnID string, recoveryAt, toolCompletedAt time.Time, errorMessage, toolNotice string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var status string
	var startedAtRaw string
	var threadID string
	err = tx.QueryRowContext(ctx, "SELECT status, started_at, thread_id FROM turns WHERE id = ?", turnID).Scan(&status, &startedAtRaw, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read turn %s status: %w", turnID, err)
	}
	if status != "running" && status != "starting" {
		return nil
	}
	var startedAt time.Time
	if parsed, parseErr := parseTime(startedAtRaw); parseErr == nil {
		startedAt = parsed
	}
	if startedAt.After(recoveryAt) {
		startedAt = recoveryAt
	}
	lastActivity := startedAt
	considerTime := func(t time.Time) {
		if t.After(recoveryAt) {
			t = recoveryAt
		}
		if t.After(lastActivity) {
			lastActivity = t
		}
	}
	considerActivity := func(raw string) {
		if parsed, parseErr := parseTime(raw); parseErr == nil {
			considerTime(parsed)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT created_at, tool_completed_at FROM messages WHERE turn_id = ?", turnID)
	if err != nil {
		return fmt.Errorf("query turn %s activity: %w", turnID, err)
	}
	for rows.Next() {
		var createdAt string
		var completed sql.NullString
		if err := rows.Scan(&createdAt, &completed); err != nil {
			rows.Close()
			return err
		}
		considerActivity(createdAt)
		if completed.Valid {
			considerActivity(completed.String)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if archived, found, archiveErr := loadThreadArchive(ctx, tx, threadID); archiveErr != nil {
		return archiveErr
	} else if found {
		for _, msg := range archived {
			if msg.TurnID == turnID {
				considerTime(msg.CreatedAt)
				if msg.ToolCompletedAt != nil {
					considerActivity(*msg.ToolCompletedAt)
				}
			}
		}
	}
	if lastActivity.Before(startedAt) {
		lastActivity = startedAt
	}

	// 先把行全部读进内存并关闭游标，再逐条写回：SQLite 单连接下边读边写会锁住游标。
	rows, err = tx.QueryContext(ctx, "SELECT "+messageSelectCols+" FROM messages WHERE turn_id = ? AND role = 'tool' AND COALESCE(tool_status, '') = 'running' ORDER BY seq", turnID)
	if err != nil {
		return fmt.Errorf("query running tool messages for turn %s: %w", turnID, err)
	}
	var toolMsgs []Message
	for rows.Next() {
		m, err := scanFullMessage(rows)
		if err != nil {
			rows.Close()
			return err
		}
		toolMsgs = append(toolMsgs, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate running tool messages for turn %s: %w", turnID, err)
	}
	rows.Close()

	completedAtStr := formatTime(toolCompletedAt)
	turnCompletedAtStr := formatTime(lastActivity)
	for _, m := range toolMsgs {
		m.Text = appendToolNotice(m.Text, toolNotice)
		failed := "failed"
		m.ToolStatus = &failed
		m.ToolCompletedAt = &completedAtStr
		if err := s.updateMessageTx(ctx, tx, m); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
	UPDATE turns SET status = 'interrupted', completed_at = ?,
		error_message = CASE WHEN COALESCE(error_message, '') = '' THEN ? ELSE error_message END
	WHERE id = ? AND status IN ('running', 'starting')
	`, turnCompletedAtStr, errorMessage, turnID); err != nil {
		return fmt.Errorf("finalize turn %s: %w", turnID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// FinalizeBackgroundToolMessages 把所有 tool_status='background' 的工具消息收尾为 failed，
// 写入 completedAt、并用 appendToolNotice 追加提示，返回处理条数。
// background 表示 provider 托管的后台任务（如 Bash run_in_background）：它属于已经结束的回合，
// 所以轮次收尾（FinalizeInterruptedTurn）只看 running，不会覆盖它们；而宿主启动后这些任务
// 必然已随 provider 进程消失，必须单独收敛，否则界面上会永久显示为运行中。
// 单事务处理，任一条失败整体回滚；重复调用幂等（第二次查不到 background 行，返回 0）。
func (s *Store) FinalizeBackgroundToolMessages(ctx context.Context, completedAt time.Time, toolNotice string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 先把行全部读进内存并关闭游标，再逐条写回：SQLite 单连接下边读边写会锁住游标。
	rows, err := tx.QueryContext(ctx, "SELECT "+messageSelectCols+" FROM messages WHERE role = 'tool' AND COALESCE(tool_status, '') = 'background' ORDER BY thread_id, seq")
	if err != nil {
		return 0, fmt.Errorf("query background tool messages: %w", err)
	}
	var toolMsgs []Message
	for rows.Next() {
		m, err := scanFullMessage(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		toolMsgs = append(toolMsgs, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate background tool messages: %w", err)
	}
	rows.Close()

	completedAtStr := formatTime(completedAt)
	for _, m := range toolMsgs {
		m.Text = appendToolNotice(m.Text, toolNotice)
		failed := "failed"
		m.ToolStatus = &failed
		m.ToolCompletedAt = &completedAtStr
		if err := s.updateMessageTx(ctx, tx, m); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return len(toolMsgs), nil
}

// ListOrphanRunningToolMessages 返回 tool_status='running' 且 turn_id 不在 activeTurnIDs 里的工具消息；
// SQL 先查 running，再在 Go 里剔除 active。不依赖 turns.status，因此历史 interrupted turn 下的残留也能被发现。
func (s *Store) ListOrphanRunningToolMessages(ctx context.Context, activeTurnIDs map[string]struct{}) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT `+messageSelectCols+`
	FROM messages
	WHERE role = 'tool' AND COALESCE(tool_status, '') = 'running'
	ORDER BY thread_id, seq
	`)
	if err != nil {
		return nil, fmt.Errorf("query orphan running tool messages: %w", err)
	}
	defer rows.Close()

	msgs := make([]Message, 0)
	for rows.Next() {
		m, err := scanFullMessage(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := activeTurnIDs[m.TurnID]; ok {
			continue
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphan running tool messages: %w", err)
	}
	return msgs, nil
}

// FinalizeOrphanToolMessage 只收尾单条仍为 running 的工具消息；已不是 running 时返回 false, nil。
// 它不负责 turn 状态，只用于历史遗留、不属于任何未收尾轮次的工具消息。
func (s *Store) FinalizeOrphanToolMessage(ctx context.Context, messageID, toolNotice string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	m, err := scanFullMessage(tx.QueryRowContext(ctx, "SELECT "+messageSelectCols+" FROM messages WHERE id = ? AND role = 'tool' AND COALESCE(tool_status, '') = 'running'", messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	m.Text = appendToolNotice(m.Text, toolNotice)
	failed := "failed"
	m.ToolStatus = &failed
	completedAtStr := formatTime(time.Now().UTC())
	m.ToolCompletedAt = &completedAtStr
	if err := s.updateMessageTx(ctx, tx, m); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit orphan tool message %s: %w", messageID, err)
	}
	return true, nil
}

// scanFullMessage 读取一行消息并补齐 CreatedAt 与 Media，读取方式与 ListMessages 一致。
func scanFullMessage(row rowScanner) (Message, error) {
	m, createdAtStr, mediaStr, err := scanMessageRow(row)
	if err != nil {
		return Message{}, err
	}
	if m.CreatedAt, err = parseTime(createdAtStr); err != nil {
		return Message{}, fmt.Errorf("parse message %s created_at: %w", m.ID, err)
	}
	if m.Media, err = decodeJSONList[MediaRef](mediaStr); err != nil {
		return Message{}, fmt.Errorf("decode media for message %s: %w", m.ID, err)
	}
	return m, nil
}

// appendToolNotice 幂等追加中断说明；正文为空时只写说明，避免留下前导空行。
func appendToolNotice(text, notice string) string {
	if notice == "" || strings.Contains(text, notice) {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return notice
	}
	return text + "\n\n" + notice
}
