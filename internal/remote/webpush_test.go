package remote

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// fixedPushTime 是推送相关测试共用的固定时钟。
var fixedPushTime = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

const (
	validP256dhConst = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	validAuthConst   = "BTBZMqHH6r4Tts7J_aSIgg"
)

// TestEncryptWebPush_RFC8291Vector 严格使用 RFC 8291 附录 A 测试向量验证加密输出。
func TestEncryptWebPush_RFC8291Vector(t *testing.T) {
	plaintext := []byte("When I grow up, I want to be a watermelon")

	authSecretB64 := "BTBZMqHH6r4Tts7J_aSIgg"
	authSecret, err := base64.RawURLEncoding.DecodeString(authSecretB64)
	if err != nil {
		t.Fatalf("failed to decode auth secret: %v", err)
	}

	uaPublicB64 := "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	uaPublic, err := base64.RawURLEncoding.DecodeString(uaPublicB64)
	if err != nil {
		t.Fatalf("failed to decode ua public key: %v", err)
	}

	asPrivateB64 := "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	asPrivateBytes, err := base64.RawURLEncoding.DecodeString(asPrivateB64)
	if err != nil {
		t.Fatalf("failed to decode as private key: %v", err)
	}
	asPriv, err := ecdh.P256().NewPrivateKey(asPrivateBytes)
	if err != nil {
		t.Fatalf("failed to create as ecdh private key: %v", err)
	}

	saltB64 := "DGv6ra1nlYgDCS1FRnbzlw"
	salt, err := base64.RawURLEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatalf("failed to decode salt: %v", err)
	}

	rs := uint32(4096)

	expectedCiphertextB64 := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	expected, err := base64.RawURLEncoding.DecodeString(expectedCiphertextB64)
	if err != nil {
		t.Fatalf("failed to decode expected ciphertext: %v", err)
	}

	got, err := encryptWebPush(plaintext, uaPublic, authSecret, asPriv, salt, rs)
	if err != nil {
		t.Fatalf("encryptWebPush failed: %v", err)
	}

	if !bytes.Equal(got, expected) {
		t.Fatalf("ciphertext mismatch.\nGot:      %s\nExpected: %s",
			base64.RawURLEncoding.EncodeToString(got),
			expectedCiphertextB64,
		)
	}
}

// TestVAPIDKey_PersistenceAndReuse 验证 VAPID 密钥持久化与复用。
func TestVAPIDKey_PersistenceAndReuse(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vapid.pem")

	key1, err := loadOrCreateVAPIDKey(keyPath)
	if err != nil {
		t.Fatalf("loadOrCreateVAPIDKey failed: %v", err)
	}

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("failed to stat key file: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("expected file mode 0600, got %o", fi.Mode().Perm())
	}

	key2, err := loadOrCreateVAPIDKey(keyPath)
	if err != nil {
		t.Fatalf("second loadOrCreateVAPIDKey failed: %v", err)
	}

	if key1.D.Cmp(key2.D) != 0 {
		t.Fatalf("expected key to be reused, but D scalars differ")
	}
}

// TestVAPIDJWT_Verification 验证生成的 VAPID JWT 格式及签名有效性。
func TestVAPIDJWT_Verification(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vapid.pem")
	key, err := loadOrCreateVAPIDKey(keyPath)
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	ecdhPriv, err := key.ECDH()
	if err != nil {
		t.Fatalf("failed to convert to ECDH: %v", err)
	}
	pubB64 := base64.RawURLEncoding.EncodeToString(ecdhPriv.PublicKey().Bytes())

	now := time.Now()
	header, err := vapidAuthorizationHeader(key, pubB64, "mailto:mcode@localhost", "https://push.example.com/v1/sub", now)
	if err != nil {
		t.Fatalf("vapidAuthorizationHeader failed: %v", err)
	}

	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("expected 'vapid t=' prefix, got: %s", header)
	}
	parts := strings.Split(header, ", k=")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts separated by ', k=', got: %s", header)
	}
	if parts[1] != pubB64 {
		t.Fatalf("expected public key %s, got %s", pubB64, parts[1])
	}

	jwtStr := strings.TrimPrefix(parts[0], "vapid t=")
	jwtParts := strings.Split(jwtStr, ".")
	if len(jwtParts) != 3 {
		t.Fatalf("expected 3 jwt parts, got %d", len(jwtParts))
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(jwtParts[1])
	if err != nil {
		t.Fatalf("failed to decode claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("failed to unmarshal claims: %v", err)
	}
	if claims.Aud != "https://push.example.com" {
		t.Errorf("expected aud https://push.example.com, got %s", claims.Aud)
	}
	if claims.Sub != "mailto:mcode@localhost" {
		t.Errorf("expected sub mailto:mcode@localhost, got %s", claims.Sub)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(jwtParts[2])
	if err != nil || len(sigBytes) != 64 {
		t.Fatalf("invalid signature bytes, len: %d, err: %v", len(sigBytes), err)
	}

	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])

	signingInput := jwtParts[0] + "." + jwtParts[1]
	hash := sha256.Sum256([]byte(signingInput))

	if !ecdsa.Verify(&key.PublicKey, hash[:], r, s) {
		t.Fatalf("JWT signature verification failed")
	}
}

