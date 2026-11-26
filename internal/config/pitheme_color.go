package config

import (
	"math"
)

// 本文件实现 pi 主题用到的 OKHSL/OKLCH 色彩函数，不依赖外部库。
//
// OKLab 部分照搬 CSS Color 4 的矩阵。OKHSL 部分是 Björn Ottosson 参考实现的移植：
// 饱和度相对于每个色相与亮度处的 sRGB 色域，HSL 的 l 经 (l²+K1·l)/(K3·(l+K2)) 映射到
// OKLab 亮度，色度则按 c0/cMid/cMax 三个参考点在 0..1 内分段插值。
// 该移植与 pi-tui（@earendil-works/pi-tui）的实现一致，因此 pi 主题的颜色能逐通道对上。

const (
	okhslK1 = 0.206
	okhslK2 = 0.03
	okhslK3 = (1 + okhslK1) / (1 + okhslK2)
)

// lmsSlopes：沿 chroma 方向 (a, b) 时各立方根 LMS 分量的变化率。
func lmsSlopes(a, b float64) [3]float64 {
	return [3]float64{0.3963377773761749*a + 0.2158037573099136*b,
		-0.1055613458156586*a - 0.0638541728258133*b,
		-0.0894841775298119*a - 1.2914855480194092*b}
}

// maxSaturation 返回色相 (a, b) 在 sRGB 内的最大饱和度 C/L：多项式拟合加一次 Halley 迭代。
func maxSaturation(a, b float64) float64 {
	type fit struct {
		half [2]float64
		coef [5]float64
	}
	fits := [3]fit{
		{[2]float64{-1.8817031, -0.80936501}, [5]float64{1.19086277, 1.76576728, 0.59662641, 0.75515197, 0.56771245}},
		{[2]float64{1.8144408, -1.19445267}, [5]float64{0.73956515, -0.45954404, 0.08285427, 0.12541073, -0.14503204}},
		{[2]float64{0.13110758, 1.81333971}, [5]float64{1.35733652, -0.00915799, -1.1513021, -0.50559606, 0.00692167}},
	}
	channel := 2
	for i := 0; i < 2; i++ {
		if fits[i].half[0]*a+fits[i].half[1]*b > 1 {
			channel = i
			break
		}
	}
	weights := [3][3]float64{
		{4.0767416360759583, -3.3077115392580629, 0.2309699031821043},
		{-1.2684379732850315, 2.6097573492876882, -0.341319376002657},
		{-0.0041960761386756, -0.7034186179359362, 1.7076146940746117},
	}[channel]
	k := fits[channel].coef
	saturation := k[0] + k[1]*a + k[2]*b + k[3]*a*a + k[4]*a*b
	slopes := lmsSlopes(a, b)
	var base, first, second [3]float64
	for i := 0; i < 3; i++ {
		base[i] = 1 + saturation*slopes[i]
		first[i] = 3 * slopes[i] * base[i] * base[i]
		second[i] = 6 * slopes[i] * slopes[i] * base[i]
	}
	dot := func(values [3]float64) float64 {
		return weights[0]*values[0] + weights[1]*values[1] + weights[2]*values[2]
	}
	for i := 0; i < 3; i++ {
		base[i] = base[i] * base[i] * base[i]
	}
	f := dot(base)
	f1 := dot(first)
	f2 := dot(second)
	return saturation - (f*f1)/(f1*f1-0.5*f*f2)
}

// okhueCusp 返回色相 (a, b) 最饱和 sRGB 颜色的 OKLab 亮度与色度。
func okhueCusp(a, b float64) (float64, float64) {
	saturation := maxSaturation(a, b)
	lin := oklabToLinearRGB([3]float64{1, saturation * a, saturation * b})
	lightness := math.Cbrt(1 / math.Max(lin[0], math.Max(lin[1], lin[2])))
	return lightness, lightness * saturation
}

// okhueMaxChroma 返回亮度 L 处色相 (a, b) 仍在 sRGB 内的最大色度。
func okhueMaxChroma(a, b, lightness, cuspL, cuspC float64) float64 {
	if lightness <= cuspL {
		return cuspC * lightness / cuspL
	}
	t := cuspC * (lightness - 1) / (cuspL - 1)
	slopes := lmsSlopes(a, b)
	best := math.MaxFloat64
	for _, row := range [3][3]float64{
		{4.0767416360759583, -3.3077115392580629, 0.2309699031821043},
		{-1.2684379732850315, 2.6097573492876882, -0.341319376002657},
		{-0.0041960761386756, -0.7034186179359362, 1.7076146940746117},
	} {
		var f, f1, f2 float64
		for i := 0; i < 3; i++ {
			lms := lightness + t*slopes[i]
			f += row[i] * lms * lms * lms
			f1 += row[i] * 3 * slopes[i] * lms * lms
			f2 += row[i] * 6 * slopes[i] * slopes[i] * lms
		}
		f -= 1
		if u := f1 / (f1*f1 - 0.5*f*f2); u >= 0 {
			best = math.Min(best, t-f*u)
		}
	}
	return best
}

