package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var ErrInvalidDeviceName = errors.New("remote: invalid device name")

// ErrPairingBlocked 表示该来源地址在窗口内配对失败过多，暂时拒绝配对。
var ErrPairingBlocked = errors.New("remote: too many pairing failures")

const (
	// maxPairingFailures 是同一来源地址在 pairingFailureWindow 内允许的配对失败次数。
	// 6 位码熵低，超过上限后封禁该地址 pairingBlockDuration，但不作废配对码：
	// 否则任意一个来源连发错码就能把用户正在用的码顶掉。
	maxPairingFailures   = 5
	pairingFailureWindow = 10 * time.Minute
	pairingBlockDuration = 5 * time.Minute
	// maxPairingFailureSources 限制记录表规模，避免被大量不同来源地址撑大内存。
	maxPairingFailureSources = 256
)

// pairingFailureState 记录单个来源地址在一个固定窗口内的失败次数与封禁到期时间。
type pairingFailureState struct {
	windowStart  time.Time
	count        int
	blockedUntil time.Time
}

// PairingAuthOptions 配置 PairingAuth 鉴权实例。
type PairingAuthOptions struct {
	DevicesPath string
	Now         func() time.Time
	Rand        io.Reader
	LANBaseURL  func() string
	TLSBaseURL  func() string
	// TailscaleBaseURL 只在 tailnet 入口已就绪时返回非空值，为空时主链接回落到 TLS、LAN。
	TailscaleBaseURL func() string
	OnRevoke         func(deviceID string)
}

// PairingAuth 实现 Authenticator 接口，负责管理短期一次性配对码与长期设备凭据。
type PairingAuth struct {
	mu               sync.Mutex
	store            *DeviceStore
	now              func() time.Time
	rand             io.Reader
	lanBaseURL       func() string
	tlsBaseURL       func() string
	tailscaleBaseURL func() string
	onRevoke         func(deviceID string)
	activeCode       string
	shortCode        string
	failures         map[string]*pairingFailureState
	codeExpiresAt    time.Time
	codeUsed         bool
}

// NewPairingAuth 创建 PairingAuth 实例并初始化配对码。
func NewPairingAuth(opts PairingAuthOptions) (*PairingAuth, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.Reader
	}

	store, err := NewDeviceStore(opts.DevicesPath, opts.Now)
	if err != nil {
		return nil, err
	}

	p := &PairingAuth{
		store:            store,
		now:              opts.Now,
		rand:             opts.Rand,
		lanBaseURL:       opts.LANBaseURL,
		tlsBaseURL:       opts.TLSBaseURL,
		tailscaleBaseURL: opts.TailscaleBaseURL,
		onRevoke:         opts.OnRevoke,
		failures:         make(map[string]*pairingFailureState),
	}

	// 启动时生成初始有效配对码
	if _, err := p.generateNewPairingCodeLocked(); err != nil {
		return nil, err
	}

	return p, nil
}

// Store 返回关联的设备存储。
func (p *PairingAuth) Store() *DeviceStore {
	return p.store
}

