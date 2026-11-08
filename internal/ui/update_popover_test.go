package mcodeui

import "testing"

func TestShouldShowWhatsNew(t *testing.T) {
	tests := []struct {
		name, current, lastSeen, section string
		show, persist                   bool
	}{
		{"dev", "dev", "1.0", "内容", false, false},
		{"空版本", "", "1.0", "内容", false, false},
		{"首次安装", "2.0", "", "内容", false, true},
		{"版本相同", "2.0", "2.0", "内容", false, false},
		{"没有说明", "2.0", "1.0", "", false, true},
		{"版本更新", "2.0", "1.0", "内容", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			show, persist := shouldShowWhatsNew(tt.current, tt.lastSeen, tt.section)
			if show != tt.show || persist != tt.persist {
				t.Errorf("got (%v, %v), want (%v, %v)", show, persist, tt.show, tt.persist)
			}
		})
	}
}

func TestUpdateButtonText(t *testing.T) {
	if got := updateButtonText(true); got != "更新并重启" {
		t.Errorf("installable: got %q", got)
	}
	if got := updateButtonText(false); got != "下载" {
		t.Errorf("not installable: got %q", got)
	}
}
