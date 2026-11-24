package mcodeui

import "testing"

// TestPreviewWebViewOptionsEnablesSwipeNavigation 验证无论安全沙箱与控制台采集如何组合，
// 预览 WebView 均开启双指横滑前进/后退导航。
func TestPreviewWebViewOptionsEnablesSwipeNavigation(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		for _, capture := range []bool{false, true} {
			opts := previewWebViewOptions(untrusted, capture)
			if !opts.Page.SwipeNavigation {
				t.Fatalf("previewWebViewOptions(%v, %v).Page.SwipeNavigation = false, want true", untrusted, capture)
			}
			if opts.Page.Untrusted != untrusted {
				t.Fatalf("previewWebViewOptions(%v, %v).Page.Untrusted = %v, want %v", untrusted, capture, opts.Page.Untrusted, untrusted)
			}
		}
	}
}
