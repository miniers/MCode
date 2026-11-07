package config

import (
	"errors"
	"runtime"
	"testing"

	"mcode/internal/native/keyring"
)

// (a) 与 (b)：三类旧数据经 MigrateLegacySecrets 迁移后 primary 中有值、legacy 中已删除；连续执行两次结果相同（幂等）。
func TestMigrateLegacySecrets_SuccessAndIdempotent(t *testing.T) {
	primary := keyring.NewMemoryBackend()
	legacyStores := map[string]SecretStore{
		"mcode":           NewMemorySecretStore(),
		"mcode.mcp-oauth": NewMemorySecretStore(),
	}
	legacyFor := func(service string) SecretStore {
		return legacyStores[service]
	}

	// 注入三类旧数据
	_ = legacyStores["mcode"].Set("cloudflare_api_token", "cf-token-abc")
	_ = legacyStores["mcode"].Set("openai.apiKey", "sk-openai-xyz")
	mcpAccount := "mcp-oauth:github:token"
	_ = legacyStores["mcode.mcp-oauth"].Set(mcpAccount, "gh-token-123")

	refs := LegacySecretRefs([]string{mcpAccount})

	// 第一次迁移
	errs := migrateLegacySecrets(refs, primary, legacyFor)
	if len(errs) > 0 {
		t.Fatalf("first migration had errors: %v", errs)
	}

	// 校验 primary 中有值
	val, err := primary.Get("mcode", "cloudflare_api_token")
	if err != nil || val != "cf-token-abc" {
		t.Errorf("primary cloudflare_api_token mismatch: got val=%q, err=%v", val, err)
	}
	val, err = primary.Get("mcode", "openai.apiKey")
	if err != nil || val != "sk-openai-xyz" {
		t.Errorf("primary openai.apiKey mismatch: got val=%q, err=%v", val, err)
	}
	val, err = primary.Get("mcode.mcp-oauth", mcpAccount)
	if err != nil || val != "gh-token-123" {
		t.Errorf("primary mcp account mismatch: got val=%q, err=%v", val, err)
	}

	// 校验 legacy 中已删除
	_, err = legacyStores["mcode"].Get("cloudflare_api_token")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("expected legacy cloudflare_api_token deleted, got err=%v", err)
	}
	_, err = legacyStores["mcode"].Get("openai.apiKey")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("expected legacy openai.apiKey deleted, got err=%v", err)
	}
	_, err = legacyStores["mcode.mcp-oauth"].Get(mcpAccount)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("expected legacy mcp account deleted, got err=%v", err)
	}

	// (b) 连续执行第二次结果相同（幂等）
	errs2 := migrateLegacySecrets(refs, primary, legacyFor)
	if len(errs2) > 0 {
		t.Fatalf("second migration had errors: %v", errs2)
	}

	// primary 数据保持完整
	val, err = primary.Get("mcode", "cloudflare_api_token")
	if err != nil || val != "cf-token-abc" {
		t.Errorf("second run primary cloudflare_api_token mismatch: got val=%q, err=%v", val, err)
	}
	val, err = primary.Get("mcode", "openai.apiKey")
	if err != nil || val != "sk-openai-xyz" {
		t.Errorf("second run primary openai.apiKey mismatch: got val=%q, err=%v", val, err)
	}
	val, err = primary.Get("mcode.mcp-oauth", mcpAccount)
	if err != nil || val != "gh-token-123" {
		t.Errorf("second run primary mcp account mismatch: got val=%q, err=%v", val, err)
	}
}

// 模拟总是报错的 primary 后端
type failKeyringBackend struct {
	keyring.Backend
	setErr error
}

func (f *failKeyringBackend) Set(service, account, value string) error {
	return f.setErr
}