// TestWebPushService_SubscribeAndUnsubscribe_Persistence 测试订阅生命周期与重启持久化。
func TestWebPushService_SubscribeAndUnsubscribe_Persistence(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "devices.json")
	keyPath := filepath.Join(dir, "vapid.pem")

	now := time.Now()
	store, err := NewDeviceStore(storePath, func() time.Time { return now })
	if err != nil {
		t.Fatalf("failed to create DeviceStore: %v", err)
	}

	dev := DeviceRecord{
		DeviceID:  "dev-test-1",
		Label:     "Browser",
		PairedAt:  now,
		TokenHash: HashToken("tok1"),
	}
	if err := store.Add(dev); err != nil {
		t.Fatalf("failed to add device: %v", err)
	}

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath: keyPath,
		Devices: store,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("failed to create WebPushService: %v", err)
	}

	// 验证参数校验
	if err := svc.Subscribe("dev-test-1", "http://not-https.com", "abc", "def"); err == nil {
		t.Fatalf("expected error for non-https endpoint")
	}

	endpoint := "https://fcm.googleapis.com/fcm/send/123"

	if err := svc.Subscribe("dev-test-1", endpoint, validP256dhConst, validAuthConst); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	subs := store.AllPushSubscriptions()
	if len(subs) != 1 || subs[0].Endpoint != endpoint {
		t.Fatalf("expected 1 subscription, got %+v", subs)
	}

	// 重启 DeviceStore 验证持久化
	store2, err := NewDeviceStore(storePath, func() time.Time { return now })
	if err != nil {
		t.Fatalf("failed to reload DeviceStore: %v", err)
	}
	subs2 := store2.AllPushSubscriptions()
	if len(subs2) != 1 || subs2[0].Endpoint != endpoint {
		t.Fatalf("expected 1 subscription after restart, got %+v", subs2)
	}

	// 取消订阅
	svc2, _ := NewWebPushService(WebPushOptions{
		KeyPath: keyPath,
		Devices: store2,
		Now:     func() time.Time { return now },
	})
	unsub, err := svc2.Unsubscribe("dev-test-1", endpoint)
	if err != nil || !unsub {
		t.Fatalf("Unsubscribe failed: %v, %v", unsub, err)
	}

	if len(store2.AllPushSubscriptions()) != 0 {
		t.Fatalf("expected 0 subscriptions after unsubscribe")
	}
}

// TestWebPushService_SendAndGoneRemoval 测试推送发送请求头、长度以及 404/410 清除订阅。
func TestWebPushService_SendAndGoneRemoval(t *testing.T) {
	var mu sync.Mutex
	var receivedHeaders http.Header
	var receivedBodyLen int
	statusCode := http.StatusCreated

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		receivedBodyLen = len(body)
		w.WriteHeader(statusCode)
	}))
	defer server.Close()

	dir := t.TempDir()
	storePath := filepath.Join(dir, "devices.json")
	keyPath := filepath.Join(dir, "vapid.pem")

	now := time.Now()
	store, _ := NewDeviceStore(storePath, func() time.Time { return now })
	_ = store.Add(DeviceRecord{
		DeviceID:  "dev-1",
		Label:     "Browser",
		PairedAt:  now,
		TokenHash: HashToken("tok1"),
	})

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    keyPath,
		Devices:    store,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewWebPushService failed: %v", err)
	}

	validP256dhConst := "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	validAuthConst := "BTBZMqHH6r4Tts7J_aSIgg"
	endpoint := server.URL

	// 订阅
	_ = store.AddPushSubscription("dev-1", PushSubscription{
		Endpoint:  endpoint,
		P256dh:    validP256dhConst,
		Auth:      validAuthConst,
		CreatedAt: now,
	})

	// 发送推送
	svc.Send(context.Background(), PushPayload{
		Title:    "Hello",
		Body:     "World",
		ThreadID: "t-1",
	})

	// 等待异步 goroutine 完成
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if !strings.HasPrefix(receivedHeaders.Get("Authorization"), "vapid t=") {
		t.Errorf("expected vapid auth header, got %s", receivedHeaders.Get("Authorization"))
	}
	if receivedHeaders.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("expected Content-Encoding aes128gcm, got %s", receivedHeaders.Get("Content-Encoding"))
	}
	if receivedHeaders.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("expected Content-Type application/octet-stream, got %s", receivedHeaders.Get("Content-Type"))
	}
	if receivedHeaders.Get("TTL") != "86400" {
		t.Errorf("expected TTL 86400, got %s", receivedHeaders.Get("TTL"))
	}
	if receivedHeaders.Get("Urgency") != "high" {
		t.Errorf("expected Urgency high, got %s", receivedHeaders.Get("Urgency"))
	}
	if receivedBodyLen <= 86 {
		t.Errorf("expected body length > 86 (header size), got %d", receivedBodyLen)
	}
	mu.Unlock()

	// 将服务端状态码改为 410 Gone
	mu.Lock()
	statusCode = http.StatusGone
	mu.Unlock()

	svc.Send(context.Background(), PushPayload{
		Title:    "Hello 2",
		Body:     "World 2",
		ThreadID: "t-1",
	})

	time.Sleep(100 * time.Millisecond)

	if len(store.AllPushSubscriptions()) != 0 {
		t.Fatalf("expected subscription to be removed on 410 Gone")
	}
}

