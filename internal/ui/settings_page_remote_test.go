package mcodeui

import (
	"reflect"
	"testing"

	"mcode/internal/config"
)

func remoteTestRows(t *testing.T, st *SettingsState, id string) []string {
	t.Helper()
	var found int
	var ids []string
	for _, section := range pageRemote.Sections {
		if section.ID != id {
			continue
		}
		found++
		for _, row := range visibleSettingsRows(st, section, false) {
			ids = append(ids, row.ID)
		}
	}
	if found != 1 {
		t.Fatalf("section %s 数量 = %d，期望 1", id, found)
	}
	return ids
}

func TestRemoteSections_VisibilityAndOrder(t *testing.T) {
	st := &SettingsState{cfg: config.Defaults()}
	check := func(id string, want ...string) {
		t.Helper()
		if got := remoteTestRows(t, st, id); !reflect.DeepEqual(got, want) {
			t.Errorf("%s 行 = %v，期望 %v", id, got, want)
		}
	}
	check("remote.status", "row-remote-enabled", "row-remote-status")
	check("remote.devices", "row-remote-pair", "row-remote-devices")
	check("remote.network", "row-remote-port", "row-remote-hosts")

	st.remote.status.State = RemoteStateError
	check("remote.status", "row-remote-enabled", "row-remote-status", "row-remote-error")
	st.remote.pairingOpen = true
	check("remote.devices", "row-remote-pair", "row-remote-devices")
	st.remote.status.State = RemoteStateRunning
	check("remote.status", "row-remote-enabled", "row-remote-status")
	check("remote.devices", "row-remote-pair", "row-remote-pairing", "row-remote-devices")
	check("remote.network", "row-remote-entry", "row-remote-port", "row-remote-hosts")
	st.remote.pairingOpen = false
	check("remote.devices", "row-remote-pair", "row-remote-devices")
}
