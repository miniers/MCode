package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type inspectorUploadRequest struct {
	name, mime string
	data       []byte
	finish     chan error
}

type inspectorContextClient struct {
	*FakeClient
	requests chan inspectorUploadRequest
}

func (f *inspectorContextClient) UploadAttachment(ctx context.Context, name, mime string, data []byte) (Attachment, error) {
	req := inspectorUploadRequest{name: name, mime: mime, data: append([]byte(nil), data...), finish: make(chan error, 1)}
	f.requests <- req
	select {
	case err := <-req.finish:
		if err != nil {
			return Attachment{}, err
		}
		return f.FakeClient.UploadAttachment(ctx, name, mime, data)
	case <-time.After(5 * time.Second):
		return Attachment{}, errors.New("测试未结束上传")
	}
}

func inspectorContextSetup(t *testing.T) (*chatState, *inspectorContextClient) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	client := &inspectorContextClient{FakeClient: treeTestBase(), requests: make(chan inspectorUploadRequest, 64)}
	s := newChatState(client)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/workspace"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}, {ID: "t2", ProjectID: "p1"}}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.threadID = "t1"
	ensureFileTreeLocked(st)
	fileManagerMu.Unlock()
	t.Cleanup(s.close)
	return s, client
}

func inspectorUpload(t *testing.T, client *inspectorContextClient) inspectorUploadRequest {
	t.Helper()
	select {
	case req := <-client.requests:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("未收到上传请求")
		return inspectorUploadRequest{}
	}
}

func waitInspectorContext(t *testing.T, s *chatState, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := check()
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("附件状态未达到预期")
}

func TestInspectorContextAddsWithoutSending(t *testing.T) {
	s, client := inspectorContextSetup(t)
	s.draft = "保留正文"
	ref := inspectorFileReference(s, "t1", "assets/picture.png", 2, 4)
	s.addInspectorFileRefs([]inspectorFileRef{ref})
	req := inspectorUpload(t, client)
	if req.name != "assets/picture.png:2-4.txt" || req.mime != "text/plain" || !strings.Contains(string(req.data), `"assets/picture.png"`) || !strings.Contains(string(req.data), "2-4") {
		t.Fatalf("引用文本 = %+v", req)
	}
	req.finish <- nil
	waitInspectorContext(t, s, func() bool { return len(s.attachments) == 1 && s.attachments[0].ID != "" })
	s.mu.Lock()
	if s.draft != "保留正文" || attachmentTokensFor(s.attachments[0]) != estimateAttachmentTokens("text/plain", req.data, len(req.data)) {
		t.Error("引用改变正文或未经过 token 估算")
	}
	s.mu.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sendCalls) != 0 || len(client.sendWithAttachCalls) != 0 {
		t.Fatal("添加附件触发了发送")
	}
}

func TestInspectorContextImagePathUploadsAsText(t *testing.T) {
	s, client := inspectorContextSetup(t)
	ref := inspectorFileReference(s, "t1", "assets/picture.png", 0, 0)
	s.addInspectorFileRefs([]inspectorFileRef{ref})
	req := inspectorUpload(t, client)
	if req.name != "assets/picture.png.txt" || req.mime != "text/plain" {
		t.Fatalf("图片引用不能被当作图片上传：%+v", req)
	}
	req.finish <- nil
	waitInspectorContext(t, s, func() bool { return len(s.attachments) == 1 && s.attachments[0].ID != "" })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attachments[0].Name != "assets/picture.png" || s.attachments[0].MimeType != "text/plain" {
		t.Fatal("引用展示名称不再是原路径")
	}
}

func TestInspectorContextStaysInOriginThread(t *testing.T) {
	for _, returnBeforeUpload := range []bool{false, true} {
		t.Run(fmt.Sprint(returnBeforeUpload), func(t *testing.T) {
			s, client := inspectorContextSetup(t)
			ref := inspectorFileReference(s, "t1", "a.txt", 0, 0)
			s.addInspectorFileRefs([]inspectorFileRef{ref})
			req := inspectorUpload(t, client)
			s.selectThread("t2")
			if returnBeforeUpload {
				s.selectThread("t1")
			}
			req.finish <- nil
			if returnBeforeUpload {
				waitInspectorContext(t, s, func() bool { return len(s.attachments) == 1 && s.attachments[0].ID != "" })
				s.selectThread("t2")
			}
			waitInspectorContext(t, s, func() bool {
				atts := s.drafts["t1"].Attachments
				return len(atts) == 1 && atts[0].ID != "" && !atts[0].Uploading && len(s.attachments) == 0
			})
			s.selectThread("t1")
			waitInspectorContext(t, s, func() bool { return len(s.attachments) == 1 && s.attachments[0].ID != "" })
		})
	}
}

