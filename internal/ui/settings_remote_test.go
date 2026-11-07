package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/skip2/go-qrcode"
	"mcode/internal/config"
)

// TestSettingsRemote_PureFunctions 覆盖纯函数：formatCountdown（0、-1s、299s）、formatShortCode、qrBitmap 尺寸与非空。
func TestSettingsRemote_PureFunctions(t *testing.T) {
	// formatCountdown 覆盖 0、-1s、299s
	if got := formatCountdown(0); got != "已过期" {
		t.Fatalf("formatCountdown(0) = %q, want %q", got, "已过期")
	}
	if got := formatCountdown(-1 * time.Second); got != "已过期" {
		t.Fatalf("formatCountdown(-1s) = %q, want %q", got, "已过期")
	}
	if got := formatCountdown(299 * time.Second); got != "4:59" {
		t.Fatalf("formatCountdown(299s) = %q, want %q", got, "4:59")
	}

	// formatShortCode 覆盖 6 位与非 6 位
	if got := formatShortCode("123456"); got != "123 456" {
		t.Fatalf("formatShortCode(\"123456\") = %q, want %q", got, "123 456")
	}
	if got := formatShortCode("123"); got != "123" {
		t.Fatalf("formatShortCode(\"123\") = %q, want %q", got, "123")
	}

	// qrBitmap 返回非 nil，且尺寸等于模块数×scale
	const scale = 2
	bm, err := qrBitmap("http://192.168.1.5:43117/#pair=abc", scale)
	if err != nil {
		t.Fatalf("qrBitmap failed: %v", err)
	}
	if bm == nil {
		t.Fatal("qrBitmap returned nil")
	}

	q, err := qrcode.New("http://192.168.1.5:43117/#pair=abc", qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New failed: %v", err)
	}
	q.DisableBorder = false
	expectedW := len(q.Bitmap()[0]) * scale
	expectedH := len(q.Bitmap()) * scale
	w, h := bm.Size()
	if w != expectedW || h != expectedH {
		t.Fatalf("qrBitmap size = (%d, %d), want (%d, %d)", w, h, expectedW, expectedH)
	}
}

// TestSettingsRemote_ToggleEnabled 覆盖：状态为 stopped 时页面显示“已停止”，切换开关后 fake 的 setRemoteEnabledCalls 为 [true]，再次从 dataDir 加载 config 时 Remote.Enabled 为 true，页面显示“运行中”。
func TestSettingsRemote_ToggleEnabled(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State: RemoteStateStopped,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("已停止")
	})

	if err := tt.Click("切换启用远程控制服务"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.setRemoteEnabledCalls) == 1 && fake.setRemoteEnabledCalls[0] == true
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("运行中")
	})

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if !cfg.Remote.Enabled {
		t.Fatalf("expected Remote.Enabled = true, got false")
	}
	if err := tt.Click("切换启用远程控制服务"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已停止") })
	cfg, err = config.Load(dataDir)
	if err != nil || cfg.Remote.Enabled {
		t.Fatalf("关闭服务的配置未持久化：enabled=%v err=%v", cfg.Remote.Enabled, err)
	}
}

// TestSettingsRemote_LANURLs 覆盖：状态为 running、LANURLs=["http://192.168.1.5:43117"] 时页面显示该 URL。
func TestSettingsRemote_LANURLs(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:      RemoteStateRunning,
		Port:       43117,
		LANURLs:    []string{"http://192.168.1.5:43117"},
		ListenAddr: "0.0.0.0:43117",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Enabled = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("http://192.168.1.5:43117")
	})
}

// TestSettingsRemote_Pairing 覆盖：running 时点击“配对新设备”，首次 remotePairing 调用为 refresh=false，页面显示 "123 456"；
// 点击刷新并确认后先调用 RevokeAllRemoteDevices，再以 refresh=true 换发；active 状态下的轮询会产生额外的 false 调用。
func TestSettingsRemote_Pairing(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:      RemoteStateRunning,
		Port:       43117,
		ListenAddr: "0.0.0.0:43117",
	}
	fake.remotePairing = RemotePairing{
		PairingURL:    "http://127.0.0.1:43117/#pair=abc",
		LANPairingURL: "http://192.168.1.5:43117/#pair=abc",
		ShortCode:     "123456",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Enabled = true
	_ = config.Save(dataDir, cfg)

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		// active 状态下页面每 2 秒轮询一次（refresh=false），调用次数会持续增长。
		return len(fake.remotePairingCalls) >= 1 && fake.remotePairingCalls[0] == false
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("123 456")
	})

	waitFor(t, tt, func() bool { return tt.HasText("刷新配对码") })
	clickRemoteSettingsLabel(t, tt, "刷新配对码")

	waitFor(t, tt, func() bool {
		return tt.HasText("刷新将撤销所有已配对设备")
	})
	clickRemoteSettingsLabel(t, tt, "撤销所有设备")

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.revokeAllDevicesCalls != 1 || len(fake.remotePairingCalls) == 0 {
			return false
		}
		// 轮询会在切换码前后插入额外的 false 调用，只要求首次为 false 且之后出现过 refresh=true。
		if fake.remotePairingCalls[0] != false {
			return false
		}
		for _, refresh := range fake.remotePairingCalls[1:] {
			if refresh {
				return true
			}
		}
		return false
	})
}

