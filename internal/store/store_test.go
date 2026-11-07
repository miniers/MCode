package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationIdempotency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration_test.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第一次 Open 失败: %v", err)
	}

	var v1 int
	if err := s1.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&v1); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if v1 != len(migrations) {
		t.Fatalf("期望 user_version 为 %d，实际为 %d", len(migrations), v1)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重复打开同一数据库文件
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第二次 Open 失败: %v", err)
	}
	defer s2.Close()

	var v2 int
	if err := s2.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&v2); err != nil {
		t.Fatalf("二次查询 user_version 失败: %v", err)
	}
	if v2 != v1 {
		t.Fatalf("期望 user_version 保持 %d，实际变更为 %d", v1, v2)
	}
}

func TestCascadeDelete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	proj := Project{
		ID:        "p1",
		Name:      "Project 1",
		Path:      "/path/to/p1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "Thread 1",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet",
		SessionID: "sess-1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	msg := Message{
		ID:        "m1",
		ThreadID:  "t1",
		Role:      "user",
		Kind:      "text",
		Text:      "hello world",
		TurnIndex: 1,
		TurnID:    "turn-1",
		CreatedAt: now,
	}
	if _, err := s.AppendMessage(ctx, msg); err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}

	if err := s.UpsertTurnUsage(ctx, TurnUsage{
		ThreadID:     "t1",
		TurnIndex:    1,
		InputTokens:  100,
		OutputTokens: 50,
		CachedTokens: 20,
	}); err != nil {
		t.Fatalf("UpsertTurnUsage 失败: %v", err)
	}

	// 确认数据已写入
	if _, err := s.GetThread(ctx, "t1"); err != nil {
		t.Fatalf("未能查到新创建的 thread: %v", err)
	}

	// 删除项目，验证外键级联删除
	if _, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", "p1"); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}

	// 验证线程已被级联删除
	_, err = s.GetThread(ctx, "t1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望线程被级联删除返回 ErrNotFound，实际为: %v", err)
	}

	// 验证消息已被级联删除
	var msgCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id = ?", "m1").Scan(&msgCount); err != nil {
		t.Fatalf("查询消息数量失败: %v", err)
	}
	if msgCount != 0 {
		t.Fatalf("期望消息被级联删除，但仍存在 %d 条", msgCount)
	}

	// 验证用量已被级联删除
	var usageCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM turn_usage WHERE thread_id = ?", "t1").Scan(&usageCount); err != nil {
		t.Fatalf("查询 turn_usage 数量失败: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("期望 turn_usage 被级联删除，但仍存在 %d 条", usageCount)
	}
}

func TestAppendMessageSeqIncrement(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "seq_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	for i := 1; i <= 3; i++ {
		msgTime := now.Add(time.Duration(i) * time.Minute)
		m, err := s.AppendMessage(ctx, Message{
			ID:        fmt.Sprintf("m%d", i),
			ThreadID:  "t1",
			Role:      "user",
			Kind:      "text",
			Text:      fmt.Sprintf("msg %d", i),
			TurnIndex: i,
			CreatedAt: msgTime,
		})
		if err != nil {
			t.Fatalf("AppendMessage m%d 失败: %v", i, err)
		}
		if m.Seq != int64(i) {
			t.Fatalf("期望 seq 为 %d，实际为 %d", i, m.Seq)
		}

		th, err := s.GetThread(ctx, "t1")
		if err != nil {
			t.Fatalf("GetThread 失败: %v", err)
		}
		if th.UpdatedAt.Format(time.RFC3339Nano) != msgTime.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("期望 thread updated_at 为 %v，实际为 %v", msgTime, th.UpdatedAt)
		}
	}
}

