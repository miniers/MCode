package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/openartifact/noderuntime"
)

// OpenArtifactClient 提供 open-artifact 的 Node 运行时管理、桥接启停与主页查询能力。
// 由 internal/app 的 Client 实现；界面通过类型断言获取，未实现时不显示「Artifact」分组。
type OpenArtifactClient interface {
	OpenArtifactNodeStatus(ctx context.Context) noderuntime.Status
	DownloadOpenArtifactNode(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error)
	SetOpenArtifactEnabled(enabled bool) error
	OpenArtifactState() (ready bool, err error)
	OpenArtifactHomeURL(ctx context.Context) (string, error)
}

var (
	openArtifactRunOnMain        = mygo.RunOnMain
	openArtifactActiveChatWindow = activeChatWindow
	openArtifactPreviewNewTab    = openPreviewURLInNewTab
	openArtifactOpenExternalURL  = openExternalURL
)

var openArtifactUI struct {
	mu sync.Mutex

	owner    *SettingsState
	client   OpenArtifactClient
	lastOpen bool

	nodeLoaded  bool
	nodeLoading bool
	nodeStatus  noderuntime.Status

	downloading    bool
	downloadPct    int
	downloadCancel context.CancelFunc
	downloadErr    string

	toggleBusy  bool
	toggleErr   string
	bridgeReady bool
	bridgeErr   string
	stateLoaded bool
	pollGen     uint64

	openingHome bool
	homeErr     string
}

func resetOpenArtifactUIForTest() {
	openArtifactUI.mu.Lock()
	cancel := openArtifactUI.downloadCancel
	openArtifactUI.owner = nil
	openArtifactUI.client = nil
	openArtifactUI.lastOpen = false
	openArtifactUI.nodeLoaded = false
	openArtifactUI.nodeLoading = false
	openArtifactUI.nodeStatus = noderuntime.Status{}
	openArtifactUI.downloading = false
	openArtifactUI.downloadPct = 0
	openArtifactUI.downloadCancel = nil
	openArtifactUI.downloadErr = ""
	openArtifactUI.toggleBusy = false
	openArtifactUI.toggleErr = ""
	openArtifactUI.bridgeReady = false
	openArtifactUI.bridgeErr = ""
	openArtifactUI.stateLoaded = false
	openArtifactUI.pollGen++
	openArtifactUI.openingHome = false
	openArtifactUI.homeErr = ""
	openArtifactUI.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func openArtifactClient(st *SettingsState) (OpenArtifactClient, bool) {
	c := archiveClient(st)
	if c == nil {
		return nil, false
	}
	oa, ok := c.(OpenArtifactClient)
	return oa, ok
}

func sameOpenArtifactClient(a, b OpenArtifactClient) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if !ta.Comparable() {
		return true
	}
	return a == b
}

func ensureOpenArtifactLoaded(st *SettingsState) {
	if st == nil {
		return
	}
	client, ok := openArtifactClient(st)
	if !ok {
		return
	}

	isOpen := st.IsOpen()
	var oldCancel context.CancelFunc
	needLoad := false

	openArtifactUI.mu.Lock()
	if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
		oldCancel = openArtifactUI.downloadCancel
		openArtifactUI.owner = st
		openArtifactUI.client = client
		openArtifactUI.lastOpen = isOpen
		openArtifactUI.nodeLoaded = false
		openArtifactUI.nodeLoading = false
		openArtifactUI.nodeStatus = noderuntime.Status{}
		openArtifactUI.downloading = false
		openArtifactUI.downloadPct = 0
		openArtifactUI.downloadCancel = nil
		openArtifactUI.downloadErr = ""
		openArtifactUI.toggleBusy = false
		openArtifactUI.toggleErr = ""
		openArtifactUI.bridgeReady = false
		openArtifactUI.bridgeErr = ""
		openArtifactUI.stateLoaded = false
		openArtifactUI.pollGen++
		openArtifactUI.openingHome = false
		openArtifactUI.homeErr = ""
	} else {
		if openArtifactUI.lastOpen && !isOpen && !openArtifactUI.downloading {
			openArtifactUI.nodeLoaded = false
			openArtifactUI.stateLoaded = false
		}
		openArtifactUI.lastOpen = isOpen
	}
	if !openArtifactUI.nodeLoaded && !openArtifactUI.nodeLoading {
		openArtifactUI.nodeLoading = true
		needLoad = true
	}
	openArtifactUI.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if needLoad {
		go loadOpenArtifactStatus(st, client)
	}
}