// TestSettingsRemote_SetRemoteEnabledError 覆盖：remoteErr 非 nil 时 SetRemoteEnabled 失败，页面显示错误文本。
func TestSettingsRemote_SetRemoteEnabledError(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State: RemoteStateStopped,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { return tt.HasText("已停止") })

	const errMsg = "cannot bind port 43117: address already in use"
	fake.mu.Lock()
	fake.remoteErr = errors.New(errMsg)
	fake.remoteStatus = RemoteStatus{State: RemoteStateError, Error: errMsg}
	fake.mu.Unlock()

	if err := tt.Click("切换启用远程控制服务"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("服务启动失败") && tt.HasText(errMsg) && tt.HasText("重试")
	})

	// 清除错误后点击“重试”，走 RestartRemote 并恢复运行状态。
	fake.mu.Lock()
	fake.remoteErr = nil
	fake.mu.Unlock()
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.restartRemoteCalls == 1
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("运行中")
	})
}

func TestSettingsRemote_SaveFailureDoesNotStartService(t *testing.T) {
	dataDir := settingsTestDir(t)
	// 让配置目标成为目录，可靠模拟写入失败，不依赖执行用户权限。
	if err := os.Mkdir(filepath.Join(dataDir, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeClient()
	st := &SettingsState{
		dataDir: dataDir,
		cfg:     config.Defaults(),
		chat:    &chatState{client: fake},
	}
	setRemoteEnabled(st, true)
	st.mu.Lock()
	enabled, errMsg, busy := st.cfg.Remote.Enabled, st.remote.err, st.remote.busy
	st.mu.Unlock()
	if enabled || errMsg == "" || busy {
		t.Fatalf("保存失败后状态不正确：enabled=%v err=%q busy=%v", enabled, errMsg, busy)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.setRemoteEnabledCalls) != 0 {
		t.Fatal("配置保存失败后仍然启动了服务")
	}
}

// TestSettingsRemote_DevicesEmpty 覆盖：设备列表为空时显示空态提示，且在服务未启动时也显示。
func TestSettingsRemote_DevicesEmpty(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State: RemoteStateStopped,
	}
	fake.remoteDevices = nil
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("设备") && tt.HasText("暂无已授权设备")
	})
}

func TestSettingsRemote_DevicesStackVertically(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteDevices = []RemoteDevice{
		{ID: "dev-1", Name: "iPhone 15"},
		{ID: "dev-2", Name: "Pixel 8"},
	}
	fake.mu.Unlock()

	tt := ui.NewTester(Root(fake, WithSettings(settingsTestDir(t), nil)), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("iPhone 15") && tt.HasText("Pixel 8") })

	first, ok := tt.Find("iPhone 15")
	if !ok {
		t.Fatal("找不到第一台设备")
	}
	second, ok := tt.Find("Pixel 8")
	if !ok {
		t.Fatal("找不到第二台设备")
	}
	if second.Y <= first.Y || second.X != first.X {
		t.Fatalf("设备未纵向对齐：第一台 %+v，第二台 %+v", first, second)
	}
}

// TestSettingsRemote_RevokeDevice 覆盖：显示已授权设备、二次确认撤销、调用 RevokeRemoteDevice 以及列表刷新。
func TestSettingsRemote_RevokeDevice(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State: RemoteStateStopped,
	}
	fake.remoteDevices = []RemoteDevice{
		{
			ID:         "dev-1",
			Name:       "iPhone 15",
			Platform:   "iOS",
			LastSeenAt: time.Now().Add(-2 * time.Minute),
			PairedAt:   time.Now().Add(-24 * time.Hour),
		},
		{
			ID:         "dev-2",
			Name:       "Pixel 8",
			Platform:   "Android",
			LastSeenAt: time.Now().Add(-10 * time.Minute),
			PairedAt:   time.Now().Add(-48 * time.Hour),
		},
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("iPhone 15") && tt.HasText("Pixel 8")
	})

	// 第一次点击进入二次确认态
	clickRemoteSettingsLabel(t, tt, "btn-revoke-dev-1")
	waitFor(t, tt, func() bool {
		return tt.HasText("确认撤销")
	})

	// 二次确认撤销
	clickRemoteSettingsLabel(t, tt, "btn-revoke-dev-1")

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.revokeDeviceCalls) == 1 && fake.revokeDeviceCalls[0] == "dev-1"
	})

	// 列表刷新后 iPhone 15 消失，Pixel 8 保留
	waitFor(t, tt, func() bool {
		return !tt.HasText("iPhone 15") && tt.HasText("Pixel 8")
	})
}

// TestSettingsRemote_DevicesError 覆盖：设备列表拉取失败时显示说明文字。
func TestSettingsRemote_DevicesError(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State: RemoteStateStopped,
	}
	fake.remoteDevicesErr = errors.New("read devices error")
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("无法获取已授权设备列表：read devices error")
	})
}

// clickRemoteSettingsLabel 滚动设置详情区直到目标元素进入可视区域，再点击其中心。
// 远程控制页较长，配对与设备操作位于首屏之外，直接按裁剪后的坐标点击会落在窗口外。
func clickRemoteSettingsLabel(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := settingsClick(t, tt, label); err != nil {
		t.Fatal(err)
	}
}

