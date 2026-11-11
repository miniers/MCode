package mcodeui

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	browserServerOutputMaxBytes = 16 << 10
	browserServerMaxCandidates  = 24
	browserServerProbeTimeout   = 800 * time.Millisecond
	browserServerProbeInterval  = 2 * time.Second
	browserServerExpiry         = 2 * time.Minute
)

type browserServerCandidate struct {
	threadID, workspace, url, source string
	lastSeen                         time.Time
	alive, evidence                  bool
}
type browserServerDiscovery struct {
	mu                  sync.Mutex
	threadID, workspace string
	visible             bool
	candidates          map[string]*browserServerCandidate
	cancel              context.CancelFunc
	lastProbe           time.Time
	newCount            int
}

var browserServerDiscoveries = struct {
	sync.Mutex
	states map[*chatState]*browserServerDiscovery
}{states: make(map[*chatState]*browserServerDiscovery)}
var browserServerURLPattern = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|\[::1\]|0\.0\.0\.0)(?::[0-9]{1,5})?(?:[^\s<>"']*)?`)

func browserServerOrigin(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, ".,;:!?)]}\"'"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1":
	case "0.0.0.0":
		host = "localhost"
	default:
		return ""
	}
	port := u.Port()
	if port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return ""
		}
	}
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}
func browserServerURLs(text string) []string {
	if len(text) > browserServerOutputMaxBytes {
		text = text[len(text)-browserServerOutputMaxBytes:]
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range browserServerURLPattern.FindAllString(text, -1) {
		origin := browserServerOrigin(raw)
		if origin != "" && !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out
}
func browserServerDiscoveryFor(s *chatState) *browserServerDiscovery {
	browserServerDiscoveries.Lock()
	defer browserServerDiscoveries.Unlock()
	d := browserServerDiscoveries.states[s]
	if d == nil {
		d = &browserServerDiscovery{candidates: make(map[string]*browserServerCandidate)}
		browserServerDiscoveries.states[s] = d
	}
	return d
}
func browserServerObserveToolOutput(s *chatState, update ThreadUpdate) {
	if s == nil || update.ThreadID == "" {
		return
	}
	s.mu.Lock()
	active := s.selectedThreadID == update.ThreadID
	slot := inspectorExistingSessionLocked(s, update.ThreadID)
	active = active && slot != nil && slot.Open && slot.Active == sidePanePreviewID
	s.mu.Unlock()
	if !active {
		return
	}
	_, root, main := getSelectedThreadDirs(s)
	workspace := root + "\x00" + main
	d := browserServerDiscoveryFor(s)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.threadID != update.ThreadID || d.workspace != workspace {
		d.stopLocked()
		d.threadID, d.workspace = update.ThreadID, workspace
		d.candidates = make(map[string]*browserServerCandidate)
	}
	d.visible = true
	for _, m := range update.Messages {
		if m.Role != "tool" || m.Text == "" {
			continue
		}
		for _, origin := range browserServerURLs(m.Text) {
			d.addLocked(update.ThreadID, workspace, origin, "tool")
		}
	}
}
func (d *browserServerDiscovery) addLocked(tid, workspace, origin, source string) {
	now := time.Now()
	c := d.candidates[origin]
	if c == nil {
		if len(d.candidates) >= browserServerMaxCandidates {
			return
		}
		c = &browserServerCandidate{threadID: tid, workspace: workspace, url: origin, source: source}
		d.candidates[origin] = c
		d.newCount++
	}
	c.evidence = true
	c.lastSeen = now
	c.source = source
}
func (d *browserServerDiscovery) stopLocked() {
	d.visible = false
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}
func browserServerDiscoveryStop(s *chatState) {
	if s == nil {
		return
	}
	browserServerDiscoveries.Lock()
	d := browserServerDiscoveries.states[s]
	browserServerDiscoveries.Unlock()
	if d != nil {
		d.mu.Lock()
		d.stopLocked()
		d.mu.Unlock()
	}
}
func browserServerDiscoveryDispose(s *chatState) {
	if s == nil {
		return
	}
	browserServerDiscoveries.Lock()
	d := browserServerDiscoveries.states[s]
	delete(browserServerDiscoveries.states, s)
	browserServerDiscoveries.Unlock()
	if d != nil {
		d.mu.Lock()
		d.stopLocked()
		d.mu.Unlock()
	}
}
func browserServerProbe(ctx context.Context, url string) bool {
	client := &http.Client{Timeout: browserServerProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}
func browserServerDiscoveryTick(c *ui.Context, s *chatState, threadID string) {
	if c == nil || s == nil || threadID == "" {
		return
	}
	d := browserServerDiscoveryFor(s)
	now := c.Now()
	d.mu.Lock()
	if d.threadID != threadID || !d.visible {
		d.stopLocked()
		d.mu.Unlock()
		return
	}
	for k, candidate := range d.candidates {
		if !candidate.alive && now.Sub(candidate.lastSeen) > browserServerExpiry {
			delete(d.candidates, k)
		}
	}
	if d.cancel == nil && now.Sub(d.lastProbe) >= browserServerProbeInterval {
		var target string
		for _, x := range d.candidates {
			if x.evidence {
				target = x.url
				break
			}
		}
		if target != "" {
			ctx, cancel := context.WithTimeout(context.Background(), browserServerProbeTimeout)
			d.cancel = cancel
			d.lastProbe = now
			go func() {
				alive := browserServerProbe(ctx, target)
				d.mu.Lock()
				if d.visible && d.threadID == threadID {
					if x := d.candidates[target]; x != nil {
						x.alive = alive
					}
				}
				if d.cancel != nil {
					d.cancel = nil
				}
				d.mu.Unlock()
			}()
		}
	}
	busy := d.cancel != nil
	d.mu.Unlock()
	if busy {
		c.After(browserServerProbeInterval)
	}
}
func renderBrowserServerCandidates(c *ui.Context, s *chatState, threadID string, quickPorts []string) {
	if s != nil && threadID != "" {
		s.mu.Lock()
		active := s.selectedThreadID == threadID
		if slot := inspectorExistingSessionLocked(s, threadID); slot == nil || !slot.Open || slot.Active != sidePanePreviewID {
			active = false
		}
		s.mu.Unlock()
		if active {
			_, root, main := getSelectedThreadDirs(s)
			d := browserServerDiscoveryFor(s)
			d.mu.Lock()
			if d.threadID != threadID || d.workspace != root+"\x00"+main {
				d.stopLocked()
				d.threadID, d.workspace = threadID, root+"\x00"+main
				d.candidates = make(map[string]*browserServerCandidate)
			}
			d.visible = true
			d.mu.Unlock()
		}
	}
	browserServerDiscoveryTick(c, s, threadID)
	d := browserServerDiscoveryFor(s)
	d.mu.Lock()
	var discovered []browserServerCandidate
	for _, x := range d.candidates {
		if x.evidence && x.alive {
			discovered = append(discovered, *x)
		}
	}
	newCount := d.newCount
	d.newCount = 0
	d.mu.Unlock()
	render := func(key, label, target string) {
		btn := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(target).Radius(RadiusXS).Padding(Space0, Space2).Background(ThemeOverlay(c, .06)).Transition(motionListItem(c))
		if btn.Hovered() {
			btn.Background(ThemeHover(c))
		}
		if btn.Clicked() {
			openPreviewURL(s, target)
		}
		btn.Children(func() {
			ui.Text(c, label).FontSize(TypeCode).Font("monospace").FontWeight(500).TextColor(ThemeSecondaryText(c))
		})
	}
	for _, x := range discovered {
		render("preview-server-"+x.url, x.url, x.url)
	}
	for _, port := range quickPorts {
		render("preview-port-"+port, port, "http://localhost:"+port)
	}
	if newCount > 0 {
		count := newCount
		badge := ui.Row(c.Key("preview-server-discovery-badge")).Gap(Space1).AlignItems(ui.Center)
		badge.Children(func() {
			ui.Text(c, "+").FontSize(TypeCaption)
			prev := ui.Local(badge, "server-count", func() int { return count })
			_, fade := motionNumberProgress(c, badge, *prev != count)
			*prev = count
			ui.Text(c, strconv.Itoa(count)).FontSize(TypeCode).TextColor(ThemeAccent(c).Alpha(fade))
		})
	}
}
