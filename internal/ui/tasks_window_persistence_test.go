package mcodeui

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTasksWindowPersistenceValues(t *testing.T) {
	for _, tc := range []struct{ in, want float32 }{{0, 480}, {-1, 480}, {1, 360}, {900, 640}, {500, 500}, {float32(math.NaN()), 480}, {float32(math.Inf(1)), 480}, {float32(math.Inf(-1)), 480}} {
		if got := clampTasksDetailWidth(tc.in); got != tc.want {
			t.Errorf("%v: got %v want %v", tc.in, got, tc.want)
		}
	}
	path := resetLastSelectionMergeState(t)
	if got := loadTasksDetailWidth(); got != 480 {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte(`{"sidebar_width":300,"project_order":["old"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	clearLastSelectionGlobals()
	if got := loadTasksDetailWidth(); got != 480 {
		t.Fatal(got)
	}
	ks := &kanbanState{persistTasksDetailWidth: true}
	rememberTasksDetailWidth(ks, 530)
	waitUIStateWrites()
	if got := loadTasksDetailWidth(); got != 530 {
		t.Fatal(got)
	}
	clearLastSelectionGlobals()
	if got := loadTasksDetailWidth(); got != 530 {
		t.Fatal(got)
	}
	dir := t.TempDir()
	SetUIStateDataDir(dir)
	t.Cleanup(func() { SetUIStateDataDir("") })
	if got := loadTasksDetailWidth(); got != 480 {
		t.Fatal(got)
	}
}

func TestTasksWindowPersistenceMerge(t *testing.T) {
	path := resetLastSelectionMergeState(t)
	ks := &kanbanState{persistTasksDetailWidth: true}
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)
	s.sidebarWidth = 300
	storeSidebarProjectOrder(s, []string{"a", "b"})
	rememberTasksDetailWidth(ks, 510)
	saveLastModelSelection(ModelSelection{Provider: "p", Model: "m"})
	persistUIState(s)
	rememberTasksDetailWidth(ks, 550)
	waitUIStateWrites()
	f := mustReadUIStateFile(t, path)
	if f.TasksDetailWidth != 550 || f.SidebarWidth != 300 || f.LastModel.Model != "m" || len(f.ProjectOrder) != 2 || f.ProjectOrder[0] != "a" {
		t.Fatalf("merged: %+v", f)
	}
	if err := saveUIState(UIState{SidebarWidth: 320}); err != nil {
		t.Fatal(err)
	}
	if got := mustReadUIStateFile(t, path).TasksDetailWidth; got != 550 {
		t.Fatal(got)
	}
}

func TestTasksWindowPersistenceGeneration(t *testing.T) {
	path := resetLastSelectionMergeState(t)
	ks := &kanbanState{persistTasksDetailWidth: true}
	var once sync.Once
	uiStateBeforeWriteHook = func() { once.Do(func() { rememberTasksDetailWidth(ks, 600) }) }
	t.Cleanup(func() { uiStateBeforeWriteHook = nil })
	rememberTasksDetailWidth(ks, 500)
	waitUIStateWrites()
	if got := mustReadUIStateFile(t, path).TasksDetailWidth; got != 600 {
		t.Fatal(got)
	}
	if got := loadTasksDetailWidth(); got != 600 {
		t.Fatal(got)
	}
}

func TestTasksWindowPersistenceFailureAndHeadless(t *testing.T) {
	path := resetLastSelectionMergeState(t)
	rememberTasksDetailWidth(&kanbanState{}, 500)
	waitUIStateWrites()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("headless wrote: %v", err)
	}
	ks := &kanbanState{persistTasksDetailWidth: true}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	rememberTasksDetailWidth(ks, 520)
	waitUIStateWrites()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "broken" {
		t.Fatalf("corrupt overwritten: %q %v", b, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	saveLastModelSelection(ModelSelection{Provider: "p", Model: "m"})
	waitUIStateWrites()
	if got := mustReadUIStateFile(t, path).TasksDetailWidth; got != 520 {
		t.Fatal(got)
	}
	// A non-directory parent makes the atomic writer fail, then the same path is repaired.
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	SetUIStateDataDir(parent)
	t.Cleanup(func() { SetUIStateDataDir("") })
	rememberTasksDetailWidth(ks, 610)
	waitUIStateWrites()
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	saveLastModelSelection(ModelSelection{Provider: "p", Model: "retry"})
	waitUIStateWrites()
	if got := mustReadUIStateFile(t, filepath.Join(parent, uiStateFileName)).TasksDetailWidth; got != 610 {
		t.Fatal(got)
	}
}