func TestUpdateMessage(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "update_msg_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	_ = s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now})

	m, err := s.AppendMessage(ctx, Message{
		ID:        "m1",
		ThreadID:  "t1",
		Role:      "assistant",
		Kind:      "thinking",
		Text:      "Initial stream chunk",
		TurnIndex: 1,
		IsFinal:   false,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}

	// 模拟流式更新
	err = s.UpdateMessage(ctx, Message{
		ID:      m.ID,
		Text:    "Final response complete",
		Kind:    "text",
		IsFinal: true,
	})
	if err != nil {
		t.Fatalf("UpdateMessage 失败: %v", err)
	}

	msgs, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("期望 1 条消息，实际为 %d 条", len(msgs))
	}
	if msgs[0].Text != "Final response complete" {
		t.Fatalf("期望 text 更新为 'Final response complete'，实际为 %s", msgs[0].Text)
	}
	if msgs[0].Kind != "text" {
		t.Fatalf("期望 kind 更新为 'text'，实际为 %s", msgs[0].Kind)
	}
	if !msgs[0].IsFinal {
		t.Fatalf("期望 is_final 更新为 true")
	}

	// 更新不存在的消息应返回 ErrNotFound
	err = s.UpdateMessage(ctx, Message{ID: "non-existent", Text: "abc"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望更新不存在的消息返回 ErrNotFound，实际为 %v", err)
	}
}

func TestListMessagesPagination(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pagination_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	_ = s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now})

	// 写入 5 条消息，seq 为 1, 2, 3, 4, 5
	for i := 1; i <= 5; i++ {
		_, err := s.AppendMessage(ctx, Message{
			ID:        fmt.Sprintf("m%d", i),
			ThreadID:  "t1",
			Role:      "user",
			Kind:      "text",
			Text:      fmt.Sprintf("content %d", i),
			TurnIndex: i,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("AppendMessage %d 失败: %v", i, err)
		}
	}

	// 1. 获取最新 2 条 (seq 4, 5)
	p1, hasMore, err := s.ListMessages(ctx, "t1", 0, 2)
	if err != nil {
		t.Fatalf("ListMessages 第一页失败: %v", err)
	}
	if !hasMore {
		t.Fatalf("第一页期望 hasMore 为 true")
	}
	if len(p1) != 2 || p1[0].Seq != 4 || p1[1].Seq != 5 {
		t.Fatalf("第一页内容不符，期望 seq [4, 5]，实际为 %+v", p1)
	}

	// 2. 取 seq < 4 的前 2 条 (seq 2, 3)
	p2, hasMore, err := s.ListMessages(ctx, "t1", p1[0].Seq, 2)
	if err != nil {
		t.Fatalf("ListMessages 第二页失败: %v", err)
	}
	if !hasMore {
		t.Fatalf("第二页期望 hasMore 为 true")
	}
	if len(p2) != 2 || p2[0].Seq != 2 || p2[1].Seq != 3 {
		t.Fatalf("第二页内容不符，期望 seq [2, 3]，实际为 %+v", p2)
	}

	// 3. 取 seq < 2 的消息 (seq 1)
	p3, hasMore, err := s.ListMessages(ctx, "t1", p2[0].Seq, 2)
	if err != nil {
		t.Fatalf("ListMessages 第三页失败: %v", err)
	}
	if hasMore {
		t.Fatalf("第三页期望 hasMore 为 false")
	}
	if len(p3) != 1 || p3[0].Seq != 1 {
		t.Fatalf("第三页内容不符，期望 seq [1]，实际为 %+v", p3)
	}

	// 4. 边界：取 seq < 1 的消息，应为空
	p4, hasMore, err := s.ListMessages(ctx, "t1", p3[0].Seq, 2)
	if err != nil {
		t.Fatalf("ListMessages 第四页失败: %v", err)
	}
	if hasMore {
		t.Fatalf("第四页期望 hasMore 为 false")
	}
	if len(p4) != 0 {
		t.Fatalf("第四页期望为空，实际有 %d 条", len(p4))
	}

	// 5. 空线程查询
	emptyList, hasMore, err := s.ListMessages(ctx, "non-existent-thread", 0, 10)
	if err != nil {
		t.Fatalf("空线程 ListMessages 失败: %v", err)
	}
	if hasMore {
		t.Fatalf("空线程期望 hasMore 为 false")
	}
	if len(emptyList) != 0 {
		t.Fatalf("空线程期望结果为空切片")
	}
}

func TestGetThreadNotFound(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "notfound_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	_, err = s.GetThread(ctx, "non-existent-id")
	if err == nil {
		t.Fatalf("期望返回错误，实际为 nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 errors.Is(err, ErrNotFound) 为 true，实际为: %v", err)
	}
}

func TestPersistenceAcrossCloseAndOpen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "persistence_test.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第一次 Open 失败: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	proj := Project{
		ID:        "p_persist",
		Name:      "Persist Project",
		Path:      "/persist/path",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t_persist",
		ProjectID: "p_persist",
		Title:     "Persist Thread",
		Provider:  "anthropic",
		Model:     "claude-3-opus",
		SessionID: "sess-persist",
		CreatedAt: now,
		UpdatedAt: now,
		Archived:  false,
	}
	if err := s1.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	msg := Message{
		ID:        "m_persist",
		ThreadID:  "t_persist",
		Role:      "user",
		Kind:      "text",
		Text:      "persisted message content",
		TurnIndex: 1,
		TurnID:    "turn-p",
		IsFinal:   true,
		CreatedAt: now,
	}
	if _, err := s1.AppendMessage(ctx, msg); err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 再次打开数据库
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第二次 Open 失败: %v", err)
	}
	defer s2.Close()

	// 验证 Project
	projects, err := s2.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects 失败: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "p_persist" || projects[0].Name != "Persist Project" {
		t.Fatalf("重新打开后 Project 数据不一致: %+v", projects)
	}

	// 验证 Thread
	thRead, err := s2.GetThread(ctx, "t_persist")
	if err != nil {
		t.Fatalf("重新打开后 GetThread 失败: %v", err)
	}
	if thRead.Title != "Persist Thread" || thRead.Provider != "anthropic" || thRead.Model != "claude-3-opus" || thRead.SessionID != "sess-persist" {
		t.Fatalf("重新打开后 Thread 字段不一致: %+v", thRead)
	}

	// 验证 Message
	msgs, hasMore, err := s2.ListMessages(ctx, "t_persist", 0, 10)
	if err != nil {
		t.Fatalf("重新打开后 ListMessages 失败: %v", err)
	}
	if hasMore || len(msgs) != 1 {
		t.Fatalf("重新打开后 Message 数量不一致: len=%d, hasMore=%v", len(msgs), hasMore)
	}
	if msgs[0].ID != "m_persist" || msgs[0].Text != "persisted message content" || msgs[0].Seq != 1 || !msgs[0].IsFinal {
		t.Fatalf("重新打开后 Message 字段不一致: %+v", msgs[0])
	}
}

