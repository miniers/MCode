package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// TestMarkdownHTMLImageRefs 验证从 HTML 片段中提取 <img>、<picture> 深浅色、居中段落与无图片场景。
func TestMarkdownHTMLImageRefs(t *testing.T) {
	t.Run("单个img", func(t *testing.T) {
		raw := `<img src="docs/images/swarm.png" alt="编排与子代理" width="100%">`
		got := htmlImageRefs(raw, false)
		if len(got) != 1 || got[0].URL != "docs/images/swarm.png" || got[0].Alt != "编排与子代理" {
			t.Fatalf("单个 img 解析结果 = %+v，希望 URL=docs/images/swarm.png Alt=编排与子代理", got)
		}
	})

	t.Run("picture深浅色选择", func(t *testing.T) {
		raw := `<picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/hero-dark.png 1x, docs/images/hero-dark@2x.png 2x"><img src="docs/images/hero-light.png" alt="MCode 主窗口" width="100%"></picture>`

		dark := htmlImageRefs(raw, true)
		if len(dark) != 1 || dark[0].URL != "docs/images/hero-dark.png" || dark[0].Alt != "MCode 主窗口" {
			t.Fatalf("深色模式解析结果 = %+v，希望 URL=docs/images/hero-dark.png Alt=MCode 主窗口", dark)
		}

		light := htmlImageRefs(raw, false)
		if len(light) != 1 || light[0].URL != "docs/images/hero-light.png" || light[0].Alt != "MCode 主窗口" {
			t.Fatalf("浅色模式解析结果 = %+v，希望 URL=docs/images/hero-light.png Alt=MCode 主窗口", light)
		}

		bothSources := `<picture><source media="(prefers-color-scheme: dark)" srcset="dark.png"><source media="(prefers-color-scheme: light)" srcset="light.png"><img src="fallback.png" alt="双源"></picture>`
		if got := htmlImageRefs(bothSources, true); len(got) != 1 || got[0].URL != "dark.png" {
			t.Fatalf("双 source 深色结果 = %+v，希望 dark.png", got)
		}
		if got := htmlImageRefs(bothSources, false); len(got) != 1 || got[0].URL != "light.png" {
			t.Fatalf("双 source 浅色结果 = %+v，希望 light.png", got)
		}
	})

	t.Run("p_align_center包裹img", func(t *testing.T) {
		raw := `<p align="center"><img src="docs/images/banner.png" alt="横幅"></p>`
		got := htmlImageRefs(raw, false)
		if len(got) != 1 || got[0].URL != "docs/images/banner.png" || got[0].Alt != "横幅" {
			t.Fatalf("<p align=center><img></p> 解析结果 = %+v，希望 URL=docs/images/banner.png Alt=横幅", got)
		}
	})

	t.Run("无图片HTML返回空", func(t *testing.T) {
		for _, raw := range []string{
			"",
			"<!-- <img src=\"ignored.png\"> -->",
			"<details><summary>展开</summary>内容</details>",
			"<div>纯文本</div>",
		} {
			if got := htmlImageRefs(raw, true); len(got) != 0 {
				t.Fatalf("htmlImageRefs(%q) = %+v，希望返回空", raw, got)
			}
		}
	})
}

// TestMarkdownHTMLImage_BlockAndInline 验证 HTML 图片块、注释隐藏、非图片原文退回与行内 img 段落的渲染。
func TestMarkdownHTMLImage_BlockAndInline(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "hero.png"))
	writeTestPNG(t, filepath.Join(dir, "badge.png"))

	s := &chatState{}
	src := strings.Join([]string{
		"<!-- hidden-comment -->",
		"",
		`<picture><source media="(prefers-color-scheme: dark)" srcset="hero.png"><img src="missing-light.png" alt="主图"></picture>`,
		"",
		"<details><summary>详情</summary></details>",
		"",
		`<img src="badge.png" alt="徽章"> <img src="https://example.com/remote.png" alt="远程徽章">`,
	}, "\n")

	var opts *markdown.Options
	tester := ui.NewTester(func(c *ui.Context) {
		opts = markdownDocOptions(c, s, 13, dir)
		markdown.View(c, src, opts)
	}, 600, 400)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.SetDark(true)
	tester.Frame()

	texts := strings.Join(tester.Texts(), "\n")
	if strings.Contains(texts, "hidden-comment") {
		t.Fatalf("HTML 注释不应渲染：%q", texts)
	}
	if strings.Contains(texts, "<picture") || strings.Contains(texts, "<img") {
		t.Fatalf("HTML 图片标签不应以源码渲染：%q", texts)
	}
	if !strings.Contains(texts, "<details><summary>详情</summary></details>") {
		t.Fatalf("非图片 HTML 应保持默认原文渲染：%q", texts)
	}
	if !strings.Contains(texts, "远程徽章") {
		t.Fatalf("未加载的图片应显示 alt 文本：%q", texts)
	}
}

// TestMarkdownDocOptions 验证 markdownDocOptions 的钩子完整性、扩展集合、缓存复用与相对图片加载。
func TestMarkdownDocOptions(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir1, "sub"), 0o755); err != nil {
		t.Fatalf("创建测试子目录失败：%v", err)
	}
	writeTestPNG(t, filepath.Join(dir1, "sub", "pic.png"))

	s := &chatState{}
	var opts1, opts2, optsOtherDir, optsOtherSize *markdown.Options
	tester := ui.NewTester(func(c *ui.Context) {
		opts1 = markdownDocOptions(c, s, 13, dir1)
		opts2 = markdownDocOptions(c, s, 13, dir1)
		optsOtherDir = markdownDocOptions(c, s, 13, dir2)
		optsOtherSize = markdownDocOptions(c, s, 15, dir1)
	}, 200, 100)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()

	if opts1 == nil {
		t.Fatal("markdownDocOptions 返回 nil")
	}
	if opts1.HTML == nil || opts1.LoadImage == nil || opts1.Code == nil || opts1.Block == nil {
		t.Fatalf("markdownDocOptions 缺少必要钩子：HTML=%v LoadImage=%v Code=%v Block=%v",
			opts1.HTML != nil, opts1.LoadImage != nil, opts1.Code != nil, opts1.Block != nil)
	}
	if opts1.Extensions&parser.ExtDollarMath == 0 {
		t.Fatalf("Extensions=%v 未包含 ExtDollarMath", opts1.Extensions)
	}
	if opts1 != opts2 {
		t.Fatal("相同 (size, dir) 两次调用应返回同一指针")
	}
	if opts1 == optsOtherDir {
		t.Fatal("不同 dir 应返回不同指针")
	}
	if opts1 == optsOtherSize {
		t.Fatal("不同 size 应返回不同指针")
	}
	if src, err := opts1.LoadImage("sub/pic.png?v=1"); err != nil || src == nil {
		t.Fatalf("LoadImage 相对路径加载失败：src=%v err=%v", src, err)
	}
}

// TestMarkdownHTMLImageHookOnChatOptions 验证 chatState 主选项与静态选项都已安装 HTML 图片钩子。
func TestMarkdownHTMLImageHookOnChatOptions(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	if s.mdOpts.HTML == nil || s.mdStaticOpts.HTML == nil {
		t.Fatalf("chatState Markdown 选项缺少 HTML 钩子：mdOpts.HTML=%v mdStaticOpts.HTML=%v",
			s.mdOpts.HTML != nil, s.mdStaticOpts.HTML != nil)
	}
}
