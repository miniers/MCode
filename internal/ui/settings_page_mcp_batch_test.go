package mcodeui

import (
	"path/filepath"
	"testing"

	"mcode/internal/agentconf"
)

// TestSetMCPToolFlags 覆盖工具列表顶部的批量开关：一次写入多个工具、只影响目标服务，
// 并能通过策略文件读回同样的结果；批量关闭后 map 与文件同步清空。
func TestSetMCPToolFlags(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	uiSt := &mcpPageUIState{
		disabledTools:    map[string]map[string]bool{},
		autoApproveTools: map[string]map[string]bool{},
		policyPath:       policyPath,
	}

	// 另一个服务上已有的标记，用来验证批量操作不会波及无关服务。
	otherServer := map[string]bool{"tool-x": true}
	uiSt.disabledTools["other"] = otherServer

	toolNames := []string{"tool-a", "tool-b", "tool-c"}

	// 批量禁用：三个工具全部置位。
	setMCPToolFlags(uiSt, uiSt.disabledTools, "srv", toolNames, true)
	for _, name := range toolNames {
		if !uiSt.disabledTools["srv"][name] {
			t.Fatalf("批量禁用后 %s 应为 true", name)
		}
	}
	if uiSt.policyError != "" {
		t.Fatalf("保存策略不应报错: %s", uiSt.policyError)
	}
	policy, err := agentconf.LoadMCPToolPolicy(policyPath)
	if err != nil {
		t.Fatalf("读取策略文件失败: %v", err)
	}
	for _, name := range toolNames {
		if !policy.IsDisabled("srv", name) {
			t.Fatalf("批量禁用后文件里 %s 应为 true", name)
		}
	}
	if !policy.IsDisabled("other", "tool-x") {
		t.Fatal("批量操作不应影响其它服务")
	}
	if !uiSt.disabledTools["other"]["tool-x"] {
		t.Fatal("批量操作不应改动其它服务的标记")
	}

	// 批量关闭：目标服务的工具全部清除，文件里同样不再包含它们。
	setMCPToolFlags(uiSt, uiSt.disabledTools, "srv", toolNames, false)
	for _, name := range toolNames {
		if uiSt.disabledTools["srv"][name] {
			t.Fatalf("批量关闭后 %s 应被清除", name)
		}
	}
	policy, err = agentconf.LoadMCPToolPolicy(policyPath)
	if err != nil {
		t.Fatalf("读取策略文件失败: %v", err)
	}
	for _, name := range toolNames {
		if policy.IsDisabled("srv", name) {
			t.Fatalf("批量关闭后文件里 %s 应被清除", name)
		}
	}
	if !policy.IsDisabled("other", "tool-x") {
		t.Fatal("批量关闭不应影响其它服务")
	}

	// 免审批走的是另一份 map，批量操作不应串味。
	setMCPToolFlags(uiSt, uiSt.autoApproveTools, "srv", toolNames, true)
	for _, name := range toolNames {
		if !uiSt.autoApproveTools["srv"][name] {
			t.Fatalf("批量免审批后 %s 应为 true", name)
		}
		if uiSt.disabledTools["srv"][name] {
			t.Fatalf("批量免审批不应改动禁用状态 %s", name)
		}
	}
}
