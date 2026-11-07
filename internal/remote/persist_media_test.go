package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type persistMediaTestHarness struct {
	mu        sync.Mutex
	startedCh chan struct{}
	releaseCh chan struct{}
	turns     []harness.Turn
}

func (h *persistMediaTestHarness) Kind() harness.Kind {
	return "persist-media-harness"
}

func (h *persistMediaTestHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	h.mu.Lock()
	h.turns = append(h.turns, turn)
	h.mu.Unlock()

	emit(harness.Event{
		Type: harness.EventText,
		Text: "reply done",
	})
	emit(harness.Event{
		Type: harness.EventDone,
	})

	if h.startedCh != nil {
		select {
		case h.startedCh <- struct{}{}:
		default:
		}
	}

	if h.releaseCh != nil {
		select {
		case <-h.releaseCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (h *persistMediaTestHarness) getLastTurn() harness.Turn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.turns) == 0 {
		return harness.Turn{}
	}
	return h.turns[len(h.turns)-1]
}

func (h *persistMediaTestHarness) getTurns() []harness.Turn {
	h.mu.Lock()
	defer h.mu.Unlock()
	res := make([]harness.Turn, len(h.turns))
	copy(res, h.turns)
	return res
}

func waitMediaTurn(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for harness")
	}
}

