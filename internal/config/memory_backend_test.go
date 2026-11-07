package config

import (
	"encoding/json"
	"testing"
)

func TestMemoryBackendMode(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", MemoryBackendAuto}, {"auto", MemoryBackendAuto}, {"nmem", MemoryBackendNmem}, {"local", MemoryBackendLocal}, {"unknown", MemoryBackendAuto},
	} {
		m := MemoryConfig{Backend: tc.value}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var decoded MemoryConfig
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Backend != tc.value || decoded.BackendMode() != tc.want {
			t.Fatalf("backend %q: got %q, mode %q", tc.value, decoded.Backend, decoded.BackendMode())
		}
	}
}