func loadOpenArtifactStatus(st *SettingsState, client OpenArtifactClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status := client.OpenArtifactNodeStatus(ctx)
	ready, stateErr := client.OpenArtifactState()

	openArtifactUI.mu.Lock()
	if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
		openArtifactUI.mu.Unlock()
		return
	}
	openArtifactUI.nodeLoaded = true
	openArtifactUI.nodeLoading = false
	openArtifactUI.nodeStatus = status
	openArtifactUI.stateLoaded = true
	openArtifactUI.bridgeReady = ready
	if stateErr != nil {
		openArtifactUI.bridgeErr = stateErr.Error()
	} else {
		openArtifactUI.bridgeErr = ""
	}
	openArtifactUI.mu.Unlock()

	st.invalidate()
}

func setOpenArtifactEnabled(st *SettingsState, enabled bool) {
	if st == nil {
		return
	}
	client, ok := openArtifactClient(st)
	if !ok {
		return
	}

	openArtifactUI.mu.Lock()
	if openArtifactUI.toggleBusy {
		openArtifactUI.mu.Unlock()
		return
	}
	openArtifactUI.toggleBusy = true
	openArtifactUI.toggleErr = ""
	openArtifactUI.bridgeErr = ""
	if !enabled {
		openArtifactUI.bridgeReady = false
	}
	openArtifactUI.pollGen++
	pollGen := openArtifactUI.pollGen
	openArtifactUI.mu.Unlock()

	// 同步写入设置草稿和 base，避免开关弹回或在保存其他草稿时把磁盘配置覆盖回去。
	st.mu.Lock()
	st.cfgMu.Lock()
	prev := st.cfg.OpenArtifact.Enabled
	st.cfg.OpenArtifact.Enabled = enabled
	st.base.OpenArtifact.Enabled = enabled
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.invalidate()

	go func() {
		if err := client.SetOpenArtifactEnabled(enabled); err != nil {
			st.mu.Lock()
			st.cfgMu.Lock()
			st.cfg.OpenArtifact.Enabled = prev
			st.base.OpenArtifact.Enabled = prev
			st.cfgMu.Unlock()
			st.mu.Unlock()

			openArtifactUI.mu.Lock()
			if openArtifactUI.owner == st && sameOpenArtifactClient(openArtifactUI.client, client) {
				openArtifactUI.toggleBusy = false
				openArtifactUI.toggleErr = err.Error()
			}
			openArtifactUI.mu.Unlock()
			st.invalidate()
			return
		}

		ready, stateErr := client.OpenArtifactState()
		openArtifactUI.mu.Lock()
		if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
			openArtifactUI.mu.Unlock()
			return
		}
		openArtifactUI.toggleBusy = false
		openArtifactUI.stateLoaded = true
		openArtifactUI.bridgeReady = ready
		if stateErr != nil {
			openArtifactUI.bridgeErr = stateErr.Error()
		} else {
			openArtifactUI.bridgeErr = ""
		}
		openArtifactUI.mu.Unlock()
		st.invalidate()

		if enabled && !ready && stateErr == nil {
			pollOpenArtifactState(st, client, pollGen)
		}
	}()
}

func pollOpenArtifactState(st *SettingsState, client OpenArtifactClient, pollGen uint64) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for range 120 {
		<-ticker.C

		openArtifactUI.mu.Lock()
		active := openArtifactUI.owner == st && sameOpenArtifactClient(openArtifactUI.client, client) && openArtifactUI.pollGen == pollGen
		openArtifactUI.mu.Unlock()
		if !active {
			return
		}

		st.cfgMu.Lock()
		enabled := st.cfg.OpenArtifact.Enabled
		st.cfgMu.Unlock()
		if !enabled {
			return
		}

		ready, stateErr := client.OpenArtifactState()
		errText := ""
		if stateErr != nil {
			errText = stateErr.Error()
		}

		openArtifactUI.mu.Lock()
		if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) || openArtifactUI.pollGen != pollGen {
			openArtifactUI.mu.Unlock()
			return
		}
		changed := openArtifactUI.bridgeReady != ready || openArtifactUI.bridgeErr != errText
		openArtifactUI.stateLoaded = true
		openArtifactUI.bridgeReady = ready
		openArtifactUI.bridgeErr = errText
		openArtifactUI.mu.Unlock()

		if changed {
			st.invalidate()
		}
		if ready || stateErr != nil {
			return
		}
	}
}

