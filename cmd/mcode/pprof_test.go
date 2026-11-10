package main

import "testing"

func TestPprofAddr(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{":6060", false},
		{"0.0.0.0:6060", false},
		{"192.168.1.2:6060", false},
		{"127.0.0.1", false},
		{"127.0.0.1:", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, ok := pprofAddr(tt.addr)
			if ok != tt.ok || (ok && got != tt.addr) {
				t.Errorf("pprofAddr(%q) = %q, %t; want valid = %t", tt.addr, got, ok, tt.ok)
			}
		})
	}
}