// openRemoteSettingsPage 打开设置面板并进入远程控制页。
func openRemoteSettingsPage(t *testing.T, fake *FakeClient, dataDir string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}
	return tt
}

// openRemoteSettingsPageState 与 openRemoteSettingsPage 相同，区别是额外返回 SettingsState，
// 便于测试展开页底高级区（访问域名、HTTPS 端口、Cloudflare Token）。
func openRemoteSettingsPageState(t *testing.T, fake *FakeClient, dataDir string) (*ui.Tester, *SettingsState) {
	t.Helper()
	var st *SettingsState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		st = getChatSettings(s)
	}), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("远程控制") })
	if err := tt.Click("远程控制"); err != nil {
		t.Fatal(err)
	}
	st.setSettingsAdvancedOpen("remote", true)
	return tt, st
}

func waitRemoteCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("remote condition not met within deadline")
}

// TestSettingsRemote_RunningStatus 覆盖运行状态文案与“重启服务”按钮调用 RestartRemote。
func TestSettingsRemote_RunningStatus(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:            RemoteStateRunning,
		Port:             43117,
		ConnectedClients: 2,
		StartedListen:    "0.0.0.0:43117",
		LANURLs:          []string{"http://192.168.1.5:43117"},
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool {
		return tt.HasText("运行中 · 端口 43117 · 已连接 2 台设备")
	})

	// 重启按钮跟着监听端口行搬进了高级区，需要滚动到可视区域再点。
	clickRemoteSettingsLabel(t, tt, "重启服务")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.restartRemoteCalls == 1
	})
}

// TestSettingsRemote_PairingStates 覆盖 active、expired、used、unavailable 四种配对视图。
func TestSettingsRemote_PairingStates(t *testing.T) {
	cases := []struct {
		name      string
		state     string
		expiresAt time.Time
		want      []string
	}{
		{
			name:      "active",
			state:     "active",
			expiresAt: time.Now().Add(5 * time.Minute),
			want:      []string{"123 456", "复制配对码", "复制配对链接", "复制局域网链接", "刷新配对码"},
		},
		{
			name:      "expired",
			state:     "expired",
			expiresAt: time.Now().Add(-time.Minute),
			want:      []string{"配对二维码已过期", "配对新设备"},
		},
		{
			name:  "used",
			state: "used",
			want:  []string{"设备配对成功", "配对新设备"},
		},
		{
			name:  "unavailable",
			state: "unavailable",
			want:  []string{"设备已完成配对", "配对新设备"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.mu.Lock()
			fake.remoteStatus = RemoteStatus{
				State:         RemoteStateRunning,
				Port:          43117,
				LANURLs:       []string{"http://192.168.1.5:43117"},
				StartedListen: "0.0.0.0:43117",
			}
			fake.remotePairing = RemotePairing{
				State:         tc.state,
				PairingURL:    "http://127.0.0.1:43117/#pair=abc",
				LANPairingURL: "http://192.168.1.5:43117/#pair=abc",
				ShortCode:     "123456",
				ExpiresAt:     tc.expiresAt,
			}
			fake.mu.Unlock()

			tt := openRemoteSettingsPage(t, fake, t.TempDir())
			waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
			clickRemoteSettingsLabel(t, tt, "配对新设备")
			waitFor(t, tt, func() bool {
				for _, want := range tc.want {
					if !tt.HasText(want) {
						return false
					}
				}
				return true
			})
		})
	}
}

// TestSettingsRemote_DevicesRichInfo 覆盖在线圆点、User-Agent 概括、配对时间与最后访问信息。
func TestSettingsRemote_DevicesRichInfo(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteDevices = []RemoteDevice{
		{
			ID:          "dev-1",
			Name:        "iPhone 15",
			Online:      true,
			PairedAt:    time.Now().Add(-24 * time.Hour),
			LastSeenAt:  time.Now().Add(-2 * time.Minute),
			LastAddress: "192.168.1.5",
			LastEntry:   "tls",
			UserAgent:   "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1",
		},
	}
	fake.mu.Unlock()

	tt := openRemoteSettingsPage(t, fake, t.TempDir())
	waitFor(t, tt, func() bool {
		return tt.HasText("iPhone · iOS 18.7 · Safari") &&
			tt.HasText("配对于 ") &&
			tt.HasText("最后活跃 ") &&
			tt.HasText("最后访问：192.168.1.5 · 域名 HTTPS") &&
			tt.HasText("重命名") &&
			tt.HasText("撤销")
	})
}

// TestSettingsRemote_RenameDevice 覆盖调用 RenameRemoteDevice 并刷新设备名称。
func TestSettingsRemote_RenameDevice(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteDevices = []RemoteDevice{{ID: "dev-1", Name: "旧名称"}}
	fake.mu.Unlock()

	st := &SettingsState{
		cfg:  config.Defaults(),
		chat: &chatState{client: fake},
	}
	renameRemoteDevice(st, "dev-1", "新名称")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.renameDeviceCalls) == 1 &&
			fake.renameDeviceCalls[0] == [2]string{"dev-1", "新名称"}
	})
	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.renameDeviceID == "" && st.remote.renameDraft == ""
	})
}

