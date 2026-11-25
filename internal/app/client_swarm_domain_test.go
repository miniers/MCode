package app

import (
	"math"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote"
)

func TestBeeSessionCost(t *testing.T) {
	turns := map[int]harness.Usage{
		3: {CostUSD: 2},
		0: {CostUSD: 1},
		2: {CostUSD: 0.5},
		1: {CostUSD: 3},
		4: {},
	}
	if got := beeSessionCost(turns, true); math.Abs(got-5) > 1e-9 {
		t.Fatalf("累计语义重置后应分段求和为 5，得到 %v", got)
	}
	if got := beeSessionCost(turns, false); math.Abs(got-6.5) > 1e-9 {
		t.Fatalf("逐轮语义应求和 6.5，得到 %v", got)
	}
	if got := beeSessionCost(nil, true); got != 0 {
		t.Fatalf("无上报应为 0，得到 %v", got)
	}
}

func TestBeeSessionCostAgyCumulative(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	if backend.ProviderIsACP("agy") {
		t.Fatal("预期后端 ProviderIsACP(\"agy\") 为 false")
	}
	client := NewClient(backend)
	if !client.beeCostIsCumulative("agy") {
		t.Fatal("agy bee 必须判定为会话累计费用")
	}
	if !client.beeCostIsCumulative("claude") {
		t.Fatal("claude bee 必须判定为会话累计费用")
	}
	if client.beeCostIsCumulative("pi") {
		t.Fatal("pi bee 不应判定为会话累计费用")
	}

	turns := map[int]harness.Usage{
		0: {CostUSD: 1.0},
		1: {CostUSD: 2.5},
	}
	if got := beeSessionCost(turns, client.beeCostIsCumulative("agy")); math.Abs(got-2.5) > 1e-9 {
		t.Fatalf("agy bee 累计语义应取 2.5，得到 %v", got)
	}
}
