package mcodeui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

type attachPreviewMediaClient struct {
	*FakeClient
	mu    sync.Mutex
	media map[string]struct {
		mime string
		data []byte
		err  error
	}
}

func newAttachPreviewMediaClient() *attachPreviewMediaClient {
	return &attachPreviewMediaClient{
		FakeClient: NewFakeClient(),
		media: make(map[string]struct {
			mime string
			data []byte
			err  error
		}),
	}
}

func (c *attachPreviewMediaClient) setMedia(id, mime string, data []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.media[id] = struct {
		mime string
		data []byte
		err  error
	}{mime: mime, data: data, err: err}
}

func (c *attachPreviewMediaClient) ReadMessageMedia(ctx context.Context, threadID, messageID, mediaID, variant string) (string, []byte, error) {
	c.mu.Lock()
	entry, ok := c.media[mediaID]
	c.mu.Unlock()
	if !ok {
		return "", nil, fmt.Errorf("not found: %s", mediaID)
	}
	return entry.mime, entry.data, entry.err
}

func TestAttachPreviewClassify(t *testing.T) {
	cases := []struct {
		name            string
		fileName        string
		mime            string
		text            string
		isSnippet       bool
		hasInspectorRef bool
		want            attachPreviewKind
	}{
		{name: "go", fileName: "main.go", mime: "text/plain", want: attachPreviewCode},
		{name: "go with range", fileName: "internal/ui/app.go:10-20", mime: "text/plain", hasInspectorRef: true, want: attachPreviewCode},
		{name: "json", fileName: "config.json", mime: "application/json", want: attachPreviewCode},
		{name: "md", fileName: "README.md", mime: "text/markdown", want: attachPreviewMarkdown},
		{name: "log", fileName: "server.log", mime: "text/plain", want: attachPreviewLog},
		{name: "log-like txt", fileName: "console.txt", mime: "text/plain", text: "INFO start\nERROR fail 1\nWARN slow\nERROR fail 2", want: attachPreviewLog},
		{name: "pdf", fileName: "spec.pdf", mime: "application/pdf", want: attachPreviewDoc},
		{name: "docx", fileName: "report.docx", mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", want: attachPreviewDoc},
		{name: "bin", fileName: "dump.bin", mime: "application/octet-stream", want: attachPreviewBinary},
		{name: "zip as archive", fileName: "bundle.zip", mime: "application/zip", want: attachPreviewArchive},
		{name: "snippet", fileName: "pasted", mime: "text/plain", isSnippet: true, want: attachPreviewSnippet},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAttachPreviewKind(tc.fileName, tc.mime, tc.text, tc.isSnippet, tc.hasInspectorRef)
			if got != tc.want {
				t.Fatalf("classifyAttachPreviewKind(%q, %q) = %v, want %v", tc.fileName, tc.mime, got, tc.want)
			}
		})
	}
}

func TestAttachPreviewTrimLinesRange(t *testing.T) {
	src := "line1\nline2\nline3\nline4\nline5"

	got, f, l := trimLinesRange(src, 2, 4)
	if got != "line2\nline3\nline4" || f != 2 || l != 4 {
		t.Fatalf("normal range got (%q, %d, %d)", got, f, l)
	}

	// 反向选区自动规范化
	got, f, l = trimLinesRange(src, 4, 2)
	if got != "line2\nline3\nline4" || f != 2 || l != 4 {
		t.Fatalf("reversed range got (%q, %d, %d)", got, f, l)
	}

	// 越界索引钳位
	got, f, l = trimLinesRange(src, -3, 99)
	if got != src || f != 1 || l != 5 {
		t.Fatalf("clamped range got (%q, %d, %d)", got, f, l)
	}

	// 空文本边界
	got, f, l = trimLinesRange("", 1, 3)
	if got != "" || f != 0 || l != 0 {
		t.Fatalf("empty range got (%q, %d, %d)", got, f, l)
	}
}

