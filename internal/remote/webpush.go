package remote

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

// loadOrCreateVAPIDKey 加载或新建 VAPID P-256 私钥，PEM 文件权限 0600，先写临时文件再 rename。
func loadOrCreateVAPIDKey(path string) (*ecdsa.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("remote: invalid VAPID PEM block")
		}
		if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
			return key, nil
		}
		if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if ecKey, ok := key.(*ecdsa.PrivateKey); ok {
				return ecKey, nil
			}
		}
		return nil, errors.New("remote: unparseable stored VAPID key")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("remote: failed to generate VAPID key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("remote: failed to marshal VAPID key: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	})

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	tmpFile := filepath.Join(dir, fmt.Sprintf(".tmp_%d_%s", time.Now().UnixNano(), filepath.Base(path)))
	if err := os.WriteFile(tmpFile, pemBytes, 0600); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmpFile, 0600); err != nil {
		_ = os.Remove(tmpFile)
		return nil, err
	}
	if err := os.Rename(tmpFile, path); err != nil {
		_ = os.Remove(tmpFile)
		return nil, err
	}

	return key, nil
}

// encryptWebPush 严格按 RFC 8291 §3.3/§3.4 与 RFC 8188 aes128gcm 实现加密。
func encryptWebPush(plaintext []byte, uaPublic []byte, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte, recordSize uint32) ([]byte, error) {
	if len(uaPublic) != 65 {
		return nil, errors.New("uaPublic must be 65 bytes")
	}
	if len(authSecret) != 16 {
		return nil, errors.New("authSecret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("salt must be 16 bytes")
	}
	if asPriv == nil {
		return nil, errors.New("asPriv is required")
	}
	if recordSize == 0 {
		recordSize = 4096
	}

	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("invalid uaPublic: %w", err)
	}

	ecdhSecret, err := asPriv.ECDH(uaKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh failed: %w", err)
	}

	asPublic := asPriv.PublicKey().Bytes()

	// IKM = HKDF(salt=auth_secret, ikm=ecdh_secret, info="WebPush: info\x00"‖ua_public‖as_public, L=32)
	keyInfo := make([]byte, 0, 14+len(uaPublic)+len(asPublic))
	keyInfo = append(keyInfo, []byte("WebPush: info\x00")...)
	keyInfo = append(keyInfo, uaPublic...)
	keyInfo = append(keyInfo, asPublic...)

	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, fmt.Errorf("hkdf ikm failed: %w", err)
	}

	// PRK = HKDF-Extract(salt, IKM)
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("hkdf prk failed: %w", err)
	}

	// CEK = HKDF-Expand(PRK, "Content-Encoding: aes128gcm\x00", 16)
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("hkdf cek failed: %w", err)
	}

	// NONCE = HKDF-Expand(PRK, "Content-Encoding: nonce\x00", 12)
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("hkdf nonce failed: %w", err)
	}

	// 明文后追加 0x02 分隔符
	padded := make([]byte, len(plaintext)+1)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x02

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	// 输出头为 salt(16)‖rs(4 字节大端)‖idlen(1)=65‖as_public(65)
	header := make([]byte, 16+4+1+len(asPublic))
	copy(header[0:16], salt)
	binary.BigEndian.PutUint32(header[16:20], recordSize)
	header[20] = byte(len(asPublic))
	copy(header[21:], asPublic)

	return append(header, ciphertext...), nil
}

// EncryptWebPush 导出包装函数，随机生成临时密钥和 salt。
func EncryptWebPush(plaintext []byte, p256dhB64, authB64 string) ([]byte, error) {
	uaPublic, err := base64.RawURLEncoding.DecodeString(p256dhB64)
	if err != nil || len(uaPublic) != 65 {
		return nil, errors.New("remote: invalid p256dh public key")
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(authB64)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("remote: invalid auth secret")
	}

	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	return encryptWebPush(plaintext, uaPublic, authSecret, asPriv, salt, 4096)
}

