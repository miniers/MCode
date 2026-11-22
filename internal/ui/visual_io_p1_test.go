package mcodeui

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/testenv"
)

func srgbToLinear(v uint8) float64 {
	s := float64(v) / 255.0
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

func relativeLuminance(c ui.Color) float64 {
	return 0.2126*srgbToLinear(c.R) + 0.7152*srgbToLinear(c.G) + 0.0722*srgbToLinear(c.B)
}

func compositeOverOpaque(fg, bg ui.Color) ui.Color {
	if fg.A >= 255 {
		return fg
	}
	a := float64(fg.A) / 255.0
	return ui.Color{
		R: uint8(math.Round(float64(fg.R)*a + float64(bg.R)*(1-a))),
		G: uint8(math.Round(float64(fg.G)*a + float64(bg.G)*(1-a))),
		B: uint8(math.Round(float64(fg.B)*a + float64(bg.B)*(1-a))),
		A: 255,
	}
}

func wcagContrastRatio(fg, bg ui.Color) float64 {
	l1 := relativeLuminance(compositeOverOpaque(fg, bg))
	l2 := relativeLuminance(bg)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func TestVisualIOP1LightContrastWCAG(t *testing.T) {
	backgrounds := []struct {
		name  string
		color ui.Color
	}{
		{name: "#FFFFFF", color: ui.Hex("#FFFFFF")},
		{name: "#F5F5F3", color: ui.Hex("#F5F5F3")},
	}
	foregrounds := []struct {
		name  string
		color ui.Color
	}{
		{name: "ColorTextTertiaryLight", color: ColorTextTertiaryLight},
		{name: "ColorAccentLight", color: ColorAccentLight},
		{name: "ColorDangerLight", color: ColorDangerLight},
		{name: "ColorWarningLight", color: ColorWarningLight},
		{name: "ColorSuccessLight", color: ColorSuccessLight},
		{name: "ColorCautionLight", color: ColorCautionLight},
		{name: "ColorPlanLight", color: ColorPlanLight},
	}

	for _, fg := range foregrounds {
		for _, bg := range backgrounds {
			ratio := wcagContrastRatio(fg.color, bg.color)
			if ratio < 4.5 {
				t.Errorf("%s on %s contrast = %.2f:1, want >= 4.5:1", fg.name, bg.name, ratio)
			}
		}
	}
}

func TestVisualIOP1NoEmojiInMigratedFiles(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		effort string
		fast   bool
		want   string
	}{
		{name: "fast only", model: "gpt-5", fast: true, want: "gpt-5 · 极速"},
		{name: "effort and fast", model: "gpt-5", effort: "high", fast: true, want: "gpt-5 · 高 · 极速"},
		{name: "empty model", model: "", fast: true, want: "跟随会话子代理模型"},
	}
	for _, tc := range cases {
		got := swarmModelCapsuleText(nil, "", tc.model, tc.effort, tc.fast)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "⚡") {
			t.Errorf("%s: output still contains lightning emoji: %q", tc.name, got)
		}
	}

	s := newChatState(NewFakeClient())
	tt := ui.NewTester(func(c *ui.Context) {
		renderDraftThreadMentions(c, s, []ThreadMention{{ThreadID: "t2", Title: "关联会话"}})
	}, 400, 120)
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "💬") {
			t.Fatalf("draft thread mention still contains emoji: %q", txt)
		}
	}
	if !tt.HasText("@关联会话") {
		t.Fatalf("expected @关联会话 in mention row, texts=%q", tt.Texts())
	}
}

func TestVisualIOP1OpenInAndDownloadsNoSyncIOInRender(t *testing.T) {
	t.Run("openInAsyncCache", func(t *testing.T) {
		testenv.TempHome(t)
		clearOpenInAppsCache()
		t.Cleanup(clearOpenInAppsCache)

		origGOOS := openInGOOS
		openInGOOS = "darwin"
		t.Cleanup(func() { openInGOOS = origGOOS })

		fake := newGitFakeClient()
		s, root := buildRoot(fake)
		t.Cleanup(s.close)

		st := NewSettingsState(settingsTestDir(t), nil, s)
		st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
			{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
		}
		setChatSettings(s, st)

		origStat := openInStatFn
		t.Cleanup(func() { openInStatFn = origStat })

		probeStarted := make(chan struct{}, 1)
		probeGate := make(chan struct{})
		var gateOnce sync.Once
		closeGate := func() { gateOnce.Do(func() { close(probeGate) }) }
		t.Cleanup(closeGate)

		var statCalls atomic.Int32
		openInStatFn = func(path string) bool {
			if statCalls.Add(1) == 1 {
				select {
				case probeStarted <- struct{}{}:
				default:
				}
			}
			<-probeGate
			return path == "/Applications/Cursor.app"
		}

		tt := ui.NewTester(root, 1200, 700)
		waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

		getChatHeaderState(s).openInMenuOpen = true
		for i := 0; i < 5; i++ {
			tt.Frame()
		}

		select {
		case <-probeStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("expected background open-in probe to start")
		}
		if got := statCalls.Load(); got != 1 {
			t.Fatalf("stat calls while probe pending across 5 frames = %d, want 1", got)
		}

		closeGate()
		waitFor(t, tt, func() bool { return tt.HasText("Cursor") })

		for i := 0; i < 5; i++ {
			tt.Frame()
		}
		if got := statCalls.Load(); got != 1 {
			t.Fatalf("stat calls after cache warm across 5 frames = %d, want 1", got)
		}
	})

	t.Run("downloadsAsyncCache", func(t *testing.T) {
		temp := t.TempDir()
		testenv.SetHome(t, temp)
		clearDownloadsCache()
		t.Cleanup(clearDownloadsCache)

		dir := filepath.Join(temp, "Downloads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "demo.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}

		origScan := downloadsScanFn
		t.Cleanup(func() { downloadsScanFn = origScan })

		scanStarted := make(chan struct{}, 1)
		scanGate := make(chan struct{})
		var gateOnce sync.Once
		closeGate := func() { gateOnce.Do(func() { close(scanGate) }) }
		t.Cleanup(closeGate)

		var scanCalls atomic.Int32
		downloadsScanFn = func(d string) ([]downloadEntry, error) {
			if scanCalls.Add(1) == 1 {
				select {
				case scanStarted <- struct{}{}:
				default:
				}
			}
			<-scanGate
			return recentDownloads(d, 15)
		}

		s := newChatState(NewFakeClient())
		tt := ui.NewTester(func(c *ui.Context) {
			s.setUIServices(c.Services())
			renderDownloadsPanel(c, s, nil)
		}, 400, 400)

		select {
		case <-scanStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("expected background downloads scan to start")
		}

		for i := 0; i < 5; i++ {
			tt.Frame()
		}
		if got := scanCalls.Load(); got != 1 {
			t.Fatalf("scan calls while scan pending across 5 frames = %d, want 1", got)
		}

		closeGate()
		waitFor(t, tt, func() bool { return tt.HasText("demo.txt") })

		for i := 0; i < 5; i++ {
			tt.Frame()
		}
		if got := scanCalls.Load(); got != 1 {
			t.Fatalf("scan calls after cache warm across 5 frames = %d, want 1", got)
		}
	})
}
