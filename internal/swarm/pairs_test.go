package swarm

import "testing"

func TestFindPair(t *testing.T) {
	pairs := []Pair{
		{ID: "pair-1", Name: "Deep Work", BeeProvider: "claude", BeeModel: "opus"},
		{ID: "pair-2", Name: "Quick", BeeProvider: "gemini", BeeModel: "flash"},
	}

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantHit bool
	}{
		{name: "按 ID 精确匹配", ref: "pair-1", wantID: "pair-1", wantHit: true},
		{name: "ID 前后空白被裁剪", ref: "  pair-2  ", wantID: "pair-2", wantHit: true},
		{name: "按名称大小写不敏感匹配", ref: "deep work", wantID: "pair-1", wantHit: true},
		{name: "名称大小写混合", ref: "QUICK", wantID: "pair-2", wantHit: true},
		{name: "名称前后空白被裁剪", ref: " Quick ", wantID: "pair-2", wantHit: true},
		{name: "空引用不匹配", ref: "   ", wantHit: false},
		{name: "未知名不匹配", ref: "nope", wantHit: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FindPair(pairs, tt.ref)
			if ok != tt.wantHit {
				t.Fatalf("FindPair(%q) ok=%v, want %v", tt.ref, ok, tt.wantHit)
			}
			if tt.wantHit && got.ID != tt.wantID {
				t.Errorf("FindPair(%q) id=%q, want %q", tt.ref, got.ID, tt.wantID)
			}
			if !tt.wantHit && (got.ID != "" || got.Name != "") {
				t.Errorf("FindPair(%q) = %+v, want zero Pair", tt.ref, got)
			}
		})
	}
}

func TestFindPair_IDTakesPrecedenceOverName(t *testing.T) {
	pairs := []Pair{
		{ID: "alpha", Name: "Second"},
		{ID: "beta", Name: "alpha"},
	}
	got, ok := FindPair(pairs, "alpha")
	if !ok {
		t.Fatalf("expected hit")
	}
	if got.ID != "alpha" {
		t.Errorf("expected ID match to win, got %q", got.ID)
	}
}

func TestMatchPair(t *testing.T) {
	exact := Pair{ID: "exact", LeadProvider: "claude", LeadModel: "opus", BeeProvider: "claude", BeeModel: "sonnet"}
	wildcard := Pair{ID: "wildcard", LeadProvider: "claude", BeeProvider: "claude", BeeModel: "sonnet"}
	exactSecond := Pair{ID: "exact-second", LeadProvider: "claude", LeadModel: "opus", BeeProvider: "claude", BeeModel: "sonnet"}

	tests := []struct {
		name         string
		pairs        []Pair
		leadProvider string
		leadModel    string
		beeProvider  string
		beeModel     string
		wantID       string
		wantHit      bool
	}{
		{
			name: "精确命中主导与子代理模型", pairs: []Pair{exact},
			leadProvider: "claude", leadModel: "opus", beeProvider: "claude", beeModel: "sonnet",
			wantID: "exact", wantHit: true,
		},
		{
			name: "主导模型为空的通配配对命中", pairs: []Pair{wildcard},
			leadProvider: "claude", leadModel: "haiku", beeProvider: "claude", beeModel: "sonnet",
			wantID: "wildcard", wantHit: true,
		},
		{
			name: "精确配对优先于通配配对", pairs: []Pair{wildcard, exact},
			leadProvider: "claude", leadModel: "opus", beeProvider: "claude", beeModel: "sonnet",
			wantID: "exact", wantHit: true,
		},
		{
			name: "同组合多条时取列表里第一条", pairs: []Pair{exact, exactSecond},
			leadProvider: "claude", leadModel: "opus", beeProvider: "claude", beeModel: "sonnet",
			wantID: "exact", wantHit: true,
		},
		{
			name: "主导 provider 不同不命中", pairs: []Pair{exact},
			leadProvider: "codex", leadModel: "opus", beeProvider: "claude", beeModel: "sonnet",
			wantHit: false,
		},
		{
			name: "主导模型不同不命中", pairs: []Pair{exact},
			leadProvider: "claude", leadModel: "haiku", beeProvider: "claude", beeModel: "sonnet",
			wantHit: false,
		},
		{
			name: "子代理 provider 不同不命中", pairs: []Pair{exact},
			leadProvider: "claude", leadModel: "opus", beeProvider: "codex", beeModel: "sonnet",
			wantHit: false,
		},
		{
			name: "子代理模型不同不命中", pairs: []Pair{exact},
			leadProvider: "claude", leadModel: "opus", beeProvider: "claude", beeModel: "haiku",
			wantHit: false,
		},
		{
			name: "主导 provider 为空不命中", pairs: []Pair{wildcard},
			leadProvider: "", leadModel: "opus", beeProvider: "claude", beeModel: "sonnet",
			wantHit: false,
		},
		{
			name: "子代理选择为空时跟随主导", pairs: []Pair{{ID: "follow-lead", LeadProvider: "claude", LeadModel: "opus", BeeProvider: "claude", BeeModel: "opus"}},
			leadProvider: "claude", leadModel: "opus", beeProvider: "", beeModel: "",
			wantID: "follow-lead", wantHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MatchPair(tt.pairs, tt.leadProvider, tt.leadModel, tt.beeProvider, tt.beeModel)
			if ok != tt.wantHit {
				t.Fatalf("MatchPair ok=%v, want %v (got %+v)", ok, tt.wantHit, got)
			}
			if tt.wantHit && got.ID != tt.wantID {
				t.Errorf("MatchPair id=%q, want %q", got.ID, tt.wantID)
			}
			if !tt.wantHit && got.ID != "" {
				t.Errorf("MatchPair = %+v, want zero Pair", got)
			}
		})
	}
}

func TestPairStillMatches(t *testing.T) {
	exact := Pair{ID: "exact", LeadProvider: "claude", LeadModel: "opus", BeeProvider: "claude", BeeModel: "sonnet"}
	wildcard := Pair{ID: "wildcard", LeadProvider: "claude", BeeProvider: "claude", BeeModel: "sonnet"}

	tests := []struct {
		name        string
		pair        Pair
		leadModel   string
		beeModel    string
		beeProvider string
		want        bool
	}{
		{name: "模型一致仍然适用", pair: exact, leadModel: "opus", beeProvider: "claude", beeModel: "sonnet", want: true},
		{name: "主导模型变化后不再适用", pair: exact, leadModel: "haiku", beeProvider: "claude", beeModel: "sonnet", want: false},
		{name: "子代理模型变化后不再适用", pair: exact, leadModel: "opus", beeProvider: "claude", beeModel: "haiku", want: false},
		{name: "主导模型为空的配对对任意主导模型适用", pair: wildcard, leadModel: "haiku", beeProvider: "claude", beeModel: "sonnet", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PairStillMatches(tt.pair, "claude", tt.leadModel, tt.beeProvider, tt.beeModel); got != tt.want {
				t.Errorf("PairStillMatches = %v, want %v", got, tt.want)
			}
		})
	}
}