// (c) primary 的 Set 失败时 legacy 数据保留，经 Get 仍可读。
func TestMigrateLegacySecrets_PrimarySetFailure(t *testing.T) {
	fakePrimary := &failKeyringBackend{
		Backend: keyring.NewMemoryBackend(),
		setErr:  errors.New("simulated disk full"),
	}
	legacyStore := NewMemorySecretStore()
	legacyFor := func(service string) SecretStore {
		return legacyStore
	}

	secretKey := "cloudflare_api_token"
	secretVal := "cf-token-keep"
	_ = legacyStore.Set(secretKey, secretVal)

	refs := []SecretRef{{Service: "mcode", Account: secretKey}}
	errs := migrateLegacySecrets(refs, fakePrimary, legacyFor)
	if len(errs) == 0 {
		t.Fatalf("expected error from failed primary Set, got none")
	}

	// legacy 数据保留，且仍可读
	gotVal, err := legacyStore.Get(secretKey)
	if err != nil || gotVal != secretVal {
		t.Fatalf("expected legacy data preserved, got val=%q, err=%v", gotVal, err)
	}

	// 经由 newKeyringSecretStore 的 Get 也仍可读
	store := newKeyringSecretStore("mcode", fakePrimary, legacyStore)
	storeVal, err := store.Get(secretKey)
	if err != nil || storeVal != secretVal {
		t.Fatalf("expected store.Get to read legacy on primary failure, got val=%q, err=%v", storeVal, err)
	}
}

// (d) Get 惰性迁移：primary 为空、legacy 有值时，Get 会命中 legacy 并写入 primary，读回一致后删除 legacy。
func TestKeyringSecretStore_LazyMigration(t *testing.T) {
	primary := keyring.NewMemoryBackend()
	legacy := NewMemorySecretStore()
	store := newKeyringSecretStore("mcode", primary, legacy)

	account := "test_lazy_account"
	expectedVal := "secret_lazy_val_789"
	_ = legacy.Set(account, expectedVal)

	// 调用 Get 进行惰性迁移
	gotVal, err := store.Get(account)
	if err != nil || gotVal != expectedVal {
		t.Fatalf("expected gotVal=%q, got %q (err=%v)", expectedVal, gotVal, err)
	}

	// 校验 primary 中已经写入该值
	primaryVal, err := primary.Get("mcode", account)
	if err != nil || primaryVal != expectedVal {
		t.Fatalf("expected primary to have migrated value %q, got %q (err=%v)", expectedVal, primaryVal, err)
	}

	// 校验 legacy 中的值已删除
	_, legErr := legacy.Get(account)
	if !errors.Is(legErr, ErrSecretNotFound) {
		t.Fatalf("expected legacy value to be deleted after lazy migration, got err=%v", legErr)
	}

	// 再次 Get 应从 primary 中读取
	gotVal2, err := store.Get(account)
	if err != nil || gotVal2 != expectedVal {
		t.Fatalf("expected second Get=%q, got %q (err=%v)", expectedVal, gotVal2, err)
	}
}

// 模拟 ErrUnsupported 的 primary 后端
type unsupportedKeyringBackend struct{}

func (u *unsupportedKeyringBackend) Get(service, account string) (string, error) {
	return "", keyring.ErrUnsupported
}

func (u *unsupportedKeyringBackend) Set(service, account, value string) error {
	return keyring.ErrUnsupported
}

func (u *unsupportedKeyringBackend) Delete(service, account string) error {
	return keyring.ErrUnsupported
}

