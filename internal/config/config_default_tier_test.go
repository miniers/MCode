package config

import "testing"

func TestDefaultTierAllWritePaths(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Defaults()
	for i := range cfg.Orchestration.Tiers {
		if !cfg.Orchestration.Tiers[i].ReadOnly {
			cfg.Orchestration.Tiers[i].WritePaths = []string{"docs/**"}
		}
	}

	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Orchestration.DefaultTier == "" {
		t.Fatal("expected non-empty DefaultTier after Save and Load")
	}

	// DefaultTier 为空时也应回退到首个非只读档位并通过校验。
	cfg.Orchestration.DefaultTier = ""
	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save with empty DefaultTier failed: %v", err)
	}
	loadedFallback, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load after empty DefaultTier failed: %v", err)
	}
	if loadedFallback.Orchestration.DefaultTier == "" {
		t.Fatal("expected fallback DefaultTier to be non-empty")
	}
}
