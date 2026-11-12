package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	commandActivityStartAfter = 3 * time.Second
	commandActivityInterval   = 2 * time.Second
	commandActivityTimeout    = 5 * time.Second
)

type commandProcessView struct {
	name   string
	object string
	cpu    float64
	rss    int64
}

type commandActivityState struct {
	command       string
	steps         []string
	estimate      time.Duration
	estimateSet   bool
	previous      map[int]CommandProc
	previousAt    time.Time
	sampleCount   int
	hasProcess    bool
	cpu           float64
	lastBusyAt    time.Time
	maxStep       int
	stepStartedAt time.Time
	processes     []commandProcessView
}

// commandSteps splits a shell script at top-level command separators. Pipes stay
// in their step because they describe one pipeline, not a new progress step.
func commandSteps(cmd string) []string {
	script := toolChipShellScript(cmd)
	var steps []string
	start := 0
	quote := byte(0)
	escaped := false
	flush := func(end int) {
		if part := strings.TrimSpace(script[start:end]); part != "" {
			steps = append(steps, part)
		}
	}
	for i := 0; i < len(script); i++ {
		ch := script[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '\n', ';':
			flush(i)
			start = i + 1
		case '&', '|':
			if i+1 < len(script) && script[i+1] == ch {
				flush(i)
				i++
				start = i + 1
			}
		}
	}
	flush(len(script))
	return steps
}

func commandActivityCommand(m Message) string {
	if fields := toolInputFields(m); fields != nil {
		for _, key := range []string{"command", "cmd", "CommandLine", "command_line", "commandline", "input", "script", "code"} {
			if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
		for _, key := range []string{"args", "argv"} {
			if values, ok := fields[key].([]any); ok && len(values) > 0 {
				parts := make([]string, 0, len(values))
				for _, value := range values {
					parts = append(parts, fmt.Sprint(value))
				}
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

func commandActivityWords(s string) []string {
	var words []string
	var b strings.Builder
	quote := byte(0)
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			b.WriteByte(ch)
			escaped = false
			continue
		}
		if quote != '\'' && ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				b.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			b.WriteByte(ch)
		}
	}
	flush()
	return words
}

func commandActivityBasename(s string) string {
	return filepath.Base(strings.TrimSpace(s))
}

func commandActivityIgnored(name string) bool {
	switch strings.ToLower(commandActivityBasename(name)) {
	case "bash", "sh", "zsh", "dash", "tail", "head", "grep", "tee":
		return true
	default:
		return false
	}
}

func commandActivityCPU(previous map[int]CommandProc, previousAt time.Time, p CommandProc, now time.Time) float64 {
	if old, ok := previous[p.PID]; ok {
		interval := now.Sub(previousAt)
		if interval > 0 {
			delta := p.CPUTime - old.CPUTime
			if delta < 0 {
				delta = 0
			}
			return delta.Seconds() / interval.Seconds() * 100
		}
	}
	age := now.Sub(p.StartTime)
	if age <= 0 || p.CPUTime <= 0 {
		return 0
	}
	return p.CPUTime.Seconds() / age.Seconds() * 100
}

func commandActivityStepMatches(step string, p CommandProc) bool {
	stepWords := commandActivityWords(step)
	if len(stepWords) == 0 {
		return false
	}
	for len(stepWords) > 0 && strings.Contains(stepWords[0], "=") && !strings.HasPrefix(stepWords[0], "=") {
		stepWords = stepWords[1:]
	}
	if len(stepWords) == 0 {
		return false
	}
	argv := commandActivityWords(p.Command)
	if len(argv) == 0 {
		return false
	}
	name := strings.ToLower(commandActivityBasename(argv[0]))
	first := strings.ToLower(commandActivityBasename(stepWords[0]))
	second := ""
	if len(stepWords) > 1 && !strings.HasPrefix(stepWords[1], "-") {
		second = stepWords[1]
	}
	if (name == "compile" || name == "asm" || name == "link") && first == "go" && len(stepWords) > 1 && stepWords[1] == "build" {
		return true
	}
	if name == "vet" && first == "go" && len(stepWords) > 1 && stepWords[1] == "vet" {
		return true
	}
	if strings.HasSuffix(name, ".test") && first == "go" && len(stepWords) > 1 && stepWords[1] == "test" {
		return true
	}
	if name != first {
		return false
	}
	if second == "" {
		return true
	}
	for _, arg := range argv[1:] {
		if arg == second {
			return true
		}
	}
	return false
}

func commandActivityObject(argv []string, name string) string {
	if name == "compile" || name == "vet" || name == "asm" || name == "link" {
		for i := 1; i < len(argv); i++ {
			if argv[i] == "-p" && i+1 < len(argv) {
				return argv[i+1]
			}
			if strings.HasPrefix(argv[i], "-p=") {
				return strings.TrimPrefix(argv[i], "-p=")
			}
		}
	}
	if strings.HasSuffix(name, ".test") {
		return strings.TrimSuffix(name, ".test")
	}
	for _, arg := range argv[1:] {
		if !strings.HasPrefix(arg, "-") {
			return truncateRunes(arg, 40)
		}
	}
	return ""
}

func commandActivityProcessViews(previous map[int]CommandProc, previousAt time.Time, procs []CommandProc, now time.Time) ([]commandProcessView, float64) {
	children := make(map[int]bool, len(procs))
	for _, p := range procs {
		children[p.PPID] = true
	}
	views := make([]commandProcessView, 0, len(procs))
	var totalCPU float64
	for _, p := range procs {
		cpu := commandActivityCPU(previous, previousAt, p, now)
		totalCPU += cpu
		if children[p.PID] {
			continue
		}
		argv := commandActivityWords(p.Command)
		if len(argv) == 0 || commandActivityIgnored(argv[0]) {
			continue
		}
		name := commandActivityBasename(argv[0])
		if strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")") {
			name = strings.TrimSuffix(strings.TrimPrefix(name, "("), ")")
		}
		views = append(views, commandProcessView{name: name, object: commandActivityObject(argv, strings.ToLower(name)), cpu: cpu, rss: p.RSS})
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].cpu > views[j].cpu })
	if len(views) > 3 {
		views = views[:3]
	}
	return views, totalCPU
}

