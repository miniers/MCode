package mcodeui

import "testing"

func TestToolStateMarker(t *testing.T) {
	seen := make(map[toolMarkerShape]ToolState)
	for _, tc := range []struct {
		state ToolState
		shape toolMarkerShape
		tone  Tone
	}{
		{ToolPending, toolMarkerHollow, ToneNeutral},
		{ToolRunning, toolMarkerSpinner, ToneAccent},
		{ToolDone, toolMarkerCheck, ToneSuccess},
		{ToolError, toolMarkerAlert, ToneDanger},
	} {
		shape, tone := toolStateMarker(tc.state)
		if previous, ok := seen[shape]; ok {
			t.Fatalf("工具状态 %v 和 %v 的标记相同", previous, tc.state)
		}
		seen[shape] = tc.state
		if shape != tc.shape || tone != tc.tone {
			t.Errorf("工具状态 %v：标记 = %v，颜色 = %v", tc.state, shape, tone)
		}
	}
}
