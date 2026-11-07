package mcodeui

import (
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 轻量公式排版：只实现 TeX 的一个小子集——上下标（含花括号分组）、\frac、\sqrt、
// 常见希腊字母与运算符/关系符、直立函数名、几种间距——用 mygo 现有的绘制原语画出来。
// 排不出的输入一律返回 false，由调用方退回显示原文：宁可显示源码，也不要画错。
// 纯 Go 实现，不引入 KaTeX/MathJax 之类的 JS 运行时。

// mathBox 是排版后的一个盒子：宽 w、基线以上 asc、基线以下 desc（单位 DIP）。
// draw 在 (x, baseline) 处用 ink 画出内容，同一帧可能被调用多次（只绘制，不建元素）。
type mathBox struct {
	w, asc, desc float32
	draw         func(p *ui.Painter, x, baseline float32, ink ui.Color)
}

const (
	// 文字基线的上下高度（相对字号）：常规字体的近似值，用来把文字放到基线上。
	mathAscent  = 0.78
	mathDescent = 0.24
	// 上下标字号与位移（相对主字号）。
	mathScriptSize  = 0.7
	mathScriptRaise = 0.38
	mathScriptDrop  = 0.22
	// 分式横线与分子/分母的间距、根号弯钩宽度（相对字号）。
	mathBarGap   = 0.28
	mathHookWide = 0.45
	// TeX 间距（相对字号）：\, \; \quad。
	mathThinSpace  = 0.17
	mathThickSpace = 0.28
	// 花括号嵌套上限：畸形输入不会把递归打爆。
	mathMaxDepth = 32
)

// mathGreek 是支持的小写与大写希腊字母。
var mathGreek = map[string]string{
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε",
	"varepsilon": "ε", "zeta": "ζ", "eta": "η", "theta": "θ", "vartheta": "ϑ",
	"iota": "ι", "kappa": "κ", "lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ",
	"pi": "π", "varpi": "ϖ", "rho": "ρ", "varrho": "ϱ", "sigma": "σ",
	"varsigma": "ς", "tau": "τ", "upsilon": "υ", "phi": "φ", "varphi": "ϕ",
	"chi": "χ", "psi": "ψ", "omega": "ω",
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ", "Xi": "Ξ",
	"Pi": "Π", "Sigma": "Σ", "Upsilon": "Υ", "Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",
}

// mathSymbols 是支持的运算符、关系符与杂项符号。
var mathSymbols = map[string]string{
	"times": "×", "cdot": "·", "div": "÷", "pm": "±", "mp": "∓",
	"le": "≤", "leq": "≤", "ge": "≥", "geq": "≥", "ne": "≠", "neq": "≠",
	"approx": "≈", "equiv": "≡", "sim": "∼", "propto": "∝",
	"infty": "∞", "sum": "∑", "prod": "∏", "int": "∫", "oint": "∮",
	"to": "→", "rightarrow": "→", "leftarrow": "←", "gets": "←",
	"Rightarrow": "⇒", "partial": "∂", "nabla": "∇", "circ": "∘",
	"in": "∈", "notin": "∉", "forall": "∀", "exists": "∃", "emptyset": "∅",
	"cdots": "⋯", "ldots": "…", "dots": "…", "prime": "′", "star": "⋆",
	"angle": "∠", "perp": "⊥", "parallel": "∥",
}

// mathFunctions 是直立排版的函数名。
var mathFunctions = map[string]string{
	"sin": "sin", "cos": "cos", "tan": "tan", "cot": "cot", "sec": "sec",
	"csc": "csc", "arcsin": "arcsin", "arccos": "arccos", "arctan": "arctan",
	"sinh": "sinh", "cosh": "cosh", "tanh": "tanh", "log": "log", "ln": "ln",
	"lg": "lg", "exp": "exp", "max": "max", "min": "min", "lim": "lim",
	"sup": "sup", "inf": "inf", "det": "det", "gcd": "gcd",
}

// mathEmptyBox 返回一个只占宽度、不画内容的盒子；负宽度当作 0。
func mathEmptyBox(w float32) mathBox {
	if w < 0 {
		w = 0
	}
	return mathBox{w: w, draw: func(*ui.Painter, float32, float32, ui.Color) {}}
}

// mathHBox 把若干盒子横向拼接：宽度相加，高度取并集。
func mathHBox(boxes []mathBox) mathBox {
	var w, asc, desc float32
	for _, b := range boxes {
		w += b.w
		asc = max(asc, b.asc)
		desc = max(desc, b.desc)
	}
	return mathBox{
		w: w, asc: asc, desc: desc,
		draw: func(p *ui.Painter, x, baseline float32, ink ui.Color) {
			for _, b := range boxes {
				if b.draw != nil {
					b.draw(p, x, baseline, ink)
				}
				x += b.w
			}
		},
	}
}

// mathRuneWidth 估计单个 rune 的宽度（相对字号）：mygo 没有在不绘制时量字宽的接口，
// 这里按字符类别取近似值，够排版用；公式通常很短，误差不明显。
func mathRuneWidth(r rune, size float32) float32 {
	var em float32
	switch {
	case r == ' ':
		em = 0.27
	case r == 'i' || r == 'l' || r == 'j' || r == '.' || r == '\'' || r == '!':
		em = 0.25
	case r == ',' || r == ':':
		em = 0.28
	case r == ';':
		em = 0.3
	case r == 'f' || r == 't' || r == 'r':
		em = 0.36
	case r == 'm' || r == 'w' || r == 'M' || r == 'W':
		em = 0.85
	case r >= '0' && r <= '9':
		em = 0.52
	case r >= 'A' && r <= 'Z':
		em = 0.65
	case r >= 'a' && r <= 'z':
		em = 0.52
	case r == '=' || r == '+' || r == '-' || r == '<' || r == '>' || r == '/' || r == '|':
		em = 0.62
	case unicode.Is(unicode.Greek, r):
		em = 0.58
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		em = 0.55
	default:
		// 数学符号（∑ ∫ √ ≈ 等）大多偏宽。
		em = 0.62
	}
	return em * size
}

// mathTextRun 把一个字符串排成盒子：整体一次画出，宽度按字符估计累加。
func mathTextRun(s string, size float32) mathBox {
	if s == "" || size <= 0 {
		return mathEmptyBox(0)
	}
	var w float32
	for _, r := range s {
		w += mathRuneWidth(r, size)
	}
	y := -mathAscent * size
	return mathBox{
		w: w, asc: mathAscent * size, desc: mathDescent * size,
		draw: func(p *ui.Painter, x, baseline float32, ink ui.Color) {
			p.Text(x, baseline+y, s, size, ink)
		},
	}
}

// mathScripts 把上下标挂到基盒右侧：上标抬升、下标下沉，脚本字号更小。
func mathScripts(base, sup, sub mathBox, hasSup, hasSub bool, size float32) mathBox {
	pad := 0.06 * size
	raise := mathScriptRaise * size
	drop := mathScriptDrop * size
	var scriptW float32
	if hasSup {
		scriptW = max(scriptW, sup.w)
	}
	if hasSub {
		scriptW = max(scriptW, sub.w)
	}
	asc := base.asc
	if hasSup {
		asc = max(asc, raise+sup.asc)
	}
	desc := base.desc
	if hasSub {
		desc = max(desc, drop+sub.desc)
	}
	return mathBox{
		w: base.w + pad + scriptW, asc: asc, desc: desc,
		draw: func(p *ui.Painter, x, baseline float32, ink ui.Color) {
			if base.draw != nil {
				base.draw(p, x, baseline, ink)
			}
			sx := x + base.w + pad
			if hasSup && sup.draw != nil {
				sup.draw(p, sx, baseline-raise, ink)
			}
			if hasSub && sub.draw != nil {
				sub.draw(p, sx, baseline+drop, ink)
			}
		},
	}
}

// mathFraction 平分式：横线压在主轴（基线）上，分子在上、分母在下，各自居中。
func mathFraction(num, den mathBox, size float32) mathBox {
	gap := mathBarGap * size
	contentW := max(num.w, den.w)
	w := contentW + 0.4*size
	if w < 0.6*size {
		w = 0.6 * size
	}
	asc := gap + num.desc + num.asc
	desc := gap + den.asc + den.desc
	barH := max(1, 0.06*size)
	return mathBox{
		w: w, asc: asc, desc: desc,
		draw: func(p *ui.Painter, x, baseline float32, ink ui.Color) {
			if num.draw != nil {
				num.draw(p, x+(w-num.w)/2, baseline-(gap+num.desc), ink)
			}
			if den.draw != nil {
				den.draw(p, x+(w-den.w)/2, baseline+(gap+den.asc), ink)
			}
			p.Fill(ui.Rect{X: x + 0.05*size, Y: baseline - barH/2, W: w - 0.1*size, H: barH}, ink, 0)
		},
	}
}

// mathRadical 根号：左边用折线画根号弯钩与上横线，内部内容右移。
func mathRadical(inner mathBox, size float32) mathBox {
	hook := mathHookWide * size
	w := hook + inner.w + 0.1*size
	asc := inner.asc + 0.1*size
	desc := max(inner.desc, 0.35*size)
	strokeW := max(1, 0.07*size)
	barY := -asc
	points := [][2]float32{
		{0.06 * size, -0.3 * size},
		{0.2 * size, desc * 0.9},
		{hook, barY},
		{w, barY},
	}
	return mathBox{
		w: w, asc: asc, desc: desc,
		draw: func(p *ui.Painter, x, baseline float32, ink ui.Color) {
			pts := make([][2]float32, len(points))
			for i, pt := range points {
				pts[i] = [2]float32{x + pt[0], baseline + pt[1]}
			}
			p.StrokePolyline(pts, strokeW, ink)
			if inner.draw != nil {
				inner.draw(p, x+hook, baseline, ink)
			}
		},
	}
}

// mathParser 是递归下降的 TeX 子集解析器；pos 指向下一个待读 rune，size 是当前字号。
type mathParser struct {
	src   []rune
	pos   int
	size  float32
	depth int
}

// typesetTeX 把一段 TeX 子集排成盒子；不支持的语法返回 false（调用方退回原文）。
// size 是主字号（DIP），非正数时按 14 处理。
func typesetTeX(tex string, size float32) (mathBox, bool) {
	if size <= 0 {
		size = 14
	}
	s := strings.TrimSpace(tex)
	if s == "" {
		return mathBox{}, false
	}
	p := &mathParser{src: []rune(s), size: size}
	box, ok := p.parseSeq(0)
	if !ok || p.pos < len(p.src) {
		return mathBox{}, false
	}
	if box.w <= 0 {
		return mathBox{}, false
	}
	return box, true
}

// parseSeq 解析一串项直到 stop（0 表示到结尾）；遇到多余的右花括号视为语法错误。
func (p *mathParser) parseSeq(stop rune) (mathBox, bool) {
	boxes := make([]mathBox, 0, 4)
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		if stop != 0 && r == stop {
			return mathHBox(boxes), true
		}
		if r == '}' {
			return mathBox{}, false
		}
		item, ok := p.parseItem()
		if !ok {
			return mathBox{}, false
		}
		boxes = append(boxes, item)
	}
	if stop != 0 {
		// 花括号没有闭合。
		return mathBox{}, false
	}
	return mathHBox(boxes), true
}