// TestParseHostsNormalizes 覆盖小写化与非法主机（冒号、空白）过滤。
func TestParseHostsNormalizes(t *testing.T) {
	got := parseHosts("Example.COM, bad host, foo:123, LAN.local\nMiXeD.lan")
	want := []string{"example.com", "lan.local", "mixed.lan"}
	if len(got) != len(want) {
		t.Fatalf("parseHosts = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseHosts[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSummarizeRemoteUserAgent 覆盖常见设备的 User-Agent 概括。
func TestSummarizeRemoteUserAgent(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1",
			want: "iPhone · iOS 18.7 · Safari",
		},
		{
			ua:   "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36",
			want: "Android 14 · Chrome",
		},
		{
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15",
			want: "Mac · macOS 10.15.7 · Safari",
		},
		{ua: "", want: ""},
	}
	for _, tc := range cases {
		if got := summarizeRemoteUserAgent(tc.ua); got != tc.want {
			t.Fatalf("summarizeRemoteUserAgent(%q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

// TestRemoteAddressAndListenHelpers 覆盖监听地址解析与局域网地址变化判断。
func TestRemoteAddressAndListenHelpers(t *testing.T) {
	if got := remoteListenWithPort("0.0.0.0:43117", 5000); got != "0.0.0.0:5000" {
		t.Fatalf("remoteListenWithPort = %q", got)
	}
	if got := remoteTLSListenWithPort("", 9443); got != "0.0.0.0:9443" {
		t.Fatalf("remoteTLSListenWithPort = %q", got)
	}
	if got := remoteListenPort("127.0.0.1:6000"); got != 6000 {
		t.Fatalf("remoteListenPort = %d", got)
	}
	if !remoteAddressChanged(RemoteStatus{State: RemoteStateRunning, StartedListen: "192.168.1.5:43117"}, "192.168.1.6") {
		t.Fatal("地址变化未被识别")
	}
	if remoteAddressChanged(RemoteStatus{State: RemoteStateRunning, StartedListen: "192.168.1.5:43117"}, "192.168.1.5") {
		t.Fatal("相同地址被误判为变化")
	}
	if remoteAddressChanged(RemoteStatus{State: RemoteStateStopped, StartedListen: "192.168.1.5:43117"}, "192.168.1.6") {
		t.Fatal("服务未运行时不应提示地址变化")
	}
}

// TestRemoteTLSStatusHelpers 覆盖证书域名匹配与过期/不匹配徽标。
func TestRemoteTLSStatusHelpers(t *testing.T) {
	if !remoteCertCoversDomain([]string{"*.example.com"}, "a.example.com") {
		t.Fatal("单层通配符应匹配子域名")
	}
	if remoteCertCoversDomain([]string{"*.example.com"}, "b.a.example.com") {
		t.Fatal("通配符不应匹配多级子域名")
	}
	if remoteCertCoversDomain([]string{"example.com"}, "a.example.com") {
		t.Fatal("普通域名不应匹配子域名")
	}
	if got := remoteTLSStatusBadge(RemoteTLSStatus{NotAfter: time.Now().Add(-time.Hour)}, "", time.Now()); got != "证书已过期" {
		t.Fatalf("过期徽标 = %q", got)
	}
	if got := remoteTLSStatusBadge(RemoteTLSStatus{CertDomains: []string{"other.example.com"}}, "mcode.example.com", time.Now()); got != "证书与域名不匹配" {
		t.Fatalf("域名不匹配徽标 = %q", got)
	}
}

// TestSettingsRemote_TLSSection 覆盖证书状态、操作按钮与 Token 已保存状态。
func TestSettingsRemote_TLSSection(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTLSStatus = RemoteTLSStatus{
		Configured:         true,
		Domain:             "mcode.example.com",
		TLSPort:            8443,
		StartedURL:         "https://mcode.example.com:8443",
		CertDomains:        []string{"mcode.example.com"},
		Issuer:             "Let's Encrypt",
		NotAfter:           time.Now().Add(60 * 24 * time.Hour),
		Source:             "acme",
		HasCloudflareToken: true,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool {
		return tt.HasText("自定义域名 HTTPS") &&
			tt.HasText("mcode.example.com") &&
			tt.HasText("Let's Encrypt") &&
			tt.HasText("自动申请") &&
			tt.HasText("已保存") &&
			tt.HasText("申请证书") &&
			tt.HasText("导入证书…") &&
			tt.HasText("移除证书")
	})
}

// TestSettingsRemote_TLSActions 直接覆盖 Token 保存/清除、导入与移除证书调用。
func TestSettingsRemote_TLSActions(t *testing.T) {
	fake := NewFakeClient()
	st := &SettingsState{
		cfg:  config.Defaults(),
		chat: &chatState{client: fake},
	}

	saveRemoteCloudflareToken(st, "token-1")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		called := len(fake.saveRemoteTokenCalls) == 1 && fake.saveRemoteTokenCalls[0] == "token-1"
		fake.mu.Unlock()
		st.mu.Lock()
		done := !st.remote.tlsBusy && st.remote.tlsHasToken
		st.mu.Unlock()
		return called && done
	})

	saveRemoteCloudflareToken(st, "")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		called := len(fake.saveRemoteTokenCalls) == 2 && fake.saveRemoteTokenCalls[1] == ""
		fake.mu.Unlock()
		st.mu.Lock()
		done := !st.remote.tlsBusy && !st.remote.tlsHasToken
		st.mu.Unlock()
		return called && done
	})

	oldPicker := pickRemoteTLSFileDialog
	pickRemoteTLSFileDialog = func(title, message string) (string, bool, error) {
		switch title {
		case "选择证书链文件":
			return "/tmp/fullchain.pem", true, nil
		default:
			return "/tmp/privkey.pem", true, nil
		}
	}
	t.Cleanup(func() { pickRemoteTLSFileDialog = oldPicker })

	importRemoteTLSCertificate(st)
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		called := len(fake.importRemoteTLSCalls) == 1 &&
			fake.importRemoteTLSCalls[0] == [2]string{"/tmp/fullchain.pem", "/tmp/privkey.pem"}
		fake.mu.Unlock()
		st.mu.Lock()
		done := !st.remote.tlsBusy
		st.mu.Unlock()
		return called && done
	})

	removeRemoteTLSCertificate(st)
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		called := fake.removeRemoteTLSCalls == 1
		fake.mu.Unlock()
		st.mu.Lock()
		done := !st.remote.tlsBusy
		st.mu.Unlock()
		return called && done
	})
}