func TestStoreThreadMethods(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread_methods_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	_ = s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now, Archived: false})
	_ = s.CreateThread(ctx, Thread{ID: "t2", ProjectID: "p1", Title: "T2", Provider: "openai", CreatedAt: now, UpdatedAt: now, Archived: true})

	// 测试 UpdateThreadSession
	if err := s.UpdateThreadSession(ctx, "t1", "new-session"); err != nil {
		t.Fatalf("UpdateThreadSession 失败: %v", err)
	}
	th1, _ := s.GetThread(ctx, "t1")
	if th1.SessionID != "new-session" {
		t.Fatalf("期望 session_id 更新为 new-session，实际为 %s", th1.SessionID)
	}

	// 测试 TouchThread
	future := now.Add(1 * time.Hour)
	if err := s.TouchThread(ctx, "t1", future); err != nil {
		t.Fatalf("TouchThread 失败: %v", err)
	}
	th1, _ = s.GetThread(ctx, "t1")
	if th1.UpdatedAt.Format(time.RFC3339Nano) != future.Format(time.RFC3339Nano) {
		t.Fatalf("TouchThread 时间不匹配")
	}

	// 测试 ListThreads includeArchived 过滤
	activeThreads, err := s.ListThreads(ctx, "p1", false)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	if len(activeThreads) != 1 || activeThreads[0].ID != "t1" {
		t.Fatalf("期望只返回 1 个未归档 thread，实际为 %+v", activeThreads)
	}

	allThreads, err := s.ListThreads(ctx, "p1", true)
	if err != nil {
		t.Fatalf("ListThreads (all) 失败: %v", err)
	}
	if len(allThreads) != 2 {
		t.Fatalf("期望返回 2 个 thread，实际为 %d", len(allThreads))
	}

	// 测试 ListArchivedThreads
	archivedThreads, err := s.ListArchivedThreads(ctx, "p1")
	if err != nil {
		t.Fatalf("ListArchivedThreads 失败: %v", err)
	}
	if len(archivedThreads) != 1 || archivedThreads[0].ID != "t2" {
		t.Fatalf("期望只返回 1 个已归档 thread (t2)，实际为 %+v", archivedThreads)
	}

	// 全量测试（projectID 为空）
	allArchived, err := s.ListArchivedThreads(ctx, "")
	if err != nil {
		t.Fatalf("ListArchivedThreads (all) 失败: %v", err)
	}
	if len(allArchived) != 1 || allArchived[0].ID != "t2" {
		t.Fatalf("全量期望只返回 1 个已归档 thread (t2)，实际为 %+v", allArchived)
	}
}

func TestTurnUsageUpsertAndList(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "usage_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	_ = s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now})

	// 1. 写入第 1 轮用量
	u1 := TurnUsage{
		ThreadID:     "t1",
		TurnIndex:    1,
		InputTokens:  100,
		OutputTokens: 50,
		CachedTokens: 20,
	}
	if err := s.UpsertTurnUsage(ctx, u1); err != nil {
		t.Fatalf("UpsertTurnUsage u1 失败: %v", err)
	}

	// 2. 写入第 2 轮用量
	u2 := TurnUsage{
		ThreadID:     "t1",
		TurnIndex:    2,
		InputTokens:  200,
		OutputTokens: 80,
		CachedTokens: 0,
	}
	if err := s.UpsertTurnUsage(ctx, u2); err != nil {
		t.Fatalf("UpsertTurnUsage u2 失败: %v", err)
	}

	// 3. 覆盖写入第 1 轮用量（测试 upsert 覆盖写入）
	u1Update := TurnUsage{
		ThreadID:      "t1",
		TurnIndex:     1,
		InputTokens:   150,
		OutputTokens:  60,
		CachedTokens:  30,
		ContextTokens: 900,
	}
	if err := s.UpsertTurnUsage(ctx, u1Update); err != nil {
		t.Fatalf("UpsertTurnUsage u1Update 失败: %v", err)
	}

	// 4. 按会话列出用量，验证按 turn_index 升序
	list, err := s.ListTurnUsage(ctx, "t1")
	if err != nil {
		t.Fatalf("ListTurnUsage 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望返回 2 条用量记录，实际返回 %d", len(list))
	}

	if list[0].TurnIndex != 1 || list[0].InputTokens != 150 || list[0].OutputTokens != 60 || list[0].CachedTokens != 30 || list[0].ContextTokens != 900 {
		t.Errorf("list[0] 覆盖后数据不符: %+v", list[0])
	}
	// 旧数据没有 context_tokens 时读回 0，界面据此回退到“输入 + 输出”。
	if list[1].TurnIndex != 2 || list[1].InputTokens != 200 || list[1].OutputTokens != 80 || list[1].CachedTokens != 0 || list[1].ContextTokens != 0 {
		t.Errorf("list[1] 数据不符: %+v", list[1])
	}

	// 5. 不存在的会话返回空切片且无错误
	emptyList, err := s.ListTurnUsage(ctx, "non-existent")
	if err != nil {
		t.Fatalf("查询空会话用量失败: %v", err)
	}
	if len(emptyList) != 0 {
		t.Fatalf("期望空切片，实际返回 %d 条", len(emptyList))
	}
}

func TestThreadSortOrderRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sort_order_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	proj := Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "Thread 1",
		Provider:  "anthropic",
		SortOrder: 42,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	got, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if got.SortOrder != 42 {
		t.Fatalf("期望 SortOrder 42，实际为 %d", got.SortOrder)
	}

	if err := s.UpdateThreadSortOrder(ctx, "t1", 99); err != nil {
		t.Fatalf("UpdateThreadSortOrder 失败: %v", err)
	}

	gotUpdated, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 更新后失败: %v", err)
	}
	if gotUpdated.SortOrder != 99 {
		t.Fatalf("期望 SortOrder 99，实际为 %d", gotUpdated.SortOrder)
	}

	// 测试对不存在的线程更新 sort_order
	if err := s.UpdateThreadSortOrder(ctx, "non-existent", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望对不存在线程更新返回 ErrNotFound，实际为: %v", err)
	}
}

