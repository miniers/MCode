package app

import (
	"math"
	"testing"

	"mcode/internal/harness"
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