// (e) primary 报 ErrUnsupported 时完全回退到 legacy。
func TestKeyringSecretStore_PrimaryUnsupported(t *testing.T) {
	primary := &unsupportedKeyringBackend{}
	legacy := NewMemorySecretStore()
	store := newKeyringSecretStore("mcode", primary, legacy)

	account := "unsupported_platform_account"
	val := "unsupported_secret_value"

	// 1. 未设置时 Get 返回 ErrSecretNotFound
	_, err := store.Get(account)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound on non-existent item, got: %v", err)
	}

	// 2. Set 应回退写入 legacy
	if err := store.Set(account, val); err != nil {
		t.Fatalf("expected Set to succeed via fallback, got: %v", err)
	}

	// 校验 legacy 已被写入
	legVal, err := legacy.Get(account)
	if err != nil || legVal != val {
		t.Fatalf("expected legacy to contain %q, got val=%q, err=%v", val, legVal, err)
	}

	// 3. Get 应回退读取 legacy
	gotVal, err := store.Get(account)
	if err != nil || gotVal != val {
		t.Fatalf("expected store.Get fallback to legacy %q, got val=%q, err=%v", val, gotVal, err)
	}

	// 4. Delete 应成功删除 legacy 且不报错
	if err := store.Delete(account); err != nil {
		t.Fatalf("expected Delete to succeed without error, got: %v", err)
	}

	// 5. 再次 Get 返回 ErrSecretNotFound
	_, err = store.Get(account)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got: %v", err)
	}
}