func TestFollowUpsRoundTripAndRevision(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "follow_ups_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	proj := Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "Thread 1",
		Provider:  "anthropic",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	items := []FollowUp{
		{
			ID:          "f1",
			ThreadID:    "t1",
			Text:        "First follow-up",
			Attachments: []string{"att-1", "att-2"},
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "f2",
			ThreadID:    "t1",
			Text:        "Second follow-up",
			Attachments: nil,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	if err := s.ReplaceFollowUps(ctx, "t1", 5, items); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}

	// 验证 revision 更新
	gotTh, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if gotTh.FollowUpRevision != 5 {
		t.Fatalf("期望 FollowUpRevision 为 5，实际为 %d", gotTh.FollowUpRevision)
	}

	// 验证 ListFollowUps 顺序与附件
	list, err := s.ListFollowUps(ctx, "t1")
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望返回 2 条 follow-up，实际返回 %d", len(list))
	}
	if list[0].ID != "f1" || list[0].Position != 0 || list[0].Text != "First follow-up" {
		t.Errorf("list[0] 数据不符: %+v", list[0])
	}
	if len(list[0].Attachments) != 2 || list[0].Attachments[0] != "att-1" || list[0].Attachments[1] != "att-2" {
		t.Errorf("list[0] 附件不符: %+v", list[0].Attachments)
	}
	if list[1].ID != "f2" || list[1].Position != 1 || list[1].Text != "Second follow-up" {
		t.Errorf("list[1] 数据不符: %+v", list[1])
	}
	if len(list[1].Attachments) != 0 {
		t.Errorf("list[1] 期望空附件切片，实际为: %+v", list[1].Attachments)
	}

	// 再次 Replace 为空切片后列表为空，且 revision 更新
	if err := s.ReplaceFollowUps(ctx, "t1", 6, []FollowUp{}); err != nil {
		t.Fatalf("ReplaceFollowUps 为空切片失败: %v", err)
	}
	emptyList, err := s.ListFollowUps(ctx, "t1")
	if err != nil {
		t.Fatalf("ListFollowUps 查询空切片失败: %v", err)
	}
	if len(emptyList) != 0 {
		t.Fatalf("期望 follow-up 列表为空，实际包含 %d 条", len(emptyList))
	}
	gotTh2, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 第二次查询失败: %v", err)
	}
	if gotTh2.FollowUpRevision != 6 {
		t.Fatalf("期望 FollowUpRevision 为 6，实际为 %d", gotTh2.FollowUpRevision)
	}

	// 若现有代码没有删除会话方法则跳过级联测试
	t.Log("现有 Store 未暴露 DeleteThread 方法，按规范跳过级联删除测试")
}