// a) 带图片消息发送后关闭 backend，用同一 store 与 dataDir 新建 backend 并 AttachStore，
// threads.history 返回的消息带 Media，ThreadsMediaRead 能读到字节。
func TestPersistMedia_MessageWithMediaReloadAndRead(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "mcode.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer s.Close()

	hub1 := NewStreamHub(StreamHubOptions{})
	defer hub1.Close()
	b1 := NewMemoryBackend(hub1)
	b1.SetDataDir(dataDir)
	if err := b1.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b1 failed: %v", err)
	}

	fakeH := &persistMediaTestHarness{startedCh: make(chan struct{}, 1)}
	b1.RegisterHarness(fakeH)

	projID := "proj-media-1"
	b1.AddProject(wire.ProjectSummary{ID: projID, Name: "media-proj", Path: dataDir})

	thRes, err := b1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	thID := thRes.ThreadID

	pngBytes := makeTestPNG(100, 80)
	upRes, err := b1.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
		Name: "diagram.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	sendRes, err := b1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    thID,
		Text:        "查看这张架构图",
		ActionID:    newUUID(),
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.MessageID == nil {
		t.Fatalf("expected non-nil MessageID")
	}

	// 等待执行完成与落库
	waitMediaTurn(t, fakeH.startedCh)
	waitThreadIdle(t, b1, ctx, thID)
	original, err := b1.ThreadHistory(ctx, wire.HistoryParams{ThreadID: thID})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := b1.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID: thID, MessageID: original.Messages[len(original.Messages)-1].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	b1.Close()

	// 重建 backend 并 AttachStore
	hub2 := NewStreamHub(StreamHubOptions{})
	defer hub2.Close()
	b2 := NewMemoryBackend(hub2)
	b2.SetDataDir(dataDir)
	if err := b2.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b2 failed: %v", err)
	}
	defer b2.Close()

	history, err := b2.ThreadHistory(ctx, wire.HistoryParams{ThreadID: thID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}

	var userMsg *wire.ThreadMessage
	for i := range history.Messages {
		if history.Messages[i].Role == "user" {
			userMsg = &history.Messages[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatalf("expected user message in history")
	}
	if len(userMsg.Media) != 1 {
		t.Fatalf("expected 1 media in userMsg.Media, got %d", len(userMsg.Media))
	}
	if userMsg.Media[0].ID != upRes.AttachmentID {
		t.Fatalf("expected media ID %s, got %s", upRes.AttachmentID, userMsg.Media[0].ID)
	}
	if userMsg.Media[0].MimeType != "image/png" {
		t.Fatalf("expected mime type image/png, got %s", userMsg.Media[0].MimeType)
	}

	// 通过 ThreadsMediaRead 读取媒体原图字节
	readRes, err := b2.ThreadsMediaRead(ctx, wire.ThreadsMediaReadParams{
		ThreadID:  thID,
		MessageID: userMsg.ID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantFull,
	})
	if err != nil {
		t.Fatalf("ThreadsMediaRead failed: %v", err)
	}
	readBytes, err := base64.StdEncoding.DecodeString(readRes.Base64)
	if err != nil {
		t.Fatalf("base64 decode read data failed: %v", err)
	}
	if len(readBytes) != len(pngBytes) {
		t.Fatalf("expected %d bytes, got %d", len(pngBytes), len(readBytes))
	}
	branched, err := b2.ThreadHistory(ctx, wire.HistoryParams{ThreadID: branch.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b2.ThreadsMediaRead(ctx, wire.ThreadsMediaReadParams{
		ThreadID: branch.ThreadID, MessageID: branched.Messages[0].ID,
		MediaID: upRes.AttachmentID, Variant: wire.MediaVariantFull,
	})
	if err != nil {
		t.Fatalf("branched history media unavailable after restart: %v", err)
	}
}

// b) BranchThread 或 ForwardThread 后重建 backend，continuation 恢复，
// 下一次发送时 harness 收到的提示词以该上下文块开头。
func TestPersistMedia_BranchOrForwardContinuationRestore(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "mcode.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer s.Close()

	hub1 := NewStreamHub(StreamHubOptions{})
	defer hub1.Close()
	b1 := NewMemoryBackend(hub1)
	b1.SetDataDir(dataDir)
	if err := b1.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b1 failed: %v", err)
	}

	projID := "proj-cont-1"
	b1.AddProject(wire.ProjectSummary{ID: projID, Name: "cont-proj", Path: dataDir})

	origThRes, err := b1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	origThID := origThRes.ThreadID

	// 添加已有消息，形成上下文
	msg1 := wire.ThreadMessage{
		ID:        newUUID(),
		Role:      "user",
		Text:      "这是第一轮提问背景",
		CreatedAt: formatTime(time.Now().Add(-10 * time.Minute)),
	}
	isFinal := true
	msg2 := wire.ThreadMessage{
		ID:              newUUID(),
		Role:            "assistant",
		Text:            "这是第一轮助手的解答",
		CreatedAt:       formatTime(time.Now().Add(-9 * time.Minute)),
		IsFinalResponse: &isFinal,
	}
	b1.mu.Lock()
	if td, ok := b1.threads[origThID]; ok {
		td.messages = append(td.messages, msg1, msg2)
	}
	b1.mu.Unlock()
	b1.persistMessages(origThID, []wire.ThreadMessage{msg1, msg2})

	// 分支该会话
	branchRes, err := b1.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  origThID,
		MessageID: msg2.ID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("BranchThread failed: %v", err)
	}
	branchedThID := branchRes.ThreadID
	forwardRes, err := b1.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: origThID,
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}

	// 关闭并重启 backend
	b1.Close()

	hub2 := NewStreamHub(StreamHubOptions{})
	defer hub2.Close()
	b2 := NewMemoryBackend(hub2)
	b2.SetDataDir(dataDir)
	if err := b2.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b2 failed: %v", err)
	}
	defer b2.Close()

	fakeH := &persistMediaTestHarness{startedCh: make(chan struct{}, 1)}
	b2.RegisterHarness(fakeH)

	off := false
	b2.SetGeneralSettingsFunc(func() config.Config { return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &off}} })

	for _, threadID := range []string{branchedThID, forwardRes.ThreadID} {
		// 发送新消息，触发 harness
		_, err = b2.SendMessage(ctx, wire.SendMessageParams{
			ThreadID: threadID,
			Text:     "基于上下文继续处理",
			ActionID: newUUID(),
		})
		if err != nil {
			t.Fatalf("SendMessage on branched thread failed: %v", err)
		}

		waitMediaTurn(t, fakeH.startedCh)
		lastTurn := fakeH.getLastTurn()
		if !strings.Contains(lastTurn.Prompt, forwardedContextStart) {
			t.Fatalf("expected prompt to start with continuation block, got:\n%s", lastTurn.Prompt)
		}
		if !strings.Contains(lastTurn.Prompt, "这是第一轮提问背景") {
			t.Fatalf("expected prompt to contain previous context, got:\n%s", lastTurn.Prompt)
		}
		if !strings.Contains(lastTurn.Prompt, "基于上下文继续处理") {
			t.Fatalf("expected prompt to contain new user text, got:\n%s", lastTurn.Prompt)
		}
	}
}