func startOpenArtifactNodeDownload(st *SettingsState) {
	if st == nil {
		return
	}
	client, ok := openArtifactClient(st)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	openArtifactUI.mu.Lock()
	if openArtifactUI.downloading {
		openArtifactUI.mu.Unlock()
		cancel()
		return
	}
	openArtifactUI.downloading = true
	openArtifactUI.downloadPct = 0
	openArtifactUI.downloadCancel = cancel
	openArtifactUI.downloadErr = ""
	openArtifactUI.mu.Unlock()
	st.invalidate()

	go func() {
		defer cancel()
		lastPct := -1
		status, err := client.DownloadOpenArtifactNode(ctx, func(done, total int64) {
			pct := 0
			if total > 0 {
				pct = int(done * 100 / total)
				if pct < 0 {
					pct = 0
				} else if pct > 100 {
					pct = 100
				}
			}
			if pct == lastPct {
				return
			}
			lastPct = pct

			openArtifactUI.mu.Lock()
			if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) || !openArtifactUI.downloading || openArtifactUI.downloadPct == pct {
				openArtifactUI.mu.Unlock()
				return
			}
			openArtifactUI.downloadPct = pct
			openArtifactUI.mu.Unlock()
			st.invalidate()
		})

		openArtifactUI.mu.Lock()
		if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
			openArtifactUI.mu.Unlock()
			return
		}
		openArtifactUI.downloading = false
		openArtifactUI.downloadCancel = nil
		openArtifactUI.downloadPct = 0
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				openArtifactUI.downloadErr = err.Error()
			}
			openArtifactUI.mu.Unlock()
			st.invalidate()
			return
		}
		openArtifactUI.downloadErr = ""
		openArtifactUI.nodeLoaded = true
		openArtifactUI.nodeStatus = status
		openArtifactUI.mu.Unlock()

		st.cfgMu.Lock()
		enabled := st.cfg.OpenArtifact.Enabled
		st.cfgMu.Unlock()

		ready, stateErr := client.OpenArtifactState()
		openArtifactUI.mu.Lock()
		if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
			openArtifactUI.mu.Unlock()
			return
		}
		openArtifactUI.stateLoaded = true
		openArtifactUI.bridgeReady = ready
		if stateErr != nil {
			openArtifactUI.bridgeErr = stateErr.Error()
		} else {
			openArtifactUI.bridgeErr = ""
		}
		openArtifactUI.mu.Unlock()
		st.invalidate()

		if enabled && !ready {
			if startErr := client.SetOpenArtifactEnabled(true); startErr != nil {
				openArtifactUI.mu.Lock()
				if openArtifactUI.owner == st && sameOpenArtifactClient(openArtifactUI.client, client) {
					openArtifactUI.toggleErr = startErr.Error()
				}
				openArtifactUI.mu.Unlock()
				st.invalidate()
				return
			}
			ready, stateErr = client.OpenArtifactState()
			openArtifactUI.mu.Lock()
			if openArtifactUI.owner != st || !sameOpenArtifactClient(openArtifactUI.client, client) {
				openArtifactUI.mu.Unlock()
				return
			}
			openArtifactUI.stateLoaded = true
			openArtifactUI.bridgeReady = ready
			if stateErr != nil {
				openArtifactUI.bridgeErr = stateErr.Error()
			} else {
				openArtifactUI.bridgeErr = ""
			}
			openArtifactUI.pollGen++
			pollGen := openArtifactUI.pollGen
			openArtifactUI.mu.Unlock()
			st.invalidate()

			if !ready && stateErr == nil {
				pollOpenArtifactState(st, client, pollGen)
			}
		}
	}()
}

func cancelOpenArtifactNodeDownload() {
	openArtifactUI.mu.Lock()
	cancel := openArtifactUI.downloadCancel
	openArtifactUI.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func openOpenArtifactHome(st *SettingsState) {
	if st == nil {
		return
	}
	client, ok := openArtifactClient(st)
	if !ok {
		return
	}

	openArtifactUI.mu.Lock()
	if openArtifactUI.openingHome || !openArtifactUI.bridgeReady {
		openArtifactUI.mu.Unlock()
		return
	}
	openArtifactUI.openingHome = true
	openArtifactUI.homeErr = ""
	openArtifactUI.mu.Unlock()
	st.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		homeURL, err := client.OpenArtifactHomeURL(ctx)
		cancel()

		homeURL = strings.TrimSpace(homeURL)
		if err == nil && homeURL == "" {
			err = errors.New("openartifact: 未获取到主页地址")
		}

		openArtifactUI.mu.Lock()
		if openArtifactUI.owner == st && sameOpenArtifactClient(openArtifactUI.client, client) {
			openArtifactUI.openingHome = false
			if err != nil {
				openArtifactUI.homeErr = err.Error()
			}
		}
		openArtifactUI.mu.Unlock()
		st.invalidate()

		if err != nil {
			return
		}
		if win, chat := openArtifactActiveChatWindow(); win != nil && chat != nil && !chat.isClosed() {
			openArtifactRunOnMain(func() {
				if chat.isClosed() {
					openArtifactOpenExternalURL(homeURL)
					return
				}
				openArtifactPreviewNewTab(chat, homeURL)
			})
			return
		}
		openArtifactOpenExternalURL(homeURL)
	}()
}