func TestAttachPreviewTrimLogAroundErrors(t *testing.T) {
	// 无报错
	noErr := "INFO boot\nINFO ready\nINFO done"
	if got, matched := trimLogAroundErrors(noErr, 50); matched || got != noErr {
		t.Fatalf("no error case got matched=%v got=%q", matched, got)
	}

	// 边界：总行数少于 50 行且报错在首行或末行
	short := "ERROR first\nline2\nline3"
	got, matched := trimLogAroundErrors(short, 50)
	if !matched || got != short {
		t.Fatalf("boundary short log got matched=%v got=%q", matched, got)
	}

	// 多处邻近报错合并（不重复行）
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("INFO line %d", i+1)
	}
	lines[5] = "ERROR near top"     // [0..55]
	lines[70] = "ERROR second near" // [20..120] -> 与 [0..55] 合并为 [0..120]
	lines[250] = "ERROR far bottom" // [200..299] -> 独立区间
	mergedSrc := strings.Join(lines, "\n")

	got, matched = trimLogAroundErrors(mergedSrc, 50)
	if !matched {
		t.Fatal("expected matched=true")
	}
	if strings.Count(got, "INFO line 40") != 1 {
		t.Fatalf("overlapping intervals should merge without duplicates, got count=%d", strings.Count(got, "INFO line 40"))
	}
	if !strings.Contains(got, "ERROR near top") || !strings.Contains(got, "ERROR second near") || !strings.Contains(got, "ERROR far bottom") {
		t.Fatalf("missing expected error lines in %q", got)
	}
	if strings.Contains(got, "INFO line 150") {
		t.Fatalf("middle non-error line 150 should have been omitted")
	}
	if !strings.Contains(got, "已省略") {
		t.Fatalf("expected omission marker between disjoint error blocks")
	}
}

func TestAttachPreviewSourcePathRecorded(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(filePath, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.addAttachmentFiles([]string{filePath})
	waitAttachmentsUploaded(t, s, 1)

	s.mu.Lock()
	gotDiskPath := s.attachments[0].SourcePath
	s.mu.Unlock()
	if gotDiskPath != filePath {
		t.Fatalf("disk attachment SourcePath = %q, want %q", gotDiskPath, filePath)
	}

	if !s.addAttachment("memory.txt", "text/plain", []byte("in memory")) {
		t.Fatal("addAttachment failed")
	}
	waitAttachmentsUploaded(t, s, 2)
	s.mu.Lock()
	gotMemPath := s.attachments[1].SourcePath
	s.mu.Unlock()
	if gotMemPath != "" {
		t.Fatalf("memory attachment SourcePath = %q, want empty", gotMemPath)
	}
}

func TestAttachPreviewPendingCodeSelectAndTrim(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	code := "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n"
	if !s.addAttachment("hello.go", "text/plain", []byte(code)) {
		t.Fatal("addAttachment failed")
	}
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 hello.go")
		return ok
	})
	if err := tt.Click("附件 hello.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("选择第 2 行")
		return ok
	})

	if err := tt.Click("选择第 2 行"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "选择第 4 行"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("只附带第 2–4 行")
		return ok
	})

	if err := tt.Click("只附带第 2–4 行"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && !s.attachments[0].Uploading && s.attachments[0].Name == "hello.go:2-4"
	})

	s.mu.Lock()
	att := s.attachments[0]
	s.mu.Unlock()
	if att.PreviewText != "\nfunc hello() {\n\tprintln(\"hi\")" {
		t.Fatalf("trimmed PreviewText = %q", att.PreviewText)
	}
}

func TestAttachPreviewCarouselNavigation(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	if !s.addAttachment("first.go", "text/plain", []byte("package first\n")) {
		t.Fatal("add first.go failed")
	}
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 first.go")
		return ok
	})

	// 单个附件时不显示导航按钮
	if err := tt.Click("附件 first.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭预览")
		return ok
	})
	if _, ok := tt.Find("下一个"); ok {
		t.Fatal("single attachment should not show carousel navigation")
	}
	if err := tt.Click("关闭预览"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭预览")
		return !ok
	})

	// 多个附件时支持 → 切换
	if !s.addAttachment("second.md", "text/markdown", []byte("# Second Doc\nhello")) {
		t.Fatal("add second.md failed")
	}
	waitAttachmentsUploaded(t, s, 2)

	if err := tt.Click("附件 first.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("1 / 2") && tt.HasText("package first")
	})

	if err := tt.Click("下一个"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("2 / 2") && tt.HasText("Second Doc")
	})
}

