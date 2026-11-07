package mcodeui

import "testing"

func TestPiMagpieModeReadWrite(t *testing.T) {
	tt, st, fake := agentMagpiePageTester(t, routeTestSetting("pi", "off"))
	if got := magpieAgentMode(st, "pi"); got != agentMagpieModeOff {
		t.Fatalf("pi 模式 = %q，期望 off", got)
	}
	st.mu.Lock()
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "jp", Selected: []string{"model"}}}
	st.piMagpie.loaded = true
	st.mu.Unlock()
	clickMagpiePageRoute(t, tt, "pi", "仅 mcode")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 1 && calls[0].Agent == "pi" && calls[0].Mode == "inject"
	})
	waitFor(t, tt, func() bool {
		value, known := magpieAgentRouteValue(st, "pi")
		return known && value == "仅 mcode" && magpieAgentMode(st, "pi") == agentMagpieModeInject
	})
	endpoints, _, loaded, _, _ := magpieEndpointsFor(st, "pi")
	if !loaded || len(endpoints) != 1 || len(endpoints[0].Selected) != 1 || endpoints[0].Selected[0] != "model" {
		t.Fatalf("pi 模型快照被更改: %+v", endpoints)
	}
}
