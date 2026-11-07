package mcodeui

import (
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"
)

// rollingNumbers 保存每个 key 上一次渲染的数字：rollingNumber 的签名只带 key（与框架的动画
// 通道一致），框架又没有给元素开放任意用户状态，所以跨帧状态只能放在这张包级表里。
// 表按最近使用序号剪枝，长时间运行不会无限增长；渲染单线程，用互斥锁保护读写。
var rollingNumbers = struct {
	mu    sync.Mutex
	seq   uint64
	items map[string]*rollingNumberEntry
}{items: make(map[string]*rollingNumberEntry)}

// rollingNumberMaxEntries 是跨帧状态表的条目上限，超出后丢掉最久没用过的那条。
const rollingNumberMaxEntries = 256

// rollingNumberEntry 是一个 key 的跨帧状态。
type rollingNumberEntry struct {
	value   int     // 上一次渲染的数值，用来判断变大还是变小
	shown   []byte  // 上一次显示的字符，滚动期间就是离场的那一份
	dir     float32 // 最近一次变化的方向：+1 变大、-1 变小
	lineH   float32 // 上一帧测到的单个数字高度（DIP），用作裁剪高度
	changed bool    // 值是否刚在这一帧变化；读后立即复位，避免同一帧重复起播
	seq     uint64  // 最近使用的序号，剪枝用
}

// rollingDigits 把整数拆成要逐位渲染的字符，负号单独占一位；0 渲染成 "0"。
func rollingDigits(value int) []byte {
	return []byte(strconv.Itoa(value))
}

// rollingAlign 把 prev 右对齐到 next 的长度：位数变多时高位补 0（这一位没有离场字符），
// 位数变少时从高位截掉多余字符。被截掉的最高位没有离场动画，这是刻意简化：位数变化只在
// 一帧里出现，为它再维持一套离场字符不划算。
func rollingAlign(prev, next []byte) []byte {
	out := make([]byte, len(next))
	if len(prev) > len(next) {
		prev = prev[len(prev)-len(next):]
	}
	off := len(next) - len(prev)
	for i, ch := range prev {
		out[off+i] = ch
	}
	return out
}

// rollingShift 返回滚动进度 k（0 到 1）时进入字符与离场字符的纵向位移：
// dir 为 +1（值变大）时进入字符从下方升起、离场字符向上离开；-1 时方向相反。
func rollingShift(k, dir, lineH float32) (in, out float32) {
	return dir * (1 - k) * lineH, -dir * k * lineH
}

// rollingNumberBegin 取 key 的跨帧状态快照，并在值变化时推进它：变化时记下离场字符
// （上一次显示的那份）与方向，并把 changed 置真；首次出现时 shown 就是当前值，所以不滚动。
func rollingNumberBegin(key string, value int, digits []byte) (shown []byte, dir float32, changed bool, lineH float32) {
	rollingNumbers.mu.Lock()
	defer rollingNumbers.mu.Unlock()
	rollingNumbers.seq++
	e := rollingNumbers.items[key]
	if e == nil {
		e = &rollingNumberEntry{value: value, shown: digits, dir: 1}
		rollingNumbers.items[key] = e
		rollingNumberPruneLocked()
	}
	if e.value != value {
		e.shown = rollingDigits(e.value)
		if value > e.value {
			e.dir = 1
		} else {
			e.dir = -1
		}
		e.value = value
		e.changed = true
	}
	changed = e.changed
	e.changed = false
	return e.shown, e.dir, changed, e.lineH
}

// rollingNumberEnd 写回这一帧测到的数字高度，并在滚动播完时把 shown 落到当前值。
func rollingNumberEnd(key string, digits []byte, lineH float32, done bool) {
	rollingNumbers.mu.Lock()
	defer rollingNumbers.mu.Unlock()
	e := rollingNumbers.items[key]
	if e == nil {
		return
	}
	e.seq = rollingNumbers.seq
	if lineH > 0 {
		e.lineH = lineH
	}
	if done {
		e.shown = digits
	}
}

// rollingNumberPruneLocked 在表超出上限时丢掉最久没用过的一条；调用方须已持锁。
func rollingNumberPruneLocked() {
	if len(rollingNumbers.items) <= rollingNumberMaxEntries {
		return
	}
	oldestKey := ""
	var oldest uint64
	for k, e := range rollingNumbers.items {
		if oldestKey == "" || e.seq < oldest {
			oldestKey, oldest = k, e.seq
		}
	}
	if oldestKey != "" {
		delete(rollingNumbers.items, oldestKey)
	}
}

// rollingNumber 渲染 value，值变化时只滚动字符不同的那几位：进入的字符从下方（变大）
// 或上方（变小）滑入并淡入，离场的字符反向滑出并淡出；首次渲染与数值不变时完全静止。
// 容器带整串数字的 Label，读屏与测试都能把它当一个数看。
func rollingNumber(c *ui.Context, key string, value int) ui.Element {
	digits := rollingDigits(value)
	shown, dir, changed, lineH := rollingNumberBegin(key, value, digits)
	aligned := rollingAlign(shown, digits)

	row := ui.Row(c.Key(key)).Label(strconv.Itoa(value))
	done := true
	var measured float32
	row.Children(func() {
		for i := range digits {
			_, in, _, k := rollingDigitCell(c, key, i, aligned[i], digits[i], dir, changed, lineH)
			if k < 1 {
				done = false
			}
			if h := in.LayoutBounds().H; h > measured {
				measured = h
			}
		}
	})
	rollingNumberEnd(key, digits, measured, done)
	return row
}

// rollingDigitCell 渲染一个数字位，返回该位容器、进入字符与离场字符元素以及滚动进度 k。
// 单元高度取上一帧测得的数字高度：lineH 未知（首帧）时不裁剪，直接按自然高度显示。
func rollingDigitCell(c *ui.Context, key string, idx int, from, to byte, dir float32, changed bool, lineH float32) (cell, in, out ui.Element, k float32) {
	cell = ui.Box(c.Key(key + "/digit/" + strconv.Itoa(idx)))
	rolling := from != 0 && from != to
	// 减少动效：不播滚动，直接显示终值（只退出进入的字符，离场字符也不再建节点）。
	k, fade := float32(1), float32(1)
	if !reduceMotion(c) {
		if changed && rolling {
			cell.Jump("roll", 0)
			cell.Jump("fade", 0)
		}
		k = cell.AnimateTo("roll", 1, ui.Animation{Duration: animateDuration(c, motionRoll), Ease: motionEaseEmphasis})
		fade = cell.AnimateTo("fade", 1, ui.Animation{Duration: animateDuration(c, motionRollFade), Ease: ui.Linear})
	}
	if lineH > 0 {
		cell.Height(lineH).ClipY()
	}
	inShift, outShift := rollingShift(k, dir, lineH)
	cell.Children(func() {
		// 进入的字符排在前面并参与布局，决定这个数字位的宽度。
		in = ui.Text(c, string(to))
		in.Translate(0, inShift).Opacity(fade)
		if rolling && k < 1 {
			out = ui.Text(c, string(from))
			out.Absolute().Top(0).Translate(0, outShift).Opacity(1 - fade)
		}
	})
	return cell, in, out, k
}