func updateCommandActivity(st *commandActivityState, procs []CommandProc, now time.Time) {
	if st == nil {
		return
	}
	views, totalCPU := commandActivityProcessViews(st.previous, st.previousAt, procs, now)
	st.cpu = totalCPU
	if st.sampleCount == 0 {
		st.lastBusyAt = now
	}
	if totalCPU >= 5 {
		st.lastBusyAt = now
	}
	st.sampleCount++
	st.hasProcess = st.hasProcess || len(procs) > 0
	start := st.maxStep - 1
	if start < 0 {
		start = 0
	}
	for i := start; i < len(st.steps); i++ {
		matched := false
		for _, p := range procs {
			if commandActivityStepMatches(st.steps[i], p) {
				matched = true
				break
			}
		}
		if matched {
			if i+1 > st.maxStep {
				st.maxStep = i + 1
				st.stepStartedAt = now
			}
			break
		}
	}
	st.processes = views
	st.previous = make(map[int]CommandProc, len(procs))
	for _, p := range procs {
		st.previous[p.PID] = p
	}
	st.previousAt = now
}

func commandActivityNoOutputExpected(step string) bool {
	quote := byte(0)
	escaped := false
	for i := 0; i < len(step); i++ {
		ch := step[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '>' {
			redirectEnd := i + 1
			if redirectEnd < len(step) && step[redirectEnd] == '>' {
				redirectEnd++
			}
			if redirectEnd < len(step) && step[redirectEnd] == '&' {
				continue
			}
			if i > 0 && step[i-1] == '2' {
				target := redirectEnd
				for target < len(step) && (step[target] == ' ' || step[target] == '\t') {
					target++
				}
				if strings.HasPrefix(step[target:], "/dev/null") {
					end := target + len("/dev/null")
					if end == len(step) || strings.ContainsRune(" \t\r\n|;&", rune(step[end])) {
						continue
					}
				}
			}
			return true
		}
		if ch != '|' || (i+1 < len(step) && step[i+1] == '|') {
			continue
		}
		j := i + 1
		for j < len(step) && (step[j] == ' ' || step[j] == '\t') {
			j++
		}
		k := j
		for k < len(step) && step[k] != ' ' && step[k] != '\t' && step[k] != '|' {
			k++
		}
		next := strings.ToLower(commandActivityBasename(step[j:k]))
		if next == "tail" || next == "head" {
			return true
		}
	}
	return false
}

func commandStepStatus(i, maxStep int) string {
	if maxStep <= 0 {
		return "pending"
	}
	if i < maxStep-1 {
		return "completed"
	}
	if i == maxStep-1 {
		return "in_progress"
	}
	return "pending"
}

func commandActivitySnapshot(s *chatState, id string) commandActivityState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.commandActivity[id]; st != nil {
		copy := *st
		copy.steps = append([]string(nil), st.steps...)
		copy.processes = append([]commandProcessView(nil), st.processes...)
		return copy
	}
	return commandActivityState{}
}

