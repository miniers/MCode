package mcodeui

import (
	"errors"
	"testing"

	"mcode/internal/config"
)

func TestRemoteTLSPortErrorText(t *testing.T) {
	if got := remoteTLSPortErrorText("443", true); got != "" {
		t.Fatalf("remoteTLSPortErrorText(\"443\", true) = %q, want empty", got)
	}
	if got := remoteTLSPortErrorText("1", true); got != "" {
		t.Fatalf("remoteTLSPortErrorText(\"1\", true) = %q, want empty", got)
	}
	if got := remoteTLSPortErrorText("", false); got != "" {
		t.Fatalf("remoteTLSPortErrorText(\"\", false) = %q, want empty", got)
	}
	for _, tc := range []struct {
		text string
		init bool
	}{
		{"0", true},
		{"65536", true},
		{"abc", true},
		{"", true},
	} {
		if got := remoteTLSPortErrorText(tc.text, tc.init); got == "" {
			t.Fatalf("remoteTLSPortErrorText(%q, %v) = empty, want error", tc.text, tc.init)
		}
	}
}

func TestRemoteRevokeDeviceFailureKeepsList(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteDevices = []RemoteDevice{{ID: "dev-1", Name: "iPhone"}}
	fake.remoteErr = errors.New("revoke failed")
	fake.mu.Unlock()

	st := &SettingsState{
		cfg:  config.Defaults(),
		chat: &chatState{client: fake},
	}
	st.mu.Lock()
	st.remote.devicesLoaded = true
	st.remote.devices = []RemoteDevice{{ID: "dev-1", Name: "iPhone"}}
	st.mu.Unlock()

	revokeRemoteDevice(st, "dev-1")

	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		called := len(fake.revokeDeviceCalls) == 1 && fake.revokeDeviceCalls[0] == "dev-1"
		fake.mu.Unlock()
		if !called {
			return false
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.revokeBusyID == ""
	})

	st.mu.Lock()
	devicesErr := st.remote.devicesErr
	actionErr := st.remote.deviceActionErr
	st.mu.Unlock()

	if devicesErr != "" {
		t.Fatalf("devicesErr = %q, want empty", devicesErr)
	}
	if actionErr == "" {
		t.Fatal("deviceActionErr = empty, want non-empty error")
	}
}
