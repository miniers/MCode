package keyring

import (
	"errors"
	"testing"
)

func TestMemoryBackend(t *testing.T) {
	backend := NewMemoryBackend()
	service := "mcode.test"
	account := "test_account"

	// 1. 获取不存在的凭据返回 ErrNotFound
	val, err := backend.Get(service, account)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent item, got val=%q, err=%v", val, err)
	}

	// 2. Set 后读取
	secretVal := "super-secret-value-123"
	if err := backend.Set(service, account, secretVal); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	val, err = backend.Get(service, account)
	if err != nil || val != secretVal {
		t.Fatalf("expected %q, got val=%q, err=%v", secretVal, val, err)
	}

	// 3. 覆盖写入
	newVal := "super-secret-value-456"
	if err := backend.Set(service, account, newVal); err != nil {
		t.Fatalf("overwrite Set failed: %v", err)
	}
	val, err = backend.Get(service, account)
	if err != nil || val != newVal {
		t.Fatalf("expected %q after overwrite, got val=%q, err=%v", newVal, val, err)
	}

	// 4. 删除存在的条目
	if err := backend.Delete(service, account); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	val, err = backend.Get(service, account)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after Delete, got val=%q, err=%v", val, err)
	}

	// 5. 删除不存在的条目返回 nil
	if err := backend.Delete(service, "non_existent_acc"); err != nil {
		t.Fatalf("deleting non-existent item should return nil, got: %v", err)
	}
}

func TestEncodeDecodeValue(t *testing.T) {
	cases := []string{
		"",
		"simple-secret",
		"p@ssw0rd!#%^&*()",
		"line1\nline2\r\nline3",
		"包含中文与特殊字符：🔑🔐",
		"a\"b\\c'd",
	}

	for _, tc := range cases {
		encoded := EncodeValue(tc)
		if len(encoded) < len(b64Prefix) || encoded[:len(b64Prefix)] != b64Prefix {
			t.Fatalf("encoded value %q should have prefix %q", encoded, b64Prefix)
		}

		decoded, err := DecodeValue(encoded)
		if err != nil {
			t.Fatalf("DecodeValue failed for %q: %v", tc, err)
		}
		if decoded != tc {
			t.Fatalf("roundtrip mismatch: want %q, got %q", tc, decoded)
		}
	}

	// 没有前缀的 legacy 字符串应原样返回
	rawLegacy := "plain-legacy-secret"
	decodedLegacy, err := DecodeValue(rawLegacy)
	if err != nil {
		t.Fatalf("DecodeValue for legacy plain string failed: %v", err)
	}
	if decodedLegacy != rawLegacy {
		t.Fatalf("expected %q, got %q", rawLegacy, decodedLegacy)
	}

	// 非法 base64 应返回错误
	_, err = DecodeValue("mcode-b64:invalid-base64-!!!")
	if err == nil {
		t.Fatalf("expected error on invalid base64 data, got nil")
	}
}

func TestQuoteArg(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", `""`},
		{"simple", `"simple"`},
		{`has"quote`, `"has\"quote"`},
		{`has\slash`, `"has\\slash"`},
		{`both"and\slash`, `"both\"and\\slash"`},
		{`path/to/bin`, `"path/to/bin"`},
	}

	for _, tt := range tests {
		got := QuoteArg(tt.input)
		if got != tt.want {
			t.Errorf("QuoteArg(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