func commandActivityEstimateLocked(s *chatState, command string) time.Duration {
	want := strings.Join(strings.Fields(command), " ")
	var latest time.Time
	var estimate time.Duration
	for _, m := range s.messages {
		if !strings.EqualFold(strings.TrimSpace(m.ToolStatus), "completed") || strings.TrimSpace(m.ToolCompletedAt) == "" {
			continue
		}
		if strings.Join(strings.Fields(commandActivityCommand(m)), " ") != want {
			continue
		}
		created, okCreated := parseRFC3339Time(m.CreatedAt)
		completed, okCompleted := parseRFC3339Time(m.ToolCompletedAt)
		if !okCreated || !okCompleted || completed.Before(created) || !created.After(latest) {
			continue
		}
		latest = created
		estimate = completed.Sub(created)
	}
	return estimate
}

func ensureCommandActivityLocked(s *chatState, m Message) *commandActivityState {
	if s.commandActivity == nil {
		s.commandActivity = make(map[string]*commandActivityState)
	}
	command := commandActivityCommand(m)
	st := s.commandActivity[m.ID]
	if st == nil {
		st = &commandActivityState{command: command, steps: commandSteps(command), previous: make(map[int]CommandProc)}
		st.estimate = commandActivityEstimateLocked(s, command)
		st.estimateSet = true
		s.commandActivity[m.ID] = st
	}
	return st
}

func requestCommandActivity(c *ui.Context, s *chatState, threadID string, now time.Time) {
	if s == nil || threadID == "" {
		return
	}
	s.mu.Lock()
	if s.client == nil {
		s.mu.Unlock()
		return
	}
	if s.commandActivityPending == nil {
		s.commandActivityPending = make(map[string]bool)
	}
	if s.commandActivityNext == nil {
		s.commandActivityNext = make(map[string]time.Time)
	}
	if s.commandActivityGeneration == nil {
		s.commandActivityGeneration = make(map[string]uint64)
	}
	if s.commandActivityPending[threadID] || now.Before(s.commandActivityNext[threadID]) {
		s.mu.Unlock()
		return
	}
	ids := make([]string, 0)
	for _, m := range s.messages {
		if !toolIsRunningCommand(m) {
			continue
		}
		start, ok := parseRFC3339Time(m.CreatedAt)
		if !ok || now.Sub(start) < commandActivityStartAfter {
			continue
		}
		ensureCommandActivityLocked(s, m)
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		s.mu.Unlock()
		return
	}
	s.commandActivityPending[threadID] = true
	s.commandActivityNext[threadID] = now.Add(commandActivityInterval)
	s.commandActivityGeneration[threadID]++
	generation := s.commandActivityGeneration[threadID]
	client := s.client
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandActivityTimeout)
		defer cancel()
		activity, err := client.RunningCommandActivity(ctx, threadID)
		sampleAt := time.Now()
		s.mu.Lock()
		s.commandActivityPending[threadID] = false
		valid := err == nil && s.selectedThreadID == threadID && s.commandActivityGeneration[threadID] == generation
		if valid {
			for _, id := range ids {
				if st := s.commandActivity[id]; st != nil {
					updateCommandActivity(st, activity[id], sampleAt)
				}
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

func renderRunningCommandHeader(c *ui.Context, s *chatState, m Message, fontFam string) {
	s.mu.Lock()
	ensureCommandActivityLocked(s, m)
	killing := s.killingTools[m.ID]
	s.mu.Unlock()
	st := commandActivitySnapshot(s, m.ID)
	ui.Row(c.Key("command-header-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Start).Children(func() {
		ui.Column(c.Key("command-steps-" + m.ID)).Grow(1).MinWidth(0).Gap(Space1).Children(func() {
			if len(st.steps) < 2 || !isToolRunning(m) {
				return
			}
			for i, step := range st.steps {
				status := commandStepStatus(i, st.maxStep)
				ui.Row(c.Key(fmt.Sprintf("command-step-%s-%d", m.ID, i))).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
					renderTodoStatusIcon(c, fmt.Sprintf("command-step-icon-%s-%d", m.ID, i), status, isToolRunning(m))
					color := ThemeSecondaryText(c)
					if status == "completed" {
						color = color.Alpha(0.75)
					} else if status == "in_progress" {
						color = ThemePrimaryText(c)
					}
					label := ui.Text(c, step).Font(fontFam).FontSize(scaledFont(TypeCaption)).SingleLine().Grow(1).MinWidth(0).TextColor(color)
					if status == "in_progress" {
						label.FontWeight(500)
					}
					if i == st.maxStep-1 && !st.stepStartedAt.IsZero() {
						elapsed := c.Now().Sub(st.stepStartedAt)
						if elapsed < 0 {
							elapsed = 0
						}
						ui.Text(c, formatToolRunDuration(elapsed)).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Shrink(0)
					}
				})
			}
		})
		IconButton(c, IconButtonProps{Key: "tool-kill-" + m.ID, Icon: IconCircleStop(), Label: "终止此命令", Tooltip: "终止此命令", Size: ControlXS, Variant: ButtonGhost, Loading: killing, Disabled: killing, OnClick: func() { s.killRunningCommand(m.ID) }}).Shrink(0)
	})
}

