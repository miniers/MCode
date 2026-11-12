package git

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
)

type Progress struct {
	Phase   string
	Percent int
	Line    string
}
type progressKey struct{}

func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

var progressPattern = regexp.MustCompile(`^([^:]+):\s*(\d+)%`)

func parseProgress(line string) Progress {
	line = strings.TrimSpace(line)
	p := Progress{Percent: -1, Line: line}
	if m := progressPattern.FindStringSubmatch(line); m != nil {
		p.Phase = strings.TrimSpace(m[1])
		p.Percent, _ = strconv.Atoi(m[2])
	} else if phase, _, ok := strings.Cut(line, ":"); ok {
		p.Phase = strings.TrimSpace(phase)
	}
	return p
}

type progressWriter struct {
	stderr  *bytes.Buffer
	pending []byte
	fn      func(Progress)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.stderr.Write(p)
	for _, b := range p {
		if b == '\r' || b == '\n' {
			w.flush()
		} else {
			w.pending = append(w.pending, b)
		}
	}
	return len(p), nil
}
func (w *progressWriter) flush() {
	if len(w.pending) > 0 {
		w.fn(parseProgress(string(w.pending)))
		w.pending = nil
	}
}
func progressArgs(args []string, fn func(Progress)) []string {
	if fn == nil {
		return args
	}
	i := 0
	for i < len(args) {
		if args[i] == "-c" && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "-c") && len(args[i]) > 2 {
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return args
	}
	switch args[i] {
	case "fetch", "pull", "push", "clone":
		result := make([]string, 0, len(args)+1)
		result = append(result, args[:i+1]...)
		result = append(result, "--progress")
		return append(result, args[i+1:]...)
	}
	return args
}
