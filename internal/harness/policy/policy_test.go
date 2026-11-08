package policy

import (
	"strings"
	"testing"
)

// 渲染结果首行必须带版本标记，且标记里包含 Version 常量。
func TestRenderStartsWithVersionMarker(t *testing.T) {
	got := Render(Capabilities{Swarm: true}, Sections{SwarmLead: "lead"})
	lines := strings.Split(got, "\n")
	if len(lines) == 0 {
		t.Fatal("Render 返回空串，期望带版本标记")
	}
	if lines[0] != Marker {
		t.Fatalf("首行不是版本标记: got %q want %q", lines[0], Marker)
	}
	if !strings.Contains(Marker, Version) {
		t.Fatalf("标记未包含版本号: marker=%q version=%q", Marker, Version)
	}
	if got != Marker+"\n\n"+"lead" {
		t.Fatalf("标记后的首段内容不对: %q", got)
	}
}

// 版本号在文案或拼装顺序变化时递增，这里钉住当前值，避免改了文案忘记提版本。
func TestVersionIsBumped(t *testing.T) {
	if Version != "2026-11-01.1" {
		t.Fatalf("版本号应为 2026-11-01.1，实际 %q", Version)
	}
	if want := `<mcode_harness_policy version="2026-11-01.1"/>`; Marker != want {
		t.Fatalf("标记应使用新版本号: got %q want %q", Marker, want)
	}
}

// 空段必须被跳过，不能留下多余的分隔空行。
func TestRenderSkipsEmptySections(t *testing.T) {
	got := Render(Capabilities{Swarm: true, Kanban: true, Context: true, Automation: true}, Sections{
		SwarmLead:   "lead",
		SwarmLimits: "limits",
	})
	want := Marker + "\n\n" + "lead" + "\n\n" + "limits"
	if got != want {
		t.Fatalf("空段未被跳过:\ngot:  %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("出现连续空行: %q", got)
	}
}

// 能力开关关闭时，对应段即使有内容也不输出。
func TestRenderDropsDisabledCapabilities(t *testing.T) {
	sections := Sections{
		SwarmLead:      "lead",
		SwarmTiers:     "tiers",
		SwarmLimits:    "limits",
		SwarmRoster:    "roster",
		Kanban:         "kanban",
		ProjectContext: "context",
		Automation:     "automation",
	}
	got := Render(Capabilities{Swarm: true}, sections)
	for _, unwanted := range []string{"kanban", "context", "automation"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("能力关闭的段被输出: %q in %q", unwanted, got)
		}
	}
	for _, wanted := range []string{"lead", "tiers", "limits", "roster"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("启用的段缺失: %q in %q", wanted, got)
		}
	}
	// 在跑 bee 清单属于编排段，编排关闭时同样不输出。
	if got := Render(Capabilities{}, Sections{SwarmRoster: "roster"}); got != "" {
		t.Fatalf("编排关闭时不应输出 bee 清单，got %q", got)
	}

	// 全部关闭时连标记都不输出。
	if got := Render(Capabilities{}, sections); got != "" {
		t.Fatalf("能力全关时应返回空串，got %q", got)
	}
}

// 段顺序固定：与调用方传入顺序无关，始终按 SwarmLead、SwarmTiers、SwarmLimits、
// SwarmRoster、Kanban、ProjectContext、Automation 排列。
func TestRenderKeepsFixedOrder(t *testing.T) {
	got := Render(Capabilities{Swarm: true, Kanban: true, Context: true, Automation: true}, Sections{
		Automation:     "8",
		ProjectContext: "7",
		Kanban:         "5",
		SwarmRoster:    "4",
		SwarmLimits:    "3",
		SwarmTiers:     "2",
		SwarmLead:      "1",
	})
	want := Marker + "\n\n1\n\n2\n\n3\n\n4\n\n5\n\n7\n\n8"
	if got != want {
		t.Fatalf("段顺序不符合预期:\ngot:  %q\nwant: %q", got, want)
	}
}

// 无任何宿主规则时不输出孤立标记。
func TestRenderEmptyInput(t *testing.T) {
	if got := Render(Capabilities{Swarm: true, Kanban: true, Context: true, Automation: true}, Sections{}); got != "" {
		t.Fatalf("无内容时应返回空串，got %q", got)
	}
}
