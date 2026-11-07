package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"mcode/internal/config"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

func TestRemoteServiceLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)

	// 未 Start 时 Pairing 返回 ErrRemoteNotRunning
	if _, err := rs.Pairing(false); !errors.Is(err, ErrRemoteNotRunning) {
		t.Fatalf("expected ErrRemoteNotRunning before Start, got %v", err)
	}

	// Start("127.0.0.1:0", nil) 后 Status().State 为 running、Port>0、LANURLs 为 nil
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	status := rs.Status()
	if status.State != mcodeui.RemoteStateRunning {
		t.Fatalf("expected state running, got %s", status.State)
	}
	if status.Port <= 0 {
		t.Fatalf("expected Port > 0, got %d", status.Port)
	}
	if status.LANURLs != nil {
		t.Fatalf("expected LANURLs to be nil for loopback, got %v", status.LANURLs)
	}

	// net.Dial 该端口成功
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(status.Port)), 2*time.Second)
	if err != nil {
		t.Fatalf("net.Dial failed: %v", err)
	}
	_ = conn.Close()

	// Pairing(false) 返回 6 位数字 ShortCode，且 LANPairingURL 符合规范
	pair1, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing(false) failed: %v", err)
	}
	if !strings.HasPrefix(pair1.LANPairingURL, "http://127.0.0.1:") {
		t.Fatalf("expected LANPairingURL to start with %q, got %q", "http://127.0.0.1:", pair1.LANPairingURL)
	}
	if !strings.Contains(pair1.LANPairingURL, "#pair=") {
		t.Fatalf("expected LANPairingURL to contain #pair=, got %q", pair1.LANPairingURL)
	}
	if len(pair1.ShortCode) != 6 {
		t.Fatalf("expected 6-digit short code, got %q", pair1.ShortCode)
	}
	for _, c := range pair1.ShortCode {
		if c < '0' || c > '9' {
			t.Fatalf("expected short code digits only, got %q", pair1.ShortCode)
		}
	}

	// Pairing(true) 返回不同的 ShortCode
	pair2, err := rs.Pairing(true)
	if err != nil {
		t.Fatalf("Pairing(true) failed: %v", err)
	}
	if len(pair2.ShortCode) != 6 {
		t.Fatalf("expected 6-digit short code on refresh, got %q", pair2.ShortCode)
	}
	if pair2.PairingURL == pair1.PairingURL {
		t.Fatal("刷新后配对链接未更新")
	}

	port := status.Port

	// Stop 后 State 为 stopped，Pairing 返回 ErrRemoteNotRunning，net.Dial 失败
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rs.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	statusAfterStop := rs.Status()
	if statusAfterStop.State != mcodeui.RemoteStateStopped {
		t.Fatalf("expected state stopped, got %s", statusAfterStop.State)
	}
	if _, err := rs.Pairing(false); !errors.Is(err, ErrRemoteNotRunning) {
		t.Fatalf("expected ErrRemoteNotRunning after Stop, got %v", err)
	}

	dialConn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if dialErr == nil {
		_ = dialConn.Close()
		t.Fatalf("expected dial to fail after stop, but succeeded")
	}

	// Stop 后再次 Start 能成功
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if rs.Status().State != mcodeui.RemoteStateRunning {
		t.Fatalf("expected state running after restart, got %s", rs.Status().State)
	}
	_ = rs.Stop(ctx)

	// 先占用一个端口再 Start 到该端口，返回错误且 State 为 error、Error 非空
	occLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied failed: %v", err)
	}
	defer occLn.Close()
	occupiedAddr := occLn.Addr().String()

	err = rs.Start(occupiedAddr, nil)
	if err == nil {
		t.Fatalf("expected Start to fail on occupied address %s", occupiedAddr)
	}
	errorStatus := rs.Status()
	if errorStatus.State != mcodeui.RemoteStateError {
		t.Fatalf("expected state error on failed start, got %s", errorStatus.State)
	}
	if errorStatus.Error == "" {
		t.Fatalf("expected non-empty Error on failed start")
	}
}

func TestRemoteServiceImmediateStop(t *testing.T) {
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()
	rs := NewRemoteService(t.TempDir(), backend)
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	for i := 0; i < 20; i++ {
		if err := rs.Start("127.0.0.1:0", nil); err != nil {
			t.Fatal(err)
		}
		addr := rs.Status().ListenAddr
		if err := rs.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		// 随机端口释放后可能被并行运行的其它测试进程短暂占用，短暂重试；
		// 若是本服务泄漏监听，端口会一直被占，重试后仍会失败。
		var ln net.Listener
		var err error
		for deadline := time.Now().Add(time.Second); ; time.Sleep(20 * time.Millisecond) {
			if ln, err = net.Listen("tcp", addr); err == nil || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			t.Fatalf("停止后监听端口未释放：%v", err)
		}
		_ = ln.Close()
		if st := rs.Status(); st.State != mcodeui.RemoteStateStopped || st.Error != "" {
			t.Fatalf("停止状态不正确：%+v", st)
		}
	}
}