func TestAttachPreviewReadOnlySentAttachment(t *testing.T) {
	testenv.TempHome(t)
	client := newAttachPreviewMediaClient()
	client.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj", Path: t.TempDir()}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "Sent", UpdatedAt: time.Now()}}},
	)
	refText := inspectorRefContent("internal/demo.go", 1, 3) + "package demo\nfunc run() {}\n"
	logText := "INFO boot\nERROR crash\nINFO end"
	client.setMedia("med-ref", "text/plain", []byte(refText), nil)
	client.setMedia("med-log", "text/plain", []byte(logText), nil)
	client.messages["t1"] = []Message{
		{
			ID:        "m1",
			Role:      "user",
			Text:      "see files",
			TurnIndex: 1,
			IsFinal:   true,
			Media: []MessageMedia{
				{ID: "med-ref", Name: "internal/demo.go:1-3.txt", MimeType: "text/plain"},
				{ID: "med-log", Name: "run.log", MimeType: "text/plain"},
			},
		},
	}

	tt := ui.NewTester(Root(client), 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("预览附件 run.log")
		return ok
	})

	if err := tt.Click("预览附件 run.log"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("2 / 2") && tt.HasText("ERROR crash")
	})
	if tt.HasText("只留报错前后 50 行") {
		t.Fatal("read-only sent attachment must not show trim button")
	}

	// 切换到工作区引用项：仍保留「在右侧打开」，不显示修改裁剪按钮
	if err := tt.Click("上一个"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("1 / 2") && tt.HasText("在右侧打开")
	})
	if err := tt.Click("选择第 1 行"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("只附带第") {
		t.Fatal("read-only sent workspace ref must not show trim button")
	}
}

func TestAttachPreviewBinarySHA256(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	payload := []byte{0x00, 0x11, 0x22, 0x33, 0xaa, 0xbb, 0xcc, 0xdd}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])

	s.addAttachmentFiles([]string{binPath})
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 blob.bin")
		return ok
	})
	if err := tt.Click("附件 blob.bin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText(wantSHA) && tt.HasText("模型只读取该文件的本地路径")
	})
}

func TestAttachPreviewMotionEnds(t *testing.T) {
	testenv.TempHome(t)
	setAppReduceMotion(t, false)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	if !s.addAttachment("readme.md", "text/markdown", []byte("# Title\nbody")) {
		t.Fatal("add readme.md failed")
	}
	if !s.addAttachment("main.go", "text/plain", []byte("package main\n")) {
		t.Fatal("add main.go failed")
	}
	waitAttachmentsUploaded(t, s, 2)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 readme.md")
		return ok
	})
	if err := tt.Click("附件 readme.md"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("关闭预览")
		return ok
	})

	// 推进时钟使弹窗升起动画结束
	tt.Advance(DurSlow + 100*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("modal rise animation did not settle after DurSlow")
	}

	// 切换项动画结束后不再请求下一帧
	if err := tt.Click("下一个"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurFast + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("item switch animation did not settle after DurFast")
	}

	// 切回 Markdown 并切换原文/排版，动画结束后不再请求下一帧
	if err := tt.Click("上一个"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurFast + 50*time.Millisecond)
	tt.Frame()
	if err := tt.Click("查看 / 编辑原文"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurFast + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("markdown toggle animation did not settle after DurFast")
	}
}

func TestAttachPreviewArchiveClassify(t *testing.T) {
	cases := []struct {
		fileName string
		mime     string
		want     attachPreviewKind
	}{
		{fileName: "bundle.zip", mime: "application/zip", want: attachPreviewArchive},
		{fileName: "release.tar.gz", mime: "application/gzip", want: attachPreviewArchive},
		{fileName: "backup.tgz", mime: "application/octet-stream", want: attachPreviewArchive},
		{fileName: "archive.tar", mime: "application/x-tar", want: attachPreviewArchive},
	}
	for _, tc := range cases {
		if got := classifyAttachPreviewKind(tc.fileName, tc.mime, "", false, false); got != tc.want {
			t.Fatalf("classifyAttachPreviewKind(%q, %q) = %v, want %v", tc.fileName, tc.mime, got, tc.want)
		}
	}
}