// TestRemoteExposureNotice 覆盖暴露面提示：默认 0.0.0.0 监听必须点明全网卡可达与明文传输。
func TestRemoteExposureNotice(t *testing.T) {
	open := remoteExposureNotice(config.DefaultRemoteListen)
	for _, want := range []string{"所有网卡", "明文 HTTP"} {
		if !strings.Contains(open, want) {
			t.Fatalf("0.0.0.0 监听提示缺少 %q：%s", want, open)
		}
	}
	local := remoteExposureNotice("127.0.0.1:43117")
	if strings.Contains(local, "所有网卡") {
		t.Fatalf("回环监听不应提示全网卡：%s", local)
	}
	if !strings.Contains(local, "明文 HTTP") {
		t.Fatalf("回环监听也要提示明文传输：%s", local)
	}
}

// TestRemoteTLSCertInstalled 覆盖「已安装证书」不再等同于「TLSListen 非空」。
func TestRemoteTLSCertInstalled(t *testing.T) {
	listenOnly := RemoteTLSStatus{Configured: true, TLSPort: 8443}
	if remoteTLSCertInstalled(listenOnly) {
		t.Fatal("只启用监听的配置不应算已安装证书")
	}
	if got := remoteTLSStatusDetail(listenOnly); got != "尚未安装证书" {
		t.Fatalf("只启用监听时的证书状态 = %q", got)
	}

	cert := RemoteTLSStatus{
		Configured:  true,
		CertDomains: []string{"mcode.example.com"},
		NotAfter:    time.Now().Add(24 * time.Hour),
	}
	if !remoteTLSCertInstalled(cert) {
		t.Fatal("带证书信息的配置应算已安装")
	}
	if got := remoteTLSStatusDetail(cert); !strings.Contains(got, "mcode.example.com") {
		t.Fatalf("证书状态应包含域名：%s", got)
	}

	// 关掉 HTTPS 监听后证书仍在：依然算已安装，移除按钮不应因此禁用。
	stopped := RemoteTLSStatus{CertDomains: []string{"mcode.example.com"}}
	if !remoteTLSCertInstalled(stopped) {
		t.Fatal("关闭监听后证书仍然存在")
	}
}

// TestSettingsRemote_TLSEntryNeedsStartedURL 覆盖：TLS 没真正启动时 HTTPS 入口只标注未生效且不给复制按钮。
func TestSettingsRemote_TLSEntryNeedsStartedURL(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTLSStatus = RemoteTLSStatus{Domain: "mcode.example.com", TLSPort: 8443}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt := openRemoteSettingsPage(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("未生效：申请或导入证书后可用") })
	if tt.HasText("复制 HTTPS 入口") {
		t.Fatal("未生效的 HTTPS 入口不应提供复制按钮")
	}
}

// TestSettingsRemote_TLSRemoveNeedsConfirm 覆盖：移除证书必须先二次确认，确认前不调用后端。
func TestSettingsRemote_TLSRemoveNeedsConfirm(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTLSStatus = RemoteTLSStatus{
		Configured:  true,
		Domain:      "mcode.example.com",
		TLSPort:     8443,
		StartedURL:  "https://mcode.example.com:8443",
		CertDomains: []string{"mcode.example.com"},
		Issuer:      "Let's Encrypt",
		NotAfter:    time.Now().Add(24 * time.Hour),
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt := openRemoteSettingsPage(t, fake, dataDir)
	// 等证书状态回填后再点：按钮只在证书真的装好时才可点。
	waitFor(t, tt, func() bool { return tt.HasText("Let's Encrypt") })
	clickRemoteSettingsLabel(t, tt, "移除证书")
	waitFor(t, tt, func() bool { return tt.HasText("移除证书？") })

	fake.mu.Lock()
	calls := fake.removeRemoteTLSCalls
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("打开确认框就调用了移除证书：%d", calls)
	}

	if err := tt.Click("确认移除证书"); err != nil {
		t.Fatalf("click '确认移除证书' failed: %v", err)
	}
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.removeRemoteTLSCalls == 1
	})
}

