package tools

import (
	"fmt"
	"strings"
)

type diffLine struct {
	kind byte
	text string
}

func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter 会在末尾多出一个空串，去掉它，避免把空串当成真实的一行。
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
func unifiedDiff(path, old, updated string) string {
	if old == updated {
		return ""
	}
	a, b := diffLines(old), diffLines(updated)
	var ops []diffLine
	if len(a) > 5000 || len(b) > 5000 {
		for _, s := range a {
			ops = append(ops, diffLine{'-', s})
		}
		for _, s := range b {
			ops = append(ops, diffLine{'+', s})
		}
	} else {
		width := len(b) + 1
		dp := make([]uint16, (len(a)+1)*width)
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					dp[i*width+j] = dp[(i+1)*width+j+1] + 1
				} else if dp[(i+1)*width+j] >= dp[i*width+j+1] {
					dp[i*width+j] = dp[(i+1)*width+j]
				} else {
					dp[i*width+j] = dp[i*width+j+1]
				}
			}
		}
		i, j := 0, 0
		for i < len(a) || j < len(b) {
			if i < len(a) && j < len(b) && a[i] == b[j] {
				ops = append(ops, diffLine{' ', a[i]})
				i++
				j++
			} else if i < len(a) && (j == len(b) || dp[(i+1)*width+j] >= dp[i*width+j+1]) {
				ops = append(ops, diffLine{'-', a[i]})
				i++
			} else {
				ops = append(ops, diffLine{'+', b[j]})
				j++
			}
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", strings.TrimPrefix(path, "/"), strings.TrimPrefix(path, "/"))
	oldPos, newPos := make([]int, len(ops)+1), make([]int, len(ops)+1)
	oldPos[0] = 1
	newPos[0] = 1
	for i, x := range ops {
		oldPos[i+1] = oldPos[i]
		newPos[i+1] = newPos[i]
		if x.kind != '+' {
			oldPos[i+1]++
		}
		if x.kind != '-' {
			newPos[i+1]++
		}
	}
	for i := 0; i < len(ops); {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i == len(ops) {
			break
		}
		start := i - 3
		if start < 0 {
			start = 0
		}
		last := i
		for j := i + 1; j < len(ops); j++ {
			if ops[j].kind != ' ' {
				if j-last > 7 {
					break
				}
				last = j
			}
		}
		end := last + 4
		if end > len(ops) {
			end = len(ops)
		}
		oc, nc := oldPos[end]-oldPos[start], newPos[end]-newPos[start]
		os, ns := oldPos[start], newPos[start]
		if oc == 0 {
			os--
		}
		if nc == 0 {
			ns--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", os, oc, ns, nc)
		for _, x := range ops[start:end] {
			out.WriteByte(x.kind)
			out.WriteString(x.text)
			if !strings.HasSuffix(x.text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		i = end
	}
	return out.String()
}