func TestAttachPreviewArchivePendingExtractAndMotion(t *testing.T) {
	testenv.TempHome(t)
	setAppReduceMotion(t, false)

	origReveal := attachPreviewRevealItem
	var revealed []string
	attachPreviewRevealItem = func(p string) {
		revealed = append(revealed, p)
	}
	t.Cleanup(func() { attachPreviewRevealItem = origReveal })

	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	mainGoBody := []byte("package main\nfunc main() { println(42) }\n")
	readmeBody := []byte("# Archive Docs\nhello\n")
	zipBytes := buildTestZip(t, []testArchiveItem{
		{name: "docs/readme.md", body: readmeBody},
		{name: "src/main.go", body: mainGoBody},
		{name: "empty/", isDir: true},
	})
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	if err := os.WriteFile(zipPath, zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	s.addAttachmentFiles([]string{zipPath})
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 bundle.zip")
		return ok
	})
	if err := tt.Click("附件 bundle.zip"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("readme.md") && tt.HasText("main.go") && tt.HasText("单独附加所选文件") && tt.HasText("在 Finder 中显示")
	})

	// 加载完成并推进时钟后，动画结束不再请求下一帧。
	tt.Advance(DurSlow + 100*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("archive preview animation did not settle after loading")
	}

	// 折叠与展开目录后动画也会结束。
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("main.go")
	})
	tt.Advance(DurSnap + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("archive dir collapse animation did not settle")
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("main.go")
	})
	tt.Advance(DurSnap + 50*time.Millisecond)
	tt.Frame()

	// 未勾选前「单独附加所选文件」禁用，点击不增加附件。
	_ = tt.Click("单独附加所选文件")
	tt.Frame()
	s.mu.Lock()
	countBefore := len(s.attachments)
	s.mu.Unlock()
	if countBefore != 1 {
		t.Fatalf("expected 1 attachment before selection, got %d", countBefore)
	}

	// 勾选一个文件并点「单独附加所选文件」，待发附件多出该文件且内容正确，弹窗保持打开且清空勾选。
	if err := tt.Click("选择文件 main.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("已选 1 个文件")
	})
	if err := tt.Click("单独附加所选文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 2 && !s.attachments[1].Uploading && !tt.HasText("已选 1 个文件")
	})

	s.mu.Lock()
	extracted := s.attachments[1]
	s.mu.Unlock()
	if extracted.Name != "main.go" {
		t.Fatalf("extracted Name = %q, want %q", extracted.Name, "main.go")
	}
	if extracted.PreviewText != string(mainGoBody) {
		t.Fatalf("extracted PreviewText = %q, want %q", extracted.PreviewText, string(mainGoBody))
	}

	fake.mu.Lock()
	var uploadedBody []byte
	for _, call := range fake.uploadCalls {
		if call.Name == "main.go" {
			uploadedBody = append([]byte(nil), call.Data...)
		}
	}
	fake.mu.Unlock()
	if string(uploadedBody) != string(mainGoBody) {
		t.Fatalf("uploaded content = %q, want %q", string(uploadedBody), string(mainGoBody))
	}

	if _, ok := tt.Find("关闭预览"); !ok {
		t.Fatal("archive preview modal should remain open after extracting files")
	}

	// 验证在 Finder 中显示按钮调用 reveal。
	if err := tt.Click("在 Finder 中显示"); err != nil {
		t.Fatal(err)
	}
	if len(revealed) != 1 || revealed[0] != zipPath {
		t.Fatalf("revealed = %v, want [%q]", revealed, zipPath)
	}
}