// TestWebPushService_ObserveHub 验证观察者在审批新增与 running->idle 时触发推送。
func TestWebPushService_ObserveHub(t *testing.T) {
	pushCh := make(chan struct{}, 10)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushCh <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	storePath := filepath.Join(dir, "devices.json")
	keyPath := filepath.Join(dir, "vapid.pem")

	now := time.Now()
	store, _ := NewDeviceStore(storePath, func() time.Time { return now })
	_ = store.Add(DeviceRecord{
		DeviceID:  "dev-1",
		Label:     "Browser",
		PairedAt:  now,
		TokenHash: HashToken("tok1"),
		PushSubscriptions: []PushSubscription{
			{
				Endpoint:  server.URL,
				P256dh:    "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
				Auth:      "BTBZMqHH6r4Tts7J_aSIgg",
				CreatedAt: now,
			},
		},
	})

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    keyPath,
		Devices:    store,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	svc.ObserveHub(hub)

	// 测试 1: requests.changed 触发审批推送
	reqTitle := "是否允许运行脚本"
	hub.Notify("t-1", "requests.changed", wire.RequestsChangedParams{
		ThreadID: "t-1",
		Requests: []wire.RemoteRequest{
			{
				ThreadID:  "t-1",
				RequestID: "req-1",
				Title:     &reqTitle,
			},
		},
	})

	select {
	case <-pushCh:
		// 收到审批推送
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for approval push")
	}

	// 测试 2: 再次发送相同请求不应重复触发
	hub.Notify("t-1", "requests.changed", wire.RequestsChangedParams{
		ThreadID: "t-1",
		Requests: []wire.RemoteRequest{
			{
				ThreadID:  "t-1",
				RequestID: "req-1",
				Title:     &reqTitle,
			},
		},
	})

	select {
	case <-pushCh:
		t.Fatalf("unexpected duplicate approval push")
	case <-time.After(100 * time.Millisecond):
		// 预期不触发
	}

	// 测试 3: thread.summary 由 running -> idle 触发完成推送
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{
		ID:        "t-1",
		Title:     "构建任务",
		IsRunning: true,
	})

	select {
	case <-pushCh:
		t.Fatalf("unexpected push when starting run")
	case <-time.After(100 * time.Millisecond):
		// 预期不触发
	}

	hub.Publish("t-1", nil, nil, wire.ThreadSummary{
		ID:        "t-1",
		Title:     "构建任务",
		IsRunning: false,
	})

	select {
	case <-pushCh:
		// 收到会话完成推送
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for done push")
	}
}

func newTestPushService(t *testing.T, store *DeviceStore) *WebPushService {
	t.Helper()
	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    filepath.Join(t.TempDir(), "vapid.pem"),
		Devices:    store,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Now:        func() time.Time { return fixedPushTime },
	})
	if err != nil {
		t.Fatalf("NewWebPushService: %v", err)
	}
	return svc
}

func newPushTestStore(t *testing.T) *DeviceStore {
	t.Helper()
	store, err := NewDeviceStore(filepath.Join(t.TempDir(), "devices.json"), func() time.Time { return fixedPushTime })
	if err != nil {
		t.Fatalf("NewDeviceStore: %v", err)
	}
	return store
}

func TestWebPushService_SubscribeRejectsUnknownPushHost(t *testing.T) {
	store := newPushTestStore(t)
	_ = store.Add(DeviceRecord{DeviceID: "dev-1", Label: "Phone", PairedAt: fixedPushTime, TokenHash: HashToken("tok")})
	svc := newTestPushService(t, store)

	// 不在 allowedPushHosts 白名单里的 https 域名必须拒绝（防 SSRF）。
	for _, endpoint := range []string{
		"https://evil.example.com/push/1",
		"https://fcm.googleapis.com.evil.example.com/push/1",
		"http://fcm.googleapis.com/push/1",
	} {
		err := svc.Subscribe("dev-1", endpoint, validP256dhConst, validAuthConst)
		if err == nil {
			t.Fatalf("expected rejection for endpoint %q", endpoint)
		}
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams for %q, got %v", endpoint, err)
		}
	}
	if n := store.PushSubscriptionCount(); n != 0 {
		t.Fatalf("expected no subscriptions stored, got %d", n)
	}

	// 允许的推送服务（含子域）可以订阅。
	for _, endpoint := range []string{
		"https://web.push.apple.com/abc",
		"https://fcm.googleapis.com/fcm/send/xyz",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
	} {
		if err := svc.Subscribe("dev-1", endpoint, validP256dhConst, validAuthConst); err != nil {
			t.Fatalf("expected endpoint %q to be accepted, got %v", endpoint, err)
		}
	}
	if n := store.PushSubscriptionCount(); n != 3 {
		t.Fatalf("expected 3 subscriptions, got %d", n)
	}
}

