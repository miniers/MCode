package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type reviewCommentUpload struct {
	data   []byte
	finish chan error
}

type reviewCommentsClient struct {
	*FakeClient
	uploads chan reviewCommentUpload
	get     func(context.Context, string, string, string, string) (ReviewSnapshot, error)
}

func (client *reviewCommentsClient) ReviewGet(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
	if client.get != nil {
		return client.get(ctx, tid, scope, turn, path)
	}
	return client.FakeClient.ReviewGet(ctx, tid, scope, turn, path)
}

func (client *reviewCommentsClient) UploadAttachment(ctx context.Context, name, mime string, data []byte) (Attachment, error) {
	request := reviewCommentUpload{data: append([]byte(nil), data...), finish: make(chan error, 1)}
	client.uploads <- request
	select {
	case err := <-request.finish:
		if err != nil {
			return Attachment{}, err
		}
		return client.FakeClient.UploadAttachment(ctx, name, mime, data)
	case <-time.After(5 * time.Second):
		return Attachment{}, errors.New("测试未完成上传")
	}
}

func reviewCommentsSetup(t *testing.T) (*chatState, *reviewCommentsClient, *diffPanelState, reviewDiffKey, ReviewFileContent) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	client := &reviewCommentsClient{FakeClient: NewFakeClient(), uploads: make(chan reviewCommentUpload, 8)}
	s := newChatState(client)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/workspace"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}, {ID: "t2", ProjectID: "p1"}}
	dp := getDiffPanelState(s)
	list, full := reviewFixture()
	client.SetReviewSnapshot("t1", "turn", "", "", list)
	client.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	key := reviewKey(s, "t1", list, "a.go")
	t.Cleanup(s.close)
	return s, client, dp, key, *full.File
}

func addReviewTestComment(t *testing.T, dp *diffPanelState, key reviewDiffKey, content ReviewFileContent, lines editor.DiffLineRange, body string) {
	t.Helper()
	original, ok := reviewSelectedText(content, lines)
	if !ok || !addReviewComment(dp, reviewCommentDraft{key: key, lines: lines, original: original, body: body}) {
		t.Fatal("无法添加评论")
	}
}

func receiveReviewCommentUpload(t *testing.T, client *reviewCommentsClient) reviewCommentUpload {
	t.Helper()
	select {
	case request := <-client.uploads:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("没有上传评论附件")
		return reviewCommentUpload{}
	}
}

func waitReviewComments(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("评论状态未达到预期")
}

func waitReviewCommentsAdded(t *testing.T, s *chatState, dp *diffPanelState, tid string, count int) {
	t.Helper()
	waitReviewComments(t, func() bool {
		reconcileReviewCommentAttachments(s, dp, tid)
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		added := 0
		for _, comment := range dp.comments {
			if comment.added {
				added++
			}
		}
		return !dp.commentBatch && added == count
	})
}

func TestReviewCommentOldNewCoordinates(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	for _, side := range []editor.DiffSide{editor.DiffOld, editor.DiffNew} {
		addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: side, Start: 1, End: 1}, "核对这一行")
	}
	if !reviewCommentsAddToConversation(s, dp, "t1") {
		t.Fatal("批量添加失败")
	}
	request := receiveReviewCommentUpload(t, client)
	request.finish <- nil
	text := string(request.data)
	for _, want := range []string{`path="a.go"`, `base="base" head="head"`, "side=old lines=2-2", "side=new lines=2-2", "var a = 1\n", "var a = 2\n", "<untrusted-annotations id="} {
		if !strings.Contains(text, want) {
			t.Errorf("附件缺少 %q", want)
		}
	}
	waitReviewCommentsAdded(t, s, dp, "t1", 2)
}