// TestSettingsRemote_PollPairingRotatesQR 覆盖：状态仍是 active 但配对码换发时，轮询要重新生成二维码。
func TestSettingsRemote_PollPairingRotatesQR(t *testing.T) {
	fake := NewFakeClient()
	st := &SettingsState{cfg: config.Defaults(), chat: &chatState{client: fake}}

	old := RemotePairing{
		LANPairingURL: "http://192.168.1.5:43117/#pair=old",
		ShortCode:     "111111",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
	}
	st.remote.pairing = old
	st.remote.pairingOpen = true
	st.remote.qrFor = remotePairingTargetURL(old, "")

	fake.mu.Lock()
	fake.remotePairing = RemotePairing{
		LANPairingURL: "http://192.168.1.5:43117/#pair=new",
		ShortCode:     "222222",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
	}
	fake.mu.Unlock()

	pollRemotePairing(st)
	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.qrFor == "http://192.168.1.5:43117/#pair=new" && st.remote.qr != nil
	})
}

// TestSettingsRemote_TLSEnableSwitch 覆盖：HTTPS 入口有显式开关，关闭后清空 TLSListen，重新打开才写回监听地址。
func TestSettingsRemote_TLSEnableSwitch(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt := openRemoteSettingsPage(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("启用 HTTPS") })

	clickRemoteSettingsLabel(t, tt, "切换启用 HTTPS")
	waitFor(t, tt, func() bool {
		loaded, err := config.Load(dataDir)
		return err == nil && loaded.Remote.TLSListen == ""
	})

	// 重新打开：按输入框里的端口写回监听地址。
	clickRemoteSettingsLabel(t, tt, "切换启用 HTTPS")
	waitFor(t, tt, func() bool {
		loaded, err := config.Load(dataDir)
		return err == nil && loaded.Remote.TLSListen == "0.0.0.0:8443"
	})
}

// TestSettingsRemote_SectionOrderAndUniqueTitles 校验远程控制页的区块顺序与标题唯一性：
// 服务状态、设备、网络、Tailscale、自定义域名 HTTPS、手机推送。
func TestSettingsRemote_SectionOrderAndUniqueTitles(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:            RemoteStateRunning,
		Port:             43117,
		ConnectedClients: 1,
		StartedListen:    "0.0.0.0:43117",
		LANURLs:          []string{"http://192.168.1.5:43117"},
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)

	wantOrder := []string{
		"服务状态",
		"设备",
		"网络",
		"Tailscale",
		"自定义域名 HTTPS",
		"手机推送",
	}
	waitFor(t, tt, func() bool {
		texts := tt.Texts()
		last := -1
		for _, want := range wantOrder {
			pos := -1
			for i, text := range texts {
				if text == want {
					pos = i
					break
				}
			}
			if pos <= last {
				return false
			}
			last = pos
		}
		return true
	})

	texts := tt.Texts()
	for _, want := range wantOrder {
		count := 0
		for _, text := range texts {
			if text == want {
				count++
			}
		}
		if count != 1 {
			t.Errorf("区块标题 %q 出现 %d 次，应恰好 1 次：%v", want, count, texts)
		}
	}
	for _, gone := range []string{"连接设备", "局域网访问", "Tailscale 节点", "网络端口与域名", "访问入口", "域名与证书", "安全说明", "证书签发"} {
		for _, text := range texts {
			if text == gone {
				t.Errorf("旧的区块标题 %q 仍出现在页面上", gone)
			}
		}
	}
}

// TestSettingsRemote_StoppedHidesRuntimeSections 校验服务停止时常驻卡片保留，运行态行和区块隐藏。
func TestSettingsRemote_StoppedHidesRuntimeSections(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteDevices = nil
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}

	tt := openRemoteSettingsPage(t, fake, dataDir)

	waitFor(t, tt, func() bool {
		return tt.HasText("服务状态") && tt.HasText("设备") && tt.HasText("网络") && tt.HasText("Tailscale") && tt.HasText("监听端口") && tt.HasText("额外允许的访问域名") && tt.HasText("节点名称") && tt.HasText("重置节点")
	})
	if tt.HasText("安全说明") {
		t.Error("安全说明是只读说明文字，已从远程页删除")
	}
	// 停服仍保留已授权设备列表（空态提示）。
	waitFor(t, tt, func() bool { return tt.HasText("暂无已授权设备") })
	for _, gone := range []string{"入口地址", "通过 Tailscale 访问", "自定义域名 HTTPS", "手机推送"} {
		if tt.HasText(gone) {
			t.Errorf("服务停止时不应显示运行态内容 %q", gone)
		}
	}
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "http://") {
			t.Errorf("服务停止时不应出现可访问的入口地址：%q", text)
		}
	}
}

