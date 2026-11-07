package mcodeui

import (
	"strings"
	"testing"
)

func TestMagpieGatewayKeyDraft(t *testing.T) {
	st := &SettingsState{}
	ep := CustomEndpoint{HasAPIKey: true, HasWebKey: false}
	openCustomEndpointForm(st, &ep, endpointFormScopeMagpie)
	if !st.endpoints.editHasAPIKey || st.endpoints.editHasWebKey {
		t.Fatalf("已保存 API Key 的草稿状态不正确：API Key=%v，web key=%v", st.endpoints.editHasAPIKey, st.endpoints.editHasWebKey)
	}
	if got := magpieKeyPlaceholder(st.endpoints.editHasAPIKey); got != "留空则保留已保存的 Key" {
		t.Errorf("已保存 API Key 的提示不正确：%q", got)
	}

	openCustomEndpointForm(st, nil, endpointFormScopeMagpie)
	if st.endpoints.editHasAPIKey {
		t.Fatal("新建网关不应保留已有 API Key 状态")
	}
	if got := magpieKeyPlaceholder(st.endpoints.editHasAPIKey); !strings.Contains(got, "必填") {
		t.Errorf("新建网关的提示应注明必填：%q", got)
	}

	ep = CustomEndpoint{HasWebKey: true}
	openCustomEndpointForm(st, &ep, endpointFormScopeMagpie)
	if st.endpoints.editHasAPIKey || !st.endpoints.editHasWebKey {
		t.Fatalf("仅保存 web key 的草稿状态不正确：API Key=%v，web key=%v", st.endpoints.editHasAPIKey, st.endpoints.editHasWebKey)
	}
	if got := magpieKeyPlaceholder(st.endpoints.editHasAPIKey); !strings.Contains(got, "必填") {
		t.Errorf("仅保存 web key 时 API Key 提示应注明必填：%q", got)
	}
}

func TestMagpieKeyPlaceholder(t *testing.T) {
	placeholder := magpieKeyPlaceholder(false)
	if strings.Contains(placeholder, "留空") || !strings.Contains(placeholder, "必填") {
		t.Errorf("未保存密钥的提示应注明必填且不能提示留空：%q", placeholder)
	}
	if got := magpieKeyPlaceholder(true); got != "留空则保留已保存的 Key" {
		t.Errorf("已保存密钥的提示发生变化：%q", got)
	}
}