func TestParsePushEndpoint(t *testing.T) {
	if _, ok := ParsePushEndpoint("https://notify.windows.com/w/?appId=x"); !ok {
		t.Fatal("expected notify.windows.com to be allowed")
	}
	if _, ok := ParsePushEndpoint("https://example.com/"); ok {
		t.Fatal("expected example.com to be rejected")
	}
	if _, ok := ParsePushEndpoint("https://fcm.googleapis.com.evil.example.com/"); ok {
		t.Fatal("expected suffix spoof to be rejected")
	}
}

// TestDeviceStore_PushSubscriptionLifecycle 验证按设备保存订阅、按 endpoint 反查设备、撤销设备后订阅消失且落盘。
func TestDeviceStore_PushSubscriptionLifecycle(t *testing.T) {
	store := newPushTestStore(t)
	path := filepath.Join(filepath.Dir(store.filePath), "devices.json")
	for _, id := range []string{"dev-1", "dev-2"} {
		_ = store.Add(DeviceRecord{DeviceID: id, Label: id, PairedAt: fixedPushTime, TokenHash: HashToken(id)})
		_ = store.AddPushSubscription(id, PushSubscription{
			Endpoint:  "https://fcm.googleapis.com/" + id,
			P256dh:    validP256dhConst,
			Auth:      validAuthConst,
			CreatedAt: fixedPushTime,
		})
	}

	if deviceID, ok := store.FindDeviceByPushEndpoint("https://fcm.googleapis.com/dev-2"); !ok || deviceID != "dev-2" {
		t.Fatalf("FindDeviceByPushEndpoint = %q, %v", deviceID, ok)
	}

	if _, err := store.Remove("dev-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	reloaded, err := NewDeviceStore(path, func() time.Time { return fixedPushTime })
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	subs := reloaded.AllPushSubscriptions()
	if len(subs) != 1 || subs[0].Endpoint != "https://fcm.googleapis.com/dev-2" {
		t.Fatalf("expected only dev-2's subscription to survive, got %+v", subs)
	}
}

func TestDeviceStore_RemoveAllPushSubscriptions(t *testing.T) {
	store := newPushTestStore(t)
	_ = store.Add(DeviceRecord{DeviceID: "dev-1", Label: "a", PairedAt: fixedPushTime, TokenHash: HashToken("a")})
	_ = store.AddPushSubscription("dev-1", PushSubscription{Endpoint: "https://fcm.googleapis.com/a", P256dh: validP256dhConst, Auth: validAuthConst, CreatedAt: fixedPushTime})

	if _, err := store.RemoveAllPushSubscriptions(); err != nil {
		t.Fatalf("RemoveAllPushSubscriptions: %v", err)
	}
	if n := store.PushSubscriptionCount(); n != 0 {
		t.Fatalf("expected 0 subscriptions, got %d", n)
	}
	// 设备本身保留。
	if _, ok := store.FindByDeviceID("dev-1"); !ok {
		t.Fatal("expected device record to survive subscription purge")
	}
	if changed, err := store.RemoveAllPushSubscriptions(); err != nil || changed {
		t.Fatalf("expected second purge to be a no-op, got changed=%v err=%v", changed, err)
	}
}

func TestWebPushService_ForgetDeviceAndAll(t *testing.T) {
	store := newPushTestStore(t)
	for _, id := range []string{"dev-1", "dev-2"} {
		_ = store.Add(DeviceRecord{DeviceID: id, Label: id, PairedAt: fixedPushTime, TokenHash: HashToken(id)})
		_ = store.AddPushSubscription(id, PushSubscription{Endpoint: "https://fcm.googleapis.com/" + id, P256dh: validP256dhConst, Auth: validAuthConst, CreatedAt: fixedPushTime})
	}
	svc := newTestPushService(t, store)

	svc.ForgetDevice("dev-1")
	if subs := store.AllPushSubscriptions(); len(subs) != 1 || subs[0].Endpoint != "https://fcm.googleapis.com/dev-2" {
		t.Fatalf("expected only dev-2's subscription after forgetting dev-1, got %+v", subs)
	}
	svc.ForgetAllDevices()
	if n := store.PushSubscriptionCount(); n != 0 {
		t.Fatalf("expected 0 subscriptions after forgetting all, got %d", n)
	}
}

// TestWebPushService_SendTestRemovesGoneSubscription 验证 410 只删除对应设备的那条订阅，其余订阅保留。
func TestWebPushService_SendTestRemovesGoneSubscription(t *testing.T) {
	var mu sync.Mutex
	statusByPath := map[string]int{"/gone": http.StatusGone, "/ok": http.StatusCreated}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		code := statusByPath[r.URL.Path]
		mu.Unlock()
		w.WriteHeader(code)
	}))
	defer srv.Close()

	store := newPushTestStore(t)
	for _, id := range []string{"dev-gone", "dev-ok"} {
		_ = store.Add(DeviceRecord{DeviceID: id, Label: id, PairedAt: fixedPushTime, TokenHash: HashToken(id)})
	}

	// httptest 只提供 http 端点，直接写入存储，绕过 Subscribe 的 https 校验。
	_ = store.AddPushSubscription("dev-gone", PushSubscription{Endpoint: srv.URL + "/gone", P256dh: validP256dhConst, Auth: validAuthConst, CreatedAt: fixedPushTime})
	_ = store.AddPushSubscription("dev-ok", PushSubscription{Endpoint: srv.URL + "/ok", P256dh: validP256dhConst, Auth: validAuthConst, CreatedAt: fixedPushTime})

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    filepath.Join(t.TempDir(), "vapid.pem"),
		Devices:    store,
		HTTPClient: srv.Client(),
		Now:        func() time.Time { return fixedPushTime },
	})
	if err != nil {
		t.Fatalf("NewWebPushService: %v", err)
	}
	if sent, err := svc.SendTest(context.Background(), "t", "b"); err != nil || sent != 2 {
		t.Fatalf("SendTest = %d, %v", sent, err)
	}

	subs := store.AllPushSubscriptions()
	if len(subs) != 1 || subs[0].Endpoint != srv.URL+"/ok" {
		t.Fatalf("expected only the live endpoint to remain, got %+v", subs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 {
		t.Fatalf("expected 2 push requests, got %v", paths)
	}
}

// TestWebPushService_SendSkipsOnlineDevices 验证浏览器在线（已订阅会话）时跳过推送，离线设备仍收到。
func TestWebPushService_SendSkipsOnlineDevices(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	store := newPushTestStore(t)
	for _, id := range []string{"dev-online", "dev-offline"} {
		_ = store.Add(DeviceRecord{DeviceID: id, Label: id, PairedAt: fixedPushTime, TokenHash: HashToken(id)})
		_ = store.AddPushSubscription(id, PushSubscription{Endpoint: srv.URL + "/" + id, P256dh: validP256dhConst, Auth: validAuthConst, CreatedAt: fixedPushTime})
	}

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    filepath.Join(t.TempDir(), "vapid.pem"),
		Devices:    store,
		HTTPClient: srv.Client(),
		Now:        func() time.Time { return fixedPushTime },
		SkipOnline: func(deviceID string) bool { return deviceID == "dev-online" },
	})
	if err != nil {
		t.Fatalf("NewWebPushService: %v", err)
	}

	svc.Send(context.Background(), PushPayload{Title: "t", Body: "b"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := hits["/dev-offline"] == 1
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for offline device push")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/dev-online"] != 0 {
		t.Fatalf("expected online device to be skipped, got %d pushes", hits["/dev-online"])
	}
}