func TestStoreMediaContinuationAndAttachmentMeta(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "media_continuation_meta_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	now := time.Now().UTC()
	proj := Project{
		ID:        "p_meta",
		Name:      "Meta Project",
		Path:      "/path/to/p_meta",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	// 1. CreateThread 带 Continuation 读回一致，UpdateThreadContinuation 改写及清空后 GetThread 读回
	th := Thread{
		ID:           "t_meta",
		ProjectID:    "p_meta",
		Title:        "Thread Meta",
		Provider:     "anthropic",
		Model:        "claude-3-5-sonnet",
		SessionID:    "sess-meta",
		Continuation: "initial-continuation-token",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	gotTh, err := s.GetThread(ctx, "t_meta")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if gotTh.Continuation != "initial-continuation-token" {
		t.Fatalf("期望 Continuation 为 initial-continuation-token，实际为 %q", gotTh.Continuation)
	}

	threads, err := s.ListThreads(ctx, "p_meta", false)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	if len(threads) == 0 || threads[0].Continuation != "initial-continuation-token" {
		t.Fatalf("ListThreads 未能正确读回 Continuation: %+v", threads)
	}

	// 改写 Continuation
	if err := s.UpdateThreadContinuation(ctx, "t_meta", "updated-continuation-token"); err != nil {
		t.Fatalf("UpdateThreadContinuation 改写失败: %v", err)
	}
	gotTh, err = s.GetThread(ctx, "t_meta")
	if err != nil {
		t.Fatalf("GetThread 改写后查询失败: %v", err)
	}
	if gotTh.Continuation != "updated-continuation-token" {
		t.Fatalf("期望改写后 Continuation 为 updated-continuation-token，实际为 %q", gotTh.Continuation)
	}

	// 清空 Continuation
	if err := s.UpdateThreadContinuation(ctx, "t_meta", ""); err != nil {
		t.Fatalf("UpdateThreadContinuation 清空失败: %v", err)
	}
	gotTh, err = s.GetThread(ctx, "t_meta")
	if err != nil {
		t.Fatalf("GetThread 清空后查询失败: %v", err)
	}
	if gotTh.Continuation != "" {
		t.Fatalf("期望清空后 Continuation 为空字符串，实际为 %q", gotTh.Continuation)
	}

	// 不存在的 ID 应返回 ErrNotFound
	if err := s.UpdateThreadContinuation(ctx, "non-existent-thread", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望返回 ErrNotFound，实际为: %v", err)
	}

	// 2. 消息带 Media（含 Width/Height）append 后 ListMessages 读回一致，UpdateMessage 能改写 media
	w := 1920
	h := 1080
	msg := Message{
		ID:        "m_media_1",
		ThreadID:  "t_meta",
		Role:      "assistant",
		Kind:      "text",
		Text:      "Message with media",
		TurnIndex: 1,
		TurnID:    "turn-1",
		Media: []MediaRef{
			{
				ID:        "media-1",
				Source:    "upload",
				Name:      "screenshot.png",
				MimeType:  "image/png",
				ByteCount: 1048576,
				Width:     &w,
				Height:    &h,
			},
			{
				ID:        "media-2",
				Source:    "file",
				Name:      "raw.bin",
				MimeType:  "application/octet-stream",
				ByteCount: 512,
				Width:     nil,
				Height:    nil,
			},
		},
	}
	appended, err := s.AppendMessage(ctx, msg)
	if err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}
	if len(appended.Media) != 2 {
		t.Fatalf("AppendMessage 返回的 Media 长度期望 2，实际为 %d", len(appended.Media))
	}

	msgs, _, err := s.ListMessages(ctx, "t_meta", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("期望读回 1 条消息，实际为 %d", len(msgs))
	}
	mRead := msgs[0]
	if len(mRead.Media) != 2 {
		t.Fatalf("读回的 Media 数量期望 2，实际为 %d", len(mRead.Media))
	}
	if mRead.Media[0].ID != "media-1" || mRead.Media[0].Width == nil || *mRead.Media[0].Width != 1920 || mRead.Media[0].Height == nil || *mRead.Media[0].Height != 1080 {
		t.Fatalf("读回 media[0] 不匹配: %+v", mRead.Media[0])
	}
	if mRead.Media[1].ID != "media-2" || mRead.Media[1].Width != nil || mRead.Media[1].Height != nil {
		t.Fatalf("读回 media[1] 不匹配: %+v", mRead.Media[1])
	}

	// UpdateMessage 改写 media
	newW := 640
	mRead.Media = []MediaRef{
		{
			ID:        "media-3",
			Source:    "remote",
			Name:      "thumb.webp",
			MimeType:  "image/webp",
			ByteCount: 4096,
			Width:     &newW,
			Height:    nil,
		},
	}
	if err := s.UpdateMessage(ctx, mRead); err != nil {
		t.Fatalf("UpdateMessage 改写 media 失败: %v", err)
	}
	msgsUpdated, _, err := s.ListMessages(ctx, "t_meta", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 查询改写后消息失败: %v", err)
	}
	if len(msgsUpdated) != 1 || len(msgsUpdated[0].Media) != 1 || msgsUpdated[0].Media[0].ID != "media-3" {
		t.Fatalf("改写后的 Media 不匹配: %+v", msgsUpdated[0].Media)
	}

	// 3. ReplaceFollowUps 带 AttachmentMeta 后 ListFollowUps 读回一致
	followUps := []FollowUp{
		{
			ID:          "f_meta_1",
			ThreadID:    "t_meta",
			Text:        "Follow up with attachment meta",
			Attachments: []string{"att-1"},
			AttachmentMeta: []FollowUpAttachment{
				{
					ID:        "att-1",
					Name:      "doc.pdf",
					MimeType:  "application/pdf",
					ByteCount: 20480,
					Path:      "/tmp/doc.pdf",
				},
			},
		},
		{
			ID:             "f_meta_2",
			ThreadID:       "t_meta",
			Text:           "Follow up without meta",
			Attachments:    []string{},
			AttachmentMeta: nil,
		},
	}
	if err := s.ReplaceFollowUps(ctx, "t_meta", 1, followUps); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}

	readFollowUps, err := s.ListFollowUps(ctx, "t_meta")
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(readFollowUps) != 2 {
		t.Fatalf("期望读回 2 条 follow-up，实际为 %d", len(readFollowUps))
	}
	if len(readFollowUps[0].AttachmentMeta) != 1 {
		t.Fatalf("readFollowUps[0].AttachmentMeta 长度期望 1，实际为 %d", len(readFollowUps[0].AttachmentMeta))
	}
	attMeta := readFollowUps[0].AttachmentMeta[0]
	if attMeta.ID != "att-1" || attMeta.Name != "doc.pdf" || attMeta.MimeType != "application/pdf" || attMeta.ByteCount != 20480 || attMeta.Path != "/tmp/doc.pdf" {
		t.Fatalf("readFollowUps[0].AttachmentMeta[0] 数据不符: %+v", attMeta)
	}
	if readFollowUps[1].AttachmentMeta != nil {
		t.Fatalf("readFollowUps[1].AttachmentMeta 期望为 nil，实际为: %+v", readFollowUps[1].AttachmentMeta)
	}

	// 4. 关闭后重新 Open 同一 db 文件数据仍在
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第二次 Open 失败: %v", err)
	}
	defer s2.Close()

	// 验证 Thread (含清空后的 Continuation 为 "")
	gotThReopened, err := s2.GetThread(ctx, "t_meta")
	if err != nil {
		t.Fatalf("重开后 GetThread 失败: %v", err)
	}
	if gotThReopened.Continuation != "" {
		t.Fatalf("重开后 Continuation 期望为空，实际为 %q", gotThReopened.Continuation)
	}

	// 重开后验证 UpdateThreadContinuation 再写并验证
	if err := s2.UpdateThreadContinuation(ctx, "t_meta", "reopened-token"); err != nil {
		t.Fatalf("重开后 UpdateThreadContinuation 失败: %v", err)
	}
	gotThReopened2, err := s2.GetThread(ctx, "t_meta")
	if err != nil {
		t.Fatalf("重开后二次 GetThread 失败: %v", err)
	}
	if gotThReopened2.Continuation != "reopened-token" {
		t.Fatalf("重开后二次 Continuation 期望为 reopened-token，实际为 %q", gotThReopened2.Continuation)
	}

	// 重开后验证 Message Media
	msgsReopened, _, err := s2.ListMessages(ctx, "t_meta", 0, 10)
	if err != nil {
		t.Fatalf("重开后 ListMessages 失败: %v", err)
	}
	if len(msgsReopened) != 1 || len(msgsReopened[0].Media) != 1 || msgsReopened[0].Media[0].ID != "media-3" {
		t.Fatalf("重开后 Media 数据不符: %+v", msgsReopened)
	}

	// 重开后验证 FollowUp AttachmentMeta
	followUpsReopened, err := s2.ListFollowUps(ctx, "t_meta")
	if err != nil {
		t.Fatalf("重开后 ListFollowUps 失败: %v", err)
	}
	if len(followUpsReopened) != 2 || len(followUpsReopened[0].AttachmentMeta) != 1 {
		t.Fatalf("重开后 FollowUp AttachmentMeta 长度不符: %+v", followUpsReopened)
	}
	if followUpsReopened[0].AttachmentMeta[0].ID != "att-1" {
		t.Fatalf("重开后 AttachmentMeta 数据不符: %+v", followUpsReopened[0].AttachmentMeta)
	}
}

