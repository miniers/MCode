package mcodeui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type mentionRefreshClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	err     error
}

func (f *mentionRefreshClient) History(ctx context.Context, threadID string) ([]Message, error) {
	f.calls.Add(1)
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []Message{{Role: "user", Text: "新摘要"}}, f.err
}

func TestMentionPreviewRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "成功"
		if fail {
			name = "失败"
		}
		t.Run(name, func(t *testing.T) {
			client := &mentionRefreshClient{FakeClient: NewFakeClient(), started: make(chan struct{}, 2), release: make(chan struct{})}
			if fail {
				client.err = errors.New("读取失败")
			}
			s := newChatState(client)
			defer s.close()
			id, key := "mention-refresh-"+name, "mention-refresh-preview-"+name
			s.threads = []Thread{{ID: id, Title: "目标会话"}}
			mentionPreviewMu.Lock()
			mentionPreviewOpen[key] = true
			mentionPreviewDigests[id] = &mentionPreviewDigest{goal: "旧摘要"}
			mentionPreviewMu.Unlock()
			t.Cleanup(func() {
				mentionPreviewMu.Lock()
				delete(mentionPreviewOpen, key)
				delete(mentionPreviewDigests, id)
				mentionPreviewMu.Unlock()
			})
			tt := ui.NewTester(func(c *ui.Context) {
				anchor := ui.Box(c.Key("anchor")).Size(50, 30)
				renderMentionPreview(c, s, anchor, key, mentionSegment{ThreadID: id, Text: "@目标会话"})
			}, 800, 600)
			startMentionPreviewLoad(s, id)
			select {
			case <-client.started:
			case <-time.After(time.Second):
				t.Fatal("未发起读取")
			}
			startMentionPreviewLoad(s, id)
			tt.Frame()
			if !tt.HasText("旧摘要") || mentionPreviewDigestOf(id).loading {
				t.Fatal("刷新期间未显示旧摘要")
			}
			close(client.release)
			deadline := time.Now().Add(time.Second)
			for {
				mentionPreviewMu.Lock()
				digest := mentionPreviewDigests[id]
				done := digest != nil && !digest.refreshing
				mentionPreviewMu.Unlock()
				if done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("刷新未完成")
				}
				time.Sleep(time.Millisecond)
			}
			tt.Frame()
			want := "新摘要"
			if fail {
				want = "旧摘要"
			}
			if !tt.HasText(want) || tt.HasText("会话摘要读取失败：读取失败") {
				t.Fatalf("刷新后未显示 %q", want)
			}
			if got := client.calls.Load(); got != 1 {
				t.Fatalf("读取次数 = %d，期望 1", got)
			}
		})
	}
}

// TestSplitThreadMentions 验证 @会话引用的命中、未命中与最长匹配规则（对齐后端 mentions 收集）。
func TestSplitThreadMentions(t *testing.T) {
	threads := []Thread{
		{ID: "t-1", Title: "发布计划"},
		{ID: "t-2", Title: "发布"},
		{ID: "t-3", Title: ""}, // 无标题的会话只能按 ID 命中
	}

	cases := []struct {
		name string
		text string
		want []mentionSegment
	}{
		{
			name: "命中标题",
			text: "接着 @发布计划 收尾",
			want: []mentionSegment{
				{Text: "接着 "},
				{Text: "@发布计划", ThreadID: "t-1"},
				{Text: " 收尾"},
			},
		},
		{
			name: "最长标题优先",
			text: "@发布计划完成了吗",
			want: []mentionSegment{
				{Text: "@发布计划", ThreadID: "t-1"},
				{Text: "完成了吗"},
			},
		},
		{
			name: "按会话 ID 命中",
			text: "看 @t-2 的结论",
			want: []mentionSegment{
				{Text: "看 "},
				{Text: "@t-2", ThreadID: "t-2"},
				{Text: " 的结论"},
			},
		},
		{
			name: "无标题会话按 ID 命中",
			text: "@t-3 现在怎么样了",
			want: []mentionSegment{
				{Text: "@t-3", ThreadID: "t-3"},
				{Text: " 现在怎么样了"},
			},
		},
		{
			name: "多个引用",
			text: "@发布计划 和 @t-2",
			want: []mentionSegment{
				{Text: "@发布计划", ThreadID: "t-1"},
				{Text: " 和 "},
				{Text: "@t-2", ThreadID: "t-2"},
			},
		},
		{
			name: "整段都是引用",
			text: "@发布计划",
			want: []mentionSegment{
				{Text: "@发布计划", ThreadID: "t-1"},
			},
		},
		{
			name: "未命中的 @ 原样保留",
			text: "看看 @不存在的会话 吧",
			want: []mentionSegment{
				{Text: "看看 @不存在的会话 吧"},
			},
		},
		{
			name: "结尾单独的 @ 原样保留",
			text: "在 @",
			want: []mentionSegment{
				{Text: "在 @"},
			},
		},
		{
			name: "连续 @ 只有第二个命中",
			text: "@@发布计划",
			want: []mentionSegment{
				{Text: "@"},
				{Text: "@发布计划", ThreadID: "t-1"},
			},
		},
		{
			name: "没有 @",
			text: "普通提问",
			want: []mentionSegment{
				{Text: "普通提问"},
			},
		},
		{
			name: "空文本",
			text: "",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitThreadMentions(tc.text, threads)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitThreadMentions(%q) = %#v, want %#v", tc.text, got, tc.want)
			}
			// 切片后拼接必须还原原文，保证未命中的文本不丢字。
			var joined strings.Builder
			for _, seg := range got {
				joined.WriteString(seg.Text)
			}
			if joined.String() != tc.text {
				t.Fatalf("片段拼接后为 %q，原文为 %q", joined.String(), tc.text)
			}
		})
	}
}

// TestSplitThreadMentionsCollapsedPreview 验证折叠预览里被保留的 @会话引用仍能命中。
func TestSplitThreadMentionsCollapsedPreview(t *testing.T) {
	threads := []Thread{{ID: "t-1", Title: "发布计划"}}
	text := "@发布计划 请收尾\n" + strings.Repeat("补充内容\n", 9)
	preview := userPromptPreview(text)
	if preview == text {
		t.Fatalf("预览应被折叠截断: %q", preview)
	}
	if !hasThreadMention(splitThreadMentions(preview, threads)) {
		t.Fatalf("折叠预览里应命中 @发布计划: %q", preview)
	}
	// 预览被截断掉的引用不再命中，正文按纯文本渲染。
	if hasThreadMention(splitThreadMentions(userPromptPreview(strings.Repeat("开头\n", 9)+"@发布计划"), threads)) {
		t.Fatalf("被截断掉的引用不应命中")
	}
}