// parseItem 解析一个基元以及可能挂在它后面的上下标；上下标可以任意顺序出现。
func (p *mathParser) parseItem() (mathBox, bool) {
	baseSize := p.size
	var base mathBox
	if r := p.src[p.pos]; r == '^' || r == '_' {
		// 没有底数的上下标，按空底数处理（例如 ^{14}C 的写法）。
		base = mathEmptyBox(0)
	} else {
		b, ok := p.parseBase()
		if !ok {
			return mathBox{}, false
		}
		base = b
	}
	var sup, sub mathBox
	hasSup, hasSub := false, false
	for p.pos < len(p.src) && (p.src[p.pos] == '^' || p.src[p.pos] == '_') {
		kind := p.src[p.pos]
		p.pos++
		arg, ok := p.parseArg(baseSize * mathScriptSize)
		if !ok {
			return mathBox{}, false
		}
		if kind == '^' {
			if hasSup {
				return mathBox{}, false
			}
			sup, hasSup = arg, true
		} else {
			if hasSub {
				return mathBox{}, false
			}
			sub, hasSub = arg, true
		}
	}
	if !hasSup && !hasSub {
		return base, true
	}
	return mathScripts(base, sup, sub, hasSup, hasSub, baseSize), true
}

// parseArg 解析命令或上下标的参数：花括号分组或单个基元，字号临时换成 size。
func (p *mathParser) parseArg(size float32) (mathBox, bool) {
	if p.pos >= len(p.src) {
		return mathBox{}, false
	}
	prev := p.size
	p.size = size
	defer func() { p.size = prev }()
	if p.src[p.pos] == '{' {
		p.depth++
		if p.depth > mathMaxDepth {
			return mathBox{}, false
		}
		p.pos++
		inner, ok := p.parseSeq('}')
		p.depth--
		if !ok || p.pos >= len(p.src) || p.src[p.pos] != '}' {
			return mathBox{}, false
		}
		p.pos++
		return inner, true
	}
	return p.parseBase()
}

