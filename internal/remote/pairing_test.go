package remote

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPairingAuth_FiveMinuteExpiration(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	parts := strings.Split(info.PairingURL, "#pair=")
	if len(parts) != 2 {
		t.Fatalf("invalid pairing URL format: %s", info.PairingURL)
	}
	code := parts[1]

	// 推进时间 5 分钟零 1 秒
	currTime = currTime.Add(5*time.Minute + time.Second)

	clientInfo := ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}
	_, err = auth.Pair(code, "My Phone", clientInfo)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken after 5 minutes, got %v", err)
	}
}

func TestPairingAuth_SingleUseAndTokenStorage(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	code := strings.Split(info.PairingURL, "#pair=")[1]

	clientInfo := ClientInfo{UserAgent: "Safari", Address: "192.168.1.50", Entry: EntryLAN}
	res, err := auth.Pair(code, "iPhone", clientInfo)
	if err != nil {
		t.Fatalf("first Pair failed: %v", err)
	}

	if len(res.SessionToken) != 64 {
		t.Fatalf("expected 64-char hex session token, got %s", res.SessionToken)
	}
	if res.DeviceID == "" || res.Label != "iPhone" {
		t.Fatalf("unexpected pair result: %+v", res)
	}

	// 单次使用：第二次使用相同 code 配对必须失败
	_, err = auth.Pair(code, "iPhone Again", clientInfo)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken on second use, got %v", err)
	}

	// 验证磁盘落盘文件：权限 0600，且不含明文 sessionToken
	fi, err := os.Stat(devicesPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0600 {
		t.Fatalf("expected 0600 permission, got %04o", perm)
	}

	content, err := os.ReadFile(devicesPath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if strings.Contains(string(content), res.SessionToken) {
		t.Fatalf("disk storage must NEVER contain plaintext sessionToken")
	}

	// 验证通过 sessionToken 正常认证
	devID, label, err := auth.Authenticate(res.SessionToken, clientInfo)
	if err != nil || devID != res.DeviceID || label != "iPhone" {
		t.Fatalf("Authenticate failed: devID=%s, label=%s, err=%v", devID, label, err)
	}

	// 伪造 token 认证失败
	_, _, err = auth.Authenticate("bad-token", clientInfo)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for bad token, got %v", err)
	}
}

func TestPairingAuth_RevocationTriggersOnRevoke(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	var revokedDeviceID string
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
		OnRevoke: func(deviceID string) {
			revokedDeviceID = deviceID
		},
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	code := strings.Split(info.PairingURL, "#pair=")[1]
	clientInfo := ClientInfo{Entry: EntryLAN}

	res, err := auth.Pair(code, "Device To Revoke", clientInfo)
	if err != nil {
		t.Fatalf("Pair failed: %v", err)
	}

	revoked, err := auth.RevokeDevice(res.DeviceID)
	if err != nil || !revoked {
		t.Fatalf("RevokeDevice failed: revoked=%v, err=%v", revoked, err)
	}

	if revokedDeviceID != res.DeviceID {
		t.Fatalf("expected OnRevoke callback with %s, got %s", res.DeviceID, revokedDeviceID)
	}

	// 撤销后 token 认证应失败
	_, _, err = auth.Authenticate(res.SessionToken, clientInfo)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token should fail authentication, got %v", err)
	}
}

func TestPairingAuth_DeviceNameTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	code := strings.Split(info.PairingURL, "#pair=")[1]

	longName := strings.Repeat("A", 100)
	res, err := auth.Pair(code, longName, ClientInfo{})
	if err != nil {
		t.Fatalf("Pair failed: %v", err)
	}

	if len(res.Label) != 64 || res.Label != strings.Repeat("A", 64) {
		t.Fatalf("expected device name capped at 64 characters, got len=%d", len(res.Label))
	}
}

type infiniteByteReader struct {
	val byte
}

func (r *infiniteByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.val
	}
	return len(p), nil
}

func TestPairingAuth_InjectableRand(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	// 注入无限返回 0x42 的伪随机字节流
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
		Rand:        &infiniteByteReader{val: 0x42},
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	expectedCode, err := generateToken(&infiniteByteReader{val: 0x42})
	if err != nil {
		t.Fatalf("generateToken failed: %v", err)
	}
	if !strings.Contains(info.PairingURL, expectedCode) {
		t.Fatalf("expected deterministic pairing code %s in %s", expectedCode, info.PairingURL)
	}
}

type stepReader struct {
	b byte
}