// Pair 校验短期配对码，单次有效并立即销毁，成功后颁发长期凭据与 UUID 设备标识。
func (p *PairingAuth) Pair(token, deviceName string, info ClientInfo) (PairResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	// 按来源地址限流：同一地址失败过多时封禁一段时间，不影响其他地址继续配对。
	if p.isBlockedLocked(info.Address, now) {
		return PairResult{}, ErrPairingBlocked
	}
	if p.activeCode == "" || !now.Before(p.codeExpiresAt) {
		return PairResult{}, ErrInvalidToken
	}

	// 常数时间比较防时序攻击：支持 64 位 token 或 6 位数字 shortCode
	matchActive := subtle.ConstantTimeCompare([]byte(p.activeCode), []byte(token)) == 1
	matchShort := len(token) == 6 && subtle.ConstantTimeCompare([]byte(p.shortCode), []byte(token)) == 1
	if !matchActive && !matchShort {
		p.recordFailureLocked(info.Address, now)
		return PairResult{}, ErrInvalidToken
	}

	// 配对码单次有效，验证通过后立即作废
	p.activeCode = ""
	p.shortCode = ""
	p.codeExpiresAt = time.Time{}
	p.codeUsed = true

	// 处理设备名称：去除前后空白，超过 64 字符截断
	name := strings.TrimSpace(deviceName)
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "Web Client"
	}

	sessionToken, err := generateToken(p.rand)
	if err != nil {
		return PairResult{}, err
	}

	devID, err := generateUUID(p.rand)
	if err != nil {
		return PairResult{}, err
	}

	tokenHash := HashToken(sessionToken)
	rec := DeviceRecord{
		DeviceID:    devID,
		Label:       name,
		PairedAt:    now,
		LastSeenAt:  now,
		TokenHash:   tokenHash,
		UserAgent:   info.UserAgent,
		LastAddress: info.Address,
		LastEntry:   string(info.Entry),
	}

	if err := p.store.Add(rec); err != nil {
		return PairResult{}, err
	}

	return PairResult{
		SessionToken: sessionToken,
		DeviceID:     devID,
		Label:        name,
	}, nil
}

// Authenticate 根据客户端上报的 sessionToken 计算 SHA-256 查验身份并更新活跃时间。
func (p *PairingAuth) Authenticate(token string, info ClientInfo) (deviceID, label string, err error) {
	if token == "" {
		return "", "", ErrInvalidToken
	}

	tokenHash := HashToken(token)
	rec, ok := p.store.FindByTokenHash(tokenHash)
	if !ok {
		return "", "", ErrInvalidToken
	}

	_ = p.store.Touch(tokenHash, info)
	return rec.DeviceID, rec.Label, nil
}

// NewPairingCode 生成新的 32 字节高熵一次性配对码与 6 位数字授权码，5 分钟有效，不影响既有连接。
func (p *PairingAuth) NewPairingCode() (PairingInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.generateNewPairingCodeLocked()
}

// CurrentPairingURLs 获取当前有效配对地址；若已过期则自动换发新码。
func (p *PairingAuth) CurrentPairingURLs() PairingInfo {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if p.activeCode == "" || !now.Before(p.codeExpiresAt) {
		info, _ := p.generateNewPairingCodeLocked()
		return info
	}

	return p.makePairingInfo(p.activeCode, p.shortCode, p.codeExpiresAt)
}

// RevokeDevice 撤销指定设备并触发 OnRevoke 回调以关闭连接与清理推送。
func (p *PairingAuth) RevokeDevice(deviceID string) (revoked bool, err error) {
	revoked, err = p.store.Remove(deviceID)
	if revoked && p.onRevoke != nil {
		p.onRevoke(deviceID)
	}
	return revoked, err
}

// RenameDevice 更新指定设备的显示名称。
func (p *PairingAuth) RenameDevice(deviceID, name string) error {
	name = strings.TrimSpace(name)
	// 按字符（rune）截断，避免中文等多字节名称被截断成半个字符
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	if name == "" {
		return ErrInvalidDeviceName
	}
	return p.store.Rename(deviceID, name)
}

// RevokeAllDevices 撤销全部已配对设备，并对每台设备触发 OnRevoke 回调。
func (p *PairingAuth) RevokeAllDevices() error {
	records := p.store.AllRecords()
	var firstErr error
	for _, rec := range records {
		revoked, err := p.store.Remove(rec.DeviceID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if revoked && p.onRevoke != nil {
			p.onRevoke(rec.DeviceID)
		}
	}
	return firstErr
}

// PairingState 返回当前配对码状态（active、expired、used、unavailable），不会自动换码。
func (p *PairingAuth) PairingState() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.hasBaseURLLocked() {
		return "unavailable"
	}

	if p.activeCode != "" {
		if p.now().Before(p.codeExpiresAt) {
			return "active"
		}
		return "expired"
	}
	if p.codeUsed {
		return "used"
	}
	return "expired"
}