// parseBase 解析不带上下标的最小单位：命令、分组、空白或一串普通字符。
func (p *mathParser) parseBase() (mathBox, bool) {
	if p.pos >= len(p.src) {
		return mathBox{}, false
	}
	r := p.src[p.pos]
	switch {
	case r == '\\':
		return p.parseCommand()
	case r == '{':
		p.depth++
		if p.depth > mathMaxDepth {
			return mathBox{}, false
		}
		p.pos++
		inner, ok := p.parseSeq('}')
		p.depth--
		if !ok || p.pos >= len(p.src) || p.src[p.pos] != '}' {
			return mathBox{}, false
		}
		p.pos++
		return inner, true
	case r == '}':
		return mathBox{}, false
	case unicode.IsSpace(r):
		p.pos++
		return mathEmptyBox(0.28 * p.size), true
	}
	// 连续的字母、数字与小数点一起排版，字距更自然；其它符号单独成盒。
	if !isMathPlainRune(r) {
		p.pos++
		return mathTextRun(string(r), p.size), true
	}
	start := p.pos
	for p.pos < len(p.src) && isMathPlainRune(p.src[p.pos]) {
		p.pos++
	}
	return mathTextRun(string(p.src[start:p.pos]), p.size), true
}

// isMathPlainRune 判断 rune 是否可以并入同一次文字绘制（字母、数字与小数点）。
func isMathPlainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.'
}

