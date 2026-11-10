package mcp

import (
	"testing"
	"time"
)

func TestIdempotencyCacheEvictionKeepsInFlight(t *testing.T) {
	cache := NewIdempotencyCache(2, time.Hour)
	for _, key := range []string{"A", "B"} {
		if status, _ := cache.Begin(key, "fingerprint"); status != BeginProceed {
			t.Fatalf("Begin(%s) = %v, want BeginProceed", key, status)
		}
	}
	cache.Finish("B", ToolResult{})
	if status, _ := cache.Begin("C", "fingerprint"); status != BeginProceed {
		t.Fatalf("Begin(C) = %v, want BeginProceed", status)
	}
	if status, _ := cache.Begin("A", "fingerprint"); status != BeginInFlight {
		t.Fatalf("Begin(A) = %v, want BeginInFlight", status)
	}
	if _, ok := cache.entries["B"]; ok {
		t.Fatal("已完成的 B 应被淘汰")
	}
}

func TestIdempotencyCacheEvictionAllowsInFlightOverflow(t *testing.T) {
	cache := NewIdempotencyCache(2, time.Hour)
	for _, key := range []string{"A", "B", "C"} {
		if status, _ := cache.Begin(key, "fingerprint"); status != BeginProceed {
			t.Fatalf("Begin(%s) = %v, want BeginProceed", key, status)
		}
	}
	if len(cache.entries) != 3 {
		t.Fatalf("在途条目数 = %d, want 3", len(cache.entries))
	}
	cache.Finish("B", ToolResult{})
	if _, ok := cache.entries["B"]; ok {
		t.Fatal("完成后应淘汰 B 以恢复容量")
	}
	for _, key := range []string{"A", "C"} {
		if status, _ := cache.Begin(key, "fingerprint"); status != BeginInFlight {
			t.Fatalf("Begin(%s) = %v, want BeginInFlight", key, status)
		}
	}
}
