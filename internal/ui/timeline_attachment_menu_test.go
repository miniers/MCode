package mcodeui

import (
	"fmt"
	"testing"
	"time"
)

func TestInspectorRefContentRoundTrip(t *testing.T) {
	t.Run("无行范围", func(t *testing.T) {
		raw := inspectorRefContent("foo/bar.apk", 0, 0)
		path, first, last, ok := parseInspectorRefContent([]byte(raw))
		if !ok || path != "foo/bar.apk" || first != 0 || last != 0 {
			t.Fatalf("解析无行范围失败：got=(%q, %d, %d, %v)", path, first, last, ok)
		}
	})

	t.Run("有行范围", func(t *testing.T) {
		raw := inspectorRefContent("internal/ui/app.go", 12, 34)
		path, first, last, ok := parseInspectorRefContent([]byte(raw))
		if !ok || path != "internal/ui/app.go" || first != 12 || last != 34 {
			t.Fatalf("解析有行范围失败：got=(%q, %d, %d, %v)", path, first, last, ok)
		}
	})

	t.Run("第二行之后还有代码行", func(t *testing.T) {
		raw := "工作区相对路径：\"internal/ui/git_pane_diff.go\"\n行范围：10-20（从 1 开始，含首尾）\nfunc main() {\n\treturn\n}\n"
		path, first, last, ok := parseInspectorRefContent([]byte(raw))
		if !ok || path != "internal/ui/git_pane_diff.go" || first != 10 || last != 20 {
			t.Fatalf("解析带代码行的 diff 引用失败：got=(%q, %d, %d, %v)", path, first, last, ok)
		}
	})

	t.Run("第二行不是行范围", func(t *testing.T) {
		raw := "工作区相对路径：\"a/b.txt\"\n普通内容\n"
		path, first, last, ok := parseInspectorRefContent([]byte(raw))
		if !ok || path != "a/b.txt" || first != 0 || last != 0 {
			t.Fatalf("第二行非行范围时应返回 first=last=0：got=(%q, %d, %d, %v)", path, first, last, ok)
		}
	})

	invalidCases := []struct {
		name string
		data []byte
	}{
		{name: "普通文本", data: []byte("hello world\nsecond line\n")},
		{name: "越界路径", data: []byte(fmt.Sprintf("工作区相对路径：%q\n", "../x"))},
		{name: "空路径", data: []byte(fmt.Sprintf("工作区相对路径：%q\n", ""))},
		{name: "含换行路径", data: []byte(fmt.Sprintf("工作区相对路径：%q\n", "a\nb"))},
		{name: "当前目录", data: []byte(fmt.Sprintf("工作区相对路径：%q\n", "."))},
		{name: "绝对路径", data: []byte(fmt.Sprintf("工作区相对路径：%q\n", "/abs/path"))},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if path, first, last, ok := parseInspectorRefContent(tc.data); ok {
				t.Fatalf("非法内容 %q 不应解析成功，实际 got=(%q, %d, %d)", tc.name, path, first, last)
			}
		})
	}
}

func TestTimelineFileCapsuleDisplayName(t *testing.T) {
	cases := []struct {
		name string
		med  MessageMedia
		ref  *parsedTimelineInspectorRef
		want string
	}{
		{
			name: "引用无行范围去掉txt后缀",
			med:  MessageMedia{Name: "foo/bar.apk.txt", MimeType: "text/plain"},
			ref:  &parsedTimelineInspectorRef{path: "foo/bar.apk", ok: true},
			want: "foo/bar.apk",
		},
		{
			name: "引用有行范围",
			med:  MessageMedia{Name: "internal/ui/app.go:10-20.txt", MimeType: "text/plain"},
			ref:  &parsedTimelineInspectorRef{path: "internal/ui/app.go", first: 10, last: 20, ok: true},
			want: "internal/ui/app.go:10-20",
		},
		{
			name: "普通附件显示原名",
			med:  MessageMedia{Name: "report.pdf", MimeType: "application/pdf"},
			ref:  nil,
			want: "report.pdf",
		},
		{
			name: "普通txt未命中引用显示原名",
			med:  MessageMedia{Name: "notes.txt", MimeType: "text/plain"},
			ref:  &parsedTimelineInspectorRef{ok: false},
			want: "notes.txt",
		},
		{
			name: "空名显示附件",
			med:  MessageMedia{Name: "", MimeType: "application/octet-stream"},
			ref:  nil,
			want: "附件",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := timelineFileCapsuleDisplayName(tc.med, tc.ref); got != tc.want {
				t.Fatalf("got=%q, want=%q", got, tc.want)
			}
		})
	}
}

func TestTimelineFileCapsulePrefetchAndCache(t *testing.T) {
	const (
		threadID  = "t-capsule-ref"
		messageID = "m-capsule-ref"
		mediaID   = "med-capsule-ref"
	)
	key := timelineAttachmentRefKey(threadID, messageID, mediaID)
	timelineAttachmentRefCache.Delete(key)
	t.Cleanup(func() { timelineAttachmentRefCache.Delete(key) })

	reader := &fakeMediaReader{
		Client: NewFakeClient(),
		mime:   "text/plain",
		data:   []byte(inspectorRefContent("builds/app.apk", 5, 12)),
	}
	s := &chatState{client: reader}
	med := MessageMedia{
		ID:       mediaID,
		Name:     "builds/app.apk.txt",
		MimeType: "text/plain",
	}

	for i := 0; i < 5; i++ {
		prefetchTimelineAttachmentRef(s, threadID, messageID, med)
	}

	var ref *parsedTimelineInspectorRef
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ref = cachedTimelineAttachmentRef(threadID, messageID, mediaID); ref != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if ref == nil {
		t.Fatal("等待引用附件预取缓存完成超时")
	}

	// 完成后再次触发预取，不应再次发起 ReadMessageMedia。
	prefetchTimelineAttachmentRef(s, threadID, messageID, med)

	if got := reader.count.Load(); got != 1 {
		t.Fatalf("同一个媒体多次预取只应请求一次 ReadMessageMedia，实际 %d", got)
	}
	if !ref.ok || ref.path != "builds/app.apk" || ref.first != 5 || ref.last != 12 {
		t.Fatalf("缓存解析结果不符合预期：%+v", *ref)
	}
	if got := timelineFileCapsuleDisplayName(med, ref); got != "builds/app.apk:5-12" {
		t.Fatalf("胶囊显示名 = %q, want %q", got, "builds/app.apk:5-12")
	}

	// 非 text/plain 或非 .txt 的媒体不触发引用预取。
	prefetchTimelineAttachmentRef(s, threadID, messageID, MessageMedia{
		ID:       "med-pdf",
		Name:     "doc.pdf",
		MimeType: "application/pdf",
	})
	if got := reader.count.Load(); got != 1 {
		t.Fatalf("非 text/plain+.txt 媒体不应触发引用读取，实际请求次数 %d", got)
	}
}
