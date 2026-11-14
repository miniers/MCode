package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func renderPreviewTester(dest string, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		preview := localFileLinkPreview(c, dest)
		if preview != nil {
			preview(ui.Column(c))
		}
	}, width, height)
}

// TestLocalFilePreviewPath 展示悬浮卡片完整路径，并在路径超过上限时换行显示、限制卡片宽度。
func TestLocalFilePreviewPath(t *testing.T) {
	tempDir := t.TempDir()

	// 构造超过 72 字符且包含中文的路径前缀
	longDir := filepath.Join(tempDir, "测试长路径目录这是一个非常长的中文测试路径用于验证悬浮卡片完整路径展示及换行效果保证字符数超过七十二个字符")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatalf("创建临时长路径目录失败: %v", err)
	}

	// 1. 基线：测量单行短路径高度
	shortDest := filepath.Join(tempDir, "短路径.txt")
	ttShort := renderPreviewTester(shortDest, 800, 600)
	shortRect, shortFound := ttShort.Find(shortDest)
	if !shortFound {
		t.Fatalf("短路径未能在渲染中找到: %s", shortDest)
	}
	if shortRect.H <= 0 {
		t.Fatalf("短路径高度异常: %v", shortRect.H)
	}

	// 2. 表格驱动测试：超长路径、行号后缀、失败回退与代码预览
	codeFile := filepath.Join(longDir, "测试源码文件.go")
	codeContent := strings.Join([]string{
		"package main",
		"func Alpha() {}",
		"func BetaTarget() {",
		"    println(\"hello world\")",
		"}",
		"func Gamma() {}",
	}, "\n")
	if err := os.WriteFile(codeFile, []byte(codeContent), 0o644); err != nil {
		t.Fatalf("写入测试代码文件失败: %v", err)
	}

	emptyFile := filepath.Join(longDir, "空文件.txt")
	if err := os.WriteFile(emptyFile, []byte{}, 0o644); err != nil {
		t.Fatalf("写入空文件失败: %v", err)
	}

	cases := []struct {
		name       string
		dest       string
		wantText   string // 期望在卡片中出现的附加内容（如代码行）
		checkWrap  bool   // 是否校验超过短路径单行高度且宽度受限
		minRuneLen int    // 校验 dest 字符数下限
	}{
		{
			name:       "超长中文路径冒号行号换行",
			dest:       filepath.Join(longDir, "测试冒号文件.go:173"),
			checkWrap:  true,
			minRuneLen: 72,
		},
		{
			name:       "超长中文路径锚点行号换行",
			dest:       filepath.Join(longDir, "测试锚点文件.go#L173"),
			checkWrap:  true,
			minRuneLen: 72,
		},
		{
			name:       "超长路径代码成功预览包含地址与目标代码行",
			dest:       codeFile + "#L3",
			wantText:   "func BetaTarget() {",
			checkWrap:  true,
			minRuneLen: 72,
		},
		{
			name:       "不可读取文件完整回退路径",
			dest:       filepath.Join(longDir, "不存在的代码文件.go:173"),
			checkWrap:  true,
			minRuneLen: 72,
		},
		{
			name:       "空文件完整回退路径",
			dest:       emptyFile + "#L2",
			checkWrap:  true,
			minRuneLen: 72,
		},
		{
			name:       "图片读取失败完整回退路径",
			dest:       filepath.Join(longDir, "不存在的图片.png"),
			checkWrap:  true,
			minRuneLen: 72,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.minRuneLen > 0 {
				if runeCount := utf8.RuneCountInString(tc.dest); runeCount <= tc.minRuneLen {
					t.Fatalf("dest 字符数 = %d，未超过期望的 %d: %s", runeCount, tc.minRuneLen, tc.dest)
				}
			}

			tt := renderPreviewTester(tc.dest, 800, 600)
			rect, found := tt.Find(tc.dest)
			if !found {
				t.Fatalf("未找到完整路径 %q，当前渲染的所有文本: %v", tc.dest, tt.Texts())
			}

			if rect.W > localPreviewMaxWidth {
				t.Fatalf("路径文本宽度 %v 超过了卡片最大宽度 %v", rect.W, localPreviewMaxWidth)
			}

			if tc.checkWrap {
				if rect.H <= shortRect.H {
					t.Fatalf("路径文本未换行：高度 %v 应大于单行高度 %v (dest: %s)", rect.H, shortRect.H, tc.dest)
				}
			}

			if tc.wantText != "" {
				if _, ok := tt.Find(tc.wantText); !ok {
					t.Fatalf("未在预览中找到预期文本 %q，所有文本: %v", tc.wantText, tt.Texts())
				}
			}
		})
	}
}