func (s *stepReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = s.b
		s.b++
	}
	return len(p), nil
}

func TestPairingAuth_ShortCode(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	sr := &stepReader{b: 1}
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		Rand:        sr,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()

	// 1. ShortCode 恰为 6 位数字
	if len(info.ShortCode) != 6 {
		t.Fatalf("expected 6-digit shortCode, got %q", info.ShortCode)
	}
	for _, c := range info.ShortCode {
		if c < '0' || c > '9' {
			t.Fatalf("shortCode contains non-digit character: %q", info.ShortCode)
		}
	}

	// 2. 用 ShortCode 配对成功后，token 和 ShortCode 都失效
	parts := strings.Split(info.PairingURL, "#pair=")
	if len(parts) != 2 {
		t.Fatalf("invalid pairing URL format: %s", info.PairingURL)
	}
	token := parts[1]

	clientInfo := ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}
	res, err := auth.Pair(info.ShortCode, "Phone Via ShortCode", clientInfo)
	if err != nil {
		t.Fatalf("Pair with ShortCode failed: %v", err)
	}
	if res.SessionToken == "" || res.DeviceID == "" {
		t.Fatalf("invalid PairResult: %+v", res)
	}

	// ShortCode 已失效
	if _, err := auth.Pair(info.ShortCode, "Phone Again", clientInfo); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken when reusing ShortCode, got %v", err)
	}
	// token 也已失效
	if _, err := auth.Pair(token, "Phone Again Token", clientInfo); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken when using token after ShortCode paired, got %v", err)
	}
}

func TestPairingAuth_ShortCode_Expiration(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	sr := &stepReader{b: 10}
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		Rand:        sr,
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()

	// 到达 5 分钟边界即失效，与 UI 的倒计时一致。
	currTime = info.ExpiresAt
	clientInfo := ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}
	if _, err := auth.Pair(info.ShortCode, "Expired Phone", clientInfo); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for expired ShortCode, got %v", err)
	}
}

func TestPairingAuth_FailuresBlockSourceAddressOnly(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	sr := &stepReader{b: 20}
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		Rand:        sr,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	attacker := ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}

	// 同一来源地址连续 5 次错误：每次仍是 ErrInvalidToken，配对码不作废也不轮换
	for i := 1; i <= maxPairingFailures; i++ {
		if _, err := auth.Pair("999999", "Attacker", attacker); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("attempt %d: expected ErrInvalidToken, got %v", i, err)
		}
	}

	// 封禁期内该地址即使拿正确的码也被拒
	if _, err := auth.Pair(info.ShortCode, "Attacker", attacker); !errors.Is(err, ErrPairingBlocked) {
		t.Fatalf("expected ErrPairingBlocked for blocked address, got %v", err)
	}

	// 配对码保持有效：另一台设备用同一个码仍能配对成功
	current := auth.CurrentPairingURLs()
	if current.ShortCode != info.ShortCode || current.PairingURL != info.PairingURL {
		t.Fatalf("pairing code must not rotate because of failures: got %s, want %s", current.ShortCode, info.ShortCode)
	}
	res, err := auth.Pair(info.ShortCode, "Valid Phone", ClientInfo{Address: "192.168.1.60", Entry: EntryLAN})
	if err != nil {
		t.Fatalf("Pair from another address failed: %v", err)
	}
	if res.DeviceID == "" {
		t.Fatalf("expected valid deviceID, got empty")
	}

	// 封禁 5 分钟后自动解除，该地址可以重新配对
	currTime = currTime.Add(pairingBlockDuration + time.Second)
	newInfo, err := auth.NewPairingCode()
	if err != nil {
		t.Fatalf("NewPairingCode failed: %v", err)
	}
	if _, err := auth.Pair(newInfo.ShortCode, "Attacker", attacker); err != nil {
		t.Fatalf("expected block to expire, got %v", err)
	}
}

func TestPairingAuth_NewPairingCode_RotatesShortCode(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	sr := &stepReader{b: 30}
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		Now:         now,
		Rand:        sr,
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info1 := auth.CurrentPairingURLs()

	// 5. NewPairingCode 会换发新的 ShortCode
	info2, err := auth.NewPairingCode()
	if err != nil {
		t.Fatalf("NewPairingCode failed: %v", err)
	}

	if len(info2.ShortCode) != 6 {
		t.Fatalf("expected 6-digit short code, got %s", info2.ShortCode)
	}
	if info2.ShortCode == info1.ShortCode {
		t.Fatalf("expected rotated ShortCode, got identical %s", info2.ShortCode)
	}

	// 旧 ShortCode 失效，新 ShortCode 有效
	clientInfo := ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}
	if _, err := auth.Pair(info1.ShortCode, "Old Phone", clientInfo); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for old ShortCode, got %v", err)
	}
	if _, err := auth.Pair(info2.ShortCode, "New Phone", clientInfo); err != nil {
		t.Fatalf("Pair with new ShortCode failed: %v", err)
	}
}