// allowedPushHosts 是允许的推送端点白名单：只接受已知推送服务的 https 端点。
var allowedPushHosts = []string{
	"web.push.apple.com",
	"fcm.googleapis.com",
	"updates.push.services.mozilla.com",
	"notify.windows.com",
}

const (
	// defaultVAPIDSubject 是 VAPID sub 在未配置域名时的默认值（本机回环域名）。
	defaultVAPIDSubject = "https://mcode.local"
	// defaultPushMinInterval 是同一会话同类提醒的默认节流间隔。
	defaultPushMinInterval = 30 * time.Second
)

// ParsePushEndpoint 校验推送端点：必须是 https，且主机属于已知推送服务（或其子域）。
func ParsePushEndpoint(endpoint string) (*url.URL, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowedPushHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return u, true
		}
	}
	return nil, false
}

func vapidAuthorizationHeader(vapidKey *ecdsa.PrivateKey, publicKeyB64 string, subject string, endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("remote: invalid endpoint URL")
	}

	audHost := u.Hostname()
	port := u.Port()
	if port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		audHost = audHost + ":" + port
	}
	aud := u.Scheme + "://" + audHost

	exp := now.Add(12 * time.Hour).Unix()
	claimsJSON := fmt.Sprintf(`{"aud":%q,"exp":%d,"sub":%q}`, aud, exp, subject)
	headerJSON := `{"typ":"JWT","alg":"ES256"}`

	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))
	signingInput := headerB64 + "." + claimsB64

	h := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, vapidKey, h[:])
	if err != nil {
		return "", fmt.Errorf("remote: VAPID signing failed: %w", err)
	}

	rBytes := r.Bytes()
	sBytes := s.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	jwt := signingInput + "." + sigB64

	return fmt.Sprintf("vapid t=%s, k=%s", jwt, publicKeyB64), nil
}

// WebPushOptions 包含 WebPushService 的配置选项。
type WebPushOptions struct {
	KeyPath    string
	Devices    *DeviceStore
	HTTPClient *http.Client
	Now        func() time.Time
	// VAPIDSubject 为 VAPID 的 sub 声明，需为 mailto: 或 https: URL；为空时默认 "https://mcode.local"。
	VAPIDSubject string
	// SkipOnline 返回该设备的浏览器当前是否在线（已建立并订阅会话的远程连接）。
	// 在线设备的页面能自己刷新和提示，推送只发给离线设备，避免手机上重复两条。为 nil 时一律推送。
	SkipOnline func(deviceID string) bool
	// ThreadWorkDir 返回会话工作目录，用于让浏览器判断自己是否正停在同一个仓库上。
	ThreadWorkDir func(threadID string) (string, bool)
	// MinInterval 为同一会话同类提醒之间的最小间隔（节流）；<=0 时取 30 秒。
	MinInterval time.Duration
}

// PushPayload 对应推送通知的业务载荷，写进 service worker 的 JSON 明文。
type PushPayload struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	ThreadID string `json:"threadID"`
	Silent   bool   `json:"silent"`
	// WorkDir 为会话工作目录，浏览器据此判断自己是否正停在同一仓库上。
	WorkDir string `json:"workDir,omitempty"`
	// MessageID 为触发推送的那条消息，点开通知时前端据此定位到具体消息；拿不到时留空。
	MessageID string `json:"messageID,omitempty"`
	// RequestID 为待处理审批请求，点开通知时前端据此定位到审批卡；拿不到时留空。
	RequestID string `json:"requestID,omitempty"`
	// Answerable 为真时，通知可直接用 answers 里的按钮作答；缺省不序列化。
	Answerable bool `json:"answerable,omitempty"`
	// QuestionID 为可内联作答的问题 id，仅 Answerable 为真时出现。
	QuestionID string `json:"questionID,omitempty"`
	// Answers 为通知里的一键作答按钮，仅 Answerable 为真时出现。
	Answers []PushAnswerOption `json:"answers,omitempty"`
}