// c) 线程运行中以 WhenRunning="queue" 发送带附件消息被接受，
// 当前 turn 结束出队后 fake harness 收到的 Turn.Images 含该图片，用户消息带 Media。
func TestPersistMedia_QueueMessageWithAttachmentDequeued(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "mcode.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.SetDataDir(dataDir)
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store failed: %v", err)
	}
	defer b.Close()

	startedCh := make(chan struct{}, 2)
	releaseCh := make(chan struct{}, 2)
	fakeH := &persistMediaTestHarness{
		startedCh: startedCh,
		releaseCh: releaseCh,
	}
	b.RegisterHarness(fakeH)

	projID := "proj-queue-1"
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "queue-proj", Path: dataDir})

	thRes, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	thID := thRes.ThreadID

	// 发起第 1 轮消息并等待开始执行（此时线程处于运行中状态）
	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "第一轮提问",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage turn 1 failed: %v", err)
	}
	waitMediaTurn(t, startedCh)

	// 上传附件
	pngBytes := makeTestPNG(60, 60)
	upRes, err := b.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
		Name: "queued-avatar.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	// 运行中排队发送带附件消息
	qRes, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    thID,
		Text:        "排队带图的消息",
		WhenRunning: "queue",
		Attachments: []string{upRes.AttachmentID},
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage queued failed: %v", err)
	}
	if qRes.Queued == nil || !*qRes.Queued {
		t.Fatalf("expected Queued=true in send response")
	}
	if qRes.FollowUpID == nil {
		t.Fatalf("expected non-nil FollowUpID")
	}

	// 释放第 1 轮
	releaseCh <- struct{}{}

	// 等待第 2 轮自动出队并进入 harness
	waitMediaTurn(t, startedCh)
	// 释放第 2 轮
	releaseCh <- struct{}{}

	turns := fakeH.getTurns()
	if len(turns) < 2 {
		t.Fatalf("expected at least 2 turns executed, got %d", len(turns))
	}
	secondTurn := turns[1]
	if len(secondTurn.Images) != 1 {
		t.Fatalf("expected 1 image in second turn Images, got %d", len(secondTurn.Images))
	}
	expectedPath := filepath.Join(dataDir, "media", thID, upRes.AttachmentID+".png")
	if secondTurn.Images[0] != expectedPath {
		t.Fatalf("expected image path %q, got %q", expectedPath, secondTurn.Images[0])
	}

	// 验证时间线上的用户消息带 Media
	history, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: thID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var queuedUserMsg *wire.ThreadMessage
	for i := range history.Messages {
		if history.Messages[i].Role == "user" && strings.Contains(history.Messages[i].Text, "排队带图的消息") {
			queuedUserMsg = &history.Messages[i]
			break
		}
	}
	if queuedUserMsg == nil {
		t.Fatalf("expected queued user message in history")
	}
	if len(queuedUserMsg.Media) != 1 {
		t.Fatalf("expected 1 media on queued user message, got %d", len(queuedUserMsg.Media))
	}
	if queuedUserMsg.Media[0].ID != upRes.AttachmentID {
		t.Fatalf("expected media ID %s, got %s", upRes.AttachmentID, queuedUserMsg.Media[0].ID)
	}
}