// TestSettingsRemote_PairingTargetSwitch 覆盖：配对卡可切换二维码入口，默认局域网，切换后复制链接用所选入口。
func TestSettingsRemote_PairingTargetSwitch(t *testing.T) {
	const (
		lanURL = "http://192.168.1.5:43117/#pair=tok"
		tlsURL = "https://mcode.example.com/#pair=tok"
		tsURL  = "https://mcode.tail1234.ts.net/#pair=tok"
	)

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:   RemoteStateRunning,
		Port:    43117,
		LANURLs: []string{"http://192.168.1.5:43117"},
	}
	fake.remotePairing = RemotePairing{
		PairingURL:          tlsURL,
		LANPairingURL:       lanURL,
		TLSPairingURL:       tlsURL,
		TailscalePairingURL: tsURL,
		ShortCode:           "123456",
		ExpiresAt:           time.Now().Add(5 * time.Minute),
		State:               "active",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt, st := openRemoteSettingsPageState(t, fake, dataDir)

	// 配对卡默认收起，先点「配对新设备」把 fake 的配对信息渲染出来。
	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")

	// 默认入口是局域网，二维码也应编码局域网链接。
	waitFor(t, tt, func() bool { return tt.HasText("二维码入口") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.pairingTarget == "" || st.remote.pairingTarget == "lan"
	})
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.qrFor == lanURL
	})

	clickRemoteSettingsLabel(t, tt, "复制配对链接")
	waitRemoteCondition(t, func() bool { return tt.Clipboard() == lanURL })

	// 切到 Tailscale：只换二维码与复制目标，不重新签发配对码。
	fake.mu.Lock()
	pairingCallsBefore := len(fake.remotePairingCalls)
	fake.mu.Unlock()

	// 入口按钮的可见文字与 Label 都是 opt.Label，按文字「Tailscale」定位。
	clickRemoteSettingsLabel(t, tt, "Tailscale")
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.pairingTarget == "tailscale" && st.remote.qrFor == tsURL
	})

	clickRemoteSettingsLabel(t, tt, "复制配对链接")
	waitRemoteCondition(t, func() bool { return tt.Clipboard() == tsURL })

	// 切换过程中允许一次轮询，但不应出现 refresh=true 的重新签发。
	fake.mu.Lock()
	refreshed := false
	for _, refresh := range fake.remotePairingCalls[pairingCallsBefore:] {
		if refresh {
			refreshed = true
		}
	}
	fake.mu.Unlock()
	if refreshed {
		t.Errorf("切换二维码入口不应重新签发配对码")
	}
}

// TestSettingsRemote_PairingTargetTailscaleDisabled 覆盖：Tailscale 链接为空时该入口按钮禁用，点击不改变选择。
func TestSettingsRemote_PairingTargetTailscaleDisabled(t *testing.T) {
	const (
		lanURL = "http://192.168.1.5:43117/#pair=tok"
		tlsURL = "https://mcode.example.com/#pair=tok"
	)

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:   RemoteStateRunning,
		Port:    43117,
		LANURLs: []string{"http://192.168.1.5:43117"},
	}
	fake.remotePairing = RemotePairing{
		PairingURL:    tlsURL,
		LANPairingURL: lanURL,
		TLSPairingURL: tlsURL,
		ShortCode:     "123456",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		State:         "active",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt, st := openRemoteSettingsPageState(t, fake, dataDir)

	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")
	waitFor(t, tt, func() bool { return tt.HasText("二维码入口") })

	// 禁用按钮仍然渲染，但点击后选择与二维码都不变。
	clickRemoteSettingsLabel(t, tt, "Tailscale")
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.pairingTarget != "tailscale" && st.remote.qrFor == lanURL
	})
}

// TestSettingsRemote_PairingTargetHiddenWithSingleLink 覆盖：可用入口不足两个时整行隐藏。
func TestSettingsRemote_PairingTargetHiddenWithSingleLink(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:   RemoteStateRunning,
		Port:    43117,
		LANURLs: []string{"http://192.168.1.5:43117"},
	}
	fake.remotePairing = RemotePairing{
		LANPairingURL: "http://192.168.1.5:43117/#pair=tok",
		ShortCode:     "123456",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		State:         "active",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)

	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")
	waitFor(t, tt, func() bool { return tt.HasText("复制配对链接") })
	if tt.HasText("二维码入口") {
		t.Errorf("只有一个可用入口时不应显示二维码入口切换行")
	}
}

// TestSettingsRemote_PairingEffectiveTarget 覆盖：二维码入口高亮落在真正生效的入口上。
// 所选入口还没就绪时二维码会回退到别的入口，高亮必须跟着走，否则会出现“禁用却高亮”的按钮。
func TestSettingsRemote_PairingEffectiveTarget(t *testing.T) {
	const (
		lanURL = "http://192.168.1.5:43117/#pair=tok"
		tlsURL = "https://mcode.example.com/#pair=tok"
		tsURL  = "https://mcode.tail1234.ts.net/#pair=tok"
	)

	full := RemotePairing{PairingURL: lanURL, LANPairingURL: lanURL, TLSPairingURL: tlsURL, TailscalePairingURL: tsURL}
	lanOnly := RemotePairing{PairingURL: lanURL, LANPairingURL: lanURL}
	tsOnly := RemotePairing{PairingURL: tsURL, TailscalePairingURL: tsURL}

	cases := []struct {
		name    string
		pairing RemotePairing
		target  string
		want    string
	}{
		{"所选入口就绪", full, "tls", "tls"},
		{"默认选区落到局域网", full, "", "lan"},
		{"所选入口未就绪时回退", lanOnly, "tailscale", "lan"},
		{"只剩尾部入口", tsOnly, "lan", "tailscale"},
	}
	for _, tc := range cases {
		got := remotePairingEffectiveTarget(tc.pairing, tc.target)
		if got != tc.want {
			t.Errorf("%s：生效入口 = %q，应为 %q", tc.name, got, tc.want)
		}
		// 高亮换了对象，二维码用的链接不能跟着变。
		wantURL := remotePairingTargetURL(tc.pairing, remotePairingTargetValue(tc.target))
		if gotURL := remotePairingTargetURL(tc.pairing, got); gotURL != wantURL {
			t.Errorf("%s：二维码链接 = %q，应仍为 %q", tc.name, gotURL, wantURL)
		}
	}
}