// parseCommand 解析反斜杠命令；未知命令返回 false。
func (p *mathParser) parseCommand() (mathBox, bool) {
	start := p.pos + 1
	i := start
	for i < len(p.src) && unicode.IsLetter(p.src[i]) {
		i++
	}
	if i == start {
		// 控制符号：\, \; \! \\ \{ 等。
		if start >= len(p.src) {
			return mathBox{}, false
		}
		sym := p.src[start]
		p.pos = start + 1
		switch sym {
		case ',', ':':
			return mathEmptyBox(mathThinSpace * p.size), true
		case ';':
			return mathEmptyBox(mathThickSpace * p.size), true
		case '!':
			return mathEmptyBox(0), true
		case '{', '}', '$', '%', '&', '#', '_':
			return mathTextRun(string(sym), p.size), true
		}
		return mathBox{}, false
	}
	name := string(p.src[start:i])
	p.pos = i
	// 控制词后面的一个空格在 TeX 里被吃掉，不参与排版。
	if p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
	switch name {
	case "frac":
		num, ok := p.parseArg(p.size)
		if !ok {
			return mathBox{}, false
		}
		den, ok := p.parseArg(p.size)
		if !ok {
			return mathBox{}, false
		}
		return mathFraction(num, den, p.size), true
	case "sqrt":
		inner, ok := p.parseArg(p.size)
		if !ok {
			return mathBox{}, false
		}
		return mathRadical(inner, p.size), true
	case "quad":
		return mathEmptyBox(p.size), true
	case "qquad":
		return mathEmptyBox(2 * p.size), true
	}
	if sym, ok := mathGreek[name]; ok {
		return mathTextRun(sym, p.size), true
	}
	if sym, ok := mathSymbols[name]; ok {
		return mathTextRun(sym, p.size), true
	}
	if fn, ok := mathFunctions[name]; ok {
		return mathTextRun(fn, p.size), true
	}
	return mathBox{}, false
}

