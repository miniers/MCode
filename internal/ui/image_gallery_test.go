package mcodeui

import (
	"path/filepath"
	"testing"
)

// previewGalleryTestMessages 构造一份附件与正文图片混排的消息列表，
// 含非图片附件与远程图片，用来验证收集顺序与过滤规则。
// dir 由调用方给出，方便测试用同一目录的路径做断言。
func previewGalleryTestMessages(dir string) []Message {
	localA := filepath.Join(dir, "shot-a.png")
	localB := filepath.Join(dir, "shot-b.jpg")
	return []Message{
		{
			ID:   "m1",
			Role: "user",
			Media: []MessageMedia{
				{ID: "att1", Name: "photo.png", MimeType: "image/png"},
				{ID: "att2", Name: "spec.pdf", MimeType: "application/pdf"},
			},
		},
		{
			ID:   "m2",
			Role: "assistant",
			Text: "第一张：\n\n![截图](" + localA + ")\n\n远端：![远程](https://example.com/x.png)\n\n" +
				"第二张 ![再来一张](" + localB + ")",
		},
		{
			ID:   "m3",
			Role: "user",
			Media: []MessageMedia{
				{ID: "att3", Name: "chart.webp", MimeType: "image/webp"},
			},
		},
	}
}

// TestCollectPreviewGallery_MixedOrder 确认附件与正文图片按消息顺序排好，
// 非图片附件与远程 http 图片都被排除。
func TestCollectPreviewGallery_MixedOrder(t *testing.T) {
	dir := t.TempDir()
	localA := filepath.Join(dir, "shot-a.png")
	localB := filepath.Join(dir, "shot-b.jpg")

	items := collectPreviewGallery(previewGalleryTestMessages(dir))
	if len(items) != 4 {
		t.Fatalf("收集到 %d 张图片，希望 4 张：%+v", len(items), items)
	}

	type want struct {
		kind               previewGalleryKind
		messageID, mediaID string
		url                string
		name, alt          string
	}
	wants := []want{
		{kind: previewGalleryAttachment, messageID: "m1", mediaID: "att1", name: "photo.png"},
		{kind: previewGalleryMarkdown, messageID: "m2", url: localA, alt: "截图"},
		{kind: previewGalleryMarkdown, messageID: "m2", url: localB, alt: "再来一张"},
		{kind: previewGalleryAttachment, messageID: "m3", mediaID: "att3", name: "chart.webp"},
	}
	for i, w := range wants {
		got := items[i]
		if got.Kind != w.kind || got.MessageID != w.messageID || got.MediaID != w.mediaID {
			t.Fatalf("第 %d 张图片 = %+v，希望 kind=%v message=%s media=%s",
				i, got, w.kind, w.messageID, w.mediaID)
		}
		if got.URL != w.url || got.Alt != w.alt || got.Name != w.name {
			t.Fatalf("第 %d 张图片的地址/文字 = %q/%q/%q，希望 %q/%q/%q",
				i, got.URL, got.Alt, got.Name, w.url, w.alt, w.name)
		}
	}

	// 正文图片应当解析出本地路径，附件没有本地路径。
	if items[1].LocalPath != localA || items[2].LocalPath != localB {
		t.Fatalf("正文图片的本地路径 = %q/%q，希望 %q/%q",
			items[1].LocalPath, items[2].LocalPath, localA, localB)
	}
	if items[0].LocalPath != "" || items[3].LocalPath != "" {
		t.Fatalf("附件不应该有本地路径：%q/%q", items[0].LocalPath, items[3].LocalPath)
	}
}

