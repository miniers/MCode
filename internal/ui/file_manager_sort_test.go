package mcodeui

import (
	"reflect"
	"testing"
	"time"
)

func TestFileTreeSortEntries(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

	entryNames := func(entries []FileEntry) []string {
		out := make([]string, len(entries))
		for i, e := range entries {
			out[i] = e.Name
		}
		return out
	}

	base := []FileEntry{
		{Name: "z.md", Path: "z.md", Size: 100, ModTime: t2},
		{Name: "b_dir", Path: "b_dir", IsDir: true, Size: 9999, ModTime: t3},
		{Name: "A.txt", Path: "A.txt", Size: 300, ModTime: t1},
		{Name: "a.txt", Path: "a.txt", Size: 300, ModTime: t1},
		{Name: "a_dir", Path: "a_dir", IsDir: true, Size: 10, ModTime: t1},
		{Name: "Makefile", Path: "Makefile", Size: 50, ModTime: time.Time{}},
		{Name: "c.GO", Path: "c.GO", Size: 200, ModTime: t3},
	}

	cases := []struct {
		name string
		cfg  fileTreeSort
		want []string
	}{
		{
			name: "default_name_asc_dirs_first",
			cfg:  fileTreeSort{},
			want: []string{"a_dir", "b_dir", "A.txt", "a.txt", "c.GO", "Makefile", "z.md"},
		},
		{
			name: "name_desc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortName, Desc: true},
			want: []string{"b_dir", "a_dir", "z.md", "Makefile", "c.GO", "a.txt", "A.txt"},
		},
		{
			name: "name_asc_dirs_mixed",
			cfg:  fileTreeSort{Field: fileSortName, DirsMixed: true},
			want: []string{"A.txt", "a.txt", "a_dir", "b_dir", "c.GO", "Makefile", "z.md"},
		},
		{
			name: "name_desc_dirs_mixed",
			cfg:  fileTreeSort{Field: fileSortName, Desc: true, DirsMixed: true},
			want: []string{"z.md", "Makefile", "c.GO", "b_dir", "a_dir", "a.txt", "A.txt"},
		},
		{
			name: "size_asc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortSize},
			want: []string{"a_dir", "b_dir", "Makefile", "z.md", "c.GO", "A.txt", "a.txt"},
		},
		{
			name: "size_desc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortSize, Desc: true},
			want: []string{"a_dir", "b_dir", "A.txt", "a.txt", "c.GO", "z.md", "Makefile"},
		},
		{
			name: "size_desc_dirs_mixed",
			cfg:  fileTreeSort{Field: fileSortSize, Desc: true, DirsMixed: true},
			want: []string{"A.txt", "a.txt", "c.GO", "z.md", "Makefile", "a_dir", "b_dir"},
		},
		{
			name: "mtime_asc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortModTime},
			want: []string{"a_dir", "b_dir", "A.txt", "a.txt", "z.md", "c.GO", "Makefile"},
		},
		{
			name: "mtime_desc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortModTime, Desc: true},
			want: []string{"b_dir", "a_dir", "c.GO", "z.md", "A.txt", "a.txt", "Makefile"},
		},
		{
			name: "mtime_desc_dirs_mixed",
			cfg:  fileTreeSort{Field: fileSortModTime, Desc: true, DirsMixed: true},
			want: []string{"b_dir", "c.GO", "z.md", "A.txt", "a.txt", "a_dir", "Makefile"},
		},
		{
			name: "type_asc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortType},
			want: []string{"a_dir", "b_dir", "Makefile", "c.GO", "z.md", "A.txt", "a.txt"},
		},
		{
			name: "type_desc_dirs_first",
			cfg:  fileTreeSort{Field: fileSortType, Desc: true},
			want: []string{"a_dir", "b_dir", "A.txt", "a.txt", "z.md", "c.GO", "Makefile"},
		},
		{
			name: "type_asc_dirs_mixed",
			cfg:  fileTreeSort{Field: fileSortType, DirsMixed: true},
			want: []string{"a_dir", "b_dir", "Makefile", "c.GO", "z.md", "A.txt", "a.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]FileEntry(nil), base...)
			fileTreeSortEntries(got, tc.cfg)
			if names := entryNames(got); !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, names, tc.want)
			}
		})
	}

	// 相同名称时保持稳定排序。
	dup := []FileEntry{
		{Name: "same.txt", Path: "first"},
		{Name: "same.txt", Path: "second"},
	}
	fileTreeSortEntries(dup, fileTreeSort{})
	if dup[0].Path != "first" || dup[1].Path != "second" {
		t.Fatalf("稳定排序被破坏: %+v", dup)
	}
}

