package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	defaultIdleExpiration = 30 * 24 * time.Hour
	defaultWriteInterval  = 60 * time.Second
)

var ErrDeviceNotFound = errors.New("remote: device not found")

// PushSubscription 表示配对设备关联的单个 WebPush 推送订阅。
type PushSubscription struct {
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	CreatedAt time.Time `json:"createdAt"`
}

// DeviceRecord 表示持久化至磁盘的单个配对设备信息。
type DeviceRecord struct {
	DeviceID          string             `json:"deviceID"`
	Label             string             `json:"label"`
	PairedAt          time.Time          `json:"pairedAt"`
	LastSeenAt        time.Time          `json:"lastSeenAt"`
	TokenHash         string             `json:"tokenHash"`
	UserAgent         string             `json:"userAgent,omitempty"`
	LastAddress       string             `json:"lastAddress,omitempty"`
	LastEntry         string             `json:"lastEntry,omitempty"`
	PushSubscriptions []PushSubscription `json:"pushSubscriptions,omitempty"`
}

// DeviceStore 管理配对设备持久化记录文件。
type DeviceStore struct {
	mu              sync.RWMutex
	filePath        string
	records         []DeviceRecord
	now             func() time.Time
	lastWrite       time.Time
	hasPendingFlush bool
}

// HashToken 计算 sessionToken 的小写十六进制 SHA-256 摘要。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewDeviceStore 创建设备存储实例。若文件已存在则加载并清理超过 30 天未使用的闲置记录。
func NewDeviceStore(filePath string, now func() time.Time) (*DeviceStore, error) {
	if now == nil {
		now = time.Now
	}
	s := &DeviceStore{
		filePath: filePath,
		now:      now,
	}

	data, err := os.ReadFile(filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remote: read device store %s: %w", filePath, err)
	}
	if err == nil {
		var list []DeviceRecord
		if err := json.Unmarshal(data, &list); err != nil {
			backupPath := fmt.Sprintf("%s.corrupt-%d", filePath, time.Now().UnixNano())
			if renameErr := os.Rename(filePath, backupPath); renameErr != nil {
				return nil, fmt.Errorf("remote: backup corrupt device store %s: %w", filePath, renameErr)
			}
			log.Printf("远程设备文件损坏，已备份至 %s：%v", backupPath, err)
		} else {
			s.records = list
		}
	}

	if s.removeExpiredLocked(now()) {
		_ = s.writeLocked(now())
	}

	return s, nil
}

// Add 添加新配对设备记录并立即原子落盘。
func (s *DeviceStore) Add(rec DeviceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = append(s.records, rec)
	return s.writeLocked(s.now())
}

// FindByTokenHash 根据 tokenHash 查找设备记录。查找前先清理超过 30 天未使用的记录。
func (s *DeviceStore) FindByTokenHash(tokenHash string) (DeviceRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	for _, r := range s.records {
		if r.TokenHash == tokenHash {
			return r, true
		}
	}
	return DeviceRecord{}, false
}

// FindByDeviceID 根据 deviceID 查找设备记录。
func (s *DeviceStore) FindByDeviceID(deviceID string) (DeviceRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	for _, r := range s.records {
		if r.DeviceID == deviceID {
			return r, true
		}
	}
	return DeviceRecord{}, false
}

// Remove 根据 deviceID 撤销并移除设备记录，若找到则立即落盘。
func (s *DeviceStore) Remove(deviceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, r := range s.records {
		if r.DeviceID == deviceID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, nil
	}

	s.records = append(s.records[:idx], s.records[idx+1:]...)
	if err := s.writeLocked(s.now()); err != nil {
		return true, err
	}
	return true, nil
}

// Rename 更新指定设备的显示名称并立即落盘。
func (s *DeviceStore) Rename(deviceID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	for i := range s.records {
		if s.records[i].DeviceID == deviceID {
			s.records[i].Label = name
			return s.writeLocked(s.now())
		}
	}
	return ErrDeviceNotFound
}

// Touch 记录已认证设备的在线活跃状态，内存实时更新；写盘按 60 秒节流。
func (s *DeviceStore) Touch(tokenHash string, info ClientInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	idx := -1
	for i, r := range s.records {
		if r.TokenHash == tokenHash {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrInvalidToken
	}

	now := s.now()
	rec := &s.records[idx]
	if now.After(rec.LastSeenAt) {
		rec.LastSeenAt = now
	}

	if info.UserAgent != "" {
		ua := info.UserAgent
		if len(ua) > 256 {
			ua = ua[:256]
		}
		rec.UserAgent = ua
	}
	if info.Address != "" {
		rec.LastAddress = info.Address
	}
	if info.Entry != "" {
		rec.LastEntry = string(info.Entry)
	}

	if now.Sub(s.lastWrite) >= defaultWriteInterval {
		return s.writeLocked(now)
	}

	s.hasPendingFlush = true
	return nil
}

// Clear 清空所有设备记录并立即落盘。
func (s *DeviceStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = nil
	return s.writeLocked(s.now())
}

// Flush 将内存中因节流未落盘的更新（最新活跃时间、设备名与地址）立即写入磁盘。
// 服务停止与进程退出前需要显式调用，否则最近 60 秒内的活跃记录可能丢失。
// 没有待落盘的更新时直接返回 nil。
func (s *DeviceStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.hasPendingFlush {
		return s.writeLocked(s.now())
	}
	return nil
}

// AllRecords 返回当前所有未失效设备记录副本，按配对时间升序排序。
func (s *DeviceStore) AllRecords() []DeviceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	res := make([]DeviceRecord, len(s.records))
	copy(res, s.records)
	sort.Slice(res, func(i, j int) bool {
		return res[i].PairedAt.Before(res[j].PairedAt)
	})
	return res
}