// TestCollectPreviewGallery_SkipsNonImageOnly 确认只有非图片附件与远程图片时列表为空。
func TestCollectPreviewGallery_SkipsNonImageOnly(t *testing.T) {
	messages := []Message{
		{ID: "m1", Role: "user", Media: []MessageMedia{{ID: "att1", Name: "a.pdf", MimeType: "application/pdf"}}},
		{ID: "m2", Role: "assistant", Text: "![远程](https://example.com/x.png)"},
		{ID: "m3", Role: "user", Text: "![用户正文](https://example.com/y.png)"},
	}
	if items := collectPreviewGallery(messages); len(items) != 0 {
		t.Fatalf("收集到 %d 张图片，希望 0 张：%+v", len(items), items)
	}
}

// TestCollectPreviewGallery_DedupesRepeatedURL 确认同一地址出现两次只算一张。
func TestCollectPreviewGallery_DedupesRepeatedURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.png")
	messages := []Message{
		{ID: "m1", Role: "assistant", Text: "![一](" + path + ")\n\n![二](" + path + ")"},
	}
	if items := collectPreviewGallery(messages); len(items) != 1 {
		t.Fatalf("收集到 %d 张图片，希望 1 张：%+v", len(items), items)
	}
}

// TestPreviewGalleryIndex 确认附件按消息/媒体定位、正文图片按地址定位，输入框附件定位不到。
func TestPreviewGalleryIndex(t *testing.T) {
	dir := t.TempDir()
	localA := filepath.Join(dir, "shot-a.png")
	items := collectPreviewGallery(previewGalleryTestMessages(dir))

	cases := []struct {
		name string
		pv   *imagePreviewState
		want int
	}{
		{
			name: "附件第一张",
			pv:   &imagePreviewState{full: &messageMediaRef{threadID: "t1", messageID: "m1", mediaID: "att1"}},
			want: 0,
		},
		{
			name: "正文本地图片",
			pv:   &imagePreviewState{url: localA},
			want: 1,
		},
		{
			name: "附件最后一张",
			pv:   &imagePreviewState{full: &messageMediaRef{threadID: "t1", messageID: "m3", mediaID: "att3"}},
			want: 3,
		},
		{
			name: "输入框附件只有图片源",
			pv:   &imagePreviewState{alt: "粘贴的图片"},
			want: -1,
		},
		{
			name: "会话里没有的附件",
			pv:   &imagePreviewState{full: &messageMediaRef{threadID: "t1", messageID: "m9", mediaID: "att9"}},
			want: -1,
		},
		{
			name: "会话里没有的正文图片",
			pv:   &imagePreviewState{url: filepath.Join(dir, "missing.png")},
			want: -1,
		},
	}
	for _, tc := range cases {
		if got := previewGalleryIndex(items, tc.pv); got != tc.want {
			t.Fatalf("%s：下标 = %d，希望 %d", tc.name, got, tc.want)
		}
	}
	if got := previewGalleryIndex(nil, &imagePreviewState{url: localA}); got != -1 {
		t.Fatalf("空列表下标 = %d，希望 -1", got)
	}
}

// TestPreviewGalleryFromSnapshot_FillsThreadAndCaches 确认快照结果补上会话 ID，
// 且消息数量与最后一条消息都没变时复用同一份结果。
func TestPreviewGalleryFromSnapshot_FillsThreadAndCaches(t *testing.T) {
	messages := previewGalleryTestMessages(t.TempDir())

	first := previewGalleryFromSnapshot(messages, "thread-1", 7)
	if len(first) == 0 {
		t.Fatal("快照没有收集到图片")
	}
	for i, item := range first {
		if item.ThreadID != "thread-1" {
			t.Fatalf("第 %d 张图片的会话 = %q，希望 thread-1", i, item.ThreadID)
		}
	}

	second := previewGalleryFromSnapshot(messages, "thread-1", 7)
	if &first[0] != &second[0] {
		t.Fatal("消息快照没变时应该复用缓存的画廊列表")
	}

	grown := append(messages, Message{ID: "m4", Role: "user"})
	third := previewGalleryFromSnapshot(grown, "thread-1", 8)
	if &third[0] == &first[0] {
		t.Fatal("消息变多之后应该重新计算画廊列表")
	}
}