func TestFileTreeSortPreferenceRoundTrip(t *testing.T) {
	st := &fileManagerState{threadID: "t1"}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	tree.sort = fileTreeSort{
		Field:     fileSortSize,
		Desc:      true,
		DirsMixed: true,
	}
	expanded, active, anchor, fileSort, fileSortDesc, fileDirsMixed := fileTreePreferenceLocked(st)
	fileManagerMu.Unlock()

	if fileSort != "size" || !fileSortDesc || !fileDirsMixed {
		t.Fatalf("fileTreePreferenceLocked 返回异常: sort=%q desc=%v mixed=%v", fileSort, fileSortDesc, fileDirsMixed)
	}

	pref := normalizeInspectorPreference(InspectorPreference{
		Open:          true,
		Active:        sidePaneFileID,
		Width:         inspectorDefaultWidth,
		FileExpanded:  expanded,
		FileActive:    active,
		FileAnchor:    anchor,
		FileSort:      fileSort,
		FileSortDesc:  fileSortDesc,
		FileDirsMixed: fileDirsMixed,
	})
	if pref.FileSort != "size" || !pref.FileSortDesc || !pref.FileDirsMixed {
		t.Fatalf("normalizeInspectorPreference 意外改动有效值: %+v", pref)
	}

	st2 := &fileManagerState{threadID: "t1"}
	fileManagerMu.Lock()
	fileTreeSeedPreference(st2, pref)
	restored := ensureFileTreeLocked(st2).sort
	fileManagerMu.Unlock()

	if restored != tree.sort {
		t.Fatalf("round-trip 不一致: got %+v, want %+v", restored, tree.sort)
	}

	unknown := normalizeInspectorPreference(InspectorPreference{FileSort: "bogus"})
	if unknown.FileSort != "" {
		t.Fatalf("未知 FileSort 应归为空，实际为 %q", unknown.FileSort)
	}
}

func TestFileTreeRowShowsSize(t *testing.T) {
	now := time.Now()
	tree := map[string][]FileEntry{
		"": {
			{Name: "sub", Path: "sub", IsDir: true, Size: 4096, ModTime: now.Add(-10 * time.Minute)},
			{Name: "a.txt", Path: "a.txt", Size: 1536, ModTime: now.Add(-5 * time.Minute)},
			{Name: "b.txt", Path: "b.txt", Size: 500, ModTime: now.Add(-2 * time.Hour)},
		},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	s, tt := openFileTreeForTest(t, newTreeListRecorder(fake, tree))

	waitFor(t, tt, func() bool {
		return tt.HasText("sub") && tt.HasText("a.txt") && tt.HasText("b.txt")
	})

	sizeA := formatFileSize(1536)
	sizeB := formatFileSize(500)
	sizeDir := formatFileSize(4096)
	if !tt.HasText(sizeA) || !tt.HasText(sizeB) {
		t.Fatalf("文件行应显示大小 %q 与 %q，当前文本：%q", sizeA, sizeB, tt.Texts())
	}
	if tt.HasText(sizeDir) {
		t.Fatalf("目录行不应显示大小 %q", sizeDir)
	}

	aBefore, ok := tt.Find("a.txt")
	if !ok {
		t.Fatal("找不到 a.txt")
	}
	bBefore, ok := tt.Find("b.txt")
	if !ok {
		t.Fatal("找不到 b.txt")
	}
	if aBefore.Y >= bBefore.Y {
		t.Fatalf("默认按名称排序时 a.txt 应在 b.txt 之前: a.Y=%v b.Y=%v", aBefore.Y, bBefore.Y)
	}

	if err := tt.Click("排序"); err != nil {
		t.Fatalf("点击排序按钮失败: %v", err)
	}
	if err := tt.ChooseMenuItem("修改时间"); err != nil {
		t.Fatalf("选择修改时间失败: %v", err)
	}

	relA := formatRelativeTime(now.Add(-5 * time.Minute))
	relB := formatRelativeTime(now.Add(-2 * time.Hour))
	relDir := formatRelativeTime(now.Add(-10 * time.Minute))
	waitFor(t, tt, func() bool {
		return tt.HasText(relA) && tt.HasText(relB)
	})

	if tt.HasText(sizeA) || tt.HasText(sizeB) {
		t.Fatalf("按修改时间排序时不应继续显示大小，当前文本：%q", tt.Texts())
	}
	if tt.HasText(relDir) {
		t.Fatalf("目录行不应显示相对时间 %q", relDir)
	}

	aAfter, ok := tt.Find("a.txt")
	if !ok {
		t.Fatal("排序后找不到 a.txt")
	}
	bAfter, ok := tt.Find("b.txt")
	if !ok {
		t.Fatal("排序后找不到 b.txt")
	}
	if bAfter.Y >= aAfter.Y {
		t.Fatalf("按修改时间升序时 b.txt (-2h) 应在 a.txt (-5m) 之前: b.Y=%v a.Y=%v", bAfter.Y, aAfter.Y)
	}
	_ = s
}
