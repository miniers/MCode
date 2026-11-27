package mcodeui

import (
	"path"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

type fileSortField string

const (
	fileSortName    fileSortField = ""      // 默认：名称
	fileSortSize    fileSortField = "size"  // 大小
	fileSortModTime fileSortField = "mtime" // 修改时间
	fileSortType    fileSortField = "type"  // 类型（按扩展名）
)

// fileTreeSort 描述文件树的排序规则，零值即默认设置（名称、升序、目录置顶）。
type fileTreeSort struct {
	Field     fileSortField
	Desc      bool
	DirsMixed bool
}

func (s fileTreeSort) isDefault() bool {
	return s.Field == fileSortName && !s.Desc && !s.DirsMixed
}

func normalizeFileSortField(raw string) fileSortField {
	switch fileSortField(raw) {
	case fileSortName, fileSortSize, fileSortModTime, fileSortType:
		return fileSortField(raw)
	default:
		return fileSortName
	}
}

func fileTreeCurrentSort(st *fileManagerState) fileTreeSort {
	if st == nil {
		return fileTreeSort{}
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return fileTreeSort{}
	}
	return st.tree.sort
}

// fileTreeSortEntries 按指定规则稳定排序文件条目；平局时先按小写名称，再按原名称升序。
func fileTreeSortEntries(entries []FileEntry, cfg fileTreeSort) {
	cfg.Field = normalizeFileSortField(string(cfg.Field))
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if !cfg.DirsMixed && a.IsDir != b.IsDir {
			return a.IsDir
		}
		switch cfg.Field {
		case fileSortSize:
			sa, sb := a.Size, b.Size
			if a.IsDir {
				sa = 0
			}
			if b.IsDir {
				sb = 0
			}
			if sa != sb {
				if cfg.Desc {
					return sa > sb
				}
				return sa < sb
			}
		case fileSortModTime:
			if a.ModTime.IsZero() != b.ModTime.IsZero() {
				return !a.ModTime.IsZero()
			}
			if cmp := a.ModTime.Compare(b.ModTime); cmp != 0 {
				if cfg.Desc {
					return cmp > 0
				}
				return cmp < 0
			}
		case fileSortType:
			ea, eb := fileEntrySortExt(a), fileEntrySortExt(b)
			if ea != eb {
				if cfg.Desc {
					return ea > eb
				}
				return ea < eb
			}
		default:
			if cmp := compareFileEntryNameAsc(a, b); cmp != 0 {
				if cfg.Desc {
					return cmp > 0
				}
				return cmp < 0
			}
			return false
		}
		return compareFileEntryNameAsc(a, b) < 0
	})
}

func fileEntrySortExt(entry FileEntry) string {
	if entry.IsDir {
		return ""
	}
	return strings.ToLower(path.Ext(entry.Name))
}

func compareFileEntryNameAsc(a, b FileEntry) int {
	an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if an != bn {
		if an < bn {
			return -1
		}
		return 1
	}
	if a.Name != b.Name {
		if a.Name < b.Name {
			return -1
		}
		return 1
	}
	return 0
}

// setFileTreeSort 更新工作区文件树排序并对已加载目录重新排序，不重新读盘。
func (s *chatState) setFileTreeSort(next fileTreeSort) {
	next.Field = normalizeFileSortField(string(next.Field))
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	if tree.sort == next {
		fileManagerMu.Unlock()
		return
	}
	tree.sort = next
	for dir, kids := range tree.children {
		if len(kids) <= 1 {
			continue
		}
		entries := make([]FileEntry, 0, len(kids))
		for _, p := range kids {
			entry, ok := tree.entries[p]
			if !ok {
				entry = FileEntry{Name: path.Base(p), Path: p}
			}
			entries = append(entries, entry)
		}
		fileTreeSortEntries(entries, tree.sort)
		sorted := make([]string, len(entries))
		for i, entry := range entries {
			sorted[i] = entry.Path
		}
		tree.children[dir] = sorted
	}
	tree.changedCacheOK = false
	fileTreeBumpOutlineGenLocked(tree)
	fileManagerMu.Unlock()

	inspectorRemember(s)
	s.invalidate()
}

// fileManagerSortMenu 构建文件树顶部的排序菜单。
func fileManagerSortMenu(s *chatState, current fileTreeSort) func(*ui.Menu) {
	return func(m *ui.Menu) {
		fields := []struct {
			field fileSortField
			label string
		}{
			{field: fileSortName, label: "名称"},
			{field: fileSortSize, label: "大小"},
			{field: fileSortModTime, label: "修改时间"},
			{field: fileSortType, label: "类型"},
		}
		for _, item := range fields {
			if m.Item(item.label).Checked(current.Field == item.field).Chosen() {
				next := current
				next.Field = item.field
				s.setFileTreeSort(next)
			}
		}
		m.Separator()
		if m.Item("升序").Checked(!current.Desc).Chosen() {
			next := current
			next.Desc = false
			s.setFileTreeSort(next)
		}
		if m.Item("降序").Checked(current.Desc).Chosen() {
			next := current
			next.Desc = true
			s.setFileTreeSort(next)
		}
		m.Separator()
		if m.Item("目录置顶").Checked(!current.DirsMixed).Chosen() {
			next := current
			next.DirsMixed = !current.DirsMixed
			s.setFileTreeSort(next)
		}
	}
}