func TestStoreThreadPinnedAndArchived(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pin_archive_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	proj := Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "Thread 1",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet",
		CreatedAt: now,
		UpdatedAt: now,
		Pinned:    false,
		Archived:  false,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 1. 测试 SetThreadPinned
	if err := s.SetThreadPinned(ctx, "t1", true); err != nil {
		t.Fatalf("SetThreadPinned 失败: %v", err)
	}
	got, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if !got.Pinned {
		t.Fatalf("期望 Pinned 为 true，实际为 false")
	}

	if err := s.SetThreadPinned(ctx, "t1", false); err != nil {
		t.Fatalf("SetThreadPinned(false) 失败: %v", err)
	}
	got, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if got.Pinned {
		t.Fatalf("期望 Pinned 为 false，实际为 true")
	}

	// 不存在的 ID 返回 ErrNotFound
	if err := s.SetThreadPinned(ctx, "not-exist", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为: %v", err)
	}

	// 2. 测试 SetThreadArchived
	if err := s.SetThreadArchived(ctx, "t1", true); err != nil {
		t.Fatalf("SetThreadArchived 失败: %v", err)
	}
	got, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if !got.Archived {
		t.Fatalf("期望 Archived 为 true，实际为 false")
	}

	// ListThreads 默认不包含归档
	listActive, err := s.ListThreads(ctx, "p1", false)
	if err != nil {
		t.Fatalf("ListThreads active 失败: %v", err)
	}
	if len(listActive) != 0 {
		t.Fatalf("期望归档后 active 为空，实际有 %d 条", len(listActive))
	}

	// ListThreads includeArchived 包含归档
	listAll, err := s.ListThreads(ctx, "p1", true)
	if err != nil {
		t.Fatalf("ListThreads all 失败: %v", err)
	}
	if len(listAll) != 1 || !listAll[0].Archived {
		t.Fatalf("期望包含归档线程，实际为: %+v", listAll)
	}

	// 不存在的 ID 返回 ErrNotFound
	if err := s.SetThreadArchived(ctx, "not-exist", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为: %v", err)
	}
}

func TestStoreDeleteThread(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "delete_thread_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	proj := Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "Thread 1",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	if _, err := s.AppendMessage(ctx, Message{
		ID:        "m1",
		ThreadID:  "t1",
		Role:      "user",
		Kind:      "text",
		Text:      "hello",
		TurnIndex: 1,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}

	if err := s.UpsertTurnUsage(ctx, TurnUsage{
		ThreadID:     "t1",
		TurnIndex:    1,
		InputTokens:  10,
		OutputTokens: 20,
	}); err != nil {
		t.Fatalf("UpsertTurnUsage 失败: %v", err)
	}

	if err := s.ReplaceFollowUps(ctx, "t1", 1, []FollowUp{
		{ID: "fu1", ThreadID: "t1", Position: 0, Text: "follow up text", CreatedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}

	// 执行删除
	if err := s.DeleteThread(ctx, "t1"); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}

	// 验证线程不存在
	if _, err := s.GetThread(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望线程返回 ErrNotFound，实际为: %v", err)
	}

	// 验证消息已被删除
	msgs, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("期望消息已清空，实际有 %d 条", len(msgs))
	}

	// 验证 follow-ups 已被删除
	fus, err := s.ListFollowUps(ctx, "t1")
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(fus) != 0 {
		t.Fatalf("期望 follow-ups 已清空，实际有 %d 条", len(fus))
	}

	// 再次删除返回 ErrNotFound
	if err := s.DeleteThread(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除期望 ErrNotFound，实际为: %v", err)
	}
}

