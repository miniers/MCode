package mcodeui

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码测试 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

func waitAttachmentsUploaded(t *testing.T, s *chatState, wantCount int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := len(s.attachments) == wantCount
		if ok {
			for _, a := range s.attachments {
				if a.Uploading || a.Err != "" || a.ID == "" {
					ok = false
					break
				}
			}
		}
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("等待附件上传完成超时: got %d (%+v), want %d", len(s.attachments), s.attachments, wantCount)
}

func TestComposerContextDraftClassifyAndPreview(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	reviewRef := &inspectorFileRef{ThreadID: "t1", Identity: "ws\x00comments:tag", Path: "main.go", First: 1, Last: 2}
	selRef := &inspectorFileRef{ThreadID: "t1", Identity: "ws\x00main", Path: "src/app.go", First: 10, Last: 20}

	if !s.addAttachmentToThread("t1", "评论.txt", "text/plain", []byte("第一条审阅意见"), reviewRef) {
		t.Fatal("添加 评论.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "浏览器标注.txt", "text/plain", []byte("按钮对齐有偏差"), nil) {
		t.Fatal("添加 浏览器标注.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "浏览器标注.png", "image/png", testPNGBytes(t), nil) {
		t.Fatal("添加 浏览器标注.png 失败")
	}
	if !s.addAttachmentToThread("t1", "console.txt", "text/plain", []byte("TypeError: undefined"), nil) {
		t.Fatal("添加 console.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "src/app.go:10-20", "text/plain", []byte("工作区相对路径：\"src/app.go\"\n行范围：10-20\n"), selRef) {
		t.Fatal("添加 代码选区 失败")
	}
	if !s.addAttachmentToThread("t1", "notes.md", "text/markdown", []byte("# 说明\n普通文本上下文"), nil) {
		t.Fatal("添加 notes.md 失败")
	}
	// 超出 256KB 的文本附件应截断快照，且截断在合法 UTF-8 边界。
	oversized := strings.Repeat("甲", composerPreviewTextLimit/3+100)
	if !s.addAttachmentToThread("t1", "large.txt", "text/plain", []byte(oversized), nil) {
		t.Fatal("添加 large.txt 失败")
	}

	waitAttachmentsUploaded(t, s, 7)

	s.mu.Lock()
	atts := append([]composerAttachment(nil), s.attachments...)
	s.mu.Unlock()

	wantKinds := map[string]string{
		"评论.txt":           contextKindReview,
		"浏览器标注.txt":        contextKindAnnotation,
		"浏览器标注.png":        contextKindAnnotation,
		"console.txt":      contextKindConsole,
		"src/app.go:10-20": contextKindSelection,
		"notes.md":         contextKindText,
		"large.txt":        contextKindText,
	}
	for _, a := range atts {
		wantKind := wantKinds[a.Name]
		if a.ContextKind != wantKind {
			t.Fatalf("%s ContextKind = %q, want %q", a.Name, a.ContextKind, wantKind)
		}
		if a.Name == "浏览器标注.png" {
			if a.PreviewText != "" || len(a.Data) == 0 {
				t.Fatalf("图片附件不应保留 PreviewText 且应保留 Data")
			}
			continue
		}
		if a.PreviewText == "" {
			t.Fatalf("%s 应保留 PreviewText", a.Name)
		}
		if a.Data != nil {
			t.Fatalf("%s 非图片附件 Data 应保持 nil", a.Name)
		}
		if a.Name == "large.txt" && len(a.PreviewText) > composerPreviewTextLimit {
			t.Fatalf("large.txt PreviewText 超过上限: %d", len(a.PreviewText))
		}
	}

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, okBtn := tt.Find("打开上下文草稿区")
		_, okChip := tt.Find("附件 评论.txt")
		return okBtn && okChip
	})

	if err := tt.Click("打开上下文草稿区"); err != nil {
		t.Fatalf("点击打开上下文草稿区失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭上下文草稿区")
		return ok
	})

	for _, expected := range []string{
		"审阅评论",
		"浏览器标注",
		"控制台日志",
		"代码选区",
		"第一条审阅意见",
		"按钮对齐有偏差",
		"TypeError: undefined",
		"src/app.go:10-20",
		"notes.md",
	} {
		if !tt.HasText(expected) {
			t.Fatalf("草稿区弹窗缺少预期内容 %q，当前文本: %v", expected, tt.Texts())
		}
	}

	// 验证关闭弹窗
	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("点击关闭失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭上下文草稿区")
		return !ok
	})
}

