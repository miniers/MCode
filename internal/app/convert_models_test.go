package app

import (
	"testing"

	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

func TestProviderWireToUICapabilities(t *testing.T) {
	testenv.TempHome(t)
	effort := "medium"
	src := wire.Provider{Models: []wire.ProviderModel{{
		ID: "opus", Efforts: []string{"low", "medium", "high"},
		DefaultEffort: &effort, SupportsFast: true, Secondary: true,
	}}}
	dst := ProviderWireToUI(src)
	model := dst.Models[0]
	if model.DefaultEffort != effort || !model.SupportsFast || len(model.Efforts) != 3 {
		t.Fatalf("capabilities were lost in conversion: %+v", model)
	}
	if !model.Secondary {
		t.Fatalf("secondary 标记在转换中丢失: %+v", model)
	}
	model.Efforts[0] = "changed"
	if src.Models[0].Efforts[0] != "low" {
		t.Fatal("UI model efforts alias the wire model")
	}
}

// 模型目录原因（指针字段）转换为 UI 的空串语义：nil 变空串，有值时原样搬运。
func TestProviderWireToUIUnavailableReason(t *testing.T) {
	if got := ProviderWireToUI(wire.Provider{ID: "claude"}).UnavailableReason; got != "" {
		t.Fatalf("nil reason 应为空串，got %q", got)
	}
	reason := "未安装 Claude Code CLI"
	if got := ProviderWireToUI(wire.Provider{ID: "claude", UnavailableReason: &reason}).UnavailableReason; got != reason {
		t.Fatalf("原因文案应原样搬运，got %q", got)
	}
}