func TestStoreDeleteProjectCascadesThreads(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "delete_project_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, id := range []string{"p1", "p2"} {
		proj := Project{ID: id, Name: "Project " + id, Path: "/path/" + id, CreatedAt: now, UpdatedAt: now}
		if err := s.UpsertProject(ctx, proj); err != nil {
			t.Fatalf("UpsertProject %s 失败: %v", id, err)
		}
	}

	for _, tc := range []struct{ threadID, projectID string }{
		{"t1", "p1"},
		{"t2", "p2"},
	} {
		th := Thread{
			ID:        tc.threadID,
			ProjectID: tc.projectID,
			Title:     "Thread " + tc.threadID,
			Provider:  "anthropic",
			Model:     "claude-3-5-sonnet",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.CreateThread(ctx, th); err != nil {
			t.Fatalf("CreateThread %s 失败: %v", tc.threadID, err)
		}
		if _, err := s.AppendMessage(ctx, Message{
			ID:        "m-" + tc.threadID,
			ThreadID:  tc.threadID,
			Role:      "user",
			Kind:      "text",
			Text:      "hello",
			TurnIndex: 1,
			CreatedAt: now,
		}); err != nil {
			t.Fatalf("AppendMessage %s 失败: %v", tc.threadID, err)
		}
	}

	// 删除项目：依赖它的会话与消息随外键级联删除
	if err := s.DeleteProject(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProject 失败: %v", err)
	}

	projs, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects 失败: %v", err)
	}
	for _, p := range projs {
		if p.ID == "p1" {
			t.Fatalf("期望项目已被删除，但仍存在于 ListProjects 中")
		}
	}

	if _, err := s.GetThread(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望项目下的会话被级联删除，实际为: %v", err)
	}
	msgs, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("期望会话消息被级联删除，实际有 %d 条", len(msgs))
	}

	// 其他项目与其会话不受影响
	if _, err := s.GetThread(ctx, "t2"); err != nil {
		t.Fatalf("其它项目的会话不应被删除，实际 GetThread 失败: %v", err)
	}
	msgs, _, err = s.ListMessages(ctx, "t2", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("其它项目的消息应保留 1 条，实际有 %d 条", len(msgs))
	}

	// 再次删除返回 ErrNotFound
	if err := s.DeleteProject(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除项目期望 ErrNotFound，实际为: %v", err)
	}
}

func TestStoreThreadComposerPrefs(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread_composer_prefs_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "claude", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 新建线程默认 build 模式，其余偏好为空
	th, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.InteractionMode != "build" || th.Effort != "" || th.BeesProvider != "" || th.BeesModel != "" || th.BeesEffort != "" {
		t.Fatalf("默认 composer 偏好不符合预期: %+v", th)
	}

	if err := s.UpdateThreadComposerPrefs(ctx, "t1", "plan", "high", "codex", "gpt-5", "low", "reviewer"); err != nil {
		t.Fatalf("UpdateThreadComposerPrefs 失败: %v", err)
	}
	th, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.InteractionMode != "plan" || th.Effort != "high" || th.BeesProvider != "codex" || th.BeesModel != "gpt-5" || th.BeesEffort != "low" || th.Persona != "reviewer" {
		t.Fatalf("composer 偏好读回不一致: %+v", th)
	}

	list, err := s.ListThreads(ctx, "p1", true)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	if len(list) != 1 || list[0].InteractionMode != "plan" || list[0].BeesEffort != "low" {
		t.Fatalf("ListThreads 未带回 composer 偏好: %+v", list)
	}

	if err := s.UpdateThreadComposerPrefs(ctx, "missing", "build", "", "", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望不存在的线程返回 ErrNotFound，实际为 %v", err)
	}
}

// TestStoreThreadPersonaRoundTrip 验证人设 id 能经 CreateThread / UpdateThreadComposerPrefs 写入并读回。
func TestStoreThreadPersonaRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread_persona_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "magpie", Persona: "reviewer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	th, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.Persona != "reviewer" {
		t.Fatalf("CreateThread 未写入 persona: %+v", th)
	}

	// 换成字面量 none（用户显式选择无人设）后读回。
	if err := s.UpdateThreadComposerPrefs(ctx, "t1", "build", "", "", "", "", "none"); err != nil {
		t.Fatalf("UpdateThreadComposerPrefs 失败: %v", err)
	}
	th, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.Persona != "none" {
		t.Fatalf("persona 读回不一致: %+v", th)
	}

	list, err := s.ListThreads(ctx, "p1", true)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	if len(list) != 1 || list[0].Persona != "none" {
		t.Fatalf("ListThreads 未带回 persona: %+v", list)
	}
}

func TestStoreThreadFastMode(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread_fast_mode_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "codex", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 新建线程默认关闭快速模式，速率档位为空
	th, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.FastMode || th.SpeedTier != "" {
		t.Fatalf("默认快速模式不符合预期: %+v", th)
	}

	if err := s.UpdateThreadFastMode(ctx, "t1", true, "fast"); err != nil {
		t.Fatalf("UpdateThreadFastMode 失败: %v", err)
	}
	th, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if !th.FastMode || th.SpeedTier != "fast" {
		t.Fatalf("快速模式读回不一致: %+v", th)
	}

	list, err := s.ListThreads(ctx, "p1", true)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	if len(list) != 1 || !list[0].FastMode || list[0].SpeedTier != "fast" {
		t.Fatalf("ListThreads 未带回快速模式: %+v", list)
	}

	// 关闭快速模式时档位随之清空
	if err := s.UpdateThreadFastMode(ctx, "t1", false, ""); err != nil {
		t.Fatalf("UpdateThreadFastMode 失败: %v", err)
	}
	th, err = s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.FastMode || th.SpeedTier != "" {
		t.Fatalf("关闭快速模式后状态不符合预期: %+v", th)
	}

	if err := s.UpdateThreadFastMode(ctx, "missing", true, "fast"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望不存在的线程返回 ErrNotFound，实际为 %v", err)
	}
}