// maxPushPayloadBytes 是推送明文的硬预算。加密记录上限 4096 字节，
// 扣掉头和认证标签后仍有余量；超限时先丢按钮再截断正文。
const maxPushPayloadBytes = 3500

// fitPushPayload 保证载荷序列化后不超过 maxPushPayloadBytes。
// 降级顺序：先清掉答案按钮（保留「打开」），再截断正文到 120 字符，
// 仍超限则正文截到 60、标题截到 40 字符。
func fitPushPayload(p PushPayload) PushPayload {
	if pushPayloadSize(p) <= maxPushPayloadBytes {
		return p
	}
	p.Answerable = false
	p.QuestionID = ""
	p.Answers = nil
	if pushPayloadSize(p) <= maxPushPayloadBytes {
		return p
	}
	p.Body = truncateRunes(p.Body, 120)
	if pushPayloadSize(p) <= maxPushPayloadBytes {
		return p
	}
	p.Body = truncateRunes(p.Body, 60)
	p.Title = truncateRunes(p.Title, 40)
	return p
}

// pushPayloadSize 返回载荷序列化后的字节数；序列化失败按超限处理，避免发出坏载荷。
func pushPayloadSize(p PushPayload) int {
	data, err := json.Marshal(p)
	if err != nil {
		return maxPushPayloadBytes + 1
	}
	return len(data)
}

// pushTarget 是一次待发送的订阅及其所属设备。
type pushTarget struct {
	deviceID string
	sub      PushSubscription
}

// WebPushService 实现基于 RFC 8291 与 VAPID 的离线推送服务。
type WebPushService struct {
	vapidKey      *ecdsa.PrivateKey
	publicKeyB64  string
	subject       string
	devices       *DeviceStore
	httpClient    *http.Client
	now           func() time.Time
	skipOnline    func(deviceID string) bool
	threadWorkDir func(threadID string) (string, bool)
	minInterval   time.Duration

	detector *SessionAlertDetector

	mu       sync.Mutex
	lastSent map[string]time.Time
}

// NewWebPushService 创建并初始化 WebPush 服务实例。
func NewWebPushService(opts WebPushOptions) (*WebPushService, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Devices == nil {
		return nil, errors.New("remote: DeviceStore is required for WebPushService")
	}
	subject := opts.VAPIDSubject
	if subject == "" {
		subject = defaultVAPIDSubject
	}
	if !strings.HasPrefix(subject, "mailto:") && !strings.HasPrefix(subject, "https:") {
		return nil, errors.New("remote: VAPID subject must be a mailto: or https: URL")
	}
	minInterval := opts.MinInterval
	if minInterval <= 0 {
		minInterval = defaultPushMinInterval
	}

	key, err := loadOrCreateVAPIDKey(opts.KeyPath)
	if err != nil {
		return nil, err
	}

	ecdhPriv, err := key.ECDH()
	if err != nil {
		return nil, fmt.Errorf("remote: invalid VAPID ECDH key: %w", err)
	}
	pubB64 := base64.RawURLEncoding.EncodeToString(ecdhPriv.PublicKey().Bytes())

	return &WebPushService{
		vapidKey:      key,
		publicKeyB64:  pubB64,
		subject:       subject,
		devices:       opts.Devices,
		httpClient:    opts.HTTPClient,
		now:           opts.Now,
		skipOnline:    opts.SkipOnline,
		threadWorkDir: opts.ThreadWorkDir,
		minInterval:   minInterval,
		detector:      NewSessionAlertDetector(),
		lastSent:      make(map[string]time.Time),
	}, nil
}

// PublicKey 返回 Base64URL 编码的 65 字节未压缩 VAPID 公钥。
func (s *WebPushService) PublicKey() string {
	return s.publicKeyB64
}