// mathCacheEntry 缓存一次排版结果，流式渲染时同一段公式每帧都会被问到。
type mathCacheEntry struct {
	box mathBox
	ok  bool
}

var mathTypesetCache sync.Map // key: tex + "\x00" + 字号

// cachedTypesetTeX 带缓存的 typesetTeX；key 由公式原文与字号拼成。
func cachedTypesetTeX(tex string, size float32) (mathBox, bool) {
	key := tex + "\x00" + strconv.FormatFloat(float64(size), 'f', 2, 32)
	if v, ok := mathTypesetCache.Load(key); ok {
		e := v.(mathCacheEntry)
		return e.box, e.ok
	}
	box, ok := typesetTeX(tex, size)
	mathTypesetCache.Store(key, mathCacheEntry{box: box, ok: ok})
	return box, ok
}

// mathInk 取画公式用的颜色：优先 Markdown 主题的文字色，退回窗口主题的文字色。
func mathInk(o *markdown.Options, p *ui.Painter) ui.Color {
	if o != nil && o.Theme != nil && o.Theme.Text.A != 0 {
		return o.Theme.Text
	}
	return p.Theme().Text
}

// mathBlockSize 返回块级公式的字号：跟随 Markdown 主题的正文号，未知时用 14。
func mathBlockSize(o *markdown.Options) float32 {
	if o != nil && o.Theme != nil && o.Theme.FontSize > 0 {
		return o.Theme.FontSize
	}
	return 14
}

// installMathHooks 给 Markdown 选项装上公式钩子：行内公式按基线对齐，
// 块级公式单独居中成行；排不出的公式显示原文（行内由 mygo 自动退回，块级用等宽小字）。
func installMathHooks(o *markdown.Options) {
	if o == nil {
		return
	}
	o.InlineMath = func(tex string, size float32) (markdown.InlineObject, bool) {
		box, ok := cachedTypesetTeX(tex, size)
		if !ok || box.w <= 0 {
			return markdown.InlineObject{}, false
		}
		return markdown.InlineObject{
			Width:  box.w,
			Height: box.asc,
			Depth:  box.desc,
			Draw: func(p *ui.Painter, r ui.Rect) {
				box.draw(p, r.X, r.Y+box.asc, mathInk(o, p))
			},
		}, true
	}
	o.Math = func(c *ui.Context, tex string) {
		size := mathBlockSize(o)
		box, ok := cachedTypesetTeX(tex, size)
		if !ok || box.w <= 0 {
			// 排不出（或不支持的语法）：等宽小字显示原文，不画空白。
			el := ui.Text(c, tex)
			if o.Theme != nil && o.Theme.CodeFont != "" {
				el.Font(o.Theme.CodeFont)
			}
			el.FontSize(size*0.875).NoWrap().Margin(0.15*size, 0)
			return
		}
		// 铺满整行后自己把公式画在中间，避免依赖父容器的对齐方式。
		ui.Box(c).
			Height(box.asc+box.desc).
			AlignSelf(ui.Stretch).
			Margin(0.2*size, 0).
			Draw(func(p *ui.Painter, r ui.Rect) {
				x := r.X + (r.W-box.w)/2
				if x < r.X {
					x = r.X
				}
				box.draw(p, x, r.Y+box.asc, mathInk(o, p))
			})
	}
}