func TestReviewCommentsBatchAttachmentNoSend(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	s.draft = "保留草稿正文"
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffOld, Start: 1, End: 1}, "删除侧评论")
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 1}, "多行评论")
	if !reviewCommentsAddToConversation(s, dp, "t1") || reviewCommentsAddToConversation(s, dp, "t1") {
		t.Fatal("没有阻止重复排队")
	}
	request := receiveReviewCommentUpload(t, client)
	request.finish <- nil
	waitReviewCommentsAdded(t, s, dp, "t1", 2)
	if !strings.Contains(string(request.data), "side=new lines=1-2") || !strings.Contains(string(request.data), "多行评论") {
		t.Fatal("丢失多行坐标或评论")
	}
	s.mu.Lock()
	if s.draft != "保留草稿正文" || len(s.attachments) != 1 || s.attachments[0].Name != "评论.txt" {
		t.Errorf("草稿或附件数量错误: %q %+v", s.draft, s.attachments)
	}
	s.mu.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sendCalls) != 0 || len(client.sendWithAttachCalls) != 0 {
		t.Fatal("添加评论调用了 Send")
	}
}

func TestReviewCommentBecomesStaleOnRevisionChange(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	lines := editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}
	addReviewTestComment(t, dp, key, content, lines, "原位置")
	nextKey := key
	nextKey.head = "next"
	refreshReviewComments(dp, nextKey, content)
	if dp.comments[0].stale {
		t.Fatal("仅修订变化不应让仍匹配的原文过期")
	}
	content.NewText = "package main\n插入的新行\nvar a = 2\n"
	refreshReviewComments(dp, nextKey, content)
	if !dp.comments[0].stale || dp.comments[0].lines != lines || dp.comments[0].key.head != "head" {
		t.Fatal("评论迁移或丢失原版本")
	}
	if reviewCommentsAddToConversation(s, dp, "t1") {
		t.Fatal("仅有过期评论时不应生成附件")
	}
	select {
	case request := <-client.uploads:
		request.finish <- nil
		t.Fatal("过期评论触发上传")
	default:
	}
}

func TestReviewCommentAttachFailureKeepsComments(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "保留评论")
	if !reviewCommentsAddToConversation(s, dp, "t1") {
		t.Fatal("未开始上传")
	}
	request := receiveReviewCommentUpload(t, client)
	request.finish <- errors.New("上传失败")
	waitReviewComments(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && s.attachments[0].Err != ""
	})
	reconcileReviewCommentAttachments(s, dp, "t1")
	diffPanelMu.Lock()
	if len(dp.comments) != 1 || dp.comments[0].added || dp.comments[0].body != "保留评论" {
		t.Error("上传失败丢失评论或误标为已添加")
	}
	diffPanelMu.Unlock()
	s.mu.Lock()
	id := s.attachments[0].LocalID
	s.mu.Unlock()
	if !s.retryAttachment(id) {
		t.Fatal("不能复用现有附件重试")
	}
	retry := receiveReviewCommentUpload(t, client)
	retry.finish <- nil
	waitReviewCommentsAdded(t, s, dp, "t1", 1)
}

func TestReviewCommentsAttachLimitKeepsComments(t *testing.T) {
	s, _, dp, key, content := reviewCommentsSetup(t)
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "限额保留")
	limit := s.attachmentLimit()
	s.mu.Lock()
	s.attachments = make([]composerAttachment, limit)
	s.mu.Unlock()
	reviewCommentsAddToConversation(s, dp, "t1")
	waitReviewComments(t, func() bool { diffPanelMu.Lock(); defer diffPanelMu.Unlock(); return !dp.commentBatch })
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	if dp.comments[0].added || dp.comments[0].attachmentID != "" {
		t.Fatal("限额失败应保留未添加评论")
	}
}

func TestReviewCommentsStayInOriginThread(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "来源会话")
	reviewCommentsAddToConversation(s, dp, "t1")
	request := receiveReviewCommentUpload(t, client)
	s.selectThread("t2")
	request.finish <- nil
	waitReviewCommentsAdded(t, s, dp, "t1", 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 0 || len(s.drafts["t1"].Attachments) != 1 {
		t.Fatal("评论附件进入了错误的会话")
	}
}