// d) 排队中的附件在时钟推进超过 1 小时（SetClock）并调用 cleanupExpired 后仍存在、文件仍在；
// 重启恢复后也仍在。
func TestPersistMedia_QueuedAttachmentSurvivesExpiryAndRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "mcode.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer s.Close()

	hub1 := NewStreamHub(StreamHubOptions{})
	defer hub1.Close()
	b1 := NewMemoryBackend(hub1)
	b1.SetDataDir(dataDir)
	if err := b1.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b1 failed: %v", err)
	}

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	fakeH := &persistMediaTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	b1.RegisterHarness(fakeH)

	currentTime := time.Now()
	b1.SetClock(func() time.Time { return currentTime })

	projID := "proj-survive-1"
	b1.AddProject(wire.ProjectSummary{ID: projID, Name: "survive-proj", Path: dataDir})

	thRes, err := b1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	thID := thRes.ThreadID

	// 开启第 1 轮阻塞住
	_, err = b1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "block turn",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage block failed: %v", err)
	}
	waitMediaTurn(t, startedCh)

	// 上传附件
	pngBytes := makeTestPNG(40, 40)
	upRes, err := b1.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
		Name: "pinned.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	attFilePath := filepath.Join(dataDir, "attachments", upRes.AttachmentID+".png")
	if _, err := os.Stat(attFilePath); err != nil {
		t.Fatalf("expected attachment file on disk: %v", err)
	}

	// 排队发送
	_, err = b1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    thID,
		Text:        "queued for later",
		WhenRunning: "queue",
		Attachments: []string{upRes.AttachmentID},
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage queue failed: %v", err)
	}

	// 推进时钟 2 小时（大于 1 小时生命周期）
	currentTime = currentTime.Add(2 * time.Hour)
	cleaned := b1.attachments.cleanupExpired()
	if cleaned != 0 {
		t.Fatalf("expected 0 cleaned for queued attachment, got %d", cleaned)
	}

	// 校验磁盘文件仍然存在
	if _, err := os.Stat(attFilePath); err != nil {
		t.Fatalf("expected attachment file to survive cleanupExpired: %v", err)
	}

	// 宿主重启模拟（关闭 b1，不释放第 1 轮以保持队列未被消费）
	b1.Close()

	hub2 := NewStreamHub(StreamHubOptions{})
	defer hub2.Close()
	b2 := NewMemoryBackend(hub2)
	b2.SetDataDir(dataDir)
	b2.SetClock(func() time.Time { return currentTime.Add(3 * time.Hour) })
	if err := b2.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store to b2 failed: %v", err)
	}
	defer b2.Close()

	// 检查队列列表恢复
	qList, err := b2.QueueList(ctx, thID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(qList.FollowUps) != 1 {
		t.Fatalf("expected 1 follow up restored, got %d", len(qList.FollowUps))
	}
	if len(qList.FollowUps[0].Attachments) != 1 {
		t.Fatalf("expected 1 attachment in restored follow-up, got %d", len(qList.FollowUps[0].Attachments))
	}
	if qList.FollowUps[0].Attachments[0].ID != upRes.AttachmentID {
		t.Fatalf("expected attachment ID %s, got %s", upRes.AttachmentID, qList.FollowUps[0].Attachments[0].ID)
	}

	// 再次调用 cleanupExpired，验证重启恢复后标记的 queued 依然不会被清理
	b2.attachments.cleanupExpired()
	if _, err := os.Stat(attFilePath); err != nil {
		t.Fatalf("expected attachment file to survive after restart cleanup: %v", err)
	}

	// 验证 QueueRemove 移除该 follow-up 后会 unpin 附件
	_, err = b2.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID:   thID,
		FollowUpID: qList.FollowUps[0].ID,
		ActionID:   newUUID(),
	})
	if err != nil {
		t.Fatalf("QueueRemove failed: %v", err)
	}

	// unpin 后由于时钟已超过 1 小时，再次调用 cleanupExpired 应该被清理
	b2.attachments.cleanupExpired()
	if _, err := os.Stat(attFilePath); !os.IsNotExist(err) {
		t.Fatalf("expected attachment file to be cleaned up after unpin, but stat err was: %v", err)
	}
}

func newQueuedMediaBackend(t *testing.T) (*MemoryBackend, *store.Store, string, string, *persistMediaTestHarness) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetDataDir(dir)
	if err := b.AttachStore(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	h := &persistMediaTestHarness{startedCh: make(chan struct{}, 4)}
	b.RegisterHarness(h)
	b.AddProject(wire.ProjectSummary{ID: "project", Name: "project", Path: dir})
	th, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{ProjectID: "project"})
	if err != nil {
		t.Fatal(err)
	}
	b.SetSwarmQueueHooks(func(string) bool { return true }, nil)
	return b, s, dir, th.ThreadID, h
}