type openArtifactEnabledControl struct{}

func (openArtifactEnabledControl) render(c *ui.Context, st *SettingsState, s setting) {
	ensureOpenArtifactLoaded(st)

	var enabled bool
	if st != nil {
		st.cfgMu.Lock()
		enabled = st.cfg.OpenArtifact.Enabled
		st.cfgMu.Unlock()
	}

	openArtifactUI.mu.Lock()
	busy := openArtifactUI.toggleBusy
	errText := openArtifactUI.toggleErr
	if errText == "" && enabled && !openArtifactUI.bridgeReady {
		errText = openArtifactUI.bridgeErr
	}
	openArtifactUI.mu.Unlock()

	if errText != "" {
		ui.Text(c, errText).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			SingleLine().
			Shrink(1).
			MinWidth(0).
			Tooltip(errText)
	}

	label := "切换" + s.Title
	if busy {
		SwitchDisabled(c, s.ID+"-switch", enabled, label)
		return
	}
	val := enabled
	Switch(c, s.ID+"-switch", &val, label, func(next bool) {
		setOpenArtifactEnabled(st, next)
	})
}

type openArtifactNodeControl struct{}

func (openArtifactNodeControl) render(c *ui.Context, st *SettingsState, s setting) {
	ensureOpenArtifactLoaded(st)

	openArtifactUI.mu.Lock()
	loaded := openArtifactUI.nodeLoaded
	status := openArtifactUI.nodeStatus
	downloading := openArtifactUI.downloading
	pct := openArtifactUI.downloadPct
	downloadErr := openArtifactUI.downloadErr
	openArtifactUI.mu.Unlock()

	if downloading {
		ui.Text(c, fmt.Sprintf("下载中 %d%%", pct)).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
		ui.Column(c.Key("artifact-node-progress-wrap")).Width(96).Children(func() {
			ProgressBar(c, ProgressBarProps{
				Key:    "artifact-node-progress",
				Label:  "下载 Node",
				Ratio:  float32(pct) / 100,
				Height: Space0,
				Track:  ThemeAccent(c).Alpha(0.22),
				Fill:   ThemeAccent(c),
			})
		})
		Button(c, ButtonOpts{
			Key:     "btn-artifact-cancel-download",
			Text:    "取消",
			Variant: ButtonSecondary,
			OnClick: cancelOpenArtifactNodeDownload,
		})
		return
	}

	if !loaded {
		ui.Text(c, "检测中…").
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
		return
	}

	if status.OK {
		verText := "可用"
		if ver := strings.TrimPrefix(strings.TrimSpace(status.Version), "v"); ver != "" {
			verText = "v" + ver
		}
		ui.Text(c, verText).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
		if status.Path != "" {
			ui.Text(c, status.Path).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine().
				Shrink(1).
				MinWidth(0).
				Tooltip(status.Path)
		}
		return
	}

	statusText := fmt.Sprintf("未找到（需要 >= %s）", noderuntime.MinVersion)
	if ver := strings.TrimPrefix(strings.TrimSpace(status.Version), "v"); ver != "" {
		statusText = fmt.Sprintf("v%s（需要 >= %s）", ver, noderuntime.MinVersion)
	}
	ui.Text(c, statusText).
		FontSize(uiFont(TypeFootnote)).
		TextColor(ThemeSecondaryText(c)).
		SingleLine()
	if downloadErr != "" {
		ui.Text(c, downloadErr).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			SingleLine().
			Shrink(1).
			MinWidth(0).
			Tooltip(downloadErr)
	}
	Button(c, ButtonOpts{
		Key:     "btn-artifact-download-node",
		Text:    "下载 Node",
		Variant: ButtonSecondary,
		OnClick: func() { startOpenArtifactNodeDownload(st) },
	})
}

type openArtifactHomeControl struct{}

func (openArtifactHomeControl) render(c *ui.Context, st *SettingsState, s setting) {
	ensureOpenArtifactLoaded(st)

	openArtifactUI.mu.Lock()
	ready := openArtifactUI.bridgeReady
	opening := openArtifactUI.openingHome
	homeErr := openArtifactUI.homeErr
	openArtifactUI.mu.Unlock()

	if homeErr != "" {
		ui.Text(c, homeErr).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			SingleLine().
			Shrink(1).
			MinWidth(0).
			Tooltip(homeErr)
	}

	label := "打开主页"
	if opening {
		label = "打开中…"
	}
	Button(c, ButtonOpts{
		Key:      "btn-artifact-open-home",
		Text:     label,
		Variant:  ButtonSecondary,
		Disabled: !ready || opening,
		Loading:  opening,
		OnClick:  func() { openOpenArtifactHome(st) },
	})
}