func TestInspectorContextFailureKeepsSource(t *testing.T) {
	s, client := inspectorContextSetup(t)
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.selection.Add("a.txt")
	fileManagerMu.Unlock()
	s.addInspectorFileRefs([]inspectorFileRef{inspectorFileReference(s, "t1", "a.txt", 0, 0)})
	req := inspectorUpload(t, client)
	s.selectThread("t2")
	req.finish <- errors.New("上传失败")
	waitInspectorContext(t, s, func() bool {
		atts := s.drafts["t1"].Attachments
		return len(atts) == 1 && atts[0].Err != "" && len(atts[0].RetryData) > 0 && len(s.attachments) == 0
	})
	fileManagerMu.Lock()
	selected := st.tree.selection.Has("a.txt")
	fileManagerMu.Unlock()
	if !selected {
		t.Fatal("失败清除了来源选择")
	}
	s.selectThread("t1")
	s.mu.Lock()
	localID := s.attachments[0].LocalID
	s.mu.Unlock()
	if !s.retryAttachment(localID) {
		t.Fatal("引用不能重试")
	}
	retry := inspectorUpload(t, client)
	if retry.name != req.name || retry.mime != req.mime || string(retry.data) != string(req.data) {
		t.Fatal("重试改变了引用")
	}
	retry.finish <- nil
	waitInspectorContext(t, s, func() bool { return s.attachments[0].ID != "" && s.attachments[0].Err == "" })
}

func TestInspectorContextRespectsAttachmentLimits(t *testing.T) {
	for _, totalBytes := range []bool{false, true} {
		t.Run(fmt.Sprint(totalBytes), func(t *testing.T) {
			s, client := inspectorContextSetup(t)
			s.mu.Lock()
			if totalBytes {
				s.attachments = []composerAttachment{{LocalID: "existing", Size: maxTotalAttachmentBytes}}
			}
			s.mu.Unlock()
			if !totalBytes {
				limit := s.attachmentLimit()
				s.mu.Lock()
				s.attachments = make([]composerAttachment, limit)
				s.mu.Unlock()
			}
			s.addInspectorFileRefs([]inspectorFileRef{inspectorFileReference(s, "t1", "a.txt", 0, 0)})
			if s.attachmentNotice() == "" || len(client.requests) != 0 {
				t.Fatal("超限引用未被拒绝")
			}
		})
	}
}

func TestInspectorContextDeduplicates(t *testing.T) {
	s, client := inspectorContextSetup(t)
	ref := inspectorFileReference(s, "t1", "a.txt", 0, 0)
	s.addInspectorFileRefs([]inspectorFileRef{ref, ref})
	req := inspectorUpload(t, client)
	s.addInspectorFileRefs([]inspectorFileRef{ref})
	req.finish <- nil
	waitInspectorContext(t, s, func() bool { return len(s.attachments) == 1 && s.attachments[0].ID != "" })
	s.addInspectorFileRefs([]inspectorFileRef{ref})
	ranged := ref
	ranged.First, ranged.Last = 1, 2
	s.addInspectorFileRefs([]inspectorFileRef{ranged})
	inspectorUpload(t, client).finish <- nil
	waitInspectorContext(t, s, func() bool { return len(s.attachments) == 2 && s.attachments[1].ID != "" })
	if len(client.requests) != 0 {
		t.Fatal("重复引用触发了上传")
	}
}

func TestInspectorContextRejectsStaleOrInvalidReference(t *testing.T) {
	s, client := inspectorContextSetup(t)
	base := inspectorFileReference(s, "t1", "a.txt", 0, 0)
	refs := []inspectorFileRef{}
	for _, path := range []string{"", ".", "../secret", "/absolute", "a/../secret", ".git/config", "a\\b", "a\nb"} {
		ref := base
		ref.Path = path
		refs = append(refs, ref)
	}
	wrongThread, wrongIdentity, invalidRange := base, base, base
	wrongThread.ThreadID = "t2"
	wrongIdentity.Identity = "changed"
	invalidRange.First, invalidRange.Last = 3, 2
	refs = append(refs, wrongThread, wrongIdentity, invalidRange)
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.entries["protected"] = FileEntry{Path: "protected", Protected: true}
	fileManagerMu.Unlock()
	protected := base
	protected.Path = "protected/child"
	refs = append(refs, protected)
	s.addInspectorFileRefs(refs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 0 || len(client.requests) != 0 {
		t.Fatal("无效引用被添加")
	}
}