func uploadQueuedPNG(t *testing.T, b *MemoryBackend) string {
	t.Helper()
	res, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "queued.png", Data: base64.StdEncoding.EncodeToString(makeTestPNG(12, 8)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.AttachmentID
}

func queueMediaMessage(t *testing.T, b *MemoryBackend, threadID string, ids ...string) string {
	t.Helper()
	res, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: threadID, Text: "queued image", WhenRunning: "queue", Attachments: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.FollowUpID == nil {
		t.Fatal("expected queued message")
	}
	return *res.FollowUpID
}

func TestPersistMedia_SharedQueuedAttachment(t *testing.T) {
	for _, removeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "remove", false: "send"}[removeFirst], func(t *testing.T) {
			b, s, dir, threadID, h := newQueuedMediaBackend(t)
			id := uploadQueuedPNG(t, b)
			first := queueMediaMessage(t, b, threadID, id)
			second := queueMediaMessage(t, b, threadID, id, id)
			// 校验失败时的 pin 回滚也不能撤销已有队列引用。
			_, err := b.SendMessage(context.Background(), wire.SendMessageParams{
				ThreadID: threadID, Text: "invalid", WhenRunning: "queue", Attachments: []string{id, newUUID()},
			})
			if err == nil {
				t.Fatal("expected invalid attachment rejection")
			}
			b.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
			if removeFirst {
				_, err = b.QueueRemove(context.Background(), wire.ThreadsQueueRemoveParams{ThreadID: threadID, FollowUpID: first})
			} else {
				_, err = b.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: first})
				if err == nil {
					waitMediaTurn(t, h.startedCh)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if n := b.attachments.cleanupExpired(); n != 0 {
				t.Fatalf("shared queued attachment was cleaned: %d", n)
			}
			b.Close()
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			restored := NewMemoryBackend(hub)
			defer restored.Close()
			restored.SetDataDir(dir)
			if err := restored.AttachStore(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			next := &persistMediaTestHarness{startedCh: make(chan struct{}, 1)}
			restored.RegisterHarness(next)
			if _, err := restored.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: second}); err != nil {
				t.Fatal(err)
			}
			waitMediaTurn(t, next.startedCh)
			if images := next.getLastTurn().Images; len(images) != 1 {
				t.Fatalf("expected deduplicated image after restart, got %v", images)
			}
		})
	}
}

func TestPersistMedia_DequeueFailurePreservesAttachments(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "sendNow", true: "automatic"}[automatic], func(t *testing.T) {
			b, _, dir, threadID, h := newQueuedMediaBackend(t)
			first, second := uploadQueuedPNG(t, b), uploadQueuedPNG(t, b)
			fuID := queueMediaMessage(t, b, threadID, first, second)
			path := filepath.Join(dir, "attachments", second+".png")
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if automatic {
				b.mu.Lock()
				st, _, changed := b.startNextQueuedTurnLocked(b.threads[threadID], true, true)
				b.mu.Unlock()
				if st != nil || changed {
					t.Fatal("failed automatic dequeue started a turn")
				}
			} else if _, err := b.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID}); err == nil {
				t.Fatal("expected attachment read failure")
			}
			queue, err := b.QueueList(context.Background(), threadID)
			if err != nil || len(queue.FollowUps) != 1 {
				t.Fatalf("queue lost on failure: %+v, %v", queue, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "attachments", first+".png")); err != nil {
				t.Fatalf("first attachment consumed on partial failure: %v", err)
			}
			if err := os.Rename(path+".saved", path); err != nil {
				t.Fatal(err)
			}
			if _, err := b.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID}); err != nil {
				t.Fatal(err)
			}
			waitMediaTurn(t, h.startedCh)
			if images := h.getLastTurn().Images; len(images) != 2 {
				t.Fatalf("retry lost images: %v", images)
			}
		})
	}
}