func TestDarwinSecretsReuseExistingKeychainItems(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS shares the legacy and primary keychain")
	}
	primary := keyring.NewMemoryBackend()
	refs := LegacySecretRefs([]string{"mcp-oauth:github:token", "mcp-oauth:github:client"})
	for _, ref := range refs {
		if err := primary.Set(ref.Service, ref.Account, "existing"); err != nil {
			t.Fatal(err)
		}
		if legacy := newLegacySecretStore(ref.Service); legacy != nil {
			t.Fatal("shared keychain must not be used as a separate migration source")
		}
	}
	for i := 0; i < 2; i++ {
		if errs := migrateLegacySecrets(refs, primary, newLegacySecretStore); len(errs) != 0 {
			t.Fatal(errs)
		}
		for _, ref := range refs {
			store := newKeyringSecretStore(ref.Service, primary, newLegacySecretStore(ref.Service))
			if got, err := store.Get(ref.Account); err != nil || got != "existing" {
				t.Fatalf("shared item was lost: %s/%s: %q, %v", ref.Service, ref.Account, got, err)
			}
		}
	}
	store := newKeyringSecretStore("mcode", primary, nil)
	if err := store.Set("openai.apiKey", "updated"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get("openai.apiKey"); err != nil || got != "updated" {
		t.Fatalf("updated shared item: %q, %v", got, err)
	}
	if err := store.Delete("openai.apiKey"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("openai.apiKey"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted shared item: %v", err)
	}
}

func TestKeyringSecretStoreFailedUpdateDoesNotSaveShadowedValue(t *testing.T) {
	primary := keyring.NewMemoryBackend()
	_ = primary.Set("mcode", "key", "old")
	writeErr := errors.New("keychain locked")
	legacy := NewMemorySecretStore()
	store := newKeyringSecretStore("mcode", &failKeyringBackend{Backend: primary, setErr: writeErr}, legacy)
	if err := store.Set("key", "new"); !errors.Is(err, writeErr) {
		t.Fatalf("must report failed update, got %v", err)
	}
	if _, err := legacy.Get("key"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("failed update must not write a shadowed fallback: %v", err)
	}
}

func TestKeyringSecretStoreUpdateRemovesStaleFallback(t *testing.T) {
	primary := keyring.NewMemoryBackend()
	legacy := NewMemorySecretStore()
	_ = legacy.Set("key", "old")
	store := newKeyringSecretStore("mcode", primary, legacy)
	if err := store.Set("key", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Get("key"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("stale fallback must be removed: %v", err)
	}
	if got, err := store.Get("key"); err != nil || got != "new" {
		t.Fatalf("updated secret: %q, %v", got, err)
	}
}

type readErrorBackend struct {
	keyring.Backend
	err error
}

func (b readErrorBackend) Get(service, account string) (string, error) {
	return "", b.err
}

func TestKeyringSecretStoreReadFailure(t *testing.T) {
	readErr := errors.New("keychain access denied")
	for _, legacy := range []SecretStore{nil, NewMemorySecretStore()} {
		store := newKeyringSecretStore("mcode", readErrorBackend{err: readErr}, legacy)
		if _, err := store.Get("key"); !errors.Is(err, readErr) {
			t.Fatalf("must preserve primary error instead of reporting missing credentials: %v", err)
		}
	}
	legacy := NewMemorySecretStore()
	_ = legacy.Set("key", "existing")
	store := newKeyringSecretStore("mcode", readErrorBackend{err: readErr}, legacy)
	if got, err := store.Get("key"); err != nil || got != "existing" {
		t.Fatalf("legacy must remain readable on primary error: %q, %v", got, err)
	}
}

func TestMigrateLegacySecretsConflictPreservesBothValues(t *testing.T) {
	for _, ref := range LegacySecretRefs([]string{"mcp-oauth:github:token", "mcp-oauth:github:client"}) {
		t.Run(ref.Service+"/"+ref.Account, func(t *testing.T) {
			primary := keyring.NewMemoryBackend()
			legacy := NewMemorySecretStore()
			_ = primary.Set(ref.Service, ref.Account, "primary")
			_ = legacy.Set(ref.Account, "legacy")
			errs := migrateLegacySecrets([]SecretRef{ref}, primary, func(string) SecretStore { return legacy })
			if len(errs) != 1 {
				t.Fatalf("expected migration conflict, got %v", errs)
			}
			if got, err := legacy.Get(ref.Account); err != nil || got != "legacy" {
				t.Fatalf("conflicting legacy value lost: %q, %v", got, err)
			}
			if got, err := primary.Get(ref.Service, ref.Account); err != nil || got != "primary" {
				t.Fatalf("primary value overwritten: %q, %v", got, err)
			}
		})
	}
}

func TestLegacySecretsRemainReadableWhenMigrationFails(t *testing.T) {
	for _, ref := range LegacySecretRefs([]string{"mcp-oauth:github:token", "mcp-oauth:github:client"}) {
		t.Run(ref.Service+"/"+ref.Account, func(t *testing.T) {
			primary := &failKeyringBackend{Backend: keyring.NewMemoryBackend(), setErr: errors.New("write denied")}
			legacy := NewMemorySecretStore()
			_ = legacy.Set(ref.Account, "existing")
			for i := 0; i < 2; i++ {
				if errs := migrateLegacySecrets([]SecretRef{ref}, primary, func(string) SecretStore { return legacy }); len(errs) != 1 {
					t.Fatalf("expected failed migration: %v", errs)
				}
				store := newKeyringSecretStore(ref.Service, primary, legacy)
				if got, err := store.Get(ref.Account); err != nil || got != "existing" {
					t.Fatalf("legacy no longer readable: %q, %v", got, err)
				}
				if got, err := legacy.Get(ref.Account); err != nil || got != "existing" {
					t.Fatalf("legacy was deleted: %q, %v", got, err)
				}
			}
		})
	}
}

func TestCustomEndpointSecretAccountRoundtrip(t *testing.T) {
	primary := keyring.NewMemoryBackend()
	store := newKeyringSecretStore("mcode", primary, nil)
	account := CustomEndpointSecretAccount("abc")

	if account != "providers.custom.abc.apiKey" {
		t.Fatalf("expected account providers.custom.abc.apiKey, got %q", account)
	}

	if _, err := store.Get(account); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound before set, got %v", err)
	}

	if err := store.Set(account, "sk-test-custom-key"); err != nil {
		t.Fatalf("store.Set failed: %v", err)
	}

	got, err := store.Get(account)
	if err != nil {
		t.Fatalf("store.Get failed: %v", err)
	}
	if got != "sk-test-custom-key" {
		t.Fatalf("expected sk-test-custom-key, got %q", got)
	}

	if err := store.Delete(account); err != nil {
		t.Fatalf("store.Delete failed: %v", err)
	}

	if _, err := store.Get(account); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got %v", err)
	}
}