func (p *PairingAuth) hasBaseURLLocked() bool {
	if p.tailscaleBaseURL != nil && strings.TrimSpace(p.tailscaleBaseURL()) != "" {
		return true
	}
	if p.lanBaseURL != nil && strings.TrimSpace(p.lanBaseURL()) != "" {
		return true
	}
	if p.tlsBaseURL != nil && strings.TrimSpace(p.tlsBaseURL()) != "" {
		return true
	}
	return false
}

// isBlockedLocked 判断来源地址是否处于配对封禁期。封禁到期后丢弃记录，让该地址重新开始计数。
func (p *PairingAuth) isBlockedLocked(address string, now time.Time) bool {
	st, ok := p.failures[address]
	if !ok {
		return false
	}
	if st.blockedUntil.IsZero() {
		return false
	}
	if now.Before(st.blockedUntil) {
		return true
	}
	delete(p.failures, address)
	return false
}

// recordFailureLocked 记一次配对失败；同一地址在窗口内失败达到上限时封禁一段时间。
func (p *PairingAuth) recordFailureLocked(address string, now time.Time) {
	st, ok := p.failures[address]
	if !ok || now.Sub(st.windowStart) > pairingFailureWindow {
		st = &pairingFailureState{windowStart: now}
		p.failures[address] = st
	}
	st.count++
	if st.count >= maxPairingFailures {
		st.blockedUntil = now.Add(pairingBlockDuration)
	}

	if len(p.failures) > maxPairingFailureSources {
		for addr, item := range p.failures {
			if now.Sub(item.windowStart) > pairingFailureWindow && !now.Before(item.blockedUntil) {
				delete(p.failures, addr)
			}
		}
	}
}

func (p *PairingAuth) generateNewPairingCodeLocked() (PairingInfo, error) {
	code, err := generateToken(p.rand)
	if err != nil {
		return PairingInfo{}, err
	}
	shortCode, err := generateShortCode(p.rand)
	if err != nil {
		return PairingInfo{}, err
	}

	expiresAt := p.now().Add(5 * time.Minute)
	p.activeCode = code
	p.shortCode = shortCode
	p.codeExpiresAt = expiresAt
	p.codeUsed = false

	return p.makePairingInfo(code, shortCode, expiresAt), nil
}

func (p *PairingAuth) makePairingInfo(code, shortCode string, expiresAt time.Time) PairingInfo {
	var lanURL, tlsURL, tailscaleURL string
	if p.lanBaseURL != nil {
		base := strings.TrimRight(p.lanBaseURL(), "/")
		if base != "" {
			lanURL = fmt.Sprintf("%s/#pair=%s", base, code)
		}
	}
	if p.tlsBaseURL != nil {
		base := strings.TrimRight(p.tlsBaseURL(), "/")
		if base != "" {
			tlsURL = fmt.Sprintf("%s/#pair=%s", base, code)
		}
	}
	if p.tailscaleBaseURL != nil {
		base := strings.TrimRight(p.tailscaleBaseURL(), "/")
		if base != "" {
			tailscaleURL = fmt.Sprintf("%s/#pair=%s", base, code)
		}
	}
	// 主链接（同时是二维码内容）优先级：Tailscale > TLS > LAN。
	mainURL := tailscaleURL
	if mainURL == "" {
		mainURL = tlsURL
	}
	if mainURL == "" {
		mainURL = lanURL
	}

	return PairingInfo{
		PairingURL:          mainURL,
		LANPairingURL:       lanURL,
		TLSPairingURL:       tlsURL,
		TailscalePairingURL: tailscaleURL,
		ShortCode:           shortCode,
		ExpiresAt:           expiresAt,
	}
}

func generateToken(r io.Reader) (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func generateUUID(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func generateShortCode(r io.Reader) (string, error) {
	var b [4]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		val := binary.BigEndian.Uint32(b[:])
		if val < 4294000000 {
			return fmt.Sprintf("%06d", val%1000000), nil
		}
	}
}