// TestWebPushService_AllowSendThrottles 验证同一会话同类提醒在最小间隔内只推一次。
func TestWebPushService_AllowSendThrottles(t *testing.T) {
	store := newPushTestStore(t)
	svc := newTestPushService(t, store)
	now := fixedPushTime
	svc.now = func() time.Time { return now }

	alert := SessionAlert{Kind: SessionAlertDone, ThreadID: "t-1"}
	if !svc.allowSend(alert) {
		t.Fatal("expected first alert to be allowed")
	}
	if svc.allowSend(alert) {
		t.Fatal("expected duplicate alert inside the throttle window to be dropped")
	}
	now = now.Add(svc.minInterval + time.Second)
	if !svc.allowSend(alert) {
		t.Fatal("expected alert after the throttle window to be allowed")
	}
	if !svc.allowSend(SessionAlert{Kind: SessionAlertApproval, ThreadID: "t-1"}) {
		t.Fatal("expected a different alert kind to be allowed")
	}
}

// TestVAPIDSubjectFor 验证 VAPID sub 的取值顺序：TLS 域名 > 本机地址 > 默认值。
func TestVAPIDSubjectFor(t *testing.T) {
	if got := VAPIDSubjectFor(" example.com ", nil); got != "https://example.com" {
		t.Fatalf("VAPIDSubjectFor domain = %q", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	if got := VAPIDSubjectFor("", ln); got != "https://"+ln.Addr().String() {
		t.Fatalf("VAPIDSubjectFor listener = %q, want https://%s", got, ln.Addr().String())
	}
	if got := VAPIDSubjectFor("", nil); got != defaultVAPIDSubject {
		t.Fatalf("VAPIDSubjectFor default = %q", got)
	}
}

// decryptWebPush 按 RFC 8188 aes128gcm 解开服务端发出的推送体，用来验证后端加密的载荷能被浏览器侧读懂。
func decryptWebPush(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("push body too short: %d", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	keyLen := int(body[20])
	if 21+keyLen > len(body) {
		t.Fatalf("invalid key length %d", keyLen)
	}
	asPubBytes := body[21 : 21+keyLen]
	ciphertext := body[21+keyLen:]

	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatalf("invalid server public key: %v", err)
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}

	info := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	info = append(info, asPubBytes...)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, string(info), 32)
	if err != nil {
		t.Fatalf("hkdf ikm: %v", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatalf("hkdf prk: %v", err)
	}
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if rs == 0 || len(plain) == 0 {
		t.Fatalf("unexpected record size %d / empty plaintext", rs)
	}
	// 最后一个记录带 0x02 分隔符，之后是填充。
	end := len(plain)
	for i := len(plain) - 1; i >= 0; i-- {
		if plain[i] == 0x02 {
			end = i
			break
		}
		if plain[i] != 0x00 {
			break
		}
	}
	return plain[:end]
}

// TestWebPushService_HubPushPayload 验证从 StreamHub 触发的推送载荷能被订阅方解密，字段与 service worker 期望一致。
func TestWebPushService_HubPushPayload(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ua key: %v", err)
	}
	authSecret := bytes.Repeat([]byte{7}, 16)
	uaB64 := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	authB64 := base64.RawURLEncoding.EncodeToString(authSecret)

	type received struct {
		headers http.Header
		plain   []byte
	}
	bodyCh := make(chan received, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- received{headers: r.Header.Clone(), plain: decryptWebPush(t, body, uaPriv, authSecret)}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	store := newPushTestStore(t)
	_ = store.Add(DeviceRecord{DeviceID: "dev-phone", Label: "iPhone", PairedAt: fixedPushTime, TokenHash: HashToken("tok")})
	_ = store.AddPushSubscription("dev-phone", PushSubscription{Endpoint: srv.URL + "/push", P256dh: uaB64, Auth: authB64, CreatedAt: fixedPushTime})

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:       filepath.Join(t.TempDir(), "vapid.pem"),
		Devices:       store,
		HTTPClient:    srv.Client(),
		Now:           func() time.Time { return fixedPushTime },
		ThreadWorkDir: func(threadID string) (string, bool) { return "/Users/me/code/demo", true },
	})
	if err != nil {
		t.Fatalf("NewWebPushService: %v", err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	svc.ObserveHub(hub)
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{ID: "t-1", Title: "构建任务", IsRunning: true})
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{ID: "t-1", Title: "构建任务", IsRunning: false})

	var got received
	select {
	case got = <-bodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for push")
	}
	if got.headers.Get("Content-Encoding") != "aes128gcm" {
		t.Fatalf("expected aes128gcm Content-Encoding, got %q", got.headers.Get("Content-Encoding"))
	}
	var payload PushPayload
	if err := json.Unmarshal(got.plain, &payload); err != nil {
		t.Fatalf("decrypted payload is not JSON: %v (%q)", err, got.plain)
	}
	if payload.ThreadID != "t-1" || payload.Title != "会话已完成" || payload.Body != "构建任务" || payload.WorkDir != "/Users/me/code/demo" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if !payload.Silent {
		t.Fatal("expected finished alert to be silent so the phone does not double-chime")
	}
}