// TestSettingsPageRemote_Tree 覆盖页面树版本的远程页：主要区块标题可见、原高级区不再折叠、
// 安全说明已删除、开关直接改配置，错误状态只在出错时出现。
func TestSettingsPageRemote_Tree(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{
		State:            RemoteStateRunning,
		Port:             43117,
		ConnectedClients: 1,
		StartedListen:    "0.0.0.0:43117",
		LANURLs:          []string{"http://192.168.1.5:43117"},
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Enabled = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)

	for _, want := range []string{"服务状态", "设备", "网络", "Tailscale", "自定义域名 HTTPS", "手机推送"} {
		title := want
		waitFor(t, tt, func() bool { return tt.HasText(title) })
	}

	// 原高级区（端口、hosts、TLS 端口与令牌、Tailscale 节点名与重置）无需展开就能看到。
	for _, want := range []string{"监听端口", "额外允许的访问域名", "HTTPS 端口", "Cloudflare API 令牌", "节点名称", "重置节点"} {
		if _, ok := tt.Find(want); !ok {
			t.Errorf("页面树上应直接显示 %q：%v", want, tt.Texts())
		}
	}

	// 安全说明整节删除：只读说明文字不再出现。
	for _, gone := range []string{"安全说明", "安全与权限"} {
		if tt.HasText(gone) {
			t.Errorf("远程页不应再出现 %q", gone)
		}
	}

	// 运行中不出错误状态。
	if tt.HasText("服务启动失败") {
		t.Errorf("服务正常时不应出现启动失败提示：%v", tt.Texts())
	}

	// 开关直接写配置：关闭后配置与后端都要跟着走。
	clickRemoteSettingsLabel(t, tt, "切换启用远程控制服务")
	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.cfg.Remote.Enabled
	})
}

// TestSettingsPageRemote_TreeErrorState 覆盖页面树的错误状态行：服务出错时出现，含错误原文。
func TestSettingsPageRemote_TreeErrorState(t *testing.T) {
	const failure = "监听 0.0.0.0:43117 失败：address already in use"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateError, Error: failure, Port: 43117}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Enabled = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("服务启动失败") && tt.HasText(failure) })
}

// TestSettingsRemote_PortDraftDebounce 覆盖监听端口草稿：输入只改草稿，
// 合法端口立即落盘且保留原监听地址，越界输入不落盘也不被悄悄改回去。
func TestSettingsRemote_PortDraftDebounce(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117, StartedListen: "0.0.0.0:43117"}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Listen = "0.0.0.0:43117"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	tt.SetSize(1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("监听端口") })

	clickRemoteSettingsLabel(t, tt, "编辑监听端口")
	tt.Command("selectAll")
	tt.Type("50000")
	waitRemoteCondition(t, func() bool {
		loaded, err := config.Load(dataDir)
		return err == nil && loaded.Remote.Listen == "0.0.0.0:50000"
	})

	clickRemoteSettingsLabel(t, tt, "编辑监听端口")
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "50000" {
		t.Fatalf("端口草稿 = %q，想要 %q", got, "50000")
	}

	tt.Command("selectAll")
	tt.Type("80")
	tt.Frame()
	tt.Frame()
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Remote.Listen != "0.0.0.0:50000" {
		t.Fatalf("越界端口不应落盘：%q", loaded.Remote.Listen)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "80" {
		t.Fatalf("越界输入应留在草稿里等待修改，got %q", got)
	}
}

// TestSettingsRemote_TLSTokenMaskedDraft 覆盖 Cloudflare Token：输入按密码遮罩显示，
// 草稿保留原值，点保存把完整 Token 交给后端。
func TestSettingsRemote_TLSTokenMaskedDraft(t *testing.T) {
	const token = "cf-token-abc123"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	tt.SetSize(1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("Cloudflare API 令牌") })

	clickRemoteSettingsLabel(t, tt, "编辑Cloudflare API 令牌")
	tt.Command("selectAll")
	tt.Type(token)
	for _, text := range tt.Texts() {
		if strings.Contains(text, token) {
			t.Fatalf("令牌不应以明文出现在页面里：%q", text)
		}
	}

	clickRemoteSettingsLabel(t, tt, "保存 Token")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveRemoteTokenCalls) == 1 && fake.saveRemoteTokenCalls[0] == token
	})
}