// TestRemoteServicePushObserverNotDuplicated 验证反复启停远程服务不会叠加 StreamHub 观察者，
// 否则同一会话事件会被重复推送。
func TestRemoteServicePushObserverNotDuplicated(t *testing.T) {
	live := 0
	orig := pushHubAddObserver
	pushHubAddObserver = func(_ *remote.StreamHub, _ func(string, string, any)) func() {
		live++
		removed := false
		return func() {
			if removed {
				return
			}
			removed = true
			live--
		}
	}
	t.Cleanup(func() { pushHubAddObserver = orig })

	// 观察者挂在 backend 的 StreamHub 上，hub 为 nil 时 startPushLocked 不会登记，
	// 因此这里必须给 MemoryBackend 一个真实的 hub，推送才会按真实路径启动。
	backend := remote.NewMemoryBackend(remote.NewStreamHub(remote.StreamHubOptions{}))
	defer backend.Close()
	rs := NewRemoteService(t.TempDir(), backend)
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	for i := 0; i < 2; i++ {
		if err := rs.Start("127.0.0.1:0", nil); err != nil {
			t.Fatalf("第 %d 次启动失败：%v", i+1, err)
		}
		// 已运行时重复 Start 直接返回，也不得重复登记观察者。
		if err := rs.Start("127.0.0.1:0", nil); err != nil {
			t.Fatalf("重复启动失败：%v", err)
		}
		if live != 1 {
			t.Fatalf("第 %d 次启动后观察者数量为 %d，期望 1", i+1, live)
		}
		if err := rs.Stop(context.Background()); err != nil {
			t.Fatalf("第 %d 次停止失败：%v", i+1, err)
		}
		rs.mu.Lock()
		unobserve := rs.pushUnobserve
		rs.mu.Unlock()
		if live != 0 || unobserve != nil {
			t.Fatalf("第 %d 次停止后仍残留推送观察者：live=%d", i+1, live)
		}
	}
}

func TestRemoteServicePairingOverWebSocket(t *testing.T) {
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()
	rs := NewRemoteService(t.TempDir(), backend)
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	old, err := rs.Pairing(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+rs.Status().ListenAddr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	request := func(token string) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "session.pair",
			"params": map[string]string{"token": token},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
		_, response, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(response, &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.Error) > 0 {
			return nil
		}
		return reply.Result
	}
	oldToken := strings.Split(old.PairingURL, "#pair=")[1]
	if request(oldToken) != nil {
		t.Fatal("重启后旧配对链接仍然有效")
	}
	pair, err := rs.Pairing(false)
	if err != nil {
		t.Fatal(err)
	}
	if request(pair.ShortCode) == nil {
		t.Fatal("6 位授权码未能通过 WebSocket 配对")
	}
	if request(pair.ShortCode) != nil {
		t.Fatal("已使用的授权码仍然有效")
	}
}

func TestClientSetRemoteEnabled(t *testing.T) {
	dataDir := t.TempDir()
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	cfg := config.Defaults()
	cfg.Remote.Listen = "127.0.0.1:0"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	rs := NewRemoteService(dataDir, backend)
	client := NewClient(backend)
	client.SetRemoteService(rs)

	ctx := context.Background()

	// 调用 true 后为 running
	st, err := client.SetRemoteEnabled(ctx, true)
	if err != nil {
		t.Fatalf("SetRemoteEnabled(true) failed: %v", err)
	}
	if st.State != mcodeui.RemoteStateRunning {
		t.Fatalf("expected state running, got %s", st.State)
	}

	// 验证 Client.RemoteStatus 也是 running
	currentSt, err := client.RemoteStatus(ctx)
	if err != nil {
		t.Fatalf("RemoteStatus failed: %v", err)
	}
	if currentSt.State != mcodeui.RemoteStateRunning {
		t.Fatalf("expected state running from RemoteStatus, got %s", currentSt.State)
	}

	// 验证 Client.RemotePairing
	pairing, err := client.RemotePairing(ctx, false)
	if err != nil {
		t.Fatalf("RemotePairing failed: %v", err)
	}
	if len(pairing.ShortCode) != 6 {
		t.Fatalf("expected 6-digit short code, got %q", pairing.ShortCode)
	}

	// 调用 false 后为 stopped
	st, err = client.SetRemoteEnabled(ctx, false)
	if err != nil {
		t.Fatalf("SetRemoteEnabled(false) failed: %v", err)
	}
	if st.State != mcodeui.RemoteStateStopped {
		t.Fatalf("expected state stopped, got %s", st.State)
	}
}

func TestClientRemoteNil(t *testing.T) {
	client := NewClient(nil)
	ctx := context.Background()

	st, err := client.RemoteStatus(ctx)
	if !errors.Is(err, ErrRemoteNotRunning) {
		t.Fatalf("expected ErrRemoteNotRunning, got %v", err)
	}
	if st.State != mcodeui.RemoteStateStopped {
		t.Fatalf("expected state stopped, got %s", st.State)
	}

	st, err = client.SetRemoteEnabled(ctx, true)
	if !errors.Is(err, ErrRemoteNotRunning) {
		t.Fatalf("expected ErrRemoteNotRunning, got %v", err)
	}
	if st.State != mcodeui.RemoteStateStopped {
		t.Fatalf("expected state stopped, got %s", st.State)
	}

	_, err = client.RemotePairing(ctx, false)
	if !errors.Is(err, ErrRemoteNotRunning) {
		t.Fatalf("expected ErrRemoteNotRunning, got %v", err)
	}
}

func TestRemoteServicePairingURLUsesDomain(t *testing.T) {
	dataDir := t.TempDir()
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	cfg := config.Defaults()
	cfg.Remote.TLSDomain = "mcode.example.com"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	rs := NewRemoteService(dataDir, backend)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	// TLS 入口未运行（TLSListen 为空）时，配对链接改用域名 + 局域网 HTTP 端口。
	pairing, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing failed: %v", err)
	}
	want := "http://" + net.JoinHostPort("mcode.example.com", strconv.Itoa(rs.Status().Port)) + "/#pair="
	if !strings.HasPrefix(pairing.PairingURL, want) {
		t.Fatalf("expected PairingURL to start with %q, got %q", want, pairing.PairingURL)
	}
	if !strings.HasPrefix(pairing.LANPairingURL, "http://127.0.0.1:") {
		t.Fatalf("expected LANPairingURL to keep LAN address, got %q", pairing.LANPairingURL)
	}
}