func TestMessageToolFields(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "message_tool_fields_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "codex", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	toolName := "bash"
	toolInput := `{"cmd":"ls"}`
	toolStatus := "running"
	toolCompletedAt := "2026-10-20T01:00:00Z"

	// 1. 先 AppendMessage 一条设置了这 4 个字段的 tool 消息
	toolMsg := Message{
		ID:              "msg_tool_1",
		ThreadID:        "t1",
		Role:            "tool",
		Kind:            "call",
		Text:            "running command",
		TurnIndex:       1,
		TurnID:          "turn-1",
		ToolName:        &toolName,
		ToolInput:       &toolInput,
		ToolStatus:      &toolStatus,
		ToolCompletedAt: &toolCompletedAt,
	}

	appendedToolMsg, err := s.AppendMessage(ctx, toolMsg)
	if err != nil {
		t.Fatalf("AppendMessage tool 消息失败: %v", err)
	}

	// 2. 用 UpdateMessage 修改 ToolStatus 和 ToolCompletedAt
	updatedStatus := "completed"
	updatedCompletedAt := "2026-10-20T01:01:00Z"
	appendedToolMsg.ToolStatus = &updatedStatus
	appendedToolMsg.ToolCompletedAt = &updatedCompletedAt
	if err := s.UpdateMessage(ctx, appendedToolMsg); err != nil {
		t.Fatalf("UpdateMessage 失败: %v", err)
	}

	// 3. 用 ListMessages 读回，断言 4 个字段的值正确
	msgs, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("期望读回 1 条消息，实际为 %d", len(msgs))
	}
	readMsg := msgs[0]
	if readMsg.ToolName == nil || *readMsg.ToolName != toolName {
		t.Fatalf("ToolName 期望 %q，实际为 %v", toolName, readMsg.ToolName)
	}
	if readMsg.ToolInput == nil || *readMsg.ToolInput != toolInput {
		t.Fatalf("ToolInput 期望 %q，实际为 %v", toolInput, readMsg.ToolInput)
	}
	if readMsg.ToolStatus == nil || *readMsg.ToolStatus != updatedStatus {
		t.Fatalf("ToolStatus 期望 %q，实际为 %v", updatedStatus, readMsg.ToolStatus)
	}
	if readMsg.ToolCompletedAt == nil || *readMsg.ToolCompletedAt != updatedCompletedAt {
		t.Fatalf("ToolCompletedAt 期望 %q，实际为 %v", updatedCompletedAt, readMsg.ToolCompletedAt)
	}

	// 4. 再写一条不设这 4 个字段的消息，断言读回后都为 nil
	plainMsg := Message{
		ID:        "msg_plain_2",
		ThreadID:  "t1",
		Role:      "user",
		Kind:      "text",
		Text:      "hello",
		TurnIndex: 2,
		TurnID:    "turn-2",
	}
	if _, err := s.AppendMessage(ctx, plainMsg); err != nil {
		t.Fatalf("AppendMessage plain 消息失败: %v", err)
	}

	msgsAll, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 读回全部消息失败: %v", err)
	}
	if len(msgsAll) != 2 {
		t.Fatalf("期望读回 2 条消息，实际为 %d", len(msgsAll))
	}
	readPlain := msgsAll[1]
	if readPlain.ID != "msg_plain_2" {
		t.Fatalf("第二条消息 ID 期望 msg_plain_2，实际为 %s", readPlain.ID)
	}
	if readPlain.ToolName != nil {
		t.Fatalf("期望 readPlain.ToolName 为 nil，实际为 %q", *readPlain.ToolName)
	}
	if readPlain.ToolInput != nil {
		t.Fatalf("期望 readPlain.ToolInput 为 nil，实际为 %q", *readPlain.ToolInput)
	}
	if readPlain.ToolStatus != nil {
		t.Fatalf("期望 readPlain.ToolStatus 为 nil，实际为 %q", *readPlain.ToolStatus)
	}
	if readPlain.ToolCompletedAt != nil {
		t.Fatalf("期望 readPlain.ToolCompletedAt 为 nil，实际为 %q", *readPlain.ToolCompletedAt)
	}
}

// TestMessageErrKindRoundTrip 验证错误消息的失败类别能随消息落库并在读回时还原：
// 有类别时指针带回原值，普通消息读回 nil（界面据此回退到关键词识别）。
func TestMessageErrKindRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "message_err_kind_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	_ = s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now})
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "claude", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	errKind := "context_exhausted"
	errMsg := Message{
		ID:        "msg_err_1",
		ThreadID:  "t1",
		Role:      "assistant",
		Kind:      "error",
		Text:      "出错：prompt is too long",
		TurnIndex: 1,
		TurnID:    "turn-1",
		ErrKind:   &errKind,
	}
	if _, err := s.AppendMessage(ctx, errMsg); err != nil {
		t.Fatalf("AppendMessage 错误消息失败: %v", err)
	}
	plainMsg := Message{
		ID:        "msg_plain_kind",
		ThreadID:  "t1",
		Role:      "assistant",
		Kind:      "text",
		Text:      "普通回复",
		TurnIndex: 1,
		TurnID:    "turn-1",
	}
	if _, err := s.AppendMessage(ctx, plainMsg); err != nil {
		t.Fatalf("AppendMessage 普通消息失败: %v", err)
	}

	list, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d 条", len(list))
	}
	if list[0].ErrKind == nil || *list[0].ErrKind != "context_exhausted" {
		t.Fatalf("错误消息的 ErrKind 未落库: %+v", list[0].ErrKind)
	}
	// 旧行与普通消息没有类别，读回 nil，界面按原文关键词识别。
	if list[1].ErrKind != nil {
		t.Fatalf("普通消息的 ErrKind 期望 nil，实际为 %q", *list[1].ErrKind)
	}
}
