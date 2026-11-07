package mcodeui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestBeeWindowStripInsetsDarwin 验证 macOS 顶栏左侧让出整个交通灯区域，并与人设图标留出间隙。
func TestBeeWindowStripInsetsDarwin(t *testing.T) {
	leading, trailing := beeWindowStripInsets("darwin", ui.TitleBar{Height: beeWindowStripHeight, Left: beeWindowTrafficRight})
	if minLeading := beeWindowTrafficRight + 12; leading < minLeading {
		t.Errorf("darwin leading = %v, want >= %v（绿灯右边缘 + 12）", leading, minLeading)
	}
	if leading != beeWindowTrafficInset {
		t.Errorf("darwin leading = %v, want %v", leading, beeWindowTrafficInset)
	}
	if trailing != beeWindowStripBaseInset {
		t.Errorf("darwin trailing = %v, want %v", trailing, beeWindowStripBaseInset)
	}
}

// TestBeeWindowStripInsetsOther 验证其他平台按窗口报告的按钮区域让位，行为与改动前一致。
func TestBeeWindowStripInsetsOther(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		bar      ui.TitleBar
		wantLead float32
		wantTail float32
	}{
		{"Windows 按钮在右侧", "windows", ui.TitleBar{Height: beeWindowStripHeight, Right: 138}, beeWindowStripBaseInset, beeWindowStripBaseInset + 138},
		{"Windows 全屏", "windows", ui.TitleBar{}, beeWindowStripBaseInset, beeWindowStripBaseInset},
		{"Linux 按钮在左侧", "linux", ui.TitleBar{Height: 32, Left: 78}, 78, beeWindowStripBaseInset},
		{"Linux 无按钮", "linux", ui.TitleBar{}, beeWindowStripBaseInset, beeWindowStripBaseInset},
	}
	for _, tc := range tests {
		leading, trailing := beeWindowStripInsets(tc.goos, tc.bar)
		if leading != tc.wantLead || trailing != tc.wantTail {
			t.Errorf("%s: beeWindowStripInsets(%q, %+v) = (%v, %v), want (%v, %v)", tc.name, tc.goos, tc.bar, leading, trailing, tc.wantLead, tc.wantTail)
		}
	}
}

// TestBeeWindowStripCentersTrafficLights 验证顶栏的垂直中心与红绿灯中心重合（误差不超过 0.5pt）。
func TestBeeWindowStripCentersTrafficLights(t *testing.T) {
	trafficCenter := beeWindowTrafficY + beeWindowTrafficSize/2
	if diff := math.Abs(float64(beeWindowStripHeight/2 - trafficCenter)); diff > 0.5 {
		t.Errorf("顶栏中心 %v 与红绿灯中心 %v 相差 %vpt，超过 0.5pt", beeWindowStripHeight/2, trafficCenter, diff)
	}
}

// TestBeeStatusText 验证子代理顶栏的状态文字：运行中补后台项数，已结束但后台还在跑时改成等待后台。
func TestBeeStatusText(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		childRunning bool
		background   int
		want         string
		wantWaiting  bool
	}{
		{"运行中无后台", "running", true, 0, "正在工作", false},
		{"运行中有后台", "running", true, 2, "正在工作 · 后台 2 项", false},
		{"已完成有后台且子会话空闲", "completed", false, 1, "等待后台 · 1 项", true},
		{"已完成有后台但子会话运行中", "completed", true, 1, "已完成", false},
		{"已完成无后台", "completed", false, 0, "已完成", false},
		{"失败有后台且子会话空闲", "failed", false, 3, "等待后台 · 3 项", true},
		{"未知状态归一到空闲", "weird", false, 0, "空闲", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := beeStatusText(tc.status, tc.childRunning, tc.background); got != tc.want {
				t.Errorf("beeStatusText(%q, %v, %d) = %q, want %q", tc.status, tc.childRunning, tc.background, got, tc.want)
			}
			if got := beeWaitingBackground(tc.status, tc.childRunning, tc.background); got != tc.wantWaiting {
				t.Errorf("beeWaitingBackground(%q, %v, %d) = %v, want %v", tc.status, tc.childRunning, tc.background, got, tc.wantWaiting)
			}
		})
	}
}

// TestBeeBackgroundTaskCount 验证只统计子会话消息里的后台项，且不要求在运行。
func TestBeeBackgroundTaskCount(t *testing.T) {
	child := &chatState{messages: []Message{backgroundTasksTestBackground("bg1")}}
	if got := beeBackgroundTaskCount(child, "t1"); got != 1 {
		t.Fatalf("beeBackgroundTaskCount = %d, want 1", got)
	}
	if got := beeBackgroundTaskCount(nil, "t1"); got != 0 {
		t.Fatalf("beeBackgroundTaskCount(nil) = %d, want 0", got)
	}
}
