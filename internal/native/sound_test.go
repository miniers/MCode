package native

import (
	"math"
	"testing"
)

// TestFinishChimeSamples 验证完成提示音样本长度、峰值与首尾淡入淡出特性。
func TestFinishChimeSamples(t *testing.T) {
	sr, samples := FinishChimeSamples()

	if sr != 44100 {
		t.Fatalf("expected sample rate 44100, got %d", sr)
	}

	expectedCount := int(44100 * 0.7)
	if len(samples) != expectedCount {
		t.Fatalf("expected sample count %d, got %d", expectedCount, len(samples))
	}

	var peak float64
	for _, s := range samples {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}

	if peak <= 0 || peak > 1.0 {
		t.Errorf("expected peak in range (0, 1.0], got %f", peak)
	}

	// 首采样点接近 0（余弦淡入）
	if math.Abs(float64(samples[0])) > 1e-4 {
		t.Errorf("expected first sample to be near 0, got %f", samples[0])
	}

	// 尾采样点接近 0（线性淡出与指数衰减）
	if math.Abs(float64(samples[len(samples)-1])) > 1e-3 {
		t.Errorf("expected last sample to be near 0, got %f", samples[len(samples)-1])
	}
}

// TestAttentionChimeSamples 验证介入/审批提示音样本。
func TestAttentionChimeSamples(t *testing.T) {
	sr, samples := AttentionChimeSamples()

	if sr != 44100 {
		t.Fatalf("expected sample rate 44100, got %d", sr)
	}

	expectedCount := int(44100 * 0.7)
	if len(samples) != expectedCount {
		t.Fatalf("expected sample count %d, got %d", expectedCount, len(samples))
	}

	var peak float64
	for _, s := range samples {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}

	if peak <= 0 || peak > 1.0 {
		t.Errorf("expected peak in range (0, 1.0], got %f", peak)
	}

	if math.Abs(float64(samples[0])) > 1e-4 {
		t.Errorf("expected first sample to be near 0, got %f", samples[0])
	}
}

// TestDormantChimeSamples 验证休眠提示音样本。
func TestDormantChimeSamples(t *testing.T) {
	sr, samples := DormantChimeSamples()

	if sr != 44100 {
		t.Fatalf("expected sample rate 44100, got %d", sr)
	}

	expectedCount := int(math.Round(44100 * 0.32))
	if len(samples) != expectedCount {
		t.Fatalf("expected sample count %d, got %d", expectedCount, len(samples))
	}

	var peak float64
	for _, s := range samples {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}

	if peak <= 0 || peak > 1.0 {
		t.Errorf("expected peak in range (0, 1.0], got %f", peak)
	}
}