func TestAttachPreviewArchiveReadOnlySent(t *testing.T) {
	testenv.TempHome(t)
	client := newAttachPreviewMediaClient()
	client.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj", Path: t.TempDir()}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "Sent", UpdatedAt: time.Now()}}},
	)
	zipBytes := buildTestZip(t, []testArchiveItem{
		{name: "docs/readme.md", body: []byte("# ReadOnly\n")},
		{name: "src/app.go", body: []byte("package app\n")},
	})
	client.setMedia("med-zip", "application/zip", zipBytes, nil)
	client.messages["t1"] = []Message{
		{
			ID:        "m1",
			Role:      "user",
			Text:      "see archive",
			TurnIndex: 1,
			IsFinal:   true,
			Media: []MessageMedia{
				{ID: "med-zip", Name: "release.zip", MimeType: "application/zip"},
			},
		},
	}

	tt := ui.NewTester(Root(client), 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("预览附件 release.zip")
		return ok
	})
	if err := tt.Click("预览附件 release.zip"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("readme.md") && tt.HasText("app.go")
	})

	if tt.HasText("单独附加所选文件") {
		t.Fatal("read-only archive preview must not show extract button")
	}
	if _, ok := tt.Find("选择文件 app.go"); ok {
		t.Fatal("read-only archive preview must not show checkboxes")
	}
	if _, ok := tt.Find("在 Finder 中显示"); !ok {
		t.Fatal("read-only archive preview should still show Finder button")
	}
}

func TestAttachPreviewArchiveMissingPathAndTruncated(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	// 无 SourcePath 的待发压缩包应提示无法读取。
	zipBytes := buildTestZip(t, []testArchiveItem{{name: "a.txt", body: []byte("hi")}})
	if !s.addAttachment("memory.zip", "application/zip", zipBytes) {
		t.Fatal("addAttachment memory.zip failed")
	}
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 memory.zip")
		return ok
	})
	if err := tt.Click("附件 memory.zip"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("无法读取压缩包内容")
	})
	if err := tt.Click("关闭预览"); err != nil {
		t.Fatal(err)
	}

	// 超过 2000 条的压缩包在末尾提示已截断。
	manyItems := make([]testArchiveItem, archiveListLimit+3)
	for i := range manyItems {
		manyItems[i] = testArchiveItem{
			name: fmt.Sprintf("f_%04d.txt", i),
			body: []byte("x"),
		}
	}
	manyZip := buildTestZip(t, manyItems)
	manyPath := filepath.Join(t.TempDir(), "many.zip")
	if err := os.WriteFile(manyPath, manyZip, 0o644); err != nil {
		t.Fatal(err)
	}
	s.addAttachmentFiles([]string{manyPath})
	waitAttachmentsUploaded(t, s, 2)

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 many.zip")
		return ok
	})
	if err := tt.Click("附件 many.zip"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("已截断")
	})
}

func TestAttachPreviewArchiveFlattenCached(t *testing.T) {
	testenv.TempHome(t)
	setAppReduceMotion(t, true)

	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	zipBytes := buildTestZip(t, []testArchiveItem{
		{name: "docs/readme.md", body: []byte("# Docs\n")},
		{name: "docs/guide.md", body: []byte("# Guide\n")},
		{name: "src/main.go", body: []byte("package main\n")},
	})
	zipPath := filepath.Join(t.TempDir(), "cached.zip")
	if err := os.WriteFile(zipPath, zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	s.addAttachmentFiles([]string{zipPath})
	waitAttachmentsUploaded(t, s, 1)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 cached.zip")
		return ok
	})
	if err := tt.Click("附件 cached.zip"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("readme.md") && tt.HasText("guide.md") && tt.HasText("main.go")
	})

	st := s.getOrCreateAttachmentPreviewState()
	st.mu.Lock()
	expandedCount := len(st.items[0].ArchiveRows)
	st.mu.Unlock()
	if expandedCount != 5 {
		t.Fatalf("expanded ArchiveRows = %d, want 5", expandedCount)
	}

	if err := tt.Click("docs"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("readme.md") && !tt.HasText("guide.md") && tt.HasText("main.go")
	})

	st.mu.Lock()
	collapsedCount := len(st.items[0].ArchiveRows)
	st.mu.Unlock()
	if collapsedCount != 3 {
		t.Fatalf("collapsed ArchiveRows = %d, want 3", collapsedCount)
	}

	if err := tt.Click("docs"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("readme.md") && tt.HasText("guide.md")
	})

	st.mu.Lock()
	reexpandedCount := len(st.items[0].ArchiveRows)
	st.mu.Unlock()
	if reexpandedCount != 5 {
		t.Fatalf("re-expanded ArchiveRows = %d, want 5", reexpandedCount)
	}
}
