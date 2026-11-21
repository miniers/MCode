package mcodeui

import (
	"context"
	"fmt"
	"time"
)

const (
	storageScanTimeout  = 60 * time.Second
	storageCleanTimeout = 60 * time.Second
)

func ensureStorageScan(st *SettingsState) {
	if st == nil {
		return
	}
	client := archiveClient(st)
	st.mu.Lock()
	ps := storageStateLocked(st)
	if ps.loaded || ps.scanning || ps.attempted {
		st.mu.Unlock()
		return
	}
	if client == nil {
		ps.attempted = true
		st.mu.Unlock()
		return
	}
	st.mu.Unlock()
	startStorageScan(st)
}

func startStorageScan(st *SettingsState) {
	if st == nil {
		return
	}
	client := archiveClient(st)
	if client == nil {
		return
	}
	st.mu.Lock()
	ps := storageStateLocked(st)
	if ps.scanning {
		st.mu.Unlock()
		return
	}
	ps.gen++
	gen := ps.gen
	ps.scanning = true
	ps.attempted = true
	ps.scanErr = ""
	st.mu.Unlock()
	st.invalidate()

	go runStorageScan(st, client, ps, gen)
}

func runStorageScan(st *SettingsState, client Client, ps *storagePageState, gen uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), storageScanTimeout)
	defer cancel()
	report, err := client.StorageScan(ctx)

	st.mu.Lock()
	if st.storagePage != ps || ps.gen != gen {
		st.mu.Unlock()
		return
	}
	ps.scanning = false
	if err != nil {
		ps.scanErr = err.Error()
		st.mu.Unlock()
		st.invalidate()
		return
	}
	ps.loaded = true
	ps.scanErr = ""
	ps.report = report
	ps.selected = make(map[StorageCategory]bool, len(storageCategories))
	for _, def := range storageCategories {
		ps.selected[def.cat] = true
	}
	st.mu.Unlock()
	st.invalidate()
}

func startStorageClean(st *SettingsState, ids []string) {
	if st == nil || len(ids) == 0 {
		return
	}
	client := archiveClient(st)
	if client == nil {
		return
	}
	st.mu.Lock()
	ps := storageStateLocked(st)
	if ps.cleaning || ps.scanning {
		st.mu.Unlock()
		return
	}
	ps.confirmOpen = false
	ps.cleaning = true
	ps.cleanErr = ""
	gen := ps.gen
	st.mu.Unlock()
	st.invalidate()

	idsCopy := append([]string(nil), ids...)
	go runStorageClean(st, client, ps, gen, idsCopy)
}

func runStorageClean(st *SettingsState, client Client, ps *storagePageState, gen uint64, ids []string) {
	ctx, cancel := context.WithTimeout(context.Background(), storageCleanTimeout)
	defer cancel()
	freed, err := client.StorageClean(ctx, ids)

	st.mu.Lock()
	if st.storagePage != ps || ps.gen != gen {
		st.mu.Unlock()
		return
	}
	ps.cleaning = false
	if err != nil {
		ps.cleanErr = err.Error()
	} else {
		ps.cleanErr = ""
	}
	st.mu.Unlock()

	if freed > 0 || err == nil {
		st.setToast(fmt.Sprintf("已移到废纸篓，释放 %s", formatFileSize(freed)))
	} else {
		st.invalidate()
	}
	startStorageScan(st)
}