// AddPushSubscription 为指定设备添加或更新 WebPush 订阅。
// 按 endpoint 去重，若同一 endpoint 存在于其他设备上则先将其移除；
// 每台设备最多保留 5 个订阅，超出时按 CreatedAt 升序淘汰最旧的订阅；
// 修改完成后立即原子落盘。
func (s *DeviceStore) AddPushSubscription(deviceID string, sub PushSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	// 若 endpoint 属于其他设备，先从那个设备上移除
	for i := range s.records {
		if s.records[i].DeviceID != deviceID {
			var filtered []PushSubscription
			for _, existing := range s.records[i].PushSubscriptions {
				if existing.Endpoint != sub.Endpoint {
					filtered = append(filtered, existing)
				}
			}
			s.records[i].PushSubscriptions = filtered
		}
	}

	// 查找目标设备
	targetIdx := -1
	for i := range s.records {
		if s.records[i].DeviceID == deviceID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return ErrDeviceNotFound
	}

	subs := s.records[targetIdx].PushSubscriptions
	found := false
	for i := range subs {
		if subs[i].Endpoint == sub.Endpoint {
			subs[i] = sub
			found = true
			break
		}
	}
	if !found {
		subs = append(subs, sub)
	}

	// 每台设备最多保留 5 个，超出时按 CreatedAt 淘汰最旧的
	const maxSubsPerDevice = 5
	if len(subs) > maxSubsPerDevice {
		sort.Slice(subs, func(i, j int) bool {
			return subs[i].CreatedAt.Before(subs[j].CreatedAt)
		})
		excess := len(subs) - maxSubsPerDevice
		subs = subs[excess:]
	}

	s.records[targetIdx].PushSubscriptions = subs
	return s.writeLocked(s.now())
}

// RemovePushSubscription 移除指定设备的推送订阅（若 deviceID 为空，则在所有设备中查找 endpoint 移除）。
// 若存在并成功移除则立即落盘并返回 true，否则返回 false。
func (s *DeviceStore) RemovePushSubscription(deviceID, endpoint string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	removed := false
	for i := range s.records {
		if deviceID != "" && s.records[i].DeviceID != deviceID {
			continue
		}
		var filtered []PushSubscription
		for _, sub := range s.records[i].PushSubscriptions {
			if sub.Endpoint == endpoint {
				removed = true
			} else {
				filtered = append(filtered, sub)
			}
		}
		s.records[i].PushSubscriptions = filtered
	}

	if !removed {
		return false, nil
	}
	if err := s.writeLocked(s.now()); err != nil {
		return true, err
	}
	return true, nil
}

// FindDeviceByPushEndpoint 返回持有该推送端点的设备 ID。
func (s *DeviceStore) FindDeviceByPushEndpoint(endpoint string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rec := range s.records {
		for _, sub := range rec.PushSubscriptions {
			if sub.Endpoint == endpoint {
				return rec.DeviceID, true
			}
		}
	}
	return "", false
}

// RemoveAllPushSubscriptions 清空全部设备的推送订阅（撤销所有设备时使用），有变更才落盘。
func (s *DeviceStore) RemoveAllPushSubscriptions() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.records {
		if len(s.records[i].PushSubscriptions) > 0 {
			s.records[i].PushSubscriptions = nil
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if err := s.writeLocked(s.now()); err != nil {
		return true, err
	}
	return true, nil
}

// PushSubscriptionCount 返回全部未失效设备持有的推送订阅总数。
func (s *DeviceStore) PushSubscriptionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, rec := range s.records {
		count += len(rec.PushSubscriptions)
	}
	return count
}

// AllPushSubscriptions 返回当前所有未失效设备的所有有效推送订阅副本。
func (s *DeviceStore) AllPushSubscriptions() []PushSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.removeExpiredLocked(s.now()) {
		_ = s.writeLocked(s.now())
	}

	var res []PushSubscription
	for _, rec := range s.records {
		res = append(res, rec.PushSubscriptions...)
	}
	return res
}

func (s *DeviceStore) removeExpiredLocked(now time.Time) bool {
	var remaining []DeviceRecord
	changed := false
	for _, rec := range s.records {
		ref := rec.LastSeenAt
		if ref.IsZero() {
			ref = rec.PairedAt
		}
		if now.Sub(ref) > defaultIdleExpiration {
			changed = true
			continue
		}
		remaining = append(remaining, rec)
	}
	if changed {
		s.records = remaining
	}
	return changed
}

func (s *DeviceStore) writeLocked(now time.Time) error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(dir, fmt.Sprintf(".tmp_%d_%s", now.UnixNano(), filepath.Base(s.filePath)))
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}

	// 明确设为 0600 权限以防 umask 影响
	if err := os.Chmod(tmpFile, 0600); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	s.lastWrite = now
	s.hasPendingFlush = false
	return nil
}
