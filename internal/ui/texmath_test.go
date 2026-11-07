package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// TestTypesetTeXSupported 覆盖计划里要求支持的子集：每种语法都能排成非空盒子。
func TestTypesetTeXSupported(t *testing.T) {
	cases := []struct {
		name string
		tex  string
	}{
		{"上标", "x^2"},
		{"下标", "a_1"},
		{"上下标", "x_i^2"},
		{"花括号分组", "x^{n+1}"},
		{"分式", `\frac{a}{b}`},
		{"根号", `\sqrt{x}`},
		{"希腊字母与关系符", `\alpha \le \beta`},
		{"求和带上下限", `\sum_{i=1}^{n} i`},
		{"直立函数名", `\sin x`},
		{"常见运算符", `\times \cdot \pm \to \infty \approx \ne \ge \int`},
		{"间距", `a\,b\;c\quad d`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			box, ok := typesetTeX(tc.tex, 14)
			if !ok {
				t.Fatalf("typesetTeX(%q) 返回 false，应该能排", tc.tex)
			}
			if box.w <= 0 || box.asc <= 0 {
				t.Fatalf("typesetTeX(%q) 尺寸不合理: w=%v asc=%v desc=%v", tc.tex, box.w, box.asc, box.desc)
			}
			if box.draw == nil {
				t.Fatalf("typesetTeX(%q) 没有绘制函数", tc.tex)
			}
		})
	}
}

// TestTypesetTeXUnsupported 覆盖非法或不支持的输入：返回 false 且不 panic，调用方据此退回原文。
func TestTypesetTeXUnsupported(t *testing.T) {
	cases := []struct {
		name string
		tex  string
	}{
		{"未知环境", `\begin{matrix} a \end{matrix}`},
		{"未知命令", `x \unknowncmd y`},
		{"未闭合分组", `\frac{a}{b`},
		{"多余的右花括号", `x}`},
		{"空输入", "   "},
		{"命令缺参数", `\sqrt`},
		{"重复上标", `x^1^2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if box, ok := typesetTeX(tc.tex, 14); ok {
				t.Fatalf("typesetTeX(%q) 应该返回 false，实际 w=%v asc=%v", tc.tex, box.w, box.asc)
			}
		})
	}
}

// TestMathFractionWidth 验证分式宽度不小于分子/分母中较宽者，且基线上下都被撑开。
func TestMathFractionWidth(t *testing.T) {
	num := mathTextRun("a", 14)
	den := mathTextRun("bbbb", 14)
	frac := mathFraction(num, den, 14)
	if want := max(num.w, den.w); frac.w < want {
		t.Fatalf("分式宽 %v 小于分子/分母较大者 %v", frac.w, want)
	}
	if frac.asc <= num.asc {
		t.Fatalf("分式基线以上高度 %v 应大于分子自身 %v", frac.asc, num.asc)
	}
	if frac.desc <= den.desc {
		t.Fatalf("分式基线以下深度 %v 应大于分母自身 %v", frac.desc, den.desc)
	}
}

// TestMathScriptMetrics 验证上标把盒子上沿抬高、下标把下沿压低，且宽度为基盒加脚本宽。
func TestMathScriptMetrics(t *testing.T) {
	base := mathTextRun("x", 14)
	sup := mathTextRun("2", 14*mathScriptSize)
	sub := mathTextRun("i", 14*mathScriptSize)
	both := mathScripts(base, sup, sub, true, true, 14)
	if both.asc <= base.asc {
		t.Fatalf("上标未抬高: asc=%v base.asc=%v", both.asc, base.asc)
	}
	if both.desc <= base.desc {
		t.Fatalf("下标未下沉: desc=%v base.desc=%v", both.desc, base.desc)
	}
	if both.w <= base.w {
		t.Fatalf("带上下标的盒子宽 %v 应大于基盒宽 %v", both.w, base.w)
	}
	if both.w != base.w+0.06*14+max(sup.w, sub.w) {
		t.Fatalf("上下标宽度 %v 不等于基盒宽加脚本宽", both.w)
	}
}

// TestMathRadicalMetrics 验证根号盒子把内容右移并抬高顶部。
func TestMathRadicalMetrics(t *testing.T) {
	inner := mathTextRun("x", 14)
	rad := mathRadical(inner, 14)
	if rad.w <= inner.w {
		t.Fatalf("根号宽 %v 应大于内容宽 %v", rad.w, inner.w)
	}
	if rad.asc <= inner.asc {
		t.Fatalf("根号顶部 %v 应高于内容顶部 %v", rad.asc, inner.asc)
	}
	if rad.desc < 0.35*14 {
		t.Fatalf("根号弯钩应伸到基线以下，desc=%v", rad.desc)
	}
}

// TestTypesetTeXCache 验证缓存对同一输入返回同一结论（命中缓存不会改变结果）。
func TestTypesetTeXCache(t *testing.T) {
	first, okFirst := cachedTypesetTeX(`\frac{a}{b}`, 14)
	second, okSecond := cachedTypesetTeX(`\frac{a}{b}`, 14)
	if okFirst != okSecond || first.w != second.w || first.asc != second.asc {
		t.Fatalf("缓存前后结果不一致: %+v/%v vs %+v/%v", first, okFirst, second, okSecond)
	}
}

// TestMathHooksRenderHeadless 验证装上钩子后，Markdown 里的行内与块级公式不再以源码形式出现。
func TestMathHooksRenderHeadless(t *testing.T) {
	opts := &markdown.Options{Extensions: parser.Default | parser.ExtDollarMath}
	installMathHooks(opts)
	if opts.InlineMath == nil || opts.Math == nil {
		t.Fatal("installMathHooks 没有装上 InlineMath 与 Math 钩子")
	}
	if _, ok := opts.InlineMath("x^2", 14); !ok {
		t.Fatal("InlineMath 对 $x^2$ 应返回 true")
	}
	if _, ok := opts.InlineMath(`\unknowncmd`, 14); ok {
		t.Fatal("InlineMath 对不支持的语法应返回 false，让 mygo 退回原文")
	}

	src := "行内 $x^2$ 公式。\n\n$$\\frac{a}{b}$$\n"
	tt := ui.NewTester(func(c *ui.Context) {
		markdown.View(c, src, opts)
	}, 600, 400)
	texts := strings.Join(tt.Texts(), "\n")
	if strings.Contains(texts, "$") {
		t.Fatalf("渲染结果仍含美元符号源码: %q", texts)
	}
	if !tt.HasText("行内") || !tt.HasText("公式") {
		t.Fatalf("公式周边的正文丢了: %q", texts)
	}
}