// SubscriptionCount 返回当前保存的推送订阅总数。
func (s *WebPushService) SubscriptionCount() int {
	return s.devices.PushSubscriptionCount()
}

// Subscribe 注册 WebPush 订阅。
func (s *WebPushService) Subscribe(deviceID string, endpoint, p256dh, auth string) error {
	if deviceID == "" {
		return &wire.RPCError{Code: wire.InvalidParams, Message: "Invalid params: authenticated device required"}
	}
	endpoint = strings.TrimSpace(endpoint)
	if _, ok := ParsePushEndpoint(endpoint); !ok {
		return &wire.RPCError{Code: wire.InvalidParams, Message: "Invalid params: endpoint must be an https URL of a known push service"}
	}
	p256dh = strings.TrimSpace(p256dh)
	auth = strings.TrimSpace(auth)
	uaPub, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil || len(uaPub) != 65 {
		return &wire.RPCError{Code: wire.InvalidParams, Message: "Invalid params: p256dh must be 65 bytes"}
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil || len(authSecret) != 16 {
		return &wire.RPCError{Code: wire.InvalidParams, Message: "Invalid params: auth must be 16 bytes"}
	}

	sub := PushSubscription{
		Endpoint:  endpoint,
		P256dh:    p256dh,
		Auth:      auth,
		CreatedAt: s.now(),
	}
	return s.devices.AddPushSubscription(deviceID, sub)
}

// Unsubscribe 注销指定设备的推送订阅端点。
func (s *WebPushService) Unsubscribe(deviceID, endpoint string) (bool, error) {
	return s.devices.RemovePushSubscription(deviceID, strings.TrimSpace(endpoint))
}

// ForgetDevice 撤销设备时调用，丢弃该设备名下全部订阅，之后不再向它推送。
func (s *WebPushService) ForgetDevice(deviceID string) {
	if deviceID == "" {
		return
	}
	rec, ok := s.devices.FindByDeviceID(deviceID)
	if !ok {
		return
	}
	for _, sub := range rec.PushSubscriptions {
		_, _ = s.devices.RemovePushSubscription(deviceID, sub.Endpoint)
	}
}

// ForgetAllDevices 撤销全部设备时调用，清空所有订阅。
func (s *WebPushService) ForgetAllDevices() {
	_, _ = s.devices.RemoveAllPushSubscriptions()
}

// Send 向所有已订阅且离线的设备发送推送通知，异步非阻塞分发。
func (s *WebPushService) Send(ctx context.Context, payload PushPayload) {
	if ctx == nil {
		ctx = context.Background()
	}
	subs := s.devices.AllPushSubscriptions()
	if len(subs) == 0 {
		return
	}

	payload = fitPushPayload(payload)
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return
	}

	targets := make([]pushTarget, 0, len(subs))
	for _, sub := range subs {
		deviceID, ok := s.devices.FindDeviceByPushEndpoint(sub.Endpoint)
		if ok && s.skipOnline != nil && s.skipOnline(deviceID) {
			continue
		}
		targets = append(targets, pushTarget{deviceID: deviceID, sub: sub})
	}
	if len(targets) == 0 {
		return
	}

	go func() {
		for _, t := range targets {
			s.sendToSubscription(t, plaintext)
		}
	}()
}

func (s *WebPushService) sendToSubscription(target pushTarget, plaintext []byte) {
	sub := target.sub
	encrypted, err := EncryptWebPush(plaintext, sub.P256dh, sub.Auth)
	if err != nil {
		return
	}

	authHeader, err := vapidAuthorizationHeader(s.vapidKey, s.publicKeyB64, s.subject, sub.Endpoint, s.now())
	if err != nil {
		return
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.Endpoint, bytes.NewReader(encrypted))
	if err != nil {
		return
	}

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	// 推送服务回 404/410 表示订阅已失效：删掉它，别再往死端点重试。
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		_, _ = s.devices.RemovePushSubscription(target.deviceID, sub.Endpoint)
	}
}

