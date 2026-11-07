package remote

import (
	"net"
	"reflect"
	"testing"
)

func TestFilterLANIPv4(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPAddr{IP: net.ParseIP("169.254.1.2")},
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		&net.IPAddr{IP: net.ParseIP("2001:db8::1")},
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
		&net.IPAddr{IP: net.ParseIP("10.0.0.2")},
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)}, // 重复地址
		&net.IPAddr{IP: net.ParseIP("10.0.0.2")},                               // 重复地址
	}

	got := filterLANIPv4(addrs)
	expected := []string{"10.0.0.2", "192.168.1.5"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("filterLANIPv4 got %v, want %v", got, expected)
	}

	// 测试全部被过滤时返回 nil
	filteredOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPAddr{IP: net.ParseIP("169.254.1.2")},
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		&net.IPAddr{IP: net.ParseIP("2001:db8::1")},
	}
	if gotNil := filterLANIPv4(filteredOnly); gotNil != nil {
		t.Fatalf("expected nil when all addresses filtered, got %v", gotNil)
	}
}
