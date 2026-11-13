package remote

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDeviceStore_AddAndFind_PermissionsAndNoPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	store, err := NewDeviceStore(filePath, now)
	if err != nil {
		t.Fatalf("NewDeviceStore failed: %v", err)
	}

	secretToken := "secret-token-32bytes-hex-abcdef0123456789abcdef0123456789"
	tokenHash := HashToken(secretToken)

	rec := DeviceRecord{
		DeviceID:    "dev-1",
		Label:       "My Phone",
		PairedAt:    currTime,
		LastSeenAt:  currTime,
		TokenHash:   tokenHash,
		UserAgent:   "TestUA",
		LastAddress: "192.168.1.100",
		LastEntry:   "lan",
	}

	if err := store.Add(rec); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 验证查找
	found, ok := store.FindByTokenHash(tokenHash)
	if !ok || found.DeviceID != "dev-1" {
		t.Fatalf("expected device found, got ok=%v, dev=%+v", ok, found)
	}

	devFound, ok := store.FindByDeviceID("dev-1")
	if !ok || devFound.TokenHash != tokenHash {
		t.Fatalf("expected find by devID ok, got ok=%v, dev=%+v", ok, devFound)
	}

	// 验证文件权限 0600
	fi, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0600 {
		t.Fatalf("expected file permission 0600, got %04o", perm)
	}

	// 验证磁盘存储绝不包含明文 sessionToken
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if strings.Contains(string(fileBytes), secretToken) {
		t.Fatalf("file must not contain plaintext session token")
	}
	if !strings.Contains(string(fileBytes), tokenHash) {
		t.Fatalf("file must contain tokenHash")
	}
}

func TestDeviceStore_IdleExpiration_30Days(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	store, err := NewDeviceStore(filePath, now)
	if err != nil {
		t.Fatalf("NewDeviceStore failed: %v", err)
	}

	tokenHash := HashToken("token-expiring")
	rec := DeviceRecord{
		DeviceID:   "dev-exp",
		Label:      "Expiring Device",
		PairedAt:   currTime,
		LastSeenAt: currTime,
		TokenHash:  tokenHash,
	}
	if err := store.Add(rec); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 推进时间 31 天
	currTime = currTime.Add(31 * 24 * time.Hour)

	// 认证查表时应当清理并返回未找到
	if _, ok := store.FindByTokenHash(tokenHash); ok {
		t.Fatalf("expected device to be expired after 31 days")
	}

	// 重新实例化 store，验证在加载时也会自动清理
	storeReloaded, err := NewDeviceStore(filePath, now)
	if err != nil {
		t.Fatalf("NewDeviceStore reload failed: %v", err)
	}
	if records := storeReloaded.AllRecords(); len(records) != 0 {
		t.Fatalf("expected 0 records after idle expiration, got %d", len(records))
	}
}

func TestDeviceStore_SeenWriteThrottle_60Seconds(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "remote_devices.json")

	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	now := func() time.Time { return currTime }

	store, err := NewDeviceStore(filePath, now)
	if err != nil {
		t.Fatalf("NewDeviceStore failed: %v", err)
	}

	tokenHash := HashToken("token-throttle")
	rec := DeviceRecord{
		DeviceID:   "dev-throttle",
		Label:      "Throttle Device",
		PairedAt:   currTime,
		LastSeenAt: currTime,
		TokenHash:  tokenHash,
	}
	if err := store.Add(rec); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	fi1, _ := os.Stat(filePath)
	modTime1 := fi1.ModTime()

	// 推进 10 秒后 Touch，内存实时生效，但磁盘写盘应节流
	currTime = currTime.Add(10 * time.Second)
	info := ClientInfo{Address: "192.168.1.55"}
	if err := store.Touch(tokenHash, info); err != nil {
		t.Fatalf("Touch failed: %v", err)
	}

	// 内存已更新
	found, _ := store.FindByTokenHash(tokenHash)
	if found.LastSeenAt != currTime || found.LastAddress != "192.168.1.55" {
		t.Fatalf("memory update should take effect immediately, got %+v", found)
	}

	// 磁盘尚未落盘（因节流未超过 60 秒）
	fi2, _ := os.Stat(filePath)
	if !fi2.ModTime().Equal(modTime1) {
		t.Fatalf("expected disk write to be throttled within 60s")
	}

	// 调用 Flush 强制落盘
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// 验证时间再推进 65 秒后的 Touch 自动落盘
	currTime = currTime.Add(65 * time.Second)
	info2 := ClientInfo{Address: "192.168.1.66"}
	if err := store.Touch(tokenHash, info2); err != nil {
		t.Fatalf("Touch after 65s failed: %v", err)
	}
	fi3, _ := os.Stat(filePath)
	if fi3.ModTime().Equal(fi2.ModTime()) {
		t.Fatalf("expected disk write to execute after 60s interval")
	}
}

func TestDeviceStore_RemoveAndClear(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "remote_devices.json")

	store, err := NewDeviceStore(filePath, nil)
	if err != nil {
		t.Fatalf("NewDeviceStore failed: %v", err)
	}

	rec1 := DeviceRecord{DeviceID: "dev-1", TokenHash: "hash-1", PairedAt: time.Now()}
	rec2 := DeviceRecord{DeviceID: "dev-2", TokenHash: "hash-2", PairedAt: time.Now()}
	_ = store.Add(rec1)
	_ = store.Add(rec2)

	// Remove 单个设备
	revoked, err := store.Remove("dev-1")
	if err != nil || !revoked {
		t.Fatalf("expected dev-1 removed, got revoked=%v, err=%v", revoked, err)
	}
	if _, ok := store.FindByDeviceID("dev-1"); ok {
		t.Fatalf("dev-1 should be removed")
	}
	if _, ok := store.FindByDeviceID("dev-2"); !ok {
		t.Fatalf("dev-2 should still exist")
	}

	// Clear 清空全部
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if len(store.AllRecords()) != 0 {
		t.Fatalf("expected 0 records after Clear")
	}
}