// TestThreadsChangedListener_Chains 验证追加的会话列表监听不会覆盖已有钩子，注销后恢复。
func TestThreadsChangedListener_Chains(t *testing.T) {
	backend := NewMemoryBackend(NewStreamHub(StreamHubOptions{}))
	var first, second int
	backend.SetThreadsChangedHook(func(wire.ThreadsChanged) { first++ })
	remove := backend.AddThreadsChangedListener(func(wire.ThreadsChanged) { second++ })

	backend.mu.Lock()
	hook := backend.threadsChangedHook
	backend.mu.Unlock()
	hook(wire.ThreadsChanged{})
	if first != 1 || second != 1 {
		t.Fatalf("expected both hooks to run, got first=%d second=%d", first, second)
	}
	remove()
	hook = backend.threadsChangedHook
	hook(wire.ThreadsChanged{})
	if first != 2 || second != 1 {
		t.Fatalf("expected only the original hook after remove, got first=%d second=%d", first, second)
	}
}

func TestPushPayloadJSONOmitsEmptyTargets(t *testing.T) {
	plain, err := json.Marshal(PushPayload{Title: "完成", Body: "会话结束", ThreadID: "t-1"})
	if err != nil {
		t.Fatalf("marshal plain payload: %v", err)
	}
	for _, field := range []string{"messageID", "requestID", "answerable", "questionID", "answers"} {
		if strings.Contains(string(plain), `"`+field+`"`) {
			t.Fatalf("expected empty %s to be omitted, got %s", field, plain)
		}
	}

	targeted, err := json.Marshal(PushPayload{Title: "待审批", Body: "需要你确认", ThreadID: "t-1", MessageID: "msg-9", RequestID: "req-3"})
	if err != nil {
		t.Fatalf("marshal targeted payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(targeted, &decoded); err != nil {
		t.Fatalf("unmarshal targeted payload: %v", err)
	}
	if decoded["messageID"] != "msg-9" || decoded["requestID"] != "req-3" {
		t.Fatalf("expected messageID/requestID in payload, got %s", targeted)
	}
}

func TestPushRequestIDOnlyForSinglePendingRequest(t *testing.T) {
	single := wire.RequestsChangedParams{Requests: []wire.RemoteRequest{{RequestID: "req-1"}}}
	if got := pushRequestID("requests.changed", single); got != "req-1" {
		t.Fatalf("expected req-1, got %q", got)
	}
	multiple := wire.RequestsChangedParams{Requests: []wire.RemoteRequest{{RequestID: "req-1"}, {RequestID: "req-2"}}}
	if got := pushRequestID("requests.changed", multiple); got != "" {
		t.Fatalf("expected empty for multiple pending requests, got %q", got)
	}
	if got := pushRequestID("thread.summary", single); got != "" {
		t.Fatalf("expected empty for non-approval method, got %q", got)
	}
}

// newPushCapture 构造一台能解密推送载荷的测试手机，返回推送服务与该设备收到的明文。
func newPushCapture(t *testing.T) (*WebPushService, <-chan []byte) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ua key: %v", err)
	}
	authSecret := bytes.Repeat([]byte{9}, 16)
	plainCh := make(chan []byte, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		plainCh <- decryptWebPush(t, body, uaPriv, authSecret)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	store := newPushTestStore(t)
	_ = store.Add(DeviceRecord{DeviceID: "dev-phone", Label: "iPhone", PairedAt: fixedPushTime, TokenHash: HashToken("tok")})
	_ = store.AddPushSubscription("dev-phone", PushSubscription{
		Endpoint:  srv.URL + "/push",
		P256dh:    base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:      base64.RawURLEncoding.EncodeToString(authSecret),
		CreatedAt: fixedPushTime,
	})

	svc, err := NewWebPushService(WebPushOptions{
		KeyPath:    filepath.Join(t.TempDir(), "vapid.pem"),
		Devices:    store,
		HTTPClient: srv.Client(),
		Now:        func() time.Time { return fixedPushTime },
	})
	if err != nil {
		t.Fatalf("NewWebPushService: %v", err)
	}
	return svc, plainCh
}