// TestPairingAuth_PairingURLPriority 验证主链接优先级为 Tailscale > TLS > LAN，
// 三个链接各自独立填充，只有 Tailscale 可用时配对状态仍为可用。
func TestPairingAuth_PairingURLPriority(t *testing.T) {
	const (
		lanBaseURL       = "http://192.168.1.10:43117"
		tlsBaseURL       = "https://mcode.example.com"
		tailscaleBaseURL = "https://mcode.example.ts.net"
	)
	optionalBase := func(value string) func() string {
		if value == "" {
			return nil
		}
		return func() string { return value }
	}

	cases := []struct {
		name             string
		lan              string
		tls              string
		tailscale        string
		wantMain         string
		wantLAN          string
		wantTLS          string
		wantTailscale    string
		wantPairingState string
	}{
		{
			name:             "all entries prefer tailscale",
			lan:              lanBaseURL,
			tls:              tlsBaseURL,
			tailscale:        tailscaleBaseURL,
			wantMain:         tailscaleBaseURL,
			wantLAN:          lanBaseURL,
			wantTLS:          tlsBaseURL,
			wantTailscale:    tailscaleBaseURL,
			wantPairingState: "active",
		},
		{
			name:             "tls and lan fall back to tls",
			lan:              lanBaseURL,
			tls:              tlsBaseURL,
			wantMain:         tlsBaseURL,
			wantLAN:          lanBaseURL,
			wantTLS:          tlsBaseURL,
			wantPairingState: "active",
		},
		{
			name:             "only tailscale is available",
			tailscale:        tailscaleBaseURL,
			wantMain:         tailscaleBaseURL,
			wantTailscale:    tailscaleBaseURL,
			wantPairingState: "active",
		},
		{
			name:             "only lan is available",
			lan:              lanBaseURL,
			wantMain:         lanBaseURL,
			wantLAN:          lanBaseURL,
			wantPairingState: "active",
		},
		{
			name:             "no base url is unavailable",
			wantPairingState: "unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := NewPairingAuth(PairingAuthOptions{
				DevicesPath:      filepath.Join(t.TempDir(), "remote_devices.json"),
				LANBaseURL:       optionalBase(tc.lan),
				TLSBaseURL:       optionalBase(tc.tls),
				TailscaleBaseURL: optionalBase(tc.tailscale),
			})
			if err != nil {
				t.Fatalf("NewPairingAuth failed: %v", err)
			}

			if state := auth.PairingState(); state != tc.wantPairingState {
				t.Fatalf("PairingState() = %q, want %q", state, tc.wantPairingState)
			}

			info := auth.CurrentPairingURLs()
			var code string
			if tc.wantMain != "" {
				const marker = "#pair="
				idx := strings.Index(info.PairingURL, marker)
				if idx < 0 || idx+len(marker) == len(info.PairingURL) {
					t.Fatalf("pairing URL missing #pair code: %q", info.PairingURL)
				}
				code = info.PairingURL[idx+len(marker):]
			}
			withCode := func(baseURL string) string {
				if baseURL == "" {
					return ""
				}
				return baseURL + "/#pair=" + code
			}

			if info.PairingURL != withCode(tc.wantMain) {
				t.Errorf("PairingURL = %q, want %q", info.PairingURL, withCode(tc.wantMain))
			}
			if info.LANPairingURL != withCode(tc.wantLAN) {
				t.Errorf("LANPairingURL = %q, want %q", info.LANPairingURL, withCode(tc.wantLAN))
			}
			if info.TLSPairingURL != withCode(tc.wantTLS) {
				t.Errorf("TLSPairingURL = %q, want %q", info.TLSPairingURL, withCode(tc.wantTLS))
			}
			if info.TailscalePairingURL != withCode(tc.wantTailscale) {
				t.Errorf("TailscalePairingURL = %q, want %q", info.TailscalePairingURL, withCode(tc.wantTailscale))
			}
		})
	}
}