func renderRunningCommandActivity(c *ui.Context, s *chatState, m Message, fontFam string, outputMax float32) {
	st := commandActivitySnapshot(s, m.ID)
	if len(st.processes) > 0 {
		_, stalled := s.runningCommandBadge(c, m)
		ui.Column(c.Key("command-processes-"+m.ID)).FillWidth().Padding(0, 0, 0, Space5).Gap(Space1).Children(func() {
			captionSize := scaledFont(TypeCaption)
			for i, p := range st.processes {
				ui.Row(c.Key(fmt.Sprintf("command-process-%s-%d", m.ID, i))).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
					ui.Text(c, p.name).FontSize(captionSize).Width(captionSize * (90 / TypeCaption)).SingleLine().TextColor(ThemePrimaryText(c))
					ui.Text(c, p.object).FontSize(captionSize).Grow(1).MinWidth(0).SingleLine().TextColor(ThemeSecondaryText(c))
					fill, cpuColor := ThemeAccent(c), ThemeSecondaryText(c)
					if stalled {
						fill, cpuColor = ThemeWarning(c), ThemeWarning(c)
					}
					ui.Box(c).Width(captionSize * (40 / TypeCaption)).Shrink(0).Children(func() {
						ProgressBar(c, ProgressBarProps{Key: fmt.Sprintf("command-cpu-%s-%d", m.ID, i), Label: "CPU", Ratio: float32(p.cpu / 100), Height: Space1, Fill: fill, Track: ThemeBorder(c)})
					})
					ui.Text(c, fmt.Sprintf("%.0f%%", p.cpu)).FontSize(captionSize).Width(captionSize * (42 / TypeCaption)).Shrink(0).TextAlign(ui.End).TextColor(cpuColor)
					memory := ""
					if p.rss >= 1024*1024 {
						memory = fmt.Sprintf("%d MB", p.rss/(1024*1024))
					}
					ui.Text(c, memory).FontSize(captionSize).Width(captionSize * (58 / TypeCaption)).Shrink(0).TextAlign(ui.End).TextColor(ThemeSecondaryText(c))
				})
			}
		})
	}
	text := m.Text
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[idx+1:]
	} else {
		text = ""
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	stScroll := s.toolOutputScrollStateFor(m.ID)
	if stScroll.MaxY <= 0 || stScroll.Y >= stScroll.MaxY-toolOutputFollowThreshold {
		stScroll.Y = 1e9
	}
	ui.Box(c.Key("tool-output-wrap-" + m.ID)).FillWidth().Children(func() {
		ui.Scroll(c.Key("tool-output-scroll-" + m.ID)).FillWidth().MaxHeight(outputMax).TrackScroll(stScroll).Children(func() {
			ui.Text(c, text).Font(fontFam).FontSize(scaledFont(codeFontSize())).LineHeight(1.4).TextColor(ThemeSecondaryText(c)).Selectable()
		})
	})
}
