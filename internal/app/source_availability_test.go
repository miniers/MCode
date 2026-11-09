package app

import (
	"testing"
	"time"

	"mcode/internal/config"
)

func TestSourceAvailabilityFirstProbe(t *testing.T) {
	a := &sourceAvailability{probe: func() (bool, string) { return false, "未配置 API Key" }}
	if ok, reason := a.get("", config.Config{}, nil); ok || reason != "未配置 API Key" {
		t.Fatalf("首次读取 = %v, %q", ok, reason)
	}
}

func TestSourceAvailabilityFirstProbeTimeout(t *testing.T) {
	previous := firstAvailabilityWait
	firstAvailabilityWait = 20 * time.Millisecond
	t.Cleanup(func() { firstAvailabilityWait = previous })

	release := make(chan struct{})
	a := &sourceAvailability{probe: func() (bool, string) {
		<-release
		return false, "未配置 API Key"
	}}
	ok, reason := a.get("", config.Config{}, nil)
	close(release)
	<-a.done
	if !ok || reason != "" {
		t.Fatalf("超时读取 = %v, %q", ok, reason)
	}
	if ok, reason := a.get("", config.Config{}, nil); ok || reason != "未配置 API Key" {
		t.Fatalf("检查后读取 = %v, %q", ok, reason)
	}
}

func TestSourceAvailabilityProbePanic(t *testing.T) {
	a := &sourceAvailability{probe: func() (bool, string) { panic("probe failed") }}
	ok, reason := a.get("", config.Config{}, nil)
	select {
	case <-a.done:
	default:
		t.Fatal("panic 后检查未结束")
	}
	if ok || reason != "来源可用性检查失败" {
		t.Fatalf("panic 后读取 = %v, %q", ok, reason)
	}
}