func TestPersistMedia_QueueUpdateAttachments(t *testing.T) {
	b, _, _, threadID, h := newQueuedMediaBackend(t)
	first, second := uploadQueuedPNG(t, b), uploadQueuedPNG(t, b)
	fuID := queueMediaMessage(t, b, threadID, first)
	res, err := b.QueueUpdate(context.Background(), wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: fuID, Attachments: []string{second},
	})
	if err != nil || len(res.FollowUps) != 1 || res.FollowUps[0].Text != "queued image" {
		t.Fatalf("attachment-only update lost text: %+v, %v", res, err)
	}
	empty := ""
	res, err = b.QueueUpdate(context.Background(), wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: fuID, Text: &empty,
	})
	if err != nil || len(res.FollowUps) != 1 {
		t.Fatalf("clearing text lost attached image: %+v, %v", res, err)
	}
	b.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if n := b.attachments.cleanupExpired(); n != 1 {
		t.Fatalf("expected only replaced attachment to expire, got %d", n)
	}
	if _, err := b.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID}); err != nil {
		t.Fatal(err)
	}
	waitMediaTurn(t, h.startedCh)
	if images := h.getLastTurn().Images; len(images) != 1 || filepath.Base(images[0]) != second+".png" {
		t.Fatalf("updated attachment not delivered: %v", images)
	}
}

func TestPersistMedia_SwarmDoesNotDiscardAttachments(t *testing.T) {
	b, _, _, threadID, _ := newQueuedMediaBackend(t)
	id := uploadQueuedPNG(t, b)
	b.SetSwarmQueueHooks(func(string) bool { return true }, func(context.Context, string, string) (string, error) {
		t.Error("text-only Swarm route must not handle attachments")
		return "head", nil
	})
	res, err := b.QueueInterruptAndSend(context.Background(), wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID, Attachments: []string{id},
	})
	if err != nil || res.State != wire.ThreadsQueueInterruptStateQueued || len(res.Queue.FollowUps) != 1 {
		t.Fatalf("expected attachment queued behind bees: %+v, %v", res, err)
	}
}

func TestPersistMedia_PendingSendRestored(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "sendNow", true: "interrupt"}[interrupt], func(t *testing.T) {
			b, s, dir, threadID, _ := newQueuedMediaBackend(t)
			id := uploadQueuedPNG(t, b)
			fuID := queueMediaMessage(t, b, threadID, id)
			b.SetSwarmQueueHooks(nil, nil)
			// 模拟当前轮仍在等待取消，宿主在 pendingSend 真正开轮前关闭。
			b.mu.Lock()
			b.threads[threadID].summary.IsRunning = true
			b.mu.Unlock()
			var err error
			if interrupt {
				_, err = b.QueueInterruptAndSend(context.Background(), wire.ThreadsQueueInterruptAndSendParams{
					ThreadID: threadID, Attachments: []string{id},
				})
			} else {
				_, err = b.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID})
			}
			if err != nil {
				t.Fatal(err)
			}
			b.Close()
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			restored := NewMemoryBackend(hub)
			defer restored.Close()
			restored.SetDataDir(dir)
			if err := restored.AttachStore(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			h := &persistMediaTestHarness{startedCh: make(chan struct{}, 1)}
			restored.RegisterHarness(h)
			restored.SetSwarmQueueHooks(func(string) bool { return true }, nil)
			queue, err := restored.QueueList(context.Background(), threadID)
			if err != nil || len(queue.FollowUps) == 0 || len(queue.FollowUps[0].Attachments) != 1 {
				t.Fatalf("pending attachment lost on restart: %+v, %v", queue, err)
			}
			if _, err := restored.QueueSendNow(context.Background(), wire.ThreadsQueueSendNowParams{
				ThreadID: threadID, FollowUpID: queue.FollowUps[0].ID,
			}); err != nil {
				t.Fatal(err)
			}
			waitMediaTurn(t, h.startedCh)
			if images := h.getLastTurn().Images; len(images) != 1 {
				t.Fatalf("restored pending send lost image: %v", images)
			}
		})
	}
}
