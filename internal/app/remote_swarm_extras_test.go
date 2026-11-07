package app

import (
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/config"
)

// 档位的写入白名单要原样透传：配置 → 协议 → 配置后相等，且三个切片互不共享底层数组。
func TestSwarmTiersWritePathsRoundTrip(t *testing.T) {
	in := []config.SwarmTier{{
		ID:           "deep",
		Name:         "深度",
		Description:  "只做定位与方案设计",
		ReadOnly:     false,
		WritePaths:   []string{"docs/**/*.md", "*.md"},
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-5",
		Effort:       "high",
		FastMode:     true,
		SystemPrompt: "只写方案文档",
	}}

	wireTiers := swarmTiersToWire(in)
	if len(wireTiers) != 1 {
		t.Fatalf("wire 档位数 = %d, 期望 1", len(wireTiers))
	}
	if got, want := strings.Join(wireTiers[0].WritePaths, ","), strings.Join(in[0].WritePaths, ","); got != want {
		t.Fatalf("透传到协议的写入白名单 = %q, 期望 %q", got, want)
	}

	out := swarmTiersFromWire(wireTiers)
	if len(out) != 1 {
		t.Fatalf("配置档位数 = %d, 期望 1", len(out))
	}
	if got, want := strings.Join(out[0].WritePaths, ","), strings.Join(in[0].WritePaths, ","); got != want {
		t.Fatalf("往返后的写入白名单 = %q, 期望 %q", got, want)
	}

	// 其他字段也要透传，避免只修了白名单却漏字段。
	if out[0].ID != in[0].ID || out[0].ReadOnly != in[0].ReadOnly || out[0].SystemPrompt != in[0].SystemPrompt ||
		out[0].Provider != in[0].Provider || out[0].Model != in[0].Model || out[0].Effort != in[0].Effort ||
		out[0].FastMode != in[0].FastMode {
		t.Fatalf("往返后档位字段不一致: %+v", out[0])
	}

	// 改协议的切片不能影响配置切片，改回程的切片也不能影响协议切片。
	wireTiers[0].WritePaths[0] = "changed-in-wire"
	if in[0].WritePaths[0] != "docs/**/*.md" {
		t.Fatalf("修改协议切片影响了配置切片: %q", in[0].WritePaths[0])
	}
	out[0].WritePaths[1] = "changed-in-config"
	if wireTiers[0].WritePaths[1] != "*.md" {
		t.Fatalf("修改配置切片影响了协议切片: %q", wireTiers[0].WritePaths[1])
	}

	// 空白名单不应序列化出 writePaths 键。
	raw, err := json.Marshal(swarmTiersToWire([]config.SwarmTier{{ID: "chore", Name: "杂活", Description: "机械活"}}))
	if err != nil {
		t.Fatalf("序列化协议档位失败: %v", err)
	}
	if strings.Contains(string(raw), "writePaths") {
		t.Fatalf("空写入白名单却序列化出 writePaths: %s", raw)
	}
}