// SendTest 向所有已订阅设备发送一条测试通知，同步等待发送结束并返回发送端点数量。
func (s *WebPushService) SendTest(ctx context.Context, title, body string) (int, error) {
	subs := s.devices.AllPushSubscriptions()
	if len(subs) == 0 {
		return 0, nil
	}
	plaintext, err := json.Marshal(PushPayload{Title: title, Body: body})
	if err != nil {
		return 0, err
	}
	for _, sub := range subs {
		deviceID, _ := s.devices.FindDeviceByPushEndpoint(sub.Endpoint)
		s.sendToSubscription(pushTarget{deviceID: deviceID, sub: sub}, plaintext)
	}
	return len(subs), nil
}

// ObserveHub 挂载 StreamHub 观察者回调，自动响应审批新增与会话结束。
func (s *WebPushService) ObserveHub(hub *StreamHub) {
	if hub == nil {
		return
	}
	hub.AddObserver(func(threadID, method string, params any) {
		s.HandleNotification(context.Background(), threadID, method, params)
	})
}

// HandleThreadsChanged 处理会话列表变动：出错结束的会话也发一条。
func (s *WebPushService) HandleThreadsChanged(threads []wire.ThreadSummary) {
	for _, th := range threads {
		if th.ID == "" {
			continue
		}
		s.HandleNotification(context.Background(), th.ID, "thread.summary", th)
	}
}

// HandleNotification 消费一条会话事件；满足提醒条件且未被节流时发送推送。
func (s *WebPushService) HandleNotification(ctx context.Context, threadID, method string, params any) {
	alert, ok := s.detector.Observe(threadID, method, params)
	if !ok {
		return
	}
	if !s.allowSend(alert) {
		return
	}
	payload := PushPayload{
		Title:    alert.Title,
		Body:     alert.Body,
		ThreadID: alert.ThreadID,
		// “完成”带提示音、“出错”静音；桌面端已按同样的声音规则响铃，推送侧对应为静音。
		Silent:    alert.Kind != SessionAlertApproval,
		RequestID: pushRequestID(method, params),
	}
	// 深链与按钮分开判定：RequestID 管点开定位，Answerable 管通知内直接作答。
	if target := inlineAnswerTarget(method, params); target != nil {
		payload.Answerable = true
		payload.QuestionID = target.QuestionID
		payload.Answers = target.Answers
	}
	if s.threadWorkDir != nil {
		if dir, ok := s.threadWorkDir(alert.ThreadID); ok {
			payload.WorkDir = dir
		}
	}
	s.Send(ctx, payload)
}

// pushRequestID 取审批事件里唯一待处理请求的 id。多条待处理时 map 遍历顺序不可辨，
// 无法判断哪一条是本次新增，宁可留空也不高亮错卡。
func pushRequestID(method string, params any) string {
	if method != "requests.changed" {
		return ""
	}
	switch value := params.(type) {
	case wire.RequestsChangedParams:
		if len(value.Requests) == 1 {
			return value.Requests[0].RequestID
		}
	case *wire.RequestsChangedParams:
		if value != nil && len(value.Requests) == 1 {
			return value.Requests[0].RequestID
		}
	case map[string]any:
		// JSON 解码后的表单（转发或测试路径）：同样只认唯一一条。
		requests, _ := value["requests"].([]any)
		if len(requests) != 1 {
			return ""
		}
		request, _ := requests[0].(map[string]any)
		id, _ := request["requestID"].(string)
		return id
	}
	return ""
}

// allowSend 节流：同一会话同类提醒在 MinInterval 内的重复提醒直接丢弃。
func (s *WebPushService) allowSend(alert SessionAlert) bool {
	key := string(alert.Kind) + ":" + alert.ThreadID
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[key]; ok && now.Sub(last) < s.minInterval {
		return false
	}
	s.lastSent[key] = now
	return true
}
