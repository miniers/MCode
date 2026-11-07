package app

import (
	"reflect"
	"testing"

	"mcode/internal/config"
)

// piTuningEnv 只在设置非空时给出对应环境变量，两项都为空时返回 nil。
func TestPiTuningEnv(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.ProviderConfig
		want []string
	}{
		{
			name: "都未设置返回 nil",
			cfg:  config.ProviderConfig{},
			want: nil,
		},
		{
			name: "空白视觉模型视为未设置",
			cfg:  config.ProviderConfig{PiVisionModel: "   "},
			want: nil,
		},
		{
			name: "只开长缓存",
			cfg:  config.ProviderConfig{PiCacheLong: true},
			want: []string{"PI_CACHE_RETENTION=long"},
		},
		{
			name: "只填视觉模型",
			cfg:  config.ProviderConfig{PiVisionModel: "anthropic/claude-haiku-4-5"},
			want: []string{"MCODE_PI_VISION_MODEL=anthropic/claude-haiku-4-5"},
		},
		{
			name: "视觉模型去掉首尾空白",
			cfg:  config.ProviderConfig{PiVisionModel: "  openai/gpt-4o-mini  "},
			want: []string{"MCODE_PI_VISION_MODEL=openai/gpt-4o-mini"},
		},
		{
			name: "两项都设置",
			cfg:  config.ProviderConfig{PiCacheLong: true, PiVisionModel: " openai/gpt-4o-mini "},
			want: []string{"PI_CACHE_RETENTION=long", "MCODE_PI_VISION_MODEL=openai/gpt-4o-mini"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := piTuningEnv(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("piTuningEnv(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}