func TestComposerContextDraftEditReuploadAndRemove(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	if !s.addAttachmentToThread("t1", "评论.txt", "text/plain", []byte("原审阅意见"), nil) {
		t.Fatal("添加 评论.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "console.txt", "text/plain", []byte("ERROR: boom"), nil) {
		t.Fatal("添加 console.txt 失败")
	}
	waitAttachmentsUploaded(t, s, 2)

	s.mu.Lock()
	oldID := s.attachments[0].ID
	s.mu.Unlock()

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开上下文草稿区")
		return ok
	})
	if err := tt.Click("打开上下文草稿区"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭上下文草稿区")
		return ok
	})

	if err := tt.Click("编辑 评论.txt"); err != nil {
		t.Fatalf("点击编辑失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("编辑 评论.txt")
		return ok
	})
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("修改后的审阅意见")
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点击保存失败: %v", err)
	}

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 2 &&
			!s.attachments[0].Uploading &&
			s.attachments[0].ID != "" &&
			s.attachments[0].ID != oldID &&
			s.attachments[0].PreviewText == "修改后的审阅意见"
	})

	fake.mu.Lock()
	lastUpload := fake.uploadCalls[len(fake.uploadCalls)-1]
	fake.mu.Unlock()
	if string(lastUpload.Data) != "修改后的审阅意见" {
		t.Fatalf("重新上传的数据不正确: %q", string(lastUpload.Data))
	}

	if err := tt.Click("移除 console.txt"); err != nil {
		t.Fatalf("点击移除失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && s.attachments[0].Name == "评论.txt"
	})
}

func TestComposerContextDraftMergeAndClear(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	if !s.addAttachmentToThread("t1", "评论.txt", "text/plain", []byte("审阅意见A"), nil) {
		t.Fatal("添加 评论.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "console.txt", "text/plain", []byte("控制台报错B"), nil) {
		t.Fatal("添加 console.txt 失败")
	}
	if !s.addAttachmentToThread("t1", "浏览器标注.png", "image/png", testPNGBytes(t), nil) {
		t.Fatal("添加 浏览器标注.png 失败")
	}
	waitAttachmentsUploaded(t, s, 3)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开上下文草稿区")
		return ok
	})
	if err := tt.Click("打开上下文草稿区"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("合并为一份")
		return ok
	})
	if err := tt.Click("合并为一份"); err != nil {
		t.Fatalf("点击合并失败: %v", err)
	}

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.attachments) != 2 {
			return false
		}
		for _, a := range s.attachments {
			if a.Uploading || a.ID == "" {
				return false
			}
		}
		return true
	})

	s.mu.Lock()
	var hasImage, hasMerged bool
	var mergedText string
	for _, a := range s.attachments {
		if a.Name == "浏览器标注.png" && isAllowedImageMime(a.MimeType) {
			hasImage = true
		}
		if a.Name == "上下文草稿.txt" {
			hasMerged = true
			mergedText = a.PreviewText
		}
	}
	s.mu.Unlock()

	if !hasImage {
		t.Fatal("合并后应保留图片附件 浏览器标注.png")
	}
	if !hasMerged {
		t.Fatal("合并后应生成 上下文草稿.txt")
	}
	for _, part := range []string{"## 审阅评论", "审阅意见A", "## 控制台日志", "控制台报错B"} {
		if !strings.Contains(mergedText, part) {
			t.Fatalf("合并内容缺少 %q: %q", part, mergedText)
		}
	}

	if err := tt.Click("清空全部"); err != nil {
		t.Fatalf("点击清空全部失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		empty := len(s.attachments) == 0 && len(s.snippets) == 0
		s.mu.Unlock()
		_, open := tt.Find("关闭上下文草稿区")
		return empty && !open
	})
}

func TestComposerContextDraftThreadSwitchIsolation(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}, {ID: "t2"}}

	if !s.addAttachmentToThread("t1", "评论.txt", "text/plain", []byte("线程1评论"), nil) {
		t.Fatal("添加 t1 评论.txt 失败")
	}
	waitAttachmentsUploaded(t, s, 1)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开上下文草稿区")
		return ok
	})
	if err := tt.Click("打开上下文草稿区"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭上下文草稿区")
		return ok
	})

	// 切换到线程 t2：草稿弹窗自动关闭，且 t1 附件被隔离在 t1 草稿中。
	s.selectThread("t2")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		tid := s.selectedThreadID
		cnt := len(s.attachments)
		s.mu.Unlock()
		_, open := tt.Find("关闭上下文草稿区")
		return tid == "t2" && cnt == 0 && !open
	})

	if !s.addAttachmentToThread("t2", "console.txt", "text/plain", []byte("线程2日志"), nil) {
		t.Fatal("添加 t2 console.txt 失败")
	}
	waitAttachmentsUploaded(t, s, 1)

	// 切回 t1：应恢复 t1 的 PreviewText 与 ContextKind。
	s.selectThread("t1")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == "t1" && len(s.attachments) == 1 && s.attachments[0].Name == "评论.txt"
	})

	s.mu.Lock()
	att1 := s.attachments[0]
	s.mu.Unlock()
	if att1.ContextKind != contextKindReview || att1.PreviewText != "线程1评论" {
		t.Fatalf("切回 t1 后附件元数据未保留: %+v", att1)
	}

	// 再切回 t2：应恢复 t2 的 console.txt。
	s.selectThread("t2")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == "t2" && len(s.attachments) == 1 && s.attachments[0].Name == "console.txt"
	})
	s.mu.Lock()
	att2 := s.attachments[0]
	s.mu.Unlock()
	if att2.ContextKind != contextKindConsole || att2.PreviewText != "线程2日志" {
		t.Fatalf("切回 t2 后附件元数据未保留: %+v", att2)
	}
}