func TestReviewCommentsBatchRevalidatesUnopenedFile(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "后台变更")
	_, next := reviewFixture()
	next.HeadRevision = "next"
	next.File.NewText = "package main\nvar a = 3\nvar a = 2\n"
	client.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", next)
	reviewCommentsAddToConversation(s, dp, "t1")
	waitReviewComments(t, func() bool { diffPanelMu.Lock(); defer diffPanelMu.Unlock(); return !dp.commentBatch })
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	if !dp.comments[0].stale || dp.comments[0].added || dp.comments[0].lines.Start != 1 {
		t.Fatal("批量添加未核对原位置")
	}
}

func TestReviewCommentsOriginChangesDuringValidation(t *testing.T) {
	for _, change := range []string{"thread", "workspace", "close"} {
		t.Run(change, func(t *testing.T) {
			s, client, dp, key, content := reviewCommentsSetup(t)
			started, release := make(chan struct{}), make(chan struct{})
			client.get = func(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ReviewSnapshot{}, ctx.Err()
				}
				return client.FakeClient.ReviewGet(ctx, tid, scope, turn, path)
			}
			addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "固定来源")
			reviewCommentsAddToConversation(s, dp, "t1")
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("没有重新校验评论")
			}
			switch change {
			case "thread":
				s.selectThread("t2")
			case "workspace":
				s.mu.Lock()
				s.projects[0].Path = "/other-workspace"
				s.mu.Unlock()
			case "close":
				s.close()
			}
			close(release)
			if change == "thread" {
				request := receiveReviewCommentUpload(t, client)
				request.finish <- nil
				waitReviewCommentsAdded(t, s, dp, "t1", 1)
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.attachments) != 0 || len(s.drafts["t1"].Attachments) != 1 {
					t.Fatal("校验期间切会话导致附件串入当前会话")
				}
				return
			}
			waitReviewComments(t, func() bool { diffPanelMu.Lock(); defer diffPanelMu.Unlock(); return !dp.commentBatch })
			select {
			case request := <-client.uploads:
				request.finish <- nil
				t.Fatal("来源失效后仍上传评论")
			default:
			}
		})
	}
}

func TestReviewReadMarkerInvalidatesOnContentChange(t *testing.T) {
	key := reviewDiffKey{workspace: "/workspace", scope: "turn", path: "a.go", head: "head"}
	file := ReviewFile{Path: "a.go", NewHash: "one"}
	dp := &diffPanelState{}
	readKey := reviewReadKey(key, file)
	toggleReviewRead(dp, readKey)
	file.NewHash = "two"
	if slices.Contains(dp.readKeys, reviewReadKey(key, file)) {
		t.Fatal("新内容仍被标为已读")
	}
	file.NewHash, file.OldHash = "", "deleted-one"
	oldKey := reviewReadKey(key, file)
	file.OldHash = "deleted-two"
	if oldKey == reviewReadKey(key, file) {
		t.Fatal("删除文件未使用旧侧指纹")
	}
	otherWorkspace := key
	otherWorkspace.workspace = "/other"
	if readKey == reviewReadKey(otherWorkspace, ReviewFile{NewHash: "one"}) {
		t.Fatal("已读跨工作区串用")
	}
}