// TestPushPayloadInlineAnswer 验证可作答的提问会把按钮写进推送载荷，字段形状与 service worker 约定一致。
func TestPushPayloadInlineAnswer(t *testing.T) {
	svc, plainCh := newPushCapture(t)
	req := answerableQuestionRequest()
	svc.HandleNotification(context.Background(), "t-1", "requests.changed", wire.RequestsChangedParams{
		ThreadID: "t-1",
		Requests: []wire.RemoteRequest{req},
	})

	var plain []byte
	select {
	case plain = <-plainCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for push")
	}

	var doc map[string]any
	if err := json.Unmarshal(plain, &doc); err != nil {
		t.Fatalf("decrypted payload is not JSON: %v (%q)", err, plain)
	}
	if doc["answerable"] != true || doc["questionID"] != "q-1" || doc["requestID"] != "req-1" {
		t.Fatalf("unexpected answerable payload: %q", plain)
	}
	answers, ok := doc["answers"].([]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected two answers, got %q", plain)
	}
	first, _ := answers[0].(map[string]any)
	if first["action"] != "ans0" || first["label"] != "允许" || first["value"] != "允许" {
		t.Fatalf("unexpected first answer: %v", first)
	}
	second, _ := answers[1].(map[string]any)
	if second["action"] != "ans1" || second["label"] != "拒绝" || second["value"] != "拒绝" {
		t.Fatalf("unexpected second answer: %v", second)
	}
}

// TestPushPayloadInlineAnswerOmitsButtons 验证不可作答的场景完全不出现 answerable/questionID/answers 字段。
func TestPushPayloadInlineAnswerOmitsButtons(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*wire.RemoteRequest)
	}{
		{"secret", func(r *wire.RemoteRequest) { r.Questions[0].IsSecret = true }},
		{"multi select", func(r *wire.RemoteRequest) { r.Questions[0].AllowsMultiple = true }},
		{"three choices", func(r *wire.RemoteRequest) {
			r.Questions[0].Choices = append(r.Questions[0].Choices, wire.RemoteQuestionChoice{Label: "其他"})
		}},
		{"two questions", func(r *wire.RemoteRequest) {
			r.Questions = append(r.Questions, wire.RemoteQuestion{ID: "q-2", Choices: r.Questions[0].Choices})
		}},
		{"command approval", func(r *wire.RemoteRequest) { r.Kind = string(harness.RequestKindCommand) }},
	}
	svc, plainCh := newPushCapture(t)
	for i, tc := range cases {
		threadID := "t-" + tc.name
		req := answerableQuestionRequest()
		req.ThreadID = threadID
		req.RequestID = "req-" + tc.name
		tc.mutate(&req)

		svc.HandleNotification(context.Background(), threadID, "requests.changed", wire.RequestsChangedParams{
			ThreadID: threadID,
			Requests: []wire.RemoteRequest{req},
		})

		var plain []byte
		select {
		case plain = <-plainCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("case %d (%s): timed out waiting for push", i, tc.name)
		}
		for _, field := range []string{"answerable", "questionID", "answers"} {
			if strings.Contains(string(plain), `"`+field+`"`) {
				t.Fatalf("case %s: expected %s to be omitted, got %q", tc.name, field, plain)
			}
		}
		if !strings.Contains(string(plain), `"requestID":"req-`+tc.name+`"`) {
			t.Fatalf("case %s: expected requestID to stay for deep link, got %q", tc.name, plain)
		}
	}
}

