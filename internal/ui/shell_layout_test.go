package mcodeui

import "testing"

func TestHeaderCollapseWidth(t *testing.T) {
	for _, tc := range []struct {
		name            string
		window, sidebar float32
		progress        float32
		want            float32
	}{
		{name: "展开", window: 1200, sidebar: 288, progress: 1, want: 912},
		{name: "收起", window: 1200, sidebar: 288, progress: 0, want: 1200},
		{name: "动画中", window: 1200, sidebar: 288, progress: 0.5, want: 1056},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := headerCollapseWidth(tc.window, tc.sidebar, tc.progress); got != tc.want {
				t.Fatalf("headerCollapseWidth(%v, %v, %v) = %v, want %v", tc.window, tc.sidebar, tc.progress, got, tc.want)
			}
		})
	}
}

func TestShellLayout(t *testing.T) {
	if HeaderHeight != 45 {
		t.Fatalf("HeaderHeight = %v, want 45", HeaderHeight)
	}
	if HeaderHeight != ControlMD+2*Space3+1 {
		t.Fatalf("HeaderHeight = %v, want %v", HeaderHeight, ControlMD+2*Space3+1)
	}
	for _, pane := range []shellPane{paneSidebar, paneMain, paneSettings} {
		if got := headerInsetFor(pane); got != HeaderHeight {
			t.Errorf("headerInsetFor(%v) = %v, want %v", pane, got, HeaderHeight)
		}
	}
	if WindowMinWidth != 948 || SidebarDefaultWidth != 288 || SidebarMinWidth != 240 || SidebarMaxWidth != 420 {
		t.Errorf("外壳宽度常量不符合预期")
	}
}