// okhslChromaStops 返回亮度 L、色相 (a, b) 下的色度参考点 [c0, cMid, cMax]。
func okhslChromaStops(l, a, b float64) [3]float64 {
	cuspL, cuspC := okhueCusp(a, b)
	cMax := okhueMaxChroma(a, b, l, cuspL, cuspC)
	k := cMax / math.Min(l*(cuspC/cuspL), (1-l)*(cuspC/(1-cuspL)))
	midS := 0.11516993 +
		1/(7.4477897+
			4.1590124*b+
			a*(-2.19557347+
				1.75198401*b+
				a*(-2.13704948-10.02301043*b+a*(-4.24894561+5.38770819*b+4.69891013*a))))
	midT := 0.11239642 +
		1/(1.6132032-
			0.68124379*b+
			a*(0.40370612+
				0.90148123*b+
				a*(-0.27087943+0.6122399*b+a*(0.00299215-0.45399568*b-0.14661872*a))))
	cMid := 0.9 * k * math.Sqrt(math.Sqrt(1/(1/math.Pow(l*midS, 4)+1/math.Pow((1-l)*midT, 4))))
	c0 := math.Sqrt(1 / (1/math.Pow(l*0.4, 2) + 1/math.Pow((1-l)*0.8, 2)))
	return [3]float64{c0, cMid, cMax}
}

// okhslToSRGB 把 OKHSL（h 度，s/l 为 0..1）转成 0..1 的 sRGB 分量（越界时 clamp）。
func okhslToSRGB(hdeg, s, l float64) [3]float64 {
	s = clamp01(s)
	l = clamp01(l)
	lab := [3]float64{okhslToOklabL(l), 0, 0}
	if lab[0] > 0 && lab[0] < 1 && s > 0 {
		angle := 2 * math.Pi * math.Mod(math.Mod(hdeg, 360)+360, 360) / 360
		a, b := math.Cos(angle), math.Sin(angle)
		stops := okhslChromaStops(lab[0], a, b)
		c0, cMid, cMax := stops[0], stops[1], stops[2]
		var chroma float64
		if s < 0.8 {
			t := 1.25 * s
			k1 := 0.8 * c0
			chroma = t * k1 / (1 - (1-k1/cMid)*t)
		} else {
			t := 5 * (s - 0.8)
			k1 := 0.2 * cMid * cMid * 1.25 * 1.25 / c0
			chroma = cMid + t*k1/(1-(1-k1/(cMax-cMid))*t)
		}
		lab = [3]float64{lab[0], chroma * a, chroma * b}
	}
	return linearToSRGBClamped(oklabToLinearRGB(lab))
}

// oklchToSRGB 把 OKLCH（L 0..1，C，h 度）转成 0..1 的 sRGB 分量（越界时 clamp）。
func oklchToSRGB(l, c, hdeg float64) [3]float64 {
	h := math.Mod(hdeg, 360)
	if h < 0 {
		h += 360
	}
	hue := h * math.Pi / 180
	return linearToSRGBClamped(oklabToLinearRGB([3]float64{clamp01(l), math.Max(0, c) * math.Cos(hue), math.Max(0, c) * math.Sin(hue)}))
}

// okhslToOklabL 把 OKHSL 亮度映射到 OKLab 亮度。
func okhslToOklabL(x float64) float64 { return (x*x + okhslK1*x) / (okhslK3 * (x + okhslK2)) }

// clamp01 限制到 0..1。
func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// oklabFromLinearRGB 线性 sRGB → OKLab（CSS Color 4 的矩阵）。
func oklabFromLinearRGB(r, g, b float64) [3]float64 {
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
	l, m, s = math.Cbrt(l), math.Cbrt(m), math.Cbrt(s)
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// oklabToLinearRGB OKLab → 线性 sRGB（可能越界）。
func oklabToLinearRGB(lab [3]float64) [3]float64 {
	l := lab[0] + 0.3963377774*lab[1] + 0.2158037573*lab[2]
	m := lab[0] - 0.1055613458*lab[1] - 0.0638541728*lab[2]
	s := lab[0] - 0.0894841775*lab[1] - 1.2914855480*lab[2]
	l, m, s = l*l*l, m*m*m, s*s*s
	return [3]float64{
		4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.7076147010*s,
	}
}

func srgbToLinear(c float64) float64 {
	if c >= 0.04045 {
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return c / 12.92
}

// linearToSRGBClamped 线性 sRGB → 0..1 的 sRGB 分量，越界分量 clamp 到边界。
func linearToSRGBClamped(lin [3]float64) [3]float64 {
	var out [3]float64
	for i, v := range lin {
		v = clamp01(v)
		if v >= 0.0031308 {
			out[i] = 1.055*math.Pow(v, 1/2.4) - 0.055
		} else {
			out[i] = 12.92 * v
		}
	}
	return out
}