// TestFitPushPayloadBudget 验证三级降级：先丢答案按钮，再把正文截到 120 字，
// 仍超预算时把正文截到 60 字、标题截到 40 字；序列化后都不超过预算。
func TestFitPushPayloadBudget(t *testing.T) {
	longAnswer := strings.Repeat("审批", 800)
	answers := []PushAnswerOption{
		{Action: "ans0", Label: longAnswer, Value: longAnswer},
		{Action: "ans1", Label: longAnswer, Value: longAnswer},
	}

	cases := []struct {
		name         string
		payload      PushPayload
		wantBody     string // 非空时要求正文与之完全相等
		wantBodyMax  int    // 大于 0 时要求正文字符数不超过该上限
		wantTitle    string // 非空时要求标题与之完全相等
		wantTitleMax int    // 大于 0 时要求标题字符数不超过该上限
		wantAnswers  bool   // 真表示答案相关字段应保留
		wantSame     bool   // 真表示载荷应原样返回
	}{
		{
			name: "仅答案按钮超长时只丢答案",
			payload: PushPayload{
				Title:      "完成",
				Body:       "会话结束",
				ThreadID:   "t-1",
				RequestID:  "req-1",
				Answerable: true,
				QuestionID: "q-1",
				Answers:    answers,
			},
			wantBody:    "会话结束",
			wantTitle:   "完成",
			wantAnswers: false,
		},
		{
			name: "正文超长时把正文截到 120 字",
			payload: PushPayload{
				Title:      "完成",
				Body:       strings.Repeat("字", 4000),
				ThreadID:   "t-1",
				RequestID:  "req-1",
				Answerable: true,
				QuestionID: "q-1",
				Answers:    answers,
			},
			wantBodyMax: 120,
			wantTitle:   "完成",
			wantAnswers: false,
		},
		{
			name: "正文截到 120 字仍超限时继续截短正文和标题",
			payload: PushPayload{
				Title:      strings.Repeat("标题", 1000),
				Body:       strings.Repeat("正文", 2000),
				ThreadID:   "t-1",
				RequestID:  "req-1",
				Answerable: true,
				QuestionID: "q-1",
				Answers:    answers,
			},
			wantBodyMax:  60,
			wantTitleMax: 40,
			wantAnswers:  false,
		},
		{
			name: "未超预算的载荷原样返回",
			payload: PushPayload{
				Title:      "完成",
				Body:       "会话结束",
				ThreadID:   "t-1",
				RequestID:  "req-1",
				Answerable: true,
				QuestionID: "q-1",
				Answers: []PushAnswerOption{
					{Action: "ans0", Label: "继续", Value: "继续"},
					{Action: "ans1", Label: "暂停", Value: "暂停"},
				},
			},
			wantSame:    true,
			wantAnswers: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fitted := fitPushPayload(tc.payload)
			data, err := json.Marshal(fitted)
			if err != nil {
				t.Fatalf("marshal fitted payload: %v", err)
			}
			if len(data) > maxPushPayloadBytes {
				t.Fatalf("fitted payload = %d bytes, want <= %d", len(data), maxPushPayloadBytes)
			}
			if fitted.ThreadID != tc.payload.ThreadID {
				t.Fatalf("threadID = %q, want %q", fitted.ThreadID, tc.payload.ThreadID)
			}
			if fitted.RequestID != tc.payload.RequestID {
				t.Fatalf("requestID = %q, want %q", fitted.RequestID, tc.payload.RequestID)
			}
			gotAnswers := fitted.Answerable || fitted.QuestionID != "" || len(fitted.Answers) != 0
			if gotAnswers != tc.wantAnswers {
				t.Fatalf("answers kept = %v, want %v (fitted %+v)", gotAnswers, tc.wantAnswers, fitted)
			}
			if tc.wantBody != "" && fitted.Body != tc.wantBody {
				t.Fatalf("body = %q, want %q", fitted.Body, tc.wantBody)
			}
			if tc.wantBodyMax > 0 {
				if got := utf8.RuneCountInString(fitted.Body); got > tc.wantBodyMax {
					t.Fatalf("body length = %d, want <= %d", got, tc.wantBodyMax)
				}
			}
			if tc.wantTitle != "" && fitted.Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", fitted.Title, tc.wantTitle)
			}
			if tc.wantTitleMax > 0 {
				if got := utf8.RuneCountInString(fitted.Title); got > tc.wantTitleMax {
					t.Fatalf("title length = %d, want <= %d", got, tc.wantTitleMax)
				}
			}
			if tc.wantSame && !reflect.DeepEqual(fitted, tc.payload) {
				t.Fatalf("expected payload untouched, got %+v", fitted)
			}
		})
	}
}