func TestReviewReadMarkerPersistsPerScope(t *testing.T) {
	s, _, dp, key, _ := reviewCommentsSetup(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	WithUIStatePersistence()(s)
	file := ReviewFile{Path: "a.go", NewHash: "same"}
	turnKey := reviewReadKey(key, file)
	key.scope = "session"
	sessionKey := reviewReadKey(key, file)
	toggleReviewRead(dp, turnKey)
	rememberReviewPreference(s, dp, "t1")
	waitUIStateWrites()
	if err := saveUIState(UIState{SidebarWidth: 300}); err != nil {
		t.Fatal(err)
	}
	restored := &diffPanelState{}
	loadReviewPreference(s, restored, "t1")
	if !slices.Contains(restored.readKeys, turnKey) || slices.Contains(restored.readKeys, sessionKey) {
		t.Fatal("已读没有恢复或跨作用域串用")
	}
	toggleReviewRead(restored, turnKey)
	rememberReviewPreference(s, restored, "t1")
	waitUIStateWrites()
	again := &diffPanelState{}
	loadReviewPreference(s, again, "t1")
	if len(again.readKeys) != 0 {
		t.Fatal("取消已读未持久化")
	}
}

func TestReviewSelectedFileRestored(t *testing.T) {
	s, client, dp, _, _ := reviewCommentsSetup(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	WithUIStatePersistence()(s)
	selectReviewFile(s, dp, "t1", "a.go")
	waitUIStateWrites()
	restarted := newChatState(client)
	restarted.selectedThreadID = "t1"
	WithUIStatePersistence()(restarted)
	t.Cleanup(restarted.close)
	restored := getDiffPanelState(restarted)
	restarted.loadDiff("t1", "")
	waitReviewComments(t, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return restored.requestGen > 0 && !restored.loading && restored.selectedPath == "a.go"
	})
	data, err := os.ReadFile(uiStatePath())
	if err != nil || strings.Contains(string(data), "OldText") || strings.Contains(string(data), "var a") || strings.Contains(string(data), "comments") {
		t.Fatalf("偏好保存了正文或评论: %s %v", data, err)
	}
}

func TestReviewReadMarkerEvictsOldest(t *testing.T) {
	dp := &diffPanelState{}
	for i := 0; i <= reviewReadMarkerLimit; i++ {
		toggleReviewRead(dp, fmt.Sprint(i))
	}
	if len(dp.readKeys) != reviewReadMarkerLimit || dp.readKeys[0] != "1" || dp.readKeys[len(dp.readKeys)-1] != fmt.Sprint(reviewReadMarkerLimit) {
		t.Fatal("没有按最旧顺序限制已读条目")
	}
}

func TestReviewCommentPopoverAndReadButton(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	_, tt, dp := newReviewTester(t, fake)
	clickText(t, tt, "标记为已读")
	diffPanelMu.Lock()
	selected := dp.selectedPath
	readCount := len(dp.readKeys)
	diffPanelMu.Unlock()
	if selected != "" || readCount != 1 {
		t.Fatal("勾选已读不应选择文件或读取全文")
	}
	clickText(t, tt, "a.go")
	waitFor(t, tt, func() bool { return len(dp.views) == 1 })
	tt.Advance(DurSheet + 50*time.Millisecond)
	var view *reviewDiffView
	for _, item := range dp.views {
		view = item
	}
	if !view.state.LineSelection || view.state.OnLineSelection == nil || view.state.OnGutterButton == nil {
		t.Fatal("没有开启评论行选择")
	}
	view.state.OnGutterButton(editor.DiffLineRange{Side: editor.DiffOld, Start: 1, End: 1})
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("删除侧行内评论")
	tt.Frame()
	clickText(t, tt, "添加")
	tt.Advance(DurBase + 50*time.Millisecond)
	if !tt.HasText("删除侧行内评论") || !tt.HasText("添加到对话 1") {
		t.Fatal("评论注解或批量入口不可见")
	}
	diffPanelMu.Lock()
	if len(dp.comments) != 1 || dp.comments[0].lines.Side != editor.DiffOld {
		t.Error("浮层评论丢失旧侧坐标")
	}
	diffPanelMu.Unlock()
	clickText(t, tt, "展开评论")
	tt.Advance(DurBase + 50*time.Millisecond)
	if len(view.state.Annotations) != 1 || view.state.Annotations[0].Height != ControlLG*4 {
		t.Fatal("评论没有展开")
	}
	clickText(t, tt, "收起评论")
	tt.Advance(DurBase + 50*time.Millisecond)
	clickText(t, tt, "删除评论")
	tt.Advance(DurBase + 50*time.Millisecond)
	if tt.HasText("添加到对话 1") {
		t.Fatal("删除后批量入口应消失")
	}
}

func TestReviewReadButtonReduceMotion(t *testing.T) {
	s, _, dp, _, _ := reviewCommentsSetup(t)
	snapshot, _ := reviewFixture()
	file := snapshot.Files[0]
	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderReviewReadButton(c, s, dp, "t1", snapshot, file, "test")
	}, 320, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Frame()
	clickText(t, tt, "标记为已读")
	tt.Frame()
	if _, ok := tt.Find("标记为未读"); !ok || len(dp.readKeys) != 1 {
		t.Fatal("减少动效时已读状态没有直接完成")
	}
}
